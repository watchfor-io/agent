package hostinfo

import (
	"net"
	"sort"
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

// addresses lists the routable addresses per interface: what an operator
// would grep for when a host shows up as "10.0.4.7" somewhere else.
func addresses() map[string][]string {
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
	return filterAddresses(in)
}

// filterAddresses drops loopback and down interfaces, loopback and
// link-local addresses, and orders the rest so the output is stable.
func filterAddresses(in []ifaceAddrs) map[string][]string {
	out := map[string][]string{}
	sort.Slice(in, func(i, j int) bool { return in[i].name < in[j].name })
	for _, i := range in {
		if len(out) == maxAddrInterfaces {
			break
		}
		if i.flags&net.FlagLoopback != 0 || i.flags&net.FlagUp == 0 {
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
