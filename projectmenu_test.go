package main

import (
	"testing"
	"time"
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
	if err := h.tt.ChooseMenuItem("Remove project…"); err == nil {
		t.Fatal("Remove project… was offered while a run is live")
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
