package main

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

type modelInfo struct {
	name, short, desc string
	provider          string
	color             ui.Color
	icon              *ui.SVG
}

type family struct {
	name, desc string
	color      ui.Color
	icon       *ui.SVG
}

var families = []family{
	{"Claude", "Anthropic", ui.Hex("#d97757"), icClaude},
	{"Codex", "OpenAI", ui.Hex("#10a37f"), icOpenAI},
	{"Gemini", "Google", ui.Hex("#4f7cf7"), icGemini},
	{"Grok", "xAI", ui.Hex("#2c2c31"), icGrok},
}

var models = []modelInfo{
	{"Claude Opus 5.5", "Opus 5.5", "Most capable, for complex work", "Claude", ui.Hex("#d97757"), icClaude},
	{"Claude Sonnet 5.5", "Sonnet 5.5", "Smart and fast, for every day", "Claude", ui.Hex("#d97757"), icClaude},
	{"Claude Haiku 5.5", "Haiku 5.5", "Fastest, for quick edits", "Claude", ui.Hex("#d97757"), icClaude},
	{"GPT-5.5 Codex", "GPT-5.5 Codex", "Tuned for agentic coding", "Codex", ui.Hex("#10a37f"), icOpenAI},
	{"GPT-5.5", "GPT-5.5", "General reasoning", "Codex", ui.Hex("#10a37f"), icOpenAI},
	{"Gemini 3 Pro", "Gemini 3 Pro", "Long context, sees images", "Gemini", ui.Hex("#4f7cf7"), icGemini},
	{"Gemini 3 Flash", "Gemini 3 Flash", "Fast and light", "Gemini", ui.Hex("#4f7cf7"), icGemini},
	{"Grok 4", "Grok 4", "Reasoning, with what's happening now", "Grok", ui.Hex("#2c2c31"), icGrok},
	{"Grok Code Fast", "Grok Code Fast", "Quick agentic coding", "Grok", ui.Hex("#2c2c31"), icGrok},
}

// modelNames is every model's name, for the settings' choice.
func modelNames() []string {
	names := make([]string, len(models))
	for i, m := range models {
		names[i] = m.name
	}
	return names
}

func modelByName(name string) modelInfo {
	for _, m := range models {
		if m.name == name {
			return m
		}
	}
	return models[0]
}

var efforts = []string{"Low", "Medium", "High", "Max"}

// snap carries the effort's thumb to its level: a spring without bounce,
// which takes over a drag at the speed the pointer let it go.
var snap = ui.Animation{Spring: ui.SpringOptions{Duration: 300 * time.Millisecond}}

// snapZone is how near a level's point, in levels, the dragged thumb
// locks onto it.
const snapZone = 0.12

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// effortStops is the effort track's gradient, blue to red, laid across
// the whole track: the more effort, the more of it, and the hotter, the
// fill shows.
var effortStops = []ui.GradientStop{
	{Color: ui.Hex("#2563eb"), At: 0},
	{Color: ui.Hex("#7c3aed"), At: 0.4},
	{Color: ui.Hex("#db2777"), At: 0.72},
	{Color: ui.Hex("#ef4444"), At: 1},
}

// effortColor is the color of an effort level, the gradient's at its stop.
func effortColor(level int) ui.Color {
	return []ui.Color{ui.Hex("#60a5fa"), ui.Hex("#a78bfa"), ui.Hex("#f472b6"), ui.Hex("#f87171")}[level]
}

var (
	maxPixels = []ui.Color{ui.Hex("#fecaca"), ui.Hex("#fda4af"), ui.Hex("#ffffff"), ui.Hex("#fca5a5"), ui.Hex("#f9a8d4")}
	// electric is fast mode's color: its lightning, its filled icon.
	electric = ui.Hex("#60c3ff")
)

func (a *app) effortLevel() int {
	for i, e := range efforts {
		if e == a.effort {
			return i
		}
	}
	return 1
}

// modelPicker is the composer's model button, and the popover it opens:
// the families of models beside the models of the one pointed at, as a
// submenu, and the effort slider. High effort lights a glow around the
// button, and fast mode runs electricity around it in the glow's place.
func (a *app) modelPicker(c *ui.Context) {
	m := modelByName(a.model)
	level := a.effortLevel()
	b := a.tip(c, ghost(c, "model", a.pickerOpen).Padding(4, 8, 4, 4).Gap(6).Label("Model"), "Model and effort")
	if b.Children(func() {
		a.pickerGlow(c, b, level)
		if a.fast {
			a.electricBorder(c)
		}
		modelTile(c, m.icon, m.color, 18)
		ui.Text(c, a.model).FontSize(12).SingleLine()
		ui.Text(c, a.effort).FontSize(11).FontWeight(600).TextColor(effortColor(level))
		if a.fast {
			a.zapIcon(c, "trigger-zap", 12)
		}
		ui.Icon(c, icChevronD).FontSize(11)
	}).Clicked() {
		a.pickerOpen = !a.pickerOpen
		a.pickerFamily = m.provider
	}
	if a.pickerFamily == "" {
		a.pickerFamily = m.provider
	}
	a.popover(c, b, &a.pickerOpen, func(panel ui.Element) {
		panel.AttachTo(b, ui.AnchorTopStart, ui.AnchorBottomStart).Margin(0, 0, 10, -4).Width(420).Radius(16).Clip()
		a.frosted(c, panel).Shadow(0, 16, 40, 0, ui.RGBA(0, 0, 0, 0.35)).Children(func() {
			ui.Row(c).AlignItems(ui.Stretch).Children(func() {
				a.pickerFamilies(c)
				ui.Box(c).Width(1).Background(palette.border)
				a.pickerModels(c)
			})
			ui.Divider(c).Background(palette.border)
			a.effortSlider(c)
		})
	})
}

// fastButton turns fast mode on and off, beside the effort in the
// picker: just its lightning, which strikes as it comes on.
func (a *app) fastButton(c *ui.Context) {
	label := "Fast mode: off"
	if a.fast {
		label = "Fast mode: on"
	}
	b := a.tip(c, ghost(c, "fast", false).Padding(6).Role(ui.RoleSwitch).Checked(a.fast).Label("Fast mode"), label)
	if b.Children(func() { a.zapIcon(c, "fast-zap", 14) }).Clicked() {
		a.fast = !a.fast
		if a.fast {
			a.fastAt = c.Now()
		}
	}
}

// modelTile is a model's provider mark, white on the provider's color.
func modelTile(c *ui.Context, icon *ui.SVG, color ui.Color, size float32) {
	ui.Box(c).Size(size, size).Radius(size * 0.3).Center().
		LinearGradient(ui.LinearGradient{From: color.Mix(ui.Hex("#ffffff"), 0.12), To: color.Mix(ui.Hex("#000000"), 0.15), Angle: 135, Oklab: true}).
		Children(func() { ui.Icon(c, icon).FontSize(size * 0.68).TextColor(ui.Hex("#ffffff")) })
}

// pickerGlow is a soft glow around the model button, in the effort's
// colors from High up: borders of a gradient, a
// sharp one and blurred ones stacked. At Max it slowly turns. Fast mode's
// electricity takes its place. Without a GPU only the sharp one shows: a
// blurred layer is drawn anew every frame, and the glass composer under it
// with it.
func (a *app) pickerGlow(c *ui.Context, b ui.Element, level int) {
	on := float32(0)
	switch {
	case a.fast:
	case level >= 3:
		on = 1
	case level == 2:
		on = 0.55
	}
	// The glow fades in and out as the settings change.
	k := b.AnimateTo("glow", on, ui.Animation{Duration: 250 * time.Millisecond, Ease: easeOut})
	if k < 0.01 {
		return
	}
	stops := []ui.GradientStop{
		{Color: effortStops[1].Color, At: 0},
		{Color: effortStops[2].Color, At: 0.35},
		{Color: effortStops[3].Color, At: 0.65},
		{Color: effortStops[1].Color, At: 1},
	}
	angle := float32(0)
	if level >= 3 {
		angle = b.Loop("turn", 4*time.Second, ui.Linear) * 360
	}
	g := ui.ConicGradient{Stops: stops, Angle: angle, Oklab: true}
	layers := []struct{ width, blur, alpha float32 }{{1, 0, 0.9}, {2.5, 3, 0.6}, {5, 9, 0.45}}
	if !gpu {
		layers = layers[:1]
	}
	for _, l := range layers {
		ui.Box(c).Absolute().Top(-1).Left(-1).Right(-1).Bottom(-1).PassThrough().Radius(7).
			Border(l.width, ui.Color{}).BorderConicGradient(g).Blur(l.blur).Opacity(k * l.alpha)
	}
}

func (a *app) pickerFamilies(c *ui.Context) {
	ui.Column(c).Width(150).Padding(6).Gap(1).Children(func() {
		for _, f := range families {
			open := f.name == a.pickerFamily
			r := row(c, f.name, open).Padding(6, 8).Gap(8)
			// Pointing at a family opens its models, as a submenu does.
			if r.Hovered() {
				a.pickerFamily = f.name
			}
			if r.Children(func() {
				modelTile(c, f.icon, f.color, 22)
				ui.Column(c).Grow(1).Gap(1).Children(func() {
					ui.Text(c, f.name).FontWeight(500).TextColor(palette.text)
					ui.Text(c, f.desc).FontSize(11).TextColor(palette.muted)
				})
				ui.Icon(c, icChevronR).FontSize(11)
			}).Clicked() {
				a.pickerFamily = f.name
			}
		}
	})
}

// pickerModels shows the models of the family pointed at. Every family's
// list is built in the same cell, the others invisible, so the column is
// as tall as the longest list whichever shows: the popover keeps its size,
// and the families stay put under the pointer as it moves between them.
func (a *app) pickerModels(c *ui.Context) {
	ui.Grid(c).Columns(1).Grow(1).Children(func() {
		for _, f := range families {
			list := ui.Column(c).Key(f.name).RowStart(1).ColumnStart(1).Padding(6).Gap(1)
			if f.name != a.pickerFamily {
				list.Invisible()
			}
			list.Children(func() {
				for _, m := range models {
					if m.provider == f.name {
						a.pickerModel(c, m)
					}
				}
			})
		}
	})
}

func (a *app) pickerModel(c *ui.Context, m modelInfo) {
	selected := m.name == a.model
	if row(c, m.name, selected).Padding(7, 10).Gap(10).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(1).Children(func() {
			ui.Text(c, m.short).FontWeight(500).TextColor(palette.text).SingleLine()
			ui.Text(c, m.desc).FontSize(11.5).TextColor(palette.muted).SingleLine()
		})
		if selected {
			ui.Icon(c, icCheck).FontSize(14).TextColor(palette.accent)
		}
	}).Clicked() {
		a.model = m.name
	}
}

// effortSlider steps from Low to Max, as Codex's: a track whose gradient
// fill grows warmer as the effort goes up, twinkling at High and Max, a
// burst and a glow at the thumb at Max.
func (a *app) effortSlider(c *ui.Context) {
	level := a.effortLevel()
	ui.Column(c).Padding(12, 14, 14).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Effort").FontWeight(500)
			// The new level rises in as it changes.
			ui.Text(c, efforts[level]).Key(efforts[level]).FontWeight(600).TextColor(effortColor(level)).Transition(riseIn).Margin(0, 0, 0, 8)
			ui.Spacer(c)
			a.fastButton(c)
		})
		v := float64(level)
		steps := float32(len(efforts) - 1)
		sb := ui.SliderBase(c.Key("effort"), &v, 0, float64(steps)).Step(1).Height(28).Label("Effort").Cursor(ui.CursorPointer)
		setLevel := func(l int) {
			if l == level {
				return
			}
			haptic() // a tick on the trackpad at each step
			if l == 3 {
				a.maxAt = c.Now()
			}
			a.effort = efforts[l]
			level = l
		}
		// While the pointer holds the thumb, it stays under the pointer,
		// free between the levels; only within a small zone around a
		// level's point does it lock onto the point, choosing that level.
		// Let go, a spring carries it to the level it last locked onto,
		// from where it was, at the speed it was going: half way to the
		// next level is not enough. Keys and clicks on the track move it
		// by the same spring.
		// Held is the whole press, not only the frames the pointer moves
		// in: a pause mid-drag keeps the thumb where it is.
		var pos float32
		held := sb.Pressed()
		if w := sb.Bounds().W; held && w > 2*stopInset {
			x, _, _ := sb.PointerPosition()
			at := min(max((x-stopInset)/(w-2*stopInset)*steps, 0), steps)
			if !a.effortHeld {
				a.effortHeld, a.effortPressAt = true, at
			}
			a.effortLastAt = at
			if nearest := float32(int(at + 0.5)); abs32(at-nearest) <= snapZone {
				setLevel(int(nearest))
				at = nearest
			}
			pos = sb.Jump("pos", at)
		} else {
			if a.effortHeld {
				// A click, pressed and let go in place, picks the level
				// nearest it, wherever on the track.
				a.effortHeld = false
				if abs32(a.effortLastAt-a.effortPressAt) < 0.05 {
					setLevel(int(a.effortLastAt + 0.5))
				}
			}
			if sb.Changed() {
				setLevel(int(v + 0.5))
			}
			pos = sb.AnimateTo("pos", float32(level), snap)
		}
		maxAt := a.maxAt
		sb.Draw(func(p *ui.Painter, r ui.Rect) {
			if drawEffortTrack(p, r, pos, level, maxAt, &a.effortFlow) {
				p.AnimationFrame()
			}
		})
		ui.Row(c).Children(func() {
			ui.Text(c, "Faster").FontSize(11).TextColor(palette.faint).Grow(1)
			ui.Text(c, "Smarter").FontSize(11).TextColor(palette.faint)
		})
	})
}

const (
	trackH     = 20
	thumbR     = 12
	stopInset  = 10 // the thumb's center at the first and last stops
	burstTime  = 650 * time.Millisecond
	flashTime  = 550 * time.Millisecond
	strikeTime = 450 * time.Millisecond
	glowRadius = 26
)

// drawEffortTrack draws the track in r with the thumb pos levels along it,
// and reports whether it's still moving.
func drawEffortTrack(p *ui.Painter, r ui.Rect, pos float32, level int, maxAt time.Time, flow *flowState) bool {
	t := p.Now()
	steps := float32(len(efforts) - 1)
	x0, x1 := r.X+stopInset, r.X+r.W-stopInset
	cy := r.Y + r.H/2
	tx := x0 + (x1-x0)*pos/steps
	track := ui.Rect{X: r.X, Y: cy - trackH/2, W: r.W, H: trackH}
	p.Fill(track, palette.text.Alpha(0.08), trackH/2)

	// The fill, up to the thumb, shows the gradient laid across the whole
	// track, brighter the more effort.
	fill := ui.Rect{X: track.X, Y: track.Y, W: tx - track.X, H: trackH}
	g := ui.LinearGradient{Stops: stopsUpTo(effortStops, fill.W/r.W), Angle: 90, Oklab: true}
	p.FillGradient(fill, g, trackH/2)
	// Light flows through the fill toward the thumb, as through a fibre:
	// more of it, and faster, the more effort.
	p.Clip(fill, trackH/2, func() { flow.draw(p, track, fill, level) })
	animating := fill.W > 0

	// The stops.
	for i := range len(efforts) {
		x := x0 + (x1-x0)*float32(i)/steps
		col := palette.text.Alpha(0.25)
		if float32(i) <= pos {
			col = ui.RGBA(255, 255, 255, 0.6)
		}
		p.Fill(ui.Rect{X: x - 1.5, Y: cy - 1.5, W: 3, H: 3}, col, 1.5)
	}

	// At Max, a glow behind the thumb, a flash and a burst as it arrives.
	if level >= 3 {
		drawGlow(p, tx, cy, glowRadius, effortStops[3].Color, 0.55)
		since := t.Sub(maxAt)
		if !maxAt.IsZero() && since < burstTime {
			drawBurst(p, tx, cy, thumbR+1, thumbR+16, float32(since)/float32(burstTime), maxPixels)
			if k := float32(since) / float32(flashTime); k < 1 {
				drawGlow(p, tx, cy, glowRadius+8, ui.Hex("#fff7ed"), 0.7*(1-k)*(1-k))
			}
		}
	}

	// The thumb.
	thumb := ui.Rect{X: tx - thumbR, Y: cy - thumbR, W: 2 * thumbR, H: 2 * thumbR}
	p.Shadow(thumb, thumbR, 0, 1, 4, 0, ui.RGBA(0, 0, 0, 0.35))
	p.Fill(thumb, ui.Hex("#ffffff"), thumbR)
	p.Fill(ui.Rect{X: tx - 3, Y: cy - 3, W: 6, H: 6}, effortColor(level), 3)
	return animating || float32(level) != pos
}

// zapIcon is the lightning of fast mode: an outline while it's off,
// filled electric blue while it's on. As it comes on, lightning strikes out of
// it: forking bolts that flicker a few times, new each flicker, the icon
// flashing white.
func (a *app) zapIcon(c *ui.Context, key string, size float32) {
	icon, col := icZap, palette.muted
	if a.fast {
		icon, col = icZapFilled, electric
	}
	at := a.fastAt
	striking := a.fast && !at.IsZero() && c.Now().Sub(at) < strikeTime
	box := ui.Box(c).Key(key).Size(size, size).Children(func() {
		z := ui.Icon(c, icon)
		flash := z.Keyframes("flash", []ui.Keyframe{
			{Value: 0},
			{At: 30 * time.Millisecond, Value: 1, Ease: easeOut},
			{At: strikeTime, Value: 0, Ease: easeOut},
		}, ui.KeyframeOptions{Run: uint64(at.UnixNano())})
		if !striking {
			flash = 0
		}
		z.FontSize(size).TextColor(col.Mix(boltCore, flash))
	})
	if !striking {
		return
	}
	box.DrawOver(func(p *ui.Painter, r ui.Rect) {
		since := p.Now().Sub(at)
		if since >= strikeTime {
			return
		}
		k := float32(since) / float32(strikeTime)
		lit := strobe(k)
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		drawGlow(p, cx, cy, size*2.2, boltGlow, 0.7*lit)
		flicker := int(since / (45 * time.Millisecond))
		for i := range 4 {
			angle := (float32(i)+0.6*hash(i, flicker, 9))*math.Pi/2 + 0.4
			drawBolt(p, cx, cy, angle, size*(1.3+0.7*hash(i, flicker, 10)), 1.3, i*131+flicker*17, lit)
		}
		p.AnimationFrame()
	})
}

// electricBorder runs a charged line around the model button while fast
// mode is on, flashing brighter as the zap strikes it on. Where the
// desktop asks for less motion, or the window draws without a GPU, the
// line holds still.
func (a *app) electricBorder(c *ui.Context) {
	at := a.fastAt
	reduce := c.Preferences().ReduceMotion
	dark := a.theme.dark
	m := float32(elMargin - 1)
	ui.Box(c).Absolute().Top(-m).Left(-m).Right(-m).Bottom(-m).PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
		still := reduce || !p.Accelerated()
		since := p.Now().Sub(at)
		if at.IsZero() {
			since = p.Now().Sub(electricStart)
		}
		k := float32(1)
		if !at.IsZero() && since < strikeTime {
			k += 0.6 * strobe(float32(since)/float32(strikeTime))
		}
		drawElectricity(p, r, 7, since.Seconds(), still, k, dark)
		if !still {
			p.AnimationFrame()
		}
	})
}

// stopsUpTo returns the part of a gradient from 0 to f, laid out from 0 to
// 1: the fill shows the track's gradient only as far as the thumb, the
// same colors at every level, red only at Max.
func stopsUpTo(stops []ui.GradientStop, f float32) []ui.GradientStop {
	if f <= 0 {
		return stops[:1]
	}
	var out []ui.GradientStop
	for i, s := range stops {
		if s.At <= f {
			out = append(out, ui.GradientStop{Color: s.Color, At: s.At / f})
			continue
		}
		// The color at f, between the last stop and this one.
		prev := stops[i-1]
		k := (f - prev.At) / (s.At - prev.At)
		out = append(out, ui.GradientStop{Color: prev.Color.Mix(s.Color, k), At: 1})
		break
	}
	return out
}
