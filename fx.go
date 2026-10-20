package main

// Pixel effects for the effort slider and fast mode, adapted from MyGo's
// effort-slider example (examples/effort-slider/galaxy.go, MIT License,
// Copyright (c) 2026 MyGo contributors): twinkling pixels, a burst of
// pixels and soft glows, drawn with a Painter.

import (
	"image"
	"math"

	"github.com/egoist/mygo/ui"
)

// hash returns a number from 0 to 1 that only depends on its arguments.
func hash(a, b, c int) float32 {
	h := uint32(a)*0x9e3779b1 ^ uint32(b)*0x85ebca77 ^ uint32(c)*0xc2b2ae3d
	h ^= h >> 15
	h *= 0x2c1b3c6d
	h ^= h >> 12
	h *= 0x297a2d39
	h ^= h >> 15
	return float32(h&0xffffff) / 0xffffff
}

// twinkle is how lit a pixel seeded by a and b is at t seconds: it blinks
// on and off at a pace of its own.
func twinkle(a, b int, t float64) float32 {
	period := 0.7 + 1.1*float64(hash(a, b, 1))
	k := math.Mod(t/period+float64(hash(a, b, 2)), 1)
	on := 0.35 + 0.4*float64(hash(a, b, 3))
	if k > on {
		return 0
	}
	return float32(math.Sin(k / on * math.Pi))
}

func smoothstep(lo, hi, x float32) float32 {
	k := min(max((x-lo)/(hi-lo), 0), 1)
	return k * k * (3 - 2*k)
}

// drawBurst draws a ring of pixels bursting out of (cx, cy), k from 0 to
// 1 into the burst, from radius r0 to about r1.
func drawBurst(p *ui.Painter, cx, cy, r0, r1, k float32, colors []ui.Color) {
	if k <= 0 || k >= 1 {
		return
	}
	const n = 24
	e := float32(1 - math.Pow(float64(1-k), 3)) // eases out
	for i := range n {
		angle := (float64(i) + 0.3*float64(hash(i, 1, 8))) * 2 * math.Pi / n
		r := r0 + (r1-r0)*(0.5+0.5*hash(i, 1, 9))*e
		sin, cos := math.Sincos(angle)
		x, y := cx+r*float32(cos), cy+r*float32(sin)
		p.Fill(ui.Rect{X: x - 1, Y: y - 1, W: 2, H: 2}, colors[i%len(colors)].Alpha(1-k*k), 0)
	}
}

// glows are soft round glows, by color and opacity step: the painter has
// no blur of shapes, so a glow is a bitmap made once.
var glows = map[int]*ui.Bitmap{}

const glowSteps = 32

// drawGlow draws a soft glow of color c and opacity a, radius r DIPs,
// centered on (cx, cy).
func drawGlow(p *ui.Painter, cx, cy, r float32, c ui.Color, a float32) {
	step := int(min(a, 1)*glowSteps + 0.5)
	if step <= 0 {
		return
	}
	key := step<<24 | int(c.R)<<16 | int(c.G)<<8 | int(c.B)
	bmp := glows[key]
	if bmp == nil {
		const n = 96
		img := image.NewRGBA(image.Rect(0, 0, n, n))
		alpha := float64(step) / glowSteps
		for y := range n {
			for x := range n {
				dx, dy := (float64(x)+0.5)/n*2-1, (float64(y)+0.5)/n*2-1
				d2 := dx*dx + dy*dy
				v := alpha * math.Exp(-d2*4.5) * math.Max(0, 1-d2)
				i := img.PixOffset(x, y)
				img.Pix[i] = uint8(float64(c.R)*v + 0.5)
				img.Pix[i+1] = uint8(float64(c.G)*v + 0.5)
				img.Pix[i+2] = uint8(float64(c.B)*v + 0.5)
				img.Pix[i+3] = uint8(255*v + 0.5)
			}
		}
		bmp = ui.NewBitmap(img)
		glows[key] = bmp
	}
	p.Image(bmp, ui.Rect{X: cx - r, Y: cy - r, W: 2 * r, H: 2 * r}, ui.FillBox)
}

var (
	// Electric blue, around a core of blue-white.
	boltGlow = ui.Hex("#3b9eff")
	boltCore = ui.Hex("#eaf6ff")
)

// strobe is how lit lightning is k of the way through a strike, from 0
// to 1: a few flickers, each dimmer than the last, as a real strike's
// return strokes are.
func strobe(k float32) float32 {
	pattern := []float32{1, 0.15, 0.9, 0, 0.65, 0.2, 0.4, 0, 0.15, 0}
	i := int(k * float32(len(pattern)))
	if i < 0 || i >= len(pattern) {
		return 0
	}
	return pattern[i]
}

// drawBolt draws a jagged bolt of lightning from (x, y) along angle
// (radians), length DIPs long, forking once or twice, with a glow around
// a bright core; seed picks its shape, a its opacity.
func drawBolt(p *ui.Painter, x, y, angle, length, width float32, seed int, a float32) {
	if a <= 0.02 || length < 3 {
		return
	}
	const segs = 6
	sin, cos := math.Sincos(float64(angle))
	dx, dy := float32(cos), float32(sin)
	step := length / segs
	var path ui.Path
	path.MoveTo(x, y)
	type fork struct{ x, y float32 }
	var forks []fork
	for i := 1; i <= segs; i++ {
		jitter := (hash(seed, i, 1) - 0.5) * step * 1.6
		if i == segs {
			jitter *= 0.4
		}
		px := x + dx*step*float32(i) - dy*jitter
		py := y + dy*step*float32(i) + dx*jitter
		path.LineTo(px, py)
		if (i == 2 || i == 4) && hash(seed, i, 2) > 0.45 {
			forks = append(forks, fork{px, py})
		}
	}
	p.StrokePath(&path, width*4, boltGlow.Alpha(0.28*a))
	p.StrokePath(&path, width*2, boltGlow.Alpha(0.6*a))
	p.StrokePath(&path, width, boltCore.Alpha(a))
	for j, f := range forks {
		turn := float32(0.7)
		if hash(seed, j, 3) > 0.5 {
			turn = -turn
		}
		drawBolt(p, f.x, f.y, angle+turn, length*0.45, width*0.7, seed*31+j+7, a*0.8)
	}
}

// perimeter returns the point d DIPs clockwise around the rounded
// rectangle r, its corners rounded by rad, from the start of its top
// edge, and the outward normal there.
func perimeter(r ui.Rect, rad, d float32) (x, y, nx, ny float32) {
	rad = min(rad, r.W/2, r.H/2)
	w, h := r.W-2*rad, r.H-2*rad
	arc := float32(math.Pi/2) * rad
	total := 2*(w+h) + 4*arc
	d = float32(math.Mod(float64(d), float64(total)))
	if d < 0 {
		d += total
	}
	// corner returns the point at angle (radians, clockwise from the
	// right) around the corner centered on (cx, cy).
	corner := func(cx, cy, angle float32) (float32, float32, float32, float32) {
		sin, cos := math.Sincos(float64(angle))
		return cx + rad*float32(cos), cy + rad*float32(sin), float32(cos), float32(sin)
	}
	left, top, right, bottom := r.X+rad, r.Y+rad, r.X+r.W-rad, r.Y+r.H-rad
	switch {
	case d < w: // top edge
		return left + d, r.Y, 0, -1
	case d < w+arc: // top right corner
		return corner(right, top, -math.Pi/2+(d-w)/rad)
	case d < w+arc+h: // right edge
		return r.X + r.W, top + d - w - arc, 1, 0
	case d < w+2*arc+h: // bottom right corner
		return corner(right, bottom, (d-w-arc-h)/rad)
	case d < 2*w+2*arc+h: // bottom edge
		return right - (d - w - 2*arc - h), r.Y + r.H, 0, 1
	case d < 2*w+3*arc+h: // bottom left corner
		return corner(left, bottom, math.Pi/2+(d-2*w-2*arc-h)/rad)
	case d < 2*w+3*arc+2*h: // left edge
		return r.X, bottom - (d - 2*w - 3*arc - h), -1, 0
	default: // top left corner
		return corner(left, top, math.Pi+(d-2*w-3*arc-2*h)/rad)
	}
}

// drawElectricBorder draws electricity running around r: a faint charged
// outline, and arcs that crackle as they race around it, their jagged
// shape new twenty times a second.
func drawElectricBorder(p *ui.Painter, r ui.Rect, radius float32, secs float64, a float32) {
	p.Stroke(r, boltGlow.Alpha(0.35*a), radius, 1)
	rad := min(radius, r.W/2, r.H/2)
	total := 2*(r.W+r.H-4*rad) + 2*math.Pi*rad
	const arcs = 3
	tick := int(secs * 20)
	for i := range arcs {
		head := float32(math.Mod(secs*0.45+float64(i)/arcs, 1)) * total
		length := total * (0.12 + 0.08*hash(i, tick, 4))
		var path ui.Path
		const pitch = 3
		n := int(length / pitch)
		for j := 0; j <= n; j++ {
			d := head - length + float32(j)*pitch
			x, y, nx, ny := perimeter(r, radius, d)
			// Jagged, and tapering to nothing at both ends of the arc.
			taper := float32(math.Sin(float64(j) / float64(n) * math.Pi))
			off := (hash(i*97+j, tick, 5) - 0.5) * 3 * taper
			x, y = x+nx*off, y+ny*off
			if j == 0 {
				path.MoveTo(x, y)
			} else {
				path.LineTo(x, y)
			}
		}
		lit := a * (0.6 + 0.4*hash(i, tick, 6))
		p.StrokePath(&path, 4, boltGlow.Alpha(0.25*lit))
		p.StrokePath(&path, 1.2, boltCore.Alpha(0.95*lit))
		hx, hy, _, _ := perimeter(r, radius, head)
		drawGlow(p, hx, hy, 7, boltGlow, 0.6*lit)
	}
}

// drawFlow draws light flowing through r, left to right, as pulses of
// data through a fibre: lanes of comets, a bright head and a tail fading
// behind it, over a faint glowing core. level, from 0 to 3, sets how many
// pulses run and how fast; secs is the time, which moves them.
func drawFlow(p *ui.Painter, r ui.Rect, level int, secs float64) {
	if r.W <= 0 {
		return
	}
	k := float32(level) / 3
	// The core: a line of light along the fibre, brighter with effort.
	core := ui.Rect{X: r.X, Y: r.Y + r.H/2 - 0.75, W: r.W, H: 1.5}
	p.FillGradient(core, ui.LinearGradient{From: boltCore.Alpha(0), To: boltCore.Alpha(0.12 + 0.25*k), Angle: 90}, 0.75)

	lanes := []float32{0.3, 0.5, 0.7} // across the fibre
	pulses := 1 + level               // a lane's
	speed := 40 + 55*float64(level)   // DIPs a second
	tail := 18 + 22*k
	span := float64(r.W + tail)
	for li, lane := range lanes {
		y := r.Y + r.H*lane
		for i := range pulses {
			seed := li*17 + i
			// Each pulse at a pace and a place of its own along the lane.
			v := speed * (0.8 + 0.4*float64(hash(seed, 0, 11)))
			x := r.X + float32(math.Mod(secs*v+float64(hash(seed, 0, 12))*span, span)) - tail
			lit := (0.45 + 0.55*hash(seed, 0, 13)) * (0.5 + 0.5*k)
			h := float32(1.2)
			if li == 1 {
				h = 1.8 // the middle lane, the brightest
			}
			p.FillGradient(ui.Rect{X: x, Y: y - h/2, W: tail, H: h},
				ui.LinearGradient{From: boltCore.Alpha(0), To: boltCore.Alpha(0.9 * lit), Angle: 90}, h/2)
			drawGlow(p, x+tail, y, 3+2*k, boltCore, 0.55*lit)
		}
	}
}
