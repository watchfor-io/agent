// Package agent runs the collect → encode → send loop and owns the two
// policies around it: which module failures are worth a log line, and what
// happens to a batch the endpoint would not take.
package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/modules"
	"github.com/watchfor-io/agent/internal/push"
	"github.com/watchfor-io/agent/internal/spool"
)

type Sink interface {
	Send(ctx context.Context, body []byte) (push.Ack, error)
}

type Options struct {
	Modules  []modules.Module
	Sink     Sink
	Spool    *spool.Spool // nil disables buffering
	Host     metric.Host
	Facts    func() metric.Facts
	Version  string
	Interval time.Duration
	Log      *slog.Logger
}

const (
	factsEvery    = time.Hour
	replayPerTick = 20
)

type Agent struct {
	o         Options
	interval  time.Duration // effective: the configured one, or longer if the server asks
	lastFacts time.Time
	failing   map[string]string
}

func New(o Options) *Agent {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Agent{o: o, interval: o.Interval, failing: map[string]string{}}
}

// Interval is the cadence currently in use.
func (a *Agent) Interval() time.Duration { return a.interval }

func (a *Agent) Run(ctx context.Context) error {
	if err := a.tick(ctx); err != nil {
		return err
	}
	t := time.NewTicker(a.interval)
	defer t.Stop()
	current := a.interval
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := a.tick(ctx); err != nil {
				return err
			}
			if a.interval != current {
				current = a.interval
				t.Reset(current)
			}
		}
	}
}

// applyAck stretches the cadence to the server's minimum. The configured
// interval stays the floor, so a plan upgrade brings the agent back down
// without a restart.
func (a *Agent) applyAck(ack push.Ack) {
	want := max(a.o.Interval, time.Duration(ack.Interval)*time.Second)
	if want == a.interval {
		return
	}
	if want > a.o.Interval {
		a.o.Log.Info("interval set by the server", "interval", want.String(), "configured", a.o.Interval.String())
	} else {
		a.o.Log.Info("interval back to the configured value", "interval", want.String())
	}
	a.interval = want
}

// Once is the cron mode: rates need two samples, so it collects, waits gap,
// collects again and sends only the second batch. The priming pass never
// becomes a payload, so the one batch that is sent carries the facts.
func (a *Agent) Once(ctx context.Context, gap time.Duration) error {
	a.collect(ctx)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(gap):
	}
	return a.send(ctx, a.Collect(ctx))
}

func (a *Agent) tick(ctx context.Context) error {
	err := a.send(ctx, a.Collect(ctx))
	if errors.Is(err, push.ErrUnauthorized) {
		return err
	}
	return nil
}

// Collect runs every module once and wraps the samples as a payload.
func (a *Agent) Collect(ctx context.Context) *metric.Payload {
	b := a.collect(ctx)
	p := &metric.Payload{
		Version: metric.PayloadVersion,
		Agent:   a.o.Version,
		Host:    a.o.Host,
		Samples: b.Samples(),
	}
	if now := time.Now(); a.o.Facts != nil && now.Sub(a.lastFacts) >= factsEvery {
		f := a.o.Facts()
		p.Facts = &f
		a.lastFacts = now
	}
	return p
}

func (a *Agent) collect(ctx context.Context) *metric.Batch {
	b := metric.NewBatch(time.Now())
	budget := max(a.interval/2, 2*time.Second)
	for _, m := range a.o.Modules {
		mctx, cancel := context.WithTimeout(ctx, budget)
		err := m.Collect(mctx, b)
		cancel()
		a.note(m.Name(), err)
	}
	return b
}

// note logs a module failure once, and its recovery once, instead of once
// per tick — a permission problem should be one line, not one every 15s.
func (a *Agent) note(module string, err error) {
	if err == nil {
		if _, was := a.failing[module]; was {
			delete(a.failing, module)
			a.o.Log.Info("module recovered", "module", module)
		}
		return
	}
	if a.failing[module] != err.Error() {
		a.failing[module] = err.Error()
		a.o.Log.Warn("module failed", "module", module, "error", err)
	}
}

func (a *Agent) send(ctx context.Context, p *metric.Payload) error {
	body, err := push.Encode(p)
	if err != nil {
		return err
	}
	ack, err := a.o.Sink.Send(ctx, body)
	if err != nil {
		switch {
		case errors.Is(err, push.ErrUnauthorized):
			a.o.Log.Error("token rejected, stopping", "error", err)
			return err
		case push.Retryable(err) && a.o.Spool != nil:
			if serr := a.o.Spool.Put(body); serr != nil {
				a.o.Log.Error("send failed and spool refused the batch", "error", err, "spool", serr)
			} else {
				a.o.Log.Warn("send failed, batch spooled", "error", err, "samples", len(p.Samples))
			}
		default:
			a.o.Log.Error("batch dropped", "error", err, "samples", len(p.Samples))
		}
		return nil
	}
	a.applyAck(ack)
	a.replay(ctx)
	return nil
}

func (a *Agent) replay(ctx context.Context) {
	if a.o.Spool == nil {
		return
	}
	for i := 0; i < replayPerTick; i++ {
		name, body, err := a.o.Spool.Next()
		if err != nil || name == "" {
			return
		}
		if _, err := a.o.Sink.Send(ctx, body); err != nil {
			if !push.Retryable(err) {
				a.o.Log.Error("spooled batch dropped", "error", err)
				a.o.Spool.Remove(name)
				continue
			}
			return
		}
		a.o.Spool.Remove(name)
	}
}
