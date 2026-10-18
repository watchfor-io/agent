//go:build linux

package agent

import (
	"os"
	"strings"
	"time"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/procfs"
)

// selfUsage is the agent's own CPU time at the last look.
type selfUsage struct {
	ticks uint64
	at    time.Time
}

// footprint adds what the agent itself costs to every batch —
// agent.rss_bytes, agent.cpu_pct (from the second look on) and
// agent.spool_batches — so "is the agent the problem?" is answered by the
// agent's own series, not by guessing from the process list. A nil batch
// only takes the first look (Once's priming pass).
func (a *Agent) footprint(b *metric.Batch) {
	p, err := procfs.ReadProcess(os.Getpid())
	if err != nil {
		return
	}
	now := a.o.Now()
	prev := a.self
	a.self = selfUsage{ticks: p.CPUTicks(), at: now}
	if b == nil {
		return
	}
	b.Add("agent.rss_bytes", float64(p.RSSBytes))
	if elapsed := now.Sub(prev.at).Seconds(); !prev.at.IsZero() && elapsed > 0 && p.CPUTicks() >= prev.ticks {
		b.Add("agent.cpu_pct", float64(p.CPUTicks()-prev.ticks)/procfs.ClockTicks/elapsed*100)
	}
	if a.o.Spool != nil && a.o.StateDir != "" {
		b.Add("agent.spool_batches", float64(spooled(a.o.StateDir)))
	}
}

// spooled counts the batches waiting by name alone — no stat per file,
// since it runs every tick and an outage can leave thousands.
func spooled(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if name := e.Name(); !e.IsDir() && strings.HasSuffix(name, ".gz") && !strings.HasPrefix(name, ".") {
			n++
		}
	}
	return n
}
