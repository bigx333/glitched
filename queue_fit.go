package main

import (
	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
)

// queueFit chooses whether the queue's Send now is drawn with its label
// or as its icon: with its label only while every message being shown
// fits beside it, whatever draws the rows. It decides before the frame is
// laid out, from this frame's room for the queue and what the last layout
// says each row takes besides its message, so the first frame a row is
// drawn in is drawn as it stays.
type queueFit struct {
	// compact is whether this frame draws Send now as its icon.
	compact bool
	// icon and label are the widths of a row action drawn as its icon
	// and of Send now with its label, as last laid out.
	icon, label float32
	rows        map[api.ID]*queueRowFit
}

// queueRowFit is what a row of the queue was last laid out as: its box,
// its message's slot and its Send nows, n of them.
type queueRowFit struct {
	box, slot ui.Handle
	steers    []ui.Handle
	n         int
}

// row is message's row, kept while the queue shows it.
func (f *queueFit) row(message api.ID) *queueRowFit {
	if f.rows == nil {
		f.rows = map[api.ID]*queueRowFit{}
	}
	r := f.rows[message]
	if r == nil {
		r = &queueRowFit{}
		f.rows[message] = r
	}
	return r
}

// steer is the handle of the next Send now drawn in the row.
func (r *queueRowFit) steer() *ui.Handle {
	if r.n == len(r.steers) {
		r.steers = append(r.steers, ui.Handle{})
	}
	r.n++
	return &r.steers[r.n-1]
}

// choose decides compact for this frame's q, whose states take stateW. A
// row shows its message whole if its room, this frame's, holds the
// message and what the row last took besides it, its Send nows each as
// wide as with its label. A row not laid out yet takes what the built-in
// Queue's row does; one being edited shows no message, and no Send now.
func (f *queueFit) choose(c *ui.Context, a *app, q []*client.UserEntry, stateW float32) {
	if f.icon == 0 {
		f.icon = 2*6 + 11
	}
	if f.label == 0 {
		w, _ := c.MeasureText(0, ui.Span{Text: "Send now", Size: 11})
		f.label = f.icon + 4 + w
	}
	room := a.queueWidth() - 2*(queueBorder+queuePad)
	compact, guessed := false, false
	kept := map[api.ID]*queueRowFit{}
	for _, u := range q {
		r := f.row(u.Message)
		kept[u.Message] = r
		n := r.n
		r.n = 0
		if u.Message == a.queueEditing.message {
			continue
		}
		natural, _ := c.MeasureText(0, ui.Span{Text: oneLine(u.Text), Size: 12})
		rest := queueRowPadL + queueRowPadR + stateW + queueTextGap + 5*queueActionGap + f.label + 4*f.icon
		if box := r.box.Bounds(c); box.W > 0 {
			rest = box.W - r.slot.Bounds(c).W
			for _, s := range r.steers[:n] {
				rest += f.label - s.Bounds(c).W
			}
		} else {
			guessed = true
		}
		if natural+rest > room {
			compact = true
		}
	}
	f.rows = kept
	f.compact = compact
	if guessed {
		// Laid out, the rows say what they take: the next frame checks.
		c.Invalidate()
	}
}

// queueWidth is the queue's width in this frame: the composer's, in the
// room the chat has in this frame, as composer lays it out.
func (a *app) queueWidth() float32 {
	frame := min(a.contentInner(a.chatRoom())-a.composerLeft()-columnRight, a.composerMaxW())
	return frame - a.composerAround.w
}

// sawSteer keeps a Send now's width as last laid out, w, as its label's
// width if it's wider than an icon.
func (f *queueFit) sawSteer(w float32) {
	if w > f.icon+1 {
		f.label = w
	}
}
