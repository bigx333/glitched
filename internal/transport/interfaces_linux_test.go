package transport

import (
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestScanFindsWhatEachInterfaceHolds(t *testing.T) {
	ifs, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, i := range ifs {
		as, err := i.Addrs()
		if err != nil {
			t.Fatal(err)
		}
		var ips []netip.Addr
		for _, a := range as {
			ip, _ := netip.AddrFromSlice(a.(*net.IPNet).IP)
			ips = append(ips, ip.Unmap())
		}
		if len(ips) > 0 {
			want = append(want, joined(ips))
		}
	}
	var got []string
	for _, ips := range scanInterfaces() {
		got = append(got, joined(ips))
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("one read of the address table found\n%s\nwant, from each interface's own read,\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func joined(ips []netip.Addr) string {
	ips = slices.Clone(ips)
	slices.SortFunc(ips, netip.Addr.Compare)
	var s []string
	for _, ip := range ips {
		s = append(s, ip.String())
	}
	return strings.Join(s, " ")
}
