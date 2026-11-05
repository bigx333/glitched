package main

import (
	"testing"
	"time"
)

// The sidebar's Add project picks a folder and shows a new thread in it;
// once that's sent, the folder is a project, with its chip.
func TestAddProject(t *testing.T) {
	dir := t.TempDir()
	was := chooseFolder
	chooseFolder = func(*app) string { return dir }
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
	<-picked
	h.tt.Advance(time.Second)
	if h.a.current().id() != th.id() {
		t.Fatalf("after a cancelled pick the chat shows %q", h.a.current().View.Title)
	}
}
