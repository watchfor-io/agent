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
}

func (s *fakeSink) Send(_ context.Context, body []byte) (push.Ack, error) {
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

func TestUnauthorizedStops(t *testing.T) {
	sink := &fakeSink{fail: push.ErrUnauthorized}
	a := newAgent(t, sink, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.Run(ctx); !errors.Is(err, push.ErrUnauthorized) {
		t.Errorf("Run should return the auth error, got %v", err)
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
