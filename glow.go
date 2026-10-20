package main

import (
	_ "embed"
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
// again; where the window draws without a GPU, plain text.
func shimmerText(c *ui.Context, text string, size float32, base, shine ui.Color) {
	box := ui.Box(c).Grow(1).Height(size * 1.4)
	box.Label(text).Draw(func(p *ui.Painter, r ui.Rect) {
		_, h := p.MeasureText(0, ui.Span{Text: text, Size: size})
		y := r.Y + (r.H-h)/2
		if !p.Accelerated() {
			p.Text(r.X, y, text, size, base)
			return
		}
		secs := float32(p.Now().Sub(shimmerStart).Seconds())
		shimmer.Text(p, r.X, y, text, size, shader.Options{Values: shimmerValues.
			Float(shimmerTime, secs).Color(shimmerBase, base).Color(shimmerShine, shine)})
		p.AnimationFrame()
	})
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

// workingGlow glows around the composer card while the agent works,
// fading in and out with it; it holds still where the desktop asks for
// less motion.
func workingGlow(c *ui.Context, card ui.Element, working bool) {
	if !gpu {
		return
	}
	target := float32(0)
	if working {
		target = 1
	}
	k := card.AnimateTo("working-glow", target, ui.Animation{Duration: 400 * time.Millisecond, Ease: ease})
	if k < 0.01 {
		return
	}
	angle := float32(0)
	if !c.Preferences().ReduceMotion {
		angle = card.Loop("glow-turn", 5*time.Second, ui.Linear) * 360
	}
	g := ui.ConicGradient{Stops: glowStops, Angle: angle, Oklab: true}
	for _, l := range glowLayers {
		ui.Box(c).Absolute().Top(-1).Left(-1).Right(-1).Bottom(-1).PassThrough().Radius(21).
			Border(l.width, ui.Color{}).BorderConicGradient(g).Blur(l.blur).Opacity(k * l.alpha)
	}
}
