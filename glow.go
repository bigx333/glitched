package main

import (
	_ "embed"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
)

// While the agent works, its label shimmers and the composer glows, as in
// MyGo's chat-glow example: a band of light sweeps across "Working…",
// drawn by a shader (shimmer.kage, from the example, MIT License,
// Copyright (c) 2026 MyGo contributors), and the composer's edge glows in
// a gradient that turns slowly, a sharp border with blurred ones over it.

//go:embed shimmer.kage
var shimmerSource string

var (
	shimmer                                = shader.MustCompile("package main\n" + shimmerSource)
	shimmerTime, shimmerBase, shimmerShine = shimmer.Uniform("Time"), shimmer.Uniform("Base"), shimmer.Uniform("Shine")
	shimmerValues                          = shimmer.NewValues()
	shimmerStart                           = time.Now()
)

// shimmerText is a label with a shine sweeping across it, again and
// again; where the window draws without a GPU, plain text. Long labels
// truncate to fit the box so they never paint over sibling controls.
func shimmerText(c *ui.Context, text string, size float32, base, shine ui.Color) {
	box := ui.Box(c).Grow(1).Shrink(1).MinWidth(0).Height(size * 1.4).Clip()
	box.Label(text).Draw(func(p *ui.Painter, r ui.Rect) {
		shown := fitShimmer(p, text, size, r.W)
		_, h := p.MeasureText(0, ui.Span{Text: shown, Size: size})
		y := r.Y + (r.H-h)/2
		if !p.Accelerated() {
			p.Text(r.X, y, shown, size, base)
			return
		}
		secs := float32(p.Now().Sub(shimmerStart).Seconds())
		shimmer.Text(p, r.X, y, shown, size, shader.Options{Values: shimmerValues.
			Float(shimmerTime, secs).Color(shimmerBase, base).Color(shimmerShine, shine)})
		p.AnimationFrame()
	})
}

// fitShimmer flattens line breaks to spaces and truncates text with an
// ellipsis when it exceeds maxW, keeping single-line shimmer boxes from
// painting over adjacent rows or sibling controls.
func fitShimmer(p *ui.Painter, text string, size, maxW float32) string {
	if strings.ContainsAny(text, "\r\n") {
		text = strings.Join(strings.FieldsFunc(text, func(r rune) bool { return r == '\r' || r == '\n' }), " ")
	}
	if maxW <= 0 || text == "" {
		return text
	}
	if w, _ := p.MeasureText(0, ui.Span{Text: text, Size: size}); w <= maxW {
		return text
	}
	rs := []rune(text)
	lo, hi := 0, len(rs)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if w, _ := p.MeasureText(0, ui.Span{Text: string(rs[:mid]) + "…", Size: size}); w <= maxW {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo == 0 {
		return "…"
	}
	return string(rs[:lo]) + "…"
}

// glowLayers are the composer glow's layers: a border this wide, blurred
// this much, after chat-glow's, which follows Apple Intelligence's glow.
var glowLayers = []struct{ width, blur, alpha float32 }{{1.5, 0, 0.9}, {3, 4, 0.6}, {7, 12, 0.5}}

var glowStops = []ui.GradientStop{
	{Color: ui.Hex("#8b5cf6"), At: 0},
	{Color: ui.Hex("#3b82f6"), At: 0.3},
	{Color: ui.Hex("#ec4899"), At: 0.62},
	{Color: ui.Hex("#a78bfa"), At: 0.85},
	{Color: ui.Hex("#8b5cf6"), At: 1},
}

// gpu is whether the window drew its last frame on a GPU, as a painter
// tells; the view, which builds before painting, reads it. Without one,
// layers blurred every frame cost too much: the glow draws none.
var gpu bool

// noteGPU notes, as the root paints, whether the window paints on a GPU.
func noteGPU(root ui.Element) {
	root.Draw(func(p *ui.Painter, _ ui.Rect) { gpu = p.Accelerated() })
}

// glowTurn is how long the glow takes to turn once.
const glowTurn = 5 * time.Second

// workingGlow glows around the composer card while the agent works,
// fading in and out with it; it holds still where the desktop asks for
// less motion. It's the edge of the Agents panel over the card too, of
// radius r, which turns with it as one: how far is the window's clock, not
// each pane's own. It returns how far in it is.
func workingGlow(c *ui.Context, card ui.Element, working bool, r float32) float32 {
	if !gpu {
		return 0
	}
	target := float32(0)
	if working {
		target = 1
	}
	k := card.AnimateTo("working-glow", target, ui.Animation{Duration: 400 * time.Millisecond, Ease: ease})
	if k < 0.01 {
		return 0
	}
	angle := float32(0)
	if !c.Preferences().ReduceMotion {
		angle = float32(c.Now().Sub(shimmerStart)%glowTurn) / float32(glowTurn) * 360
		c.AnimationFrame()
	}
	g := ui.ConicGradient{Stops: glowStops, Angle: angle, Oklab: true}
	for _, l := range glowLayers {
		ui.Box(c).Absolute().Top(-1).Left(-1).Right(-1).Bottom(-1).PassThrough().Radius(r+1).
			Border(l.width, ui.Color{}).BorderConicGradient(g).Blur(l.blur).Opacity(k * l.alpha)
	}
	return k
}
