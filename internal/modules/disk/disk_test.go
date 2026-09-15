//go:build linux

package disk

import (
	"context"
	"strings"
	"testing"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/procfs"
	"github.com/watchfor-io/agent/internal/testutil"
)

func TestUsageAndIO(t *testing.T) {
	procfs.Root = testutil.ProcRoot(t)
	m, err := New(testutil.Node(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	b := metric.NewBatch(testutil.Now())
	if err := m.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if _, ok := testutil.Find(b, "disk.used_pct", metric.Labels{"mount": "/"}); !ok {
		t.Error("root filesystem usage missing")
	}
	for _, s := range b.Samples() {
		if s.M == "disk.used_pct" && (s.L["fs"] == "tmpfs" || s.L["mount"] == "/proc") {
			t.Errorf("pseudo filesystem leaked: %+v", s.L)
		}
	}
	if testutil.Names(b)["diskio.read_bytes_per_s"] {
		t.Error("io rates need a second sample")
	}
	b2 := metric.NewBatch(testutil.Now())
	m.Collect(context.Background(), b2)
	names := testutil.Names(b2)
	if !names["diskio.read_bytes_per_s"] || !names["diskio.util_pct"] {
		t.Errorf("io rates missing on second tick: %v", names)
	}
	for _, s := range b2.Samples() {
		if s.M == "diskio.util_pct" && s.V > 100 {
			t.Errorf("util over 100: %v", s)
		}
		if strings.HasPrefix(s.M, "diskio.") && !wholeDisk.MatchString(s.L["device"]) {
			t.Errorf("partition or virtual device leaked into io stats: %s", s.L["device"])
		}
	}
}

func TestExplicitMountsAndDevices(t *testing.T) {
	procfs.Root = testutil.ProcRoot(t)
	m, err := New(testutil.Node(t, "mounts: [/nonexistent-mount]\nio: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := metric.NewBatch(testutil.Now())
	m.Collect(context.Background(), b)
	if b.Len() != 0 {
		t.Errorf("explicit missing mount and io off should yield nothing, got %d samples", b.Len())
	}
}
