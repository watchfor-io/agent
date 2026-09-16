package agent

import (
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
	rec, _ = RecordRejection(dir, "abc", t0.Add(1*time.Minute))
	rec, _ = RecordRejection(dir, "abc", t0.Add(6*time.Minute))
	if rec.Count != 3 || rec.Sticky() {
		t.Fatalf("three rejections within 6 minutes must not be sticky: %+v", rec)
	}
	rec, _ = RecordRejection(dir, "abc", t0.Add(16*time.Minute))
	if !rec.Sticky() {
		t.Fatalf("four rejections over 16 minutes should be sticky: %+v", rec)
	}
	got, ok := ReadRejection(dir, "abc")
	if !ok || got.Count != 4 || !got.First.Equal(t0) {
		t.Fatalf("ReadRejection = %+v, %v", got, ok)
	}
	// A different token starts from zero.
	if _, ok := ReadRejection(dir, "def"); ok {
		t.Fatal("other token blocked")
	}
	rec, _ = RecordRejection(dir, "def", t0.Add(20*time.Minute))
	if rec.Count != 1 {
		t.Fatalf("new token count = %d", rec.Count)
	}
	ClearRejected(dir)
	if _, ok := ReadRejection(dir, "def"); ok {
		t.Fatal("record survived ClearRejected")
	}
}
