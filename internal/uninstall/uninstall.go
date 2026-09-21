//go:build linux

// Package uninstall takes the agent off a machine: the service and the
// update timer, the cron entry, the binary, the state, the config with
// its token, and the service user. It removes what the installer created
// or what agent.yml names — never an arbitrary directory.
package uninstall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/watchfor-io/agent/internal/config"
	"github.com/watchfor-io/agent/internal/update"
)

const (
	// Service is the unit the installer enables.
	Service = "watchfor-agent.service"
	// User is the service account the installer creates.
	User = "watchfor-agent"
	// CronFile is the entry a cron install (no systemd) uses instead
	// of the service.
	CronFile = "/etc/cron.d/watchfor-agent"
	// LockDir is where the upgrade lock lives; a leftover lock file
	// goes with the rest.
	LockDir    = update.LockDir
	lockFile   = update.LockName
	binaryName = "watchfor-agent"
	// dirName is what every directory the installer creates is called. A
	// directory the config points at under another name is left alone:
	// this removes the agent, not whatever a typo in agent.yml names.
	dirName = "watchfor-agent"
)

// Options say where the agent lives; zero values are the installer's
// defaults.
type Options struct {
	ConfigPath string
	// ExePath is the binary to remove (os.Executable, symlinks resolved).
	// Only a file called watchfor-agent is removed.
	ExePath    string
	KeepConfig bool
	UnitDir    string
	CronFile   string
	LockDir    string
	User       string
	Out        io.Writer
	Exec       update.Exec
}

func (o *Options) setDefaults() {
	if o.UnitDir == "" {
		o.UnitDir = update.UnitDir
	}
	if o.CronFile == "" {
		o.CronFile = CronFile
	}
	if o.LockDir == "" {
		o.LockDir = LockDir
	}
	if o.User == "" {
		o.User = User
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
}

// Paths is everything Run touches, resolved from the options and agent.yml.
type Paths struct {
	Units      []string
	Cron       string
	StateDir   string
	TokenFile  string
	ConfigDir  string // empty when agent.yml is not in a watchfor-agent directory
	ConfigFile string
	Lock       string
	Exe        string
}

// Resolve reads the state directory and token file from agent.yml (the
// defaults when it is missing or unreadable) and lays out the paths.
func Resolve(o Options) Paths {
	o.setDefaults()
	p := Paths{
		Units: []string{
			filepath.Join(o.UnitDir, Service),
			filepath.Join(o.UnitDir, update.UpdateTimer),
			filepath.Join(o.UnitDir, update.UpdateService),
		},
		Cron:       o.CronFile,
		StateDir:   config.DefaultSpoolDir,
		ConfigFile: o.ConfigPath,
		Lock:       filepath.Join(o.LockDir, lockFile),
		Exe:        o.ExePath,
	}
	if v, err := config.Get(o.ConfigPath, "spool.dir"); err == nil && v != "" {
		p.StateDir = v
	}
	if v, err := config.Get(o.ConfigPath, "server.token_file"); err == nil {
		p.TokenFile = v
	}
	if dir := filepath.Dir(o.ConfigPath); filepath.Base(dir) == dirName {
		p.ConfigDir = dir
	}
	return p
}

// Plan lists what is present and would go — for the confirmation prompt.
func Plan(ctx context.Context, o Options) []string {
	o.setDefaults()
	p := Resolve(o)
	var out []string
	add := func(what, path string) {
		if path == "" {
			return
		}
		if _, err := os.Lstat(path); err == nil {
			out = append(out, fmt.Sprintf("%-7s %s", what, path))
		}
	}
	for _, u := range p.Units {
		add("unit", u)
	}
	add("cron", p.Cron)
	add("state", p.StateDir)
	if !o.KeepConfig {
		add("token", p.TokenFile)
		if p.ConfigDir != "" {
			add("config", p.ConfigDir)
		} else {
			add("config", p.ConfigFile)
		}
	}
	add("lock", p.Lock)
	if filepath.Base(p.Exe) == binaryName {
		add("binary", p.Exe)
	}
	if userExists(ctx, o) {
		out = append(out, fmt.Sprintf("%-7s %s (system user)", "user", o.User))
	}
	return out
}

// Run removes everything. It carries on past a failure so one stubborn
// file does not leave the rest behind, and returns the failures joined.
func Run(ctx context.Context, o Options) error {
	o.setDefaults()
	p := Resolve(o)
	var errs []error
	say := func(format string, a ...any) { fmt.Fprintf(o.Out, format+"\n", a...) }
	run := func(name string, args ...string) error {
		_, err := o.Exec(ctx, name, args...)
		return err
	}
	rmFile := func(what, path string) {
		if path == "" {
			return
		}
		switch err := os.Remove(path); {
		case err == nil:
			say("removed %s", path)
		case errors.Is(err, os.ErrNotExist):
		default:
			errs = append(errs, fmt.Errorf("%s: %w", what, err))
		}
	}
	rmDir := func(path string) {
		if path == "" {
			return
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return
		}
		if !safeDir(path) {
			say("kept %s: not a %s directory — remove it yourself if only the agent used it", path, dirName)
			return
		}
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, err)
			return
		}
		say("removed %s", path)
	}

	// Stop first, so nothing writes to the state or restarts from the
	// binary while it goes.
	_ = run("systemctl", "disable", "-q", "--now", Service)
	_ = run("systemctl", "disable", "-q", "--now", update.UpdateTimer)
	for _, u := range p.Units {
		rmFile("unit", u)
	}
	rmFile("cron entry", p.Cron)
	_ = run("systemctl", "daemon-reload")
	_ = run("systemctl", "reset-failed", Service)

	rmDir(p.StateDir)
	if o.KeepConfig {
		if _, err := os.Lstat(p.ConfigFile); err == nil {
			say("kept %s (-keep-config)", p.ConfigFile)
		}
	} else {
		if p.TokenFile != "" {
			switch err := wipe(p.TokenFile); {
			case err == nil:
				say("removed %s (overwritten first)", p.TokenFile)
			case errors.Is(err, os.ErrNotExist):
			default:
				errs = append(errs, fmt.Errorf("token file: %w", err))
			}
		}
		if p.ConfigDir != "" {
			rmDir(p.ConfigDir)
		} else {
			rmFile("config", p.ConfigFile)
		}
	}
	rmFile("lock", p.Lock)
	if userExists(ctx, o) {
		if err := run("userdel", o.User); err != nil {
			errs = append(errs, fmt.Errorf("userdel %s: %w", o.User, err))
		} else {
			say("removed system user %s", o.User)
		}
		_ = run("groupdel", o.User) // already gone when it was the user's private group
		if o.KeepConfig && p.ConfigDir != "" {
			// a kept config must not belong to a uid that no longer exists
			// #nosec G122 -- Lchown acts on the link itself, never its target,
			// so a planted symlink cannot redirect the chown out of the tree.
			_ = filepath.WalkDir(p.ConfigDir, func(path string, _ os.DirEntry, err error) error {
				if err == nil {
					_ = os.Lchown(path, 0, 0)
				}
				return nil
			})
		}
	}
	// The binary last: on Linux an unlinked executable keeps running.
	switch {
	case p.Exe == "":
	case filepath.Base(p.Exe) == binaryName:
		rmFile("binary", p.Exe)
	default:
		say("kept %s: not called %s", p.Exe, binaryName)
	}
	return errors.Join(errs...)
}

// safeDir: absolute and called watchfor-agent — the only directories
// removed recursively.
func safeDir(path string) bool {
	clean := filepath.Clean(path)
	return filepath.IsAbs(clean) && filepath.Base(clean) == dirName
}

// wipe overwrites a regular file with zeros before removing it, so the
// token does not linger in freed blocks. A symlink is only unlinked.
func wipe(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if st.Mode().IsRegular() && st.Size() > 0 {
		if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NOFOLLOW, 0); err == nil {
			if fst, err := f.Stat(); err == nil && fst.Mode().IsRegular() { // still the same kind of thing
				_, _ = f.Write(make([]byte, fst.Size()))
				_ = f.Sync()
			}
			_ = f.Close()
		}
	}
	return os.Remove(path)
}

func userExists(ctx context.Context, o Options) bool {
	_, err := o.Exec(ctx, "id", "-u", o.User)
	return err == nil
}
