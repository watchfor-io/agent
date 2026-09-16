package hostinfo

import (
	"net"
	"sort"
	"strings"
)

// Caps keep a host with hundreds of veth/bridge addresses from turning the
// facts document into a dump; the dashboard shows the first few anyway.
const (
	maxAddrInterfaces = 32
	maxAddrsPerIface  = 8
)

type ifaceAddrs struct {
	name  string
	flags net.Flags
	addrs []net.IP
}

// virtualIface reports interface names that belong to container and VM
// plumbing (bridges, veth pairs, CNI, libvirt) rather than to the host's
// own connectivity. A node running a hundred containers has a hundred of
// these; none of them is "the host's address". The interface carrying the
// primary address is always kept, whatever its name (a WireGuard-only
// host reaches the server through wg0).
func virtualIface(name string) bool {
	for _, p := range []string{"docker", "br-", "veth", "cni", "flannel", "cali", "tunl", "vxlan", "virbr", "vnet", "lxc", "lxd", "kube", "dummy", "ovs", "tap"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// addresses lists the routable addresses per interface: what an operator
// would grep for when a host shows up as "10.0.4.7" somewhere else. keep
// names an interface that is never filtered out.
func addresses(keep string) map[string][]string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var in []ifaceAddrs
	for _, i := range ifaces {
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		e := ifaceAddrs{name: i.Name, flags: i.Flags}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				e.addrs = append(e.addrs, n.IP)
			}
		}
		in = append(in, e)
	}
	return filterAddresses(in, keep)
}

// filterAddresses drops loopback and down interfaces, loopback and
// link-local addresses, and orders the rest so the output is stable.
func filterAddresses(in []ifaceAddrs, keep string) map[string][]string {
	out := map[string][]string{}
	// The kept (primary) interface first, so the caps never drop it.
	sort.SliceStable(in, func(i, j int) bool {
		if (in[i].name == keep) != (in[j].name == keep) {
			return in[i].name == keep
		}
		return in[i].name < in[j].name
	})
	for _, i := range in {
		if len(out) == maxAddrInterfaces {
			break
		}
		if i.flags&net.FlagLoopback != 0 || i.flags&net.FlagUp == 0 {
			continue
		}
		if i.name != keep && virtualIface(i.name) {
			continue
		}
		var keep []string
		for _, ip := range i.addrs {
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
				continue
			}
			keep = append(keep, ip.String())
			if len(keep) == maxAddrsPerIface {
				break
			}
		}
		if len(keep) > 0 {
			out[i.name] = keep
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
