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
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/watchfor-io/agent/internal/agent"
	"github.com/watchfor-io/agent/internal/config"
	"github.com/watchfor-io/agent/internal/hostinfo"
	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/modules"
	"github.com/watchfor-io/agent/internal/push"
	"github.com/watchfor-io/agent/internal/spool"

	_ "github.com/watchfor-io/agent/internal/modules/disk"
	_ "github.com/watchfor-io/agent/internal/modules/network"
	_ "github.com/watchfor-io/agent/internal/modules/processes"
	_ "github.com/watchfor-io/agent/internal/modules/system"
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
		usage(os.Stderr)
		return exitUsage
	}
	cmd, args := args[0], args[1:]
	switch cmd {
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

	cfg, err := config.Load(*configPath)
	if err != nil {
		if config.IsNotExist(err) && cmd == "check" {
			cfg, err = config.Parse(strings.NewReader(""))
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			return exitError
		}
	}
	log := newLogger(cfg.Log.Level)

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
		Facts:    func() metric.Facts { return hostinfo.FactsFor(cfg.Server.URL) },
		Version:  Version,
		Interval: cfg.Interval,
		StateDir: cfg.Spool.Dir,
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

	if !cfg.HasToken() {
		log.Error("no token: set server.token_file (or server.token: env:NAME) in " + *configPath)
		return exitError
	}
	if cfg.Server.URL == "" {
		log.Error("server.url is not set")
		return exitError
	}
	client, err := push.New(cfg.Server.URL, cfg.Server.Token, cfg.Server.CAFile, cfg.Server.Timeout, Version)
	if err != nil {
		log.Error("client", "error", err)
		return exitError
	}
	opts.Sink = client

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

	if cfg.Spool.MaxMB > 0 {
		sp, err := spool.Open(cfg.Spool.Dir, int64(cfg.Spool.MaxMB)<<20)
		if err != nil {
			log.Warn("spool disabled", "dir", cfg.Spool.Dir, "error", err)
		} else {
			opts.Spool = sp
			if n, _ := sp.Stats(); n > 0 {
				log.Info("spool has batches to replay", "count", n)
			}
		}
	}

	a := agent.New(opts)
	log.Info("starting", "version", Version, "host", opts.Host.Name, "id", opts.Host.ID,
		"interval", cfg.Interval.String(), "modules", names(mods), "server", cfg.Server.URL)

	if cmd == "once" {
		err = a.Once(ctx, onceGap)
	} else {
		err = a.Run(ctx)
	}
	switch {
	case errors.Is(err, agent.ErrTokenRejected):
		return exitAuth
	case err != nil && !errors.Is(err, context.Canceled):
		log.Error("stopped", "error", err)
		return exitError
	}
	return exitOK
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
		"  watchfor-agent <command> [options]\n\n",
		head("Collect")+"\n",
		row("run", "Collect on the configured interval and push — what the systemd service runs"),
		row("once", "Collect once, push once, exit — for cron"),
		row("check", "Collect once and print the batch that would be sent — no network, no token"),
		"\n"+head("Maintain")+" "+note("(root)")+"\n",
		row("upgrade", "Install a newer signed release and restart the service"),
		row("", note("-check reports only · -version X.Y.Z picks a release · -if-available acts on the server's hint")),
		row("auto-update", "on | off | status — daily signed updates through a systemd timer"),
		row("uninstall", "Remove the service, timer, binary, state, config with the token, and the user"),
		row("", note("-yes skips the question · -keep-config leaves /etc/watchfor-agent in place")),
		"\n"+head("Inspect")+"\n",
		row("config", "get | set | keys | path — read or change one setting in agent.yml"),
		row("verify", "Check a release's checksums.txt signature (built-in key) and file sha256s"),
		row("version", "Print the version"),
		"\n"+head("Options")+"\n",
		fmt.Sprintf("  %-15s agent.yml to use %s\n", "-config <path>", note("(default "+defaultConfig+"; env WATCHFOR_AGENT_CONFIG)")),
		"\n"+head("Exit codes")+"\n",
		note("  0 ok · 1 error · 2 usage · 3 token rejected by the server · 10 update available (upgrade -check)")+"\n",
	)
}
