// Package transport carries the Endpoint and hub calls as JSON over HTTP, on
// whatever network a Transport provides.
package transport

import (
	"context"
	"fmt"
	"net"

	"glitched/internal/api"
)

// Transport is the network a node listens and dials on (network.md).
type Transport interface {
	Listen(ctx context.Context) (net.Listener, error)
	Dial(ctx context.Context, machine api.ID) (net.Conn, error)
	WhoIs(conn net.Conn) (Peer, error)
}

// Peer is who the network says is on the other end of a connection. It is
// the route, not the authorization.
type Peer struct {
	Machine api.ID // zero for a caller on this machine
	Name    string
}

// Loopback is a Transport confined to this machine: a unix socket, or TCP on
// 127.0.0.1. There is one machine, so Dial ignores which one is asked for.
type Loopback struct {
	Network string // "unix" or "tcp"
	Address string // a socket path, or 127.0.0.1:port
}

func (l Loopback) Listen(ctx context.Context) (net.Listener, error) {
	if l.Network == "tcp" {
		if host, _, err := net.SplitHostPort(l.Address); err != nil || !net.ParseIP(host).IsLoopback() {
			return nil, fmt.Errorf("transport: %s is not a loopback address", l.Address)
		}
	}
	var lc net.ListenConfig
	return lc.Listen(ctx, l.Network, l.Address)
}

func (l Loopback) Dial(ctx context.Context, _ api.ID) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, l.Network, l.Address)
}

func (Loopback) WhoIs(conn net.Conn) (Peer, error) {
	local := Peer{Name: "local"}
	if conn.LocalAddr().Network() == "unix" {
		return local, nil
	}
	if a, ok := conn.RemoteAddr().(*net.TCPAddr); ok && a.IP.IsLoopback() {
		return local, nil
	}
	return Peer{}, fmt.Errorf("transport: %s is not a loopback peer", conn.RemoteAddr())
}
