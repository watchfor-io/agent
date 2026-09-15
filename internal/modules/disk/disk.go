//go:build linux

// Package disk collects filesystem usage per mount and I/O rates per block
// device.
package disk

import (
	"context"
	"regexp"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/modules"
	"github.com/watchfor-io/agent/internal/procfs"
)

type Config struct {
	// Mounts restricts usage to these mount points; default is every real
	// filesystem. IgnoreFS extends the built-in pseudo-filesystem list.
	Mounts   []string `yaml:"mounts"`
	IgnoreFS []string `yaml:"ignore_fs"`
	// Devices restricts I/O stats; default is whole disks (not partitions).
	Devices []string `yaml:"devices"`
	IO      *bool    `yaml:"io"`
}

// Pseudo and ephemeral filesystems whose "usage" means nothing to an operator.
var pseudoFS = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "overlay": true, "squashfs": true, "proc": true,
	"sysfs": true, "cgroup": true, "cgroup2": true, "devpts": true, "mqueue": true,
	"debugfs": true, "tracefs": true, "securityfs": true, "pstore": true, "bpf": true,
	"autofs": true, "hugetlbfs": true, "fusectl": true, "configfs": true,
	"binfmt_misc": true, "nsfs": true, "efivarfs": true, "ramfs": true,
	"rpc_pipefs": true, "selinuxfs": true, "fuse.gvfsd-fuse": true, "fuse.portal": true,
}

var hiddenMountPrefixes = []string{"/proc", "/sys", "/dev", "/run", "/snap/", "/var/lib/docker/", "/var/lib/containers/"}

// Whole-disk names; partitions (sda1, nvme0n1p2) and virtual devices (loop,
// ram, zram) are left out so a rate is not counted twice.
var wholeDisk = regexp.MustCompile(`^(sd[a-z]+|vd[a-z]+|xvd[a-z]+|hd[a-z]+|nvme\d+n\d+|mmcblk\d+|md\d+|dm-\d+|rbd\d+)$`)

type module struct {
	cfg    Config
	ignore map[string]bool
	prev   map[string]procfs.DiskStat
	prevAt time.Time
}

func init() { modules.Register("disk", New) }

func New(node *yaml.Node) (modules.Module, error) {
	m := &module{ignore: make(map[string]bool, len(pseudoFS))}
	if err := modules.Decode(node, &m.cfg); err != nil {
		return nil, err
	}
	for fs := range pseudoFS {
		m.ignore[fs] = true
	}
	for _, fs := range m.cfg.IgnoreFS {
		m.ignore[fs] = true
	}
	return m, nil
}

func (m *module) Name() string { return "disk" }

func (m *module) Collect(_ context.Context, b *metric.Batch) error {
	if err := m.usage(b); err != nil {
		return err
	}
	if m.cfg.IO == nil || *m.cfg.IO {
		return m.io(b)
	}
	return nil
}

func (m *module) usage(b *metric.Batch) error {
	mounts, err := procfs.ReadMounts()
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(mounts))
	for _, mt := range mounts {
		if !m.wantMount(mt) || seen[mt.Device] {
			continue
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(mt.Point, &st); err != nil {
			continue // unreachable network mount, or a path we may not enter
		}
		bs := uint64(st.Frsize)
		if bs == 0 {
			bs = uint64(st.Bsize)
		}
		total := st.Blocks * bs
		if total == 0 {
			continue
		}
		seen[mt.Device] = true
		avail := st.Bavail * bs
		used := total - st.Bfree*bs
		l := metric.Labels{"mount": mt.Point, "device": mt.Device, "fs": mt.FSType}
		b.Add("disk.total_bytes", float64(total), l)
		b.Add("disk.used_bytes", float64(used), l)
		b.Add("disk.free_bytes", float64(avail), l)
		// df semantics: reserved blocks are neither used nor available.
		b.Add("disk.used_pct", pct(used, used+avail), l)
		if st.Files > 0 {
			b.Add("disk.inodes_used_pct", pct(st.Files-st.Ffree, st.Files), l)
		}
	}
	return nil
}

func (m *module) wantMount(mt procfs.Mount) bool {
	if len(m.cfg.Mounts) > 0 {
		for _, p := range m.cfg.Mounts {
			if p == mt.Point {
				return true
			}
		}
		return false
	}
	if m.ignore[mt.FSType] || strings.HasPrefix(mt.FSType, "fuse.") && m.ignore["fuse."+strings.TrimPrefix(mt.FSType, "fuse.")] {
		return false
	}
	for _, p := range hiddenMountPrefixes {
		if mt.Point == strings.TrimSuffix(p, "/") || strings.HasPrefix(mt.Point, p) {
			return false
		}
	}
	return true
}

func (m *module) io(b *metric.Batch) error {
	stats, err := procfs.ReadDiskStats()
	if err != nil {
		return err
	}
	now := time.Now()
	elapsed := now.Sub(m.prevAt).Seconds()
	next := make(map[string]procfs.DiskStat, len(stats))
	for _, d := range stats {
		if !m.wantDevice(d.Name) {
			continue
		}
		next[d.Name] = d
		l := metric.Labels{"device": d.Name}
		b.Add("diskio.in_progress", float64(d.IOInProgress), l)
		p, ok := m.prev[d.Name]
		if !ok || elapsed <= 0 || d.ReadsCompleted < p.ReadsCompleted {
			continue
		}
		reads := d.ReadsCompleted - p.ReadsCompleted
		writes := d.WritesCompleted - p.WritesCompleted
		b.Add("diskio.read_bytes_per_s", float64((d.SectorsRead-p.SectorsRead)*procfs.SectorSize)/elapsed, l)
		b.Add("diskio.write_bytes_per_s", float64((d.SectorsWritten-p.SectorsWritten)*procfs.SectorSize)/elapsed, l)
		b.Add("diskio.read_ops_per_s", float64(reads)/elapsed, l)
		b.Add("diskio.write_ops_per_s", float64(writes)/elapsed, l)
		b.Add("diskio.util_pct", min(float64(d.IOTicksMs-p.IOTicksMs)/(elapsed*1000)*100, 100), l)
		if ops := reads + writes; ops > 0 {
			b.Add("diskio.await_ms", float64((d.ReadTicksMs-p.ReadTicksMs)+(d.WriteTicksMs-p.WriteTicksMs))/float64(ops), l)
		}
	}
	m.prev, m.prevAt = next, now
	return nil
}

func (m *module) wantDevice(name string) bool {
	if len(m.cfg.Devices) > 0 {
		for _, d := range m.cfg.Devices {
			if d == name {
				return true
			}
		}
		return false
	}
	return wholeDisk.MatchString(name)
}

func pct(part, whole uint64) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}
