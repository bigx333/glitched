package main

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// The inbox's actions play a little joke on the thread's row: snoozed, it
// nods off and floats z's, and settled, it sighs with relief in a burst of
// confetti, both where it was, before it folds away into its new section;
// sent back, it lands in the inbox and then rewinds and says jk. They only
// paint, so nothing around the row moves.
type gagKind int

const (
	gagSnooze gagKind = iota
	gagSettle
	gagUndo
)

type rowGagFx struct {
	kind gagKind
	at   time.Time
	// from is the section the row was in: it stays there for hold, to
	// play its joke where it was seen, before it folds into its new one.
	from category
	hold time.Duration
	// The joke plays from delay after at, for life.
	delay, life time.Duration
}

// done reports whether the joke is over at now.
func (fx rowGagFx) done(now time.Time) bool { return now.Sub(fx.at) >= fx.delay+fx.life }

// The jokes' lengths. Snoozing and settling play before the row moves,
// all of it, so the row holds for as long; sending back plays after, once
// the row has folded into the inbox.
const (
	snoozeLife = 900 * time.Millisecond
	settleLife = 650 * time.Millisecond
	undoLife   = 1150 * time.Millisecond
	foldIn     = 200 * time.Millisecond // fold's duration, as the row lands
)

var confetti = []ui.Color{ui.Hex("#4ade80"), ui.Hex("#facc15"), ui.Hex("#f472b6"), ui.Hex("#60a5fa"), ui.Hex("#ffffff")}

// gag plays kind on the thread's row; from is the section it leaves.
func (a *app) gag(c *ui.Context, thread api.ID, kind gagKind, from category) {
	if c.Preferences().ReduceMotion {
		return
	}
	a.rowFx[thread] = newGag(kind, c.Now(), from)
	a.fxEdits++
	if kind == gagSettle {
		hapticBump()
	}
}

// newGag is the joke of kind, played at at on a row leaving from.
func newGag(kind gagKind, at time.Time, from category) rowGagFx {
	fx := rowGagFx{kind: kind, at: at, from: from}
	switch kind {
	case gagSnooze:
		fx.hold, fx.life = snoozeLife, snoozeLife
	case gagSettle:
		fx.hold, fx.life = settleLife, settleLife
	case gagUndo:
		fx.delay, fx.life = foldIn, undoLife
	}
	return fx
}

func run(fx rowGagFx) ui.KeyframeOptions {
	return ui.KeyframeOptions{Run: uint64(fx.at.UnixNano()), Delay: fx.delay}
}

// rowGag moves the whole row: sent back, it boings as it rewinds, in the
// inbox it landed in.
func rowGag(r ui.Element, fx rowGagFx) {
	if fx.kind != gagUndo {
		return
	}
	x := r.Keyframes("boing-x", []ui.Keyframe{
		{Value: -14},
		{At: 340 * time.Millisecond, Value: 5, Ease: easeOut},
		{At: 560 * time.Millisecond, Value: -2.5, Ease: easeInOut},
		{At: 800 * time.Millisecond, Value: 0, Ease: easeInOut},
	}, run(fx))
	r.Translate(x, 0)
}

// tileGag moves the project's tile: nodding off, squishing with relief,
// or spinning back like a rewind.
func tileGag(tile ui.Element, fx rowGagFx) {
	switch fx.kind {
	case gagSnooze:
		// Drifts off, nods, and is asleep, dimmed, as it goes.
		deg := tile.Keyframes("nod", []ui.Keyframe{
			{Value: 0},
			{At: 250 * time.Millisecond, Value: -18, Ease: easeInOut},
			{At: 420 * time.Millisecond, Value: -9, Ease: easeInOut},
			{At: 600 * time.Millisecond, Value: -22, Ease: easeInOut},
		}, run(fx))
		dim := tile.Keyframes("drowsy", []ui.Keyframe{
			{Value: 1},
			{At: 300 * time.Millisecond, Value: 0.55, Ease: easeOut},
		}, run(fx))
		tile.TransformOrigin(0.5, 1).Rotate(deg).Opacity(dim)
	case gagSettle:
		// Phew: a squash of relief, then back.
		sx := tile.Keyframes("phew-x", []ui.Keyframe{
			{Value: 1},
			{At: 120 * time.Millisecond, Value: 1.3, Ease: easeOut},
			{At: 290 * time.Millisecond, Value: 0.88, Ease: easeInOut},
			{At: 520 * time.Millisecond, Value: 1, Ease: easeOut},
		}, run(fx))
		sy := tile.Keyframes("phew-y", []ui.Keyframe{
			{Value: 1},
			{At: 120 * time.Millisecond, Value: 0.75, Ease: easeOut},
			{At: 290 * time.Millisecond, Value: 1.12, Ease: easeInOut},
			{At: 520 * time.Millisecond, Value: 1, Ease: easeOut},
		}, run(fx))
		tile.TransformOrigin(0.5, 1).Scale(sx, sy)
	case gagUndo:
		// Rewind: a turn backwards.
		deg := tile.Keyframes("rewind", []ui.Keyframe{
			{Value: 0},
			{At: 800 * time.Millisecond, Value: -360, Ease: easeOut},
		}, run(fx))
		tile.Rotate(deg)
	}
}

// floatGag floats the joke's words up from the tile: z's that drift off
// one after another, a "phew.", a "jk!".
func floatGag(c *ui.Context, fx rowGagFx) {
	switch fx.kind {
	case gagSnooze:
		for i, size := range []float32{10, 13, 16} {
			delay := time.Duration(i) * 150 * time.Millisecond
			z := ui.Text(c, "z").Key(i).FontSize(size).FontWeight(800).TextColor(tintSnooze).NoWrap().
				Absolute().Left(18).Top(-4).PassThrough()
			opts := run(fx)
			opts.Delay += delay
			k := z.Keyframes("rise", []ui.Keyframe{
				{Value: 0},
				{At: 600 * time.Millisecond, Value: 1, Ease: easeOut},
			}, opts)
			sway := 2.5 * float32(math.Sin(float64(k)*math.Pi*2))
			// Drifting off to the right over the row, not up into what's
			// above it.
			z.Translate(26*k+7*float32(i), -12*k-3*float32(i)+sway).
				Opacity(riseFade(k))
		}
	case gagSettle:
		floatWord(c, fx, "phew.", tintSettle, true, settleLife)
	case gagUndo:
		floatWord(c, fx, "jk!", tintBack, false, undoLife)
	}
}

func floatWord(c *ui.Context, fx rowGagFx, word string, col ui.Color, italic bool, life time.Duration) {
	w := ui.Text(c, word).Key(word).FontSize(11).FontWeight(700).TextColor(col).NoWrap().
		Absolute().Left(22).Top(-8).PassThrough()
	if italic {
		w.Italic()
	}
	k := w.Keyframes("float", []ui.Keyframe{
		{Value: 0},
		{At: life, Value: 1, Ease: easeOut},
	}, run(fx))
	pop := w.Keyframes("pop", []ui.Keyframe{
		{Value: 0.6},
		{At: 200 * time.Millisecond, Value: 1.15, Ease: easeOut},
		{At: 380 * time.Millisecond, Value: 1, Ease: easeInOut},
	}, run(fx))
	w.TransformOrigin(0, 1).Scale(pop, pop).Translate(0, -14*k).Opacity(riseFade(k))
}

// burstGag pops confetti out of the tile, settled.
func burstGag(wrap ui.Element, fx rowGagFx) {
	at := fx.at
	wrap.DrawOver(func(p *ui.Painter, r ui.Rect) {
		k := float32(p.Now().Sub(at)) / float32(settleLife)
		if k >= 1 {
			return
		}
		drawBurst(p, r.X+r.W/2, r.Y+r.H/2, 8, 26, k, confetti)
		p.AnimationFrame()
	})
}

// riseFade is the opacity of words floating up, k of the way: in at once,
// held, then out as they near the top.
func riseFade(k float32) float32 {
	return smoothstep(0, 0.12, k) * (1 - smoothstep(0.6, 1, k))
}
