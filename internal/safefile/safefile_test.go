package safefile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A symlink planted at the target name is replaced, never written through.
func TestWriteAtomicReplacesSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "state")); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(dir, "state", []byte("9.9.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep\n" {
		t.Fatalf("symlink target was overwritten: %q", b)
	}
	st, err := os.Lstat(filepath.Join(dir, "state"))
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		t.Fatalf("target is not a fresh 0600 regular file: %v %v", st, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".state.tmp-*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

// The old fixed temp name (".state.tmp") planted as a symlink is ignored:
// the temp is a fresh O_EXCL file with a random suffix.
func TestWriteAtomicIgnoresPlantedTemp(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	_ = os.WriteFile(victim, []byte("keep\n"), 0o600)
	_ = os.Symlink(victim, filepath.Join(dir, ".state.tmp"))
	if err := WriteAtomic(dir, "state", []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep\n" {
		t.Fatalf("victim overwritten: %q", b)
	}
	if st, _ := os.Stat(filepath.Join(dir, "state")); st.Mode().Perm() != 0o640 {
		t.Fatalf("perm = %v", st.Mode().Perm())
	}
}

func TestWriteAtomicRejectsPaths(t *testing.T) {
	for _, name := range []string{"", "../x", "a/b"} {
		if err := WriteAtomic(t.TempDir(), name, nil, 0o600); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
	if err := WriteAtomic("", "x", nil, 0o600); err == nil {
		t.Error("empty dir accepted")
	}
}

// Reading a FIFO, a symlink or an oversized file fails at once.
func TestReadFileRefusesNonRegular(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	real := filepath.Join(dir, "real")
	_ = os.WriteFile(real, []byte("hello"), 0o600)
	link := filepath.Join(dir, "link")
	_ = os.Symlink(real, link)

	done := make(chan error, 1)
	go func() { _, err := ReadFile(fifo, 64); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Errorf("fifo: err = %v, want ErrNotRegular", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ReadFile blocked on a FIFO")
	}
	if _, err := ReadFile(link, 64); !errors.Is(err, ErrNotRegular) {
		t.Errorf("symlink: err = %v, want ErrNotRegular", err)
	}
	if _, err := ReadFile(dir, 64); !errors.Is(err, ErrNotRegular) {
		t.Errorf("directory: err = %v, want ErrNotRegular", err)
	}
	if _, err := ReadFile(real, 4); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized: err = %v, want ErrTooLarge", err)
	}
	hard := filepath.Join(dir, "hard")
	if err := os.Link(real, hard); err == nil {
		if _, err := ReadFile(hard, 64); !errors.Is(err, ErrNotRegular) {
			t.Errorf("hard link: err = %v, want ErrNotRegular", err)
		}
		_ = os.Remove(hard)
	}
	if b, err := ReadFile(real, 5); err != nil || string(b) != "hello" {
		t.Errorf("regular: %q %v", b, err)
	}
	if b, err := ReadFile(real, 0); err != nil || !strings.HasPrefix(string(b), "hello") {
		t.Errorf("unlimited: %q %v", b, err)
	}
}
