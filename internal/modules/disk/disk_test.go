//go:build linux

package disk

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/procfs"
	"github.com/watchfor-io/agent/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestUsageAndIO(t *testing.T) {
	testutil.UseProcRoot(t, &procfs.Root)
	m, err := New(testutil.Node(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	b := metric.NewBatch(time.Now())
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
	b2 := metric.NewBatch(time.Now())
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
	testutil.UseProcRoot(t, &procfs.Root)
	m, err := New(testutil.Node(t, "mounts: [/nonexistent-mount]\nio: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := metric.NewBatch(time.Now())
	m.Collect(context.Background(), b)
	if b.Len() != 0 {
		t.Errorf("explicit missing mount and io off should yield nothing, got %d samples", b.Len())
	}
}

// A default (empty) config hides what comes and goes — FUSE app images,
// USB sticks under /media, snaps — and an explicit mounts: list brings a
// chosen one back.
func TestDefaultHidesEphemeralMounts(t *testing.T) {
	mod, err := New(&yaml.Node{Kind: yaml.MappingNode})
	if err != nil {
		t.Fatal(err)
	}
	m := mod.(*module)
	cases := map[procfs.Mount]bool{
		{Device: "/dev/nvme0n1p2", Point: "/", FSType: "ext4"}:                             true,
		{Device: "/dev/sdb1", Point: "/data", FSType: "xfs"}:                               true,
		{Device: "viber", Point: "/tmp/.mount_viberpdkAiC", FSType: "fuse.viber.AppImage"}: false,
		{Device: "/dev/sdc1", Point: "/media/someone/USB", FSType: "vfat"}:                 false,
		{Device: "/dev/sdc1", Point: "/run/media/user/USB", FSType: "vfat"}:                false,
		{Device: "/dev/loop3", Point: "/snap/core/123", FSType: "squashfs"}:                false,
		{Device: "sshfs#host:", Point: "/home/u/remote", FSType: "fuse.sshfs"}:             false,
		{Device: "nas:/vol", Point: "/srv/nas", FSType: "nfs4"}:                            true,
	}
	for mt, want := range cases {
		if got := m.wantMount(mt); got != want {
			t.Errorf("default: %s (%s) watched=%v, want %v", mt.Point, mt.FSType, got, want)
		}
	}
	// chosen on purpose: the USB drive and the FUSE mount come back
	chosen, _ := New(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "mounts"},
		{Kind: yaml.SequenceNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "/media/someone/USB"}, {Kind: yaml.ScalarNode, Value: "/home/u/remote"}}},
	}})
	c := chosen.(*module)
	for _, mt := range []procfs.Mount{{Point: "/media/someone/USB", FSType: "vfat"}, {Point: "/home/u/remote", FSType: "fuse.sshfs"}} {
		if !c.wantMount(mt) {
			t.Errorf("explicit mounts: %s not watched", mt.Point)
		}
	}
	if c.wantMount(procfs.Mount{Point: "/", FSType: "ext4"}) {
		t.Error("explicit mounts: / watched although not listed")
	}
}
