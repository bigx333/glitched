package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Out of the box, the glass is Apple's: no color of the app's over the
// sidebar, and none over the panels. The tint is an opt-in, and the panels
// a further one, by a switch in Appearance.
func TestGlassUntintedByDefault(t *testing.T) {
	a := offline(t)
	if tint := a.glassTint(); tint.A != 0 {
		t.Fatalf("the bars are tinted out of the box: %v", tint)
	}
	if a.settings.tintPanels {
		t.Fatal("the panels are tinted out of the box")
	}
	a.router.Push("/settings/appearance")
	tt := ui.NewTester(a.view, 1320, 860)
	tt.Frame()
	tt.Advance(time.Second)
	if err := tt.Click("Tint the panels"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.settings.tintPanels {
		t.Fatal("the switch does not opt the panels in")
	}
}

// The command palette, a pane of glass, takes the tint only once the
// panels are opted in. It opens over the General settings, where no other
// pane of glass is in the frame: the frames then differ by the palette
// alone.
func TestPanelsTintOnlyWhenAsked(t *testing.T) {
	shot := func(tint float64, panels bool) []byte {
		a := offline(t)
		a.settings.vibrancy, a.settings.tintPanels = tint, panels
		a.router.Push("/settings/general")
		// A small window and one frame, the palette just opened: the glass
		// is slow to draw by hand.
		tt := ui.NewTester(a.view, 640, 400)
		a.openCommandPalette()
		tt.Frame()
		if _, ok := tt.Find("Toggle diff panel"); !ok {
			t.Fatal("no palette")
		}
		return tt.Image().Pix
	}
	clear := shot(1, false)
	if !bytes.Equal(clear, shot(0.85, false)) {
		t.Fatal("the tint reached the palette with the panels opted out")
	}
	if bytes.Equal(clear, shot(0.85, true)) {
		t.Fatal("the tint did not reach the palette with the panels opted in")
	}
}
