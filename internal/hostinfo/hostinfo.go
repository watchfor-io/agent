//go:build linux

// Package hostinfo builds the host identity and the slow-changing facts that
// accompany every batch.
package hostinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/procfs"
)

// EtcRoot is where /etc and /var are read from; tests point it at a fixture.
var EtcRoot = "/"

// Host builds the identity that heads every batch: the hashed machine-id,
// the configured name and tags, and the OS, kernel and architecture.
func Host(name string, tags map[string]string) metric.Host {
	return metric.Host{
		ID:     ID(),
		Name:   name,
		Tags:   tags,
		OS:     osName(),
		Kernel: kernel(),
		Arch:   runtime.GOARCH,
	}
}

// ID is a stable, opaque host identifier. The raw machine-id is hashed:
// systemd documents it as something not to expose outside the machine,
// and a hash keeps the same stability without linking the host to it.
func ID() string {
	seed := ""
	for _, p := range []string{"etc/machine-id", "var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(filepath.Join(EtcRoot, p)); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			seed = strings.TrimSpace(string(b))
			break
		}
	}
	if seed == "" {
		seed, _ = os.Hostname()
	}
	sum := sha256.Sum256([]byte("watchfor-agent\n" + seed))
	return hex.EncodeToString(sum[:8])
}

// Options say how the facts are collected. The zero value asks nothing of
// the network beyond a route lookup and logs nothing.
type Options struct {
	// Target is the server's host name: the kernel is asked which source
	// address it would use to reach it, and that is the primary address.
	// Empty means a public resolver address stands in for the question.
	Target string
	// CloudMetadata allows the one network lookup facts can make: asking
	// the detected cloud's metadata service for the instance size.
	CloudMetadata bool
	// Log, when set, gets debug lines about what was recognised and asked.
	Log *slog.Logger
}

func (o Options) debug(msg string, args ...any) {
	if o.Log != nil {
		o.Log.Debug(msg, args...)
	}
}

// Collect gathers the slow-changing facts.
func Collect(o Options) metric.Facts {
	f := metric.Facts{CPUModel: cpuModel(), Hardware: hardware(o), Virtualization: virtualization()}
	if st, err := procfs.ReadStat(); err == nil {
		f.CPUCores = len(st.PerCPU)
		f.BootTime = st.BootTime
	}
	if m, err := procfs.ReadMeminfo(); err == nil {
		f.MemTotal = m["MemTotal"]
	}
	f.PrimaryAddress, f.PrimaryAddress6, f.PrimaryIface = primaryAddress(o.Target)
	f.Addresses = addresses(f.PrimaryIface)
	// what the firmware, a hypervisor or the kernel calls things is theirs
	f.CPUModel, f.PrimaryIface = procfs.Clean(f.CPUModel), procfs.Clean(f.PrimaryIface)
	for name, addrs := range f.Addresses {
		if c := procfs.Clean(name); c != name {
			delete(f.Addresses, name)
			f.Addresses[c] = addrs
		}
	}
	return f
}

func osName() string {
	b, err := os.ReadFile(filepath.Join(EtcRoot, "etc/os-release"))
	if err != nil {
		return "linux"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return procfs.Clean(strings.Trim(v, `"`))
		}
	}
	return "linux"
}

// kernel reads /proc rather than uname(2): Utsname's byte type differs
// between architectures and this avoids a build tag per GOARCH.
func kernel() string {
	b, err := os.ReadFile(filepath.Join(procfs.Root, "sys/kernel/osrelease"))
	if err != nil {
		return ""
	}
	return procfs.Clean(strings.TrimSpace(string(b)))
}

// virtualization is best effort: DMI vendor strings for VMs, well-known
// marker files for containers, the cpuinfo hypervisor flag as a fallback.
func virtualization() string {
	if _, err := os.Stat(filepath.Join(EtcRoot, ".dockerenv")); err == nil {
		return "docker"
	}
	if b, err := os.ReadFile(filepath.Join(procfs.Root, "1/cgroup")); err == nil {
		s := string(b)
		switch {
		case strings.Contains(s, "docker"):
			return "docker"
		case strings.Contains(s, "lxc"):
			return "lxc"
		case strings.Contains(s, "kubepods"):
			return "kubernetes"
		}
	}
	d := readDMI()
	if c := detectCloud(d); c != nil {
		return c.virt
	}
	vendor := d.vendor + " " + d.product
	for marker, name := range map[string]string{
		"QEMU": "kvm", "KVM": "kvm", "VMware": "vmware", "VirtualBox": "virtualbox",
		"Xen": "xen", "Microsoft Corporation": "hyperv", "Parallels": "parallels", "innotek": "virtualbox",
	} {
		if strings.Contains(vendor, marker) {
			return name
		}
	}
	if b, err := os.ReadFile(filepath.Join(procfs.Root, "cpuinfo")); err == nil && strings.Contains(string(b), " hypervisor") {
		return "vm"
	}
	return ""
}

func readSys(p string) string {
	b, err := os.ReadFile(filepath.Join(procfs.SysRoot, p))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
