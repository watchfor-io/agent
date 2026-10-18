//go:build linux

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
//	upgrade                 latest version the server reported, else watchfor.io
//	upgrade -version 0.3.0  that release exactly
//	upgrade -check          report only; exit 10 when an update is available
//	upgrade -if-available   only if the running agent recorded a hint (timer)
func runUpgrade(args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	configPath := fs.String("config", envOr("WATCHFOR_AGENT_CONFIG", defaultConfig), "path to agent.yml (for the state directory)")
	version := fs.String("version", "", "install this release instead of the newest")
	check := fs.Bool("check", false, "report whether an update is available; exit 10 if so")
	ifAvailable := fs.Bool("if-available", false, "act only on the running agent's hint; never ask watchfor.io")
	noRestart := fs.Bool("no-restart", false, "do not restart the systemd service afterwards")
	allowDowngrade := fs.Bool("allow-downgrade", false, "permit a lower version than the running one")
	syncUnits := fs.Bool("sync-units", false, "bring the service unit and cron file up to this release where they are unmodified, then exit")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}

	if *syncUnits {
		return runSyncUnits()
	}

	// Read settings without requiring the token (config.Get validates the
	// file the way the agent does, minus the token source).
	stateDir := config.DefaultSpoolDir
	if v, err := config.Get(*configPath, "spool.dir"); err == nil && v != "" {
		stateDir = v
	}
	// The timer runs -if-available; an operator who switched auto-update
	// off in agent.yml (but left the timer) still gets no surprise.
	if *ifAvailable {
		if v, err := config.Get(*configPath, "updates.auto"); err == nil && v == "false" {
			fmt.Printf("auto-update is off in %s; nothing to do (sudo watchfor-agent auto-update on to enable)\n", *configPath)
			return exitOK
		}
	}
	opts := update.Options{
		Current:        Version,
		Target:         *version,
		StateDir:       stateDir,
		IfAvailable:    *ifAvailable,
		AllowDowngrade: *allowDowngrade,
		Restart:        !*noRestart,
		SyncUnits:      !*noRestart,
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

// runSyncUnits is `upgrade -sync-units`, which `upgrade` runs with the
// binary it has just installed: the new release's unit and cron file
// replace an earlier release's untouched ones; an edited file is named and
// left alone (`health` keeps pointing at it).
func runSyncUnits() int {
	ctx, cancel := context.WithTimeout(context.Background(), update.CommandTimeout)
	defer cancel()
	changed, kept, err := update.SyncUnits(ctx, "", "", execCmdCombined)
	for _, c := range changed {
		fmt.Printf("updated %s to this release's\n", c)
	}
	for _, k := range kept {
		fmt.Printf("kept %s: it was changed locally (put changes in a drop-in: sudo systemctl edit watchfor-agent)\n", k)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sync-units:", err)
		return exitError
	}
	return exitOK
}
