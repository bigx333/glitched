package daemon

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
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
	detect = func(context.Context) (api.Tailnet, bool) { return api.Tailnet{}, false }
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

	wrong := transport.NewEndpointClient(transport.Embedded{Node: laptop, Address: addr, Token: "wrong"}, api.ID{})
	defer wrong.Close()
	if _, err := wrong.Info(ctx); !errors.Is(err, transport.ErrRefused) {
		t.Fatalf("a wrong token: %v, want ErrRefused", err)
	}
}
