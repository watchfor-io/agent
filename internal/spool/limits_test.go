package spool

import (
	"bytes"
	"errors"
	"testing"
)

// Two batches in one clock tick are two files, and a batch bigger than
// the whole spool is refused rather than written and trimmed away.
func TestNamesDoNotCollideAndOversizeIsRefused(t *testing.T) {
	sp, err := Open(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if err := sp.Put([]byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := sp.Stats(); n != 50 {
		t.Fatalf("50 puts left %d files: names collided", n)
	}
	small, err := Open(t.TempDir(), 16)
	if err != nil {
		t.Fatal(err)
	}
	if err := small.Put(bytes.Repeat([]byte("x"), 17)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("an oversize batch was not refused: %v", err)
	}
	if n, _ := small.Stats(); n != 0 {
		t.Fatalf("an oversize batch left %d files", n)
	}
}
