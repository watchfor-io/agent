//go:build linux

package disk

import (
	"testing"

	"github.com/watchfor-io/agent/internal/procfs"
	"gopkg.in/yaml.v3"
)

// A hidden prefix is a path, not a string: /proc is hidden, /procdata is
// a filesystem somebody mounted on purpose.
func TestHiddenPrefixesArePaths(t *testing.T) {
	mod, err := New(&yaml.Node{Kind: yaml.MappingNode})
	if err != nil {
		t.Fatal(err)
	}
	m := mod.(*module)
	for point, want := range map[string]bool{
		"/proc": false, "/proc/sys/fs": false, "/sys": false, "/dev/shm": false, "/run/user/1000": false,
		"/procdata": true, "/system": true, "/devices": true, "/runtime": true, "/": true, "/data": true,
	} {
		if got := m.wantMount(procfs.Mount{Point: point, FSType: "ext4", Device: "/dev/sda1"}); got != want {
			t.Errorf("wantMount(%q) = %v, want %v", point, got, want)
		}
	}
}
