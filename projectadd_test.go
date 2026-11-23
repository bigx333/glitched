package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/jsonfile"
)

// The sidebar's Add project picks a folder and shows a new thread in it;
// once that's sent, the folder is a project, with its chip.
func TestAddProject(t *testing.T) {
	dir := t.TempDir()
	was := chooseFolder
	chooseFolder = func(*app) string { return dir + string(filepath.Separator) } // as a picker may give it
	t.Cleanup(func() { chooseFolder = was })
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	addFolder(h)
	h.until("the draft in the folder", func() bool {
		cur := h.a.current()
		return cur.id().IsZero() && cur.View.Workspace == dir && cur.local()
	})
	h.a.draft = "Set it up"
	h.tt.Advance(time.Second)
	h.click("Send")
	h.until("the project", func() bool {
		h.tt.Frame()
		for _, p := range h.a.projects {
			if p.key.workspace == dir {
				return true
			}
		}
		return false
	})
}

// A pick cancelled leaves the wizard on Source, and the chat as it was.
func TestAddProjectCancelled(t *testing.T) {
	was := chooseFolder
	picked := make(chan struct{})
	chooseFolder = func(*app) string { close(picked); return "" }
	t.Cleanup(func() { chooseFolder = was })
	h := start(t, inboxDir(t))
	th := h.thread("SIP re-INVITE handling")
	h.a.select_(th)
	h.tt.Advance(time.Second)
	newProject(h)
	h.click("Existing folder")
	select {
	case <-picked:
	case <-time.After(10 * time.Second):
		t.Fatal("Add project opened no picker")
	}
	h.until("the picker closed", func() bool { return !h.a.picking })
	h.tt.Advance(time.Second)
	if !h.tt.Focused("Existing folder") || !h.a.wizard.open {
		t.Fatalf("a cancelled pick left Source: %q", h.tt.Texts())
	}
	if h.a.current().id() != th.id() {
		t.Fatalf("after a cancelled pick the chat shows %q", h.a.current().View.Title)
	}
}

// While the picker is open, Existing folder opens no second one.
func TestAddProjectOpensOnePicker(t *testing.T) {
	was := chooseFolder
	var opened atomic.Int32
	release := make(chan struct{})
	chooseFolder = func(*app) string { opened.Add(1); <-release; return "" }
	t.Cleanup(func() { chooseFolder = was })
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	h.a.openWizard(sourceNone)
	h.a.chooseSource(sourceFolder)
	for deadline := time.Now().Add(10 * time.Second); opened.Load() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("Add project opened no picker")
		}
		time.Sleep(time.Millisecond) // the picker opening, off the main thread
	}
	h.a.chooseSource(sourceFolder)
	close(release)
	h.until("the picker closed", func() bool { return !h.a.picking })
	time.Sleep(100 * time.Millisecond) // time for a second picker to open
	h.until("the picker closed", func() bool { return !h.a.picking })
	if n := opened.Load(); n != 1 {
		t.Fatalf("%d pickers opened", n)
	}
}

// pickFolder has Existing folder's picker give a new folder named name.
func pickFolder(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	was := chooseFolder
	chooseFolder = func(*app) string { return dir }
	t.Cleanup(func() { chooseFolder = was })
	return dir
}

// addAndLeave adds the picked folder as a project, with no thread sent in
// it, and shows another thread, so the folder's name shows only where
// the project does.
func addAndLeave(h *harness, dir string) {
	h.t.Helper()
	th := h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	addFolder(h)
	h.until("the draft in the folder", func() bool { return h.a.current().View.Workspace == dir })
	h.a.select_(th)
	h.tt.Advance(time.Second)
}

// newThreadsIn are the palette's New thread in rows that name name.
func newThreadsIn(t *testing.T, h *harness, name string) int {
	t.Helper()
	h.a.openCommandPalette()
	shown, _ := paletteShows(t, h)
	h.a.cmdkOpen = false
	h.tt.Frame()
	n := 0
	for _, cmd := range shown {
		if strings.HasPrefix(cmd.label, "New thread in ") && strings.Contains(cmd.label, name) {
			n++
		}
	}
	return n
}

// An added project stays with no thread sent in it, and after a restart
// too: its chip shows, the draft's workspace menu offers it, and the
// palette starts a thread in it.
func TestAddProjectStaysWithNoThread(t *testing.T) {
	dir := pickFolder(t, "lighthouse")
	state := inboxDir(t)
	h := start(t, state)
	addAndLeave(h, dir)
	for _, h := range []*harness{h, start(t, state)} {
		h.thread("SIP re-INVITE handling")
		h.untilShown("the project's chip", "lighthouse")
		h.click("New thread")
		h.click("Workspace")
		if err := h.tt.ChooseMenuItem("lighthouse"); err != nil {
			t.Fatalf("%v; the menu has %q", err, h.tt.Menu())
		}
		h.tt.Frame()
		if ws := h.a.current().View.Workspace; ws != dir {
			t.Fatalf("choosing lighthouse left the draft in %q", ws)
		}
		if n := newThreadsIn(t, h, "lighthouse"); n != 1 {
			t.Fatalf("the palette has %d New thread in lighthouse", n)
		}
	}
}

// Adding a folder that is a project already keeps the one project.
func TestAddProjectTwiceKeepsOne(t *testing.T) {
	dir := pickFolder(t, "lighthouse")
	state := inboxDir(t)
	h := start(t, state)
	addAndLeave(h, dir)
	chooseFolder = func(*app) string { return dir + string(filepath.Separator) }
	addAndLeave(h, dir)
	if n := newThreadsIn(t, h, "lighthouse"); n != 1 {
		t.Fatalf("after adding it twice the palette has %d New thread in lighthouse", n)
	}
	var file []map[string]string
	if err := jsonfile.Load(filepath.Join(state, "projects.json"), &file); err != nil {
		t.Fatal(err)
	}
	if len(file) != 1 || file[0]["workspace"] != dir || file[0]["added"] == "" {
		t.Fatalf("projects.json holds %v, want the folder once", file)
	}
	again := start(t, state)
	again.thread("SIP re-INVITE handling")
	if n := newThreadsIn(t, again, "lighthouse"); n != 1 {
		t.Fatalf("after a restart the palette has %d New thread in lighthouse", n)
	}
}

// Removing an added project with no thread asks, says it has none, and
// forgets it: it doesn't come back with a restart.
func TestRemoveAnAddedProjectWithNoThread(t *testing.T) {
	dir := pickFolder(t, "lighthouse")
	state := inboxDir(t)
	h := start(t, state)
	addAndLeave(h, dir)
	h.rightClick("lighthouse")
	if err := h.tt.ChooseMenuItem("Remove project…"); err != nil {
		t.Fatalf("menu %q: %v", h.tt.Menu(), err)
	}
	h.tt.Frame()
	if !h.tt.HasText("Remove “lighthouse”?") ||
		!h.tt.HasText("It has no threads, so it only leaves the lists. The folder, "+dir+", isn't touched.") {
		t.Fatalf("no alert; texts %q", h.tt.Texts())
	}
	h.click("Remove")
	settlePlugins(t, h.a, h.tt, h.p)
	if h.tt.HasText("lighthouse") || newThreadsIn(t, h, "lighthouse") != 0 {
		t.Fatalf("the project stays: %q", h.tt.Texts())
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the folder went with the project: %v", err)
	}
	again := start(t, state)
	again.thread("SIP re-INVITE handling")
	if n := newThreadsIn(t, again, "lighthouse"); n != 0 {
		t.Fatalf("after a restart the palette has %d New thread in lighthouse", n)
	}
}

// The strip's New project is one button, which opens the wizard.
func TestTheStripsNewProjectOpensTheWizard(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	h.click("New project")
	if m := h.tt.Menu(); len(m) > 0 {
		t.Fatalf("New project opened a menu, %q", m)
	}
	settlePlugins(t, h.a, h.tt, h.p)
	h.untilShown("the wizard", "New project on this machine")
}
