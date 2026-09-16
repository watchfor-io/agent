package hostinfo

import (
	"net"
	"testing"
)

func TestPrimaryAddressShape(t *testing.T) {
	ip4, ip6, iface := primaryAddress("https://ingest.watchfor.io")
	if ip4 == "" && ip6 == "" {
		t.Skip("no route to anywhere on this machine")
	}
	for _, s := range []string{ip4, ip6} {
		if s == "" {
			continue
		}
		ip := net.ParseIP(s)
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			t.Fatalf("bad primary address %q", s)
		}
	}
	if iface == "" {
		t.Fatalf("no interface found for %s / %s", ip4, ip6)
	}
	if got := ifaceHolding(net.ParseIP("192.0.2.1")); got != "" {
		t.Fatalf("documentation address must not match an interface, got %q", got)
	}
}

func TestVirtualInterfacesFilteredButPrimaryKept(t *testing.T) {
	up := net.FlagUp
	in := []ifaceAddrs{
		{name: "eth0", flags: up, addrs: []net.IP{net.ParseIP("10.0.1.5")}},
		{name: "docker0", flags: up, addrs: []net.IP{net.ParseIP("172.17.0.1")}},
		{name: "veth1a2b", flags: up, addrs: []net.IP{net.ParseIP("172.17.0.2")}},
		{name: "wg0", flags: up, addrs: []net.IP{net.ParseIP("10.8.0.2")}},
		{name: "br-x", flags: up, addrs: []net.IP{net.ParseIP("172.18.0.1")}},
	}
	got := filterAddresses(in, "wg0")
	if _, ok := got["docker0"]; ok {
		t.Fatal("docker0 kept")
	}
	if _, ok := got["veth1a2b"]; ok {
		t.Fatal("veth kept")
	}
	if got["wg0"] == nil || got["eth0"] == nil {
		t.Fatalf("real interfaces dropped: %v", got)
	}
	// A primary interface with a "virtual" name is still kept.
	got = filterAddresses(in, "docker0")
	if got["docker0"] == nil {
		t.Fatalf("primary docker0 dropped: %v", got)
	}
}
