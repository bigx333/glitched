package transport

import (
	"encoding/binary"
	"maps"
	"net/netip"
	"slices"
	"syscall"
)

// scanInterfaces reads the kernel's address table once. net.Interface.Addrs
// reads all of it for each interface, so a scan through it costs the square
// of the interfaces: 1.6s for 1,000.
func scanInterfaces() [][]netip.Addr {
	tab, err := syscall.NetlinkRIB(syscall.RTM_GETADDR, syscall.AF_UNSPEC)
	if err != nil {
		return nil
	}
	msgs, err := syscall.ParseNetlinkMessage(tab)
	if err != nil {
		return nil
	}
	byIndex := map[uint32][]netip.Addr{}
	for _, m := range msgs {
		if m.Header.Type != syscall.RTM_NEWADDR || len(m.Data) < syscall.SizeofIfAddrmsg {
			continue
		}
		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			continue
		}
		// On a point-to-point link IFA_ADDRESS is the far end's, and
		// IFA_LOCAL this end's.
		var addr, local []byte
		for _, a := range attrs {
			switch a.Attr.Type {
			case syscall.IFA_ADDRESS:
				addr = a.Value
			case syscall.IFA_LOCAL:
				local = a.Value
			}
		}
		if local != nil {
			addr = local
		}
		if ip, ok := netip.AddrFromSlice(addr); ok {
			index := binary.NativeEndian.Uint32(m.Data[4:8])
			byIndex[index] = append(byIndex[index], ip.Unmap())
		}
	}
	return slices.Collect(maps.Values(byIndex))
}
