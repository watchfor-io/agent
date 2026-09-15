package hostinfo

import (
	"net"
	"reflect"
	"testing"
)

func TestFilterAddresses(t *testing.T) {
	in := []ifaceAddrs{
		{name: "lo", flags: net.FlagUp | net.FlagLoopback, addrs: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}},
		{name: "eth0", flags: net.FlagUp, addrs: []net.IP{net.ParseIP("10.0.4.7"), net.ParseIP("fe80::1"), net.ParseIP("2001:db8::7")}},
		{name: "eth1", flags: 0, addrs: []net.IP{net.ParseIP("192.168.9.9")}}, // down
		{name: "wg0", flags: net.FlagUp, addrs: []net.IP{net.ParseIP("10.50.0.2")}},
	}
	got := filterAddresses(in)
	want := map[string][]string{"eth0": {"10.0.4.7", "2001:db8::7"}, "wg0": {"10.50.0.2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if filterAddresses(nil) != nil {
		t.Fatal("no interfaces must be nil, so the facts document omits the key")
	}
}
