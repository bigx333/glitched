package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"

	"glitched/internal/api"
)

// Tailnet is TCP over an installed Tailscale, or over loopback (network.md).
// Being on the tailnet is the route, not the authorization: a client presents
// Token on every upgrade, and the server checks it with Server.Authorize.
type Tailnet struct {
	Address string // host:port; a client may name the host, a listener must give its IP
	Token   string
}

// credentialed is a Transport whose clients present a token on every
// upgrade, so its servers must check one.
type credentialed interface{ header() http.Header }

func (t Tailnet) header() http.Header {
	return http.Header{"Authorization": {"Bearer " + t.Token}}
}

// ErrRefused is a call's error when the server refused the client's token.
var ErrRefused = errors.New("transport: the server refused the token")

var (
	tailscale4 = netip.MustParsePrefix("100.64.0.0/10")
	tailscale6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
	// A subnet router forwards a 4via6 address to its LAN as plain IPv4.
	via6 = netip.MustParsePrefix("fd7a:115c:a1e0:b1a::/64")
)

// onTailnet reports whether ip is loopback or a Tailscale address. Those are
// the only places a token may cross, because nothing but WireGuard encrypts
// it.
func onTailnet(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || tailscale4.Contains(ip) || tailscale6.Contains(ip) && !via6.Contains(ip)
}

// interfaceAddrs lists each network interface's addresses; a test swaps it
// for interfaces this machine lacks.
var interfaceAddrs = func() [][]netip.Addr {
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

var errNotTailnet = errors.New("is not a loopback or Tailscale address (100.64.0.0/10, or fd7a:115c:a1e0::/48 outside 4via6's fd7a:115c:a1e0:b1a::/64), the only places a token may cross without TLS")

func (t Tailnet) Listen(ctx context.Context) (net.Listener, error) {
	host, _, err := net.SplitHostPort(t.Address)
	if err != nil {
		return nil, fmt.Errorf("transport: %w", err)
	}
	if ip, err := netip.ParseAddr(host); err != nil || !onTailnet(ip) {
		return nil, fmt.Errorf("transport: %s %w", t.Address, errNotTailnet)
	}
	var lc net.ListenConfig
	return lc.Listen(ctx, "tcp", t.Address)
}

// dialNet is how Tailnet dials; a test swaps it for routes this machine lacks.
var dialNet = new(net.Dialer).DialContext

// Dial refuses a host that resolved off the tailnet, or that Tailscale
// doesn't route to, before the token is sent.
func (t Tailnet) Dial(ctx context.Context, _ api.ID) (net.Conn, error) {
	// A UDP connect picks the route and sends nothing, so an address no peer
	// holds is refused before a SYN leaves by the default route.
	probe, err := dialNet(ctx, "udp", t.Address)
	if err != nil {
		return nil, err
	}
	err = t.route(probe)
	probe.Close()
	if err != nil {
		return nil, err
	}
	c, err := dialNet(ctx, "tcp", t.Address)
	if err != nil {
		return nil, err
	}
	if err := t.route(c); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// route refuses c unless it stays inside WireGuard or on this machine. On
// Linux, Tailscale routes only its current peers; any other 100.x address
// leaves by the default route, in cleartext, from a LAN address that may
// itself be in 100.64.0.0/10 when the ISP uses carrier-grade NAT.
func (t Tailnet) route(c net.Conn) error {
	local, remote := ipOf(c.LocalAddr()), ipOf(c.RemoteAddr())
	switch {
	case !onTailnet(remote):
		return fmt.Errorf("%s is %s, which %w", t.Address, remote, errNotTailnet)
	case !inside(local, remote):
		return fmt.Errorf("%s: Tailscale doesn't route to %s, so the connection would leave from %s, which isn't on the Tailscale interface; is Tailscale up, in kernel mode, and is that machine a peer on this tailnet?", t.Address, remote, local)
	}
	return nil
}

// inside reports whether a connection from local to remote stays on loopback
// or runs inside WireGuard. A Tailscale-range local address isn't proof of
// that; it must be on the interface Tailscale runs.
func inside(local, remote netip.Addr) bool {
	switch {
	case !onTailnet(local) || !onTailnet(remote) || local.IsLoopback() != remote.IsLoopback():
		return false
	case local.IsLoopback():
		return true
	}
	return slices.Contains(tailscaleAddrs(), local)
}

// tailscaleAddrs are the addresses of each interface that holds one in
// fd7a:115c:a1e0::/48. Tailscale gives every node such an address; a
// carrier-grade NAT link or another overlay in 100.64.0.0/10 has none.
func tailscaleAddrs() []netip.Addr {
	var out []netip.Addr
	for _, addrs := range interfaceAddrs() {
		if slices.ContainsFunc(addrs, tailscale6.Contains) {
			out = append(out, addrs...)
		}
	}
	return out
}

func ipOf(a net.Addr) netip.Addr {
	ap, _ := netip.ParseAddrPort(a.String())
	return ap.Addr().Unmap()
}

func (Tailnet) WhoIs(conn net.Conn) (Peer, error) {
	local, remote := ipOf(conn.LocalAddr()), ipOf(conn.RemoteAddr())
	if !inside(local, remote) {
		return Peer{}, fmt.Errorf("transport: a connection from %s to %s is neither on loopback nor from a Tailscale address to this machine's Tailscale interface", conn.RemoteAddr(), conn.LocalAddr())
	}
	return Peer{Name: remote.String()}, nil
}

// TailscaleIP is this machine's Tailscale address, IPv4 if it has one. It
// is false when Tailscale isn't up in kernel mode.
func TailscaleIP() (netip.Addr, bool) {
	var v6 netip.Addr
	for _, ip := range tailscaleAddrs() {
		switch {
		case tailscale4.Contains(ip):
			return ip, true
		case tailscale6.Contains(ip) && !v6.IsValid():
			v6 = ip
		}
	}
	return v6, v6.IsValid()
}
