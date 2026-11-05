package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// ticking posts nothing every so often until the test ends, as the
// window's frames wake the This machine page to read the engine again.
func ticking(t *testing.T, h *harness) {
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(100 * time.Millisecond):
				h.p.post(func() {})
			}
		}
	}()
}

// The This machine page serves the app's own engine to the tailnet and
// pairs a token, which another app then reaches it with, until revoked.
func TestThisMachineServesAndPairs(t *testing.T) {
	dir := tempDir(t)
	h := start(t, dir)
	ticking(t, h)
	h.click("Settings")
	h.click("This machine")
	h.until("the engine's status", func() bool { return h.tt.HasText("Online") && h.a.backend.Machine().Embedded })
	if !h.tt.HasText(h.local().Name+" runs inside this app, and stops when it quits.") || !h.tt.HasText("Restart") {
		t.Errorf("the page shows %q, want the engine embedded with a Restart", h.tt.Texts())
	}
	if !h.tt.HasText("No tokens yet. A machine needs one to reach this engine.") {
		t.Errorf("the page shows %q, want no tokens", h.tt.Texts())
	}

	// The second change comes once the engine restarted for the first has
	// read engine.json, before it reports in, and must still be the one it
	// serves. The posts wait for the test to run them, so the engine
	// reporting in waits too.
	first, addr := freeAddr(t), freeAddr(t)
	if err := h.a.backend.Configure(func(c *daemon.Config) { c.Listen = []string{first} }); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		if c, err := net.Dial("tcp", first); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine never served %s", first)
		}
		h.p.run()
		time.Sleep(5 * time.Millisecond)
	}
	if h.a.backend.Machine().Embedded {
		t.Log("the restarted engine reported in before the second change; this run doesn't cover a change mid-restart")
	}
	if err := h.a.backend.Configure(func(c *daemon.Config) { c.Listen = []string{addr} }); err != nil {
		t.Fatal(err)
	}
	h.until("the restarted engine to accept on "+addr, func() bool {
		m := h.a.backend.Machine()
		if time.Since(m.Read) >= inspectEvery {
			h.a.backend.Inspect()
		}
		h.tt.Frame()
		return m.Embedded && len(m.Listeners) == 1 && m.Listeners[0].Accepting && h.local().Status == client.Status(client.Online{})
	})
	h.tt.Frame()
	if !h.tt.HasText("Accepting connections") || !h.tt.HasText(addr) {
		t.Errorf("the page shows %q, want %s accepting", h.tt.Texts(), addr)
	}
	if c, err := net.Dial("tcp", first); err == nil {
		c.Close()
		t.Errorf("the engine still serves %s, which the second change replaced", first)
	}

	h.a.pairLabel = "laptop"
	h.tt.Frame()
	h.click("Pair")
	h.until("laptop's token", func() bool { return h.tt.HasText("Token for laptop, shown only this once") && h.tt.HasText("Revoke") })
	token := h.a.paired.token
	h.tt.Advance(time.Second) // let the token's row fold in
	if !h.tt.HasText(token) {
		t.Fatalf("the page shows %q, want the token", h.tt.Texts())
	}
	if h.tt.HasText("Copy CLI command") {
		t.Errorf("the page offers a command for %s, which another machine can't reach", addr)
	}
	h.click("Copy token")
	h.tt.Frame()
	if h.tt.Clipboard() != token {
		t.Errorf("copied %q, want the token", h.tt.Clipboard())
	}
	h.click("Done")
	h.tt.Frame()
	if h.tt.HasText(token) {
		t.Errorf("the token still shows after Done")
	}

	o := start(t, tempDir(t))
	if err := o.addServer(client.Server{Label: "desk", Addr: addr, Token: token}); err != nil {
		t.Fatalf("another app couldn't reach this one with the token it paired: %v", err)
	}

	h.click("Revoke")
	h.tt.Frame()
	o.until("the revoked token refused", func() bool {
		for _, e := range o.a.backend.Envs() {
			if _, refused := e.Status.(client.Refused); refused && e.Name == "desk" {
				return true
			}
		}
		return false
	})
	if ts, _ := daemon.Tokens(dir); len(ts) != 0 {
		t.Errorf("tokens.json keeps %+v after the revoke", ts)
	}
}

// An engine another process runs shows as attached, with nothing to
// restart.
func TestThisMachineAttachesToAnotherProcess(t *testing.T) {
	dir := tempDir(t)
	engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	h := start(t, dir)
	h.click("Settings")
	h.click("This machine")
	h.until("the engine's status", func() bool { return h.tt.HasText("runs in another process") })
	if h.a.backend.Machine().Embedded || h.tt.HasText("Restart") {
		t.Errorf("an attached engine offers a restart: %q", h.tt.Texts())
	}
}

// The Agents card's limits are saved in engine.json, and the engine
// embedded here applies one at once, with no restart: a parent at the new
// limit of one child at work starts no second.
func TestTheAgentsCardAppliesALimitAtOnce(t *testing.T) {
	dir := tempDir(t)
	ad := fake.New(fake.Turn(true))
	h := startWith(t, dir, func(c *daemon.Config) { c.Providers = daemon.FakeProviders(ad) })
	ticking(t, h)
	h.click("Settings")
	h.click("This machine")
	h.until("the Agents card", func() bool { return h.tt.HasText("At work at once") && h.a.backend.Machine().Embedded })
	for range 4 { // the first Decrease is At work at once's, from 5
		h.click("Decrease")
	}
	h.tt.Frame()
	cfg, err := daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Children.Working != 1 || !h.a.backend.Machine().Embedded {
		t.Fatalf("engine.json's limits %+v, embedded %v; want 1 at work, and the engine not restarted", cfg.Children, h.a.backend.Machine().Embedded)
	}

	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	t.Cleanup(func() { ep.Close() })
	ad.Spawns(ep, "split", fake.Child{Title: "one", Task: "go"}, fake.Child{Title: "two", Task: "go"})
	parent, run := api.NewID(), api.NewID()
	for _, c := range []api.Command{
		{ID: api.NewID(), Target: parent, Body: api.ThreadCreate{Workspace: "/work/t3code", Title: "Split it", Model: fakeModel}},
		{ID: run, Target: parent, Body: api.MessageDispatch{Text: "split", Mode: api.StartNow}},
	} {
		if _, err := ep.Submit(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	var rej *api.RejectedError
	h.until("the second child's refusal", func() bool {
		answered, err := ad.Spawned(fake.ChildOf(run, 1))
		if answered && !errors.As(err, &rej) {
			t.Fatalf("the second child at work under a limit of 1: %v, want refused", err)
		}
		return answered
	})
}
