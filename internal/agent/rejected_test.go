package agent

import (
	"testing"
	"time"
)

func TestRejectedMarker(t *testing.T) {
	dir := t.TempDir()
	if _, ok := RejectedAt(dir, "abc"); ok {
		t.Fatal("empty dir reports a rejection")
	}
	at := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if err := MarkRejected(dir, "abc", at); err != nil {
		t.Fatal(err)
	}
	if got, ok := RejectedAt(dir, "abc"); !ok || !got.Equal(at) {
		t.Fatalf("RejectedAt = %v, %v", got, ok)
	}
	// A different token (fingerprint) is not blocked by an old record.
	if _, ok := RejectedAt(dir, "def"); ok {
		t.Fatal("other token blocked")
	}
	ClearRejected(dir)
	if _, ok := RejectedAt(dir, "abc"); ok {
		t.Fatal("record survived ClearRejected")
	}
}
