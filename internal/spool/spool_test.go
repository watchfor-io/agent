package spool

import (
	"bytes"
	"os"
	"testing"
)

func TestOrderAndRemove(t *testing.T) {
	s, err := Open(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"one", "two", "three"} {
		if err := s.Put([]byte(d)); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"one", "two", "three"} {
		name, data, err := s.Next()
		if err != nil || string(data) != want {
			t.Fatalf("Next = %q, %v; want %q", data, err, want)
		}
		if err := s.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	if name, _, _ := s.Next(); name != "" {
		t.Errorf("spool should be empty, got %q", name)
	}
}

func TestTrimDropsOldest(t *testing.T) {
	s, err := Open(t.TempDir(), 25)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		s.Put(bytes.Repeat([]byte{'a' + byte(i)}, 10))
	}
	n, size := s.Stats()
	if n != 2 || size != 20 {
		t.Errorf("after trim: %d files, %d bytes", n, size)
	}
	_, data, _ := s.Next()
	if data[0] != 'd' {
		t.Errorf("oldest kept should be the 4th batch, got %q", data)
	}
}

func TestNoTempFilesLeftBehind(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, 1<<20)
	s.Put([]byte("x"))
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name()[0] == '.' {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o500)
	if _, err := Open(dir, 1); err == nil {
		t.Error("Open should fail on a read-only directory")
	}
}
