package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// A project chip's Remove project… asks, always, then deletes every thread
// in the project, those under them too, and the chip goes; the other
// projects and their threads stay, and the filter forgets it.
func TestRemoveProject(t *testing.T) {
	h := start(t, inboxDir(t))
	h.a.settings.confirmDelete = false // removing a project asks whatever this says
	h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	h.click("t3code")
	h.tt.Advance(time.Second)
	if !h.a.projectFilter[projectKey{workspace: "/work/t3code"}] {
		t.Fatal("clicking the chip didn't filter to it")
	}
	h.rightClick("t3code")
	if err := h.tt.ChooseMenuItem("Remove project…"); err != nil {
		t.Fatalf("menu %q: %v", h.tt.Menu(), err)
	}
	h.tt.Frame()
	if !h.tt.HasText("Remove “t3code” and its 3 threads?") ||
		!h.tt.HasText("They're removed from every list and can't be brought back. Their history stays in the engine's log. The folder, /work/t3code, isn't touched.") {
		t.Fatalf("no alert; texts %q", h.tt.Texts())
	}
	h.click("Remove")
	h.until("the project gone", func() bool {
		h.tt.Frame()
		for _, p := range h.a.projects {
			if p.key.workspace == "/work/t3code" {
				return false
			}
		}
		return true
	})
	for _, want := range []string{"SIP re-INVITE handling", "Markdown table column alignment"} {
		if _, ok := h.tt.Find(want); !ok {
			t.Errorf("%q went with the project", want)
		}
	}
	if len(h.a.projectFilter) != 0 {
		t.Errorf("the filter still holds %v", h.a.projectFilter)
	}
	settlePlugins(t, h.a, h.tt, h.p)
	if h.tt.HasText("t3code") {
		t.Error("the chip still shows")
	}
}

// Removing a project deletes the threads under its threads as well, and
// counts them in the alert.
func TestRemoveProjectTakesItsTrees(t *testing.T) {
	h := tree3(t)
	h.rightClick("t3code")
	if err := h.tt.ChooseMenuItem("Remove project…"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if !h.tt.HasText("Remove “t3code” and its 3 threads?") {
		t.Fatalf("no alert; texts %q", h.tt.Texts())
	}
	h.click("Remove")
	h.until("every thread gone", func() bool { return len(h.local().Threads) == 0 })
}

// Cancel keeps the project and its threads.
func TestRemoveProjectCancelled(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	h.rightClick("t3code")
	if err := h.tt.ChooseMenuItem("Remove project…"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.click("Cancel")
	h.tt.Advance(time.Second)
	if h.a.removeOpen || len(h.a.projects) != 3 {
		t.Fatalf("after Cancel: open %v, %d projects", h.a.removeOpen, len(h.a.projects))
	}
	for _, th := range h.local().Threads {
		if len(th.Pending) > 0 {
			t.Errorf("%q has a command on its way", th.View.Title)
		}
	}
}

// As a thread is stopped before it's deleted, a project whose thread has a
// run, an uncertain one too, isn't removed until it's stopped.
func TestRemoveProjectWaitsForALiveRun(t *testing.T) {
	h := start(t, uncertainDir(t))
	h.thread("Stuck")
	h.tt.Advance(time.Second)
	h.rightClick("t3code")
	if m := h.tt.Menu(); !slices.Contains(m, "Remove project…") {
		t.Fatalf("the chip's menu %q has no Remove project…", m)
	}
	if err := h.tt.ChooseMenuItem("Remove project…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Remove project… while a run is live: %v", err)
	}
}

// The draft's workspace menu adds a project and removes one, as the
// sidebar does.
func TestWorkspaceMenuAddsAndRemoves(t *testing.T) {
	dir := t.TempDir()
	was := chooseFolder
	chooseFolder = func(*app) string { return dir }
	t.Cleanup(func() { chooseFolder = was })
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	h.click("New thread")
	h.click("Workspace")
	if err := h.tt.ChooseMenuItem("Remove project", "mygo…"); err != nil {
		t.Fatalf("%v; the menu has %q", err, h.tt.Menu())
	}
	h.tt.Frame()
	if !h.tt.HasText("Remove “mygo” and its thread?") {
		t.Fatalf("no alert; texts %q", h.tt.Texts())
	}
	h.click("Remove")
	h.until("mygo gone", func() bool { h.tt.Frame(); return len(h.a.projects) == 2 })

	h.tt.Advance(time.Second)
	h.click("Workspace")
	if err := h.tt.ChooseMenuItem("Add project…"); err != nil {
		t.Fatalf("%v; the menu has %q", err, h.tt.Menu())
	}
	h.until("the draft in the folder", func() bool { return h.a.current().View.Workspace == dir })
}

// The palette finds Add project… and each Remove project by "workspace"
// too, and runs them.
func TestPaletteAddsAndRemovesProjects(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type("workspace")
	var got []string
	for _, cmd := range h.a.commands() {
		got = append(got, cmd.label)
	}
	want := []string{"Add project…", "Remove project agent-phone…", "Remove project mygo…", "Remove project t3code…"}
	if !slices.Equal(got, want) {
		t.Fatalf("palette lists %q for workspace, want %q", got, want)
	}
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("remove project mygo")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if !h.tt.HasText("Remove “mygo” and its thread?") {
		t.Fatalf("no alert; texts %q", h.tt.Texts())
	}
}

// A draft left in the project's workspace, with no machine named, starts
// over in the home folder once the project is removed, while another
// thread is shown.
func TestRemoveProjectResetsItsDraft(t *testing.T) {
	h := start(t, inboxDir(t))
	sip := h.thread("SIP re-INVITE handling")
	h.a.fresh = draft{nil, "/work/t3code"}
	h.a.select_(sip)
	h.tt.Advance(time.Second)
	h.a.askRemove(h.a.projectOf(h.thread("Add keyboard shortcut for new thread")))
	h.tt.Frame()
	h.click("Remove")
	h.until("t3code gone", func() bool { h.tt.Frame(); return len(h.a.projects) == 2 })
	if h.a.fresh.workspace == "/work/t3code" {
		t.Fatalf("the draft is still in the removed workspace: %+v", h.a.fresh)
	}
}

// A project whose thread is under another root of the same project, by way
// of a thread in another workspace, has each thread deleted once.
func TestRemoveProjectDeletesEachThreadOnce(t *testing.T) {
	h := crossTree(t)
	top := h.thread("Ship the sidebar sort")
	h.a.askRemove(h.a.projectOf(top))
	h.tt.Frame()
	if !h.tt.HasText("Remove “t3code” and 3 threads?") || !h.tt.HasText("1 of them is under its threads, in other workspaces. They're removed from every list and can't be brought back. Their history stays in the engine's log. The folder, /work/t3code, isn't touched.") {
		t.Fatalf("no alert; texts %q", h.tt.Texts())
	}
	h.click("Remove")
	// The engine deletes a thread's tree with it: one delete, to the top.
	sent := 0
	for _, th := range h.local().Threads {
		sent += len(th.Pending)
	}
	if sent != 1 || len(top.Pending) != 1 {
		t.Fatalf("%d deletes sent, want one, to the top thread", sent)
	}
	// While it's on its way, nothing of the tree shows.
	if ps := h.a.gather(); len(ps) != 0 {
		t.Fatalf("%d projects left, want the tree's both gone", len(ps))
	}
}

// crossTree is the app on a thread, Ship the sidebar sort, in t3code, with
// a child in mygo, Patch the table, which has one back in t3code, Check
// the sort. The engine starts a child only from its parent's live run, so
// the tree is made here, in what the GUI holds of the threads.
func crossTree(t *testing.T) *harness {
	t.Helper()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	mid := seed(t, d, "/work/mygo", "Patch the table")
	seed(t, d, "/work/t3code", "Check the sort")
	h := start(t, dir)
	h.thread("Patch the table").View.Parent = top
	h.thread("Check the sort").View.Parent = mid
	h.tt.Advance(time.Second)
	return h
}

// An alert left open while a thread at the top starts a run removes
// nothing: the run is stopped first, as the menus have it.
func TestRemoveProjectRechecksForALiveRun(t *testing.T) {
	h := start(t, inboxDir(t))
	th := h.thread("Markdown table column alignment")
	h.tt.Advance(time.Second)
	h.a.askRemove(h.a.projectOf(th))
	h.tt.Frame()
	if !h.tt.HasText("Remove “mygo” and its thread?") {
		t.Fatalf("no alert; texts %q", h.tt.Texts())
	}
	th.View.Run = api.RunUncertain // a run starting while the alert is open
	h.tt.Frame()
	if !h.tt.HasText("An agent is working in mygo: stop it first, and nothing is removed until then. The folder, /work/mygo, isn't touched.") {
		t.Fatalf("the alert doesn't say the agent is stopped first: %q", h.tt.Texts())
	}
	h.click("Remove")
	h.tt.Frame()
	if len(th.Pending) != 0 || !h.tt.HasText("Not removed") {
		t.Fatalf("a live project was removed: %d pending, texts %q", len(th.Pending), h.tt.Texts())
	}
}

// A draft left in a server's project starts over in a workspace the
// server has, its newest project left, never in "~", which its engine
// doesn't expand and claude can't start in.
func TestRemoveProjectResetsADraftOnAServer(t *testing.T) {
	h := machines(t)
	devbox := h.thread("Retry failed invoice webhooks").env
	h.a.fresh = draft{devbox, "/srv/billing"}
	h.a.askRemove(h.a.projectOf(h.thread("Retry failed invoice webhooks")))
	h.tt.Frame()
	if !h.tt.HasText("Remove “billing on devbox” and its thread?") {
		t.Fatalf("the alert doesn't name the machine: %q", h.tt.Texts())
	}
	h.click("Remove")
	h.until("billing gone", func() bool {
		h.tt.Frame()
		return !slices.ContainsFunc(h.a.projects, func(p *project) bool { return p.key.workspace == "/srv/billing" })
	})
	if h.a.fresh != (draft{devbox, "/srv/api"}) {
		t.Fatalf("the draft is %+v, want devbox's api project, the one left", h.a.fresh)
	}
}

// A draft on a server whose last project is removed goes to the app's
// default, this machine's home, rather than a folder the server lacks.
func TestRemoveAServersLastProjectMovesItsDraftHome(t *testing.T) {
	h := machines(t)
	devbox := h.thread("Retry failed invoice webhooks").env
	for _, ws := range []string{"/srv/billing", "/srv/api"} {
		h.a.fresh = draft{devbox, ws}
		i := slices.IndexFunc(h.a.projects, func(p *project) bool { return p.env == devbox && p.key.workspace == ws })
		if i < 0 {
			t.Fatalf("devbox has no project in %s", ws)
		}
		h.a.removeProject(h.a.projects[i])
		h.until(ws+" gone", func() bool {
			h.tt.Frame()
			return !slices.ContainsFunc(h.a.projects, func(p *project) bool { return p.key.workspace == ws })
		})
	}
	if h.a.fresh != (draft{}) {
		t.Fatalf("the draft is %+v, want the default draft: devbox has no project left", h.a.fresh)
	}
	if d := h.a.current(); d.env != h.local() || d.View.Workspace != homeDir() {
		t.Fatalf("the draft shows %q, want this machine's home", d.View.Workspace)
	}
}

// A live thread under another of the project's, by way of another
// workspace, counts as working but doesn't keep the project from being
// removed, as a live child doesn't keep a thread from its Delete….
func TestRemoveProjectIsntHeldByALiveThreadUnderItsOwn(t *testing.T) {
	h := crossTree(t)
	h.thread("Check the sort").View.Run = api.RunUncertain // as a lost run leaves it
	h.tt.Frame()
	p := h.a.projectOf(h.thread("Ship the sidebar sort"))
	if _, all := h.a.projectTrees(p); working(all) != 1 {
		t.Fatalf("%d of the project's threads live, want the one made so", working(all))
	}
	if h.a.projectLive(p) {
		t.Fatal("a live thread under the project's own keeps it from being removed")
	}
}

// Two projects of one name on the machine are told apart by their folders.
func TestProjectsOfOneNameAreToldApart(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/a/web", "Fix the header")
	seed(t, d, "/b/web", "Fix the footer")
	h := start(t, dir)
	h.thread("Fix the footer")
	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type("remove project")
	var got []string
	for _, cmd := range h.a.commands() {
		got = append(got, cmd.label)
	}
	if want := []string{"Remove project /a/web…", "Remove project /b/web…"}; !slices.Equal(got, want) {
		t.Fatalf("palette lists %q, want %q", got, want)
	}
}

// A remove whose deletes fail as they're sent, with no engine, keeps the
// draft where it was, in the project still there.
func TestAFailedRemoveKeepsTheDraft(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "Ship the sidebar sort")
	h := start(t, dir)
	th := h.thread("Ship the sidebar sort")
	h.a.fresh = draft{h.local(), "/work/t3code"}
	d.Close()
	h.until("the engine gone", func() bool { return h.local().Status != client.Status(client.Online{}) })
	h.a.removeProject(h.a.projectOf(th))
	h.until("the failure", func() bool { return len(th.Pending) == 1 && th.Pending[0].Err != nil })
	if h.a.fresh.workspace != "/work/t3code" {
		t.Fatalf("after a failed remove the draft is in %q", h.a.fresh.workspace)
	}
}

// The palette lists Remove for a project an agent works in, so the rows
// don't shift as a run starts; its alert says to stop the agent first.
func TestPaletteListsRemoveWhileWorking(t *testing.T) {
	h := start(t, uncertainDir(t))
	h.thread("Stuck")
	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type("remove project t3code")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if !h.tt.HasText("An agent is working in t3code: stop it first, and nothing is removed until then. The folder, /work/t3code, isn't touched.") {
		t.Fatalf("no alert; texts %q", h.tt.Texts())
	}
}

// The palette's unshown words find a command only whole: part of one
// doesn't put an action ahead of the thread the query names.
func TestPaletteFindsByWholeWordsOnly(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type("old")
	for _, cmd := range h.a.commands() {
		if cmd.section == "Actions" {
			t.Errorf("%q listed for old", cmd.label)
		}
	}
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("remove workspace mygo")
	var got []string
	for _, cmd := range h.a.commands() {
		got = append(got, cmd.label)
	}
	if want := []string{"Remove project mygo…"}; !slices.Equal(got, want) {
		t.Fatalf("palette lists %q, want %q", got, want)
	}
}

// A draft the delete leaves in another workspace whose threads all went
// with the trees, as the shown thread's, starts over in the home folder
// too, lest that project come back with the next thread.
func TestRemoveProjectResetsADraftItsTreesEmptied(t *testing.T) {
	h := crossTree(t)
	mid := h.thread("Patch the table") // mygo's only thread, under t3code's
	h.a.select_(mid)
	h.tt.Advance(time.Second)
	p := h.a.projectOf(h.thread("Ship the sidebar sort"))
	if _, all := h.a.projectTrees(p); !slices.ContainsFunc(all, func(n node) bool { return n.t.id() == mid.id() }) {
		t.Fatal("mygo's thread isn't under t3code's")
	}
	h.a.removeProject(p)
	if h.a.fresh != (draft{h.local(), homeDir()}) {
		t.Fatalf("the draft is %+v, want the home folder: mygo went with t3code's trees", h.a.fresh)
	}
}
