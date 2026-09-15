//go:build linux

// Package processes collects process-state counts, the top consumers of CPU
// and memory, and per-`watch` aggregates ("is nginx running, how much does
// it use").
package processes

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/modules"
	"github.com/watchfor-io/agent/internal/procfs"
)

type Config struct {
	Top   *int    `yaml:"top"`
	Watch []Watch `yaml:"watch"`
}

type Watch struct {
	Name    string `yaml:"name"`    // exact comm (the kernel's 15-char name)
	Cmdline string `yaml:"cmdline"` // substring of the full command line
	User    string `yaml:"user"`
}

func (w Watch) label() string {
	if w.Name != "" {
		return w.Name
	}
	return w.Cmdline
}

const (
	defaultTop = 10
	cmdlineCap = 256
)

type cpuKey struct {
	ticks uint64
	start uint64
}

type module struct {
	cfg     Config
	top     int
	prev    map[int]cpuKey
	prevAt  time.Time
	users   map[int]string
	cmdline bool // any watch matches on cmdline
}

func init() { modules.Register("processes", New) }

func New(node *yaml.Node) (modules.Module, error) {
	m := &module{top: defaultTop, users: map[int]string{}}
	if err := modules.Decode(node, &m.cfg); err != nil {
		return nil, err
	}
	if m.cfg.Top != nil {
		if *m.cfg.Top < 0 || *m.cfg.Top > 100 {
			return nil, errors.New("top must be between 0 and 100")
		}
		m.top = *m.cfg.Top
	}
	for i, w := range m.cfg.Watch {
		if w.Name == "" && w.Cmdline == "" {
			return nil, fmt.Errorf("watch[%d]: name or cmdline is required", i)
		}
		if w.Cmdline != "" {
			m.cmdline = true
		}
	}
	return m, nil
}

func (m *module) Name() string { return "processes" }

type entry struct {
	p       procfs.Process
	cpu     float64 // -1 until a second sample exists
	cmdline string
}

func (m *module) Collect(_ context.Context, b *metric.Batch) error {
	pids, err := procfs.Pids()
	if err != nil {
		return err
	}
	now := time.Now()
	elapsed := now.Sub(m.prevAt).Seconds()
	next := make(map[int]cpuKey, len(pids))
	entries := make([]entry, 0, len(pids))
	var total, running, sleeping, blocked, zombie, stopped, threads int

	for _, pid := range pids {
		p, err := procfs.ReadProcess(pid)
		if err != nil {
			continue
		}
		total++
		threads += p.Threads
		switch p.State {
		case 'R':
			running++
		case 'S':
			sleeping++
		case 'D':
			blocked++
		case 'Z':
			zombie++
		case 'T', 't':
			stopped++
		}
		e := entry{p: p, cpu: -1}
		// Same pid with a different start time is a reused pid, not a delta.
		if prev, ok := m.prev[pid]; ok && prev.start == p.StartTicks && elapsed > 0 && p.CPUTicks() >= prev.ticks {
			e.cpu = float64(p.CPUTicks()-prev.ticks) / procfs.ClockTicks / elapsed * 100
		}
		next[pid] = cpuKey{ticks: p.CPUTicks(), start: p.StartTicks}
		if m.cmdline {
			e.cmdline = procfs.Cmdline(pid, cmdlineCap)
		}
		entries = append(entries, e)
	}
	m.prev, m.prevAt = next, now

	b.Add("proc.total", float64(total))
	b.Add("proc.running", float64(running))
	b.Add("proc.sleeping", float64(sleeping))
	b.Add("proc.blocked", float64(blocked))
	b.Add("proc.zombie", float64(zombie))
	b.Add("proc.stopped", float64(stopped))
	b.Add("proc.threads", float64(threads))

	if m.top > 0 {
		m.topN(b, entries)
	}
	if len(m.cfg.Watch) > 0 {
		up, _ := procfs.ReadUptime()
		m.watches(b, entries, up)
	}
	return nil
}

func (m *module) topN(b *metric.Batch, entries []entry) {
	byCPU := make([]entry, 0, len(entries))
	for _, e := range entries {
		if e.cpu > 0 {
			byCPU = append(byCPU, e)
		}
	}
	sort.Slice(byCPU, func(i, j int) bool { return byCPU[i].cpu > byCPU[j].cpu })
	for _, e := range byCPU[:min(m.top, len(byCPU))] {
		b.Add("proc.cpu_pct", e.cpu, m.labels(e.p))
	}

	byRSS := append([]entry(nil), entries...)
	sort.Slice(byRSS, func(i, j int) bool { return byRSS[i].p.RSSBytes > byRSS[j].p.RSSBytes })
	for _, e := range byRSS[:min(m.top, len(byRSS))] {
		if e.p.RSSBytes > 0 {
			b.Add("proc.rss_bytes", float64(e.p.RSSBytes), m.labels(e.p))
		}
	}
}

func (m *module) watches(b *metric.Batch, entries []entry, uptime time.Duration) {
	for _, w := range m.cfg.Watch {
		var count int
		var cpu float64
		var rss uint64
		var oldest uint64
		cpuKnown := false
		for _, e := range entries {
			if !m.matches(w, e) {
				continue
			}
			count++
			rss += e.p.RSSBytes
			if e.cpu >= 0 {
				cpu += e.cpu
				cpuKnown = true
			}
			if oldest == 0 || e.p.StartTicks < oldest {
				oldest = e.p.StartTicks
			}
		}
		l := metric.Labels{"watch": w.label()}
		b.Add("procwatch.count", float64(count), l)
		if count == 0 {
			continue
		}
		b.Add("procwatch.rss_bytes", float64(rss), l)
		if cpuKnown {
			b.Add("procwatch.cpu_pct", cpu, l)
		}
		if uptime > 0 {
			b.Add("procwatch.uptime_s", uptime.Seconds()-float64(oldest)/procfs.ClockTicks, l)
		}
	}
}

func (m *module) matches(w Watch, e entry) bool {
	if w.Name != "" && e.p.Comm != w.Name {
		return false
	}
	if w.Cmdline != "" && !strings.Contains(e.cmdline, w.Cmdline) {
		return false
	}
	if w.User != "" && m.userName(e.p.UID) != w.User {
		return false
	}
	return true
}

func (m *module) labels(p procfs.Process) metric.Labels {
	return metric.Labels{"pid": strconv.Itoa(p.PID), "name": p.Comm, "user": m.userName(p.UID)}
}

func (m *module) userName(uid int) string {
	if uid < 0 {
		return ""
	}
	if name, ok := m.users[uid]; ok {
		return name
	}
	name := strconv.Itoa(uid)
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}
	m.users[uid] = name
	return name
}
