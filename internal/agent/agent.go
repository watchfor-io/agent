// Package agent runs the collect → encode → send loop and owns the two
// policies around it: which module failures are worth a log line, and what
// happens to a batch the endpoint would not take.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/modules"
	"github.com/watchfor-io/agent/internal/push"
	"github.com/watchfor-io/agent/internal/spool"
	"github.com/watchfor-io/agent/internal/update"
)

// Sink is where an encoded batch goes: the push client in production, and
// stdout for `check`, which prints the batch instead of sending it.
type Sink interface {
	Send(ctx context.Context, body []byte) (push.Ack, error)
}

// Options wire an Agent together: the modules to run, where batches go,
// the spool that keeps them when they cannot, and the host identity that
// heads every payload.
type Options struct {
	Modules  []modules.Module
	Sink     Sink
	Spool    *spool.Spool // nil disables buffering
	Host     metric.Host
	Facts    func() metric.Facts
	Version  string
	Interval time.Duration
	// StateDir is where a newer-release hint is left for `upgrade` (the
	// spool directory). Empty disables the file; the log line still appears.
	StateDir string
	// TokenFingerprint identifies the token for the rejected-token record.
	TokenFingerprint string
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
	Log *slog.Logger
}

const (
	factsEvery    = time.Hour
	replayPerTick = 4 // a burst after an outage must stay under the server's per-host budget
)

// Agent is one host's collect → encode → send loop, plus the state the
// loop's policies need: which modules are failing, which update was
// already mentioned, and whether a rejected token is being waited out.
type Agent struct {
	o         Options
	interval  time.Duration // effective: the configured one, or longer if the server asks
	lastFacts time.Time
	failing   map[string]string
	// updateNoted is the newer version already logged, so the warning
	// appears once per release, not once per push.
	updateNoted  string
	stateCleared bool
	accepted     bool // first successful push seen: any rejected-token record is stale
	// pauseUntil: after a token rejection the agent waits before asking
	// again (batches are spooled meanwhile); ErrTokenRejected is returned
	// only once the rejection has held long enough to be final.
	pauseUntil time.Time

	// dropNoted is the last dropped count the log was told about, and when.
	dropNoted   int
	dropNotedAt time.Time
}

// ErrTokenRejected is returned by Run/Once once the server has rejected
// the token repeatedly over a span of time: the host was removed in
// WatchFor or its token was rotated, and the agent should stop.
var ErrTokenRejected = errors.New("token rejected by the server repeatedly")

// New prepares an Agent from o, filling in slog's default logger and the
// real clock when they are left nil.
func New(o Options) *Agent {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Agent{o: o, interval: o.Interval, failing: map[string]string{}}
}

// Interval is the cadence currently in use.
func (a *Agent) Interval() time.Duration { return a.interval }

// Run is the daemon mode: one tick at once, then one per interval, the
// ticker following the server's cadence when it changes. It returns nil
// when ctx ends and ErrTokenRejected when the agent should stop.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.tick(ctx); err != nil {
		return err
	}
	if a.interval <= 0 {
		return errors.New("agent: the interval must be positive")
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
	if errors.Is(err, ErrTokenRejected) {
		return err
	}
	return nil
}

// rejected handles a 401/403 from the server. One rejection can be the
// server having a bad minute (its token cache cold, its database away),
// so the agent records it, spools the batch and waits with backoff; only
// a rejection that holds across several attempts and minutes is final.
func (a *Agent) rejected(err error, body []byte) error {
	now := a.o.Now()
	rec, rerr := RecordRejection(a.o.StateDir, a.o.TokenFingerprint, now)
	if rerr != nil {
		a.o.Log.Warn("could not record the rejected token; the stop-after-repeats rule cannot hold", "error", rerr)
	}
	if rec.Sticky() {
		a.o.Log.Error("stopping: the server has rejected this host's token repeatedly",
			"error", err, "rejections", rec.Count, "since", rec.First.Format(time.RFC3339),
			"why", "the host was removed in WatchFor or its token was rotated",
			"fix", "put the new token in place and restart, or stop the agent: sudo systemctl disable --now watchfor-agent")
		return ErrTokenRejected
	}
	wait := rec.Backoff()
	a.pauseUntil = now.Add(wait)
	if a.o.Spool != nil {
		_ = a.o.Spool.Put(body)
	}
	a.o.Log.Warn("token rejected by the server; will try again",
		"error", err, "attempt", rec.Count, "of", StickyRejections, "retry_in", wait.String(),
		"note", "if the host was removed in WatchFor or its token rotated, the agent stops by itself once the rejection has held for over "+StickySpan.String())
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
	if now := a.o.Now(); a.o.Facts != nil && now.Sub(a.lastFacts) >= factsEvery {
		f := a.o.Facts()
		shrinkFacts(&f, factsMaxBytes)
		p.Facts = &f
		a.lastFacts = now
		a.o.Log.Debug("host facts collected", "cpu", f.CPUModel, "cores", f.CPUCores, "hardware", f.Hardware,
			"virtualization", f.Virtualization, "primary_address", f.PrimaryAddress, "interfaces", len(f.Addresses))
	}
	return p
}

func (a *Agent) collect(ctx context.Context) *metric.Batch {
	b := metric.NewBatch(a.o.Now())
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
	if now := a.o.Now(); now.Before(a.pauseUntil) {
		// Waiting out a token rejection or a Retry-After: keep the batch,
		// do not ask yet.
		if a.o.Spool != nil {
			_ = a.o.Spool.Put(body)
		}
		return nil
	}
	ack, err := a.o.Sink.Send(ctx, body)
	if err != nil {
		switch {
		case errors.Is(err, push.ErrUnauthorized):
			return a.rejected(err, body)
		case push.Retryable(err) && a.o.Spool != nil:
			if wait := push.RetryAfter(err); wait > 0 {
				a.pauseUntil = a.o.Now().Add(wait) // the server said when; the spool holds the meantime
				a.o.Log.Debug("server asked for a pause", "wait", wait)
			}
			if serr := a.o.Spool.Put(body); serr != nil {
				a.o.Log.Error("send failed and spool refused the batch", "error", err, "spool", serr)
			} else {
				if ctx.Err() != nil { // shutdown or reload, not the server
					a.o.Log.Debug("send cut short, batch spooled", "samples", len(p.Samples))
				} else {
					a.o.Log.Warn("send failed, batch spooled", "error", err, "samples", len(p.Samples))
				}
			}
		default:
			a.o.Log.Error("batch dropped", "error", err, "samples", len(p.Samples))
		}
		return nil
	}
	a.o.Log.Debug("batch accepted", "samples", len(p.Samples), "bytes", len(body), "facts", p.Facts != nil, "latest_version", ack.LatestVersion)
	a.noteDropped(ack, len(p.Samples))
	a.applyAck(ack)
	a.noteUpdate(ack.LatestVersion)
	if !a.accepted {
		a.accepted = true
		ClearRejected(a.o.StateDir)
	}
	a.replay(ctx)
	return nil
}

// noteUpdate acts on the server's "latest release" hint: one warning per
// newer version, plus the state file `watchfor-agent upgrade` reads. A
// development build is never nagged. When the server stops reporting a
// newer version (this host was upgraded by hand), the stale hint goes.
func (a *Agent) noteUpdate(latest string) {
	if latest == "" {
		return // the server had no opinion; what is recorded stays
	}
	cur, err := update.Parse(a.o.Version)
	if err != nil {
		return
	}
	lv, err := update.Parse(latest)
	if err != nil || update.Compare(lv, cur) <= 0 {
		if !a.stateCleared {
			a.stateCleared = true
			_ = update.ClearState(a.o.StateDir)
		}
		return
	}
	if a.updateNoted != lv.String() {
		a.updateNoted = lv.String()
		a.o.Log.Warn("update available", "running", a.o.Version, "latest", lv.String(), "how", "sudo watchfor-agent upgrade")
	}
	if a.o.StateDir != "" {
		if cur, _, ok := update.ReadState(a.o.StateDir); ok && cur == lv.String() {
			return // the timer already knows
		}
		if err := update.WriteState(a.o.StateDir, lv.String()); err != nil {
			a.o.Log.Debug("could not record the update hint", "dir", a.o.StateDir, "error", err)
		} else {
			a.stateCleared = false // a later "nothing newer" must clear it again
		}
	}
}

func (a *Agent) replay(ctx context.Context) {
	if a.o.Spool == nil {
		return
	}
	for i := 0; i < replayPerTick; i++ {
		name, body, err := a.o.Spool.Next()
		if err != nil {
			if name != "" {
				a.o.Log.Warn("spooled batch unreadable; dropped", "file", name, "error", err)
				a.forget(name)
			}
			return
		}
		if name == "" {
			return
		}
		if _, err := a.o.Sink.Send(ctx, body); err != nil {
			if errors.Is(err, push.ErrUnauthorized) {
				return // the token is the problem, not this batch; tick() decides what happens next
			}
			if !push.Retryable(err) {
				a.o.Log.Error("spooled batch dropped", "error", err)
				a.forget(name)
				continue
			}
			return
		}
		a.forget(name)
	}
}

// forget removes a spooled batch that has been dealt with. A file that
// will not go is said so: it would be sent again on the next replay.
func (a *Agent) forget(name string) {
	if err := a.o.Spool.Remove(name); err != nil {
		a.o.Log.Warn("spooled batch could not be removed; it may be sent twice", "file", name, "error", err)
	}
}

// noteDropped says when the server took the batch but kept nothing of it
// — a clock that is off is the usual cause, and nothing else would tell
// anyone. Said once, then again when the count changes or every ten
// minutes, so a stuck clock is not a log storm.
func (a *Agent) noteDropped(ack push.Ack, sent int) {
	if ack.Dropped == 0 || sent == 0 {
		a.dropNoted = 0
		return
	}
	now := a.o.Now()
	if ack.Dropped == a.dropNoted && now.Sub(a.dropNotedAt) < 10*time.Minute {
		return
	}
	a.dropNoted, a.dropNotedAt = ack.Dropped, now
	if ack.Accepted == 0 {
		a.o.Log.Error("the server accepted the batch but kept none of its samples — this machine's clock is probably off", "sent", sent, "dropped", ack.Dropped)
		return
	}
	a.o.Log.Warn("the server dropped some samples", "sent", sent, "kept", ack.Accepted, "dropped", ack.Dropped)
}

// factsMaxBytes is what the server keeps of a facts document; a bigger one
// is thrown away whole there, CPU and memory included, so it is trimmed
// here first.
const factsMaxBytes = 4000

// shrinkFacts keeps the facts document under limit by giving up addresses:
// first the extra addresses of every interface but the primary one, then
// the other interfaces altogether. The primary interface always stays.
func shrinkFacts(f *metric.Facts, limit int) {
	size := func() int {
		b, _ := json.Marshal(f)
		return len(b)
	}
	if size() <= limit {
		return
	}
	for name, addrs := range f.Addresses {
		if name != f.PrimaryIface && len(addrs) > 1 {
			f.Addresses[name] = addrs[:1]
		}
	}
	if size() <= limit {
		return
	}
	for name := range f.Addresses {
		if name != f.PrimaryIface {
			delete(f.Addresses, name)
		}
	}
	if size() <= limit {
		return
	}
	if addrs := f.Addresses[f.PrimaryIface]; len(addrs) > 2 {
		f.Addresses[f.PrimaryIface] = addrs[:2]
	}
}
