//go:build linux

package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// The service and a cron entry on one host: the cron run steps aside
// quietly, a second daemon refuses (after waiting for a `once` to end).
func TestOneAgentPerHost(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	first, code, ok := lockInstance(ctx, "run", dir, quiet)
	if !ok || code != exitOK {
		t.Fatalf("first run: code %d, proceed %v", code, ok)
	}
	defer first.Release()
	if _, code, ok := lockInstance(ctx, "once", dir, quiet); ok || code != exitOK {
		t.Errorf("once next to a running agent: code %d, proceed %v; want a quiet exit 0", code, ok)
	}
	cctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, code, ok := lockInstance(cctx, "run", dir, quiet); ok {
		t.Errorf("second run proceeded (code %d)", code)
	}
	// no state directory: the agent still runs, as before
	if _, code, ok := lockInstance(ctx, "once", dir+"/missing", quiet); !ok || code != exitOK {
		t.Errorf("missing state dir: code %d, proceed %v; want to run without the lock", code, ok)
	}
}

type stuckAgent struct{ stuck bool }

func (s stuckAgent) Stalled(time.Time) (time.Duration, bool) { return time.Hour, s.stuck }

func TestStallGuardExitsOnAStuckRound(t *testing.T) {
	old := stallEvery
	stallEvery = time.Millisecond
	defer func() { stallEvery = old }()

	exited := make(chan int, 1)
	go stallGuard(context.Background(), stuckAgent{stuck: true}, quiet, func(c int) { exited <- c })
	select {
	case c := <-exited:
		if c != exitError {
			t.Errorf("exit code %d, want %d", c, exitError)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stuck round did not end the daemon")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stallGuard(ctx, stuckAgent{}, quiet, func(int) { t.Error("a healthy agent was ended") })
}
