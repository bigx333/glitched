package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Out of the box, the glass is Apple's: no color of the app's over the
// sidebar, and none over the panels. The tint is an opt-in, and the panels
// a further one.
func TestGlassUntintedByDefault(t *testing.T) {
	a := newApp()
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
	if _, ok := tt.Find("Tint panels"); !ok {
		t.Fatal("no Tint panels switch in Appearance")
	}
}

// The command palette, a pane of glass over the chat, takes the tint only
// once the panels are opted in.
func TestPanelsTintOnlyWhenAsked(t *testing.T) {
	shot := func(tint float64, panels bool) []byte {
		a := newApp()
		a.settings.vibrancy, a.settings.tintPanels, a.settings.wind = tint, panels, false
		// A small window and one frame, the palette just opened: the glass
		// is slow to draw by hand.
		tt := ui.NewTester(a.view, 640, 400)
		a.openCommandPalette()
		tt.Frame()
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
