package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
)

func TestTailnetListensOnlyOnLoopbackOrTailscale(t *testing.T) {
	ok := []string{"127.0.0.1:0", "[::1]:0", "100.64.0.1:7465", "100.127.255.254:7465", "[fd7a:115c:a1e0::1]:7465"}
	refused := []string{"0.0.0.0:7465", "[::]:7465", ":7465", "8.8.8.8:7465", "192.168.1.10:7465", "100.128.0.1:7465", "[fd7a:115c:a1e1::1]:7465", "localhost:7465"}
	for _, addr := range ok {
		// This machine may not hold the address, so only the rule's refusal counts.
		ln, err := Tailnet{Address: addr}.Listen(context.Background())
		if errors.Is(err, errNotTailnet) {
			t.Errorf("%s refused: %v", addr, err)
		}
		if ln != nil {
			ln.Close()
		}
	}
	for _, addr := range refused {
		ln, err := Tailnet{Address: addr}.Listen(context.Background())
		if !errors.Is(err, errNotTailnet) {
			t.Errorf("%s: %v, want the tailnet rule's refusal", addr, err)
		}
		if ln != nil {
			ln.Close()
		}
	}
}

func TestAuthorizeRefusesWith401AndClosesARevokedSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	grant, revoke := context.WithCancel(ctx)
	var revoked atomic.Bool
	ep := &fakeEndpoint{reads: make(chan error, 2)}
	srv := &Server{Transport: Tailnet{}, Endpoint: ep, Authorize: func(r *http.Request) (context.Context, error) {
		if revoked.Load() || r.Header.Get("Authorization") != "Bearer good" {
			return nil, errors.New("unknown token")
		}
		return grant, nil
	}}
	ln, err := Tailnet{Address: "127.0.0.1:0"}.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() { done <- srv.Serve(ctx, ln) }()
	defer func() { cancel(); <-done }()
	addr := ln.Addr().String()

	bad := NewEndpointClient(Tailnet{Address: addr, Token: "bad"}, api.ID{})
	defer bad.Close()
	if _, err := bad.Info(ctx); !errors.Is(err, ErrRefused) {
		t.Fatalf("a wrong token: %v, want ErrRefused", err)
	}
	good := NewEndpointClient(Tailnet{Address: addr, Token: "good"}, api.ID{})
	defer good.Close()
	if _, err := good.Info(ctx); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() {
		_, err := good.Read(ctx, api.Query{Thread: blocks})
		blocked <- err
	}()
	<-ep.reads
	revoked.Store(true)
	revoke()
	if err := <-blocked; !errors.Is(err, errLost) {
		t.Fatalf("a call on a socket whose grant ended: %v, want the connection lost", err)
	}
	if _, err := good.Info(ctx); !errors.Is(err, ErrRefused) {
		t.Fatalf("redialling with a revoked token: %v, want ErrRefused", err)
	}
}

type routedConn struct {
	net.Conn
	local, remote net.Addr
}

func (c routedConn) LocalAddr() net.Addr  { return c.local }
func (c routedConn) RemoteAddr() net.Addr { return c.remote }

// route makes Tailnet's dials, per network, return a pipe whose ends claim
// the given local and remote addresses, as a route this machine lacks would.
// It returns what the far ends received, by network, once Tailnet closed
// them.
func route(t *testing.T, ends map[string][2]string) func() map[string]int {
	t.Helper()
	old := dialNet
	t.Cleanup(func() { dialNet = old })
	var (
		mu       sync.Mutex
		received = map[string]int{}
		far      sync.WaitGroup
	)
	dialNet = func(_ context.Context, network, _ string) (net.Conn, error) {
		e, ok := ends[network]
		if !ok {
			t.Errorf("dialled %s", network)
			return nil, errors.New("no route")
		}
		near, end := net.Pipe()
		far.Go(func() {
			n, _ := io.Copy(io.Discard, end)
			mu.Lock()
			received[network] += int(n)
			mu.Unlock()
		})
		at := func(s string) net.Addr { return net.TCPAddrFromAddrPort(netip.MustParseAddrPort(s)) }
		return routedConn{near, at(e[0]), at(e[1])}, nil
	}
	return func() map[string]int {
		far.Wait()
		mu.Lock()
		defer mu.Unlock()
		return received
	}
}

func TestTailnetSendsNoTokenOutsideWireGuard(t *testing.T) {
	// Tailscale has no peer at 100.101.2.3, so the kernel routes it by the
	// default route from the LAN address.
	received := route(t, map[string][2]string{
		"udp": {"192.168.1.5:40000", "100.101.2.3:7465"},
		"tcp": {"192.168.1.5:40000", "100.101.2.3:7465"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c := NewEndpointClient(Tailnet{Address: "100.101.2.3:7465", Token: "secret"}, api.ID{})
	_, err := c.Info(ctx)
	c.Close()
	if err == nil || !strings.Contains(err.Error(), "Tailscale doesn't route to 100.101.2.3") {
		t.Fatalf("a dial that left by the LAN: %v, want Tailscale doesn't route there", err)
	}
	if got := received(); got["tcp"] != 0 || got["udp"] != 0 {
		t.Fatalf("the far end received %v bytes", got)
	}
}

func TestTailnetDialsOnlyBetweenLoopbacksOrTailscaleAddresses(t *testing.T) {
	cases := []struct {
		name     string
		udp, tcp [2]string // local, remote
		ok       bool
	}{
		{"loopback", [2]string{"127.0.0.1:1", "127.0.0.1:7465"}, [2]string{"127.0.0.1:1", "127.0.0.1:7465"}, true},
		{"a peer", [2]string{"100.64.0.9:1", "100.70.0.1:7465"}, [2]string{"100.64.0.9:1", "100.70.0.1:7465"}, true},
		{"a peer over IPv6", [2]string{"[fd7a:115c:a1e0::9]:1", "[fd7a:115c:a1e0::1]:7465"}, [2]string{"[fd7a:115c:a1e0::9]:1", "[fd7a:115c:a1e0::1]:7465"}, true},
		{"a peer as IPv4-mapped", [2]string{"[::ffff:100.64.0.9]:1", "[::ffff:100.70.0.1]:7465"}, [2]string{"[::ffff:100.64.0.9]:1", "[::ffff:100.70.0.1]:7465"}, true},
		{"a name that resolved off the tailnet", [2]string{"192.168.1.5:1", "192.168.1.10:7465"}, [2]string{"192.168.1.5:1", "192.168.1.10:7465"}, false},
		{"IPv4-mapped off the tailnet", [2]string{"[::ffff:192.168.1.5]:1", "[::ffff:192.168.1.10]:7465"}, [2]string{"[::ffff:192.168.1.5]:1", "[::ffff:192.168.1.10]:7465"}, false},
		{"a route that changed after the probe", [2]string{"100.64.0.9:1", "100.70.0.1:7465"}, [2]string{"192.168.1.5:1", "100.70.0.1:7465"}, false},
		{"loopback to a Tailscale address", [2]string{"127.0.0.1:1", "100.70.0.1:7465"}, [2]string{"127.0.0.1:1", "100.70.0.1:7465"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			received := route(t, map[string][2]string{"udp": tc.udp, "tcp": tc.tcp})
			c, err := Tailnet{Address: tc.tcp[1], Token: "secret"}.Dial(context.Background(), api.ID{})
			if c != nil {
				c.Close()
			}
			if (err == nil) != tc.ok {
				t.Fatalf("dial %s from %s: %v, want ok=%v", tc.tcp[1], tc.tcp[0], err, tc.ok)
			}
			received()
		})
	}
}

func TestTailnetRefusesAnAddressNoPeerHoldsBeforeConnecting(t *testing.T) {
	received := route(t, map[string][2]string{"udp": {"192.168.1.5:40000", "100.101.2.3:7465"}})
	_, err := Tailnet{Address: "100.101.2.3:7465", Token: "secret"}.Dial(context.Background(), api.ID{})
	if err == nil {
		t.Fatal("dialled an address Tailscale doesn't route")
	}
	received()
}

func TestTailnetListenAddressForms(t *testing.T) {
	ok := []string{"[::ffff:100.64.0.1]:7465", "[::ffff:127.0.0.1]:0"}
	refused := []string{"[::ffff:8.8.8.8]:7465", "[::ffff:0.0.0.0]:7465", "[fe80::1%eth0]:7465", "[fd7a:115c:a1e0::1%tailscale0]:7465", "[fe80::1%lo]:7465"}
	for _, addr := range ok {
		ln, err := Tailnet{Address: addr}.Listen(context.Background())
		if errors.Is(err, errNotTailnet) {
			t.Errorf("%s refused: %v", addr, err)
		}
		if ln != nil {
			ln.Close()
		}
	}
	for _, addr := range refused {
		ln, err := Tailnet{Address: addr}.Listen(context.Background())
		if !errors.Is(err, errNotTailnet) {
			t.Errorf("%s: %v, want the tailnet rule's refusal", addr, err)
		}
		if ln != nil {
			ln.Close()
		}
	}
}

func TestTailnetServesOnlyLoopbackOrTailscalePeers(t *testing.T) {
	at := func(s string) net.Addr { return net.TCPAddrFromAddrPort(netip.MustParseAddrPort(s)) }
	cases := []struct {
		local, remote string
		ok            bool
	}{
		{"127.0.0.1:7465", "127.0.0.1:40000", true},
		{"100.64.0.1:7465", "100.70.0.2:40000", true},
		{"100.64.0.1:7465", "192.168.1.5:40000", false},
		{"100.64.0.1:7465", "127.0.0.1:40000", false},
	}
	for _, tc := range cases {
		near, far := net.Pipe()
		_, err := Tailnet{}.WhoIs(routedConn{near, at(tc.local), at(tc.remote)})
		near.Close()
		far.Close()
		if (err == nil) != tc.ok {
			t.Errorf("a socket from %s to %s: %v, want ok=%v", tc.remote, tc.local, err, tc.ok)
		}
	}
}

func TestTailnetPresentsItsTokenByValueOrPointer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv := &Server{Transport: Tailnet{}, Endpoint: &fakeEndpoint{}, Authorize: func(r *http.Request) (context.Context, error) {
		if r.Header.Get("Authorization") != "Bearer good" {
			return nil, errors.New("unknown token")
		}
		return context.Background(), nil
	}}
	ln, err := Tailnet{Address: "127.0.0.1:0"}.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() { done <- srv.Serve(ctx, ln) }()
	defer func() { cancel(); <-done }()
	tn := Tailnet{Address: ln.Addr().String(), Token: "good"}
	for name, tr := range map[string]Transport{"Tailnet": tn, "*Tailnet": &tn} {
		c := NewEndpointClient(tr, api.ID{})
		if _, err := c.Info(ctx); err != nil {
			t.Errorf("%s with the right token: %v", name, err)
		}
		c.Close()
	}
}

func TestTailnetServerNeedsAuthorize(t *testing.T) {
	for name, tr := range map[string]Transport{"Tailnet": Tailnet{}, "*Tailnet": &Tailnet{}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		ln, err := Tailnet{Address: "127.0.0.1:0"}.Listen(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = (&Server{Transport: tr, Endpoint: &fakeEndpoint{}}).Serve(ctx, ln)
		cancel()
		ln.Close()
		if err == nil {
			t.Errorf("a %s server without Authorize served", name)
		}
	}
}
