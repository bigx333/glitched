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

// Dial refuses a host that resolved off the tailnet before the token is sent.
func (t Tailnet) Dial(ctx context.Context, _ api.ID) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", t.Address)
	if err != nil {
		return nil, err
	}
	if !onTailnet(c.RemoteAddr().(*net.TCPAddr).AddrPort().Addr()) {
		c.Close()
		return nil, fmt.Errorf("%s reached %s, which %w", t.Address, c.RemoteAddr(), errNotTailnet)
	}
	return c, nil
}

func (Tailnet) WhoIs(conn net.Conn) (Peer, error) {
	a, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok || !onTailnet(a.AddrPort().Addr()) {
		return Peer{}, fmt.Errorf("transport: %s %w", conn.RemoteAddr(), errNotTailnet)
	}
	return Peer{Name: a.IP.String()}, nil
}
