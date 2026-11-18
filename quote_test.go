package main

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// quoteReady is the app on a thread with a reply, "Looking at the code.",
// and its first words dragged over.
func quoteReady(t *testing.T) (*harness, string) {
	t.Helper()
	h := enterReady(t, fake.New(fake.Turn(false)), "go")
	return h, dragHalfReply(t, h)
}

// dragHalfReply drags over the first words of the reply, "Looking at the
// code.", and returns what was selected.
func dragHalfReply(t *testing.T, h *harness) string {
	t.Helper()
	reply := "Looking at the code."
	h.untilShown("the reply", reply)
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
	return sel
}

// replyReady is the app on a thread whose one reply is md, once it shows
// wait.
func replyReady(t *testing.T, md, wait string) *harness {
	t.Helper()
	h := enterReady(t, fake.New([]provider.Event{provider.TextDelta{Text: md}}), "go")
	h.untilShown("the reply", wait)
	h.tt.Advance(2 * time.Second)
	return h
}

// untilShown runs what the manager posts and lets the clock run, as a
// streamed reply shows its words over time, until text shows.
func (h *harness) untilShown(what, text string) {
	h.t.Helper()
	for end := time.Now().Add(30 * time.Second); !h.tt.HasText(text); time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			h.t.Fatalf("timed out waiting for %s; the window shows %q", what, h.tt.Texts())
		}
		if h.p.run() {
			h.tt.Frame()
		}
		h.tt.Advance(16 * time.Millisecond)
	}
}

// barShown is whether the selection's bar shows.
func barShown(h *harness) bool { return h.tt.HasText("Ask about the selection") }

// barAt is where the selection's bar shows: around its box and buttons.
func barAt(h *harness) (ui.Rect, bool) {
	in, ok := h.tt.Find("Ask about the selection")
	if !ok {
		return ui.Rect{}, false
	}
	b, _ := h.tt.Find("Explain")
	x, y := min(in.X, b.X), min(in.Y, b.Y)
	return ui.Rect{X: x, Y: y, W: max(in.X+in.W, b.X+b.W) - x, H: max(in.Y+in.H, b.Y+b.H) - y}, true
}

// inTimeline is whether r lies in the timeline, between the pane's top
// and the composer's.
func inTimeline(h *harness, r ui.Rect) bool {
	return r.Y >= h.a.paneAt.Y-0.5 && r.Y+r.H <= h.a.composerAt.Y+0.5
}

// startsWith is the box of the first text starting with prefix.
func startsWith(h *harness, prefix string) ui.Rect {
	h.t.Helper()
	for _, s := range h.tt.Texts() {
		if strings.HasPrefix(s, prefix) {
			r, _ := h.tt.Find(s)
			return r
		}
	}
	h.t.Fatalf("no text starting %q", prefix)
	return ui.Rect{}
}

// drag selects from (x0, y0) to (x1, y1) as a hand does, the selection
// starting a little way from the press, and lets it rest.
func drag(h *harness, x0, y0, x1, y1 float32) {
	step := float32(20)
	if x1 < x0 {
		step = -step
	}
	h.tt.Press(x0, y0)
	h.tt.Move(x0+step, y0)
	h.tt.Move((x0+x1)/2, (y0+y1)/2)
	h.tt.Move(x1, y1)
	h.tt.Frame()
	h.tt.Release(x1, y1)
	h.tt.Advance(quoteWait + 50*time.Millisecond)
}

// wheel scrolls the chat by dy, a notch of 40 at a time, the pointer at
// the pane's right edge, off the bar.
func wheel(h *harness, dy float32) {
	at := h.a.paneAt
	notch := float32(40)
	if dy < 0 {
		notch = -notch
	}
	for range int(dy / notch) {
		h.tt.Scroll(at.X+at.W-8, at.Y+at.H/2, 0, notch)
		h.tt.Advance(50 * time.Millisecond)
	}
	h.tt.Advance(time.Second)
}

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

// Explain leaves the composer's undo: ⌘Z takes back what it asked.
func TestExplainUndoes(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)), "go")
	h.tt.Type("Before that,")
	dragHalfReply(t, h)
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	h.click("Explain")
	h.tt.Advance(time.Second)
	if want := "Before that,\n\n" + explainAsk; h.a.draft != want {
		t.Fatalf("draft %q, want %q", h.a.draft, want)
	}
	h.tt.Key(ui.Cmd, ui.KeyZ)
	h.tt.Frame()
	if strings.Contains(h.a.draft, explainAsk) {
		t.Errorf("⌘Z after Explain left the draft %q; want what Explain asked taken back", h.a.draft)
	}
}

// A question typed in the bar goes at once, with the quote, leaving the
// draft and its attachments as they were, and the composer taking what's
// typed next.
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
	h.tt.Type("!")
	h.tt.Frame()
	if !strings.Contains(h.a.draft, "!") {
		t.Errorf("typed after asking: draft %q; want what was typed in the composer", h.a.draft)
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

// A press elsewhere while the bar waits for the selection to rest, as in
// the composer to type there, leaves the bar unshown.
func TestSelectionBarWaitingPressedElsewhere(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)), "go")
	h.untilShown("the reply", "Looking at the code.")
	h.tt.Advance(time.Second)
	r, _ := h.tt.Find("Looking at the code.")
	h.tt.ClickAt(r.X+10, r.Y+r.H/2)
	h.tt.ClickAt(r.X+10, r.Y+r.H/2)
	h.tt.Advance(quoteWait / 3)
	if barShown(h) || h.a.quote.text != "Looking" {
		t.Fatalf("a word double-clicked: bar %v, selection %q; want it waiting on Looking", barShown(h), h.a.quote.text)
	}
	at := h.a.composerAt
	h.tt.ClickAt(at.X+at.W/2, at.Y+30)
	h.tt.Type("hello")
	h.tt.Advance(time.Second)
	if h.a.draft != "hello" {
		t.Fatalf("typed in the composer: draft %q", h.a.draft)
	}
	if barShown(h) {
		t.Error("the composer pressed before the selection rested, the selection's bar came up all the same")
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
	if got, want := selectedText(parts, "héllo world\n\nsecond line"), "éllo\n\nsecond"; got != want {
		t.Errorf("selectedText = %q, want %q", got, want)
	}
	// A heading, and the paragraph on the line after it.
	parts = []ui.SelectedText{{Text: "Title", Start: 0, End: 5}, {Text: "Text", Start: 0, End: 4}}
	if got, want := selectedText(parts, "# Title\nText"), "Title\n\nText"; got != want {
		t.Errorf("selectedText of a heading and a paragraph = %q, want %q", got, want)
	}
}

// quoteAll selects all of the reply from the word of at, and returns what
// Quote quotes of it and what ⌘C copies.
func quoteAll(t *testing.T, h *harness, at string) (quote, copied string) {
	t.Helper()
	doubleClick(h, at)
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	h.tt.Key(ui.Cmd, ui.KeyC)
	h.tt.Frame()
	copied = strings.TrimSpace(h.tt.Clipboard())
	h.click("Quote")
	h.tt.Advance(time.Second)
	i := slices.IndexFunc(h.a.attachments, func(at attachment) bool { return at.quote })
	if i < 0 {
		t.Fatalf("attachments %+v; want a quote", h.a.attachments)
	}
	return h.a.attachments[i].text, copied
}

// codeLines is n lines of code, each after indent.
func codeLines(n int, indent string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%sline%02d := %d\n", indent, i, i)
	}
	return b.String()
}

// A quote of a code block has its lines as the reply has them, as ⌘C
// copies them, however many texts MyGo lays it out in, and a paragraph
// after it as one.
func TestSelectionQuotesLongCode(t *testing.T) {
	for name, md := range map[string]string{
		"70 lines":                 "Intro here.\n\n```go\n" + codeLines(70, "") + "```\n\nOutro here.",
		"32 lines":                 "Intro here.\n\n```go\n" + codeLines(32, "") + "```\n\nOutro here.",
		"64 lines":                 "Intro here.\n\n```go\n" + codeLines(64, "") + "```\n\nOutro here.",
		"40 lines in a list item":  "Intro here.\n\n1. Run this:\n\n   ```go\n" + codeLines(40, "   ") + "   ```\n\nOutro here.",
		"40 lines in a blockquote": "Intro here.\n\n> ```go\n" + codeLines(40, "> ") + "> ```\n\nOutro here.",
	} {
		t.Run(name, func(t *testing.T) {
			h := replyReady(t, md, "Outro here.")
			q, copied := quoteAll(t, h, "Outro here.")
			if !strings.Contains(copied, "line31 := 31\nline32 := 32") {
				t.Fatalf("⌘C copied %q; want the code's lines", copied)
			}
			if q != copied {
				t.Errorf("the quote is\n%s\nwant it as ⌘C copies the selection:\n%s", q, copied)
			}
		})
	}
}

// A quote keeps apart the blocks ⌘C keeps apart, though the reply's
// Markdown has them on lines one after the other, or has them as lines
// one after the other of a code block elsewhere.
func TestSelectionQuotesBlocksApart(t *testing.T) {
	for name, md := range map[string]string{
		"a paragraph, then a blockquote":              "Intro line here.\n> Quoted line here.\n\nOutro here.",
		"a blockquote in a blockquote":                "> Outer line here.\n> > Inner line here.\n\nOutro here.",
		"paragraphs also in code":                     "Run these.\n\nnpm install\n\nnpm test\n\n```sh\nnpm install\nnpm test\n```\n\nOutro here.",
		"a loose list's items also in code":           "Steps:\n\n- npm install\n\n- npm test\n\nAt once:\n\n```sh\nnpm install\nnpm test\n```\n\nOutro here.",
		"paragraphs also at a long code block's seam": "line32 := 32\n\nline33 := 33\n\n```go\n" + codeLines(40, "") + "```\n\nOutro here.",
	} {
		t.Run(name, func(t *testing.T) {
			h := replyReady(t, md, "Outro here.")
			if q, copied := quoteAll(t, h, "Outro here."); q != copied {
				t.Errorf("the quote is\n%s\nwant it as ⌘C copies the selection:\n%s", q, copied)
			}
		})
	}
}

// Changed a key at a time, all of a reply of many paragraphs selected
// takes about as long a frame to follow as a few words: its text is read
// in time in proportion to it.
func TestSelectionOfManyParagraphsFollowsQuickly(t *testing.T) {
	var md strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&md, "Paragraph %04d of a long reply that goes on.\n\n", i)
	}
	h := replyReady(t, md.String()+"Outro here.", "Outro here.")
	// shrink is how long a frame takes as Shift and the left arrow shrink
	// the selection, the least of a few runs.
	shrink := func() time.Duration {
		var least time.Duration
		for i := range 5 {
			start := time.Now()
			for range 10 {
				h.tt.Key(ui.Shift, ui.KeyLeft)
			}
			if took := time.Since(start) / 10; i == 0 || took < least {
				least = took
			}
		}
		return least
	}
	doubleClick(h, "Outro here.")
	few := shrink()
	h.tt.Key(ui.Cmd, ui.KeyA)
	all := shrink()
	h.tt.HoldModifiers(0)
	if n := len([]rune(h.a.quote.text)); n < 40000 {
		t.Fatalf("all of the reply selected and shrunk, %d runes of it selected", n)
	}
	if all > 2*few {
		t.Errorf("shrunk a key at a time, all of the reply selected takes %v a frame, a few words %v; want about as long", all, few)
	}
}

// A frame with all of a long reply selected allocates about as much as
// one with nothing selected: MyGo gives the selection's parts in each
// frame, and their text is read anew only as they change.
func TestSelectionReadAsItChanges(t *testing.T) {
	var paras strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&paras, "Paragraph %04d of a long reply that goes on.\n\n", i)
	}
	for name, md := range map[string]string{
		"1000 paragraphs":     paras.String() + "Outro here.",
		"10000 lines of code": "Intro here.\n\n```go\n" + codeLines(10000, "") + "```\n\nOutro here.",
	} {
		t.Run(name, func(t *testing.T) {
			h := replyReady(t, md, "Outro here.")
			allocs := func() float64 { return testing.AllocsPerRun(10, func() { h.tt.Frame() }) }
			none := allocs()
			doubleClick(h, "Outro here.")
			h.tt.Key(ui.Cmd, ui.KeyA)
			h.tt.HoldModifiers(0)
			h.tt.Move(1, 1)
			h.tt.Advance(time.Second)
			if !barShown(h) {
				t.Fatalf("all of the reply selected, no bar: %+v", h.a.quote)
			}
			// What the bar itself allocates, and a little more.
			if all := allocs(); all > none+100 {
				t.Errorf("a frame allocates %.0f times with all of the reply selected, %.0f with nothing selected; want about as many", all, none)
			}
		})
	}
}

// Quoted, cleared and selected again, the same word brings the bar back,
// though nothing else was selected between.
func TestSelectionBarReturnsAfterQuote(t *testing.T) {
	h, _ := quoteReady(t)
	r := doubleClick(h, "Looking at the code.")
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
	doubleClick(h, "Looking at the code.")
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
	// Two frames: the second reads what the first covered.
	h.tt.Frame()
	h.tt.Frame()
	if h.a.covered {
		t.Error("the bar, untouched, covers the timeline, holding ⌘⏎ from the cards")
	}
	if err := h.tt.Click("Ask about the selection"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	h.tt.Frame()
	h.tt.Frame()
	if !h.a.covered || h.a.blind {
		t.Errorf("typed in, the bar: covered %v, blind %v; want it to cover only its place", h.a.covered, h.a.blind)
	}
}

// doubleClick selects the word of s at its start.
func doubleClick(h *harness, s string) ui.Rect {
	r, _ := h.tt.Find(s)
	doubleClickAt(h, r.X+10, r.Y+r.H/2)
	return r
}

// doubleClickAt selects the word at (x, y), and lets it rest.
func doubleClickAt(h *harness, x, y float32) {
	h.tt.ClickAt(x, y)
	h.tt.ClickAt(x, y)
	h.tt.Advance(quoteWait + 50*time.Millisecond)
}

// Selected just over the composer, the bar goes above the selection,
// clear of both.
func TestSelectionBarAboveNearComposer(t *testing.T) {
	h, _ := quoteReady(t)
	h.untilShown("the reply's end", "Done.")
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

// A drag's bar keeps clear of all it selected, not just where it was let
// go: under its last line, or above its first where under would cross the
// composer.
func TestSelectionBarClearOfSelection(t *testing.T) {
	const line = 19.5
	t.Run("down to the composer", func(t *testing.T) {
		para := "Alpha " + strings.Repeat("lorem ipsum dolor sit amet ", 30) + "omega."
		h := replyReady(t, "Intro here.\n\n"+para, "Intro here.")
		r := startsWith(h, "Alpha ")
		drag(h, r.X+2, r.Y+10, r.X+300, r.Y+r.H-10)
		b, ok := barAt(h)
		if !ok {
			t.Fatalf("no bar: %+v", h.a.quote)
		}
		if b.Y < r.Y+r.H && b.Y+b.H > r.Y || !inTimeline(h, b) {
			t.Errorf("the bar (y %v to %v) over the selected lines (y %v to %v), or out of the timeline (%v to %v); want it clear of both",
				b.Y, b.Y+b.H, r.Y, r.Y+r.H, h.a.paneAt.Y, h.a.composerAt.Y)
		}
	})
	t.Run("up", func(t *testing.T) {
		para := "Alpha " + strings.Repeat("lorem ipsum dolor sit amet ", 60) + "omega."
		h := replyReady(t, para+"\n\n"+strings.Repeat("Filler line.\n\n", 6)+"Outro here.", "Outro here.")
		r := startsWith(h, "Alpha ")
		drag(h, r.X+300, r.Y+4*line+10, r.X+100, r.Y+10)
		b, ok := barAt(h)
		if !ok {
			t.Fatalf("no bar: %+v", h.a.quote)
		}
		if top, bottom := r.Y, r.Y+5*line; b.Y < bottom && b.Y+b.H > top || !inTimeline(h, b) {
			t.Errorf("the bar (y %v to %v) over the lines dragged up over (y %v to %v), or out of the timeline (%v to %v); want it clear of both",
				b.Y, b.Y+b.H, top, bottom, h.a.paneAt.Y, h.a.composerAt.Y)
		}
	})
	t.Run("up with a rest on the way", func(t *testing.T) {
		para := "Alpha " + strings.Repeat("lorem ipsum dolor sit amet ", 60) + "omega."
		h := replyReady(t, para+"\n\n"+strings.Repeat("Filler line.\n\n", 6)+"Outro here.", "Outro here.")
		r := startsWith(h, "Alpha ")
		x0, y0 := r.X+300, r.Y+7*line+10 // line 8
		h.tt.Press(x0, y0)
		h.tt.Move(x0-20, y0)
		h.tt.Move(x0-60, y0-4*line) // line 4
		h.tt.Advance(quoteWait + 100*time.Millisecond)
		h.tt.Move(x0-200, y0-7*line) // line 1
		h.tt.Release(x0-200, y0-7*line)
		h.tt.Advance(quoteWait + 500*time.Millisecond)
		b, ok := barAt(h)
		if !ok {
			t.Fatalf("no bar: %+v", h.a.quote)
		}
		if top, bottom := r.Y, r.Y+8*line; b.Y < bottom-1 && b.Y+b.H > top || !inTimeline(h, b) {
			t.Errorf("the bar (y %v to %v) over the lines dragged up over (y %v to %v), or out of the timeline (%v to %v); want it clear of both",
				b.Y, b.Y+b.H, top, bottom, h.a.paneAt.Y, h.a.composerAt.Y)
		}
	})
}

// A selection made other than by a drag keeps its bar clear of all it
// selected, though the pointer is elsewhere: Shift and a click, Select
// All from the menu or ⌘A, Shift and the arrows; a word double-clicked
// has it by the word.
func TestSelectionBarClearOfSelectionNotDragged(t *testing.T) {
	const line = 19.5
	para := "Alpha " + strings.Repeat("lorem ipsum dolor sit amet ", 40) + "omega."
	// clear fails unless the bar, shown, keeps off the lines selected,
	// from top to bottom.
	clear := func(t *testing.T, h *harness, top, bottom float32) {
		t.Helper()
		b, ok := barAt(h)
		if !ok {
			t.Fatalf("no bar: %+v", h.a.quote)
		}
		if b.Y < bottom-1 && b.Y+b.H > top || !inTimeline(h, b) {
			t.Errorf("the bar (y %v to %v) over the lines selected (y %v to %v), or out of the timeline (%v to %v); want it clear of both",
				b.Y, b.Y+b.H, top, bottom, h.a.paneAt.Y, h.a.composerAt.Y)
		}
	}
	t.Run("Shift and a click", func(t *testing.T) {
		h := replyReady(t, para+"\n\nOutro here.", "Outro here.")
		r := startsWith(h, "Alpha ")
		// The caret on line 6, then Shift and a click on line 1.
		h.tt.ClickAt(r.X+300, r.Y+5*line+10)
		h.tt.Advance(time.Second)
		h.tt.ClickAtWith(ui.Shift, r.X+100, r.Y+10)
		h.tt.HoldModifiers(0)
		h.tt.Advance(quoteWait + 500*time.Millisecond)
		clear(t, h, r.Y, r.Y+6*line)
	})
	t.Run("Select All", func(t *testing.T) {
		h := replyReady(t, para+"\n\nOutro here.", "Outro here.")
		r := startsWith(h, "Alpha ")
		h.tt.RightClickAt(r.X+300, r.Y+10)
		if err := h.tt.ChooseMenuItem("Select All"); err != nil {
			t.Fatalf("%v: menu %q", err, h.tt.Menu())
		}
		h.tt.Advance(quoteWait + 500*time.Millisecond)
		outro, _ := h.tt.Find("Outro here.")
		clear(t, h, r.Y, outro.Y+outro.H)
	})
	t.Run("⌘A", func(t *testing.T) {
		h := replyReady(t, para+"\n\nOutro here.", "Outro here.")
		r := startsWith(h, "Alpha ")
		h.tt.ClickAt(r.X+10, r.Y+10)
		h.tt.Key(ui.Cmd, ui.KeyA)
		h.tt.HoldModifiers(0)
		h.tt.Advance(quoteWait + 500*time.Millisecond)
		outro, _ := h.tt.Find("Outro here.")
		clear(t, h, r.Y, outro.Y+outro.H)
	})
	t.Run("Shift and the arrows", func(t *testing.T) {
		h := replyReady(t, para+"\n\nOutro here.", "Outro here.")
		r := startsWith(h, "Alpha ")
		doubleClickAt(h, r.X+10, r.Y+10)
		for range 4 {
			h.tt.Key(ui.Shift, ui.KeyDown)
		}
		h.tt.HoldModifiers(0)
		h.tt.Advance(quoteWait + 500*time.Millisecond)
		clear(t, h, r.Y, r.Y+5*line)
	})
	t.Run("a word double-clicked", func(t *testing.T) {
		h := replyReady(t, para+"\n\nOutro here.", "Outro here.")
		r := startsWith(h, "Alpha ")
		doubleClickAt(h, r.X+10, r.Y+10)
		b, ok := barAt(h)
		if !ok || h.a.quote.text != "Alpha" {
			t.Fatalf("double-clicked Alpha: bar %v on %q", ok, h.a.quote.text)
		}
		if b.Y < r.Y+line || b.Y > r.Y+3*line {
			t.Errorf("the bar at y %v, the word's line at y %v to %v; want the bar just under it", b.Y, r.Y, r.Y+line)
		}
	})
}

// A drag let go over the composer or over the header brings up its bar
// too, in the timeline.
func TestSelectionBarLetGoPastTheTimeline(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&b, "Paragraph %02d of a long reply that goes on.\n\n", i)
	}
	h := replyReady(t, b.String()+"Outro here.", "Outro here.")
	wheel(h, -600)
	for _, to := range []struct {
		name   string
		y0, y1 float32
	}{
		{"over the composer", 300, h.a.composerAt.Y + 20},
		{"over the header", 400, 30},
	} {
		drag(h, 430, to.y0, 520, to.y1)
		h.tt.Advance(time.Second)
		bar, ok := barAt(h)
		if n := len([]rune(h.a.quote.text)); n == 0 {
			t.Fatalf("let go %s: nothing selected", to.name)
		}
		if !ok || !inTimeline(h, bar) {
			t.Errorf("let go %s (y %v): bar %v at %v; want it in the timeline (%v to %v)", to.name, to.y1, ok, bar, h.a.paneAt.Y, h.a.composerAt.Y)
		}
		h.tt.Key(0, ui.KeyEscape)
		h.tt.ClickAt(1100, 300)
		h.tt.Advance(time.Second)
	}
}

// With the diff panel beside the chat, the bar of a selection at the
// reply's right edge stays in the chat, off the panel.
func TestSelectionBarInChatColumn(t *testing.T) {
	h := replyReady(t, "Start here. "+strings.Repeat("word ", 300)+"\n\nOutro here.", "Outro here.")
	h.click("Toggle diff  ⌘D")
	h.tt.Advance(2 * time.Second)
	h.a.listFor(h.a.current().id()).ScrollTo(0, ui.Start)
	h.tt.Advance(time.Second)
	para := startsWith(h, "Start here.")
	y := para.Y + 30
	drag(h, para.X+para.W-120, y, para.X+para.W-4, y)
	b, ok := barAt(h)
	if !ok {
		t.Fatalf("no bar: %+v", h.a.quote)
	}
	if pane := h.a.paneAt; b.X < pane.X || b.X+b.W > pane.X+pane.W+0.5 {
		t.Errorf("the bar at x %v to %v; want it in the chat's pane, x %v to %v, off the diff panel", b.X, b.X+b.W, pane.X, pane.X+pane.W)
	}
}

// A word's bar shown under it keeps off it as the chat's column narrows
// and the reply rewraps, moving the word down: as the diff panel opens
// beside the chat, as the sidebar shows again, as the window narrows.
func TestSelectionBarClearOfSelectionAfterRewrap(t *testing.T) {
	key := func(k ui.Key) func(h *harness) {
		return func(h *harness) { h.tt.Key(ui.Cmd, k); h.tt.HoldModifiers(0) }
	}
	for _, tc := range []struct {
		name          string
		repeat        int
		widen, narrow func(h *harness)
	}{
		{"the diff panel", 10, nil, key(ui.KeyD)},
		{"the sidebar", 20, func(h *harness) { h.tt.SetSize(1000, 860); key(ui.KeyB)(h) }, key(ui.KeyB)},
		{"the window", 22, nil, func(h *harness) { h.tt.SetSize(900, 860) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			long := "Alpha " + strings.Repeat("lorem ipsum dolor sit amet consectetur ", tc.repeat) + "omega."
			h := replyReady(t, long+"\n\nTarget words here.\n\nOutro here.", "Outro here.")
			if tc.widen != nil {
				tc.widen(h)
				h.tt.Advance(time.Second)
			}
			doubleClick(h, "Target words here.")
			h.tt.Move(1, 1)
			h.tt.Advance(time.Second)
			if _, ok := barAt(h); !ok || h.a.quote.text != "Target" {
				t.Fatalf("double-clicked Target: bar %v on %q", ok, h.a.quote.text)
			}
			// How far down the reply the selected line is.
			down := func() (ui.Rect, float32) {
				line, _ := h.tt.Find("Target words here.")
				return line, line.Y - startsWith(h, "Alpha ").Y
			}
			_, was := down()
			tc.narrow(h)
			h.tt.Advance(2 * time.Second)
			line, now := down()
			if now <= was {
				t.Fatalf("the selected line went from %v to %v down the reply; want the reply rewrapped over more lines", was, now)
			}
			if b, ok := barAt(h); ok && meets(b, line) {
				t.Errorf("rewrapped, the bar (y %v to %v) lies over the selected line (y %v to %v)", b.Y, b.Y+b.H, line.Y, line.Y+line.H)
			}
		})
	}
}

// wideThread is the app on a thread of n turns, each replied to with a
// paragraph of long lines.
func wideThread(t *testing.T, n int) *harness {
	t.Helper()
	para := "Alpha " + strings.Repeat("lorem ipsum dolor sit amet ", 24) + "omega."
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New([]provider.Event{provider.TextDelta{Text: para}}))
	var msgs []string
	for i := 1; i <= n; i++ {
		msgs = append(msgs, fmt.Sprintf("Step %d.", i))
	}
	seed(t, d, "/work/t3code", "Wide replies", msgs...)
	h := start(t, dir)
	h.a.select_(h.thread("Wide replies"))
	last := fmt.Sprintf("Step %d.", n)
	h.untilShown("its transcript", last)
	h.tt.Advance(time.Second)
	return h
}

// In use, the bar of a selection scrolled so far away that its reply
// isn't built, held at the timeline's edge, stays in the chat's pane as
// the pane narrows: as the diff panel opens by a bar at the right of the
// chat, as the sidebar shows by one at its left.
func TestHeldSelectionBarInPane(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int
		key   ui.Key
		// at is where on a line of the reply, r, the word selected is.
		at func(r ui.Rect) float32
	}{
		{"the diff panel", 1320, ui.KeyD, func(r ui.Rect) float32 { return r.X + r.W - 60 }},
		{"the sidebar", 1000, ui.KeyB, func(r ui.Rect) float32 { return r.X + 10 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := wideThread(t, 10)
			h.tt.SetSize(tc.width, 860)
			if tc.key == ui.KeyB {
				h.tt.Key(ui.Cmd, ui.KeyB)
				h.tt.HoldModifiers(0)
			}
			h.tt.Advance(time.Second)
			h.a.listFor(h.a.current().id()).ScrollTo(0, ui.Start)
			h.tt.Advance(time.Second)
			r := startsWith(h, "Alpha ")
			doubleClickAt(h, tc.at(r), r.Y+10)
			h.click("Ask about the selection")
			h.tt.Advance(200 * time.Millisecond)
			h.tt.Type("what")
			wheel(h, 2400)
			if _, ok := barAt(h); !ok || !h.tt.Focused("Ask about the selection") {
				t.Fatalf("typed in, the bar of a selection scrolled away: shown %v, its box focused %v", ok, h.tt.Focused("Ask about the selection"))
			}
			h.tt.Key(ui.Cmd, tc.key)
			h.tt.HoldModifiers(0)
			h.tt.Advance(2 * time.Second)
			b, ok := barAt(h)
			if pane := h.a.paneAt; !ok || b.X < pane.X || b.X+b.W > pane.X+pane.W {
				t.Errorf("%s shown, the bar: shown %v at x %v to %v; want it in the chat's pane, x %v to %v", tc.name, ok, b.X, b.X+b.W, pane.X, pane.X+pane.W)
			}
		})
	}
}

// Scrolled up, the bar of a word just over Jump to latest keeps off it,
// which a click then takes to the end, quoting nothing.
func TestSelectionBarLeavesJumpToLatest(t *testing.T) {
	var md strings.Builder
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&md, "Paragraph %02d of a long reply that goes on.\n\n", i)
	}
	h := replyReady(t, md.String()+"Outro here.", "Outro here.")
	wheel(h, -400)
	jump, ok := h.tt.Find("Jump to latest")
	if !ok {
		t.Fatalf("scrolled up, no Jump to latest: %q", h.tt.Texts())
	}
	// The lowest paragraph wholly over the pill.
	var para ui.Rect
	for _, s := range h.tt.Texts() {
		if r, ok := h.tt.Find(s); ok && strings.HasPrefix(s, "Paragraph ") && r.Y+r.H < jump.Y-4 && r.Y > para.Y {
			para = r
		}
	}
	doubleClickAt(h, para.X+200, para.Y+para.H/2)
	h.tt.Move(1, 1)
	h.tt.Advance(time.Second)
	if !barShown(h) {
		t.Fatalf("a word double-clicked over Jump to latest, no bar: %+v", h.a.quote)
	}
	h.tt.ClickAt(jump.X+jump.W/2, jump.Y+jump.H/2)
	h.tt.Advance(2 * time.Second)
	if slices.ContainsFunc(h.a.attachments, func(at attachment) bool { return at.quote }) || h.tt.HasText("Jump to latest") {
		t.Errorf("Jump to latest clicked by the bar shown unasked: attachments %+v, Jump to latest shown %v; want the chat at its end, nothing quoted",
			h.a.attachments, h.tt.HasText("Jump to latest"))
	}
}

// askAfterReply is a turn that replies, then asks to run a command.
var askAfterReply = []provider.Event{
	provider.TextDelta{Text: "May I run the tests now?"},
	provider.RequestOpened{ID: "run-1", Kind: "approval", Body: []byte(`{"operation":"run","command":"bun run test","cwd":"/work/t3code","reason":"Runs the tests."}`)},
	provider.TextDelta{Text: " Ran."},
}

// Shown unasked by a selection just over a waiting approval card, the bar
// keeps off the card, which ⌘⏎ and Allow once answer at the first press.
func TestSelectionBarLeavesApprovalCard(t *testing.T) {
	for name, answer := range map[string]func(h *harness){
		"⌘⏎":         func(h *harness) { h.tt.Key(ui.Cmd, ui.KeyEnter); h.tt.HoldModifiers(0) },
		"Allow once": func(h *harness) { h.click("Allow once") },
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := asking(t, askAfterReply)
			h.tt.Advance(time.Second)
			doubleClick(h, "May I run the tests now?")
			h.tt.Move(300, 60)
			h.tt.Advance(2 * time.Second)
			b, ok := barAt(h)
			if !ok {
				t.Fatalf("no bar: %+v", h.a.quote)
			}
			answer(h)
			h.tt.Frame()
			if !answering(h, lastRequest(h.a.current())) {
				hd, _ := h.tt.Find("Approval needed")
				t.Errorf("the bar at %v, the card's header at %v: %s didn't answer the card", b, hd, name)
			}
		})
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

// A quote carried to a new thread, ⌘N, goes with its first message, which
// titles the thread with the draft rather than the quote.
func TestQuoteInNewThreadTitledByDraft(t *testing.T) {
	h, sel := quoteReady(t)
	from := h.a.current().id()
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	h.click("Quote")
	h.tt.Advance(time.Second)
	h.tt.Key(ui.Cmd, ui.KeyN)
	h.tt.Advance(time.Second)
	h.tt.Type("Fix the flicker")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the new thread", func() bool { id := h.a.current().id(); return !id.IsZero() && id != from })
	if got := h.a.current().View.Title; got != "Fix the flicker" {
		t.Errorf("the new thread is titled %q; want the draft's %q", got, "Fix the flicker")
	}
	want := "> " + sel + "\n\nFix the flicker"
	h.until("the message with its quote", func() bool { return slices.Contains(sentTexts(h), want) })
}

// A thread started with a quote is titled as its message would be
// without the quote: the draft, and what's attached after it.
func TestQuoteInNewThreadTitledWithAttachment(t *testing.T) {
	h, sel := quoteReady(t)
	from := h.a.current().id()
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	h.click("Quote")
	h.tt.Advance(time.Second)
	h.tt.Key(ui.Cmd, ui.KeyN)
	h.tt.Advance(time.Second)
	h.a.attachments = append(h.a.attachments, attachment{name: "Pasted text", text: "some pasted text"})
	h.tt.Type("Fix it")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the new thread", func() bool { id := h.a.current().id(); return !id.IsZero() && id != from })
	if got, want := h.a.current().View.Title, "Fix it\n\nsome pasted text"; got != want {
		t.Errorf("the new thread is titled %q; want %q, its message without the quote", got, want)
	}
	want := "> " + sel + "\n\nFix it\n\nsome pasted text"
	h.until("the message with its quote", func() bool { return slices.Contains(sentTexts(h), want) })
}

// With the draft empty, a quote goes with what's attached, a blank line
// between them.
func TestQuoteSentWithoutDraft(t *testing.T) {
	h, sel := quoteReady(t)
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	h.click("Quote")
	h.tt.Advance(time.Second)
	h.a.attachments = append(h.a.attachments, attachment{name: "Pasted text", text: "pasted"})
	h.tt.Frame()
	n := len(sentTexts(h))
	h.click("Send")
	h.until("the message", func() bool { return len(sentTexts(h)) > n })
	if got, want := sentTexts(h)[n], "> "+sel+"\n\npasted"; got != want {
		t.Errorf("sent %q, want %q", got, want)
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

// While any of what's selected shows in the timeline, its bar shows there
// too, however the chat scrolls; once all of it has gone well past an
// edge, the bar has, unused.
func TestSelectionBarInTimelineWithSelection(t *testing.T) {
	para := "Alpha " + strings.Repeat("lorem ipsum dolor sit amet ", 40) + "omega."
	var fill strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&fill, "Filler paragraph %02d.\n\n", i)
	}
	h := replyReady(t, fill.String()+para+"\n\n"+fill.String()+"Outro here.", "Outro here.")
	r := startsWith(h, "Alpha ")
	for range 40 {
		if r.H > 0 && r.Y+r.H/2 >= h.a.paneAt.Y+h.a.paneAt.H/2-100 {
			break
		}
		wheel(h, -200)
		r = startsWith(h, "Alpha ")
	}
	drag(h, r.X+2, r.Y+10, r.X+r.W-2, r.Y+r.H-10)
	h.tt.Move(1, 1)
	h.tt.Advance(time.Second)
	top, bottom := h.a.paneAt.Y, h.a.composerAt.Y
	for _, dy := range []float32{40, -40} {
		// The selection's box as shown, cut to the chat's pane: none once
		// it has gone past an edge; gone counts the notches since.
		seen, gone := false, 0
		for range 80 {
			wheel(h, dy)
			r = startsWith(h, "Alpha ")
			b, shown := barAt(h)
			switch in := r.H > 0 && r.Y+r.H > top+2 && r.Y < bottom-2; {
			case in && !shown:
				t.Fatalf("scrolled by %v, the selection at y %v to %v shows in the timeline (%v to %v), no bar", dy, r.Y, r.Y+r.H, top, bottom)
			case shown && !inTimeline(h, b):
				t.Fatalf("scrolled by %v, the bar at y %v to %v, past the timeline (%v to %v)", dy, b.Y, b.Y+b.H, top, bottom)
			case shown && gone >= 3:
				t.Fatalf("scrolled by %v, the selection gone from the pane %d notches ago, the bar shows unused", dy, gone)
			}
			seen = seen || r.H > 0
			if seen && r.H == 0 {
				gone++
			}
			if gone > 3 {
				break
			}
		}
		if gone <= 3 {
			t.Fatalf("scrolled by %v, the selection never went past the edge", dy)
		}
	}
}

// In use, the bar stays as the chat scrolls the selection away, held at
// the timeline's edge, taking what's typed; let be, it goes with the
// selection, and comes back with the reply, its question kept, though the
// chat scrolled so far that the reply went, and its selection with it.
func TestSelectionBarStaysOnScroll(t *testing.T) {
	h, th := longHarness(t, nil)
	h.a.listFor(th.id()).ScrollTo(0, ui.Start)
	h.tt.Advance(time.Second)
	doubleClick(h, "Looking at the code.")
	h.click("Ask about the selection")
	h.tt.Advance(200 * time.Millisecond)
	h.tt.Type("what does this")
	wheel(h, 720)
	if b, ok := barAt(h); !ok || !inTimeline(h, b) {
		t.Fatalf("typed in, the bar of a selection scrolled away: shown %v at %v; want it held in the timeline (%v to %v)", ok, b, h.a.paneAt.Y, h.a.composerAt.Y)
	}
	h.tt.Type(" mean")
	for range 3 { // past its buttons
		h.tt.Key(0, ui.KeyTab)
	}
	h.tt.Advance(time.Second)
	if barShown(h) {
		t.Error("let be, the bar of a selection scrolled away still shows")
	}
	wheel(h, 2400)
	wheel(h, -2400-720-240)
	if _, ok := h.tt.Find("Looking at the code."); !ok || !barShown(h) {
		t.Fatalf("scrolled far and back: the reply shows %v, the bar %v; want both", ok, barShown(h))
	}
	h.click("Ask about the selection")
	h.tt.Key(0, ui.KeyEnter)
	want := "> Looking\n\nwhat does this mean"
	h.until("the question sent", func() bool { return slices.Contains(sentTexts(h), want) })
}

// paced is an adapter whose agents' text comes a piece every few
// milliseconds, as a model's does, rather than all at once.
type paced struct{ provider.Adapter }

func (p paced) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	g, err := p.Adapter.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return pacedAgent{g}, nil
}

type pacedAgent struct{ provider.Agent }

func (g pacedAgent) Next(ctx context.Context) (provider.Event, error) {
	e, err := g.Agent.Next(ctx)
	if _, ok := e.(provider.TextDelta); ok {
		time.Sleep(20 * time.Millisecond)
	}
	return e, err
}

// The agent, allowed from elsewhere, works on, a command and a line at a
// time, as a question is typed in the bar of its earlier words: the
// chat's list builds the reply's row as it lays out, after the view, and
// the bar takes what's typed throughout.
func TestSelectionBarAsAgentWorksOn(t *testing.T) {
	script := []provider.Event{
		provider.TextDelta{Text: "May I run the tests now?"},
		provider.RequestOpened{ID: "run-1", Kind: "approval", Body: []byte(`{"operation":"run","command":"bun run test","cwd":"/work/t3code","reason":"Runs the tests."}`)},
	}
	for i := 1; i <= 30; i++ {
		item := fmt.Sprintf("w%d", i)
		script = append(script,
			provider.Work{Item: item, Kind: "command", Title: "bun run test", Status: api.WorkStarted},
			provider.Work{Item: item, Kind: "command", Title: "bun run test", Status: api.WorkCompleted, Exit: new(0)},
			provider.TextDelta{Text: fmt.Sprintf("Line %d of what the agent found.", i)},
		)
	}
	dir := tempDir(t)
	engine(t, daemon.Config{Dir: dir}, paced{fake.New(script)})
	h := start(t, dir)
	h.a.draft = "Run the sidebar tests."
	h.tt.Frame()
	h.click("Send")
	h.until("the approval card", func() bool { return h.tt.HasText("Approval needed") })
	h.tt.Advance(time.Second)
	th := h.a.current()
	doubleClick(h, "May I run the tests now?")
	h.click("Ask about the selection")
	h.tt.Advance(300 * time.Millisecond)
	h.tt.Type("wh")
	// Allowed from another window of the app, or a phone.
	h.a.backend.Respond(th.env, th.id(), lastRequest(th).Request, client.AllowOnce)
	lines := func() int { return strings.Count(texts(entriesOf(h.a.current())), "of what the agent found.") }
	h.until("the turn's end", func() bool {
		h.tt.Advance(16 * time.Millisecond)
		return !h.a.current().View.Working() && lines() == 30
	})
	h.tt.Advance(time.Second)
	h.tt.Type("at?")
	h.tt.Frame()
	if h.a.quote.ask != "what?" {
		t.Errorf("typed on as the agent worked: the bar's box holds %q, the composer's draft %q; want %q in the box", h.a.quote.ask, h.a.draft, "what?")
	}
}

// A question half typed in the bar, a little way up from the thread's
// end, goes on in the bar as the chat is scrolled back to the end and the
// composer opens from its line, and goes with its quote.
func TestSelectionBarKeepsFocusAsComposerOpens(t *testing.T) {
	var md strings.Builder
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&md, "Paragraph %02d of a long reply that goes on.\n\n", i)
	}
	h := replyReady(t, md.String()+"Outro here.", "Outro here.")
	th := h.a.current()
	wheel(h, -240)
	if !h.a.composerCompact(th) {
		t.Fatal("scrolled up from the end, the composer is open; want it folded to its line")
	}
	// A paragraph that shows at the end too.
	var para string
	for _, s := range h.tt.Texts() {
		if r, ok := h.tt.Find(s); ok && strings.HasPrefix(s, "Paragraph ") && r.Y > h.a.paneAt.Y+400 && r.Y < h.a.paneAt.Y+440 {
			para = s
		}
	}
	if para == "" {
		t.Fatalf("no paragraph there: %q", h.tt.Texts())
	}
	doubleClick(h, para)
	h.click("Ask about the selection")
	h.tt.Advance(200 * time.Millisecond)
	h.tt.Type("what does this")
	wheel(h, 240)
	if h.a.composerCompact(th) {
		t.Fatal("scrolled back to the end, the composer is still folded")
	}
	h.tt.Type(" mean")
	h.tt.Frame()
	if h.a.quote.ask != "what does this mean" || h.a.draft != "" {
		t.Fatalf("typed on after the composer opened: the bar's box holds %q, the draft %q; want the box to hold %q, the draft empty", h.a.quote.ask, h.a.draft, "what does this mean")
	}
	h.tt.Key(0, ui.KeyEnter)
	want := "> Paragraph\n\nwhat does this mean"
	h.until("the question sent", func() bool { return slices.Contains(sentTexts(h), want) })
}

// The chat with a waiting card left and come back to many times keeps as
// many handles bound in the window as it did: MyGo keeps a window's
// bindings until it closes, so a card that shows again binds no new one.
func TestWaitingCardShownAgainBindsNoHandle(t *testing.T) {
	h, th := asking(t, fake.Turn(true))
	// The window's bindings, which no API counts.
	refs := func() int { return reflect.ValueOf(h.tt).Elem().FieldByName("rt").Elem().FieldByName("refs").Len() }
	h.tt.Advance(time.Second)
	before := refs()
	for range 20 {
		h.click("Settings")
		h.tt.Advance(300 * time.Millisecond)
		h.a.select_(th)
		h.tt.Advance(300 * time.Millisecond)
	}
	if !h.tt.HasText("Approval needed") {
		t.Fatalf("back on the thread, no waiting card: %q", h.tt.Texts())
	}
	if after := refs(); after > before {
		t.Errorf("the window's handle bindings went from %d to %d as the chat with a waiting card was left and come back to 20 times; want no more", before, after)
	}
}

// With a code block in the draft, Explain asks in the text under it, and
// the code and the ask go together.
func TestExplainWithACodeBlockAsksUnderIt(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)), "go")
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("x := 1")
	h.tt.Frame()
	if !h.a.code.on {
		t.Fatal("no block opened")
	}
	sel := dragHalfReply(t, h)
	h.tt.Advance(quoteWait + 50*time.Millisecond)
	h.click("Explain")
	h.tt.Advance(time.Second)
	if got := h.a.code.segs[len(h.a.code.segs)-1].text; got != explainAsk {
		t.Fatalf("the text under the block %q, want %q", got, explainAsk)
	}
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	want := "> " + sel + "\n\n```\nx := 1\n```\n" + explainAsk
	h.until("the quote, the code and the ask sent", func() bool { return slices.Contains(sentTexts(h), want) })
}
