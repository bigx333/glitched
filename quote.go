package main

import (
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"

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
	text  string    // what was selected
	read  selection // what text was read from
	state quoteState
	at    time.Time // when the selection was made or last changed
	// from and to are where the pointer was, relative to the reply's
	// text, as the selection was first seen and as it last changed: where
	// it began and where it ended, the text w wide then; loose, a
	// selection they don't place, which may be anywhere in the reply
	// (followSelection).
	from, to ui.Point
	w        float32
	loose    bool
	// inUse is the bar in use in the last frame: pointed at or holding the
	// focus; focused is it holding the focus in this pass.
	inUse, focused bool
	ask            string
	// spot is where the bar last showed.
	spot quoteSpot
	// reply is where the selection's reply is, as its row last noted it,
	// and seen whether the row built since the bar last did.
	reply replyAt
	seen  bool
}

// replyAt is where the last frame drew a reply: its text at md, in its
// column at col, which the pass that built the row binds to app.quoteCol.
type replyAt struct {
	md, col ui.Rect
}

// selection is a reply's selection as last read: its parts, from the
// reply whose Markdown is src, and their text.
type selection struct {
	src   string
	parts []ui.SelectedText
	text  string
}

// textOf is the text of parts from the reply whose Markdown is src
// (selectedText), read anew only when either changed since r was read:
// MyGo gives a reply's selection in each frame that builds the reply.
func (r *selection) textOf(parts []ui.SelectedText, src string) string {
	if len(parts) == 0 {
		return ""
	}
	if src != r.src || !slices.Equal(parts, r.parts) {
		*r = selection{src: src, parts: parts, text: selectedText(parts, src)}
	}
	return r.text
}

// codeChunk is how many lines of a code block MyGo lays out in one text,
// a part of a selection each (ui/markdown's chunkLines).
const codeChunk = 32

// selectedText is the text of a selection's parts, from a reply whose
// Markdown is src, as ⌘C copies it: a paragraph each, but for the texts
// MyGo lays a long code block out in, which go on from line to line; a
// list's items and a table's cells are paragraphs too, as MyGo tells no
// part's block.
func selectedText(parts []ui.SelectedText, src string) string {
	var b strings.Builder
	var prev ui.SelectedText
	var seams map[string][]string // src's, once needed
	for _, p := range parts {
		r := []rune(p.Text)
		from, to := min(max(p.Start, 0), len(r)), min(max(p.End, 0), len(r))
		if from >= to {
			continue
		}
		switch {
		case b.Len() == 0:
		case prev.End >= utf8.RuneCountInString(prev.Text) && from == 0 && strings.Count(prev.Text, "\n") == codeChunk-1:
			// A text of as many lines as MyGo lays out in one, selected to
			// its end, and the next from its start: one code block where
			// the next is the text after it in the block, or its start, as
			// the block is still being written.
			if seams == nil {
				seams = codeSeams(src)
			}
			if slices.ContainsFunc(seams[prev.Text], func(next string) bool { return strings.HasPrefix(next, p.Text) }) {
				b.WriteString("\n")
			} else {
				b.WriteString("\n\n")
			}
		default:
			b.WriteString("\n\n")
		}
		b.WriteString(string(r[from:to]))
		prev = p
	}
	return strings.TrimSpace(b.String())
}

// codeSeams maps each text MyGo lays a code block of src out in, but the
// block's last, to the texts after it in its block: one, unless blocks
// have a text alike.
func codeSeams(src string) map[string][]string {
	d := parser.Parse(src)
	seams := map[string][]string{}
	var b strings.Builder
	for i := range d.Len() {
		if d.Block(i).Kind != parser.CodeBlock || d.LineCount(i) <= codeChunk {
			continue
		}
		n := d.LineCount(i)
		var prev string
		for first := 0; first < n; first += codeChunk {
			b.Reset()
			for k := first; k < min(first+codeChunk, n); k++ {
				if k > first {
					b.WriteByte('\n')
				}
				line, _ := d.Line(i, k)
				b.WriteString(line)
			}
			text := b.String()
			if text == "" {
				text = " " // as MyGo shows a text of one empty line
			}
			if first > 0 {
				seams[prev] = append(seams[prev], text)
			}
			prev = text
		}
	}
	return seams
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
// whose Markdown is src, column col and text md, and notes where the
// reply is, for its bar (selectionBar). It builds nothing of the bar, as
// the chat's list may build the row as it lays out, after the bar: a
// frame shows a change in whether the bar shows.
func (a *app) followSelection(c *ui.Context, t thread, key, src string, col, md ui.Element) {
	of := quoteOf{t.id(), key}
	s := &a.quote
	open := s.state == quoteOpen
	switch text := s.read.textOf(md.SelectedTexts(), src); {
	case text == "":
		if s.of == of && !s.inUse && s.ask == "" {
			*s = quoteBar{}
		}
	case s.of != of || s.text != text:
		// Made or changed: wait for it to rest. Grown or shrunk at an end,
		// as a drag changes it, it began where it did.
		x, y, _ := md.PointerPosition()
		p, w := ui.Point{X: x, Y: y}, md.Bounds().W
		from := p
		if s.of == of && atAnEnd(s.text, text) && s.w == w {
			from = s.from
		}
		// The pointer tells where the selection is when it moved across
		// it, or is on the word it selected at once, as a double-click
		// does; not with a modifier held, as with Shift and a click or the
		// keys, nor for more than a word selected at once, as by the
		// menu's Select All, which may be anywhere in the reply.
		loose := c.Modifiers() != 0 || from == p && strings.ContainsFunc(text, unicode.IsSpace)
		*s = quoteBar{of: of, text: text, read: s.read, at: a.now, from: from, to: p, w: w, loose: loose}
		c.After(quoteWait)
	case s.state == quoteWaiting && md.PressedOutside():
		// Pressed elsewhere before it rests, as in the composer to type:
		// not shown over what was pressed.
		a.closeQuote()
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
		s.reply, s.seen = replyAt{md: md.Bounds(), col: col.Bounds()}, true
		col.Bind(&a.quoteCol)
		followUp(c, col)
	}
}

// selectionBar builds the bar of the selection in t's timeline once it
// rests, by its reply as the reply's row last noted it (followSelection).
// It's built here alone, after the timeline, whose list builds the row in
// the view, or as it lays out after the view, or, while the chat is
// scrolled far from the reply, not at all, as the bar in use stays.
func (a *app) selectionBar(c *ui.Context, t thread) {
	s := &a.quote
	r := s.reply
	if !s.seen {
		r = replyAt{} // not built since: held at the edge it went past
	}
	s.seen, s.focused = false, false
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
	if el := c.Resolve(a.quoteCol); !spot.pinned && el.Valid() {
		el.Children(func() {
			at = ui.Box(c).Key("quote-at").Absolute().Left(spot.x-r.col.X).Top(spot.y-r.col.Y).Size(1, 1).PassThrough()
		})
	}
	a.quoteBarAt(c, t, at, spot)
}

// atAnEnd is whether now is was grown or shrunk at one end.
func atAnEnd(was, now string) bool {
	return strings.HasPrefix(now, was) || strings.HasSuffix(now, was) ||
		strings.HasPrefix(was, now) || strings.HasSuffix(was, now)
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
// the composer, and above Jump to latest, whose clicks it would take, held
// at its edge where neither fits, while any of the selection is in it;
// once none is, only in use, held at the edge the selection went past.
func (a *app) placeQuote(c *ui.Context, s *quoteBar, md, col ui.Rect) (quoteSpot, bool) {
	// The selection runs from the line of the point higher up to that of
	// the one lower down, a line of the reply's around each; loose, or the
	// reply rewrapped since, its text another width, over all of the reply.
	line := c.Theme().FontSize * 1.5
	hi, lo := s.from, s.to
	if hi.Y > lo.Y {
		hi, lo = lo, hi
	}
	top := md.Y + min(max(hi.Y, 0), md.H) - line
	bottom := md.Y + min(max(lo.Y, 0), md.H) + line
	if s.loose || md.W != s.w {
		top, bottom = md.Y, md.Y+md.H
	}
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
	// It goes no lower than Jump to latest, where the last frame drew it.
	floor := bandBottom
	if h := a.covers[coverKey{kind: "jump"}]; h != nil && a.jumpK > 0 {
		if r := h.Bounds(c); r.H > 0 {
			floor = min(floor, r.Y)
		}
	}
	// Held with its reply not built, it stays where it was across the
	// chat, but in the pane as it is now, which a panel opened beside it
	// or the sidebar shown may have narrowed.
	held := max(min(s.spot.x, a.paneAt.X+a.paneAt.W-columnRight-quoteBarW), a.paneAt.X+columnLeft)
	switch {
	case md.H <= 0 && !s.inUse:
		return quoteSpot{}, false
	case md.H <= 0 && s.spot.y < (bandTop+bandBottom)/2:
		return quoteSpot{x: held, y: bandTop + quoteGap, pinned: true}, true
	case md.H <= 0:
		return quoteSpot{x: held, y: floor - quoteGap, above: true, pinned: true}, true
	}
	if (bottom < bandTop || top > bandBottom) && !s.inUse {
		return quoteSpot{}, false
	}
	spots := []quoteSpot{
		under,
		{x: by(hi), y: top - quoteGap, above: true},
		{x: by(lo), y: floor - quoteGap, above: true, pinned: true},
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
		if r.Y >= bandTop && r.Y+r.H <= floor && !a.overWaiting(r) {
			return p, true
		}
	}
	return spots[2], true // none clear: held at the edge it tried first
}

// overWaiting is whether r lies over a waiting card where the last frame
// drew it, as this pass or the one before noted, the latter for a card
// the chat's list built as it laid out, after the bar.
func (a *app) overWaiting(r ui.Rect) bool {
	for _, drawn := range []map[api.ID]sight{a.drawn, a.drew} {
		for _, s := range drawn {
			if meets(r, s.card) {
				return true
			}
		}
	}
	return false
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
				if ask := strings.TrimSpace(s.ask); a.sendText(t, blockquote(s.text)+"\n\n"+ask, ask) {
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
			s.focused = bar.FocusWithin()
			s.inUse = bar.Hovered() || s.focused
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
