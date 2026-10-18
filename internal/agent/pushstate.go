package agent

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/watchfor-io/agent/internal/safefile"
)

// PushStateFile records how the server answered the last push, for
// `health` ("WatchFor accepts this host"). It is rewritten only when the
// answer changes, never once per push.
const PushStateFile = "push-state"

// The answers PushState records.
const (
	PushAccepted      = "accepted"
	PushTokenRejected = "token_rejected"
	PushPlanPaused    = "plan_paused"
	PushNoAnswer      = "no_answer" // network trouble, 5xx, 429: the batch is spooled
	PushRefused       = "refused"   // a 4xx about the batch itself: it was dropped
)

// PushState is the last answer and since when it has been the answer.
type PushState struct {
	Result string
	Since  time.Time
}

// ReadPushState returns the recorded answer; ok is false when there is
// none or the file cannot be read (the state folder is 0700 — `health`
// run without root cannot look in).
func ReadPushState(dir string) (PushState, bool) {
	if dir == "" {
		return PushState{}, false
	}
	b, err := safefile.ReadFile(filepath.Join(dir, PushStateFile), stateCap)
	if err != nil {
		return PushState{}, false
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return PushState{}, false
	}
	since, err := time.Parse(time.RFC3339, f[1])
	if err != nil {
		return PushState{}, false
	}
	return PushState{Result: f[0], Since: since}, true
}

// notePush records result when it differs from the last one recorded.
func (a *Agent) notePush(result string) {
	if a.o.StateDir == "" {
		return
	}
	if a.pushState == "" {
		if st, ok := ReadPushState(a.o.StateDir); ok {
			a.pushState = st.Result // a `once` starts from what the last run saw
		}
	}
	if a.pushState == result {
		return
	}
	line := result + " " + a.o.Now().UTC().Format(time.RFC3339) + "\n"
	if err := writeState(a.o.StateDir, PushStateFile, []byte(line)); err != nil {
		a.o.Log.Debug("could not record the push result", "dir", a.o.StateDir, "error", err)
		return
	}
	a.pushState = result
}

// writeState replaces dir/name with data the way every state file is
// written: safefile's O_EXCL temp, fsync, rename.
func writeState(dir, name string, data []byte) error {
	return safefile.WriteAtomic(dir, name, data, 0o600)
}

// stateCap bounds a state-file read: they are one short line.
const stateCap = 4 << 10
