package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchReloadsOnlyOnValidChange(t *testing.T) {
	old := WatchInterval
	WatchInterval = 60 * time.Millisecond
	t.Cleanup(func() { WatchInterval = old })
	dir := t.TempDir()
	p := filepath.Join(dir, "agent.yml")
	// a strictly newer mtime every write, even on coarse filesystems
	stamp := time.Now()
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o640); err != nil {
			t.Fatal(err)
		}
		stamp = stamp.Add(2 * time.Second)
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	write("server:\n  url: https://ingest.example\n  token: wfh_a\ninterval: 15s\n")

	invalid := make(chan error, 8)
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { done <- Watch(ctx, p, true, func(err error) { invalid <- err }) }()
	time.Sleep(100 * time.Millisecond) // let the watcher take its first look before the file moves

	tick := func() { time.Sleep(WatchInterval + 150*time.Millisecond) }
	// broken file: reported, not reloaded
	write("server:\n  url: https://ingest.example\n  token: wfh_a\ninterval: 15s\nbogus: 1\n")
	tick()
	select {
	case err := <-done:
		t.Fatalf("watch returned on an invalid file: %v", err)
	default:
	}
	select {
	case <-invalid:
	default:
		t.Fatal("invalid change not reported")
	}
	// same content touched again: nothing
	write("server:\n  url: https://ingest.example\n  token: wfh_a\ninterval: 15s\nbogus: 1\n")
	tick()
	select {
	case err := <-done:
		t.Fatalf("watch returned on a touch: %v", err)
	default:
	}
	// a valid change: reload
	write("server:\n  url: https://ingest.example\n  token: wfh_a\ninterval: 30s\n")
	select {
	case err := <-done:
		if !errors.Is(err, ErrReload) {
			t.Fatalf("got %v, want ErrReload", err)
		}
	case <-time.After(WatchInterval + time.Second):
		t.Fatal("valid change not picked up")
	}
}

// A broken edit is refused once; putting the old text back is not a
// change and must not restart the agent.
func TestRestoringAfterARejectedEditIsNoChange(t *testing.T) {
	old := WatchInterval
	WatchInterval = 60 * time.Millisecond
	t.Cleanup(func() { WatchInterval = old })
	path := filepath.Join(t.TempDir(), "agent.yml")
	good := "server:\n  url: https://ingest.example\n  token: wfh_a\ninterval: 15s\n"
	write := func(text string) {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		// a new mtime on a fast disk: the watcher compares size and mtime first
		now := time.Now().Add(time.Second)
		_ = os.Chtimes(path, now, now)
	}
	write(good)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	invalid := make(chan error, 4)
	done := make(chan error, 1)
	go func() { done <- Watch(ctx, path, true, func(err error) { invalid <- err }) }()
	time.Sleep(WatchInterval + 50*time.Millisecond)
	write(good + "bogus: 1\n")
	select {
	case <-invalid:
	case <-time.After(WatchInterval + time.Second):
		t.Fatal("the broken edit was not reported")
	}
	write(good)
	select {
	case err := <-done:
		t.Fatalf("restoring the old text restarted the agent: %v", err)
	case <-invalid:
		t.Fatal("the restored text was reported as invalid")
	case <-time.After(3*WatchInterval + 200*time.Millisecond):
	}
}
