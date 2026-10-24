//go:build !linux

package transport

import (
	"net"
	"net/netip"
)

func scanInterfaces() [][]netip.Addr {
	ifs, _ := net.Interfaces()
	all := make([][]netip.Addr, 0, len(ifs))
	for _, i := range ifs {
		as, _ := i.Addrs()
		var ips []netip.Addr
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok {
				ip, _ := netip.AddrFromSlice(n.IP)
				ips = append(ips, ip.Unmap())
			}
		}
		all = append(all, ips)
	}
	return all
}
