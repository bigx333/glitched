package main

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
)

// The app's overlays and covers. An overlay is a dialog, popover or panel
// over the window. Each is built here and notes when it shows, as the cards'
// keys, which reach past the focus (OverlayShortcut), wait under any of
// them, as MyGo's window shortcuts wait behind a dialog. A frame's keys go
// where the pass before registered them, so the cards read what that pass
// showed.
//
// A cover is what the app draws over the timeline: the composer, Jump to
// latest, a turn's card, the overlays, tooltips and toasts. Each binds a
// handle as it builds, which reads where the frame on screen drew it before
// this pass builds anything (lookOver), so that a waiting card reads its
// lines and what covers them from the one frame (approval.go). An overlay
// that names no cover, an alert, and the suggestions' flyout cover all of
// the timeline, as the app does not know where they are.
//
// TestEveryOverlayIsTheApps keeps every overlay, tooltip and toast here.

// coverKey is a cover's: a kind, and for one of many, its anchor's identity.
type coverKey struct {
	kind string
	of   uint64
}

// cover binds e to the cover key names.
func (a *app) cover(key coverKey, e ui.Element) ui.Element {
	h := a.covers[key]
	if h == nil {
		h = new(ui.Handle)
		a.covers[key] = h
	}
	a.coverN++
	return e.Bind(h)
}

// lookOver reads where the frame on screen drew each cover, and the
// composer, before this pass builds either.
func (a *app) lookOver(c *ui.Context) {
	a.over = a.over[:0]
	for _, h := range a.covers {
		if r := h.Bounds(c); r.W > 0 && r.H > 0 {
			a.over = append(a.over, r)
		}
	}
	slices.SortFunc(a.over, func(p, q ui.Rect) int {
		return cmp.Or(cmp.Compare(p.Y, q.Y), cmp.Compare(p.X, q.X), cmp.Compare(p.H, q.H), cmp.Compare(p.W, q.W))
	})
	a.composerAt = a.composerH.Bounds(c)
}

// hidden is whether a cover the frame on screen drew lies over r.
func (a *app) hidden(r ui.Rect) bool {
	return slices.ContainsFunc(a.over, func(v ui.Rect) bool { return meets(r, v) })
}

// coversOf are the covers the frame on screen drew over r, cut to it.
func (a *app) coversOf(r ui.Rect) []ui.Rect {
	var out []ui.Rect
	for _, v := range a.over {
		if meets(r, v) {
			out = append(out, meet(r, v))
		}
	}
	return out
}

// meets is whether r and v overlap by more than the slack within allows.
func meets(r, v ui.Rect) bool {
	m := meet(r, v)
	return m.W > 0.5 && m.H > 0.5
}

func (a *app) popover(c *ui.Context, anchor ui.Element, open *bool, fn func(panel ui.Element)) {
	if *open {
		focusBefore(c, fmt.Sprint("popover ", anchor.ID()))
	}
	ui.PopoverBase(c, anchor, open, func(panel ui.Element) {
		a.cover(coverKey{"popover", anchor.ID()}, panel)
		fn(panel)
	})
	a.covering = a.covering || *open
}

func (a *app) dialog(c *ui.Context, open *bool, fn func(backdrop, panel ui.Element)) {
	if *open {
		focusBefore(c, fmt.Sprintf("dialog %p", open))
	}
	ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		a.cover(coverKey{"dialog", backdrop.ID()}, backdrop)
		fn(backdrop, panel)
	})
	a.covering = a.covering || *open
	a.modalNow = a.modalNow || *open && open != &a.cmdkOpen
}

func (a *app) alert(c *ui.Context, open *bool, title, message string, buttons ...string) int {
	if *open {
		focusBefore(c, fmt.Sprintf("alert %p", open))
	}
	chose := ui.AlertDialog(c, open, title, message, buttons...)
	a.covering = a.covering || *open
	a.modalNow = a.modalNow || *open
	a.blinding = a.blinding || *open
	return chose
}

// focusBefore keeps, while the overlay key names shows, the element that
// had the focus before it opened, for the focus to go back to as it
// closes. MyGo gives the focus back to the element that had it as an
// overlay opened, but notes which that was once it has built what's in
// it, by when a field there that takes the focus as it shows (AutoFocus),
// as the palette's search and an alert's default button do, has it, and
// the focus goes nowhere. An empty overlay of its own, opened first,
// notes the element before anything takes it, and closes with the one it
// stands for, as it's built only while that is open.
func focusBefore(c *ui.Context, key string) {
	ui.Overlay(c, func() { ui.Box(c.Key("focus before " + key)).Absolute().PassThrough() })
}

// overlay builds fn over the window, which covers the app while open, and
// the timeline where fn's cover is; one that names none covers all of it.
func (a *app) overlay(c *ui.Context, open bool, fn func()) {
	n := a.coverN
	ui.Overlay(c, fn)
	a.covering = a.covering || open
	a.blinding = a.blinding || a.coverN == n
}

// tip shows s by e as Element.Tooltip does, in the theme's look, and
// covers what it lies over.
func (a *app) tip(c *ui.Context, e ui.Element, s string) ui.Element {
	if s == "" {
		return e
	}
	e.Description(s)
	t := c.Theme()
	fill, text := t.Inverse, t.InverseText
	if fill == ui.Transparent {
		fill = t.Text
	}
	if text == ui.Transparent {
		text = t.Background
	}
	ui.TooltipBase(c, e, func(tip ui.Element) {
		tip.MaxWidth(t.Space(80)).Padding(t.Space(1.25), t.Space(2)).Radius(t.Space(1.25)).
			Background(fill).TextColor(text).FontSize(t.FontSize - 1)
		tip.Shadow(0, 2, 8, 0, ui.RGBA(0, 0, 0, 0.2))
		tip.Children(func() { ui.Text(c, s) })
		tip.AttachTo(e, ui.AnchorTop, ui.AnchorBottom).Margin(0, 0, t.Space(1.5), 0)
		a.cover(coverKey{"tip", e.ID()}, tip)
	})
	return e
}

// toast builds the i-th toast shown, which covers what it lies over.
func (a *app) toast(c *ui.Context, i int, t ui.Toast) ui.ToastParts {
	parts := ui.ToastBase(c, t)
	a.cover(coverKey{"toast", uint64(i)}, parts.Root)
	return parts
}
