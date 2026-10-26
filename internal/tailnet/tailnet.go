// Package tailnet is how this machine is on the user's tailnet: through an
// installed Tailscale's local API, or through Glitched's own tsnet node
// when none is installed (docs/daemon/network.md).
package tailnet

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/paths"
	"tailscale.com/safesocket"
	"tailscale.com/tsnet"

	"glitched/internal/api"
)

func statusOf(mode api.TailnetMode, st *ipnstate.Status) api.Tailnet {
	s := api.Tailnet{Mode: mode, State: st.BackendState, AuthURL: st.AuthURL}
	for _, ip := range st.TailscaleIPs {
		if !s.IP.IsValid() || ip.Is4() && !s.IP.Is4() {
			s.IP = ip
		}
	}
	if st.Self != nil {
		s.Name = strings.TrimSuffix(st.Self.DNSName, ".")
		if u, ok := st.User[st.Self.UserID]; ok {
			s.User = u.LoginName
		}
	}
	if st.CurrentTailnet != nil {
		s.Tailnet = st.CurrentTailnet.Name
	}
	return s
}

// A lookup reads an installed Tailscale's status once, for every Detect
// that asks while it runs.
type lookup struct {
	done chan struct{}
	st   *ipnstate.Status
	err  error
}

// pending is the lookup under way. On macOS finding the local API runs
// lsof, which no context bounds, so a stalled one is waited on, not
// joined by another per call.
var pending struct {
	sync.Mutex
	l *lookup
}

// installedStatus asks the installed Tailscale's local API for its status;
// a test swaps it for one that stalls.
var installedStatus = func(ctx context.Context) (*ipnstate.Status, error) {
	lc := local.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: dialInstalled}}
	return lc.StatusWithoutPeers(ctx)
}

// lookupTimeout bounds a lookup's call to the local API, once found.
const lookupTimeout = 5 * time.Second

// Detect reads an installed Tailscale's status. It fails at once when none
// is installed, and with ctx's error when one doesn't answer in time,
// even while finding the local API stalls.
func Detect(ctx context.Context) (api.Tailnet, error) {
	pending.Lock()
	l := pending.l
	if l == nil {
		l = &lookup{done: make(chan struct{})}
		pending.l = l
		go func() {
			call, cancel := context.WithTimeout(context.Background(), lookupTimeout)
			l.st, l.err = installedStatus(call)
			cancel()
			pending.Lock()
			pending.l = nil
			pending.Unlock()
			close(l.done)
		}()
	}
	pending.Unlock()
	select {
	case <-l.done:
		if l.err != nil {
			return api.Tailnet{}, l.err
		}
		return statusOf(api.TailnetInstalled, l.st), nil
	case <-ctx.Done():
		return api.Tailnet{}, ctx.Err()
	}
}

// dialInstalled reaches an installed Tailscale's local API as local.Client
// does by default: the macOS app's TCP port, else tailscaled's socket. Its
// client keeps no connection alive past a call, as Detect runs often and
// none should outlive the engine.
func dialInstalled(ctx context.Context, _, _ string) (net.Conn, error) {
	if port, _, err := safesocket.LocalTCPPortAndToken(); err == nil {
		return new(net.Dialer).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	}
	return safesocket.ConnectContext(ctx, paths.DefaultTailscaledSocket())
}

// Node is Glitched's own tsnet node: a device on the tailnet in userspace,
// in this process, that needs no root and adds no network interface. It
// keeps its identity in its directory, so it is the same device on every
// start.
type Node struct {
	srv     *tsnet.Server
	lc      *local.Client
	control string
}

// Start brings the node up in the background. Until it is signed in, its
// Status has the URL to sign in at. control is a control server such as
// Headscale's, or "" for Tailscale's.
func Start(dir, hostname, control string) (*Node, error) {
	srv := &tsnet.Server{
		Dir:        dir,
		Hostname:   hostname,
		ControlURL: control,
		Logf:       func(string, ...any) {},
		UserLogf:   func(format string, args ...any) { slog.Info("tailnet: " + fmt.Sprintf(format, args...)) },
	}
	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("tailnet: start the embedded node: %w", err)
	}
	lc, err := srv.LocalClient()
	if err != nil {
		srv.Close()
		return nil, fmt.Errorf("tailnet: the embedded node's local API: %w", err)
	}
	return &Node{srv: srv, lc: lc, control: control}, nil
}

func (n *Node) Status(ctx context.Context) (api.Tailnet, error) {
	st, err := n.lc.StatusWithoutPeers(ctx)
	if err != nil {
		return api.Tailnet{Mode: api.TailnetEmbedded, Control: n.control}, err
	}
	s := statusOf(api.TailnetEmbedded, st)
	s.Control = n.control
	return s, nil
}

// Listen listens on the node's tailnet addresses; addr is ":port".
func (n *Node) Listen(network, addr string) (net.Listener, error) { return n.srv.Listen(network, addr) }

func (n *Node) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return n.srv.Dial(ctx, network, addr)
}

// WhoIs names the tailnet node at remoteAddr, a connection's remote
// address, and fails for an address no peer holds.
func (n *Node) WhoIs(ctx context.Context, remoteAddr string) (string, error) {
	who, err := n.lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(who.Node.Name, "."), nil
}

// Logout signs the node out, so the tailnet no longer lists it.
func (n *Node) Logout(ctx context.Context) error { return n.lc.Logout(ctx) }

func (n *Node) Close() error { return n.srv.Close() }
