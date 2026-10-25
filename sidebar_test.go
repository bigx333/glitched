package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// The sidebar's keycaps show only while ⌘ is held.
func TestShortcutHints(t *testing.T) {
	a := offline(t)
	tt := ui.NewTester(a.view, 1320, 860)
	inSidebar := func() bool {
		r, ok := tt.Find("⌘")
		return ok && r.X < a.sidebarWidth
	}
	if inSidebar() {
		t.Error("keycaps show without ⌘ held")
	}
	tt.HoldModifiers(ui.Cmd)
	if !inSidebar() {
		t.Error("keycaps hidden while ⌘ is held")
	}
	tt.HoldModifiers(0)
	if inSidebar() {
		t.Error("keycaps still show after ⌘ is let go")
	}
}

// The sidebar's right edge drags it wider or narrower, within limits.
func TestSidebarEdge(t *testing.T) {
	a := offline(t)
	tt := ui.NewTester(a.view, 1320, 860)
	r, ok := tt.Find("Resize sidebar")
	if !ok {
		t.Fatal("no edge to drag")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x+60, y)
	tt.Release(x+60, y)
	if a.sidebarWidth != 332 {
		t.Errorf("sidebar %v wide after dragging its edge 60 right, want 332", a.sidebarWidth)
	}
	r, _ = tt.Find("Resize sidebar")
	x = r.X + r.W/2
	tt.Press(x, y)
	tt.Move(x-500, y)
	tt.Release(x-500, y)
	if a.sidebarWidth != minSidebarWidth {
		t.Errorf("sidebar %v wide after dragging far left, want %v", a.sidebarWidth, minSidebarWidth)
	}
}

// Each project's chip names it, so projects that share an icon and color
// tell apart.
func TestProjectChipsNameTheProject(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	for _, name := range []string{"t3code", "mygo", "agent-phone"} {
		if !h.tt.HasText(name) {
			t.Errorf("no chip naming %s; texts %q", name, h.tt.Texts())
		}
	}
}
