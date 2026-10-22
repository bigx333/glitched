package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"

	"glitched/internal/api"
)

// Tailnet is TCP over an installed Tailscale, or over loopback (network.md).
// Being on the tailnet is the route, not the authorization: a client presents
// Token on every upgrade, and the server checks it with Server.Authorize.
type Tailnet struct {
	Address string // host:port; a client may name the host, a listener must give its IP
	Token   string
}

// ErrRefused is a call's error when the server refused the client's token.
var ErrRefused = errors.New("transport: the server refused the token")

var tailscale = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// onTailnet reports whether ip is loopback or a Tailscale address. Those are
// the only places a token may cross, because nothing but WireGuard encrypts
// it.
func onTailnet(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return true
	}
	for _, p := range tailscale {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

var errNotTailnet = errors.New("is not a loopback or Tailscale address (100.64.0.0/10, fd7a:115c:a1e0::/48), the only places a token may cross without TLS")

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
// leaves by the default route, in cleartext, from a LAN address.
func (t Tailnet) route(c net.Conn) error {
	local, remote := ipOf(c.LocalAddr()), ipOf(c.RemoteAddr())
	switch {
	case !onTailnet(remote):
		return fmt.Errorf("%s is %s, which %w", t.Address, remote, errNotTailnet)
	case !inside(local, remote):
		return fmt.Errorf("%s: Tailscale doesn't route to %s, so the connection would leave from %s, outside WireGuard; is Tailscale up, and is that machine a peer on this tailnet?", t.Address, remote, local)
	}
	return nil
}

// inside reports whether a connection between a and b stays on loopback or
// runs between two Tailscale addresses.
func inside(a, b netip.Addr) bool {
	return onTailnet(a) && onTailnet(b) && a.IsLoopback() == b.IsLoopback()
}

func ipOf(a net.Addr) netip.Addr {
	ap, _ := netip.ParseAddrPort(a.String())
	return ap.Addr().Unmap()
}

func (Tailnet) WhoIs(conn net.Conn) (Peer, error) {
	local, remote := ipOf(conn.LocalAddr()), ipOf(conn.RemoteAddr())
	if !inside(local, remote) {
		return Peer{}, fmt.Errorf("transport: a connection from %s to %s is neither on loopback nor between Tailscale addresses", conn.RemoteAddr(), conn.LocalAddr())
	}
	return Peer{Name: remote.String()}, nil
}

// TailscaleIP is this machine's Tailscale address, IPv4 if it has one. It
// is false when Tailscale isn't up.
func TailscaleIP() (netip.Addr, bool) {
	addrs, _ := net.InterfaceAddrs()
	var v6 netip.Addr
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, _ := netip.AddrFromSlice(n.IP)
		if ip = ip.Unmap(); ip.IsLoopback() || !onTailnet(ip) {
			continue
		}
		if ip.Is4() {
			return ip, true
		}
		if !v6.IsValid() {
			v6 = ip
		}
	}
	return v6, v6.IsValid()
}
