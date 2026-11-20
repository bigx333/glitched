package main

import (
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/plugins"
)

// A new thread opens as a hero: its composer large in the middle of the
// pane, on the chat's own background, under a "What should we build?"
// with no transcript yet. The first message docks it: the composer glides
// down to its place at the bottom and settles to its usual size, the
// transcript fades in as it arrives, and the thread's row slides into the
// sidebar. Opening a new thread from an established one by the pointer is
// the same way back (⌘N jumps, as keyboard actions don't animate): the
// thread left fades and slides out as the composer lifts into the middle,
// growing, and the heading comes in as it gets there.
//
// The pane's one value, dockK, is how far the composer is from the hero
// (0) to its dock (1): any thread but the draft is docked. It moves on the
// composer morph's spring, which a new target takes over from where it is,
// keeping its velocity; everything else is staged on it, so turning round
// mid-way turns everything round. With less motion asked for, it's at once
// (AnimateTo).

// heroCardH is the hero composer card's least height: a few lines more
// than the docked card's two.
const heroCardH = 176

// arrive slides a thread's row into the sidebar as it leaves the hero,
// growing it from nothing on the composer's spring, so the rows under it
// make room as it comes.
var arrive = ui.ElementTransition{
	Colors:    true,
	Size:      true,
	Animation: &composerOpen,
	Enter:     &ui.Motion{X: -24, Collapse: true},
	Exit:      &ui.Motion{Collapse: true},
}

// arriveFor is how long a row just sent from the hero keeps arrive, past
// its slide in; then it folds between sections as any other.
const arriveFor = time.Second

// stage is k's progress through [from, to], eased at both ends: the
// staging of what comes and goes with the dock.
func stage(k, from, to float32) float32 {
	x := clamp01((k - from) / (to - from))
	return x * x * (3 - 2*x)
}

// Stages of dockK: the hero's heading goes as the composer starts down,
// and comes in only as it arrives back up; the transcript comes in over
// the last of the glide, as the spring is 75% of the way there a quarter
// of the way through its time; going up, it goes first.
func heroHeadingK(d float32) float32 { return 1 - stage(d, 0, 0.3) }
func transcriptK(d float32) float32  { return stage(d, 0.6, 1) }

// hero is whether t is shown as the hero: the draft of a new thread.
func hero(t thread) bool { return t.id().IsZero() }

// dockMorph moves the composer between the hero and its dock, on host, for
// the frame. The keyboard's ⌘N jumps, as keyboard actions don't animate.
func (a *app) dockMorph(host ui.Element, t thread) {
	target := float32(1)
	if hero(t) {
		target = 0
	}
	if a.dockJump {
		a.dockK = host.Jump("dock", target)
		return
	}
	// Held to [0, 1]: turned round near an end, the spring carries on past
	// it for a moment.
	a.dockK = clamp01(host.AnimateTo("dock", target, composerOpen))
}

// heroLift is how far above its dock the hero composer's bar sits: the
// card in the middle of the pane, as the last frame laid the pane out.
func (a *app) heroLift() float32 {
	return max(0, a.paneAt.H/2-composerBarBottom-heroCardH/2)
}

// departing is the thread whose transcript the pane shows over the hero's
// while the composer lifts into it, fading and sliding out: the one left
// for it. It's none once there, or for anything but the hero.
func (a *app) departing(t thread) (thread, bool) {
	if !hero(t) || a.dockK == 0 || a.leaving.IsZero() {
		return thread{}, false
	}
	from, ok := a.find(a.leaving)
	return from, ok
}

// heroHeading is the hero's greeting over its composer, gone by a third of
// the way down.
func (a *app) heroHeading(c *ui.Context, d float32) {
	o := heroHeadingK(d)
	if o <= 0 {
		return
	}
	// What it says is a plugin's: the built-in Hero heading, or one of the
	// user's that replaces it. How it fades is the hero's.
	box := func() ui.Element {
		return ui.Column(c).Key("hero-heading").AlignItems(ui.Center).Padding(0, 0, 28).PassThrough().Opacity(o).Translate(0, -12*(1-o))
	}
	a.pluginComponent(c, "hero.heading", nil, componentCopy{key: "hero", box: box, context: func(plugins.ThreadRecord) any { return struct{}{} }})
}

// leaveFor notes the thread shown as the one left for a new thread's hero,
// to fade out behind the composer as it lifts; another hero, left before
// it got there, leaves that one fading.
func (a *app) leaveFor() {
	if t := a.current(); !t.id().IsZero() {
		a.leaving = t.id()
	}
}

// arriving is whether t's row is sliding into the sidebar, just sent from
// the hero, or about to.
func (a *app) arriving(t thread) bool {
	return !a.arrived.IsZero() && t.id() == a.arrived && (a.arrivedAt.IsZero() || a.now.Sub(a.arrivedAt) < arriveFor)
}

// sentFromHero lets the thread id, just created from the hero, slide into
// the sidebar. Its slide starts with the next frame built (view), not at
// now: sent with Enter, between frames, now is the last frame's, which
// may be long past.
func (a *app) sentFromHero(id api.ID) {
	a.dockJump = false
	a.arrived, a.arrivedAt = id, time.Time{}
}
