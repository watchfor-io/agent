//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/watchfor-io/agent/internal/config"
	"github.com/watchfor-io/agent/internal/health"
)

// runHealth is `watchfor-agent health`: the screen's Health check for a
// terminal without the screen — a quick look over SSH, a cron job, an
// Ansible task. Exit 1 when something is wrong, so scripts can branch.
func runHealth(args []string) int {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	configPath := fs.String("config", envOr("WATCHFOR_AGENT_CONFIG", defaultConfig), "path to agent.yml")
	fix := fs.Bool("fix", false, "apply every repair that is safe, then check again")
	deep := fs.Bool("deep", false, "also download the signed release and compare the binary on disk with it")
	quiet := fs.Bool("q", false, "print only what is not right")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg, cfgErr := config.ParseFile(*configPath)
	o := healthProbe
	o.ConfigPath, o.Config, o.ConfigErr, o.Version = *configPath, cfg, cfgErr, Version
	o.Root, o.Exec, o.Deep = os.Geteuid() == 0, health.Exec(execCmdCombined), *deep
	checks := health.Run(ctx, o)
	if *fix {
		repaired := 0
		for _, c := range checks {
			if c.Fix == nil {
				continue
			}
			if err := c.Fix(ctx); err != nil {
				fmt.Printf("%s %s: could not %s: %v\n", stBarBad.Render("✗"), c.Name, c.FixMsg, err)
				continue
			}
			fmt.Printf("%s %s: %s\n", stOn.Render("✓"), c.Name, c.FixMsg)
			repaired++
		}
		if repaired > 0 {
			fmt.Println()
		}
		checks = health.Run(ctx, o)
	}
	return printChecks(checks, *quiet, !*fix)
}

// printChecks writes one line per check, the way the screen shows them,
// and says which could be repaired. The exit status is 1 when anything
// failed; a warning alone is not a failure.
func printChecks(checks []health.Check, quiet, offerFix bool) int {
	mark := map[health.State]string{
		health.OK:      stOn.Render("●"),
		health.Warn:    stWarn.Render("●"),
		health.Fail:    stBarBad.Render("●"),
		health.Skipped: stDim.Render("○"),
	}
	nameW := 0
	for _, c := range checks {
		nameW = max(nameW, len(c.Name))
	}
	failed, fixable := 0, 0
	for _, c := range checks {
		if c.State == health.Fail {
			failed++
		}
		if c.Fix != nil {
			fixable++
		}
		if quiet && (c.State == health.OK || c.State == health.Skipped) {
			continue
		}
		fmt.Printf("%s %-*s   %s\n", mark[c.State], nameW, c.Name, c.Detail)
		if c.Fix != nil && offerFix {
			fmt.Printf("  %-*s   %s\n", nameW, "", stDim.Render("fix: "+c.FixMsg))
		}
	}
	switch {
	case failed == 0 && fixable == 0:
		if !quiet {
			fmt.Println(stOn.Render("everything checks out"))
		}
	case fixable > 0 && offerFix:
		fmt.Println(stDim.Render(fmt.Sprintf("%d can be repaired: sudo watchfor-agent health -fix", fixable)))
	}
	if failed > 0 {
		return exitError
	}
	return exitOK
}
