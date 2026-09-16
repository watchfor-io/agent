package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/watchfor-io/agent/internal/config"
	"github.com/watchfor-io/agent/internal/update"
)

func execCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// runAutoUpdate is `watchfor-agent auto-update on|off|status`: the
// setting is written to agent.yml (updates.auto) and the systemd timer
// that performs the updates is enabled or removed to match. Root.
func runAutoUpdate(args []string) int {
	fs := flag.NewFlagSet("auto-update", flag.ContinueOnError)
	configPath := fs.String("config", envOr("WATCHFOR_AGENT_CONFIG", defaultConfig), "path to agent.yml")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	action := fs.Arg(0)
	ctx := context.Background()
	switch action {
	case "on", "off":
		want := action == "on"
		if _, err := config.Set(*configPath, "updates.auto", fmt.Sprint(want)); err != nil {
			fmt.Fprintln(os.Stderr, "auto-update:", err)
			return exitError
		}
		var err error
		if want {
			err = update.EnableTimer(ctx, "", execCmd)
		} else {
			err = update.DisableTimer(ctx, "", execCmd)
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
		if update.TimerEnabled(ctx, execCmd) {
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
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	rest := fs.Args()
	usage := func() int {
		fmt.Fprintf(os.Stderr, `usage: watchfor-agent config <command> [-config %s]

  get <key>          print the effective value (defaults applied)
  set <key> <value>  write the value; the file is validated first
  keys               list the settable keys
  path               print the config file path

Restart the service after set: sudo systemctl restart watchfor-agent
`, defaultConfig)
		return exitUsage
	}
	if len(rest) == 0 {
		return usage()
	}
	switch rest[0] {
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
			fmt.Print(" — restart to apply: sudo systemctl restart watchfor-agent")
		}
		fmt.Println()
		return exitOK
	}
	return usage()
}
