package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// The sidebar's commands show their keycaps, so the shortcuts are found.
func TestKeycaps(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	if r, ok := tt.Find("⌘"); !ok || r.X > a.sidebarWidth {
		t.Errorf("no keycap in the sidebar (found %v at %v)", ok, r)
	}
}

// The sidebar's right edge drags it wider or narrower, within limits,
// and Archived opens the archived threads.
func TestSidebarEdge(t *testing.T) {
	a := newApp()
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
	if err := tt.Click("Archived"); err != nil {
		t.Fatal(err)
	}
	if a.router.Path() != "/settings/archived" {
		t.Errorf("Archived opened %q", a.router.Path())
	}
}
