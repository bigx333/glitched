package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// The sidebar's button that hides it never draws under the window's
// buttons (macOS's traffic lights) as the sidebar slides out or back in:
// on no frame the window asks for does it start left of their room.
func TestTheHideSidebarButtonClearsTheTrafficLightsAsItSlides(t *testing.T) {
	a := offline(t)
	tt := newTester(t, a, 760, 860)
	const lights = 78
	tt.SetTitleBar(ui.TitleBar{Height: 52, Left: lights})
	settleTester(tt)
	if _, ok := tt.Find("Hide sidebar  ⌘B"); !ok {
		t.Fatalf("the open sidebar has no Hide sidebar button: %q", tt.Texts())
	}
	for _, way := range []string{"closing", "opening"} {
		a.toggleSidebar(false)
		tt.Frame()
		for i := range 60 {
			if r, ok := tt.Find("Hide sidebar  ⌘B"); ok && r.W > 0 && r.X < lights {
				t.Fatalf("%s, frame %d draws Hide sidebar at %+v, under the traffic lights' %d points (sidebar %v open)", way, i, r, lights, a.sidebarOpen)
			}
			tt.Advance(16 * time.Millisecond)
		}
		if want := way == "opening"; a.sidebarOpen != map[bool]float32{true: 1, false: 0}[want] {
			t.Fatalf("%s didn't end: the sidebar is %v open", way, a.sidebarOpen)
		}
	}
	if _, ok := tt.Find("Hide sidebar  ⌘B"); !ok {
		t.Fatalf("open again, the sidebar has no Hide sidebar button: %q", tt.Texts())
	}
}
