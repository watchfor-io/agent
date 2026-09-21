//go:build linux

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/watchfor-io/agent/internal/uninstall"
)

// runUninstall is `watchfor-agent uninstall`: stop and remove the service
// and the update timer, the cron entry, the state, the config with its
// token, the service user and this binary. Shows what it found and asks
// first on a terminal; -yes for scripts. Root.
func runUninstall(args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	configPath := fs.String("config", envOr("WATCHFOR_AGENT_CONFIG", defaultConfig), "path to agent.yml")
	keepConfig := fs.Bool("keep-config", false, "leave agent.yml and the token file in place")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "uninstall: needs root — sudo watchfor-agent uninstall")
		return exitError
	}
	exe, err := os.Executable()
	if err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
	}
	opts := uninstall.Options{
		ConfigPath: *configPath,
		ExePath:    exe,
		KeepConfig: *keepConfig,
		Out:        os.Stdout,
		Exec:       execCmdCombined,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	plan := uninstall.Plan(ctx, opts)
	if len(plan) == 0 {
		fmt.Println("nothing of watchfor-agent found on this machine")
		return exitOK
	}
	fmt.Println("This removes watchfor-agent from this machine:")
	for _, line := range plan {
		fmt.Println("  " + line)
	}
	if !*yes {
		if !isTerminal(os.Stdin) {
			fmt.Fprintln(os.Stderr, "uninstall: no terminal to confirm on — pass -yes")
			return exitUsage
		}
		fmt.Print("Remove all of the above? [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
		default:
			fmt.Println("nothing removed")
			return exitOK
		}
	}
	if err := uninstall.Run(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, "uninstall:", err)
		return exitError
	}
	fmt.Println("watchfor-agent removed from this machine. Remove the host in WatchFor as well (Hosts → the host → Remove): that revokes its token and drops its history.")
	return exitOK
}
