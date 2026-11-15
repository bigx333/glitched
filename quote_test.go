package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/provider/fake"
)

// quoteReady is the app on a thread with a reply, "Looking at the code.",
// and its first words dragged over.
func quoteReady(t *testing.T) (*harness, string) {
	t.Helper()
	h := enterReady(t, fake.New(fake.Turn(false)), "go")
	reply := "Looking at the code."
	h.until("the reply", func() bool { return h.tt.HasText(reply) })
	h.tt.Advance(time.Second)
	r, ok := h.tt.Find(reply)
	if !ok {
		t.Fatalf("no %q among %q", reply, h.tt.Texts())
	}
	y := r.Y + r.H/2
	h.tt.Press(r.X+1, y)
	h.tt.Move(r.X+15, y)
	h.tt.Move(r.X+70, y)
	h.tt.Release(r.X+70, y)
	h.tt.Frame()
	sel := h.a.quote.text
	if !strings.HasPrefix(sel, "Looking") || sel == reply {
		t.Fatalf("dragged over half the reply, the selection is %q", sel)
	}
	return h, sel
}

// barShown is whether the selection's bar shows.
func barShown(h *harness) bool { return h.a.quote.open && h.tt.HasText("Ask about the selection") }

// The bar waits for the selection to rest, then shows by it; Quote puts the
// selection on the composer, which sends it as a blockquote before the
// draft, and closes the bar.
func TestSelectionBarQuotes(t *testing.T) {
	h, sel := quoteReady(t)
	if barShown(h) {
		t.Fatal("the bar shows as the selection is made; want it to wait for it to rest")
	}
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	if !barShown(h) {
		t.Fatalf("the selection rested, no bar: %q", h.tt.Texts())
	}
	// Below the selection, leaving it in sight.
	r, _ := h.tt.Find("Looking at the code.")
	if b, _ := h.tt.Find("Explain"); b.Y < r.Y+r.H {
		t.Errorf("the bar's Explain at y %v, over the reply's line (%v to %v); want it below", b.Y, r.Y, r.Y+r.H)
	}
	if err := h.tt.Click("Quote"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	if barShown(h) {
		t.Error("Quote left the bar open")
	}
	i := slices.IndexFunc(h.a.attachments, func(at attachment) bool { return at.quote })
	if i < 0 || h.a.attachments[i].text != sel {
		t.Fatalf("attachments %+v; want a quote of %q", h.a.attachments, sel)
	}
	h.a.draft = "why?"
	h.tt.Frame()
	if got, want := h.a.message(h.a.current()), "> "+sel+"\n\nwhy?"; got != want {
		t.Errorf("message %q, want %q", got, want)
	}
	h.tt.Key(0, ui.KeyEnter)
	h.until("the quote sent", func() bool { return slices.Contains(sentTexts(h), "> "+sel+"\n\nwhy?") })
	if slices.ContainsFunc(h.a.attachments, func(at attachment) bool { return at.quote }) {
		t.Error("the quote stayed on the composer after it was sent")
	}
}

// Explain quotes the selection and asks for an explanation in the draft,
// after what the draft held.
func TestSelectionBarExplains(t *testing.T) {
	h, sel := quoteReady(t)
	h.a.draft = "Before that, "
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	if err := h.tt.Click("Explain"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	if want := "Before that,\n\n" + explainAsk; h.a.draft != want {
		t.Errorf("draft %q, want %q", h.a.draft, want)
	}
	if !slices.ContainsFunc(h.a.attachments, func(at attachment) bool { return at.quote && at.text == sel }) {
		t.Errorf("attachments %+v; want a quote of %q", h.a.attachments, sel)
	}
}

// A question typed in the bar goes at once, with the quote, leaving the
// draft and its attachments as they were.
func TestSelectionBarAsks(t *testing.T) {
	h, sel := quoteReady(t)
	h.a.draft = "unrelated draft"
	h.a.attachments = []attachment{{name: "Pasted text", text: "kept"}}
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	if err := h.tt.Click("Ask about the selection"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Texts())
	}
	h.tt.Frame()
	if !barShown(h) {
		t.Fatal("a press in the bar's box closed it")
	}
	h.tt.Type("what does it mean?")
	h.tt.Key(0, ui.KeyEnter)
	want := "> " + sel + "\n\nwhat does it mean?"
	h.until("the question sent", func() bool { return slices.Contains(sentTexts(h), want) })
	h.tt.Advance(time.Second)
	if h.a.draft != "unrelated draft" || len(h.a.attachments) != 1 || h.a.attachments[0].text != "kept" {
		t.Errorf("after asking: draft %q, attachments %+v; want the draft untouched", h.a.draft, h.a.attachments)
	}
	if barShown(h) {
		t.Error("the bar stayed after the question went")
	}
}

// Escape closes the bar, which doesn't come back for the same selection.
func TestSelectionBarEscape(t *testing.T) {
	h, _ := quoteReady(t)
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	if !barShown(h) {
		t.Fatal("no bar")
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Advance(time.Second)
	if barShown(h) {
		t.Error("Escape left the bar open")
	}
}

func TestBlockquote(t *testing.T) {
	for in, want := range map[string]string{
		"one":                "> one",
		"one\n\ntwo  ":       "> one\n>\n> two",
		"  padded\nlines \n": "> padded\n> lines",
	} {
		if got := blockquote(in); got != want {
			t.Errorf("blockquote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSelectedText(t *testing.T) {
	parts := []ui.SelectedText{
		{Text: "héllo world", Start: 1, End: 5},
		{Text: "x", Start: 3, End: 9}, // out of range: nothing
		{Text: "second line", Start: 0, End: 6},
	}
	if got, want := selectedText(parts), "éllo\n\nsecond"; got != want {
		t.Errorf("selectedText = %q, want %q", got, want)
	}
}

// Quoted, cleared and selected again, the same word brings the bar back,
// though nothing else was selected between.
func TestSelectionBarReturnsAfterQuote(t *testing.T) {
	h, _ := quoteReady(t)
	r, _ := h.tt.Find("Looking at the code.")
	word := func() { // a double-click on "Looking"
		h.tt.ClickAt(r.X+10, r.Y+r.H/2)
		h.tt.ClickAt(r.X+10, r.Y+r.H/2)
		h.tt.Advance(quoteWait + 50*time.Millisecond)
	}
	word()
	if !barShown(h) || h.a.quote.text != "Looking" {
		t.Fatalf("double-clicked, bar %v on %q", barShown(h), h.a.quote.text)
	}
	if err := h.tt.Click("Quote"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	// A click at the reply's end clears the selection.
	h.tt.ClickAt(r.X+r.W-2, r.Y+r.H/2)
	h.tt.Advance(time.Second)
	word()
	if !barShown(h) {
		t.Fatalf("the word selected again after a quote, no bar: %+v", h.a.quote)
	}
}

// Shown unasked, the bar leaves the cards' keys alone: it covers the
// timeline only while pointed at or typed in.
func TestSelectionBarCoversOnlyInUse(t *testing.T) {
	h, _ := quoteReady(t)
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	h.tt.Move(1, 1)
	h.tt.Advance(time.Second)
	if !barShown(h) {
		t.Fatal("no bar")
	}
	if h.a.covered {
		t.Error("the bar, untouched, covers the timeline, holding ⌘⏎ from the cards")
	}
	if err := h.tt.Click("Ask about the selection"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	if !h.a.covered || h.a.blind {
		t.Errorf("typed in, the bar: covered %v, blind %v; want it to cover only its place", h.a.covered, h.a.blind)
	}
}

// doubleClick selects the word of s at its start.
func doubleClick(h *harness, s string) ui.Rect {
	r, _ := h.tt.Find(s)
	h.tt.ClickAt(r.X+10, r.Y+r.H/2)
	h.tt.ClickAt(r.X+10, r.Y+r.H/2)
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	return r
}

// Selected just over the composer, the bar goes above the selection,
// clear of both.
func TestSelectionBarAboveNearComposer(t *testing.T) {
	h, _ := quoteReady(t)
	h.tt.Advance(time.Second)
	r := doubleClick(h, "Done.")
	if !barShown(h) || h.a.quote.text != "Done" {
		t.Fatalf("double-clicked Done., bar %v on %q", barShown(h), h.a.quote.text)
	}
	b, _ := h.tt.Find("Explain")
	if b.Y+b.H > r.Y {
		t.Errorf("the bar's Explain (%v to %v) reaches the selected line at %v; want it above", b.Y, b.Y+b.H, r.Y)
	}
}

// ⌘⏎ in the bar's box sends the question, not the composer's draft.
func TestSelectionBarCmdEnter(t *testing.T) {
	h, sel := quoteReady(t)
	h.a.draft = "composer draft"
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	if err := h.tt.Click("Ask about the selection"); err != nil {
		t.Fatal(err)
	}
	h.tt.Type("why?")
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	want := "> " + sel + "\n\nwhy?"
	h.until("the question sent", func() bool { return slices.Contains(sentTexts(h), want) })
	if slices.Contains(sentTexts(h), "composer draft") || h.a.draft != "composer draft" {
		t.Errorf("⌘⏎ in the box: sent %q, draft %q; want only the question sent", sentTexts(h), h.a.draft)
	}
}

// After Explain, what's typed goes after the ask.
func TestExplainLeavesCaretAtEnd(t *testing.T) {
	h, _ := quoteReady(t)
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	if err := h.tt.Click("Explain"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	h.tt.Type(" More")
	h.tt.Frame()
	if want := explainAsk + " More"; h.a.draft != want {
		t.Errorf("typed after Explain: draft %q, want %q", h.a.draft, want)
	}
}

// With the pointer on the bar, a selection changed from the keyboard is
// what Quote quotes.
func TestSelectionBarFollowsNewSelection(t *testing.T) {
	h, _ := quoteReady(t)
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	b, _ := h.tt.Find("Quote")
	h.tt.Move(b.X+b.W/2, b.Y+b.H/2)
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	if err := h.tt.Click("Quote"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if i := slices.IndexFunc(h.a.attachments, func(at attachment) bool { return at.quote }); i < 0 || h.a.attachments[i].text != "Looking at the code." {
		t.Errorf("attachments %+v; want a quote of the whole reply", h.a.attachments)
	}
}

func TestThreadTitleFromDraft(t *testing.T) {
	if got := threadTitle("> a quote\n\nFix the flicker", " Fix the flicker "); got != "Fix the flicker" {
		t.Errorf("title %q, want the draft's", got)
	}
	if got := threadTitle("> only a quote", ""); got != "> only a quote" {
		t.Errorf("title %q, want the text without a draft", got)
	}
}

// Scrolled past the pane's top or under the composer, the bar waits
// unseen rather than riding over the header or the composer.
func TestSelectionBarStaysInTimeline(t *testing.T) {
	a, tt, th := longThread(t, nil)
	h := &harness{t: t, a: a, tt: tt}
	l := a.listFor(th.id())
	l.ScrollTo(0, ui.Start)
	tt.Advance(time.Second)
	doubleClick(h, "Looking at the code.")
	if !barShown(h) {
		t.Fatalf("no bar on the first reply: %+v", a.quote)
	}
	mid := a.paneAt.X + a.paneAt.W/2
	for _, dy := range []float32{40, -40} {
		for range 30 {
			tt.Scroll(mid, a.paneAt.Y+a.paneAt.H/2, 0, dy)
			tt.Advance(100 * time.Millisecond)
			if r, ok := tt.Find("Ask about the selection"); ok && (r.Y < a.paneAt.Y || r.Y+r.H > a.composerAt.Y) {
				t.Fatalf("scrolled by %v, the bar shows at y %v to %v, past the timeline (%v to %v)", dy, r.Y, r.Y+r.H, a.paneAt.Y, a.composerAt.Y)
			}
		}
	}
}
