package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/watchfor-io/agent/internal/safefile"
)

// PauseFile carries a pause from one `once` to the next: cron starts a new
// process every minute, and without it each run forgot that the server had
// said "come back in 30 s" (Retry-After), that the token was rejected a
// minute ago (back off 1, 5, 15 … minutes), or that the server has been
// failing for a while — and asked again at once. The daemon keeps all of
// this in memory and does not use the file.
const PauseFile = "pause-state"

// maxPause bounds a pause read back from disk: the longest the agent ever
// waits is an hour (a token backoff, a Retry-After). A clock stepped back,
// or a file someone edited, cannot silence cron for longer.
const maxPause = time.Hour + time.Minute

// loadPause restores what the last `once` left: the pause, whether its
// batches are dropped (a plan pause), and the counters the backoffs grow
// from. Counters older than an hour past their pause are forgotten.
func (a *Agent) loadPause() {
	if a.o.StateDir == "" {
		return
	}
	b, err := safefile.ReadFile(filepath.Join(a.o.StateDir, PauseFile), stateCap)
	if err != nil {
		return
	}
	f := strings.Fields(string(b))
	if len(f) < 4 {
		return
	}
	until, err := time.Parse(time.RFC3339Nano, f[0])
	if err != nil {
		return
	}
	now := a.o.Now()
	if now.Sub(until) > time.Hour {
		return // the trouble is long over
	}
	if limit := now.Add(maxPause); until.After(limit) {
		until = limit
	}
	a.pauseUntil, a.pauseDrop = until, f[1] == "1"
	a.failures, _ = strconv.Atoi(f[2])
	a.planPauses, _ = strconv.Atoi(f[3])
	a.failures, a.planPauses = min(max(a.failures, 0), 64), min(max(a.planPauses, 0), 1<<20)
}

// savePause writes what the next `once` needs, or removes the file when
// nothing is in force.
func (a *Agent) savePause() {
	if a.o.StateDir == "" {
		return
	}
	path := filepath.Join(a.o.StateDir, PauseFile)
	if !a.pauseUntil.After(a.o.Now()) && a.failures == 0 && a.planPauses == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			a.o.Log.Debug("could not remove the pause record", "error", err)
		}
		return
	}
	drop := "0"
	if a.pauseDrop {
		drop = "1"
	}
	line := strings.Join([]string{a.pauseUntil.UTC().Format(time.RFC3339Nano), drop, strconv.Itoa(a.failures), strconv.Itoa(a.planPauses)}, " ") + "\n"
	if err := writeState(a.o.StateDir, PauseFile, []byte(line)); err != nil {
		a.o.Log.Debug("could not record the pause for the next run", "error", err)
	}
}
