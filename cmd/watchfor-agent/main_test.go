//go:build linux

package main

import (
	"context"
	"os"
	"os/user"
	"testing"
	"time"

	"github.com/watchfor-io/agent/internal/detect"
	"github.com/watchfor-io/agent/internal/health"
	"github.com/watchfor-io/agent/internal/modules/disk"
	"github.com/watchfor-io/agent/internal/modules/processes"
)

// Tests must not sit through the real fade of a status message, and must
// not hit the machine the way the program does; the per-test wizards
// carry a fixed summary instead of collecting one.
func TestMain(m *testing.M) {
	fadeAfter = time.Millisecond
	sampleEvery = time.Millisecond
	healthProbe = health.Options{
		Dial:          func(context.Context, string, string) error { return nil },
		HaveSystemctl: func() bool { return false },
		LookupUser:    func(string) (*user.User, error) { return nil, user.UnknownUserError("no such user in tests") },
	}
	os.Exit(m.Run())
}

// testSummary is a small but complete machine: enough for every pane to
// have something to show, the same on every developer's laptop and in CI.
func testSummary() detect.Summary {
	return detect.Summary{
		Hostname: "test-host", CPUModel: "Test CPU", Cores: 4, MemTotal: 8 << 30,
		Hardware: "Test Systems Box 1", Virtualization: "kvm",
		PrimaryAddress: "10.0.0.5", PrimaryIface: "eth0",
		Addresses: map[string][]string{"eth0": {"10.0.0.5"}},
		Mounts: []disk.Discovered{
			{Point: "/", FSType: "ext4", Device: "/dev/sda2", TotalBytes: 50 << 30, UsedPct: 40},
			{Point: "/data", FSType: "xfs", Device: "/dev/sdb1", TotalBytes: 200 << 30, UsedPct: 10},
		},
		Devices:    []string{"sda", "sdb"},
		Disks:      []disk.Disk{{Name: "sda", SizeBytes: 256 << 30, SSD: true}, {Name: "sdb", SizeBytes: 2 << 40}},
		Interfaces: []string{"eth0"}, IfaceState: map[string]string{"eth0": "up"},
		Processes: []processes.Running{
			{Name: "nginx", Count: 4, RSS: 120 << 20, User: "www-data", Cmdline: "nginx: worker process"},
			{Name: "postgres", Count: 1, RSS: 900 << 20, User: "postgres", Cmdline: "/usr/lib/postgresql/16/bin/postgres -D /var/lib/postgresql"},
		},
	}
}
