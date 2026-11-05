package main

import (
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

// The levels of autonomy the agent runs at.
const (
	accessAsk  = "Ask for approval"
	accessAuto = "Approve for me"
	accessFull = "Full access"
)

var accessLevels = []struct {
	name, desc string
	icon       *ui.SVG
}{
	{accessAsk, "Always ask to edit external files and use the internet", icHand},
	{accessAuto, "Approve file edits in the workspace, and ask before commands and the internet", icShieldTerminal},
	{accessFull, "Unrestricted access to the internet and any file on your computer", icShieldAlert},
}

// narrowComposer is the chat's width under which the composer's access
// button shows only its icon, for the send button to keep its room.
const narrowComposer = 600

var (
	danger     = ui.Hex("#f97316")
	dangerDeep = ui.Hex("#dc2626")
)

const (
	// holdTime is how long Full access is held to be granted: deliberate,
	// with the system's answer snappy as the press lets go early.
	holdTime    = 700 * time.Millisecond
	releaseTime = 200 * time.Millisecond
	// grantTime is how long the composer shakes and flashes once it's
	// granted.
	grantTime = 500 * time.Millisecond
)

// shake is how far the composer jumps in the frames after Full access is
// granted, a 60th of a second apart, after MyGo's effort-slider example,
// three times as far.
var shake = [][2]float32{
	{0.75, 1}, {-0.78, 0.3}, {0.9, -1.03}, {0.4, 0.37}, {0.25, 0}, {0.15, -0.2},
	{0.15, 0.32}, {-0.5, -0.22}, {-0.22, 0}, {0.15, -0.25}, {-0.3, 0.35},
	{-0.22, -0.15}, {-0.15, -0.08}, {-0.05, -0.02}, {0, 0},
}

func shakeFrames(axis int) []ui.Keyframe {
	frames := make([]ui.Keyframe, len(shake))
	for i, s := range shake {
		frames[i] = ui.Keyframe{At: time.Duration(i) * time.Second / 60, Value: 3 * s[axis]}
	}
	frames[0].Value = 0
	return frames
}

var (
	calmBlue  = ui.Hex("#60a5fa")
	scanTeal  = ui.Hex("#2dd4bf")
	fxTime    = 600 * time.Millisecond
	sweepTime = 650 * time.Millisecond
)

// accessEffects plays on the composer card as the level changes, each as
// much as it risks: Ask for approval glows a calm blue once along its
// edge; Approve for me sweeps a teal scan across it; Full access shakes it
// and flashes its edge red.
func (a *app) accessEffects(card ui.Element) {
	if a.accessAt.IsZero() {
		return
	}
	run := ui.KeyframeOptions{Run: uint64(a.accessAt.UnixNano())}
	pulse := func(key string, d time.Duration) float32 {
		return card.Keyframes(key, []ui.Keyframe{
			{Value: 0},
			{At: 40 * time.Millisecond, Value: 1, Ease: easeOut},
			{At: d, Value: 0, Ease: easeOut},
		}, run)
	}
	switch a.access {
	case accessFull:
		card.Translate(card.Keyframes("shake-x", shakeFrames(0), run), card.Keyframes("shake-y", shakeFrames(1), run))
		if k := pulse("flash", grantTime); k > 0.01 {
			card.InsetShadow(0, 0, 18, 0, dangerDeep.Alpha(0.55*k)).Shadow(0, 0, 24, 2, dangerDeep.Alpha(0.45*k))
		}
	case accessAsk:
		// A calm breath of blue: in, held a moment, out.
		k := card.Keyframes("calm", []ui.Keyframe{
			{Value: 0},
			{At: 120 * time.Millisecond, Value: 1, Ease: easeOut},
			{At: 280 * time.Millisecond, Value: 1},
			{At: fxTime + 150*time.Millisecond, Value: 0, Ease: easeInOut},
		}, run)
		if k > 0.01 {
			// The rim is a shadow, not a border: a border takes room inside
			// the card, which would shift its content as it comes and goes.
			card.InsetShadow(0, 0, 16, 0, calmBlue.Alpha(0.45*k)).Shadow(0, 0, 22, 2, calmBlue.Alpha(0.45*k)).
				Shadow(0, 0, 0, 1, calmBlue.Alpha(0.6*k))
		}
	case accessAuto:
		// A band of light crosses the card, left to right, as a scan.
		at := a.accessAt
		card.DrawOver(func(p *ui.Painter, r ui.Rect) {
			k := float32(p.Now().Sub(at)) / float32(sweepTime)
			if k >= 1 {
				return
			}
			x := r.X - 80 + (r.W+160)*easeInOut(k)
			band := ui.Rect{X: x - 60, Y: r.Y, W: 120, H: r.H}
			p.FillGradient(band, ui.LinearGradient{Stops: []ui.GradientStop{
				{Color: scanTeal.Alpha(0), At: 0},
				{Color: scanTeal.Alpha(0.16), At: 0.8},
				{Color: scanTeal.Alpha(0.5), At: 0.97},
				{Color: scanTeal.Alpha(0), At: 1},
			}, Angle: 90}, 0)
			p.AnimationFrame()
		})
		if k := pulse("scan-edge", sweepTime); k > 0.01 {
			card.Shadow(0, 0, 16, 1, scanTeal.Alpha(0.22*k))
		}
	}
}

// accessIcon is the level's icon on the button, moving as the level
// comes on: the hand waves stop, the shield pops, the alert shudders.
func (a *app) accessIcon(c *ui.Context, icon *ui.SVG) {
	ic := ui.Icon(c, icon).FontSize(13)
	if a.accessAt.IsZero() {
		return
	}
	run := ui.KeyframeOptions{Run: uint64(a.accessAt.UnixNano())}
	switch a.access {
	case accessAsk:
		deg := ic.Keyframes("wave", []ui.Keyframe{
			{Value: 0},
			{At: 90 * time.Millisecond, Value: -16, Ease: easeOut},
			{At: 200 * time.Millisecond, Value: 12, Ease: easeInOut},
			{At: 310 * time.Millisecond, Value: -8, Ease: easeInOut},
			{At: 420 * time.Millisecond, Value: 0, Ease: easeOut},
		}, run)
		ic.TransformOrigin(0.5, 1).Rotate(deg)
	case accessAuto:
		s := ic.Keyframes("pop", []ui.Keyframe{
			{Value: 1},
			{At: 110 * time.Millisecond, Value: 1.22, Ease: easeOut},
			{At: 300 * time.Millisecond, Value: 1, Ease: easeInOut},
		}, run)
		ic.Scale(s, s)
	case accessFull:
		x := ic.Keyframes("shudder", shakeFrames(0), run)
		ic.Translate(x, 0)
	}
}

// accessPicker is the composer's autonomy button and the popover it
// opens. Full access is held to be granted, as hazard tape fills the row,
// and it keeps the button striped while it's on.
func (a *app) accessPicker(c *ui.Context) {
	full := a.access == accessFull
	icon := icShieldTerminal
	for _, l := range accessLevels {
		if l.name == a.access {
			icon = l.icon
		}
	}
	b := ghost(c, "access", a.accessOpen).Label("Access").Tooltip("How freely the agent acts")
	if full {
		b.TextColor(danger)
		// Hazard tape, and a slow warning pulse: it's on.
		pulse := b.Loop("warn", 1600*time.Millisecond, pingPong)
		b.Background(dangerDeep.Alpha(0.1+0.08*pulse)).Stripes(danger.Alpha(0.16), 5, 5, 45).
			Shadow(0, 0, 0, 1, danger.Alpha(0.35+0.25*pulse)) // a rim that takes no room
	}
	if b.Children(func() {
		a.accessIcon(c, icon)
		// A narrow chat, beside a panel, keeps it to its icon.
		if a.chatWidth == 0 || a.chatWidth >= narrowComposer {
			ui.Text(c, a.access).FontSize(12).SingleLine()
		}
		ui.Icon(c, icChevronD).FontSize(11)
	}).Clicked() {
		a.accessOpen = !a.accessOpen
	}
	a.popover(c, b, &a.accessOpen, func(panel ui.Element) {
		panel.AttachTo(b, ui.AnchorTopStart, ui.AnchorBottomStart).Margin(0, 0, 10, -4).Width(420).Radius(16).Clip()
		a.pane(panel, glass.Glass{}).Shadow(0, 16, 40, 0, ui.RGBA(0, 0, 0, 0.35)).Children(func() {
			ui.Column(c).Padding(6).Gap(1).Children(func() {
				for _, l := range accessLevels {
					if l.name == accessFull {
						a.fullAccessRow(c, l.desc, l.icon)
						continue
					}
					if a.accessRow(c, l.name, l.desc, l.icon, palette.text, palette.muted).Clicked() {
						if a.access != l.name {
							a.accessAt = c.Now()
						}
						a.access, a.accessOpen = l.name, false
					}
				}
			})
		})
	})
}

func (a *app) accessRow(c *ui.Context, name, desc string, icon *ui.SVG, title, sub ui.Color) ui.Element {
	selected := a.access == name
	return row(c, name, false).Padding(9, 10).Gap(12).Children(func() {
		ui.Icon(c, icon).FontSize(17).TextColor(title)
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			ui.Text(c, name).FontSize(13.5).FontWeight(500).TextColor(title)
			ui.Text(c, desc).FontSize(12).TextColor(sub)
		})
		if selected {
			ui.Icon(c, icCheck).FontSize(15).TextColor(title)
		}
	})
}

// fullAccessRow grants Full access only when held: hazard tape fills the
// row from the left over holdTime, linearly, and drains back quickly if
// it's let go early. Once full, the composer shakes, its edge flashes red
// and the trackpad bumps. Choosing it while it's on does nothing more.
func (a *app) fullAccessRow(c *ui.Context, desc string, icon *ui.SVG) {
	on := a.access == accessFull
	r := ui.ButtonBase(c.Key("full")).Gap(12).Padding(9, 10).Radius(7).AlignItems(ui.Center).Justify(ui.Start).
		Cursor(ui.CursorPointer).Label(accessFull).Background(ui.Transparent).Transition(hoverFade)
	holding := r.Pressed() && !on
	target, anim := float32(0), ui.Animation{Duration: releaseTime, Ease: easeOut}
	if holding {
		target, anim = 1, ui.Animation{Duration: holdTime, Ease: ui.Linear}
	}
	fill := r.AnimateTo("hold", target, anim)
	if holding && fill >= 0.999 {
		a.access, a.accessOpen, a.accessAt = accessFull, false, c.Now()
		hapticBump()
	}
	if r.Hovered() {
		r.Background(dangerDeep.Alpha(0.12))
	}
	r.Children(func() {
		// The hazard tape, filling as it's held.
		if fill > 0.001 {
			ui.Box(c).Absolute().Top(0).Left(0).Bottom(0).WidthPercent(fill*100).Radius(7).PassThrough().
				LinearGradient(ui.LinearGradient{From: dangerDeep.Alpha(0.45), To: danger.Alpha(0.6), Angle: 90}).
				Stripes(ui.RGBA(0, 0, 0, 0.28), 6, 6, 45)
		}
		// It shudders a little more the longer it's held.
		ic := ui.Icon(c, icon).FontSize(17).TextColor(danger)
		if holding {
			j := fill * 1.2
			ic.Translate(j*(hash(int(fill*60), 1, 1)-0.5)*2, j*(hash(int(fill*60), 2, 1)-0.5)*2)
		}
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			title := accessFull
			if holding {
				title = "Hold to grant full access…"
			}
			ui.Text(c, title).FontSize(13.5).FontWeight(600).TextColor(danger)
			ui.Text(c, desc).FontSize(12).TextColor(danger.Alpha(0.85))
		})
		if on {
			ui.Icon(c, icCheck).FontSize(15).TextColor(danger)
		}
	})
}
