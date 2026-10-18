//go:build linux

package procfs

import (
	"io"
	"os"
	"strconv"
	"strings"
)

// Container names the container a process runs in, as its cgroup path
// gives it away: the runtime and the container's short id (or name, for
// LXC and systemd-nspawn). The zero value is "on the host".
//
// The cgroup is the signal because it is world-readable — the agent runs
// unprivileged and may not readlink another user's /proc/<pid>/ns/* — and
// because it names the runtime. A nested pid namespace alone is not
// enough: browser sandboxes and flatpak live in one too, yet their uid is
// a real host user and the host's name for it is right.
type Container struct {
	Runtime string // docker, podman, containerd, cri-o, kubernetes, lxc, nspawn, container
	ID      string // 12 hex digits, or the LXC/nspawn name
}

// String is the label value: "docker:3f2a9c1b7d4e", "" on the host.
func (c Container) String() string {
	if c.Runtime == "" {
		return ""
	}
	if c.ID == "" {
		return c.Runtime
	}
	return c.Runtime + ":" + c.ID
}

// cgroupCap bounds the read: a process in hundreds of v1 hierarchies has
// a long file, but the container shows in the first few lines.
const cgroupCap = 4096

// ReadContainer reads /proc/<pid>/cgroup and names the container in it.
// A process gone, or a kernel without cgroups, is the zero value and an
// error the caller may ignore.
func ReadContainer(pid string) (Container, error) {
	f, err := os.Open(path(pid, "cgroup"))
	if err != nil {
		return Container{}, err
	}
	defer f.Close()
	b := make([]byte, cgroupCap)
	n, err := io.ReadFull(f, b)
	if n == 0 && err != nil {
		return Container{}, err
	}
	return ParseCgroup(string(b[:n])), nil
}

// ReadContainerPID is ReadContainer for a numeric pid.
func ReadContainerPID(pid int) (Container, error) { return ReadContainer(strconv.Itoa(pid)) }

// ParseCgroup reads proc(7)'s "hierarchy-ID:controllers:path" lines — v2's
// single "0::/…" and v1's one line per controller — and returns the first
// container any path names.
func ParseCgroup(s string) Container {
	for _, line := range strings.Split(s, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if c := containerOf(parts[2]); c.Runtime != "" {
			return c
		}
	}
	return Container{}
}

// containerOf walks one cgroup path. The shapes, from the runtimes' own
// code: docker-<id>.scope / /docker/<id> (Docker, systemd or cgroupfs
// driver), libpod-<id>.scope (Podman), cri-containerd-<id>.scope,
// crio-<id>.scope and nerdctl-<id>.scope, a bare 64-hex segment
// (containerd and Kubernetes on cgroupfs), lxc.payload.<name> or /lxc/<name>
// (LXC, LXD, Incus) and systemd-nspawn@<name>.service/payload. The host-side
// helpers next to them — libpod-conmon-<id>.scope, lxc.monitor.<name>,
// nspawn's supervisor — run as host users and are not containers.
func containerOf(p string) Container {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	kube := strings.Contains(p, "kubepods")
	for i, seg := range segs {
		switch {
		case strings.HasPrefix(seg, "lxc.payload."):
			return Container{Runtime: "lxc", ID: label(strings.TrimPrefix(seg, "lxc.payload."))}
		case seg == "lxc.payload" && i+1 < len(segs):
			return Container{Runtime: "lxc", ID: label(segs[i+1])}
		case seg == "lxc" && i == 0 && len(segs) > 1:
			return Container{Runtime: "lxc", ID: label(segs[1])}
		case strings.HasPrefix(seg, "systemd-nspawn@") && strings.HasSuffix(seg, ".service"):
			if i+1 < len(segs) && segs[i+1] == "supervisor" {
				return Container{}
			}
			name := strings.TrimSuffix(strings.TrimPrefix(seg, "systemd-nspawn@"), ".service")
			return Container{Runtime: "nspawn", ID: label(name)}
		}
		runtime, id := scopeID(seg)
		if id == "" && isContainerID(seg) {
			runtime, id = "containerd", seg
			if i > 0 && segs[i-1] == "docker" {
				runtime = "docker"
			}
		}
		if id == "" {
			continue
		}
		if kube {
			runtime = "kubernetes"
		}
		return Container{Runtime: runtime, ID: id[:12]}
	}
	return Container{}
}

// scopeID splits "<prefix>-<64 hex>.scope" (or without .scope, as Podman's
// cgroupfs driver names it) into the runtime and the id.
func scopeID(seg string) (runtime, id string) {
	seg = strings.TrimSuffix(seg, ".scope")
	i := strings.LastIndexByte(seg, '-')
	if i < 0 || !isContainerID(seg[i+1:]) {
		return "", ""
	}
	switch seg[:i] {
	case "docker":
		return "docker", seg[i+1:]
	case "libpod":
		return "podman", seg[i+1:]
	case "cri-containerd", "nerdctl", "containerd":
		return "containerd", seg[i+1:]
	case "crio":
		return "cri-o", seg[i+1:]
	}
	return "", ""
}

// label keeps a container name fit for a label: clean, and at most 64
// characters (LXC allows longer; nobody reads past that on a dashboard).
func label(s string) string {
	s = Clean(s)
	if r := []rune(s); len(r) > 64 {
		return string(r[:64])
	}
	return s
}

// isContainerID is the OCI runtimes' id: 64 lowercase hex digits.
func isContainerID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
