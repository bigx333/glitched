package transport

import (
	"context"
	"fmt"
	"net"
	"time"

	"glitched/internal/api"
)

// A Node is a tailnet device that runs in this process, in userspace, as
// tailnet.Node does.
type Node interface {
	Listen(network, addr string) (net.Listener, error)
	Dial(ctx context.Context, network, addr string) (net.Conn, error)
	WhoIs(ctx context.Context, remoteAddr string) (string, error)
}

// Embedded is TCP over this process's own tailnet node, for a machine
// without Tailscale. The node carries every byte inside WireGuard, so no
// route check applies. Being on the tailnet is still only the route: the
// token's handshake authorizes, as on Tailnet.
type Embedded struct {
	Node    Node
	Address string // host:port to dial; ":port" to listen on the node's every address
	Token   string
}

func (t Embedded) key() Key { return KeyOf(t.Token) }

func (t Embedded) Listen(context.Context) (net.Listener, error) {
	return t.Node.Listen("tcp", t.Address)
}

// Dial refuses a connection that doesn't reach a peer on the tailnet
// before anything is sent: for an address no peer holds, the node falls
// back to an ordinary connection, outside WireGuard.
func (t Embedded) Dial(ctx context.Context, _ api.ID) (net.Conn, error) {
	c, err := t.Node.Dial(ctx, "tcp", t.Address)
	if err != nil {
		return nil, err
	}
	if _, err := t.Node.WhoIs(ctx, c.RemoteAddr().String()); err != nil {
		c.Close()
		return nil, fmt.Errorf("transport: %s isn't a peer on the tailnet, so the connection would leave outside WireGuard: %w", t.Address, err)
	}
	return c, nil
}

// WhoIs names the peer the node says holds the connection's remote
// address, and refuses one it doesn't know.
func (t Embedded) WhoIs(conn net.Conn) (Peer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	name, err := t.Node.WhoIs(ctx, conn.RemoteAddr().String())
	if err != nil {
		return Peer{}, err
	}
	return Peer{Name: name}, nil
}
