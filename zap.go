package main

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
)

// An approval answered is zapped away as fast mode's lightning strikes:
// its answers go to pixels, which break off them from the button pressed
// outward, flash and scatter, in fast mode's electric blue for Allow once
// and in red for Deny; then the room the row took closes on the
// composer's spring, and the card keeps its answer as it always does.
// Where the desktop asks for less motion, or there's no GPU, the row
// fades and closes.

// zapTime is how long the answers take to break up: as long as fast
// mode's strike.
const zapTime = strikeTime

// approvalZap is a request's answers as they were answered, kept to break
// up.
type approvalZap struct {
	request api.ID
	at      time.Time
	// h is the row's height; ox, oy where in it the button pressed was;
	// parts the buttons, in the row, that break up; denied whether Deny was
	// pressed, which breaks them up in red.
	h, ox, oy float32
	parts     []zapPart
	labels    []zapLabel
	denied    bool
}

// zapPart is a button that breaks up: where it is in the row, and its
// face.
type zapPart struct {
	r    ui.Rect
	body ui.Color
}

// zapLabel is a label of the row, painted as the row breaks up so that
// nothing of it is left to press or to read out: where it is, its text,
// and a key's chip, its padding and colors.
type zapLabel struct {
	r          ui.Rect
	span       ui.Span
	pad        float32
	bg, border ui.Color
}

// answerRow is where a waiting request's answers were laid out in the last
// frame, for a zap to break up from the button pressed, its key's too.
type answerRow struct {
	request          api.ID
	row, allow, deny ui.Rect
	labels           []zapLabel
}

// answer answers r, breaking its answers up from the button for it when
// the row showed in the last frame.
func (a *app) answer(c *ui.Context, t thread, r *client.RequestEntry, ans client.Answer) {
	if at := a.answerAt; at.request == r.Request && at.row.H > 0 {
		b := at.allow
		if ans == client.Deny {
			b = at.deny
		}
		in := func(x ui.Rect) ui.Rect { return ui.Rect{X: x.X - at.row.X, Y: x.Y - at.row.Y, W: x.W, H: x.H} }
		labels := make([]zapLabel, len(at.labels))
		for i, l := range at.labels {
			l.r = in(l.r)
			labels[i] = l
		}
		a.zap = approvalZap{
			request: r.Request, at: c.Now(), h: at.row.H,
			ox: b.X + b.W/2 - at.row.X, oy: b.Y + b.H/2 - at.row.Y,
			parts:  []zapPart{{in(at.allow), palette.accent}, {in(at.deny), palette.red.Alpha(0.18)}},
			labels: labels, denied: ans == client.Deny,
		}
	}
	if a.backend.Respond(t.env, t.id(), r.Request, ans).IsZero() {
		a.zap = approvalZap{}
	}
}

// zapping is whether r's answers are breaking up.
func (a *app) zapping(r *client.RequestEntry) bool { return a.zap.request == r.Request }

// zapRow is r's answers breaking up, in the room they took, which closes
// once they're half gone. It's all painted: the buttons are gone.
func (a *app) zapRow(c *ui.Context) {
	z := a.zap
	since := c.Now().Sub(z.at)
	k := min(1, float32(since)/float32(zapTime))
	still := c.Preferences().ReduceMotion || !gpu
	hold := zapTime / 2
	if still {
		hold = zapTime
	}
	target := float32(1)
	if since >= hold {
		target = 0
	}
	slot := ui.Box(c).Key("decide-zap")
	room := slot.AnimateTo("room", target, composerClose)
	// Done, or long done where it wasn't shown, as the card scrolled away.
	if k >= 1 && room < 0.01 || since > 2*zapTime+time.Second {
		a.zap = approvalZap{}
		return
	}
	c.AnimationFrame()
	slot.Height(z.h * room).Shrink(0).ClipY().PassThrough().Children(func() {
		ui.Box(c).Key("zap").Absolute().Top(0).Left(0).Right(0).Height(z.h).PassThrough().
			Draw(func(p *ui.Painter, rect ui.Rect) {
				k := min(1, float32(p.Now().Sub(z.at))/float32(zapTime))
				// The row as it was, fading as its pixels take its place.
				fade := 1 - smoothstep(0, 0.18, k)
				if still {
					fade = 1 - k
				}
				drawAnswers(p, rect, z, fade)
				if !still && k < 1 {
					drawZap(p, rect, z, k, palette.muted)
				}
				if k < 1 {
					p.AnimationFrame()
				}
			})
	})
}

// drawAnswers paints the row of answers as it was laid out, in r, at
// opacity o: the buttons' faces, their labels and their keys.
func drawAnswers(p *ui.Painter, r ui.Rect, z approvalZap, o float32) {
	if o <= 0 {
		return
	}
	at := func(x ui.Rect) ui.Rect { return ui.Rect{X: r.X + x.X, Y: r.Y + x.Y, W: x.W, H: x.H} }
	for _, part := range z.parts {
		p.Fill(at(part.r), part.body.Alpha(o*float32(part.body.A)/255), 6)
	}
	for _, l := range z.labels {
		b := at(l.r)
		if l.bg != (ui.Color{}) {
			p.Fill(b, l.bg.Alpha(o*float32(l.bg.A)/255), 4)
		}
		if l.border != (ui.Color{}) {
			p.Stroke(b, l.border.Alpha(o*float32(l.border.A)/255), 4, 1)
		}
		s := l.span
		s.Color = s.Color.Alpha(o * float32(s.Color.A) / 255)
		w, h := p.MeasureText(0, s)
		x := b.X + l.pad
		if l.pad > 0 {
			x = b.X + (b.W-w)/2 // a key's chip centres its keys
		}
		p.RichText(x, b.Y+(b.H-h)/2, 0, s)
	}
}

// A denied approval's flash: white, warmed with red.
var deniedCore = ui.Hex("#ffe4e4")

// drawZap draws a request's answers breaking up, k from 0 to 1 into it, in
// r, the row: each button a grid of pixels of its face as it fades, pixels
// breaking off them outward from the button pressed, the nearest first,
// with fast mode's flash. Those that fly flash white as they go, then fade
// to ink, some to fast mode's blue; the rest just go. Denied, they break up
// in red, the red of Delete and of Full access: red pixels, a red flash.
func drawZap(p *ui.Painter, r ui.Rect, z approvalZap, k float32, ink ui.Color) {
	const cell, rad, life = 4, 6, 0.45
	glow, core, spark := boltGlow, boltCore, electric
	if z.denied {
		glow, core, spark, ink = dangerDeep, deniedCore, palette.red, palette.red.Mix(dangerDeep, 0.35)
	}
	ox, oy := r.X+z.ox, r.Y+z.oy
	reach := float32(math.Hypot(float64(max(z.ox, r.W-z.ox)), float64(r.H)))
	drawGlow(p, ox, oy, 28, glow, 0.6*strobe(k))
	shown := smoothstep(0, 0.18, k)
	for pi, part := range z.parts {
		b := ui.Rect{X: r.X + part.r.X, Y: r.Y + part.r.Y, W: part.r.W, H: part.r.H}
		for gy := range int(b.H / cell) {
			for gx := range int(b.W / cell) {
				// Each button's pixels their own.
				sx := gx + 1000*pi
				x, y := b.X+float32(gx)*cell, b.Y+float32(gy)*cell
				cx, cy := x+cell/2, y+cell/2
				// Out of the button's rounded corners.
				qx := max(b.X+rad-cx, cx-(b.X+b.W-rad), 0)
				qy := max(b.Y+rad-cy, cy-(b.Y+b.H-rad), 0)
				if math.Hypot(float64(qx), float64(qy)) > rad {
					continue
				}
				dx, dy := cx-ox, cy-oy
				d := float32(math.Hypot(float64(dx), float64(dy)))
				launch := 0.4*d/reach + 0.12*hash(sx, gy, 21)
				if k < launch {
					p.Fill(ui.Rect{X: x, Y: y, W: cell - 1, H: cell - 1}, part.body.Alpha(shown*float32(part.body.A)/255), 0)
					continue
				}
				u := (k - launch) / life
				if u >= 1 || hash(sx, gy, 22) > 0.55 {
					continue
				}
				// Away from the button, give or take, and drifting up.
				n := max(d, 1)
				ux := dx/n + (hash(sx, gy, 23)-0.5)*0.9
				uy := dy/n + (hash(sx, gy, 24)-0.5)*0.9 - 0.6
				e := 1 - (1-u)*(1-u)*(1-u) // eases out
				dist := (14 + 34*hash(sx, gy, 25)) * e
				col := ink
				if hash(sx, gy, 27) > 0.6 {
					col = spark
				}
				col = core.Mix(col, smoothstep(0, 0.25, u))
				px, py := cx+ux*dist, cy+uy*dist
				p.Fill(ui.Rect{X: px - 1, Y: py - 1, W: 2, H: 2}, col.Alpha((1-u)*(1-u)), 0)
			}
		}
	}
}
