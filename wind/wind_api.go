// Package wind is the texture behind the composer of the Claude desktop
// app's Code tab: a field of monospaced characters that a wind lights in
// passing gusts, which the pointer warms and clicks ripple. It is MyGo's
// wind example (examples/wind, MIT License, Copyright (c) 2026 MyGo
// contributors), its texture kept as it is, with this file exporting it
// for the chat's background.
package wind

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// Texture is a field of glyphs blown by a wind.
type Texture struct{ t *texture }

// New returns a texture blown by the wind of seed. later runs a function
// outside the window's frames, where the glyph atlases are drawn
// (Window.Update); nil draws them in the frame.
func New(seed uint32, later func(fn func())) *Texture {
	return &Texture{newTexture(seed, later)}
}

// SetLater sets where the atlases are drawn, once the window exists.
func (x *Texture) SetLater(later func(fn func())) { x.t.later = later }

// Draw paints the texture in r with p, moving it on when its next frame
// is due. It draws nothing where the window does not draw on a GPU.
func (x *Texture) Draw(p *ui.Painter, r ui.Rect, dark bool) { x.t.draw(p, r, dark) }

// SetStill shows the wind as it stands, without moving, as while the
// desktop asks for less motion.
func (x *Texture) SetStill(still bool) { x.t.still = still }

// Move warms the air where the pointer moved, at (x, y) in the pane, in
// DIPs; it reports whether the texture was resting, and needs a frame to
// wake.
func (x *Texture) Move(px, py float32) bool {
	calm := x.t.calm
	x.t.move(px, py, time.Now())
	return calm
}

// Tap blows a ripple out from (x, y); every third is a heart.
func (x *Texture) Tap(px, py float32) { x.t.tap(px, py, time.Now()) }

// Wake has the wind blow again for its whole budget, after the user did
// something, as typing.
func (x *Texture) Wake() { x.t.wake() }

// Reseed starts another wind, in a look the last ones did not show.
func (x *Texture) Reseed(seed uint32) { x.t.reseed(seed) }

// Look returns the name of the set of characters it shows, and its seed.
func (x *Texture) Look() (string, uint32) { return x.t.w.look, x.t.w.seed }
