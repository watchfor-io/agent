//go:build linux

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Two agents on one state directory: the second is told so, waits when
// asked to, and gets in as soon as the first lets go.
func TestOneInstancePerStateDir(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	first, err := Lock(ctx, dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(ctx, dir, 0); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lock = %v, want ErrBusy", err)
	}
	start := time.Now()
	if _, err := Lock(ctx, dir, 600*time.Millisecond); !errors.Is(err, ErrBusy) || time.Since(start) < 500*time.Millisecond {
		t.Fatalf("waiting lock = %v after %s, want ErrBusy after the wait", err, time.Since(start))
	}
	go func() { time.Sleep(300 * time.Millisecond); first.Release() }()
	second, err := Lock(ctx, dir, 5*time.Second)
	if err != nil {
		t.Fatalf("lock after release = %v", err)
	}
	second.Release()

	st, err := os.Stat(filepath.Join(dir, LockFile))
	if err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("lock file = %v, %v; want 0644 so root and the service account can both take it", st, err)
	}
}

func TestLockRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, LockFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(context.Background(), dir, 0); err == nil {
		t.Error("a symlinked lock file must be refused")
	}
}
