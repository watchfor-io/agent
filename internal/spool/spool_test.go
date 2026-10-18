package spool

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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

// A FIFO or symlink planted under a batch name must not hang Next or make
// it read the link's target: it is unlinked and the real batches still
// come out in order.
func TestNextSkipsNonRegularEntries(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("root-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	// names that sort before anything Put writes
	if err := syscall.Mkfifo(filepath.Join(dir, "00000000000000000000-000000.gz"), 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "00000000000000000001-000000.gz")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "00000000000000000002-000000.gz"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Put([]byte("real")); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Stats(); n != 1 {
		t.Errorf("Stats counts %d batches, want 1", n)
	}
	if n, _ := Stats(dir); n != 1 {
		t.Errorf("package Stats counts %d batches, want 1", n)
	}
	type res struct {
		name string
		data []byte
		err  error
	}
	done := make(chan res, 1)
	go func() { n, d, err := s.Next(); done <- res{n, d, err} }()
	select {
	case r := <-done:
		if r.err != nil || string(r.data) != "real" {
			t.Fatalf("Next = %q, %q, %v; want the real batch", r.name, r.data, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Next blocked on a FIFO in the spool")
	}
	for _, n := range []string{"00000000000000000000-000000.gz", "00000000000000000001-000000.gz"} {
		if _, err := os.Lstat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%s was not removed: %v", n, err)
		}
	}
	if b, _ := os.ReadFile(secret); string(b) != "root-only" {
		t.Error("symlink target was touched")
	}
}
