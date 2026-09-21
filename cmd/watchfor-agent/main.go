//go:build linux

// Command watchfor-agent collects host metrics described by agent.yml and
// pushes them to WatchFor.
//
//	watchfor-agent run      daemon; systemd runs this
//	watchfor-agent once     one collection, one push, exit (cron)
//	watchfor-agent check    print what would be sent, no network
//	watchfor-agent upgrade  install a newer signed release, restart the service
//	watchfor-agent auto-update on|off|status
//	watchfor-agent uninstall  remove everything the installer put on the machine
//	watchfor-agent verify     check a downloaded release against the built-in key
//	watchfor-agent config get|set|keys|path
//	watchfor-agent version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/watchfor-io/agent/internal/agent"
	"github.com/watchfor-io/agent/internal/config"
	"github.com/watchfor-io/agent/internal/hostinfo"
	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/modules"
	"github.com/watchfor-io/agent/internal/push"
	"github.com/watchfor-io/agent/internal/spool"
)

// Version is stamped by the release build via -ldflags.
var Version = "dev"

const (
	defaultConfig = "/etc/watchfor-agent/agent.yml"
	onceGap       = time.Second

	exitOK    = 0
	exitError = 1
	exitUsage = 2
	exitAuth  = 3
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		// On a terminal the agent opens its overview; in a script it
		// explains itself.
		if isTerminal(os.Stdin) {
			return runHome(nil)
		}
		usage(os.Stderr)
		return exitUsage
	}
	cmd, args := args[0], args[1:]
	if strings.HasPrefix(cmd, "-") && cmd != "-version" && cmd != "--version" && cmd != "-h" && cmd != "--help" && cmd != "-help" {
		// options first: `-config x.yml check` is `check -config x.yml`,
		// and options alone (`watchfor-agent -config x.yml`) are the screen
		lead := flag.NewFlagSet("watchfor-agent", flag.ContinueOnError)
		lead.SetOutput(io.Discard)
		cfg := lead.String("config", "", "")
		if err := lead.Parse(append([]string{cmd}, args...)); err != nil || lead.NArg() == 0 {
			return runHome(append([]string{cmd}, args...))
		}
		rest := lead.Args()[1:]
		if *cfg != "" {
			rest = append([]string{"-config", *cfg}, rest...)
		}
		return run(append([]string{lead.Arg(0)}, rest...))
	}
	switch cmd {
	case "home", "overview", "dashboard":
		return runHome(args)
	case "help", "-h", "--help", "-help":
		usage(os.Stdout)
		return exitOK
	case "version", "-version", "--version":
		fmt.Println("watchfor-agent " + Version)
		return exitOK
	case "upgrade":
		return runUpgrade(args)
	case "auto-update":
		return runAutoUpdate(args)
	case "config":
		return runConfig(args)
	case "uninstall":
		return runUninstall(args)
	case "verify":
		return runVerify(args)
	case "health":
		return runHealth(args)
	case "configure":
		return runConfigure(args)
	case "run", "once", "check":
	default:
		fmt.Fprintf(os.Stderr, "watchfor-agent: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		return exitUsage
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	configPath := fs.String("config", envOr("WATCHFOR_AGENT_CONFIG", defaultConfig), "path to agent.yml")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "watchfor-agent %s: unexpected argument %q\n", cmd, fs.Arg(0))
		return exitUsage
	}

	return runCollector(cmd, *configPath)
}

// runCollector is `run`, `once` and `check` once the flags are read: the
// config, the modules, then either one batch on stdout, one push, or the
// daemon loop with agent.yml under watch.
func runCollector(cmd, configPath string) int {
	cfg, err := loadRunConfig(cmd, configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return exitError
	}
	log := newLogger(cfg.Log.Level)
	facts := hostinfo.Options{Target: cfg.Server.URL, CloudMetadata: cfg.Facts.CloudMetadataEnabled() && cmd != "check", Log: log} // check promises no network

	mods, err := buildModules(cfg)
	if err != nil {
		log.Error("modules", "error", err)
		return exitError
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	opts := agent.Options{
		Modules:  mods,
		Host:     hostinfo.Host(cfg.Host.Name, cfg.Host.Tags),
		Facts:    func() metric.Facts { return hostinfo.Collect(facts) },
		Version:  Version,
		Interval: cfg.Interval,
		Log:      log,
	}

	if cmd == "check" {
		opts.Sink = printSink{}
		a := agent.New(opts)
		if err := a.Once(ctx, onceGap); err != nil {
			log.Error("check failed", "error", err)
			return exitError
		}
		fmt.Fprintf(os.Stderr, "host %s (%s) · modules: %s · nothing was sent\n",
			opts.Host.Name, opts.Host.ID, strings.Join(names(mods), ", "))
		return exitOK
	}

	client, code := newSink(cfg, configPath, log)
	if code != exitOK {
		return code
	}
	opts.Sink = client
	opts.StateDir = cfg.Spool.Dir // only a sending agent records hints and rejections
	// A token the server kept rejecting (host removed in WatchFor, or token
	// rotated) is not tried again: say why and stop, until a different
	// token is configured. A rejection that has not held long enough yet
	// is retried normally.
	opts.TokenFingerprint = client.TokenFingerprint()
	if rec, ok := agent.ReadRejection(cfg.Spool.Dir, opts.TokenFingerprint); ok && rec.Sticky() {
		log.Error("token rejected by the server; not retrying",
			"rejections", rec.Count, "since", rec.First.Format(time.RFC3339), "last", rec.Last.Format(time.RFC3339),
			"why", "the host was removed in WatchFor or its token was rotated",
			"fix", "put the new token in "+cfg.Server.TokenFile+" and restart, or stop the agent: sudo systemctl disable --now watchfor-agent")
		return exitAuth
	}
	opts.Spool = openSpool(cfg, log)

	a := agent.New(opts)
	log.Info("starting", "version", Version, "host", opts.Host.Name, "id", opts.Host.ID,
		"interval", cfg.Interval.String(), "modules", names(mods), "server", cfg.Server.URL)

	if cmd == "once" {
		err = a.Once(ctx, onceGap)
	} else {
		var code int
		if code, err = runDaemon(ctx, a, configPath, log); code != 0 {
			return code
		}
	}
	switch {
	case errors.Is(err, agent.ErrTokenRejected):
		return exitAuth
	case err != nil && !errors.Is(err, context.Canceled):
		log.Error("stopped", "error", err)
		return exitError
	}
	if cmd == "run" {
		log.Info("stopped") // a clean stop reads differently from a crash in the journal
	}
	return exitOK
}

// loadRunConfig reads agent.yml. `check` may run without one, on the
// defaults, since it sends nothing — but only when nobody named a file:
// a path that was given and is not there is a mistake, not a request for
// the defaults.
func loadRunConfig(cmd, path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil && config.IsNotExist(err) && cmd == "check" && path == defaultConfig && os.Getenv("WATCHFOR_AGENT_CONFIG") == "" {
		fmt.Fprintf(os.Stderr, "no %s yet; showing what the defaults would send\n", path)
		return config.Parse(strings.NewReader(""))
	}
	return cfg, err
}

// newSink is the HTTPS client for the server named in agent.yml, or the
// exit code that explains why there cannot be one.
func newSink(cfg *config.Config, configPath string, log *slog.Logger) (*push.Client, int) {
	if !cfg.HasToken() {
		log.Error("no token: set server.token_file (or server.token: env:NAME) in " + configPath)
		return nil, exitError
	}
	if cfg.Server.URL == "" {
		log.Error("server.url is not set")
		return nil, exitError
	}
	client, err := push.New(cfg.Server.URL, cfg.Server.Token, cfg.Server.CAFile, cfg.Server.Timeout, Version)
	if err != nil {
		log.Error("client", "error", err)
		return nil, exitError
	}
	return client, exitOK
}

// openSpool is where batches wait while the server is unreachable; a
// spool that cannot be opened is a warning, not a reason not to run.
func openSpool(cfg *config.Config, log *slog.Logger) *spool.Spool {
	if cfg.Spool.Max() <= 0 {
		return nil
	}
	sp, err := spool.Open(cfg.Spool.Dir, int64(cfg.Spool.Max())<<20)
	if err != nil {
		log.Warn("spool disabled", "dir", cfg.Spool.Dir, "error", err)
		return nil
	}
	if n, _ := sp.Stats(); n > 0 {
		log.Info("spool has batches to replay", "count", n)
	}
	return sp
}

// runDaemon is the collector loop with agent.yml under watch: a valid
// change ends the run with ExitReload and systemd starts it again on the
// new file, so `configure`, an editor or Ansible take effect within
// seconds. code is non-zero only for that exit.
func runDaemon(ctx context.Context, a *agent.Agent, configPath string, log *slog.Logger) (code int, err error) {
	runCtx, cancelRun := context.WithCancel(ctx)
	watch := make(chan error, 1)
	go func() {
		werr := config.Watch(runCtx, configPath, true, func(werr error) {
			log.Warn("agent.yml changed but the agent would not accept it; keeping the running config", "error", werr)
		})
		watch <- werr
		if werr != nil {
			cancelRun() // a valid change: stop the run, we exit to be started again
		}
	}()
	err = a.Run(runCtx)
	cancelRun()
	if werr := <-watch; errors.Is(werr, config.ErrReload) && (err == nil || errors.Is(err, context.Canceled)) {
		log.Info("agent.yml changed; restarting on the new config")
		return config.ExitReload, nil
	}
	return 0, err
}

func buildModules(cfg *config.Config) ([]modules.Module, error) {
	keys := make([]string, 0, len(cfg.Modules))
	for k := range cfg.Modules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	mods := make([]modules.Module, 0, len(keys))
	for _, k := range keys {
		node := cfg.Modules[k]
		m, err := modules.Build(k, &node)
		if err != nil {
			return nil, err
		}
		mods = append(mods, m)
	}
	return mods, nil
}

func names(mods []modules.Module) []string {
	out := make([]string, len(mods))
	for i, m := range mods {
		out[i] = m.Name()
	}
	return out
}

// printSink is the `check` destination: the exact batch, decoded, on stdout.
type printSink struct{}

func (printSink) Send(_ context.Context, body []byte) (push.Ack, error) {
	p, err := push.Decode(body)
	if err != nil {
		return push.Ack{}, err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return push.Ack{Accepted: len(p.Samples)}, enc.Encode(p)
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// usage prints the command reference: to stdout for `help`, to stderr
// when the command line made no sense. Colour only on a terminal that
// wants it (NO_COLOR and TERM=dumb switch it off).
func usage(w *os.File) {
	bold, dim, cmd, off := "", "", "", ""
	if st, err := w.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 &&
		os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" {
		bold, dim, cmd, off = "\033[1m", "\033[2m", "\033[36m", "\033[0m"
	}
	head := func(s string) string { return bold + s + off }
	row := func(name, text string) string { return fmt.Sprintf("  %s%-13s%s %s\n", cmd, name, off, text) }
	note := func(s string) string { return dim + s + off }
	fmt.Fprint(w,
		head("watchfor-agent "+Version)+" — the WatchFor host agent\n",
		note("  Collects CPU, memory, disk, network and process metrics from this server and\n  pushes them to WatchFor over HTTPS. Docs: https://watchfor.io/docs/hosts")+"\n\n",
		head("Usage")+"\n",
		"  watchfor-agent <command> [options]\n",
		"  watchfor-agent                       "+note("no command: the screen — live readings, every setting, the service, updates")+"\n\n",
		head("Collect")+"\n",
		row("run", "Collect on the configured interval and push — what the systemd service runs"),
		row("once", "Collect once, push once, exit — for cron"),
		row("check", "Collect once and print the batch that would be sent — no network, no token"),
		"\n"+head("Maintain")+" "+note("(root)")+"\n",
		row("upgrade", "Install a newer signed release and restart the service"),
		row("", note("-check reports only · -version X.Y.Z picks a release · -if-available acts on the server's hint")),
		row("", note("-no-restart leaves the service alone · -allow-downgrade permits an older release")),
		row("auto-update", "on | off | status — daily signed updates through a systemd timer"),
		row("uninstall", "Remove the service, timer, binary, state, config with the token, and the user"),
		row("", note("-yes skips the question · -keep-config leaves /etc/watchfor-agent in place")),
		"\n"+head("Inspect")+"\n",
		row("health", "Check the installation: files, permissions, service, timer, the way out to WatchFor — exit 1 when something is wrong"),
		row("", note("-fix repairs what it safely can · -deep compares the binary with the signed release · -q shows only problems")),
		row("configure", "The screen, opened on the settings — the same as running with no command"),
		row("config", "get | set | keys | path | init | detect — read or change one setting, write a fresh agent.yml, or print what the collectors see"),
		row("", note("-config may stand before or after the word")),
		row("verify", "Check a release's checksums.txt signature (built-in key), then the sha256 of every file named after the flags"),
		row("", note("-checksums FILE · -sig FILE (default FILE.minisig) · -q prints nothing on success")),
		row("version", "Print the version"),
		"\n"+head("Options")+"\n",
		fmt.Sprintf("  %-15s agent.yml to use %s\n", "-config <path>", note("(default "+defaultConfig+"; env WATCHFOR_AGENT_CONFIG)")),
		"\n"+head("Exit codes")+"\n",
		note("  0 ok · 1 error · 2 usage · 3 token rejected by the server · 10 update available (upgrade -check)")+"\n",
		note("  75 agent.yml changed and validated — the daemon asks to be started again, the unit does it at once")+"\n",
	)
}

// isTerminal is true for a real terminal — a char device like /dev/null
// does not count, so `watchfor-agent < /dev/null` gets the usage text.
func isTerminal(f *os.File) bool { return term.IsTerminal(f.Fd()) }
