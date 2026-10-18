// Package agent runs the collect → encode → send loop and owns the two
// policies around it: which module failures are worth a log line, and what
// happens to a batch the endpoint would not take.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
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
	factsEvery = time.Hour
	// backoffCap is the longest the agent waits between attempts while
	// the server keeps failing; the spool holds the batches meanwhile.
	backoffCap = 5 * time.Minute
	// stallGrace is what a round may take beyond two intervals before it
	// counts as stuck: every module's budget, a push and a replay burst at
	// their timeouts fit well inside it.
	stallGrace = 10 * time.Minute
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

	// failures counts transient send failures in a row; from the second
	// one on, the agent waits an exponentially longer, jittered time
	// before asking again, so thousands of hosts do not hit a recovering
	// server in lockstep every tick.
	failures int
	jitter   func(time.Duration) time.Duration

	// roundStart is when the round in progress began (unix ns, 0 when
	// idle) and roundLimit how long it may take; both are read by the
	// stall guard on another goroutine.
	roundStart atomic.Int64
	roundLimit atomic.Int64

	// pushState is the last answer recorded in PushStateFile.
	pushState string
	// planPauses counts plan_forbidden answers in a row, for their
	// backoff; pauseDrop says the pause in force is one where batches are
	// not kept (the plan does not take them).
	planPauses int
	pauseDrop  bool
	// lastSent is whether the last batch sent was accepted, for Once;
	// deferred says it was kept back on purpose (a pause in force).
	lastSent bool
	deferred bool

	self selfUsage // the agent's own CPU time at the last tick
}

// ErrTokenRejected is returned by Run/Once once the server has rejected
// the token repeatedly over a span of time: the host was removed in
// WatchFor or its token was rotated, and the agent should stop.
var ErrTokenRejected = errors.New("token rejected by the server repeatedly")

// ErrNotAccepted is Once's answer when the batch did not get through: the
// server was unreachable or refused it. The batch is in the spool when
// there is one; the exit status tells cron (and whoever reads its mail).
var ErrNotAccepted = errors.New("the batch was not accepted by the server")

// New prepares an Agent from o, filling in slog's default logger and the
// real clock when they are left nil.
func New(o Options) *Agent {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	a := &Agent{o: o, interval: o.Interval, failing: map[string]string{}}
	a.jitter = func(d time.Duration) time.Duration { return d/2 + rand.N(d/2+1) } // #nosec G404 -- spreading retries, not a secret
	a.roundLimit.Store(int64(2*o.Interval + stallGrace))
	return a
}

// Stalled reports a round that has been running longer than it possibly
// can when it is making progress — a module stuck in a syscall on a dead
// NFS mount, a deadlock — and for how long. The daemon's stall guard exits
// on it so the service manager starts a fresh agent instead of leaving a
// process that looks alive and sends nothing.
func (a *Agent) Stalled(now time.Time) (time.Duration, bool) {
	start := a.roundStart.Load()
	if start == 0 {
		return 0, false
	}
	running := now.Sub(time.Unix(0, start))
	return running, running > time.Duration(a.roundLimit.Load())
}

// round brackets one collect-and-send for Stalled.
func (a *Agent) round() func() {
	a.roundLimit.Store(int64(2*a.interval + stallGrace))
	a.roundStart.Store(a.o.Now().UnixNano())
	return func() { a.roundStart.Store(0) }
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

// maxServerInterval caps how far the server may stretch the cadence. No
// plan asks for more than a few minutes; a bad or hostile answer ("push
// once a year") must not quietly turn the agent into a silent one.
const maxServerInterval = time.Hour

// serverInterval is the cadence the server asked for, clamped to
// 0…maxServerInterval (in seconds first, so a huge value cannot overflow
// time.Duration into a negative one).
func serverInterval(seconds int) time.Duration {
	return time.Duration(min(max(seconds, 0), int(maxServerInterval/time.Second))) * time.Second
}

// applyAck stretches the cadence to the server's minimum, capped at
// maxServerInterval. The configured interval stays the floor, so a plan
// upgrade brings the agent back down without a restart.
func (a *Agent) applyAck(ack push.Ack) {
	want := max(a.o.Interval, serverInterval(ack.Interval))
	if want == a.interval {
		return
	}
	if ack.Interval > int(maxServerInterval/time.Second) { // said once per change, not per push
		a.o.Log.Warn("the server asked for a longer interval than the agent allows; using the cap",
			"asked_s", ack.Interval, "cap", maxServerInterval.String())
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
//
// A pause the server asked for (Retry-After, a rejected token's backoff, a
// plan pause, the backoff while it fails) outlives the process: it is
// read from the state folder at the start and written back at the end,
// so cron's next run honours it. A run that kept its batch back for such
// a pause has done what it should and returns nil.
func (a *Agent) Once(ctx context.Context, gap time.Duration) error {
	defer a.round()()
	a.loadPause()
	defer a.savePause()
	a.collect(ctx)
	a.footprint(nil)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(gap):
	}
	if err := a.send(ctx, a.Collect(ctx)); err != nil {
		return err
	}
	switch {
	case a.lastSent:
		return nil
	case a.deferred:
		a.o.Log.Debug("a pause is in force; the batch waits", "until", a.pauseUntil.Format(time.RFC3339), "dropped", a.pauseDrop)
		return nil
	}
	return ErrNotAccepted
}

func (a *Agent) tick(ctx context.Context) error {
	defer a.round()()
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
func (a *Agent) rejected(err error, bodies [][]byte) error {
	now := a.o.Now()
	reason := push.Reason(err)
	rec, rerr := RecordRejection(a.o.StateDir, a.o.TokenFingerprint, reason, now)
	if rerr != nil {
		a.o.Log.Warn("could not record the rejected token; the stop-after-repeats rule cannot hold", "error", rerr)
	}
	if rec.Sticky() {
		a.o.Log.Error("stopping: the server has rejected this host's token repeatedly",
			"error", err, "reason", rec.Reason, "rejections", rec.Count, "since", rec.First.Format(time.RFC3339),
			"why", rec.Why(),
			"fix", "put the new token in place and restart, or stop the agent: sudo systemctl disable --now watchfor-agent")
		return ErrTokenRejected
	}
	wait := rec.Backoff()
	a.pauseUntil = now.Add(wait)
	a.keep(bodies)
	a.o.Log.Warn("token rejected by the server; will try again",
		"error", err, "reason", rec.Reason, "why", rec.Why(), "attempt", rec.Count, "of", StickyRejections, "retry_in", wait.String(),
		"note", "if the rejection holds for over "+StickySpan.String()+" the agent stops by itself (exit 3)")
	return nil
}

// Collect runs every module once and wraps the samples as a payload.
func (a *Agent) Collect(ctx context.Context) *metric.Payload {
	b := a.collect(ctx)
	a.footprint(b)
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
	bodies, err := encodeChunks(p, maxBatchSamples, maxBatchBytes)
	if err != nil {
		return err
	}
	a.lastSent, a.deferred = false, false
	if now := a.o.Now(); now.Before(a.pauseUntil) {
		// Waiting out a token rejection, a Retry-After or a backoff: keep
		// the batch, do not ask yet. A plan pause keeps nothing.
		a.deferred = true
		if !a.pauseDrop {
			a.keep(bodies)
		}
		return nil
	}
	a.pauseDrop = false
	accepted, splits := 0, 0
	for i := 0; i < len(bodies); i++ {
		body := bodies[i]
		ack, err := a.o.Sink.Send(ctx, body)
		if err == nil {
			accepted++
			a.noteAccepted(ack, body)
			continue
		}
		switch {
		case push.TooLarge(err):
			// Over ingest's per-batch limit: halve it and send the halves.
			if halves, ok := splitBody(body); ok {
				a.o.Log.Debug("the server found the batch too large; sending it in halves", "error", err)
				bodies = append(bodies[:i+1:i+1], append(halves, bodies[i+1:]...)...)
				splits++
				continue
			}
			a.notePush(PushRefused)
			a.o.Log.Error("batch dropped: a single sample is over the server's size limit", "error", err)
			continue
		case errors.Is(err, push.ErrUnauthorized):
			a.notePush(PushTokenRejected)
			return a.rejected(err, bodies[i:])
		case errors.Is(err, push.ErrPlanPaused):
			a.planPaused(err)
			return nil
		case push.Retryable(err):
			a.failed(ctx, err, bodies[i:], len(p.Samples))
			return nil
		default:
			a.notePush(PushRefused)
			a.o.Log.Error("batch dropped", "error", err, "samples", len(p.Samples))
			continue
		}
	}
	if accepted == 0 {
		return nil
	}
	a.lastSent = accepted == len(bodies)-splits
	a.failures = 0
	if a.planPauses > 0 {
		a.planPauses = 0
		a.o.Log.Info("WatchFor takes this host's batches again")
	}
	a.notePush(PushAccepted)
	a.o.Log.Debug("batch accepted", "samples", len(p.Samples), "requests", len(bodies), "facts", p.Facts != nil)
	if !a.accepted {
		a.accepted = true
		ClearRejected(a.o.StateDir)
	}
	a.replay(ctx)
	return nil
}

// noteAccepted acts on one accepted request's answer.
func (a *Agent) noteAccepted(ack push.Ack, body []byte) {
	a.o.Log.Debug("request accepted", "bytes", len(body), "kept", ack.Accepted, "latest_version", ack.LatestVersion)
	a.noteDropped(ack, ack.Accepted+ack.Dropped)
	a.applyAck(ack)
	a.noteUpdate(ack.LatestVersion)
}

// failed handles a transient failure: the server's Retry-After, the
// backoff, and the spool for what was not sent.
func (a *Agent) failed(ctx context.Context, err error, bodies [][]byte, samples int) {
	if wait := push.RetryAfter(err); wait > 0 {
		a.pauseUntil = a.o.Now().Add(wait) // the server said when; the spool holds the meantime
		a.o.Log.Debug("server asked for a pause", "wait", wait)
	}
	if ctx.Err() != nil { // shutdown or reload, not the server
		if a.keep(bodies) == nil && a.o.Spool != nil {
			a.o.Log.Debug("send cut short, batch spooled", "samples", samples)
		}
		return
	}
	a.backoff()
	a.notePush(PushNoAnswer)
	if loc, ok := push.Redirect(err); ok {
		a.o.Log.Warn("the server answered with a redirect, which the agent never follows (the token goes only where agent.yml says); check server.url",
			"location", loc)
	}
	switch serr := a.keep(bodies); {
	case a.o.Spool == nil:
		a.o.Log.Warn("send failed; no spool, batch dropped", "error", err, "samples", samples)
	case serr != nil:
		a.o.Log.Error("send failed and spool refused the batch", "error", err, "spool", serr)
	default:
		a.o.Log.Warn("send failed, batch spooled", "error", err, "samples", samples)
	}
}

// keep spools bodies, oldest first; the error is the first Put that
// failed (nil without a spool: nothing to report there). Batches the
// spool gave up to make room are said once per call.
func (a *Agent) keep(bodies [][]byte) error {
	if a.o.Spool == nil {
		return nil
	}
	var first error
	for _, b := range bodies {
		if err := a.o.Spool.Put(b); err != nil && first == nil {
			first = err
		}
	}
	if n := a.o.Spool.TakeDropped(); n > 0 {
		a.o.Log.Warn("spool full: dropped the oldest batches to keep the newest", "dropped", n)
	}
	return first
}

// backoff counts a transient failure and, from the second in a row,
// pauses sending: the interval doubled per failure, capped at backoffCap,
// jittered down to half so the hosts that lost the server together do
// not come back together. A longer Retry-After the server sent stands.
func (a *Agent) backoff() {
	a.failures++
	if a.failures < 2 || a.interval <= 0 {
		return
	}
	wait := backoffCap
	if shift := a.failures - 2; shift < 16 {
		wait = min(a.interval<<shift, backoffCap)
	}
	wait = a.jitter(wait)
	if until := a.o.Now().Add(wait); until.After(a.pauseUntil) {
		a.pauseUntil = until
		a.o.Log.Debug("server unavailable; backing off", "failures", a.failures, "wait", wait.String())
	}
}

// planPaused handles ingest's plan_forbidden: the token is valid (so any
// rejected-token record is stale), the plan does not take this host now.
// WatchFor restores paused hosts by itself when the limit allows, so the
// agent never gives up: it asks again after 1, 5, 15, then every 30
// minutes, jittered, and drops the batches meanwhile — a plan that does
// not take the host would not take its backlog either.
func (a *Agent) planPaused(err error) {
	a.planPauses++
	if a.planPauses == 1 {
		ClearRejected(a.o.StateDir)
	}
	a.notePush(PushPlanPaused)
	wait := 30 * time.Minute
	switch a.planPauses {
	case 1:
		wait = time.Minute
	case 2:
		wait = 5 * time.Minute
	case 3:
		wait = 15 * time.Minute
	}
	wait = a.jitter(wait)
	a.pauseUntil, a.pauseDrop = a.o.Now().Add(wait), true
	log := a.o.Log.Debug
	if a.planPauses == 1 || a.planPauses%48 == 0 { // once, then about once a day
		log = a.o.Log.Warn
	}
	log("WatchFor paused this host: the plan does not include server monitoring, or the organization is over its host limit",
		"error", err, "retry_in", wait.Round(time.Second).String(),
		"fix", "raise the plan or remove another host in WatchFor; the agent picks up by itself, no restart needed")
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

// replay sends spooled batches after a successful push: as many requests
// as the server's per-host budget leaves (replayBudget), each one merging
// consecutive spooled batches up to the per-request limits — a day of
// one-minute batches goes in a few minutes, not in hours.
func (a *Agent) replay(ctx context.Context) {
	if a.o.Spool == nil {
		return
	}
	single := false // after a merged request was refused, one batch at a time
	for range a.replayBudget() {
		names, body := a.nextReplay(single)
		if len(names) == 0 {
			return
		}
		_, err := a.o.Sink.Send(ctx, body)
		switch {
		case err == nil:
			for _, n := range names {
				a.forget(n)
			}
		case errors.Is(err, push.ErrUnauthorized) || errors.Is(err, push.ErrPlanPaused):
			return // the token or the plan is the problem, not this batch; the next tick decides
		case push.TooLarge(err) && len(names) > 1:
			single = true
		case push.TooLarge(err):
			a.splitSpooled(names[0], body)
		case push.Retryable(err):
			return
		case len(names) > 1:
			single = true // find the batch the server objects to
		default:
			a.o.Log.Error("spooled batch dropped", "error", err)
			a.forget(names[0])
		}
	}
}

// nextReplay reads the oldest spooled batches and merges those that fit
// one request (same host, at most maxBatchSamples samples and about
// maxBatchBytes). A single batch goes out byte for byte as it was spooled.
// Unreadable or corrupt files are dropped on the way; a batch over the
// limits by itself (spooled by an older release) is split in place first.
func (a *Agent) nextReplay(single bool) (names []string, body []byte) {
	var merged *metric.Payload
	var first []byte
	size := 0
	for _, n := range a.o.Spool.Oldest(replayScan) {
		b, err := a.o.Spool.Read(n)
		if err != nil {
			a.o.Log.Warn("spooled batch unreadable; dropped", "file", n, "error", err)
			a.forget(n)
			continue
		}
		p, err := push.Decode(b)
		if err != nil {
			a.o.Log.Warn("spooled batch is not a batch; dropped", "file", n, "error", err)
			a.forget(n)
			continue
		}
		if merged == nil {
			if len(p.Samples) > maxBatchSamples || len(b) > maxBatchBytes {
				if parts, err := encodeChunks(p, maxBatchSamples, maxBatchBytes); err == nil && len(parts) > 1 {
					if err := a.o.Spool.Split(n, parts); err == nil {
						return a.nextReplay(single)
					}
				}
				return []string{n}, b
			}
			if single {
				return []string{n}, b
			}
			merged, first, size = p, b, len(b)
			names = append(names, n)
			continue
		}
		if p.Host.ID != merged.Host.ID || p.Host.Name != merged.Host.Name ||
			len(merged.Samples)+len(p.Samples) > maxBatchSamples || size+len(b) > maxBatchBytes {
			break
		}
		merged.Samples = append(merged.Samples, p.Samples...)
		merged.Agent, merged.Host = p.Agent, p.Host
		if p.Facts != nil {
			merged.Facts = p.Facts
		}
		size += len(b)
		names = append(names, n)
	}
	if len(names) <= 1 {
		return names, first
	}
	body, err := push.Encode(merged)
	if err != nil {
		return names[:1], first
	}
	return names, body
}

// splitSpooled replaces a spooled batch the server found too large with
// its two halves, in place; one that cannot be split is dropped.
func (a *Agent) splitSpooled(name string, body []byte) {
	halves, ok := splitBody(body)
	if !ok {
		a.o.Log.Error("spooled batch dropped: a single sample is over the server's size limit", "file", name)
		a.forget(name)
		return
	}
	if err := a.o.Spool.Split(name, halves); err != nil {
		a.o.Log.Warn("could not split a spooled batch the server found too large; dropped", "file", name, "error", err)
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
