package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// thisMachine opens Settings › This machine once the engine's status reads.
func thisMachine(t *testing.T, h *harness) {
	t.Helper()
	ticking(t, h)
	h.tt.SetSize(1320, 2000)
	h.click("Settings")
	h.click("This machine")
	h.until("the engine's status", func() bool { return h.tt.HasText("CHILDREN FROM OTHER MACHINES") && h.a.backend.Machine().Embedded })
}

func ceilingOf(t *testing.T, dir string) *api.Access {
	t.Helper()
	cfg, err := daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.ChildrenFromOtherMachines
}

// On B, Children from other machines is off until chosen, and Full warns
// what an agent at Full can do here. The engine embedded here applies it
// without a restart, as it reads it at each link's create.
func TestChildrenFromOtherMachinesWarnsAtFull(t *testing.T) {
	dir := tempDir(t)
	h := start(t, dir)
	thisMachine(t, h)
	if ceilingOf(t, dir) != nil || h.tt.HasText("Mint a link") {
		t.Fatalf("children from other machines are on, or a link can be minted, before they're chosen: %v", ceilingOf(t, dir))
	}
	shoot(t, h, "b-off")
	envBefore := h.local()
	h.click(accessFull)
	h.until("the Full warning", func() bool { return h.tt.HasText(fullWarning) })
	if c := ceilingOf(t, dir); c == nil || *c != api.AccessFull {
		t.Fatalf("engine.json holds %v, want full", c)
	}
	shoot(t, h, "b-full-warning")
	h.click(accessAuto)
	h.until("the warning gone", func() bool { return !h.tt.HasText(fullWarning) })
	if c := ceilingOf(t, dir); c == nil || *c != api.AccessAutoEdits {
		t.Fatalf("engine.json holds %v, want auto edits", c)
	}
	if !h.a.backend.Machine().Embedded || h.local() != envBefore || h.local().Status != client.Status(client.Online{}) {
		t.Error("choosing the setting restarted the engine, which ends the turns its agents run")
	}
	h.click("Off")
	h.until("off", func() bool { return ceilingOf(t, dir) == nil && !h.tt.HasText("Mint a link") })
}

// On B, a link is minted for a label, at most at the setting, its token
// shown once with the line to run on A, listed, and revoked.
func TestMintingALinkShowsItsTokenOnceAndRevokes(t *testing.T) {
	dir := tempDir(t)
	auto := api.AccessAutoEdits
	if _, err := daemon.Edit(dir, func(c *daemon.Config) error { c.ChildrenFromOtherMachines = &auto; return nil }); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	thisMachine(t, h)
	h.a.mint.label, h.a.mint.access, h.a.mint.working = "laptop", int(api.AccessAutoEdits), 2
	h.tt.Frame()
	h.click("Mint a link")
	h.until("the link's token", func() bool {
		return h.tt.HasText("Link for laptop, at " + accessAuto + " at most. Its token, shown only this once")
	})
	token := h.a.minted.token
	h.tt.Advance(time.Second)
	if token == "" || !h.tt.HasText(token) {
		t.Fatalf("the page shows %q, want the token", h.tt.Texts())
	}
	ts, err := daemon.Tokens(dir)
	if err != nil || len(ts) != 1 || ts[0].Link == nil || ts[0].Link.Access != api.AccessAutoEdits || ts[0].Link.Working != 2 || ts[0].Label != "laptop" {
		t.Fatalf("tokens.json holds %+v (%v), want laptop's link at auto edits, 2 at work", ts, err)
	}
	if !h.tt.HasText("Turn on Serve on the tailnet for laptop to reach this machine. Then on laptop, add it with this token in Settings › This machine › Machines this one delegates to.") || h.tt.HasText("Copy command") {
		t.Errorf("the page shows %q, want how laptop reaches this machine once it serves, and no command to copy before it does", h.tt.Texts())
	}
	if h.tt.HasText("No tokens yet. A machine needs one to reach this engine.") == false {
		t.Error("the Tokens card lists the link, which reaches no more than its scope")
	}
	shoot(t, h, "b-minted")
	h.click("Copy token")
	h.tt.Frame()
	if c := h.tt.Clipboard(); c != token {
		t.Errorf("copied %q, want the token", c)
	}
	h.click("Done")
	h.tt.Frame()
	if h.tt.HasText(token) {
		t.Error("the token still shows after Done")
	}
	if !h.tt.HasText(accessAuto + " at most, 2 at work at once. Minted now.") {
		t.Errorf("the page shows %q, want laptop's link listed", h.tt.Texts())
	}
	h.tt.Advance(time.Second)
	shoot(t, h, "b-links")
	h.click("Revoke")
	h.until("the link revoked", func() bool {
		ts, _ := daemon.Tokens(dir)
		return len(ts) == 0 && !h.tt.HasText("laptop")
	})

	h.a.mint.label = "tablet"
	h.tt.Frame()
	h.click("Mint a link")
	h.until("tablet's token", func() bool { return h.a.minted.token != "" && h.tt.HasText(h.a.minted.token) })
	token = h.a.minted.token
	h.tt.Advance(time.Second)
	h.click("Revoke")
	h.until("the revoked link's token gone", func() bool { return !h.tt.HasText(token) && h.a.minted.token == "" })
}

// In the narrowest window, the setting's choices, minting and adding a
// machine stay inside it.
func TestTheLinkCardsFitANarrowWindow(t *testing.T) {
	dir := tempDir(t)
	full := api.AccessFull
	if _, err := daemon.Edit(dir, func(c *daemon.Config) error { c.ChildrenFromOtherMachines = &full; return nil }); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	thisMachine(t, h)
	h.tt.SetSize(760, 2400)
	h.tt.Advance(time.Second)
	h.tt.Frame()
	fits(t, h, 760, "Off", "Full access", fullWarning, "Mint a link", "Add machine", "The link's token")
	shoot(t, h, "b-narrow-settings")
}

// On A, a machine is added with the link it minted for this one, refused
// with a whole-engine token, listed, and removed, which leaves the link
// valid on that machine.
func TestAddingAndRemovingAMachineThisOneDelegatesTo(t *testing.T) {
	b, addr := tempDir(t), freeAddr(t)
	auto := api.AccessAutoEdits
	if _, err := daemon.Edit(b, func(c *daemon.Config) error { c.ChildrenFromOtherMachines = &auto; return nil }); err != nil {
		t.Fatal(err)
	}
	link, _, err := daemon.PairLink(b, "a", api.Link{Access: auto})
	if err != nil {
		t.Fatal(err)
	}
	whole, err := daemon.Pair(b, "window")
	if err != nil {
		t.Fatal(err)
	}
	engine(t, daemon.Config{Dir: b, Listen: []string{addr}}, fake.New(fake.Turn(false)))

	dir := tempDir(t)
	h := start(t, dir)
	thisMachine(t, h)
	if !h.tt.HasText("None yet. Add a machine with a link it minted for this one, and agents here may start children there.") {
		t.Errorf("the page shows %q, want no machines", h.tt.Texts())
	}
	h.a.delegate = delegateForm{label: "desk", server: addr, token: whole}
	h.tt.Frame()
	h.click("Add machine")
	h.until("the whole-engine token refused", func() bool {
		return strings.Contains(h.a.machineErr, "reaches all of the engine") && !h.a.delegate.adding
	})
	if ms, _ := daemon.Machines(dir); len(ms) != 0 {
		t.Fatalf("machines.json holds %+v after a whole-engine token", ms)
	}
	h.a.delegate.token = link
	h.tt.Frame()
	h.click("Add machine")
	h.until("desk listed", func() bool { return h.tt.HasText("desk") && h.tt.HasText(addr) && !h.a.delegate.adding })
	if ms, _ := daemon.Machines(dir); len(ms) != 1 || ms[0].Label != "desk" || ms[0].Token != link {
		t.Fatalf("machines.json holds %+v, want desk with its link", ms)
	}
	if h.tt.HasText(link) {
		t.Error("the page shows the link's token")
	}
	h.tt.Advance(time.Second)
	shoot(t, h, "a-delegates")
	h.click("Remove")
	h.until("the removal's word", func() bool {
		return h.tt.HasText("Removed desk. Its link stays valid on desk until you revoke it there, in Settings › This machine.")
	})
	if ms, _ := daemon.Machines(dir); len(ms) != 0 {
		t.Fatalf("machines.json holds %+v after the remove", ms)
	}
	if ts, _ := daemon.Tokens(b); !slices.ContainsFunc(ts, func(t daemon.Token) bool { return t.Link != nil }) {
		t.Error("removing the machine here revoked its link there")
	}
	shoot(t, h, "a-removed")
}

// remoteChild is a parent with a child the test makes stand for a remote
// child's placeholder, on the machine desk, which machines.json lists.
func remoteChild(t *testing.T) (*harness, thread, thread) {
	t.Helper()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	seedChild(t, d, top, "Port the sort", "port it")
	link := api.NewID()
	b, err := json.Marshal([]daemon.Machine{{Label: "desk", Server: "desk:7433", Engine: api.NewID(), Link: link, Token: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "machines.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	parent, kid := h.thread("Ship the sidebar sort"), h.thread("Port the sort")
	kid.View.Remote = &api.Placement{Engine: api.NewID(), Link: link}
	kid.View.LastTurn = nil
	// Nothing opened Settings: the app reads machines.json for the label.
	h.until("desk named", func() bool {
		h.tt.Frame()
		s, _, _ := h.a.remoteState(kid)
		return strings.HasSuffix(s, " on desk")
	})
	return h, parent, kid
}

// A placeholder's row, in the agents panel and the sidebar, says where its
// child runs and what it does there, in each state; lost, why as its tip.
func TestARemoteChildsRowSaysItsState(t *testing.T) {
	h, parent, kid := remoteChild(t)
	h.a.select_(parent)
	h.a.childrenOpen[parent.id()] = true
	h.tt.Frame()
	h.click("Agents")
	h.tt.Advance(time.Second)
	for _, c := range []struct {
		name  string
		set   func(*api.Thread)
		state string
	}{
		{"creating", func(v *api.Thread) { v.Run = api.RunStarting }, "Creating on desk"},
		{"working", func(v *api.Thread) { v.Remote.Thread, v.Run = api.NewID(), api.RunRunning }, "Working on desk"},
		{"needs-you", func(v *api.Thread) { v.Run = api.RunWaiting }, "Needs you on desk"},
		{"idle", func(v *api.Thread) { v.Run = api.RunIdle }, "Idle on desk"},
		{"cut-off", func(v *api.Thread) { v.Run = api.RunUncertain }, "Needs you on desk"},
		{"lost", func(v *api.Thread) { v.Remote.Lost = "the link was revoked on desk" }, "Lost on desk"},
	} {
		c.set(&kid.View)
		h.tt.Frame()
		h.tt.Advance(time.Second)
		h.tt.Frame()
		if n := strings.Count(strings.Join(h.tt.Texts(), "\n"), c.state); n != 2 {
			t.Errorf("%s: %q shows %d times, want in the agents panel and the sidebar: %q", c.name, c.state, n, h.tt.Texts())
		}
		shoot(t, h, "a-row-"+c.name)
	}
	if !failedLast(kid) {
		t.Error("a lost child's dot isn't red")
	}
	h.hover("Lost on desk")
	h.tt.Advance(time.Second)
	if !h.tt.HasText("the link was revoked on desk") {
		t.Errorf("the lost row's tip shows %q, want why", h.tt.Texts())
	}
}

// Opening a placeholder whose machine this app doesn't pair says so; the
// child's thread is there.
func TestOpeningARemoteChildUnpairedSaysToPairIt(t *testing.T) {
	h, _, kid := remoteChild(t)
	kid.View.Remote.Thread, kid.View.Run = api.NewID(), api.RunIdle
	kid.View.Remote.Lost = ""
	h.a.choose(kid)
	h.tt.Frame()
	h.tt.Advance(time.Second)
	h.tt.Frame()
	if h.a.selected != kid.id() || !h.tt.HasText("Idle on desk") || !h.tt.HasText("This child's thread is on desk. Pair desk in Settings › Servers to open it here.") {
		t.Errorf("the placeholder shows %q, want it says to pair desk", h.tt.Texts())
	}
	shoot(t, h, "a-unpaired")
}

// Opening a placeholder whose machine this app pairs opens the child's
// thread there.
func TestOpeningARemoteChildPairedOpensItThere(t *testing.T) {
	b, addr := tempDir(t), freeAddr(t)
	token, err := daemon.Pair(b, "gui")
	if err != nil {
		t.Fatal(err)
	}
	db := engine(t, daemon.Config{Dir: b, Listen: []string{addr}}, fake.New(fake.Turn(false)))
	there := seed(t, db, "/work/t3code", "Port the sort there")
	env, err := db.Endpoint().Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h, parent, kid := remoteChild(t)
	if err := h.addServer(client.Server{Label: "desk", Addr: addr, Token: token}); err != nil {
		t.Fatal(err)
	}
	h.until("desk's thread", func() bool { _, ok := h.a.find(there); return ok })
	kid.View.Remote.Engine, kid.View.Remote.Thread, kid.View.Run = env.ID, there, api.RunIdle
	h.a.select_(parent)
	h.tt.Frame()
	h.click("Agents")
	h.tt.Advance(time.Second)
	h.click("Port the sort")
	h.tt.Advance(time.Second)
	if h.a.selected != there {
		t.Fatalf("opening the placeholder shows %v, want desk's thread %v", h.a.selected, there)
	}
}

// A blocked report of a remote child says it needs the user on its
// machine.
func TestARemoteChildsBlockedReportNeedsYouOnItsMachine(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(true)))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: kid, Body: api.MessageDispatch{Text: "go"}}); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	h.a.select_(h.thread("Ship the sidebar sort"))
	h.until("the blocked report", func() bool { return h.tt.HasText("Needs you") && h.tt.HasText("Wants to edit main.go") })
	child := h.thread("Write the tests")
	h.until("the child waits", func() bool { return child.View.Run == api.RunWaiting })
	child.View.Remote = &api.Placement{Engine: api.NewID(), Link: api.NewID(), Thread: api.NewID(), Run: child.View.Turn.Run, Machine: "desk"}
	child.View.Requests = nil
	h.tt.Frame()
	h.tt.Advance(time.Second)
	h.tt.Frame()
	if !h.tt.HasText("Needs you on desk") {
		t.Errorf("the card shows %q, want Needs you on desk", h.tt.Texts())
	}
	shoot(t, h, "a-needs-you-card")
	child.View.Remote.Run = api.NewID()
	h.tt.Frame()
	if h.tt.HasText("Needs you on desk") || !h.tt.HasText("Was waiting") {
		t.Errorf("with another run waiting there, the card shows %q, want its own run's report was waiting", h.tt.Texts())
	}
}

// A thread a link started says, before its title, which thread on which
// machine started it.
func TestALinksThreadSaysWhereItCameFrom(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "Port the sort", "port it")
	link := linkFor(t, dir, "laptop")
	h := start(t, dir)
	k := h.thread("Port the sort")
	k.View.Origin = &api.Origin{Engine: api.NewID(), Thread: api.NewID(), Title: "Ship the sidebar sort", Link: link}
	h.a.choose(k)
	h.until("where the thread came from", func() bool { h.tt.Frame(); return h.tt.HasText("from Ship the sidebar sort on laptop") })
	shoot(t, h, "b-origin")
}

// fits fails t when a text the window shows runs past its right edge.
func fits(t *testing.T, h *harness, width float32, texts ...string) {
	t.Helper()
	for _, s := range texts {
		r, ok := h.tt.Find(s)
		if !ok {
			t.Errorf("no %q in %q", s, h.tt.Texts())
			continue
		}
		if r.X < 0 || r.X+r.W > width {
			t.Errorf("%q spans %v..%v, past the window's %v", s, r.X, r.X+r.W, width)
		}
	}
}

// In a narrow window a placeholder's row, its notice and a link's
// thread's origin stay inside the window.
func TestRemoteSurfacesFitANarrowWindow(t *testing.T) {
	h, parent, kid := remoteChild(t)
	h.tt.SetSize(760, 860)
	kid.View.Remote.Thread, kid.View.Run = api.NewID(), api.RunWaiting
	h.a.select_(parent)
	h.a.childrenOpen[parent.id()] = true
	h.tt.Frame()
	h.click("Agents")
	h.tt.Advance(time.Second)
	h.tt.Frame()
	fits(t, h, 760, "Needs you on desk")
	shoot(t, h, "a-narrow-row")
	kid.View.Run, kid.View.Remote.Lost = api.RunIdle, "the link was revoked on desk, so this engine no longer follows the child there"
	h.a.choose(kid)
	h.tt.Frame()
	h.tt.Advance(time.Second)
	h.tt.Frame()
	fits(t, h, 760, "Lost on desk", kid.View.Remote.Lost, "This child's thread is on desk. Pair desk in Settings › Servers to open it here.")
	shoot(t, h, "a-narrow-notice")
	parent.View.Origin = &api.Origin{Engine: api.NewID(), Thread: api.NewID(), Title: "A parent thread with a long title that runs on and on", Link: linkFor(t, h.a.backend.Machine().Config.Dir, "laptop")}
	h.a.choose(parent)
	h.until("where the thread came from", func() bool { h.tt.Frame(); return h.tt.HasText("from A parent thread with a long title that runs on and on on laptop") })
	h.tt.Advance(time.Second)
	h.tt.Frame()
	fits(t, h, 760, "from A parent thread with a long title that runs on and on on laptop")
	shoot(t, h, "b-narrow-origin")
}

// linkFor mints a link in dir for the machine label names, and returns
// its id.
func linkFor(t *testing.T, dir, label string) api.ID {
	t.Helper()
	auto := api.AccessAutoEdits
	if _, err := daemon.Edit(dir, func(c *daemon.Config) error { c.ChildrenFromOtherMachines = &auto; return nil }); err != nil {
		t.Fatal(err)
	}
	_, l, err := daemon.PairLink(dir, label, api.Link{Access: auto})
	if err != nil {
		t.Fatal(err)
	}
	return l.ID
}

// A placeholder whose machine this app pairs, before that machine made
// its thread, says it opens here once it has.
func TestARemoteChildPairedButNotYetMadeSaysItOpensOnceMade(t *testing.T) {
	b, addr := tempDir(t), freeAddr(t)
	token, err := daemon.Pair(b, "gui")
	if err != nil {
		t.Fatal(err)
	}
	db := engine(t, daemon.Config{Dir: b, Listen: []string{addr}}, fake.New(fake.Turn(false)))
	env, err := db.Endpoint().Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h, _, kid := remoteChild(t)
	if err := h.addServer(client.Server{Label: "desk", Addr: addr, Token: token}); err != nil {
		t.Fatal(err)
	}
	h.until("desk online", func() bool { return h.a.envOf(env.ID) != nil })
	kid.View.Remote.Engine, kid.View.Run = env.ID, api.RunStarting
	h.a.choose(kid)
	h.tt.Frame()
	h.tt.Advance(time.Second)
	h.tt.Frame()
	if !h.tt.HasText("Creating on desk") || !h.tt.HasText("Its thread opens here once desk has made it.") || h.tt.HasText("Pair desk in Settings") {
		t.Errorf("the placeholder shows %q, want it opens once desk made it", h.tt.Texts())
	}
}
