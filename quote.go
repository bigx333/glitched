package main

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// Text selected in an assistant's reply brings up a bar by where the
// selection ended, as T3 Code's does: Quote puts the selection on the
// composer as a quote, which goes with the next message as a blockquote;
// Explain does that and asks for an explanation in the draft; a question
// typed in its box goes at once, with the quote and nothing of the draft.
// It waits for the selection to rest, as there is no telling the end of a
// drag otherwise, and keeps what was selected while it's in use, as a
// press in it may take the selection away.

// quoteWait is how long a selection rests before its bar shows.
const quoteWait = 300 * time.Millisecond

// explainAsk is what Explain asks.
const explainAsk = "Explain this."

// quoteBar is the bar of the selection in a reply, if any.
type quoteBar struct {
	of   string // the reply's: its thread and its entry's key
	text string // what was selected
	at   time.Time
	// x and y are where the selection ended, relative to the reply's
	// text: the pointer as the selection last changed.
	x, y float32
	open bool
	// shut is a bar closed on this selection, which doesn't come back
	// until another is made.
	shut bool
	// held is the bar in use in the last frame: pointed at or holding the
	// focus.
	held bool
	ask  string
}

// selectedText is the text of a selection's parts, a paragraph each, as
// the reply's own copy as Markdown has them.
func selectedText(parts []ui.SelectedText) string {
	lines := make([]string, 0, len(parts))
	for _, p := range parts {
		r := []rune(p.Text)
		from, to := min(max(p.Start, 0), len(r)), min(max(p.End, 0), len(r))
		if from < to {
			lines = append(lines, string(r[from:to]))
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n\n"))
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

// selectionBar follows the selection in the reply of t keyed key, whose
// column is col and text md, and shows its bar once it rests.
func (a *app) selectionBar(c *ui.Context, t thread, key string, col, md ui.Element) {
	of := t.id().String() + "/" + key
	s := &a.quote
	text := selectedText(md.SelectedTexts())
	switch {
	case text != "" && (s.of != of || s.text != text):
		// Made or changed: wait for it to rest, where the pointer is.
		if s.of == of && s.open && s.held {
			break // the bar in use keeps what it has
		}
		*s = quoteBar{of: of, text: text, at: a.now}
		s.x, s.y, _ = md.PointerPosition()
		c.After(quoteWait)
	case text != "" && !s.open && !s.shut:
		if a.now.Sub(s.at) < quoteWait {
			c.After(quoteWait - a.now.Sub(s.at))
			break
		}
		s.open = true
	case text == "" && s.of == of && !s.held:
		*s = quoteBar{}
	}
	if s.of != of || !s.open {
		return
	}
	// Where the selection ended, in the reply's column, which the bar
	// hangs from, following it as the chat scrolls.
	r, cb := md.Bounds(), col.Bounds()
	x := min(max(s.x, 0), r.W) + r.X - cb.X
	y := min(max(s.y, 0), r.H) + r.Y - cb.Y
	at := ui.Box(c).Key("quote-at").Absolute().Left(x).Top(y).Size(1, 1).PassThrough()
	// It covers the timeline, holding the cards' keys, only while in use:
	// shown unasked, it leaves ⌘⏎ to approve.
	a.overlay(c, s.held, func() {
		bar := a.cover(coverKey{"quote", at.ID()}, ui.Row(c).Key("quote-bar")).
			AttachTo(at, ui.AnchorBottomStart, ui.AnchorTopStart).Margin(16, 0, 0, -12).
			Gap(4).Padding(4).AlignItems(ui.Center).Radius(10).Width(360).
			Background(palette.surface).Border(1, palette.border).Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.3))
		bar.Children(func() {
			in := ui.TextInput(c.Key("quote-ask"), &s.ask).Placeholder("Ask about this…").Label("Ask about the selection").
				Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).Padding(6, 8).
				FontSize(12.5).Grow(1).Shrink(1)
			if in.Submitted() && strings.TrimSpace(s.ask) != "" {
				if a.sendText(t, blockquote(s.text)+"\n\n"+strings.TrimSpace(s.ask)) {
					a.closeQuote()
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
		if s.open { // not closed by its buttons in this frame
			s.held = bar.Hovered() || bar.FocusWithin()
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
	a.quote = quoteBar{of: a.quote.of, text: a.quote.text, shut: true}
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
	}
	a.closeQuote()
	a.draftArea.Focus()
}
