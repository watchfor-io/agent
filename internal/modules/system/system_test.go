//go:build linux

package system

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/modules"
	"github.com/watchfor-io/agent/internal/procfs"
	"github.com/watchfor-io/agent/internal/testutil"
)

func TestCollect(t *testing.T) {
	procfs.Root = testutil.ProcRoot(t)
	m, err := New(testutil.Node(t, "per_core: true"))
	if err != nil {
		t.Fatal(err)
	}
	first := metric.NewBatch(testutil.Now())
	if err := m.Collect(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	names := testutil.Names(first)
	for _, want := range []string{"cpu.count", "load.1", "mem.used_pct", "mem.total_bytes", "sys.uptime_s", "swap.total_bytes"} {
		if !names[want] {
			t.Errorf("first tick missing %s", want)
		}
	}
	if names["cpu.usage_pct"] {
		t.Error("cpu rate needs two samples; first tick must not emit it")
	}

	second := metric.NewBatch(testutil.Now())
	if err := m.Collect(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	names = testutil.Names(second)
	// Identical fixtures: zero delta means no cpu percentages, but the
	// context-switch rate is computable (0/s).
	if !names["sys.context_switches_per_s"] {
		t.Error("second tick should emit rates")
	}
	for _, s := range second.Samples() {
		if s.M == "mem.used_pct" && (s.V < 0 || s.V > 100) {
			t.Errorf("mem.used_pct = %v", s.V)
		}
	}
}

func TestUnknownOptionRejected(t *testing.T) {
	var n yaml.Node
	yaml.Unmarshal([]byte("percore: true"), &n)
	_, err := New(&n)
	if err == nil || !strings.Contains(err.Error(), "percore") {
		t.Errorf("expected unknown-field error, got %v", err)
	}
}

var _ modules.Module = (*module)(nil)
