package main

import (
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// inSidebar reports whether a thread titled title has a row in the inbox.
func (a *app) inSidebar(title string) bool {
	return slices.ContainsFunc(a.itemsIn(catInbox), func(it inboxItem) bool { return it.t.View.Title == title })
}

// A new thread opens as a hero, its composer in the middle of the pane
// under its heading; sending docks it, frame by frame, the transcript
// fading in at the end, and slides its row into the sidebar.
func TestHeroDocks(t *testing.T) {
	h := start(t, inboxDir(t))
	h.a.select_(h.thread("Investigate websocket reconnect loop"))
	h.tt.Advance(time.Second)
	if h.a.dockK != 1 {
		t.Fatalf("an established thread's composer: dock %v; want 1", h.a.dockK)
	}
	h.click("New thread")
	h.tt.Advance(time.Second)
	if h.a.dockK != 0 || !hero(h.a.current()) {
		t.Fatalf("New thread: dock %v, hero %v", h.a.dockK, hero(h.a.current()))
	}
	if !h.tt.HasText("Send a message to start the thread.") {
		t.Error("the hero has no heading")
	}
	if b, ok := h.tt.Find("Send"); !ok || b.Y < 860/3 || b.Y > 860*2/3 {
		t.Errorf("hero's Send at %v; want in the middle third", b)
	}
	// The press on New thread left the focus on it: the hero takes it.
	h.tt.Type("Build the hero")
	if h.a.draft != "Build the hero" {
		t.Fatalf("typed into the hero: draft %q", h.a.draft)
	}
	h.click("Send")
	if hero(h.a.current()) || !h.a.inSidebar("Build the hero") || !h.a.arriving(h.a.current()) {
		t.Fatalf("sent: hero %v, in the sidebar %v", hero(h.a.current()), h.a.inSidebar("Build the hero"))
	}
	const frame = 16 * time.Millisecond
	prev := h.a.dockK
	for i := range 40 {
		h.tt.Advance(frame)
		d := h.a.dockK
		if d < prev || d-prev > 0.3 {
			t.Fatalf("frame %d: dock went %v → %v", i, prev, d)
		}
		if i == 3 && (d == 0 || d == 1) {
			t.Fatalf("64ms after sending, dock %v; want mid-way", d)
		}
		prev = d
	}
	if h.a.dockK != 1 {
		t.Fatalf("640ms after sending, dock %v; want 1", h.a.dockK)
	}
}

// Into the hero by the pointer, the composer lifts frame by frame, the
// thread left fading out under it before the heading comes in; back to
// that thread mid-way, it turns round from where it is. ⌘N, and less
// motion asked for, put it there at once.
func TestHeroEnters(t *testing.T) {
	h := start(t, inboxDir(t))
	first := h.thread("Investigate websocket reconnect loop")
	h.a.select_(first)
	h.tt.Advance(time.Second)
	h.click("New thread")
	const frame = 16 * time.Millisecond
	prev := float32(1)
	sawLeaving := false
	for i := range 40 {
		h.tt.Advance(frame)
		d := h.a.dockK
		if d > prev || prev-d > 0.3 {
			t.Fatalf("frame %d: dock went %v → %v", i, prev, d)
		}
		if from, ok := h.a.departing(h.a.current()); ok {
			sawLeaving = from.id() == first.id()
		}
		if i == 2 && (d == 0 || d == 1) {
			t.Fatalf("48ms into New thread, dock %v; want mid-way", d)
		}
		prev = d
	}
	if h.a.dockK != 0 || !sawLeaving {
		t.Fatalf("640ms after New thread: dock %v, saw the thread left fade %v", h.a.dockK, sawLeaving)
	}
	// New thread, then straight back: it turns round, never jumping.
	h.a.select_(first)
	h.tt.Advance(time.Second)
	h.click("New thread")
	h.tt.Advance(3 * frame)
	mid := h.a.dockK
	if mid <= 0 || mid >= 1 {
		t.Fatalf("48ms into the hero, dock %v; want mid-way", mid)
	}
	h.a.select_(first)
	prev = mid
	for range 40 {
		h.tt.Advance(frame)
		if d := h.a.dockK - prev; d < -0.3 || d > 0.3 {
			t.Fatalf("turning back, dock went %v → %v", prev, h.a.dockK)
		}
		prev = h.a.dockK
	}
	if h.a.dockK != 1 {
		t.Fatalf("back on the thread left: dock %v; want 1", h.a.dockK)
	}
	// ⌘N: there at once.
	h.tt.Key(ui.Cmd, ui.KeyN)
	h.tt.Frame()
	if h.a.dockK != 0 {
		t.Errorf("⌘N moved: dock %v", h.a.dockK)
	}
	h.a.select_(first)
	h.tt.Advance(time.Second)
	h.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	h.click("New thread")
	h.tt.Frame()
	if h.a.dockK != 0 {
		t.Errorf("with less motion, entering the hero moved: dock %v", h.a.dockK)
	}
}

// The hero is in the middle of the pane, with or without the sidebar:
// the turn markers' room comes in only as the transcript does.
func TestTheHeroIsCentred(t *testing.T) {
	h := start(t, inboxDir(t))
	for _, sidebar := range []bool{true, false} {
		if h.a.showSidebar != sidebar {
			h.a.toggleSidebar(false)
		}
		h.a.newThread()
		h.tt.Advance(time.Second)
		model, ok1 := h.tt.Find("Model")
		send, ok2 := h.tt.Find("Send")
		if !ok1 || !ok2 {
			t.Fatalf("no hero composer: %q", h.tt.Texts())
		}
		paneL := h.a.sidebarWidth * h.a.sidebarOpen
		paneMid := paneL + (1320-paneL)/2
		cardMid := (model.X - 8 + send.X + send.W + 8) / 2
		if d := cardMid - paneMid; d < -1.5 || d > 1.5 {
			t.Errorf("sidebar %v: the hero's card is %.1f DIPs off the pane's middle", sidebar, d)
		}
	}
}
