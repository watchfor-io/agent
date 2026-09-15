//go:build linux

// Package network collects per-interface throughput and error rates plus
// socket counts.
package network

import (
	"context"
	"path"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/modules"
	"github.com/watchfor-io/agent/internal/procfs"
)

type Config struct {
	// Interfaces restricts collection to exact names; default is everything
	// not matched by Ignore (glob patterns).
	Interfaces []string `yaml:"interfaces"`
	Ignore     []string `yaml:"ignore"`
}

var defaultIgnore = []string{"lo", "veth*", "docker*", "br-*", "virbr*"}

type module struct {
	cfg    Config
	prev   map[string]procfs.NetDev
	prevAt time.Time
}

func init() { modules.Register("network", New) }

func New(node *yaml.Node) (modules.Module, error) {
	m := &module{}
	if err := modules.Decode(node, &m.cfg); err != nil {
		return nil, err
	}
	if m.cfg.Ignore == nil {
		m.cfg.Ignore = defaultIgnore
	}
	for _, g := range m.cfg.Ignore {
		if _, err := path.Match(g, ""); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *module) Name() string { return "network" }

func (m *module) Collect(_ context.Context, b *metric.Batch) error {
	devs, err := procfs.ReadNetDev()
	if err != nil {
		return err
	}
	now := time.Now()
	elapsed := now.Sub(m.prevAt).Seconds()
	next := make(map[string]procfs.NetDev, len(devs))
	for _, d := range devs {
		if !m.want(d.Name) {
			continue
		}
		next[d.Name] = d
		p, ok := m.prev[d.Name]
		if !ok || elapsed <= 0 || d.RxBytes < p.RxBytes || d.TxBytes < p.TxBytes {
			continue
		}
		l := metric.Labels{"if": d.Name}
		r := func(cur, prev uint64) float64 {
			if cur < prev {
				return 0
			}
			return float64(cur-prev) / elapsed
		}
		b.Add("net.rx_bytes_per_s", r(d.RxBytes, p.RxBytes), l)
		b.Add("net.tx_bytes_per_s", r(d.TxBytes, p.TxBytes), l)
		b.Add("net.rx_packets_per_s", r(d.RxPackets, p.RxPackets), l)
		b.Add("net.tx_packets_per_s", r(d.TxPackets, p.TxPackets), l)
		b.Add("net.rx_errors_per_s", r(d.RxErrors, p.RxErrors), l)
		b.Add("net.tx_errors_per_s", r(d.TxErrors, p.TxErrors), l)
		b.Add("net.rx_dropped_per_s", r(d.RxDropped, p.RxDropped), l)
		b.Add("net.tx_dropped_per_s", r(d.TxDropped, p.TxDropped), l)
	}
	m.prev, m.prevAt = next, now

	if s, err := procfs.ReadSockStat(); err == nil {
		b.Add("net.sockets_used", float64(s.SocketsUsed))
		b.Add("net.tcp_in_use", float64(s.TCPInUse))
		b.Add("net.tcp_time_wait", float64(s.TCPTimeWait))
		b.Add("net.tcp_orphan", float64(s.TCPOrphan))
		b.Add("net.udp_in_use", float64(s.UDPInUse))
	}
	return nil
}

func (m *module) want(name string) bool {
	if len(m.cfg.Interfaces) > 0 {
		for _, n := range m.cfg.Interfaces {
			if n == name {
				return true
			}
		}
		return false
	}
	for _, g := range m.cfg.Ignore {
		if ok, _ := path.Match(g, name); ok {
			return false
		}
	}
	return true
}
