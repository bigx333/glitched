package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// The app's motion tokens: every animation takes its curve and duration
// from here. UI motion stays under 300 ms, and keyboard-driven actions
// don't animate at all.
var (
	// easeOut is for what enters or leaves.
	easeOut = ui.CubicBezier(0.23, 1, 0.32, 1)
	// easeInOut is for what moves across the screen.
	easeInOut = ui.CubicBezier(0.77, 0, 0.175, 1)
	// easeDrawer is iOS's sheet curve, for the sidebar.
	easeDrawer = ui.CubicBezier(0.32, 0.72, 0, 1)
	// ease is CSS's, for hover colors and toasts.
	ease = ui.CubicBezier(0.25, 0.1, 0.25, 1)
)

var (
	// hoverFade fades a hover's color.
	hoverFade = ui.ElementTransition{Colors: true, Duration: 150 * time.Millisecond, Ease: ease}
	// fold grows rows in and collapses them out, fading, as an accordion.
	fold = ui.ElementTransition{
		Colors:   true,
		Size:     true,
		Duration: 200 * time.Millisecond,
		Ease:     easeOut,
		Enter:    &ui.Motion{Collapse: true},
		Exit:     &ui.Motion{Collapse: true},
	}
	// riseIn brings a new message in, 8 DIPs below where it settles.
	riseIn = ui.ElementTransition{
		Colors:   true,
		Duration: 200 * time.Millisecond,
		Ease:     easeOut,
		Enter:    &ui.Motion{Y: 8},
	}
	// slideIn shows a panel sliding a little in from its edge as it
	// fades in, after the layout made room for it at once.
	slideFrom = func(x, y float32) ui.ElementTransition {
		return ui.ElementTransition{
			Colors:   true,
			Duration: 200 * time.Millisecond,
			Ease:     easeOut,
			Enter:    &ui.Motion{X: x, Y: y},
			Exit:     &ui.Motion{X: x, Y: y},
		}
	}
	// press is the feedback of a press.
	press = ui.Animation{Duration: 160 * time.Millisecond, Ease: easeOut}
	// turn rotates a chevron as its section opens.
	turn = ui.Animation{Duration: 200 * time.Millisecond, Ease: easeOut}
	// sidebarSlide opens and closes the sidebar.
	sidebarSlide = ui.Animation{Duration: 250 * time.Millisecond, Ease: easeDrawer}
)

// indicator glides a selection indicator between the options of a small
// control, as the thumb of a segmented control.
func indicator(match any) ui.ElementTransition {
	return ui.ElementTransition{Duration: 200 * time.Millisecond, Ease: easeInOut, Match: match}
}

// chevron is a disclosure chevron, turned down while open.
func chevron(c *ui.Context, open bool) ui.Element {
	ch := ui.Icon(c, icChevronR).FontSize(12)
	deg := float32(0)
	if open {
		deg = 90
	}
	return ch.Rotate(ch.AnimateTo("turn", deg, turn))
}

// pingPong eases there and back again over one period, for a pulse.
func pingPong(t float32) float32 {
	if t < 0.5 {
		return ui.EaseInOut(t * 2)
	}
	return ui.EaseInOut((1 - t) * 2)
}
