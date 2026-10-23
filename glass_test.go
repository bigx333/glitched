package main

import (
	"bytes"
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Out of the box, the glass is Apple's: no color of the app's over the
// sidebar, and none over the panels. The tint is an opt-in, and the panels
// a further one, by a switch in Appearance.
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
	if err := tt.Click("Tint the panels"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.settings.tintPanels {
		t.Fatal("the switch does not opt the panels in")
	}
}

// The command palette, a pane of glass over the chat, takes the tint only
// once the panels are opted in.
func TestPanelsTintOnlyWhenAsked(t *testing.T) {
	shot := func(tint float64, panels bool) (*image.RGBA, ui.Rect) {
		a := newApp()
		a.settings.vibrancy, a.settings.tintPanels, a.settings.wind = tint, panels, false
		// A small window and one frame, the palette just opened: the glass
		// is slow to draw by hand.
		tt := ui.NewTester(a.view, 640, 400)
		a.openCommandPalette()
		tt.Frame()
		r, ok := tt.Find("New thread in t3code")
		if !ok || r.W == 0 {
			t.Fatalf("no palette row to look at: %v", r)
		}
		return tt.Image(), r
	}
	// A palette row, the glass around its text: the composer, a pane of
	// its own under the palette, takes no part in it.
	row := func(img *image.RGBA, r ui.Rect) []byte {
		x0, x1 := int(r.X), int(r.X+r.W)
		var b []byte
		for y := int(r.Y) - 6; y < int(r.Y+r.H)+6; y++ {
			b = append(b, img.Pix[img.PixOffset(x0, y):img.PixOffset(x1, y)]...)
		}
		return b
	}
	clear, at := shot(1, false)
	if out, _ := shot(0.85, false); !bytes.Equal(clear.Pix, out.Pix) {
		t.Fatal("the tint reached the window with the panels opted out")
	}
	if in, r := shot(0.85, true); bytes.Equal(row(clear, at), row(in, r)) {
		t.Fatal("the tint did not reach the palette with the panels opted in")
	}
}
