package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/watchfor-io/agent/internal/push"
	"github.com/watchfor-io/agent/internal/spool"
)

// A host the plan does not take (403 plan_forbidden) is paused, not dead:
// no stop however long it lasts, a stale rejected-token record goes, the
// batches are not hoarded, the asks thin out to every 30 minutes, and the
// first accepted batch puts everything back.
func TestPlanPauseNeverStops(t *testing.T) {
	dir := t.TempDir()
	sp, _ := spool.Open(t.TempDir(), 1<<20)
	sink := &fakeSink{fail: fmt.Errorf("%w: over the host limit", push.ErrPlanPaused)}
	a := newAgent(t, sink, sp)
	a.o.StateDir, a.o.TokenFingerprint = dir, "abc"
	now := time.Unix(1_700_000_000, 0)
	a.o.Now = func() time.Time { return now }
	a.jitter = func(d time.Duration) time.Duration { return d }
	if _, err := RecordRejection(dir, "abc", "token_unknown", now.Add(-2*time.Hour)); err != nil { // left by an older agent that took the 403 for a dead token
		t.Fatal(err)
	}

	var waits []time.Duration
	for range 60 { // a day and more of pauses
		if err := a.tick(context.Background()); err != nil {
			t.Fatalf("a plan pause stopped the agent: %v", err)
		}
		waits = append(waits, a.pauseUntil.Sub(now))
		calls := sink.calls
		if err := a.tick(context.Background()); err != nil || sink.calls != calls {
			t.Fatalf("asked during the pause (err %v)", err)
		}
		now = a.pauseUntil
	}
	for i, want := range []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		if waits[i] != want {
			t.Errorf("pause %d = %s, want %s", i+1, waits[i], want)
		}
	}
	if n, _ := sp.Stats(); n != 0 {
		t.Errorf("%d batches spooled during a plan pause, want none", n)
	}
	if _, ok := ReadRejection(dir, "abc"); ok {
		t.Error("the stale rejected-token record survived a plan_forbidden answer")
	}
	if st, ok := ReadPushState(dir); !ok || st.Result != PushPlanPaused {
		t.Errorf("push state = %+v, %v; want plan_paused", st, ok)
	}

	sink.fail = nil
	if err := a.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 1 || a.planPauses != 0 {
		t.Errorf("after the plan allows it again: sent %d, planPauses %d", len(sink.got), a.planPauses)
	}
	if st, _ := ReadPushState(dir); st.Result != PushAccepted || !st.Since.Equal(now.UTC()) {
		t.Errorf("push state = %+v, want accepted since %s", st, now.UTC())
	}
}

// `once` tells cron when nothing got through.
func TestOnceFailsWhenNothingWasAccepted(t *testing.T) {
	sp, _ := spool.Open(t.TempDir(), 1<<20)
	sink := &fakeSink{fail: &push.StatusError{Code: 503}}
	a := newAgent(t, sink, sp)
	if err := a.Once(context.Background(), time.Millisecond); !errors.Is(err, ErrNotAccepted) {
		t.Errorf("once against a failing server = %v, want ErrNotAccepted", err)
	}
	if n, _ := sp.Stats(); n != 1 {
		t.Errorf("spooled %d, want the batch kept", n)
	}
	ok := newAgent(t, &fakeSink{}, nil)
	if err := ok.Once(context.Background(), time.Millisecond); err != nil {
		t.Errorf("once that was accepted = %v", err)
	}
}

// The push state is written when the answer changes, not on every push.
func TestPushStateOnlyOnChange(t *testing.T) {
	dir := t.TempDir()
	a := newAgent(t, &fakeSink{}, nil)
	a.o.StateDir = dir
	now := time.Unix(1_700_000_000, 0)
	a.o.Now = func() time.Time { return now }
	a.tick(context.Background())
	first, _ := ReadPushState(dir)
	now = now.Add(time.Hour)
	a.tick(context.Background())
	if st, _ := ReadPushState(dir); !st.Since.Equal(first.Since) {
		t.Errorf("an unchanged answer rewrote the state: %v → %v", first.Since, st.Since)
	}
	// a new agent (the next `once`) picks up where the last one was
	b := newAgent(t, &fakeSink{}, nil)
	b.o.StateDir = dir
	b.o.Now = func() time.Time { return now.Add(time.Hour) }
	b.tick(context.Background())
	if st, _ := ReadPushState(dir); !st.Since.Equal(first.Since) {
		t.Error("a fresh agent rewrote an unchanged answer")
	}
}

// Every batch says what the agent itself costs.
func TestFootprint(t *testing.T) {
	dir := t.TempDir()
	sp, _ := spool.Open(dir, 1<<20)
	sink := &fakeSink{}
	a := newAgent(t, sink, sp)
	a.o.StateDir = dir
	if err := a.Once(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range sink.got[0].Samples {
		names[s.M] = true
	}
	for _, want := range []string{"agent.rss_bytes", "agent.cpu_pct", "agent.spool_batches"} {
		if !names[want] {
			t.Errorf("batch has no %s", want)
		}
	}
}
