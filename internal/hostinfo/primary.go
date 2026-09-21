//go:build linux

package hostinfo

import (
	"net"
	"strings"
	"time"
)

// primaryAddress asks the kernel which source address it would pick to
// reach target (the server's host name, or a well-known public address
// when unknown). A UDP "connection" sends nothing on the wire; it only
// resolves the route. IPv4 and IPv6 are asked separately so a dual-stack
// host reports both, and the interface holding the IPv4 (else IPv6)
// address is named.
func primaryAddress(target string) (ip4, ip6, iface string) {
	host := target
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		host = "1.1.1.1"
	}
	ip4 = sourceFor("udp4", host, "1.1.1.1")
	ip6 = sourceFor("udp6", host, "2606:4700:4700::1111")
	primary := ip4
	if primary == "" {
		primary = ip6
	}
	if primary != "" {
		iface = ifaceHolding(net.ParseIP(primary))
	}
	return ip4, ip6, iface
}

// sourceFor returns the local address the kernel would use for network
// (udp4/udp6) towards host, falling back to a public address when host
// does not resolve in that family. "" when the host has no route at all.
func sourceFor(network, host, fallback string) string {
	d := net.Dialer{Timeout: 500 * time.Millisecond}
	for _, h := range []string{host, fallback} {
		c, err := d.Dial(network, net.JoinHostPort(h, "443"))
		if err != nil {
			continue
		}
		la, _ := c.LocalAddr().(*net.UDPAddr)
		_ = c.Close() // nothing was sent; only the chosen source address mattered
		if la == nil || la.IP == nil || la.IP.IsLoopback() || la.IP.IsUnspecified() {
			continue
		}
		return la.IP.String()
	}
	return ""
}

func ifaceHolding(ip net.IP) string {
	if ip == nil {
		return ""
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, i := range ifaces {
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
				return i.Name
			}
		}
	}
	return ""
}
