package agent

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRejectionBecomesStickyOnlyAfterRepeatedRejections(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	rec, err := RecordRejection(dir, "abc", t0)
	if err != nil || rec.Count != 1 || rec.Sticky() {
		t.Fatalf("first rejection: %+v, %v", rec, err)
	}
	if rec.Backoff() != time.Minute {
		t.Fatalf("backoff after one rejection = %v", rec.Backoff())
	}
	// Follow the backoff the daemon would: 1, 5, 15, 30 min, then hourly.
	for i, at := range []time.Duration{1, 6, 21, 51} {
		rec, _ = RecordRejection(dir, "abc", t0.Add(at*time.Minute))
		if rec.Sticky() {
			t.Fatalf("rejection %d at +%dm must not be sticky: %+v", i+2, at, rec)
		}
	}
	if rec.Count != 5 || rec.Backoff() != time.Hour {
		t.Fatalf("after five rejections: count %d, backoff %v", rec.Count, rec.Backoff())
	}
	rec, _ = RecordRejection(dir, "abc", t0.Add(111*time.Minute))
	if !rec.Sticky() {
		t.Fatalf("six rejections over 1h51m should be sticky: %+v", rec)
	}
	got, ok := ReadRejection(dir, "abc")
	if !ok || got.Count != 6 || !got.First.Equal(t0) {
		t.Fatalf("ReadRejection = %+v, %v", got, ok)
	}
	// A different token starts from zero.
	if _, ok := ReadRejection(dir, "def"); ok {
		t.Fatal("other token blocked")
	}
	rec, _ = RecordRejection(dir, "def", t0.Add(120*time.Minute))
	if rec.Count != 1 {
		t.Fatalf("new token count = %d", rec.Count)
	}
	ClearRejected(dir)
	if _, ok := ReadRejection(dir, "def"); ok {
		t.Fatal("record survived ClearRejected")
	}
}

// The marker lives in a folder root sometimes writes to: a symlink or a
// FIFO planted under its name neither redirects the write nor stalls the
// read.
func TestRejectedMarkerIgnoresPlantedFiles(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, RejectedMarker)); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadRejection(dir, "abc"); ok {
		t.Error("read a record through a symlink")
	}
	if _, err := RecordRejection(dir, "abc", time.Now()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep\n" {
		t.Errorf("the write went through the symlink: %q", b)
	}
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no mkfifo here")
	}
	if err := os.Rename(fifo, filepath.Join(filepath.Dir(fifo), RejectedMarker)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { ReadRejection(filepath.Dir(fifo), "abc"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO marker hung")
	}
}
