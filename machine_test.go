package main

import (
	"context"
	"errors"
	"net"
	"strings"
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
// pairs a token, which another app then reaches it with, until removed.
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
	h.until("laptop's token", func() bool { return h.tt.HasText("Token for laptop, shown only this once") && h.tt.HasText("Remove") })
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

	h.click("Remove")
	h.tt.Frame()
	o.until("the removed token refused", func() bool {
		for _, e := range o.a.backend.Envs() {
			if _, refused := e.Status.(client.Refused); refused && e.Name == "desk" {
				return true
			}
		}
		return false
	})
	if ts, _ := daemon.Tokens(dir); len(ts) != 0 {
		t.Errorf("tokens.json keeps %+v after the removal", ts)
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

// Compact before cache expires defaults to on, saves to engine.json when
// toggled in the Agents card, and applies live without restarting the engine.
func TestTheAgentsCardTogglesCompactBeforeExpire(t *testing.T) {
	dir := tempDir(t)
	h := start(t, dir)
	h.tt.SetSize(1280, 1400)
	ticking(t, h)
	h.click("Settings")
	h.click("This machine")
	h.until("the Agents card", func() bool { return h.tt.HasText("Compact before cache expires") && h.a.backend.Machine().Embedded })

	cfg, err := daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CompactBeforeExpireEnabled() {
		t.Fatalf("CompactBeforeExpireEnabled() defaulted to false, want true")
	}

	h.click("Enable compact before cache expires")
	h.tt.Frame()
	cfg, err = daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CompactBeforeExpireEnabled() || !h.a.backend.Machine().Embedded {
		t.Fatalf("after toggle off: enabled=%v embedded=%v, want false, true", cfg.CompactBeforeExpireEnabled(), h.a.backend.Machine().Embedded)
	}

	h.click("Enable compact before cache expires")
	h.tt.Frame()
	cfg, err = daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CompactBeforeExpireEnabled() || !h.a.backend.Machine().Embedded {
		t.Fatalf("after toggle on: enabled=%v embedded=%v, want true, true", cfg.CompactBeforeExpireEnabled(), h.a.backend.Machine().Embedded)
	}
}

// Sync my skills to paired machines defaults to on, saves to engine.json
// when toggled in the Agents card, and leaves the engine embedded: only
// the app reads it.
func TestTheAgentsCardTogglesSyncSkills(t *testing.T) {
	dir := tempDir(t)
	h := start(t, dir)
	h.tt.SetSize(1280, 1600)
	ticking(t, h)
	h.click("Settings")
	h.click("This machine")
	h.until("the Agents card", func() bool {
		return h.tt.HasText("Sync my skills to paired machines") && h.a.backend.Machine().Embedded
	})
	syncs := func(want bool) {
		t.Helper()
		cfg, err := daemon.Defaults(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SyncSkillsEnabled() != want || !h.a.backend.Machine().Embedded {
			t.Fatalf("sync skills %v, embedded %v; want %v, true", cfg.SyncSkillsEnabled(), h.a.backend.Machine().Embedded, want)
		}
	}
	syncs(true)
	for _, want := range []bool{false, true} {
		h.click("Enable sync my skills to paired machines")
		h.tt.Frame()
		syncs(want)
	}
}

// Keep awake while agents work defaults to on, saves to engine.json when
// The Agents card's keep awake switch saves the setting and lets the
// engine embedded here go of its hold on the machine at once, and take it
// again, as an agent works, without restarting it.
func TestTheAgentsCardTogglesKeepAwake(t *testing.T) {
	dir := tempDir(t)
	held := fake.New(fake.Turn(false))
	held.HoldStart = true
	holds := make(chan bool, 4)
	h := startWith(t, dir, func(c *daemon.Config) {
		c.Providers["fake"] = held
		c.Inhibit = func(ctx context.Context) error {
			holds <- true
			<-ctx.Done()
			holds <- false
			return nil
		}
	})
	h.tt.SetSize(1280, 1400)
	ticking(t, h)
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	t.Cleanup(func() { ep.Close() })
	id := api.NewID()
	for _, b := range []api.Intent{api.ThreadCreate{Workspace: "/work/t3code", Title: "Busy", Model: fakeModel}, api.MessageDispatch{Text: "go", Mode: api.StartNow}} {
		if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	holdIs := func(want bool) {
		t.Helper()
		select {
		case got := <-holds:
			if got != want {
				t.Fatalf("the engine's hold went to %v, want %v", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("the engine's hold never went to %v", want)
		}
	}
	holdIs(true)
	h.click("Settings")
	h.click("This machine")
	h.until("the Agents card", func() bool { return h.tt.HasText("Keep awake while agents work") && h.a.backend.Machine().Embedded })
	for _, want := range []bool{false, true} {
		h.click("Enable keep awake while agents work")
		h.tt.Advance(time.Second)
		holdIs(want)
		cfg, err := daemon.Defaults(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.KeepAwakeEnabled() != want || !h.a.backend.Machine().Embedded {
			t.Fatalf("keep awake %v, embedded %v; want %v, true", cfg.KeepAwakeEnabled(), h.a.backend.Machine().Embedded, want)
		}
	}
	if held.Starts() != 1 {
		t.Fatalf("the agent started %d times, want once: the switch restarted the engine", held.Starts())
	}
}

// An engine another process runs keeps its provider log as it started
// with: once the switch is saved otherwise, the switch's row says how
// the engine keeps it, and the card says once when it applies the switch.
func TestAnAttachedEnginesProviderLogSwitchSaysWhenItApplies(t *testing.T) {
	dir := tempDir(t)
	cfg, err := daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProviderLog = true
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	engine(t, cfg, fake.New(fake.Turn(false)))
	h := start(t, dir)
	h.tt.SetSize(1280, 1400)
	ticking(t, h)
	h.click("Settings")
	h.click("This machine")
	const about = "Keep the shape of what each thread's agent CLI reads and writes"
	h.until("the attached engine's Agents card", func() bool {
		m := h.a.backend.Machine()
		return h.tt.HasText(about) && !m.Embedded && m.Engine != nil && m.Engine.ProviderLog
	})
	h.click("Enable provider log")
	h.until("the switch's row to say the log stays on", func() bool {
		m := h.a.backend.Machine()
		return h.tt.HasText("glitched serve keeps the provider log on.") && !m.Config.ProviderLog && m.Engine != nil && m.Engine.ProviderLog
	})
	notes, agents := 0, false
	for _, s := range h.tt.Texts() {
		agents = agents || s == "AGENTS"
		if agents && strings.Contains(s, "next starts") {
			notes++
		}
	}
	if notes != 1 {
		t.Errorf("the Agents card says when glitched serve applies a change %d times, want once: %q", notes, h.tt.Texts())
	}
}

// The provider log is off by default; its switch in the Agents card saves
// it to engine.json, and the engine embedded here applies it without a
// restart.
func TestTheAgentsCardTogglesTheProviderLog(t *testing.T) {
	dir := tempDir(t)
	h := start(t, dir)
	h.tt.SetSize(1280, 1400)
	ticking(t, h)
	h.click("Settings")
	h.click("This machine")
	h.until("the Agents card", func() bool { return h.tt.HasText("Provider log") && h.a.backend.Machine().Embedded })
	for _, want := range []bool{false, true, false} {
		cfg, err := daemon.Defaults(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ProviderLog != want || !h.a.backend.Machine().Embedded {
			t.Fatalf("the provider log is on %v, embedded %v; want %v, true", cfg.ProviderLog, h.a.backend.Machine().Embedded, want)
		}
		h.click("Enable provider log")
		h.tt.Advance(time.Second)
	}
}

// The Agents card's continue after a restart switch is off until the user
// turns it on, and saves the setting the engine reads as it next starts,
// without restarting the engine now, which would cut its agents off.
func TestTheAgentsCardTogglesContinueAfterARestart(t *testing.T) {
	dir := tempDir(t)
	held := fake.New(fake.Turn(false))
	held.HoldStart = true
	h := startWith(t, dir, func(c *daemon.Config) { c.Providers["fake"] = held })
	h.tt.SetSize(1280, 1400)
	ticking(t, h)
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	t.Cleanup(func() { ep.Close() })
	id := api.NewID()
	for _, b := range []api.Intent{api.ThreadCreate{Workspace: "/work/t3code", Title: "Busy", Model: fakeModel}, api.MessageDispatch{Text: "go", Mode: api.StartNow}} {
		if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	h.until("the agent starting", func() bool { return held.Starts() == 1 })
	h.click("Settings")
	h.click("This machine")
	h.until("the Agents card", func() bool { return h.tt.HasText("Continue agents after a restart") && h.a.backend.Machine().Embedded })
	for _, want := range []bool{true, false} {
		cfg, err := daemon.Defaults(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ContinueAfterRestart == want {
			t.Fatalf("continue after a restart is %v before the click, want %v", cfg.ContinueAfterRestart, !want)
		}
		h.click("Enable continue agents after a restart")
		h.tt.Advance(time.Second)
		if cfg, err = daemon.Defaults(dir); err != nil || cfg.ContinueAfterRestart != want || !h.a.backend.Machine().Embedded {
			t.Fatalf("continue after a restart %v (%v), embedded %v; want %v, embedded", cfg.ContinueAfterRestart, err, h.a.backend.Machine().Embedded, want)
		}
	}
	if snap, err := ep.Read(context.Background(), api.Query{Thread: id}); err != nil || snap.Threads[0].Run != api.RunStarting || held.Starts() != 1 {
		t.Fatalf("the agent's run is %v after the switch, started %d times; want it still starting, once", snap.Threads, held.Starts())
	}
}

// With no engine to ask, This machine reads Tailscale's status as the
// app's engine would: a test's, made up, never this machine's.
func TestThisMachineWithNoEngineReadsTheEnginesTailscale(t *testing.T) {
	p := &posts{wake: make(chan struct{}, 1)}
	a := testApp(t, tempDir(t), nil, p.post)
	a.backend.Inspect()
	deadline := time.Now().Add(10 * time.Second)
	for a.backend.Machine().Read.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("no status read")
		}
		select {
		case <-p.wake:
		case <-time.After(10 * time.Millisecond):
		}
		for p.step() {
		}
	}
	m := a.backend.Machine()
	if m.Status.Engine != nil || m.Status.Tailnet.User != "sam@example.com" || m.Status.Tailnet.Name != "studio.example.ts.net" {
		t.Fatalf("engine %v, tailnet %+v; want no engine and the made-up machine's tailnet", m.Status.Engine, m.Status.Tailnet)
	}
}
