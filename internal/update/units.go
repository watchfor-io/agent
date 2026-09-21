package update

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The auto-update timer: a root one-shot that runs `upgrade -if-available`
// once a day. The unit texts are the same files as packaging/ (a test keeps
// them identical), embedded so `watchfor-agent auto-update on` works
// without fetching anything.
//
//go:embed units/watchfor-agent-update.service units/watchfor-agent-update.timer
var unitFS embed.FS

const (
	// UpdateService is the one-shot unit the timer runs.
	UpdateService = "watchfor-agent-update.service"
	// UpdateTimer is the daily timer `auto-update on` enables.
	UpdateTimer = "watchfor-agent-update.timer"
	// UnitDir is where the units are written; systemd reads it first.
	UnitDir = "/etc/systemd/system"
)

// Exec runs a command and returns its combined output; tests stub it.
type Exec func(ctx context.Context, name string, args ...string) ([]byte, error)

// EnableTimer writes the two units (unchanged files are left alone) and
// enables + starts the timer. Needs root.
func EnableTimer(ctx context.Context, unitDir string, run Exec) error {
	if unitDir == "" {
		unitDir = UnitDir
	}
	for _, name := range []string{UpdateService, UpdateTimer} {
		want, err := unitFS.ReadFile("units/" + name)
		if err != nil {
			return err
		}
		p := filepath.Join(unitDir, name)
		if cur, err := os.ReadFile(p); err == nil && string(cur) == string(want) {
			continue
		}
		if err := os.WriteFile(p, want, 0o644); err != nil { // #nosec G306 -- a systemd unit is read by everyone
			if errors.Is(err, os.ErrPermission) {
				return fmt.Errorf("cannot write %s: run as root (sudo)", p)
			}
			return err
		}
	}
	if out, err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := run(ctx, "systemctl", "enable", "-q", "--now", UpdateTimer); err != nil {
		return fmt.Errorf("systemctl enable %s: %w: %s", UpdateTimer, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DisableTimer stops and disables the timer and removes both units. A
// timer that was never installed is not an error.
func DisableTimer(ctx context.Context, unitDir string, run Exec) error {
	if unitDir == "" {
		unitDir = UnitDir
	}
	_, _ = run(ctx, "systemctl", "disable", "-q", "--now", UpdateTimer)
	for _, name := range []string{UpdateTimer, UpdateService} {
		if err := os.Remove(filepath.Join(unitDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			if errors.Is(err, os.ErrPermission) {
				return fmt.Errorf("cannot remove %s: run as root (sudo)", filepath.Join(unitDir, name))
			}
			return err
		}
	}
	_, _ = run(ctx, "systemctl", "daemon-reload")
	return nil
}

// TimerEnabled reports whether systemd has the timer enabled.
func TimerEnabled(ctx context.Context, run Exec) bool {
	_, err := run(ctx, "systemctl", "is-enabled", "-q", UpdateTimer)
	return err == nil
}
