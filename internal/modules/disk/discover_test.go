//go:build linux

package disk

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/watchfor-io/agent/internal/procfs"
)

// A machine with encryption and LVM stacks devices on one another; the
// list has to say so, or three lines look like three disks.
func TestStackingIsDiscovered(t *testing.T) {
	root := t.TempDir()
	old := procfs.SysRoot
	procfs.SysRoot = root
	t.Cleanup(func() { procfs.SysRoot = old })
	write := func(path, body string) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// nvme0n1 ← nvme0n1p3 ← dm-0 (LUKS) ← dm-1 (LVM)
	write("block/nvme0n1/size", "500118192\n")
	write("block/nvme0n1/queue/rotational", "0\n")
	write("block/dm-0/size", "493684736\n")
	write("block/dm-0/dm/name", "dm_crypt-0\n")
	write("block/dm-0/dm/uuid", "CRYPT-LUKS2-77d9-dm_crypt-0\n")
	write("block/dm-0/slaves/nvme0n1p3/.keep", "")
	write("block/dm-1/size", "493682688\n")
	write("block/dm-1/dm/name", "ubuntu--vg-ubuntu--lv\n")
	write("block/dm-1/dm/uuid", "LVM-emvkSiry\n")
	write("block/dm-1/slaves/dm-0/.keep", "")
	// the partition lives under the disk, as sysfs has it
	write("block/nvme0n1/nvme0n1p3/partition", "3\n")
	if err := os.MkdirAll(filepath.Join(root, "class/block"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "block/nvme0n1/nvme0n1p3"), filepath.Join(root, "class/block/nvme0n1p3")); err != nil {
		t.Fatal(err)
	}

	by := map[string]Disk{}
	for _, d := range Disks([]string{"nvme0n1", "dm-0", "dm-1"}) {
		by[d.Name] = d
	}
	if d := by["nvme0n1"]; d.Parent != "" || d.Kind != "" || !d.SSD {
		t.Errorf("the disk itself: %+v", d)
	}
	if d := by["dm-0"]; d.Parent != "nvme0n1" || d.Kind != "encrypted" || d.Label != "dm_crypt-0" {
		t.Errorf("the LUKS mapping should sit on the disk that holds its partition: %+v", d)
	}
	if d := by["dm-1"]; d.Parent != "dm-0" || d.Kind != "LVM" {
		t.Errorf("the volume should sit on the mapping: %+v", d)
	}
}
