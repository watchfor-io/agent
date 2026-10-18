package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/modules"
	"github.com/watchfor-io/agent/internal/push"
	"github.com/watchfor-io/agent/internal/spool"
)

// manyModule adds n samples a tick — a host with a thousand interfaces.
type manyModule struct{ n int }

func (m *manyModule) Name() string { return "many" }
func (m *manyModule) Collect(_ context.Context, b *metric.Batch) error {
	for i := range m.n {
		b.Add("net.rx_bytes_per_s", float64(i), metric.Labels{"if": fmt.Sprintf("cali%05d", i)})
	}
	return nil
}

// ingestSink refuses, like ingest, a request of more than limit samples
// with 413, and records what it took.
type ingestSink struct {
	limit    int
	requests []int
	refused  int
}

func (s *ingestSink) Send(_ context.Context, body []byte) (push.Ack, error) {
	p, err := push.Decode(body)
	if err != nil {
		return push.Ack{}, err
	}
	if len(p.Samples) > s.limit {
		s.refused++
		return push.Ack{}, &push.StatusError{Code: 413, Body: "too many samples in one batch"}
	}
	s.requests = append(s.requests, len(p.Samples))
	return push.Ack{Accepted: len(p.Samples)}, nil
}

func (s *ingestSink) total() (n int) {
	for _, r := range s.requests {
		n += r
	}
	return n
}

// QA found 1300 interfaces make a 10 472-sample batch, which ingest
// refuses with 413 — and the agent dropped it. It is now sent in requests
// within the limit, and a 413 anyway (a server with a lower limit) halves
// the request instead of dropping it.
func TestBigBatchesAreSplitNotDropped(t *testing.T) {
	sink := &ingestSink{limit: 10000}
	a := New(Options{Modules: []modules.Module{&manyModule{n: 10472}}, Sink: sink, Version: "t", Interval: time.Minute})
	if err := a.send(context.Background(), a.Collect(context.Background())); err != nil {
		t.Fatal(err)
	}
	if sink.refused != 0 || len(sink.requests) < 3 || sink.total() < 10472 || !a.lastSent {
		t.Fatalf("requests %v, refused %d", sink.requests, sink.refused)
	}
	for _, r := range sink.requests {
		if r > maxBatchSamples {
			t.Fatalf("a request of %d samples", r)
		}
	}

	low := &ingestSink{limit: 1000}
	a = New(Options{Modules: []modules.Module{&manyModule{n: 4000}}, Sink: low, Version: "t", Interval: time.Minute})
	if err := a.send(context.Background(), a.Collect(context.Background())); err != nil {
		t.Fatal(err)
	}
	if low.total() < 4000 || low.refused == 0 || !a.lastSent {
		t.Fatalf("with a 1000-sample server: requests %v, refused %d", low.requests, low.refused)
	}
}

// A spooled batch an older release made too big is split in place and
// delivered; small spooled batches go many to a request.
func TestReplaySplitsOldBigBatchesAndMergesSmallOnes(t *testing.T) {
	sp, _ := spool.Open(t.TempDir(), 64<<20)
	big := &metric.Payload{Version: metric.PayloadVersion, Host: metric.Host{ID: "h", Name: "test"}}
	for i := range 10472 {
		big.Samples = append(big.Samples, metric.Sample{T: 1, M: "net.rx_bytes_per_s", V: float64(i)})
	}
	body, _ := push.Encode(big)
	if err := sp.Put(body); err != nil {
		t.Fatal(err)
	}
	small := &metric.Payload{Version: metric.PayloadVersion, Host: metric.Host{ID: "h", Name: "test"},
		Samples: make([]metric.Sample, 124)}
	for range 100 {
		b, _ := push.Encode(small)
		if err := sp.Put(b); err != nil {
			t.Fatal(err)
		}
	}
	sink := &ingestSink{limit: 10000}
	a := newAgent(t, sink, sp)
	a.interval = time.Minute
	for i := 0; i < 5; i++ {
		a.replay(context.Background())
	}
	if n, _ := sp.Stats(); n != 0 {
		t.Fatalf("%d spooled batches left after 5 ticks", n)
	}
	if sink.refused != 0 || sink.total() != 10472+100*124 {
		t.Fatalf("delivered %d samples in %v (refused %d)", sink.total(), sink.requests, sink.refused)
	}
	if len(sink.requests) > 8 {
		t.Fatalf("%d requests for 101 spooled batches: not merged", len(sink.requests))
	}
}

// The replay budget stays inside ingest's per-host allowance:
// 3×(batches a minute)+2, at most 60, one of them the regular push.
func TestReplayBudget(t *testing.T) {
	for sec, want := range map[int]int{5: 2, 10: 2, 15: 2, 30: 3, 60: 4, 120: 4, 3600: 4} {
		a := New(Options{Interval: time.Duration(sec) * time.Second})
		got := a.replayBudget()
		if got != want {
			t.Errorf("interval %ds: budget %d, want %d", sec, got, want)
		}
		perMin := min(60, 3*((60+sec-1)/sec)+2)
		ticksPerMin := max(60/sec, 1)
		if (got+1)*ticksPerMin > perMin {
			t.Errorf("interval %ds: %d requests a minute, ingest allows %d", sec, (got+1)*ticksPerMin, perMin)
		}
	}
}
