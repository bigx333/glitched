package daemon

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"

	"glitched/internal/api"
	"glitched/internal/provider/fake"
	"glitched/internal/tailnet"
	"glitched/internal/transport"
)

// control runs a tailnet in this process, as Headscale would serve one,
// and returns its control server's URL.
func control(t *testing.T) string {
	t.Helper()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })
	c := &testcontrol.Server{
		DERPMap:        integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1"),
		DNSConfig:      &tailcfg.DNSConfig{Proxied: true},
		MagicDNSDomain: "tail-scale.ts.net",
		Logf:           logger.Discard,
	}
	c.HTTPTestServer = httptest.NewUnstartedServer(c)
	c.HTTPTestServer.Start()
	t.Cleanup(c.HTTPTestServer.Close)
	return c.HTTPTestServer.URL
}

// notInstalled pretends this machine has no Tailscale.
func notInstalled(t *testing.T) {
	was := detect
	detect = func(context.Context) (api.Tailnet, error) { return api.Tailnet{}, errors.New("no tailscaled") }
	t.Cleanup(func() { detect = was })
}

func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !ok(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// Without Tailscale, an engine told to serve the tailnet runs its own
// node, signed in to the control server it names. A machine without
// Tailscale either reaches it through a node of its own with a token the
// engine paired, and is refused with another.
func TestAnEngineWithoutTailscaleServesOnItsOwnNode(t *testing.T) {
	notInstalled(t)
	url := control(t)
	dir := stateDir(t)
	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Epoch: 1, Tailnet: true, Control: url, Adapter: fake.New(fake.Turn(true))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	var tn api.Tailnet
	until(t, "the engine's node on the tailnet", func() bool {
		env, err := d.Endpoint().Info(context.Background())
		tn = env.Tailnet
		return err == nil && tn.Serving != ""
	})
	if tn.Mode != api.TailnetEmbedded || tn.Control != url || !tn.IP.IsValid() || tn.Name == "" {
		t.Fatalf("the engine reports %+v, want its own node on %s with an address and a MagicDNS name", tn, url)
	}

	laptop, err := tailnet.Start(t.TempDir(), "laptop", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { laptop.Close() })
	until(t, "the laptop's node on the tailnet", func() bool {
		st, err := laptop.Status(context.Background())
		return err == nil && st.Running()
	})

	addr := net.JoinHostPort(tn.IP.String(), Port)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := transport.NewEndpointClient(transport.Embedded{Node: laptop, Address: addr, Token: token}, api.ID{})
	defer c.Close()
	env, err := c.Info(ctx)
	if err != nil {
		t.Fatalf("the laptop couldn't reach the engine at %s: %v", addr, err)
	}
	if env.Tailnet.Serving != tn.Serving {
		t.Errorf("over the tailnet the engine says it serves at %q, want %q", env.Tailnet.Serving, tn.Serving)
	}

	// An address no peer holds would be reached outside WireGuard.
	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	off := transport.NewEndpointClient(transport.Embedded{Node: laptop, Address: plain.Addr().String(), Token: token}, api.ID{})
	defer off.Close()
	if _, err := off.Info(ctx); err == nil || !strings.Contains(err.Error(), "isn't a peer on the tailnet") {
		t.Fatalf("dialing %s, off the tailnet: %v, want it refused", plain.Addr(), err)
	}

	wrong := transport.NewEndpointClient(transport.Embedded{Node: laptop, Address: addr, Token: "wrong"}, api.ID{})
	defer wrong.Close()
	if _, err := wrong.Info(ctx); !errors.Is(err, transport.ErrRefused) {
		t.Fatalf("a wrong token: %v, want ErrRefused", err)
	}

	// An engine without Tailscale that uploads to the hub on the tailnet
	// reaches it through a node of its own.
	hostDir := stateDir(t)
	host, err := Start(context.Background(), Config{Dir: hostDir, Epoch: 1, HubServer: addr, HubToken: token, Control: url, Adapter: fake.New(fake.Turn(true))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close() })
	if host.Node() == nil {
		t.Fatal("the uploading engine runs no node of its own")
	}
	until(t, "the uploading engine's node on the tailnet", func() bool {
		env, err := host.Endpoint().Info(context.Background())
		return err == nil && env.Tailnet.Running()
	})
	if env, _ := host.Endpoint().Info(ctx); env.Tailnet.Serving != "" {
		t.Errorf("an engine that only uploads says it serves at %q, where nothing listens", env.Tailnet.Serving)
	}
	thread := api.NewID()
	create := api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{Workspace: "/w", Title: "up", Model: api.ModelRef{Provider: "fake", Model: "fake"}}}
	if _, err := host.Endpoint().Submit(ctx, create); err != nil {
		t.Fatal(err)
	}
	hubc := transport.NewHubClient(Socket(dir), api.ID{})
	defer hubc.Close()
	until(t, "the thread on the hub", func() bool {
		evs, err := hubc.Events(ctx, thread, api.Cursor{}, 10)
		return err == nil && len(evs) > 0
	})
}

// A Tailscale slow to answer at start is there: the engine waits for it
// rather than running a node of its own beside it.
func TestASlowTailscaleIsNotMissing(t *testing.T) {
	was := detect
	detect = func(ctx context.Context) (api.Tailnet, error) { <-ctx.Done(); return api.Tailnet{}, ctx.Err() }
	t.Cleanup(func() { detect = was })
	d, err := Start(context.Background(), Config{Dir: stateDir(t), Hub: true, Epoch: 1, Tailnet: true, Adapter: fake.New(fake.Turn(true))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if d.Node() != nil {
		t.Fatal("the engine started a node of its own beside a Tailscale slow to answer")
	}
}

// Only this machine learns the login URL: whoever opens it can sign the
// machine in to their own account.
func TestOnlyThisMachineSeesTheLoginURL(t *testing.T) {
	was := detect
	detect = func(context.Context) (api.Tailnet, error) {
		return api.Tailnet{Mode: api.TailnetInstalled, State: "NeedsLogin", AuthURL: "https://login.example/a"}, nil
	}
	t.Cleanup(func() { detect = was })
	d, err := Start(context.Background(), Config{Dir: stateDir(t), Hub: true, Epoch: 1, Adapter: fake.New(fake.Turn(true))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ctx := context.Background()
	local, _ := d.Endpoint().Info(ctx)
	remote, _ := endpoint{d.host, d, true}.Info(ctx)
	if local.Tailnet.AuthURL == "" || remote.Tailnet.AuthURL != "" || remote.Tailnet.State != "NeedsLogin" {
		t.Fatalf("this machine sees %+v and a remote peer %+v; want the URL only here", local.Tailnet, remote.Tailnet)
	}
}
