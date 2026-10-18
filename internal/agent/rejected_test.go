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
	rec, err := RecordRejection(dir, "abc", "token_unknown", t0)
	if err != nil || rec.Count != 1 || rec.Sticky() {
		t.Fatalf("first rejection: %+v, %v", rec, err)
	}
	if rec.Backoff() != time.Minute {
		t.Fatalf("backoff after one rejection = %v", rec.Backoff())
	}
	// Follow the backoff the daemon would: 1, 5, 15, 30 min, then hourly.
	for i, at := range []time.Duration{1, 6, 21, 51} {
		rec, _ = RecordRejection(dir, "abc", "token_unknown", t0.Add(at*time.Minute))
		if rec.Sticky() {
			t.Fatalf("rejection %d at +%dm must not be sticky: %+v", i+2, at, rec)
		}
	}
	if rec.Count != 5 || rec.Backoff() != time.Hour {
		t.Fatalf("after five rejections: count %d, backoff %v", rec.Count, rec.Backoff())
	}
	rec, _ = RecordRejection(dir, "abc", "token_unknown", t0.Add(111*time.Minute))
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
	rec, _ = RecordRejection(dir, "def", "token_unknown", t0.Add(120*time.Minute))
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
	if _, err := RecordRejection(dir, "abc", "token_unknown", time.Now()); err != nil {
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

// Up to 0.8 the marker had no reason and a plan pause landed in it too:
// such a record is never final by itself — one more rejection, which
// stamps the reason, has to confirm it.
func TestLegacyMarkerNeedsConfirmation(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Now().Add(-3 * time.Hour).UTC()
	legacy := "abc 5 " + t0.Format(time.RFC3339) + " " + t0.Add(2*time.Hour).Format(time.RFC3339) + "\n"
	if err := os.WriteFile(filepath.Join(dir, RejectedMarker), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, ok := ReadRejection(dir, "abc")
	if !ok || rec.Count != 5 || rec.Reason != "" || rec.Sticky() {
		t.Fatalf("legacy record = %+v, %v; want read, not sticky", rec, ok)
	}
	rec, _ = RecordRejection(dir, "abc", "token_unknown", time.Now())
	if !rec.Sticky() || rec.Reason != "token_unknown" {
		t.Fatalf("confirmed = %+v, want sticky with its reason", rec)
	}
	if got, _ := ReadRejection(dir, "abc"); got.Reason != "token_unknown" {
		t.Fatalf("reason not stored: %+v", got)
	}
}

func TestStaleRejection(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "token")
	if err := os.WriteFile(tok, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(tok, old, old); err != nil {
		t.Fatal(err)
	}
	if why := StaleRejection(dir, tok); why != "" {
		t.Fatalf("no record: %q", why)
	}
	// A record for a plan pause is stale whatever the times say.
	if _, err := RecordRejection(dir, "abc", "plan_forbidden", time.Now()); err != nil {
		t.Fatal(err)
	}
	if why := ClearStaleRejection(dir, tok); why == "" {
		t.Fatal("a plan_forbidden record was kept")
	}
	if _, ok := ReadAnyRejection(dir); ok {
		t.Fatal("ClearStaleRejection left it")
	}
	// A real rejection, then the token file is written again: stale.
	if _, err := RecordRejection(dir, "abc", "token_unknown", time.Now()); err != nil {
		t.Fatal(err)
	}
	if why := StaleRejection(dir, tok); why != "" {
		t.Fatalf("token older than the record: %q", why)
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(tok, future, future); err != nil {
		t.Fatal(err)
	}
	if why := StaleRejection(dir, tok); why == "" {
		t.Fatal("token written after the rejection: record not stale")
	}
}
