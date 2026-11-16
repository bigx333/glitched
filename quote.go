package main

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// Text selected in an assistant's reply brings up a bar by the selection,
// as T3 Code's does: Quote puts the selection on the composer as a quote,
// which goes with the next message as a blockquote; Explain does that and
// asks for an explanation in the draft; a question typed in its box goes at
// once, with the quote and nothing of the draft. It waits for the
// selection to rest, as there is no telling the end of a drag otherwise,
// and keeps what was selected while it's in use or holds a question, as a
// press in it may take the selection away, and so does the chat scrolled
// far enough that the reply isn't built.

// quoteWait is how long a selection rests before its bar shows.
const quoteWait = 300 * time.Millisecond

// explainAsk is what Explain asks.
const explainAsk = "Explain this."

// quoteOf is a reply's: its thread, and its entry's key.
type quoteOf struct {
	thread api.ID
	key    string
}

// quoteState is where the bar of a selection is.
type quoteState int

const (
	// quoteWaiting is a selection made or changed, its bar waiting for it
	// to rest.
	quoteWaiting quoteState = iota
	// quoteOpen is the bar open, showing where it finds a place.
	quoteOpen
	// quoteDismissed is the bar closed on its selection, which it doesn't
	// come back for until another is made.
	quoteDismissed
)

// quoteBar is the bar of the selection in a reply: none while text is "".
type quoteBar struct {
	of    quoteOf
	text  string // what was selected
	state quoteState
	at    time.Time // when the selection was made or last changed
	// from and to are where the pointer was, relative to the reply's
	// text, as the selection was first seen and as it last changed: where
	// it began and where it ended.
	from, to ui.Point
	// inUse is the bar in use in the last frame: pointed at or holding the
	// focus.
	inUse bool
	ask   string
	// spot is where the bar last showed.
	spot quoteSpot
	// reply is where the selection's reply is, as its row last noted it,
	// and seen whether the row built since the bar last did.
	reply replyAt
	seen  bool
}

// replyAt is where the last frame drew a reply: its text at md, in its
// column at col, which the pass that built the row built as el.
type replyAt struct {
	md, col ui.Rect
	el      ui.Element
}

// codeChunk is how many lines of a code block MyGo lays out as one text,
// a part of a selection each (ui/markdown's chunkLines).
const codeChunk = 32

// selectedText is the text of a selection's parts as ⌘C copies it, a
// paragraph each, but for the chunks of a code block, which go on from
// line to line; a list's items and a table's cells are paragraphs too, as
// MyGo tells no part's block.
func selectedText(parts []ui.SelectedText) string {
	var b strings.Builder
	var prev ui.SelectedText
	for _, p := range parts {
		r := []rune(p.Text)
		from, to := min(max(p.Start, 0), len(r)), min(max(p.End, 0), len(r))
		if from >= to {
			continue
		}
		switch {
		case b.Len() == 0:
		case prev.End >= utf8.RuneCountInString(prev.Text) && strings.Count(prev.Text, "\n") == codeChunk-1 && from == 0:
			// A whole chunk selected to its end, and the next from its start.
			b.WriteString("\n")
		default:
			b.WriteString("\n\n")
		}
		b.WriteString(string(r[from:to]))
		prev = p
	}
	return strings.TrimSpace(b.String())
}

// blockquote is text quoted in Markdown.
func blockquote(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, l := range lines {
		if l = strings.TrimRight(l, " \t\r"); l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

// quoteAttachment is a quote on the composer.
func quoteAttachment(text string) attachment {
	detail := strings.Join(strings.Fields(text), " ")
	if r := []rune(detail); len(r) > 60 {
		detail = string(r[:60]) + "…"
	}
	return attachment{name: "Quote", detail: detail, text: text, quote: true}
}

// followSelection follows the selection in the reply of t keyed key,
// whose column is col and text md, and notes where the reply is, for its
// bar (selectionBar). It builds nothing of the bar, as the chat's list may
// build the row as it lays out, after the bar: a frame shows a change in
// whether the bar shows.
func (a *app) followSelection(c *ui.Context, t thread, key string, col, md ui.Element) {
	of := quoteOf{t.id(), key}
	s := &a.quote
	open := s.state == quoteOpen
	switch text := selectedText(md.SelectedTexts()); {
	case text == "":
		if s.of == of && !s.inUse && s.ask == "" {
			*s = quoteBar{}
		}
	case s.of != of || s.text != text:
		// Made or changed: wait for it to rest. Changed as it's made, it
		// began where it did.
		x, y, _ := md.PointerPosition()
		p := ui.Point{X: x, Y: y}
		from := p
		if s.of == of && s.text != "" && s.state == quoteWaiting {
			from = s.from
		}
		*s = quoteBar{of: of, text: text, at: a.now, from: from, to: p}
		c.After(quoteWait)
	case s.state == quoteWaiting:
		if rest := a.now.Sub(s.at); rest < quoteWait {
			c.After(quoteWait - rest)
			break
		}
		s.state = quoteOpen
	}
	if (s.state == quoteOpen) != open {
		c.Invalidate()
	}
	if s.of == of && s.state == quoteOpen {
		s.reply, s.seen = replyAt{md: md.Bounds(), col: col.Bounds(), el: col}, true
		followUp(c, col)
	}
}

// selectionBar builds the bar of the selection in t's timeline once it
// rests, by its reply as the reply's row last noted it (followSelection).
// It's the one place that builds the bar, after the timeline, whose list
// builds the row in the view, as it lays out after the view, or not at
// all while the chat is scrolled far from it, as the bar in use stays.
func (a *app) selectionBar(c *ui.Context, t thread) {
	s := &a.quote
	r := s.reply
	if !s.seen {
		r = replyAt{} // not built since: held at the edge it went past
	}
	s.seen = false
	if s.state != quoteOpen || s.of.thread != t.id() {
		return
	}
	spot, ok := a.placeQuote(c, s, r.md, r.col)
	if !ok {
		s.inUse = false
		return
	}
	// By the selection, the bar hangs from a point in the reply's column,
	// which follows it as the chat scrolls in this frame, when this pass
	// built the column; else, or pinned, from one in the window
	// (quoteBarAt).
	var at ui.Element
	if !spot.pinned && r.el.Valid() {
		r.el.Children(func() {
			at = ui.Box(c).Key("quote-at").Absolute().Left(spot.x-r.col.X).Top(spot.y-r.col.Y).Size(1, 1).PassThrough()
		})
	}
	a.quoteBarAt(c, t, at, spot)
}

// quoteSpot is where the bar of a selection goes in the window: its left
// edge at x, and its top at y, or its bottom when above. pinned is a bar
// held at an edge of the timeline, which stays as the chat scrolls,
// rather than by the selection, which it follows.
type quoteSpot struct {
	x, y          float32
	above, pinned bool
}

// quoteGap is between the bar and what it keeps clear of, quoteLead how
// far it starts before the point of the selection it's by, and quoteBarW
// and quoteBarH its width and about its height.
const quoteGap, quoteLead, quoteBarW, quoteBarH = 4, 12, 360, 44

// placeQuote is where the bar of selection s goes, as the last frame drew
// its reply's text at md in the column col, none for a reply not built,
// and whether it shows: under the selection, or above it where under would
// cross the composer or lie over a waiting card, whose keys it would hold,
// and in the column. It shows in the timeline, between the pane's top and
// the composer, held at its edge where neither fits, while any of the
// selection is in it; once none is, only in use, held at the edge the
// selection went past.
func (a *app) placeQuote(c *ui.Context, s *quoteBar, md, col ui.Rect) (quoteSpot, bool) {
	// The selection runs from the line of the point higher up to that of
	// the one lower down, a line of the reply's around each.
	line := c.Theme().FontSize * 1.5
	hi, lo := s.from, s.to
	if hi.Y > lo.Y {
		hi, lo = lo, hi
	}
	top := md.Y + min(max(hi.Y, 0), md.H) - line
	bottom := md.Y + min(max(lo.Y, 0), md.H) + line
	by := func(p ui.Point) float32 {
		x := md.X + min(max(p.X, 0), md.W) - quoteLead
		return max(min(x, col.X+col.W-quoteBarW), col.X)
	}
	under := quoteSpot{x: by(lo), y: bottom + quoteGap}
	if a.paneAt.H <= 0 {
		return under, md.H > 0 // no timeline laid out to keep it in
	}
	bandTop, bandBottom := a.paneAt.Y, a.paneAt.Y+a.paneAt.H
	if a.composerAt.H > 0 {
		bandBottom = a.composerAt.Y
	}
	switch {
	case md.H <= 0 && !s.inUse:
		return quoteSpot{}, false
	case md.H <= 0 && s.spot.y < (bandTop+bandBottom)/2:
		return quoteSpot{x: s.spot.x, y: bandTop + quoteGap, pinned: true}, true
	case md.H <= 0:
		return quoteSpot{x: s.spot.x, y: bandBottom - quoteGap, above: true, pinned: true}, true
	}
	if (bottom < bandTop || top > bandBottom) && !s.inUse {
		return quoteSpot{}, false
	}
	spots := []quoteSpot{
		under,
		{x: by(hi), y: top - quoteGap, above: true},
		{x: by(lo), y: bandBottom - quoteGap, above: true, pinned: true},
		{x: by(hi), y: bandTop + quoteGap, pinned: true},
	}
	if bottom <= bandBottom {
		// Past only the pane's top, it's held there first.
		spots[2], spots[3] = spots[3], spots[2]
	}
	for _, p := range spots {
		r := ui.Rect{X: p.x, Y: p.y, W: quoteBarW, H: quoteBarH}
		if p.above {
			r.Y -= quoteBarH
		}
		if r.Y >= bandTop && r.Y+r.H <= bandBottom && !slices.ContainsFunc(a.waitingAt, func(v ui.Rect) bool { return meets(r, v) }) {
			return p, true
		}
	}
	return spots[2], true // none clear: held at the edge it tried first
}

// quoteBarAt builds the bar of the selection where spot is, from at by the
// selection or, without it, from the window.
func (a *app) quoteBarAt(c *ui.Context, t thread, at ui.Element, spot quoteSpot) {
	s := &a.quote
	s.spot = spot
	// Shown unasked, it leaves ⌘⏎ to the cards: it covers the timeline,
	// holding their keys, only while in use.
	coversTimeline := s.inUse
	a.overlay(c, coversTimeline, func() {
		if !at.Valid() {
			at = ui.Box(c).Key("quote-pin").Absolute().Left(spot.x).Top(spot.y).Size(1, 1).PassThrough()
		}
		self := ui.AnchorTopLeft
		if spot.above {
			self = ui.AnchorBottomLeft
		}
		bar := a.cover(coverKey{kind: "quote"}, ui.Row(c).Key("quote-bar")).
			AttachTo(at, ui.AnchorTopLeft, self).
			Gap(4).Padding(4).AlignItems(ui.Center).Radius(10).Width(quoteBarW).
			Background(palette.surface).Border(1, palette.border).Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.3))
		followUp(c, bar)
		bar.Children(func() {
			in := ui.TextInput(c.Key("quote-ask"), &s.ask).Placeholder("Ask about this…").Label("Ask about the selection").
				Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).Padding(6, 8).
				FontSize(12.5).Grow(1).Shrink(1)
			// ⌘⏎ too, which would otherwise reach the composer's, stopping
			// the agent or sending the draft.
			if cmdEnter := in.Shortcut(ui.Cmd, ui.KeyEnter); (in.Submitted() || cmdEnter) && strings.TrimSpace(s.ask) != "" {
				if a.sendText(t, blockquote(s.text)+"\n\n"+strings.TrimSpace(s.ask), "") {
					a.closeQuote()
					// Not back to the reply, which takes no typing.
					a.draftArea.Focus()
				}
				return
			}
			if quoteButton(c, "quote", icQuote, "Quote").Clicked() {
				a.attachQuote(s.text, "")
				return
			}
			if quoteButton(c, "explain", icSparkles, "Explain").Clicked() {
				a.attachQuote(s.text, explainAsk)
				return
			}
		})
		if bar.OverlayShortcut(0, ui.KeyEscape) || bar.PressedOutside() {
			a.closeQuote()
			return
		}
		if s.state == quoteOpen { // not closed by its buttons in this frame
			s.inUse = bar.Hovered() || bar.FocusWithin()
		}
	})
}

// followUp has a frame that draws e elsewhere than the last frame did
// build another: the bar, placed from where the last frame drew the reply,
// goes anew where it is, as the chat scrolled in that frame.
func followUp(c *ui.Context, e ui.Element) {
	was := e.LayoutBounds()
	e.Draw(func(_ *ui.Painter, r ui.Rect) {
		if r != was {
			c.Invalidate()
		}
	})
}

func quoteButton(c *ui.Context, key string, icon *ui.SVG, label string) ui.Element {
	return ghost(c, key, false).Padding(5, 8).Label(label).Children(func() {
		ui.Icon(c, icon).FontSize(13)
		ui.Text(c, label).FontSize(12)
	})
}

// closeQuote closes the bar, which stays closed on this selection.
func (a *app) closeQuote() {
	a.quote = quoteBar{of: a.quote.of, text: a.quote.text, state: quoteDismissed}
}

// attachQuote puts text on the composer as a quote, with ask added to the
// draft, and takes the composer there.
func (a *app) attachQuote(text, ask string) {
	a.attachments = append(a.attachments, quoteAttachment(text))
	if ask != "" {
		if d := strings.TrimRight(a.draft, " \t\n"); d != "" {
			a.draft = d + "\n\n" + ask
		} else {
			a.draft = ask
		}
		a.draftToEnd = true
	}
	a.closeQuote()
	a.draftArea.Focus()
}
