package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// parentShown is tree3's top thread, shown, its report in.
func parentShown(t *testing.T) (*harness, thread) {
	t.Helper()
	h := tree3(t)
	top := h.thread("Ship the sidebar sort")
	h.a.select_(top)
	h.until("its report", func() bool { return h.tt.HasText("Done") })
	h.tt.Advance(time.Second)
	return h, top
}

// Over the parent's composer, Agents opens a panel of everything under it;
// a row opens its thread, which closes the panel, and the child's
// breadcrumb leads back, to the pill, not a panel folding away.
func TestAgentsStrip(t *testing.T) {
	h, top := parentShown(t)
	h.click("Agents")
	h.tt.Advance(time.Second) // it grows into the panel
	if !h.a.agentsOpen[top.id()] {
		t.Fatal("Agents didn't open")
	}
	// The report card names Write the tests too: the panel's grandchild is
	// its only row.
	r, ok := h.tt.Find("Run them")
	if !ok || r.X < h.a.sidebarWidth {
		t.Fatalf("no grandchild in the panel: %q", h.tt.Texts())
	}
	h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Frame()
	if h.a.current().View.Title != "Run them" {
		t.Fatalf("the row opened %q", h.a.current().View.Title)
	}
	if h.a.agentsOpen[top.id()] || h.tt.HasText("Close agents") {
		t.Error("the panel stayed open over the child")
	}
	h.click("Back to Ship the sidebar sort")
	h.tt.Frame()
	if h.a.current().id() != top.id() {
		t.Fatalf("the breadcrumb went to %q", h.a.current().View.Title)
	}
	if h.a.agentsOpen[top.id()] || h.a.agentsK != 0 || h.tt.HasText("Close agents") {
		t.Errorf("open coming back: k %v", h.a.agentsK)
	}
}

// The pill grows into the panel and folds back; turned round mid-way, it
// goes back from where it is, and the chat's room stays the pill's.
func TestAgentsMorph(t *testing.T) {
	h, top := parentShown(t)
	closedRoom := h.a.agentsRoom(top)
	h.click("Agents")
	h.tt.Advance(60 * time.Millisecond)
	h.tt.Frame() // laid out, for the ✕ to be found
	mid := h.a.agentsK
	if mid <= 0 || mid >= 1 {
		t.Fatalf("k %v 60ms into opening", mid)
	}
	if r := h.a.agentsRoom(top); r != closedRoom {
		t.Errorf("room %v mid-way, %v closed: the panel pushes the chat", r, closedRoom)
	}
	h.click("Close agents")
	if h.a.agentsOpen[top.id()] {
		t.Fatal("the ✕ didn't close it mid-way")
	}
	h.tt.Advance(16 * time.Millisecond)
	if d := mid - h.a.agentsK; d < -0.15 || d > 0.25 {
		t.Errorf("turned round from %v to %v in a frame", mid, h.a.agentsK)
	}
	h.tt.Advance(time.Second)
	if h.a.agentsK != 0 {
		t.Errorf("k %v after folding", h.a.agentsK)
	}
}

// The open panel floats over the chat: the chat stays where it was, and
// Escape or a press outside closes it; a press inside doesn't.
func TestAgentsOverlay(t *testing.T) {
	h, top := parentShown(t)
	const last = "All done: the sort is tested."
	before, ok := h.tt.Find(last)
	if !ok {
		t.Fatal("no last reply")
	}
	h.click("Agents")
	h.tt.Advance(time.Second)
	if after, _ := h.tt.Find(last); after != before {
		t.Errorf("the chat moved from %v to %v as the panel opened", before, after)
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Advance(time.Second)
	if h.a.agentsOpen[top.id()] || h.a.agentsK != 0 {
		t.Errorf("Escape left it open: k %v", h.a.agentsK)
	}
	h.click("Agents")
	h.tt.Advance(time.Second)
	h.tt.ClickAt(before.X+before.W/2, before.Y+before.H/2)
	h.tt.Advance(time.Second)
	if h.a.agentsOpen[top.id()] {
		t.Error("a press outside left it open")
	}
	h.click("Agents")
	h.tt.Advance(time.Second)
	h.tt.Move(10, 10)
	h.tt.Frame()
	hd, _ := h.tt.Find("Close agents")
	h.tt.ClickAt(hd.X-40, hd.Y+hd.H/2) // the head, beside its ✕
	h.tt.Frame()
	if !h.a.agentsOpen[top.id()] {
		t.Error("a press inside closed it")
	}
}

// What fades with the composer's morph takes no presses while it's mostly
// gone: mid-fold, the Agents pill is there but faint, and a press on it
// opens nothing.
func TestComposerFadedTakesNoPresses(t *testing.T) {
	h, top := parentShown(t)
	// Long enough to scroll: the parent's own turns.
	for range 6 {
		h.a.draft = "Again."
		h.tt.Frame()
		h.click("Send")
		h.until("the turn", func() bool { v := h.a.current().View; return !v.Live() && v.Queued == 0 })
	}
	h.tt.Advance(time.Second)
	h.a.listFor(top.id()).ScrollTo(0, ui.Start)
	h.tt.Frame()
	for range 20 {
		if h.a.composerK < 0.6 {
			break
		}
		h.tt.Advance(16 * time.Millisecond)
	}
	if k := h.a.composerK; k <= 0.3 || k >= 0.6 {
		t.Fatalf("k %v; want mid-way, the strip faint", k)
	}
	r, ok := h.tt.Find("Agents")
	if !ok {
		t.Fatal("no Agents")
	}
	h.tt.Press(r.X+r.W/2, r.Y+r.H/2)
	r, _ = h.tt.Find("Agents")
	h.tt.Release(r.X+r.W/2, r.Y+r.H/2)
	if h.a.agentsOpen[top.id()] {
		t.Error("the faint pill took a press")
	}
}
