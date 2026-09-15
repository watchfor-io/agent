//go:build linux

// Package hostinfo builds the host identity and the slow-changing facts that
// accompany every batch.
package hostinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/procfs"
)

// EtcRoot is overridable for tests.
var EtcRoot = "/"

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

func Facts() metric.Facts {
	f := metric.Facts{CPUModel: cpuModel(), Virtualization: virtualization()}
	if st, err := procfs.ReadStat(); err == nil {
		f.CPUCores = len(st.PerCPU)
		f.BootTime = st.BootTime
	}
	if m, err := procfs.ReadMeminfo(); err == nil {
		f.MemTotal = m["MemTotal"]
	}
	f.Addresses = addresses()
	return f
}

func osName() string {
	b, err := os.ReadFile(filepath.Join(EtcRoot, "etc/os-release"))
	if err != nil {
		return "linux"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
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
	return strings.TrimSpace(string(b))
}

func cpuModel() string {
	b, err := os.ReadFile(filepath.Join(procfs.Root, "cpuinfo"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "model name", "Model", "cpu model":
			return strings.TrimSpace(v)
		}
	}
	return ""
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
	vendor := readSys("class/dmi/id/sys_vendor") + " " + readSys("class/dmi/id/product_name")
	for marker, name := range map[string]string{
		"QEMU": "kvm", "KVM": "kvm", "VMware": "vmware", "VirtualBox": "virtualbox",
		"Xen": "xen", "Microsoft Corporation": "hyperv", "Amazon EC2": "ec2",
		"Google": "gce", "DigitalOcean": "digitalocean", "Hetzner": "hetzner", "OpenStack": "openstack",
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
