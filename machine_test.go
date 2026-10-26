package main

import (
	"net"
	"testing"
	"time"

	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
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
	if err := h.a.backend.SetListen([]string{first}); err != nil {
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
	if err := h.a.backend.SetListen([]string{addr}); err != nil {
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
