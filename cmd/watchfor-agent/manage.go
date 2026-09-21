//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/watchfor-io/agent/internal/config"
	"github.com/watchfor-io/agent/internal/detect"
	"github.com/watchfor-io/agent/internal/hostinfo"
	"github.com/watchfor-io/agent/internal/update"
)

// runAutoUpdate is `watchfor-agent auto-update on|off|status`: the
// setting is written to agent.yml (updates.auto) and the systemd timer
// that performs the updates is enabled or removed to match. Root.
func runAutoUpdate(args []string) int {
	fs := flag.NewFlagSet("auto-update", flag.ContinueOnError)
	configPath := fs.String("config", envOr("WATCHFOR_AGENT_CONFIG", defaultConfig), "path to agent.yml")
	words, err := parseAnywhere(fs, args)
	if err != nil {
		return exitUsage
	}
	action := ""
	if len(words) > 0 {
		action = words[0]
	}
	ctx := context.Background()
	switch action {
	case "on", "off":
		want := action == "on"
		if os.Geteuid() != 0 {
			fmt.Fprintf(os.Stderr, "auto-update %s changes the systemd timer: run it as root — sudo watchfor-agent auto-update %s\n", action, action)
			return exitError
		}
		if _, err := config.Set(*configPath, "updates.auto", fmt.Sprint(want)); err != nil {
			fmt.Fprintln(os.Stderr, "auto-update:", err)
			return exitError
		}
		var err error
		if want {
			err = update.EnableTimer(ctx, "", execCmdCombined)
		} else {
			err = update.DisableTimer(ctx, "", execCmdCombined)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "auto-update:", err)
			return exitError
		}
		if want {
			fmt.Printf("auto-update on: a newer signed release the server reports is installed daily (%s); updates.auto: true in %s\n", update.UpdateTimer, *configPath)
		} else {
			fmt.Printf("auto-update off: timer removed; updates.auto: false in %s — upgrade by hand with: sudo watchfor-agent upgrade\n", *configPath)
		}
		return exitOK
	case "status", "":
		setting := "unset"
		if v, err := config.Get(*configPath, "updates.auto"); err == nil {
			setting = v
		}
		timer := "off"
		if update.TimerEnabled(ctx, execCmdCombined) {
			timer = "on"
		}
		fmt.Printf("auto-update: timer %s (%s) · updates.auto in %s: %s\n", timer, update.UpdateTimer, *configPath, setting)
		if (timer == "on") != (setting == "true") && setting != "unset" {
			fmt.Println("the timer and the config disagree — run: sudo watchfor-agent auto-update on|off")
		}
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "usage: watchfor-agent auto-update on|off|status [-config %s]\n", defaultConfig)
		return exitUsage
	}
}

// runConfig is `watchfor-agent config get|set|keys|path`: read or write
// one setting in agent.yml. Set validates the whole file the way the agent
// does at start, so a bad value never lands on disk. Root for set.
func runConfig(args []string) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	configPath := fs.String("config", envOr("WATCHFOR_AGENT_CONFIG", defaultConfig), "path to agent.yml")
	rest, err := parseAnywhere(fs, args)
	if err != nil {
		return exitUsage
	}
	usage := func() int {
		fmt.Fprintf(os.Stderr, `usage: watchfor-agent config <command> [-config %s]

  get <key>          print the effective value (defaults applied)
  set <key> <value>  write the value; the file is validated first
  keys               list the settable keys
  path               print the config file path
  detect             what this machine has: cpu, memory, hardware, mounts, disks, interfaces
  init               write a commented agent.yml with the detected lists in it
                     [-server URL] [-token-file PATH] [-interval 60s] [-spool-dir DIR] [-out PATH] [-force]

Restart the service after set: sudo systemctl restart watchfor-agent
`, defaultConfig)
		return exitUsage
	}
	if len(rest) == 0 {
		return usage()
	}
	switch rest[0] {
	case "detect":
		detect.Collect(factsOptions(*configPath)).Print(os.Stdout)
		return exitOK
	case "init":
		ifs := flag.NewFlagSet("config init", flag.ContinueOnError)
		server := ifs.String("server", "https://ingest.watchfor.io", "ingest URL")
		tokenFile := ifs.String("token-file", "/etc/watchfor-agent/token", "where the host token is")
		interval := ifs.String("interval", "1m", "push interval to start from")
		spoolDir := ifs.String("spool-dir", config.DefaultSpoolDir, "where batches wait while the server is unreachable")
		out := ifs.String("out", *configPath, "file to write")
		force := ifs.Bool("force", false, "overwrite an existing file")
		if err := ifs.Parse(rest[1:]); err != nil {
			return exitUsage
		}
		if _, err := os.Stat(*out); err == nil && !*force {
			fmt.Fprintf(os.Stderr, "config init: %s exists; pass -force to overwrite it\n", *out)
			return exitError
		}
		sum := detect.Collect(hostinfo.Options{Target: *server, CloudMetadata: true})
		o := detect.DefaultOptions()
		o.Server, o.TokenFile, o.Interval, o.SpoolDir, o.Path = *server, *tokenFile, *interval, *spoolDir, *out
		text := sum.YAML(o)
		if _, err := config.WriteConfig(*out, text); err != nil { // validated, atomic, mode set regardless of umask
			fmt.Fprintln(os.Stderr, "config init:", err)
			return exitError
		}
		fmt.Printf("wrote %s — %d mounts, %d disks, %d interfaces listed inside; edit, then: sudo systemctl restart watchfor-agent\n",
			*out, len(sum.Mounts), len(sum.Devices), len(sum.Interfaces))
		return exitOK
	case "keys":
		fmt.Println(strings.Join(config.Keys(), "\n"))
		return exitOK
	case "path":
		fmt.Println(*configPath)
		return exitOK
	case "get":
		if len(rest) != 2 {
			return usage()
		}
		v, err := config.Get(*configPath, rest[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			return exitError
		}
		fmt.Println(v)
		return exitOK
	case "set":
		if len(rest) != 3 {
			return usage()
		}
		if _, err := config.Set(*configPath, rest[1], rest[2]); err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			return exitError
		}
		fmt.Printf("%s = %s written to %s", rest[1], rest[2], *configPath)
		if rest[1] != "updates.auto" {
			fmt.Print(" — a running agent picks it up within half a minute; an older one needs: sudo systemctl restart watchfor-agent")
		}
		fmt.Println()
		return exitOK
	}
	return usage()
}

// factsOptions reads what agent.yml says about collecting facts — the
// server to route towards and whether the cloud may be asked — with the
// defaults when there is no file yet.
func factsOptions(path string) hostinfo.Options {
	o := hostinfo.Options{CloudMetadata: true}
	if cfg, err := config.ParseFile(path); err == nil {
		o.Target, o.CloudMetadata = cfg.Server.URL, cfg.Facts.CloudMetadataEnabled()
	}
	return o
}

// parseAnywhere reads flags wherever they stand: `config get interval
// -config x` and `config -config x get interval` mean the same thing.
// Go's flag package stops at the first word that is not a flag; this
// carries on past it.
func parseAnywhere(fs *flag.FlagSet, args []string) (words []string, err error) {
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return words, nil
		}
		words = append(words, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
