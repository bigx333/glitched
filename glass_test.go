package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Out of the box, the glass takes a tint: the Glass tint setting is at
// 0.70, so the app's background lies at 30% over the sidebar's glass, in
// the light theme the System choice starts in as in the dark one, and the
// panels take the same tint. The switch in Appearance turns the panels off.
func TestGlassTintedByDefault(t *testing.T) {
	a := offline(t)
	if a.settings.vibrancy != 0.7 {
		t.Fatalf("the Glass tint is %v out of the box, want 0.7", a.settings.vibrancy)
	}
	if !a.settings.tintPanels {
		t.Fatal("the panels are untinted out of the box")
	}
	a.router.Push("/settings/appearance")
	tt := ui.NewTester(a.view, 1320, 860)
	tt.Frame()
	tt.Advance(time.Second)
	if bg := palette.bg; bg != ui.Hex("#ffffff") {
		t.Fatalf("the System theme starts in %v, want the light background", bg)
	}
	// 30% of 255, rounded: 77.
	if tint := a.glassTint(); tint.A != 77 {
		t.Fatalf("the light theme's tint is %v, want alpha 77", tint)
	}
	a.settings.theme = "Dark"
	tt.Frame()
	if bg := palette.bg; bg != ui.Hex("#0c0c0e") {
		t.Fatalf("the Dark theme draws on %v", bg)
	}
	if tint := a.glassTint(); tint.A != 77 {
		t.Fatalf("the dark theme's tint is %v, want alpha 77", tint)
	}
	if err := tt.Click("Tint the panels"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.settings.tintPanels {
		t.Fatal("the switch does not turn the panels off")
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
