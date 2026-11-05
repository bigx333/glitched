package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
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
	if err := h.tt.RightClick("t3code"); err != nil {
		t.Fatal(err)
	}
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
	if h.tt.HasText("t3code") {
		t.Error("the chip still shows")
	}
}

// Removing a project deletes the threads under its threads as well.
func TestRemoveProjectTakesItsTrees(t *testing.T) {
	h := tree3(t)
	if err := h.tt.RightClick("t3code"); err != nil {
		t.Fatal(err)
	}
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
	if err := h.tt.RightClick("t3code"); err != nil {
		t.Fatal(err)
	}
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
	if err := h.tt.RightClick("t3code"); err != nil {
		t.Fatal(err)
	}
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
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	mid := seedChildIn(t, d, "/work/mygo", top, "Patch the table")
	seedChildIn(t, d, "/work/t3code", mid, "Check the sort")
	h := start(t, dir)
	h.thread("Check the sort")
	h.tt.Advance(time.Second)
	h.a.askRemove(h.a.projectOf(h.thread("Ship the sidebar sort")))
	h.tt.Frame()
	if !h.tt.HasText("Remove “t3code” and 3 threads?") || !h.tt.HasText("1 of them is under its threads, in other workspaces. They're removed from every list and can't be brought back. Their history stays in the engine's log. The folder, /work/t3code, isn't touched.") {
		t.Fatalf("no alert; texts %q", h.tt.Texts())
	}
	h.click("Remove")
	for _, th := range h.local().Threads {
		if n := len(th.Pending); n > 1 {
			t.Errorf("%q was sent %d deletes", th.View.Title, n)
		}
	}
	h.until("every thread gone", func() bool { return len(h.local().Threads) == 0 })
}

// seedChildIn creates a child of parent in workspace on the engine.
func seedChildIn(t *testing.T, d *daemon.Daemon, workspace string, parent api.ID, title string) api.ID {
	t.Helper()
	id := api.NewID()
	body := api.ThreadCreate{Workspace: workspace, Title: title, Model: fakeModel, Parent: parent}
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: body}); err != nil {
		t.Fatal(err)
	}
	return id
}

// An alert left open while a thread at the top starts a run removes
// nothing: the run is stopped first, as the menus have it.
func TestRemoveProjectRechecksForALiveRun(t *testing.T) {
	h := start(t, uncertainDir(t))
	th := h.thread("Stuck")
	h.tt.Advance(time.Second)
	h.a.askRemove(h.a.projectOf(th))
	h.tt.Frame()
	if !h.tt.HasText("An agent is working in t3code: stop it first, and nothing is removed until then.") {
		t.Fatalf("the alert doesn't say the agent is stopped first: %q", h.tt.Texts())
	}
	h.click("Remove")
	h.tt.Frame()
	if len(th.Pending) != 0 || !h.tt.HasText("Not removed") {
		t.Fatalf("a live project was removed: %d pending, texts %q", len(th.Pending), h.tt.Texts())
	}
}

// A draft left in a server's project starts over in the server's home,
// not this machine's, once the project is removed.
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
		_, ok := h.a.find(h.thread("Rotate the API keys").id())
		h.tt.Frame()
		return ok && h.a.fresh.workspace != "/srv/billing"
	})
	if h.a.fresh != (draft{devbox, "~"}) {
		t.Fatalf("the draft is %+v, want devbox's home", h.a.fresh)
	}
}

// A live thread under another of the project's, by way of another
// workspace, doesn't keep the project from being removed: it stops as its
// tree goes, as with a thread's Delete….
func TestRemoveProjectStopsALiveThreadUnderItsOwn(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	mid := seedChildIn(t, d, "/work/mygo", top, "Patch the table")
	seedChildIn(t, d, "/work/t3code", mid, "Check the sort")
	h := start(t, dir)
	h.thread("Check the sort").View.Run = api.RunUncertain // as a lost run leaves it
	h.tt.Frame()
	if p := h.a.projectOf(h.thread("Ship the sidebar sort")); h.a.projectLive(p) {
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
