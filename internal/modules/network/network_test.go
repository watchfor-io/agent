//go:build linux

package network

import (
	"context"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/procfs"
	"github.com/watchfor-io/agent/internal/testutil"
)

func TestRatesAndIgnore(t *testing.T) {
	testutil.UseProcRoot(t, &procfs.Root)
	m, err := New(testutil.Node(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	b := metric.NewBatch(time.Now())
	m.Collect(context.Background(), b)
	if !testutil.Names(b)["net.sockets_used"] {
		t.Error("sockstat gauges missing")
	}
	b2 := metric.NewBatch(time.Now())
	m.Collect(context.Background(), b2)
	var ifaces int
	for _, s := range b2.Samples() {
		if s.M == "net.rx_bytes_per_s" {
			ifaces++
			if s.L["if"] == "lo" {
				t.Error("lo must be ignored by default")
			}
		}
	}
	if ifaces == 0 {
		t.Error("no interface rates on second tick")
	}
}

func TestBadGlob(t *testing.T) {
	if _, err := New(testutil.Node(t, "ignore: ['[']")); err == nil {
		t.Error("invalid glob must be rejected at start")
	}
}

func TestExplicitInterfaces(t *testing.T) {
	testutil.UseProcRoot(t, &procfs.Root)
	m, _ := New(testutil.Node(t, "interfaces: [lo]"))
	m.Collect(context.Background(), metric.NewBatch(time.Now()))
	b := metric.NewBatch(time.Now())
	m.Collect(context.Background(), b)
	if _, ok := testutil.Find(b, "net.rx_bytes_per_s", metric.Labels{"if": "lo"}); !ok {
		t.Error("explicit interface list should override the ignore defaults")
	}
}

// A Kubernetes node has an interface per pod; the default list leaves the
// CNI plumbing out and keeps the real NICs.
func TestDefaultIgnoreLeavesOutCNIPlumbing(t *testing.T) {
	m, err := New(&yaml.Node{Kind: yaml.MappingNode})
	if err != nil {
		t.Fatal(err)
	}
	mod := m.(*module)
	for _, name := range []string{"lo", "cali1a2b3c4d5e6", "vxlan.calico", "tunl0", "flannel.1", "cni0", "kube-ipvs0", "lxc12ab", "cilium_host", "veth9f", "docker0", "vnet3"} {
		if mod.want(name) {
			t.Errorf("%s would be reported", name)
		}
	}
	for _, name := range []string{"eth0", "ens5", "enp3s0", "bond0", "wg0", "tailscale0", "vxlan100"} {
		if !mod.want(name) {
			t.Errorf("%s would be left out", name)
		}
	}
}
