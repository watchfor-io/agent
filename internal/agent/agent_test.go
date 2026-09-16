package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/modules"
	"github.com/watchfor-io/agent/internal/push"
	"github.com/watchfor-io/agent/internal/spool"
)

type fakeModule struct {
	name string
	err  error
	n    int
}

func (m *fakeModule) Name() string { return m.name }
func (m *fakeModule) Collect(_ context.Context, b *metric.Batch) error {
	m.n++
	b.Add("fake.ticks", float64(m.n))
	return m.err
}

type fakeSink struct {
	fail     error
	interval int // what the server asks for, seconds
	got      []*metric.Payload
	calls    int
}

func (s *fakeSink) Send(_ context.Context, body []byte) (push.Ack, error) {
	s.calls++
	if s.fail != nil {
		return push.Ack{}, s.fail
	}
	p, err := push.Decode(body)
	if err != nil {
		return push.Ack{}, err
	}
	s.got = append(s.got, p)
	return push.Ack{Accepted: len(p.Samples), Interval: s.interval}, nil
}

func newAgent(t *testing.T, sink Sink, sp *spool.Spool) *Agent {
	t.Helper()
	return New(Options{
		Modules:  []modules.Module{&fakeModule{name: "fake"}},
		Sink:     sink,
		Spool:    sp,
		Host:     metric.Host{ID: "h", Name: "test", Arch: "amd64"},
		Facts:    func() metric.Facts { return metric.Facts{CPUCores: 2} },
		Version:  "t",
		Interval: 5 * time.Second,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestOnceSendsSecondSampleWithFacts(t *testing.T) {
	sink := &fakeSink{}
	a := newAgent(t, sink, nil)
	if err := a.Once(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 1 {
		t.Fatalf("sent %d payloads, want 1", len(sink.got))
	}
	p := sink.got[0]
	if p.Samples[0].V != 2 {
		t.Errorf("second collection should be sent, got tick %v", p.Samples[0].V)
	}
	if p.Facts == nil || p.Facts.CPUCores != 2 {
		t.Error("first payload must carry facts")
	}
	if p.Host.Name != "test" || p.Version != metric.PayloadVersion {
		t.Errorf("payload = %+v", p)
	}
}

func TestSpoolAndReplay(t *testing.T) {
	sp, _ := spool.Open(t.TempDir(), 1<<20)
	sink := &fakeSink{fail: &push.StatusError{Code: 503}}
	a := newAgent(t, sink, sp)

	a.tick(context.Background())
	a.tick(context.Background())
	if n, _ := sp.Stats(); n != 2 {
		t.Fatalf("spooled %d, want 2", n)
	}

	sink.fail = nil
	a.tick(context.Background())
	if n, _ := sp.Stats(); n != 0 {
		t.Errorf("spool not drained: %d left", n)
	}
	if len(sink.got) != 3 {
		t.Errorf("sent %d payloads after recovery, want 3", len(sink.got))
	}
}

// A rejected token stops the agent only once the rejection has held
// across several attempts and minutes; a single 401 (the server's token
// cache cold, its database away) is retried after a pause, with the
// batches spooled meanwhile.
func TestUnauthorizedStopsOnlyWhenItSticks(t *testing.T) {
	sp, _ := spool.Open(t.TempDir(), 1<<20)
	sink := &fakeSink{fail: push.ErrUnauthorized}
	a := newAgent(t, sink, sp)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	a.o.Now = func() time.Time { return now }
	a.o.StateDir = t.TempDir()
	a.o.TokenFingerprint = "fp1"
	ctx := context.Background()

	if err := a.tick(ctx); err != nil {
		t.Fatalf("first rejection must not stop the agent: %v", err)
	}
	if n, _ := sp.Stats(); n != 1 {
		t.Fatalf("rejected batch should be spooled, spool has %d", n)
	}
	// Still paused: no request goes out, the batch is spooled.
	calls := sink.calls
	now = now.Add(30 * time.Second)
	if err := a.tick(ctx); err != nil || sink.calls != calls {
		t.Fatalf("paused agent asked the server (calls %d→%d, err %v)", calls, sink.calls, err)
	}
	// Rejections 2–5 at the daemon's own pace (backoff 1, 5, 15, 30 min):
	// none of them stops the agent, even though the last is 51 minutes in.
	for _, step := range []time.Duration{2, 6, 16, 31} {
		now = now.Add(step * time.Minute)
		if err := a.tick(ctx); err != nil {
			t.Fatalf("rejection at +%v must not stop the agent: %v", now.Sub(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)), err)
		}
	}
	now = now.Add(61 * time.Minute) // rejection 6 at ~+1h56m: held for over an hour, sticky
	if err := a.tick(ctx); !errors.Is(err, ErrTokenRejected) {
		t.Fatalf("a rejection held for almost two hours should stop the agent, got %v", err)
	}
	if rec, ok := ReadRejection(a.o.StateDir, "fp1"); !ok || !rec.Sticky() {
		t.Fatalf("record not sticky on disk: %+v %v", rec, ok)
	}
}

// A token that works again clears the record.
func TestAcceptedPushClearsRejection(t *testing.T) {
	a := newAgent(t, &fakeSink{}, nil)
	a.o.StateDir = t.TempDir()
	a.o.TokenFingerprint = "fp1"
	if _, err := RecordRejection(a.o.StateDir, "fp1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadRejection(a.o.StateDir, "fp1"); ok {
		t.Fatal("record survived an accepted push")
	}
}

func TestNonRetryableIsDroppedNotSpooled(t *testing.T) {
	sp, _ := spool.Open(t.TempDir(), 1<<20)
	a := newAgent(t, &fakeSink{fail: &push.StatusError{Code: 413}}, sp)
	a.tick(context.Background())
	if n, _ := sp.Stats(); n != 0 {
		t.Errorf("413 must not be spooled, got %d", n)
	}
}

func TestModuleFailureLoggedOnce(t *testing.T) {
	a := newAgent(t, &fakeSink{}, nil)
	a.note("m", errors.New("boom"))
	a.note("m", errors.New("boom"))
	if len(a.failing) != 1 {
		t.Errorf("failing = %v", a.failing)
	}
	a.note("m", nil)
	if len(a.failing) != 0 {
		t.Error("recovery should clear the failure")
	}
}

func TestServerIntervalStretchesAndReleases(t *testing.T) {
	sink := &fakeSink{interval: 60}
	a := newAgent(t, sink, nil)
	if err := a.Once(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if a.Interval() != 60*time.Second {
		t.Fatalf("interval = %s, want 60s from the server", a.Interval())
	}
	// A shorter server value never goes below the configured interval.
	sink.interval = 1
	if err := a.Once(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if a.Interval() != a.o.Interval {
		t.Fatalf("interval = %s, want the configured %s", a.Interval(), a.o.Interval)
	}
}
