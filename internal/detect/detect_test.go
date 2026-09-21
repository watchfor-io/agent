//go:build linux

package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/watchfor-io/agent/internal/config"
	"github.com/watchfor-io/agent/internal/modules/disk"
)

// The generated file must be a config the agent accepts as-is, with the
// detected lists visible in comments and the token only in the file path.
func TestGeneratedConfigIsValid(t *testing.T) {
	s := Summary{
		Hostname: "web-01", CPUModel: "AMD EPYC 7B13", Cores: 4, MemTotal: 8 << 30,
		Hardware: "Amazon EC2 t3.medium", Virtualization: "ec2",
		Mounts:     []disk.Discovered{{Point: "/", FSType: "ext4", Device: "/dev/nvme0n1p1", TotalBytes: 50 << 30}, {Point: "/data", FSType: "xfs", Device: "/dev/nvme1n1", TotalBytes: 500 << 30}},
		Devices:    []string{"nvme0n1", "nvme1n1"},
		Interfaces: []string{"ens5"},
	}
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("wfh_x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.Server, o.TokenFile = "https://ingest.watchfor.io", token
	text := s.YAML(o)
	cfg, err := config.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatalf("generated config rejected: %v\n%s", err, text)
	}
	if cfg.Interval.String() != "1m0s" || cfg.Spool.Dir != "/var/lib/watchfor-agent" || !cfg.Facts.CloudMetadataEnabled() {
		t.Fatalf("defaults not as written: interval=%s spool=%s cloud=%v", cfg.Interval, cfg.Spool.Dir, cfg.Facts.CloudMetadataEnabled())
	}
	for _, want := range []string{"/ (ext4, 50 GB)", "/data (xfs, 500 GB)", "mounts: [/, /data]", "devices: [nvme0n1, nvme1n1]", "interfaces: [ens5]", "AMD EPYC 7B13, 4 cores, 8.0 GB RAM", "token_file: " + token} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "wfh_") {
		t.Error("a token leaked into the template")
	}
}

// Three device names in a row look like three disks. The list has to
// draw what sits on what.
func TestDiskRowsAreATree(t *testing.T) {
	s := Summary{
		Devices: []string{"dm-1", "nvme0n1", "dm-0"},
		Disks: []disk.Disk{
			{Name: "nvme0n1", SizeBytes: 256060514304, SSD: true},
			{Name: "dm-0", SizeBytes: 252766584832, Parent: "nvme0n1", Label: "dm_crypt-0", Kind: "encrypted"},
			{Name: "dm-1", SizeBytes: 252765536256, Parent: "dm-0", Label: "ubuntu--vg-ubuntu--lv", Kind: "LVM"},
		},
	}
	s.Devices = s.diskOrder()
	if got := strings.Join(s.Devices, ","); got != "nvme0n1,dm-0,dm-1" {
		t.Fatalf("order is %q, want the disk first and its volumes under it", got)
	}
	rows := make([]string, len(s.Devices))
	for i, d := range s.Devices {
		rows[i] = s.DiskRow(d)
	}
	if strings.HasPrefix(rows[0], " ") || strings.Contains(rows[0], "└") {
		t.Errorf("the disk itself should not be indented: %q", rows[0])
	}
	if !strings.HasPrefix(rows[1], "└─ dm-0 · dm_crypt-0") {
		t.Errorf("the mapping should hang off the disk: %q", rows[1])
	}
	if !strings.HasPrefix(rows[2], "   └─ dm-1 · ubuntu--vg-ubuntu--lv") {
		t.Errorf("the volume should hang off the mapping: %q", rows[2])
	}
	for _, r := range rows {
		if !strings.Contains(r, "GB") {
			t.Fatalf("no size on %q", r)
		}
	}
	if !strings.Contains(rows[1], "encrypted") || !strings.Contains(rows[2], "LVM") {
		t.Errorf("the rows do not say what they are:\n%s", strings.Join(rows, "\n"))
	}
	// every row still lines up
	at := -1
	for _, r := range rows {
		i := utf8.RuneCountInString(r[:strings.Index(r, "GB")]) // columns, not bytes
		if at >= 0 && i != at {
			t.Errorf("sizes do not line up:\n%s", strings.Join(rows, "\n"))
		}
		at = i
	}
}
