//go:build linux

package network

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/watchfor-io/agent/internal/procfs"
	"gopkg.in/yaml.v3"
)

// Discover lists the interfaces an unconfigured module watches (loopback
// and the usual container/VM plumbing already left out).
func Discover() ([]string, error) {
	mod, err := New(&yaml.Node{Kind: yaml.MappingNode})
	if err != nil {
		return nil, err
	}
	m := mod.(*module)
	devs, err := procfs.ReadNetDev()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range devs {
		if m.want(d.Name) {
			out = append(out, procfs.Clean(d.Name))
		}
	}
	return out, nil
}

// State is "up", "down" or "" (unknown) for an interface, from sysfs.
func State(name string) string {
	b, err := os.ReadFile(filepath.Join(procfs.SysRoot, "class", "net", name, "operstate"))
	if err != nil {
		return ""
	}
	switch st := strings.TrimSpace(string(b)); st {
	case "up", "down":
		return st
	case "unknown":
		return "up" // point-to-point links (wireguard, tailscale) report unknown while up
	}
	return ""
}
