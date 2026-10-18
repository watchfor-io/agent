//go:build linux

package disk

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/watchfor-io/agent/internal/procfs"
	"gopkg.in/yaml.v3"
)

// Discovered is one filesystem the module would watch with an empty config.
type Discovered struct {
	Point, FSType, Device string
	TotalBytes            uint64
	UsedPct               float64
}

// Disk is one whole disk the module would report I/O for. A machine with
// encryption or LVM stacks several of them on one another — Parent and
// Kind say how, so a list of names can be read as the tree it is.
type Disk struct {
	Name      string
	SizeBytes uint64
	SSD       bool
	// Parent is the device this one sits on (dm-1 on dm-0 on nvme0n1),
	// empty for a disk that sits on the hardware itself.
	Parent string
	// Label is the name the system calls it: an LVM volume or a LUKS
	// mapping has one, a plain disk does not.
	Label string
	// Kind is "encrypted", "LVM", "RAID" or "" for a real disk.
	Kind string
	// Spans is every device under it when there is more than one (RAID,
	// a volume group across disks).
	Spans []string
}

// discoverBudget bounds the whole statfs pass of Discover: enough for a
// few rounds of statfsTimeout when several mounts hang at once.
const discoverBudget = 10 * time.Second

// Discover lists the mounts and whole disks an unconfigured module watches
// — what `config init` and `config detect` show the operator to pick from.
func Discover() (mounts []Discovered, devices []string, err error) {
	mod, err := New(&yaml.Node{Kind: yaml.MappingNode})
	if err != nil {
		return nil, nil, err
	}
	return mod.(*module).discover()
}

func (m *module) discover() (mounts []Discovered, devices []string, err error) {
	all, err := procfs.ReadMounts()
	if err != nil {
		return nil, nil, err
	}
	var want []procfs.Mount
	for _, mt := range all {
		if m.wantMount(mt) {
			want = append(want, mt)
		}
	}
	// The same guard as a collection tick: a hard NFS/CIFS mount whose
	// server is gone blocks statfs in the kernel for ever, and `config
	// init` runs inside the installer — it must list the other disks and
	// finish, not hang the install. A mount that does not answer within
	// statfsTimeout is left out, like one that cannot be read.
	ctx, cancel := context.WithTimeout(context.Background(), discoverBudget)
	defer cancel()
	results := m.stat.statAll(ctx, want)
	seen := map[string]bool{}
	for i, mt := range want {
		r := results[i]
		if r.err != nil || seen[mt.Device] {
			continue
		}
		st := r.st
		bs := uint64(max(st.Frsize, 0)) // int64 in the kernel's struct, never negative
		if bs == 0 {
			bs = uint64(max(st.Bsize, 0))
		}
		if st.Blocks*bs == 0 {
			continue
		}
		seen[mt.Device] = true
		used := (st.Blocks - st.Bfree) * bs
		avail := st.Bavail * bs
		mounts = append(mounts, Discovered{Point: mt.Point, FSType: mt.FSType, Device: mt.Device, TotalBytes: st.Blocks * bs, UsedPct: procfs.Pct(used, used+avail)})
	}
	if stats, err := procfs.ReadDiskStats(); err == nil {
		for _, d := range stats {
			if m.wantDevice(d.Name) {
				devices = append(devices, d.Name)
			}
		}
	}
	return mounts, devices, nil
}

// mapperName reads what device-mapper calls this device and what made
// it. A disk that is not device-mapper has neither.
func mapperName(name string) (label, kind string) {
	dir := filepath.Join(procfs.SysRoot, "block", name, "dm")
	b, err := os.ReadFile(filepath.Join(dir, "name"))
	if err != nil {
		return "", ""
	}
	label = procfs.Clean(strings.TrimSpace(string(b)))
	u, _ := os.ReadFile(filepath.Join(dir, "uuid"))
	switch id := strings.TrimSpace(string(u)); {
	case strings.HasPrefix(id, "CRYPT-"):
		kind = "encrypted"
	case strings.HasPrefix(id, "LVM-"):
		kind = "LVM"
	case strings.HasPrefix(id, "mpath-"):
		kind = "multipath"
	default:
		kind = "mapped"
	}
	return label, kind
}

// stacking answers what a device sits on: sysfs lists the devices under
// it as "slaves", and a partition is folded into the disk that holds it,
// so dm-1 → dm-0 → nvme0n1 rather than dm-1 → nvme0n1p3.
func stacking(name string) (parent string, spans []string) {
	dir := filepath.Join(procfs.SysRoot, "block", name, "slaves")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil
	}
	seen := map[string]bool{}
	for _, e := range entries {
		on := diskOf(e.Name())
		if on == "" || on == name || seen[on] {
			continue
		}
		seen[on] = true
		spans = append(spans, on)
	}
	sort.Strings(spans)
	if len(spans) == 1 {
		return spans[0], nil
	}
	return "", spans
}

// diskOf maps a partition to the disk that holds it and leaves a whole
// device alone.
func diskOf(name string) string {
	if _, err := os.Stat(filepath.Join(procfs.SysRoot, "block", name)); err == nil {
		return name
	}
	// /sys/class/block/nvme0n1p3/.. is the disk's own directory
	up, err := filepath.EvalSymlinks(filepath.Join(procfs.SysRoot, "class", "block", name))
	if err != nil {
		return name
	}
	disk := filepath.Base(filepath.Dir(up))
	if _, err := os.Stat(filepath.Join(procfs.SysRoot, "block", disk)); err == nil {
		return disk
	}
	return name
}

// Disks describes the whole disks (size and kind from sysfs).
func Disks(names []string) []Disk {
	out := make([]Disk, 0, len(names))
	for _, n := range names {
		d := Disk{Name: n}
		if b, err := os.ReadFile(filepath.Join(procfs.SysRoot, "block", n, "size")); err == nil {
			if sectors, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); err == nil {
				d.SizeBytes = sectors * 512
			}
		}
		if b, err := os.ReadFile(filepath.Join(procfs.SysRoot, "block", n, "queue", "rotational")); err == nil {
			d.SSD = strings.TrimSpace(string(b)) == "0"
		}
		d.Label, d.Kind = mapperName(n)
		d.Parent, d.Spans = stacking(n)
		out = append(out, d)
	}
	return out
}
