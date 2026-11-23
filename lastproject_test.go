package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"glitched/internal/jsonfile"
)

// rememberedState is the state a restart finds: added kept as projects,
// and last, unless "", as this machine's last project.
func rememberedState(t *testing.T, state string, added []string, last string) {
	t.Helper()
	var ps []addedProject
	for _, p := range added {
		ps = append(ps, addedProject{Workspace: p, Added: "2026-11-23T08:00:00Z"})
	}
	if err := jsonfile.Write(filepath.Join(state, "projects.json"), ps); err != nil {
		t.Fatal(err)
	}
	if last == "" {
		return
	}
	if err := jsonfile.Write(filepath.Join(state, "last-project.json"), map[string]string{"": last}); err != nil {
		t.Fatal(err)
	}
}

// lastProjectFile is the folder last-project.json holds for this machine.
func lastProjectFile(t *testing.T, state string) string {
	t.Helper()
	var m map[string]string
	if err := jsonfile.Load(filepath.Join(state, "last-project.json"), &m); err != nil {
		t.Fatal(err)
	}
	return m[""]
}

// New thread starts in the project the last thread was started in, No
// project included, not in the shown thread's, and keeps it in
// last-project.json.
func TestANewThreadStartsWhereTheLastOneWasStarted(t *testing.T) {
	state, proj := tempDir(t), t.TempDir()
	rememberedState(t, state, []string{proj}, "")
	h := start(t, state)
	h.a.startIn(draft{env: h.local(), workspace: proj})
	inProject := sendInDraft(h, "Tidy the imports")
	if got := lastProjectFile(t, state); got != proj {
		t.Fatalf("last-project.json holds %q, want %q", got, proj)
	}
	h.a.startIn(draft{env: h.local(), workspace: h.a.scratchRoot()})
	noProject := sendInDraft(h, "Rename the photos by date")
	if got := lastProjectFile(t, state); got != h.a.scratchRoot() {
		t.Fatalf("last-project.json holds %q, want the scratch root", got)
	}

	h.a.select_(inProject)
	h.tt.Advance(time.Second)
	h.click("New thread")
	if ws := h.a.current().View.Workspace; ws != h.a.scratchRoot() {
		t.Fatalf("New thread from a thread in %s starts in %q, want No project, where the last thread started", filepath.Base(proj), ws)
	}
	h.a.startIn(draft{env: h.local(), workspace: proj})
	sendInDraft(h, "Sort the tests")
	h.a.select_(noProject)
	h.tt.Advance(time.Second)
	h.click("New thread")
	if ws := h.a.current().View.Workspace; ws != proj {
		t.Fatalf("New thread from a thread with no project starts in %q, want %q, where the last thread started", ws, proj)
	}
}

// The project last started in is where the first draft starts after a
// restart.
func TestTheLastProjectIsKeptAcrossRestarts(t *testing.T) {
	state, proj := tempDir(t), t.TempDir()
	rememberedState(t, state, []string{proj}, proj)
	h := start(t, state)
	h.tt.Advance(time.Second)
	if ws := h.a.current().View.Workspace; ws != proj {
		t.Fatalf("the draft after a restart is in %q, want %q", ws, proj)
	}
	h.untilShown("the composer's workspace", filepath.Base(proj))
}

// A last project that is gone, as a folder or as a project, leaves the
// draft where it would start with nothing remembered: No project.
func TestALastProjectGoneSinceIsNotWhereADraftStarts(t *testing.T) {
	t.Run("its folder is gone", func(t *testing.T) {
		state, proj := tempDir(t), t.TempDir()
		rememberedState(t, state, []string{proj}, proj)
		if err := os.RemoveAll(proj); err != nil {
			t.Fatal(err)
		}
		h := start(t, state)
		h.tt.Advance(time.Second)
		if ws := h.a.current().View.Workspace; ws != h.a.scratchRoot() {
			t.Fatalf("the draft is in %q, whose folder is gone, want No project", ws)
		}
	})
	t.Run("it is no project", func(t *testing.T) {
		state := tempDir(t)
		rememberedState(t, state, nil, t.TempDir())
		h := start(t, state)
		h.tt.Advance(time.Second)
		if ws := h.a.current().View.Workspace; ws != h.a.scratchRoot() {
			t.Fatalf("the draft is in %q, no project, want No project", ws)
		}
	})
	t.Run("it is removed", func(t *testing.T) {
		state, proj := tempDir(t), t.TempDir()
		rememberedState(t, state, []string{proj}, proj)
		h := start(t, state)
		h.tt.Advance(time.Second)
		h.a.removeProject(h.a.projectOf(h.a.current()))
		h.tt.Advance(time.Second)
		if ws := h.a.current().View.Workspace; ws != h.a.scratchRoot() {
			t.Fatalf("the draft is in %q after its project was removed, want No project", ws)
		}
		h.click("New thread")
		if ws := h.a.current().View.Workspace; ws != h.a.scratchRoot() {
			t.Fatalf("New thread starts in %q, a removed project, want No project", ws)
		}
	})
}

// Quick ask opens on the project the last thread was started in, and with
// nothing remembered on the shown thread's; a quick ask is remembered too.
func TestQuickAskOpensOnTheLastProject(t *testing.T) {
	state, proj := tempDir(t), t.TempDir()
	inbox(t, state)
	rememberedState(t, state, []string{proj}, "")
	h := start(t, state)
	h.a.select_(h.thread("SIP re-INVITE handling"))
	h.tt.Advance(time.Second)
	shown := h.a.projectOf(h.a.current()).key
	if got := h.a.localProjects()[h.a.quickProject()].key; got != shown {
		t.Fatalf("with nothing remembered quick ask opens on %q, want the shown thread's %q", got.workspace, shown.workspace)
	}
	h.a.startIn(draft{env: h.local(), workspace: proj})
	sendInDraft(h, "Tidy the imports")
	h.a.select_(h.thread("SIP re-INVITE handling"))
	h.tt.Advance(time.Second)
	if got := h.a.localProjects()[h.a.quickProject()].key.workspace; got != proj {
		t.Fatalf("quick ask opens on %q, want %q, where the last thread started", got, proj)
	}
	h.a.quick.project = 0
	h.a.quick.query = "Rename the photos by date"
	h.a.startQuickThread()
	h.thread("Rename the photos by date")
	if got := lastProjectFile(t, state); got != h.a.scratchRoot() {
		t.Fatalf("after a quick ask with no project last-project.json holds %q, want the scratch root", got)
	}
}

// Removing the project the last thread was started in, from a draft in
// it, forgets it: neither that draft nor the next New thread brings it
// back.
func TestRemovingTheLastProjectForgetsIt(t *testing.T) {
	state, proj := tempDir(t), t.TempDir()
	rememberedState(t, state, []string{proj}, proj)
	h := start(t, state)
	h.a.startIn(draft{env: h.local(), workspace: proj})
	h.tt.Advance(time.Second)
	h.a.removeProject(h.a.projectOf(h.a.current()))
	h.tt.Advance(time.Second)
	if ws := h.a.current().View.Workspace; ws != h.a.scratchRoot() {
		t.Fatalf("the draft is in %q after its project was removed, want No project", ws)
	}
	h.click("New thread")
	if ws := h.a.current().View.Workspace; ws != h.a.scratchRoot() {
		t.Fatalf("New thread starts in %q, a removed project, want No project", ws)
	}
	if got := lastProjectFile(t, state); got != "" {
		t.Fatalf("last-project.json still holds %q", got)
	}
}

// New thread reads where the last thread was started as the file has it
// now, which another window on the state may have changed.
func TestNewThreadFollowsAnotherWindowsLastProject(t *testing.T) {
	state, a, b := tempDir(t), t.TempDir(), t.TempDir()
	rememberedState(t, state, []string{a, b}, a)
	h := start(t, state)
	h.tt.Advance(time.Second)
	if err := jsonfile.Write(filepath.Join(state, "last-project.json"), map[string]string{"": b}); err != nil {
		t.Fatal(err)
	}
	h.click("New thread")
	if ws := h.a.current().View.Workspace; ws != b {
		t.Fatalf("New thread starts in %q, want %q, where the other window last started one", ws, b)
	}
}

// A server's home row is no default while no thread runs there: removing
// a project leaves the draft in the server's project left, not "~".
func TestAServersHomeIsNoLastProjectWithoutAThread(t *testing.T) {
	h := machines(t)
	devbox := h.thread("Retry failed invoice webhooks").env
	h.a.rememberProject(devbox, "~")
	h.a.fresh = draft{devbox, "/srv/billing"}
	h.a.removeProject(h.a.projectOf(h.thread("Retry failed invoice webhooks")))
	h.until("billing gone", func() bool {
		h.tt.Frame()
		return !slices.ContainsFunc(h.a.projects, func(p *project) bool { return p.key.workspace == "/srv/billing" })
	})
	if h.a.fresh != (draft{devbox, "/srv/api"}) {
		t.Fatalf("the draft is %+v, want devbox's api project, the one left", h.a.fresh)
	}
}
