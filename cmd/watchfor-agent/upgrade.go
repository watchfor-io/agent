package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/watchfor-io/agent/internal/config"
	"github.com/watchfor-io/agent/internal/update"
)

// runUpgrade is `watchfor-agent upgrade`: replace this binary with a
// release whose checksums file is signed by the WatchFor key, then restart
// the service. Needs write access to the binary, so root in practice.
//
//	upgrade                 latest version the server reported, else GitHub
//	upgrade -version 0.3.0  that release exactly
//	upgrade -check          report only; exit 10 when an update is available
//	upgrade -if-available   only if the running agent recorded a hint (timer)
func runUpgrade(args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	configPath := fs.String("config", envOr("WATCHFOR_AGENT_CONFIG", defaultConfig), "path to agent.yml (for the state directory)")
	version := fs.String("version", "", "install this release instead of the newest")
	check := fs.Bool("check", false, "report whether an update is available; exit 10 if so")
	ifAvailable := fs.Bool("if-available", false, "act only on the running agent's hint; never ask GitHub")
	noRestart := fs.Bool("no-restart", false, "do not restart the systemd service afterwards")
	allowDowngrade := fs.Bool("allow-downgrade", false, "permit a lower version than the running one")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	stateDir := config.DefaultSpoolDir
	if cfg, err := config.Load(*configPath); err == nil && cfg.Spool.Dir != "" {
		stateDir = cfg.Spool.Dir
	}
	opts := update.Options{
		Current:        Version,
		Target:         *version,
		StateDir:       stateDir,
		IfAvailable:    *ifAvailable,
		AllowDowngrade: *allowDowngrade,
		Restart:        !*noRestart,
		Out:            os.Stdout,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *check {
		st, err := update.Check(ctx, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, "upgrade:", err)
			return exitError
		}
		switch {
		case st.Available:
			fmt.Printf("watchfor-agent %s · %s available (reported by %s) — run: sudo watchfor-agent upgrade\n", st.Current, st.Latest, st.Origin)
			return update.ExitUpdateAvailable
		case st.Latest == "":
			fmt.Printf("watchfor-agent %s · no update reported by the server\n", st.Current)
		default:
			fmt.Printf("watchfor-agent %s · up to date (latest %s)\n", st.Current, st.Latest)
		}
		return exitOK
	}

	if _, err := update.Run(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, "upgrade:", err)
		return exitError
	}
	return exitOK
}
