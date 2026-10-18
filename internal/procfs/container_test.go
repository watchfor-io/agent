//go:build linux

package procfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCgroup(t *testing.T) {
	id := strings.Repeat("3f2a9c1b7d4e", 5) + "abcd"
	cases := []struct {
		name, in string
		want     Container
	}{
		{"host service", "0::/system.slice/watchfor-agent.service\n", Container{}},
		{"host user session", "0::/user.slice/user-1000.slice/session-2.scope\n", Container{}},
		{"docker, systemd driver", "0::/system.slice/docker-" + id + ".scope\n", Container{"docker", "3f2a9c1b7d4e"}},
		{"docker, cgroupfs driver", "0::/docker/" + id + "\n", Container{"docker", "3f2a9c1b7d4e"}},
		{"docker on cgroup v1", "12:pids:/docker/" + id + "\n11:memory:/docker/" + id + "\n", Container{"docker", "3f2a9c1b7d4e"}},
		{"rootful podman", "0::/machine.slice/libpod-" + id + ".scope/container\n", Container{"podman", "3f2a9c1b7d4e"}},
		{"rootless podman", "0::/user.slice/user-1000.slice/user@1000.service/user.slice/libpod-" + id + ".scope/container\n", Container{"podman", "3f2a9c1b7d4e"}},
		{"podman's conmon runs on the host", "0::/machine.slice/libpod-conmon-" + id + ".scope\n", Container{}},
		{"kubernetes, systemd driver", "0::/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod1234.slice/cri-containerd-" + id + ".scope\n", Container{"kubernetes", "3f2a9c1b7d4e"}},
		{"kubernetes, cgroupfs driver", "0::/kubepods/besteffort/pod1234/" + id + "\n", Container{"kubernetes", "3f2a9c1b7d4e"}},
		{"cri-o", "0::/system.slice/crio-" + id + ".scope\n", Container{"cri-o", "3f2a9c1b7d4e"}},
		{"containerd, cgroupfs", "0::/default/" + id + "\n", Container{"containerd", "3f2a9c1b7d4e"}},
		{"lxc payload", "0::/lxc.payload.mail/system.slice/sogo.service\n", Container{"lxc", "mail"}},
		{"lxc monitor runs on the host", "0::/lxc.monitor.mail\n", Container{}},
		{"old lxc", "4:memory:/lxc/web01\n", Container{"lxc", "web01"}},
		{"nspawn payload", "0::/machine.slice/systemd-nspawn@build.service/payload/system.slice/x.service\n", Container{"nspawn", "build"}},
		{"nspawn supervisor runs on the host", "0::/machine.slice/systemd-nspawn@build.service/supervisor\n", Container{}},
		{"a VM's qemu is a host process", "0::/machine.slice/machine-qemu\\x2d1\\x2dvm.scope/libvirt/emulator\n", Container{}},
		{"not hex", "0::/docker/" + strings.Repeat("z", 64) + "\n", Container{}},
		{"garbage", "nonsense\n::\n", Container{}},
	}
	for _, c := range cases {
		if got := ParseCgroup(c.in); got != c.want {
			t.Errorf("%s: ParseCgroup = %+v, want %+v", c.name, got, c.want)
		}
	}
	if s := (Container{"docker", "3f2a9c1b7d4e"}).String(); s != "docker:3f2a9c1b7d4e" {
		t.Errorf("String = %q", s)
	}
}

// A process that exits between the listing and the read is an error, not
// a host process with a made-up container.
func TestReadContainerGone(t *testing.T) {
	old := Root
	Root = t.TempDir()
	t.Cleanup(func() { Root = old })
	if c, err := ReadContainer("12345"); err == nil || c != (Container{}) {
		t.Errorf("gone process = %+v, %v", c, err)
	}
	if err := os.MkdirAll(filepath.Join(Root, "7"), 0o755); err != nil {
		t.Fatal(err)
	}
	long := "0::/lxc.payload." + strings.Repeat("n", 200) + "\n"
	if err := os.WriteFile(filepath.Join(Root, "7", "cgroup"), []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := ReadContainer("7")
	if err != nil || c.Runtime != "lxc" || len(c.ID) != 64 {
		t.Errorf("long name = %+v, %v; want lxc, 64 characters", c, err)
	}
}
