package main

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
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
	h.click("Add project")
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

// A pick cancelled leaves the chat as it was.
func TestAddProjectCancelled(t *testing.T) {
	was := chooseFolder
	picked := make(chan struct{})
	chooseFolder = func(*app) string { close(picked); return "" }
	t.Cleanup(func() { chooseFolder = was })
	h := start(t, inboxDir(t))
	th := h.thread("SIP re-INVITE handling")
	h.a.select_(th)
	h.tt.Advance(time.Second)
	h.click("Add project")
	select {
	case <-picked:
	case <-time.After(10 * time.Second):
		t.Fatal("Add project opened no picker")
	}
	h.until("the picker closed", func() bool { return !h.a.picking })
	h.tt.Advance(time.Second)
	if h.a.current().id() != th.id() {
		t.Fatalf("after a cancelled pick the chat shows %q", h.a.current().View.Title)
	}
}

// While the picker is open, Add project opens no second one.
func TestAddProjectOpensOnePicker(t *testing.T) {
	was := chooseFolder
	var opened atomic.Int32
	release := make(chan struct{})
	chooseFolder = func(*app) string { opened.Add(1); <-release; return "" }
	t.Cleanup(func() { chooseFolder = was })
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	h.a.addProject()
	for deadline := time.Now().Add(10 * time.Second); opened.Load() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("Add project opened no picker")
		}
		time.Sleep(time.Millisecond) // the picker opening, off the main thread
	}
	h.a.addProject()
	close(release)
	h.until("the picker closed", func() bool { return !h.a.picking })
	time.Sleep(100 * time.Millisecond) // time for a second picker to open
	h.until("the picker closed", func() bool { return !h.a.picking })
	if n := opened.Load(); n != 1 {
		t.Fatalf("%d pickers opened", n)
	}
}
