//go:build linux

// Package system collects CPU, load, memory, swap and kernel-wide process
// counters from /proc.
package system

import (
	"context"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/modules"
	"github.com/watchfor-io/agent/internal/procfs"
)

type Config struct {
	PerCore bool `yaml:"per_core"`
}

type module struct {
	cfg    Config
	prev   procfs.Stat
	prevAt time.Time
	have   bool
}

func init() { modules.Register("system", New) }

func New(node *yaml.Node) (modules.Module, error) {
	m := &module{}
	if err := modules.Decode(node, &m.cfg); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *module) Name() string { return "system" }

func (m *module) Collect(_ context.Context, b *metric.Batch) error {
	st, err := procfs.ReadStat()
	if err != nil {
		return err
	}
	now := time.Now()
	// Rates need two samples; the first tick after start only primes them.
	if m.have && st.CPU.Total() >= m.prev.CPU.Total() {
		cpuPercent(b, nil, st.CPU.Sub(m.prev.CPU))
		if m.cfg.PerCore {
			for i := range st.PerCPU {
				if i < len(m.prev.PerCPU) {
					cpuPercent(b, metric.Labels{"cpu": strconv.Itoa(i)}, st.PerCPU[i].Sub(m.prev.PerCPU[i]))
				}
			}
		}
		if s := now.Sub(m.prevAt).Seconds(); s > 0 {
			b.Add("sys.context_switches_per_s", rate(st.ContextSwitches, m.prev.ContextSwitches, s))
			b.Add("sys.forks_per_s", rate(st.Forks, m.prev.Forks, s))
		}
	}
	m.prev, m.prevAt, m.have = st, now, true

	b.Add("cpu.count", float64(len(st.PerCPU)))
	b.Add("sys.procs_running", float64(st.ProcsRunning))
	b.Add("sys.procs_blocked", float64(st.ProcsBlocked))

	if la, err := procfs.ReadLoadAvg(); err == nil {
		b.Add("load.1", la.Load1)
		b.Add("load.5", la.Load5)
		b.Add("load.15", la.Load15)
	}
	if up, err := procfs.ReadUptime(); err == nil {
		b.Add("sys.uptime_s", up.Seconds())
	}

	mem, err := procfs.ReadMeminfo()
	if err != nil {
		return err
	}
	total := mem["MemTotal"]
	avail := mem["MemAvailable"]
	if avail == 0 {
		// Pre-3.14 kernels: the classic approximation.
		avail = mem["MemFree"] + mem["Buffers"] + mem["Cached"]
	}
	used := total - min(avail, total)
	b.Add("mem.total_bytes", float64(total))
	b.Add("mem.used_bytes", float64(used))
	b.Add("mem.available_bytes", float64(avail))
	b.Add("mem.free_bytes", float64(mem["MemFree"]))
	b.Add("mem.cached_bytes", float64(mem["Cached"]+mem["SReclaimable"]))
	b.Add("mem.buffers_bytes", float64(mem["Buffers"]))
	b.Add("mem.used_pct", pct(used, total))

	swapTotal := mem["SwapTotal"]
	swapUsed := swapTotal - min(mem["SwapFree"], swapTotal)
	b.Add("swap.total_bytes", float64(swapTotal))
	b.Add("swap.used_bytes", float64(swapUsed))
	if swapTotal > 0 {
		b.Add("swap.used_pct", pct(swapUsed, swapTotal))
	}
	return nil
}

func cpuPercent(b *metric.Batch, l metric.Labels, d procfs.CPUTimes) {
	total := d.Total()
	if total == 0 {
		return
	}
	p := func(v uint64) float64 { return float64(v) / float64(total) * 100 }
	b.Add("cpu.usage_pct", p(total-d.Idle-d.IOWait), l)
	b.Add("cpu.user_pct", p(d.User+d.Nice), l)
	b.Add("cpu.system_pct", p(d.System+d.IRQ+d.SoftIRQ), l)
	b.Add("cpu.iowait_pct", p(d.IOWait), l)
	b.Add("cpu.steal_pct", p(d.Steal), l)
	b.Add("cpu.idle_pct", p(d.Idle), l)
}

func rate(cur, prev uint64, seconds float64) float64 {
	if cur < prev {
		return 0
	}
	return float64(cur-prev) / seconds
}

func pct(part, whole uint64) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}
