package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/watchfor-io/agent/internal/push"
	"github.com/watchfor-io/agent/internal/spool"
	"github.com/watchfor-io/agent/internal/update"
)

// The server's hint is recorded when it names something newer, cleared
// when it names nothing newer, and left alone when it says nothing —
// `check` and a server without an opinion must not wipe what the daemon
// learned.
func TestUpdateHintFollowsTheServer(t *testing.T) {
	a := newAgent(t, &fakeSink{}, nil)
	a.o.StateDir, a.o.Version = t.TempDir(), "1.0.0"
	if err := update.WriteState(a.o.StateDir, "9.9.9"); err != nil {
		t.Fatal(err)
	}
	a.noteUpdate("")
	if v, _, ok := update.ReadState(a.o.StateDir); !ok || v != "9.9.9" {
		t.Fatalf("an empty hint wiped the state (%q, %v)", v, ok)
	}
	a.noteUpdate("0.9.0") // older than what runs: nothing to install
	if _, _, ok := update.ReadState(a.o.StateDir); ok {
		t.Fatal("a hint for an older release did not clear the state")
	}
	a.noteUpdate("2.0.0")
	if v, _, ok := update.ReadState(a.o.StateDir); !ok || v != "2.0.0" {
		t.Fatalf("a newer release was not recorded (%q, %v)", v, ok)
	}
	a.noteUpdate("1.0.0") // the release was withdrawn: the hint must go again
	if _, _, ok := update.ReadState(a.o.StateDir); ok {
		t.Fatal("a withdrawn release left a stale hint")
	}
}

// Replay drops a batch it cannot read, says so, and moves on; a rejected
// token pauses the replay without throwing the batch away.
func TestReplayDropsUnreadableAndPausesOnRejection(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	dir := t.TempDir()
	sp, err := spool.Open(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.Put([]byte("{}")); err != nil {
		t.Fatal(err)
	}
	// an older file nobody can read sorts first
	bad := filepath.Join(dir, "00000000000000000001-000000.gz")
	if err := os.WriteFile(bad, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	sink := &plainSink{fail: push.ErrUnauthorized}
	a := newAgent(t, sink, sp)
	a.replay(context.Background())
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatal("the unreadable batch was kept and would block every replay")
	}
	if n, _ := sp.Stats(); n != 1 {
		t.Fatalf("a rejected token dropped the readable batch: %d left", n)
	}
	sink.fail = nil
	a.replay(context.Background())
	if n, _ := sp.Stats(); n != 0 {
		t.Fatalf("a delivered batch stayed in the spool: %d left", n)
	}
}

// plainSink answers without reading the body: replay only cares about
// the verdict.
type plainSink struct{ fail error }

func (s *plainSink) Send(context.Context, []byte) (push.Ack, error) { return push.Ack{}, s.fail }
