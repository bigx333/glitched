package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider/fake"

	"github.com/egoist/mygo/ui"
)

// ``` and Enter open a Plain text code block, as T3 Code's composer does:
// in it Enter breaks the line rather than send, and ⌘⏎ sends the code
// fenced, closing the block.
func TestBackticksEnterOpenACodeBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if !h.a.code.on || tagOf(h.a) != "" || draftOf(h.a) != "" {
		t.Fatalf("after ``` Enter: block %+v, draft %q; want an empty Plain text block", h.a.code, draftOf(h.a))
	}
	if !h.tt.HasText("Plain text") {
		t.Errorf("no Plain text picker: %q", h.tt.Texts())
	}
	// The empty block shows no placeholder, the draft's not left over: the
	// line under its picker is all one color.
	if r, ok := h.tt.Find("Plain text"); ok {
		img := h.tt.Image()
		y0, y1 := int(r.Y+r.H)+6, int(r.Y+r.H)+22
		bg := img.RGBAAt(int(r.X), y0)
		for y := y0; y < y1; y++ {
			for x := int(r.X); x < int(r.X)+400; x++ {
				if p := img.RGBAAt(x, y); absDiff(p.R, bg.R)+absDiff(p.G, bg.G)+absDiff(p.B, bg.B) > 30 {
					t.Fatalf("the empty block shows text at %d,%d: the draft's placeholder", x, y)
				}
			}
		}
	}
	h.tt.Type("  a := 1")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("b")
	h.tt.Frame()
	if draftOf(h.a) != "  a := 1\nb" || len(sentTexts(h)) != 0 {
		t.Fatalf("Enter in the block: draft %q, sent %q; want the line broken, nothing sent", draftOf(h.a), sentTexts(h))
	}
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.until("the block", func() bool { return slices.Contains(sentTexts(h), "```\n  a := 1\nb\n```") })
	if h.a.code.on || draftOf(h.a) != "" {
		t.Errorf("after sending: block %+v, draft %q; want it closed and cleared", h.a.code, draftOf(h.a))
	}
}

// A language after the ``` is the block's, as its fence's tag; the picker
// changes it.
func TestCodeBlockLanguage(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```go")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if tagOf(h.a) != "go" || !h.tt.HasText("Go") {
		t.Fatalf("after ```go Enter: block %+v; want Go", h.a.code)
	}
	h.click("Go")
	if err := h.tt.ChooseMenuItem("Python"); err != nil {
		t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.tt.Frame()
	h.tt.Type("print(1)")
	h.tt.Frame()
	h.click("Send")
	h.until("the block", func() bool { return slices.Contains(sentTexts(h), "```python\nprint(1)\n```") })
}

// Backspace in the empty block closes it, and Enter sends again.
func TestBackspaceClosesTheEmptyCodeBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("x")
	h.tt.Key(0, ui.KeyBackspace)
	h.tt.Frame()
	if !h.a.code.on {
		t.Fatal("Backspace over the block's last letter closed it")
	}
	h.tt.Key(0, ui.KeyBackspace)
	h.tt.Frame()
	if h.a.code.on || h.tt.HasText("Plain text") {
		t.Fatalf("Backspace in the empty block: block %+v; want it closed", h.a.code)
	}
	h.tt.Type("hi")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the message", func() bool { return slices.Contains(sentTexts(h), "hi") })
}

// An empty block keeps the composer open as it scrolls away, and
// holds the approval's keys, as a draft does.
func TestEmptyCodeBlockIsADraft(t *testing.T) {
	a := offline(t)
	a.code = blocks(a, "")
	if !a.drafting() {
		t.Error("an empty code block isn't drafting")
	}
}

// A fence open in the draft, as pasted in, isn't sent half open: Enter
// breaks the line until it's closed.
func TestEnterInAnUnclosedFenceBreaksTheLine(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("look:\n```\nx")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Frame()
	if h.a.code.on || draftOf(h.a) != "look:\n```\nx\n```" || len(sentTexts(h)) != 0 {
		t.Fatalf("draft %q, block %+v, sent %q; want the fence typed out, nothing sent", draftOf(h.a), h.a.code, sentTexts(h))
	}
	h.tt.Key(0, ui.KeyEnter)
	h.until("the message", func() bool { return slices.Contains(sentTexts(h), "look:\n```\nx\n```") })
}

// While code, the draft brings up no suggestions: / and @ are code.
func TestCodeBlockSuggestsNothing(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("/mod")
	h.tt.Advance(100 * time.Millisecond)
	if len(h.a.suggestions) != 0 {
		t.Errorf("suggestions in a code block: %v", h.a.suggestions)
	}
}

func TestFenced(t *testing.T) {
	for _, c := range []struct{ tag, code, want string }{
		{"", "x", "```\nx\n```"},
		{"go", "\n\n\tfunc f() {}\n\n", "```go\n\tfunc f() {}\n```"},
		{"md", "a\n```\nb", "````md\na\n```\nb\n````"},
		{"", " \n ", ""},
	} {
		if got := fenced(c.tag, c.code); got != c.want {
			t.Errorf("fenced(%q, %q) = %q, want %q", c.tag, c.code, got, c.want)
		}
	}
}

func TestOpeningFence(t *testing.T) {
	for _, c := range []struct {
		draft, tag string
		ok         bool
	}{
		{"```", "", true},
		{"```Go ", "go", true},
		{"```ts", "typescript", true},
		{"```text", "", true},
		{"```zig", "zig", true},
		{"hi ```", "", false},
		{"```go x", "", false},
		{"``` go", "go", true},
		{"````go", "go", true},
		{"``", "", false},
	} {
		if tag, ok := fenceTag(c.draft); tag != c.tag || ok != c.ok {
			t.Errorf("fenceTag(%q) = %q, %v; want %q, %v", c.draft, tag, ok, c.tag, c.ok)
		}
	}
	if codeLanguageName("zig") != "zig" || codeLanguageName("") != "Plain text" {
		t.Error("codeLanguageName")
	}
}

func TestUnclosedFence(t *testing.T) {
	for _, c := range []struct {
		draft string
		open  bool
	}{
		{"see:\n```", true},
		{"see:\n```go\nx\n```", false},
		{"see:\n```foo```", false},
		{"````md\n```go\nx\n````", false},
		{"````md\n```go\nx\n```", true},
		{"a\n``` x", true},
	} {
		if got := unclosedFence(c.draft); got != c.open {
			t.Errorf("unclosedFence(%q) = %v, want %v", c.draft, got, c.open)
		}
	}
}

// A long paste in a code block is its code, not an attachment: it goes
// out fenced with the block's language.
func TestLongPasteInACodeBlockIsCode(t *testing.T) {
	a := offline(t)
	a.code = blocks(a, "go")
	if a.onPasteIn(a.code.segs[1])(strings.Repeat("x\n", longPaste)) || len(a.attachments) != 0 {
		t.Errorf("a long paste in a code block became an attachment: %d", len(a.attachments))
	}
}

// Undo right after Backspace closed the block, under text, keeps that
// text: the draft's undo doesn't reach back into the code.
func TestUndoAfterTheBlockClosesKeepsTheText(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("how does")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.tt.Type("x")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyBackspace)
	h.tt.Key(0, ui.KeyBackspace)
	h.tt.Frame()
	if h.a.code.on || draftOf(h.a) != "how does" {
		t.Fatalf("Backspace in the empty block: block %+v, draft %q; want it closed on how does", h.a.code, draftOf(h.a))
	}
	h.tt.Key(ui.Cmd, ui.KeyZ)
	h.tt.Frame()
	if !strings.Contains(draftOf(h.a), "how does") {
		t.Errorf("undo after the block closed: draft %q; want how does kept", draftOf(h.a))
	}
}

// A thread started from a code block takes its title from the code's first
// line, not the fence.
func TestCodeBlockTitlesTheThreadByItsCode(t *testing.T) {
	h := start(t, inboxDir(t))
	h.click("New thread")
	h.tt.Advance(time.Second)
	h.tt.Type("```go")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("func main() {}")
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.tt.Frame()
	if !h.a.inSidebar("func main() {}") {
		t.Errorf("the thread's title: %q", h.a.current().View.Title)
	}
}

// Closing the language menu without a choice gives the focus back to the
// code, as choosing does.
func TestCodeLanguageMenuGivesTheFocusBack(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.click("Plain text")
	h.tt.CloseMenu()
	h.tt.Frame()
	h.tt.Type("b")
	h.tt.Frame()
	if draftOf(h.a) != "b" {
		t.Errorf("typed after closing the menu: draft %q; want %q", draftOf(h.a), "b")
	}
}

// A long paste in a fence typed by hand is its code, not an attachment.
func TestLongPasteInAnUnclosedFenceIsCode(t *testing.T) {
	a := offline(t)
	a.draft = "look:\n```\n"
	if a.onPaste(strings.Repeat("x\n", longPaste)) || len(a.attachments) != 0 {
		t.Errorf("a long paste in an open fence became an attachment: %d", len(a.attachments))
	}
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// The language picker is there to the keyboard: Shift+Tab from the code
// reaches it, and Enter opens its menu.
func TestCodeLanguagePickerTakesTheKeyboard(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.tt.Key(ui.Shift, ui.KeyTab)
	h.tt.Frame()
	if !h.tt.Focused("Code block language") {
		t.Fatal("Shift+Tab from the code didn't reach the language picker")
	}
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if !slices.Contains(h.tt.Menu(), "Python") || draftOf(h.a) != "" {
		t.Errorf("Enter on the picker: menu %q, draft %q; want the menu open", h.tt.Menu(), draftOf(h.a))
	}
}

// A paste too long to edit goes as an attachment, fenced with the block's
// language.
func TestHugePasteInACodeBlockAttachesFenced(t *testing.T) {
	a := offline(t)
	a.code = blocks(a, "go")
	text := strings.Repeat("x\n", longCodePaste)
	if !a.onPasteIn(a.code.segs[1])(text) || len(a.attachments) != 1 || a.attachments[0].text != fenced("go", text) {
		t.Errorf("a huge paste in a code block: %d attachments", len(a.attachments))
	}
}

// A thread started from an empty block with an attachment keeps the
// message as its title, rather than none.
func TestEmptyCodeBlockDoesntBlankTheTitle(t *testing.T) {
	h := start(t, inboxDir(t))
	h.click("New thread")
	h.tt.Advance(time.Second)
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.a.attachments = append(h.a.attachments, attachment{name: "Pasted text", text: "notes here"})
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.tt.Frame()
	if !h.a.inSidebar("notes here") {
		t.Errorf("the thread's title: %q", h.a.current().View.Title)
	}
}

// An Enter that never comes to the composer's keys, as an input method's
// whose line the text area breaks itself, opens the block all the same.
func TestBackticksAndANewLineOpenACodeBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```go")
	h.tt.Type("\n")
	h.tt.Frame()
	h.tt.Type("x")
	h.tt.Frame()
	if !h.a.code.on || tagOf(h.a) != "go" || draftOf(h.a) != "x" {
		t.Errorf("after ```go and a new line: block %+v, draft %q; want a Go block holding x", h.a.code, draftOf(h.a))
	}
}

// Shift+Enter after ``` opens the block too, as the way to the next line
// a draft takes.
func TestBackticksShiftEnterOpenACodeBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```py")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("x")
	h.tt.Frame()
	if !h.a.code.on || tagOf(h.a) != "python" || draftOf(h.a) != "x" {
		t.Errorf("after ```py Shift+Enter: block %+v, draft %q; want a Python block holding x", h.a.code, draftOf(h.a))
	}
}

// ``` on a line of its own after other text opens the block there: the
// text stays over it, and goes ahead of the code.
func TestBackticksAfterTextOpenACodeBlock(t *testing.T) {
	for _, key := range []ui.Modifiers{0, ui.Shift} {
		h := enterReady(t, fake.New(fake.Turn(false)))
		h.tt.Type("how does")
		h.tt.Key(ui.Shift, ui.KeyEnter)
		h.tt.Type("```")
		h.tt.Key(key, ui.KeyEnter)
		h.tt.Type("text.split")
		h.tt.Advance(100 * time.Millisecond)
		if !h.a.code.on || beforeOf(h.a) != "how does" || !proseOf(h.a) || draftOf(h.a) != "text.split" {
			t.Fatalf("mods %v: block %+v, draft %q; want a block under how does, holding text.split", key, h.a.code, draftOf(h.a))
		}
		h.tt.Key(ui.Cmd, ui.KeyEnter)
		h.until("the message", func() bool { return slices.Contains(sentTexts(h), "how does\n```\ntext.split\n```") })
		if h.a.code.on || beforeOf(h.a) != "" {
			t.Errorf("after sending: block %+v; want it closed", h.a.code)
		}
	}
}

// Backspace in the empty block under text puts the text back in the
// draft, as it was.
func TestBackspaceGivesBackTheTextAboveTheBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("how does")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Key(0, ui.KeyBackspace)
	h.tt.Type("!")
	h.tt.Frame()
	if h.a.code.on || draftOf(h.a) != "how does!" {
		t.Errorf("Backspace in the empty block: block %+v, draft %q; want it closed, the text back", h.a.code, draftOf(h.a))
	}
}

// Enter in the text over the block sends it all, as Enter in a draft.
func TestEnterInTheTextAboveTheBlockSends(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("see")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("```go")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("x := 1")
	h.tt.Frame()
	// The text over the block: just above the block's picker.
	r, ok := h.tt.Find("Go")
	if !ok {
		t.Fatalf("no picker: %q", h.tt.Texts())
	}
	h.tt.ClickAt(r.X+4, r.Y-22)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.until("the message", func() bool { return slices.Contains(sentTexts(h), "see\n```go\nx := 1\n```") })
}

func TestFenceLine(t *testing.T) {
	end := -1
	for _, c := range []struct {
		text        string
		caret       int
		before, tag string
		after       string
		ok          bool
	}{
		{"```", end, "", "", "", true},
		{"how does\n```", end, "how does", "", "", true},
		{"a\n\n```ts", end, "a", "typescript", "", true},
		{"intro\n```\noutro", 9, "intro", "", "outro", true},
		{"```go  \nx", 5, "", "go", "x", true},
		// The caret not at the end of the fence's line.
		{"intro\n```outro", 9, "", "", "", false},
		{"fix this\n```", 3, "", "", "", false},
		// In a fence typed over it, or closed under it.
		{"```\nx\n```", end, "", "", "", false},
		{"```\nx\n```", 3, "", "", "", false},
		{"a\n```x y", end, "", "", "", false},
	} {
		caret := c.caret
		if caret == end {
			caret = len([]rune(c.text))
		}
		before, tag, after, ok := fenceLine(c.text, caret)
		if before != c.before || tag != c.tag || after != c.after || ok != c.ok {
			t.Errorf("fenceLine(%q, %d) = %q, %q, %q, %v; want %q, %q, %q, %v", c.text, caret, before, tag, after, ok, c.before, c.tag, c.after, c.ok)
		}
	}
}

// Down from the code's last line goes under the block, to text of its own,
// and Up from there comes back; Up from the code's first line goes over
// it. The text goes around the code, in order.
func TestArrowsLeaveTheCodeBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("a")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("b")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	h.tt.Type("after")
	h.tt.Frame()
	if draftOf(h.a) != "a\nb" || afterOf(h.a) != "after" {
		t.Fatalf("Down from the last line: draft %q, block %+v; want the text under the block", draftOf(h.a), h.a.code)
	}
	h.tt.Key(0, ui.KeyUp)
	h.tt.Frame()
	h.tt.Type("c")
	h.tt.Frame()
	if draftOf(h.a) != "a\nbc" {
		t.Fatalf("Up from the text under: draft %q; want the caret back at the code's end", draftOf(h.a))
	}
	h.tt.Key(0, ui.KeyUp) // to the code's first line
	h.tt.Frame()
	h.tt.Key(0, ui.KeyUp) // and over the block
	h.tt.Frame()
	h.tt.Type("before")
	h.tt.Frame()
	if beforeOf(h.a) != "before" {
		t.Fatalf("Up from the first line: block %+v; want the text over the block", h.a.code)
	}
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.until("the message", func() bool { return slices.Contains(sentTexts(h), "before\n```\na\nbc\n```\nafter") })
}

// Backspace in the empty text under the block takes it away, back to the
// code.
func TestBackspaceTakesAwayTheTextUnderTheBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("a")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyBackspace)
	h.tt.Frame()
	h.tt.Type("b")
	h.tt.Frame()
	if belowOf(h.a) || draftOf(h.a) != "ab" {
		t.Errorf("Backspace under the block: block %+v, draft %q; want it gone, the caret back in the code", h.a.code, draftOf(h.a))
	}
}

// On a code line long enough to wrap, Up goes up a row, not out of the
// block.
func TestUpOnAWrappedLineStaysInTheBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type(strings.Repeat("word ", 80))
	h.tt.Frame()
	h.tt.Key(0, ui.KeyUp)
	h.tt.Frame()
	h.tt.Type("X")
	h.tt.Frame()
	if proseOf(h.a) || !strings.Contains(draftOf(h.a), "X") {
		t.Errorf("Up on a wrapped line: block %+v, draft %q; want the caret up a row in the code", h.a.code, draftOf(h.a))
	}
}

// Enter from the text under the block sends, and the focus comes back to
// the draft: what's typed next goes in it.
func TestSendingFromTheTextUnderTheBlockKeepsTheFocus(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("a")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	h.tt.Type("after")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the message", func() bool { return slices.Contains(sentTexts(h), "```\na\n```\nafter") })
	h.tt.Frame()
	h.tt.Type("next")
	h.tt.Frame()
	if draftOf(h.a) != "next" {
		t.Errorf("typed after sending: draft %q; want %q", draftOf(h.a), "next")
	}
}

// A long paste into the text over the block is prose: an attachment, not
// fenced as the block's code.
func TestLongPasteOverTheBlockIsntCode(t *testing.T) {
	a := offline(t)
	a.code = blocks(a, "go")
	a.code.segs[0].open = true
	text := strings.Repeat("x\n", longCodePaste)
	if !a.onPasteIn(a.code.segs[0])(text) || len(a.attachments) != 1 || a.attachments[0].text != text {
		t.Errorf("a long paste over the block: %d attachments", len(a.attachments))
	}
}

// Up then Down on a code line that wraps comes back down a row, to where
// the caret was, in the block, rather than out under it.
func TestUpThenDownOnAWrappedLineStaysInTheBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type(strings.Repeat("word ", 80) + "end")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyUp)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	h.tt.Type("X")
	h.tt.Frame()
	if belowOf(h.a) || !strings.HasSuffix(draftOf(h.a), "endX") {
		t.Errorf("Up then Down on a wrapped line: below %v, after %q, draft ends %q; want the caret back at the code's end, in the block",
			belowOf(h.a), afterOf(h.a), draftOf(h.a)[max(len(draftOf(h.a))-8, 0):])
	}
}

// Up from the start of a wrapped line's second row goes up a row, to the
// start of the line, not out over the block.
func TestUpFromAWrappedRowsStartStaysInTheBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type(strings.Repeat("word ", 80))
	h.tt.Frame()
	rows := h.a.paneOf(codeID(h.a)).lines.Rows(0)
	if len(rows) < 2 {
		t.Fatalf("rows %v; want the line wrapped", rows)
	}
	h.a.focusPane(codeID(h.a), rows[1])
	h.tt.Frame()
	h.tt.Key(0, ui.KeyUp)
	h.tt.Frame()
	h.tt.Type("X")
	h.tt.Frame()
	if proseOf(h.a) || !strings.HasPrefix(draftOf(h.a), "X") {
		t.Errorf("Up from the second row's start: prose %v, before %q, draft starts %q; want the caret at the code's start, in the block",
			proseOf(h.a), beforeOf(h.a), draftOf(h.a)[:min(len(draftOf(h.a)), 8)])
	}
}

// Down from a wrapped last line of the text over the block, after Up on
// it, comes back down a row, not into the code.
func TestUpThenDownOnAWrappedLineOverTheBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type(strings.Repeat("word ", 80) + "end")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("code")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyUp) // over the block, the caret at the text's end
	h.tt.Frame()
	h.tt.Key(0, ui.KeyUp) // up a row in it
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown) // and back
	h.tt.Frame()
	h.tt.Type("X")
	h.tt.Frame()
	if !strings.HasSuffix(beforeOf(h.a), "endX") {
		t.Errorf("before ends %q, code %q; want X typed at the end of the text over the block", beforeOf(h.a)[max(len(beforeOf(h.a))-8, 0):], draftOf(h.a))
	}
}

// ``` and Enter in the text over the block open another block there,
// rather than send half a fence: the message has both, in order.
func TestFenceInTheTextOverTheBlockOpensAnother(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("a := 1")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyUp)
	h.tt.Frame()
	h.tt.Type("```py")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("print(1)")
	h.tt.Advance(500 * time.Millisecond)
	for range 20 {
		h.p.run()
		h.tt.Advance(50 * time.Millisecond)
	}
	if s := sentTexts(h); len(s) != 0 || blockCount(h.a) != 2 || codeSeg(h.a).tag != "python" || codeSeg(h.a).text != "print(1)" {
		t.Fatalf("``` Enter over the block: sent %q, draft %v; want a Python block over the first, nothing sent", s, h.a.code)
	}
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.until("the message", func() bool { return slices.Contains(sentTexts(h), "```python\nprint(1)\n```\n```\na := 1\n```") })
}

// ``` typed at the start of a line of text, and Shift+Enter, break the
// line: the text after the caret isn't the block's language.
func TestFenceBeforeTextOnItsLineDoesntOpenTheBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("intro")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("outro")
	h.tt.Key(0, ui.KeyHome)
	h.tt.Type("```")
	h.tt.Frame()
	if draftOf(h.a) != "intro\n```outro" {
		t.Fatalf("draft %q; want the ``` typed before outro", draftOf(h.a))
	}
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Frame()
	if h.a.code.on || draftOf(h.a) != "intro\n```\noutro" {
		t.Errorf("block %+v, draft %q; want the line broken after the ```, no block", h.a.code, draftOf(h.a))
	}
}

// Shift+Enter, or Enter, in the middle of a line over a ``` breaks the
// line, rather than open the block.
func TestShiftEnterOverAFenceBreaksTheLine(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("fix this")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyUp)
	h.tt.Key(0, ui.KeyHome)
	h.tt.Key(0, ui.KeyRight)
	h.tt.Key(0, ui.KeyRight)
	h.tt.Key(0, ui.KeyRight)
	h.tt.Frame()
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Frame()
	if h.a.code.on || draftOf(h.a) != "fix\n this\n```" {
		t.Errorf("block %+v, draft %q; want the line broken after fix, no block", h.a.code, draftOf(h.a))
	}
}

// ``` on a line of its own between two lines of text, and Enter or
// Shift+Enter, open the block there: the text over it and under it.
func TestFenceBetweenTextOpensTheBlockThere(t *testing.T) {
	for _, mods := range []ui.Modifiers{0, ui.Shift} {
		h := enterReady(t, fake.New(fake.Turn(false)))
		h.tt.Type("intro")
		h.tt.Key(ui.Shift, ui.KeyEnter)
		h.tt.Type("outro")
		h.tt.Key(0, ui.KeyUp)
		h.tt.Key(0, ui.KeyEnd)
		h.tt.Key(ui.Shift, ui.KeyEnter)
		h.tt.Type("```")
		h.tt.Frame()
		if draftOf(h.a) != "intro\n```\noutro" {
			t.Fatalf("draft %q; want ``` on a line between", draftOf(h.a))
		}
		h.tt.Key(mods, ui.KeyEnter)
		h.tt.Type("x")
		h.tt.Frame()
		if !h.a.code.on || beforeOf(h.a) != "intro" || afterOf(h.a) != "outro" || draftOf(h.a) != "x" {
			t.Errorf("mods %v: block %+v, draft %q; want a block between intro and outro, holding x", mods, h.a.code, draftOf(h.a))
		}
	}
}

// Undo right after ``` opened the block under text, or after typing in it,
// doesn't give the text over it back into the code too: it goes back to
// the draft as it was.
func TestUndoPastTheBlocksOpeningDoesntRepeatTheText(t *testing.T) {
	for _, typed := range []string{"", "x"} {
		h := enterReady(t, fake.New(fake.Turn(false)))
		h.tt.Type("how does")
		h.tt.Key(ui.Shift, ui.KeyEnter)
		h.tt.Type("```")
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Frame()
		h.tt.Type(typed)
		h.tt.Frame()
		if !h.a.code.on || beforeOf(h.a) != "how does" {
			t.Fatalf("block %+v; want it open under how does", h.a.code)
		}
		for i := range 3 {
			h.tt.Key(ui.Cmd, ui.KeyZ)
			h.tt.Frame()
			h.tt.Frame()
			msg := draftOf(h.a)
			if h.a.code.on {
				msg = h.a.codeMessage()
			}
			if n := strings.Count(msg, "how does"); n != 1 {
				t.Errorf("typed %q, undo %d: block %+v, draft %q; the message would hold how does %d times, want once", typed, i+1, h.a.code, draftOf(h.a), n)
			}
		}
	}
}

// ⌘⏎, the block's send, in an empty block doesn't stop the agent: there is
// nothing to send, and an approval's ⌘⏎ isn't taken from it either.
func TestCmdEnterInAnEmptyBlockDoesntStop(t *testing.T) {
	for _, approval := range []bool{false, true} {
		var h *harness
		if approval {
			h, _ = asking(t, runTests)
			r, ok := h.tt.Find("Model")
			if !ok {
				t.Fatal("no composer")
			}
			h.tt.ClickAt(r.X+40, r.Y-30)
		} else {
			h = enterReady(t, stalled{fake.New(midTurn), 1}, "go")
			h.until("working", func() bool { return h.a.current().View.Working() })
		}
		h.tt.Frame()
		h.tt.Type("```")
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Frame()
		if !h.a.code.on {
			t.Fatalf("approval %v: no block; draft %q", approval, draftOf(h.a))
		}
		h.tt.Key(ui.Cmd, ui.KeyEnter)
		for range 30 {
			if h.p.run() {
				h.tt.Frame()
			}
			h.tt.Advance(20 * time.Millisecond)
		}
		if v := h.a.current().View; v.LastTurn != nil && v.LastTurn.Outcome == api.OutcomeInterrupted {
			t.Errorf("approval %v: ⌘⏎ in an empty block stopped the agent", approval)
		}
	}
}

// A steer refused back into the composer leaves an open block, empty,
// where it is, rather than take the composer from it.
func TestRefusedSteerLeavesAnEmptyBlock(t *testing.T) {
	h := start(t, uncertainDir(t))
	th := h.thread("Stuck")
	h.a.select_(th)
	h.a.draft = "Keep going."
	h.tt.Advance(time.Second)
	h.click("Send")
	h.a.code = blocks(h.a, "go")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.p.run()
		h.tt.Frame()
		if len(th.Pending) == 0 && draftOf(h.a) != "" || len(th.Pending) == 1 && th.Pending[0].Err != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.tt.Advance(time.Second)
	if !h.a.code.on {
		t.Errorf("the refused steer closed the empty block: draft %q", draftOf(h.a))
	}
}

// Undo from the Edit menu, as macOS's ⌘Z is, goes no further back than
// the block's opening either.
func TestMenuUndoStaysInTheBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("how does")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.tt.Type("x")
	h.tt.Frame()
	for i := range 3 {
		h.tt.Command("undo")
		h.tt.Frame()
		if !h.a.code.on || beforeOf(h.a) != "how does" || strings.Contains(draftOf(h.a), "how") {
			t.Errorf("undo %d: block %+v, draft %q; want the block open under how does, the code without it", i+1, h.a.code, draftOf(h.a))
		}
	}
}

// Undo and redo in the code keep the text written under the block and the
// changes to the text over it.
func TestUndoInTheCodeKeepsTheTextAround(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("how does")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.tt.Type("x := 1")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	h.tt.Type("explain")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyUp)
	h.tt.Frame()
	for range 2 {
		h.tt.Key(ui.Cmd, ui.KeyZ)
		h.tt.Frame()
	}
	for range 3 {
		h.tt.Key(ui.Cmd|ui.Shift, ui.KeyZ)
		h.tt.Frame()
	}
	if !h.a.code.on || beforeOf(h.a) != "how does" || afterOf(h.a) != "explain" || draftOf(h.a) != "x := 1" {
		t.Errorf("undo and redo in the code: block %+v, draft %q; want how does over x := 1, explain under", h.a.code, draftOf(h.a))
	}
}

// One undo after typing code as it's typed, a key at a time, takes back
// the code's typing, not the block with it.
func TestOneUndoTakesBackTheCodesTyping(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	typing := func(s string) {
		for _, r := range s {
			h.tt.Type(string(r))
			h.tt.Advance(50 * time.Millisecond)
		}
	}
	typing("how does")
	h.tt.Advance(2 * time.Second)
	h.tt.Key(ui.Shift, ui.KeyEnter)
	typing("```go")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Advance(50 * time.Millisecond)
	typing("fmt.Println(1)")
	h.tt.Key(ui.Cmd, ui.KeyZ)
	h.tt.Frame()
	if !h.a.code.on || tagOf(h.a) != "go" || beforeOf(h.a) != "how does" || draftOf(h.a) != "" {
		t.Errorf("one undo: block %+v, draft %q; want the Go block open under how does, empty", h.a.code, draftOf(h.a))
	}
}

// ``` on a line of its own over a fenced block typed in the draft, and
// Enter, break the line rather than send.
func TestFenceOverAClosedFenceDoesntSend(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.a.draft = "see:\n```go\nfoo()\n```"
	h.tt.Frame()
	h.a.focusPane(0, 4)
	h.tt.Frame()
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	for range 20 {
		h.p.run()
		h.tt.Advance(50 * time.Millisecond)
	}
	if s := sentTexts(h); len(s) != 0 {
		t.Errorf("sent %q; want the line broken", s)
	}
}

// Backspace never opens the block: not on a draft it leaves ending in a
// fence's opening and a line break.
func TestDeletingUnderAFenceDoesntOpenTheBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("intro")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("outro")
	h.tt.Key(0, ui.KeyHome)
	h.tt.Type("```")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Key(0, ui.KeyEnd)
	for range 5 {
		h.tt.Key(0, ui.KeyBackspace)
		h.tt.Frame()
	}
	if h.a.code.on || draftOf(h.a) != "intro\n```\n" {
		t.Errorf("block %+v, draft %q; want no block, the text deleted", h.a.code, draftOf(h.a))
	}
}

// A fence typed by hand to close the block, at the end of its code, and
// Shift+Enter or Enter, close it: what follows is text under it, and Enter
// there sends, as a fence typed by hand does in a draft.
func TestTypedClosingFenceLeavesTheBlock(t *testing.T) {
	for _, mods := range []ui.Modifiers{ui.Shift, 0} {
		h := enterReady(t, fake.New(fake.Turn(false)))
		h.tt.Type("fix this:")
		h.tt.Key(ui.Shift, ui.KeyEnter)
		h.tt.Type("```go")
		h.tt.Key(ui.Shift, ui.KeyEnter)
		h.tt.Type("a := 1")
		h.tt.Key(ui.Shift, ui.KeyEnter)
		h.tt.Type("```")
		h.tt.Key(mods, ui.KeyEnter)
		h.tt.Type("thanks")
		h.tt.Frame()
		if !belowOf(h.a) || draftOf(h.a) != "a := 1" || afterOf(h.a) != "thanks" {
			t.Fatalf("mods %v: block %+v, draft %q; want a := 1 in the block, thanks under it", mods, h.a.code, draftOf(h.a))
		}
		h.tt.Key(0, ui.KeyEnter)
		h.until("the message", func() bool { return slices.Contains(sentTexts(h), "fix this:\n```go\na := 1\n```\nthanks") })
	}
}

// Choosing a language while writing the text over the block gives the
// focus back there, not to the code.
func TestCodeLanguageMenuGivesTheFocusBackToTheText(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("code")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyUp)
	h.tt.Frame()
	h.tt.Type("abo")
	h.tt.Frame()
	if err := h.tt.Click("Code block language"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Python"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.tt.Frame()
	h.tt.Type("ve")
	h.tt.Frame()
	if beforeOf(h.a) != "above" || draftOf(h.a) != "code" || tagOf(h.a) != "python" {
		t.Errorf("block %+v, code %q; want above over the Python block, code in it", h.a.code, draftOf(h.a))
	}
}

// A paste too long for the block, attached fenced, doesn't title the
// thread with its fence.
func TestCodeTitleSkipsFences(t *testing.T) {
	if got := codeTitle("```go\nfunc main() {}\n```"); got != "func main() {}" {
		t.Errorf("codeTitle = %q; want the code's first line", got)
	}
	if got := codeTitle("see\n```\nx\n```"); got != "see" {
		t.Errorf("codeTitle = %q; want the text over the code", got)
	}
}

func TestInserted(t *testing.T) {
	for _, c := range []struct {
		was, now, ins string
		ok            bool
	}{
		{"how does", "!how does", "!", true},
		{"how does", "how does!", "!", true},
		{"", "ab", "ab", true},
		{"héllo", "hé✓llo", "✓", true},
		{"é", "è", "", false},
		{"abc", "ac", "", false},
		{"abc", "abc", "", false},
	} {
		if ins, ok := inserted(c.was, c.now); ins != c.ins || ok != c.ok {
			t.Errorf("inserted(%q, %q) = %q, %v; want %q, %v", c.was, c.now, ins, ok, c.ins, c.ok)
		}
	}
}

// A fence typed at the end of the code that closes a fence in it, as in a
// Markdown block, is code: it doesn't close the block.
func TestClosingAFenceInTheCodeStaysInTheBlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```md")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	for _, l := range []string{"# Usage", "```sh", "make", "```"} {
		h.tt.Type(l)
		h.tt.Key(ui.Shift, ui.KeyEnter)
	}
	h.tt.Type("more")
	h.tt.Frame()
	if !h.a.code.on || belowOf(h.a) || draftOf(h.a) != "# Usage\n```sh\nmake\n```\nmore" {
		t.Errorf("block %+v, code %q; want the Markdown, its fence and all, in the block", h.a.code, draftOf(h.a))
	}
}

// Enter in the code of a fence typed by hand and closed sends, as in a
// draft: only at the end of a fence's opening does it break the line.
func TestEnterInAClosedTypedFenceSends(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.a.draft = "fix:\n```go\nfoo()\n```\nthanks"
	h.tt.Frame()
	h.a.focusPane(0, len("fix:\n```go\nfoo()"))
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.until("the message", func() bool { return slices.Contains(sentTexts(h), "fix:\n```go\nfoo()\n```\nthanks") })
}

// A fence typed to close the block closes it when the line break after it
// comes as text, as an input method sends it, too.
func TestClosingFenceByATypedLineBreak(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	for _, s := range []string{"```go", "\n", "a := 1", "\n", "```", "\n", "thanks"} {
		h.tt.Type(s)
		h.tt.Frame()
	}
	if !h.a.code.on || draftOf(h.a) != "a := 1" || afterOf(h.a) != "thanks" {
		t.Errorf("block %+v, code %q; want a := 1 in the block, thanks under it", h.a.code, draftOf(h.a))
	}
}

// Arrowing over the block and back, writing nothing, leaves no empty text
// over or under it.
func TestArrowsLeaveNoEmptyText(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("code")
	for _, k := range []ui.Key{ui.KeyUp, ui.KeyDown, ui.KeyDown, ui.KeyUp} {
		h.tt.Frame()
		h.tt.Key(0, k)
	}
	h.tt.Frame()
	if proseOf(h.a) || belowOf(h.a) {
		t.Errorf("block %+v; want no text over or under it", h.a.code)
	}
	h.tt.Type("!")
	h.tt.Frame()
	if draftOf(h.a) != "!code" && draftOf(h.a) != "code!" {
		t.Errorf("code %q; want the caret back in the code", draftOf(h.a))
	}
}

// Edit & resend over an open block doesn't take the focus to the draft at
// the old code's caret.
func TestEditAndResendOverABlockKeepsNoCaret(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)), "Sort them by most recent activity.")
	h.until("the reply", func() bool { return len(sentTexts(h)) > 0 && !h.a.current().View.Working() })
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("abcd")
	h.tt.Frame()
	r, ok := h.tt.Find("Sort them by most recent activity.")
	if !ok {
		t.Fatal("no message")
	}
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Frame()
	if err := h.tt.Click("Edit & resend"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.tt.Type("X")
	h.tt.Frame()
	if h.a.code.on || strings.Contains(draftOf(h.a), "SortX") {
		t.Errorf("block %+v, draft %q; want the message back as it was, no X typed into it at the old caret", h.a.code, draftOf(h.a))
	}
}

// blocks is a draft holding one empty block, tagged tag.
func blocks(a *app, tag string) codeBlock {
	return codeBlock{on: true, segs: []*segment{a.newSeg(false, "", ""), a.newSeg(true, tag, ""), a.newSeg(false, "", "")}}
}

// codeSeg is the draft's first block, or none.
func codeSeg(a *app) *segment {
	if i := slices.IndexFunc(a.code.segs, func(s *segment) bool { return s.code }); a.code.on && i >= 0 {
		return a.code.segs[i]
	}
	return &segment{}
}

// draftOf is the draft, or, as it holds blocks, its first block's code.
func draftOf(a *app) string {
	if a.code.on {
		return codeSeg(a).text
	}
	return a.draft
}

func codeID(a *app) int   { return codeSeg(a).id }
func tagOf(a *app) string { return codeSeg(a).tag }
func beforeOf(a *app) string {
	if !a.code.on {
		return ""
	}
	return a.code.segs[0].text
}
func afterOf(a *app) string {
	if !a.code.on {
		return ""
	}
	return a.code.segs[len(a.code.segs)-1].text
}
func proseOf(a *app) bool { return a.code.on && a.code.segs[0].shown() }
func belowOf(a *app) bool { return a.code.on && a.code.segs[len(a.code.segs)-1].shown() }

func (b codeBlock) String() string {
	if !b.on {
		return "{no blocks}"
	}
	var parts []string
	for _, s := range b.segs {
		switch {
		case s.code:
			parts = append(parts, fmt.Sprintf("code %d %q: %q", s.id, s.tag, s.text))
		case s.shown():
			parts = append(parts, fmt.Sprintf("text %d: %q", s.id, s.text))
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// blockCount is how many blocks the draft holds.
func blockCount(a *app) int {
	n := 0
	for _, s := range a.code.segs {
		if s.code {
			n++
		}
	}
	return n
}

// A fence typed in the text under a block, and Shift+Enter or Enter, open
// a second block there: the draft holds as many as are typed, each with
// its own picker, and they go in order, the text between them too.
func TestSecondBlockUnderTheFirst(t *testing.T) {
	for _, mods := range []ui.Modifiers{ui.Shift, 0} {
		h := enterReady(t, fake.New(fake.Turn(false)))
		h.tt.Type("```")
		h.tt.Key(ui.Shift, ui.KeyEnter)
		h.tt.Type("a := 1")
		h.tt.Key(ui.Shift, ui.KeyEnter)
		h.tt.Type("```")
		h.tt.Key(ui.Shift, ui.KeyEnter)
		h.tt.Type("then")
		h.tt.Key(ui.Shift, ui.KeyEnter)
		h.tt.Type("```go")
		h.tt.Key(mods, ui.KeyEnter)
		h.tt.Type("b := 2")
		h.tt.Frame()
		if blockCount(h.a) != 2 || len(sentTexts(h)) != 0 {
			t.Fatalf("mods %v: draft %v, sent %q; want two blocks, nothing sent", mods, h.a.code, sentTexts(h))
		}
		if !h.tt.HasText("Plain text") || !h.tt.HasText("Go") {
			t.Errorf("mods %v: pickers %q; want Plain text and Go", mods, h.tt.Texts())
		}
		h.tt.Key(ui.Cmd, ui.KeyEnter)
		h.until("the message", func() bool {
			return slices.Contains(sentTexts(h), "```\na := 1\n```\nthen\n```go\nb := 2\n```")
		})
		if h.a.code.on {
			t.Errorf("mods %v: after sending: %v; want the blocks gone", mods, h.a.code)
		}
	}
}

// twoBlocks is a draft of two blocks, holding a and b, with mid between
// them, the caret at the end of b.
func twoBlocks(t *testing.T, mid string) *harness {
	h := enterReady(t, fake.New(fake.Turn(false)))
	for _, s := range []string{"```", "a", "```", mid, "```"} {
		if s != "" {
			h.tt.Type(s)
		}
		h.tt.Key(ui.Shift, ui.KeyEnter)
		h.tt.Frame()
	}
	h.tt.Type("b")
	h.tt.Frame()
	if blockCount(h.a) != 2 {
		t.Fatalf("draft %v; want two blocks", h.a.code)
	}
	return h
}

// Up and Down go from block to block, through the text between them.
func TestArrowsAcrossTwoBlocks(t *testing.T) {
	h := twoBlocks(t, "mid")
	for _, k := range []ui.Key{ui.KeyUp, ui.KeyUp} {
		h.tt.Key(0, k)
		h.tt.Frame()
	}
	h.tt.Type("X")
	h.tt.Frame()
	if got := h.a.codeMessage(); got != "```\naX\n```\nmid\n```\nb\n```" {
		t.Fatalf("Up twice from the second block: message %q; want X at the end of the first block", got)
	}
	for _, k := range []ui.Key{ui.KeyDown, ui.KeyDown} {
		h.tt.Key(0, k)
		h.tt.Frame()
	}
	h.tt.Type("Y")
	h.tt.Frame()
	if got := h.a.codeMessage(); got != "```\naX\n```\nmid\n```\nYb\n```" {
		t.Errorf("Down twice back: message %q; want Y at the start of the second block", got)
	}
}

// Up from the second block, with no text between, goes to a line of text
// between the blocks, and leaving it empty takes it away.
func TestArrowsBetweenAdjacentBlocks(t *testing.T) {
	h := twoBlocks(t, "")
	h.tt.Key(0, ui.KeyUp)
	h.tt.Frame()
	h.tt.Type("mid")
	h.tt.Frame()
	if got := h.a.codeMessage(); got != "```\na\n```\nmid\n```\nb\n```" {
		t.Fatalf("Up from the second block: message %q; want text between the blocks", got)
	}
}

// Backspace in an empty second block takes it away, the text around it
// coming together, the caret between.
func TestBackspaceMergesTheTextAroundABlock(t *testing.T) {
	h := twoBlocks(t, "mid")
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	h.tt.Type("end")
	h.tt.Frame()
	for _, k := range []ui.Key{ui.KeyUp, ui.KeyBackspace, ui.KeyBackspace} {
		h.tt.Key(0, k)
		h.tt.Frame()
	}
	h.tt.Type("!")
	h.tt.Frame()
	if blockCount(h.a) != 1 || h.a.codeMessage() != "```\na\n```\nmid!\nend" {
		t.Errorf("Backspace in the empty second block: draft %v, message %q; want one block, mid!\\nend under it", h.a.code, h.a.codeMessage())
	}
}

// The text under a block grows with its lines rather than scroll on its
// own, its first line going under the block: the draft scrolls as one,
// and keeps the caret in sight.
func TestTextUnderTheBlockIsntClipped(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("code")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	for i := range 8 {
		h.tt.Type(fmt.Sprintf("line %d", i))
		h.tt.Key(ui.Shift, ui.KeyEnter)
	}
	h.tt.Frame()
	h.tt.Frame()
	r, ok := h.tt.Find("Message")
	under := h.a.code.segs[len(h.a.code.segs)-1]
	if lines := h.a.paneOf(under.id).lines.Height(); !ok || r.H < lines {
		t.Fatalf("the text under the block: %v high, its lines %v; want it as high as them", r.H, lines)
	}
	for i := range 30 {
		h.tt.Type(fmt.Sprintf("more %d", i))
		h.tt.Key(ui.Shift, ui.KeyEnter)
	}
	h.tt.Frame()
	h.tt.Frame()
	caret, _ := h.tt.TextCaret()
	m, ok := h.tt.Find("Model")
	if !ok || caret.Y+caret.H > m.Y || caret.Y < m.Y-maxDraftHeight-40 {
		t.Errorf("the caret at %v, the composer's controls at %v; want the caret in the draft, in sight", caret, m)
	}
}

// In the new thread's larger composer, the draft holding blocks scrolls
// too, rather than grow the card off the window's top.
func TestBlocksInTheHeroScroll(t *testing.T) {
	h := start(t, inboxDir(t))
	h.click("New thread")
	h.tt.Advance(time.Second)
	h.tt.Type("```")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("a")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	for range 40 {
		h.tt.Type("after")
		h.tt.Key(ui.Shift, ui.KeyEnter)
	}
	h.tt.Frame()
	h.tt.Frame()
	if h.a.codeScroll.MaxY <= 0 {
		t.Errorf("40 lines under the block: the draft scrolls %v; want it to scroll, the card no higher", h.a.codeScroll.MaxY)
	}
}

// A fence left half open in the text over a block isn't sent from the
// text under it: Enter breaks the line there, as in a draft with a fence
// half open, rather than send the block as that fence's code.
func TestHalfFenceInAnotherPieceDoesntSend(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	step := func(mods ui.Modifiers, k ui.Key) { h.tt.Key(mods, k); h.tt.Frame() }
	h.tt.Type("```go")
	step(0, ui.KeyEnter)
	h.tt.Type("code()")
	step(0, ui.KeyUp)
	h.tt.Type("see")
	step(ui.Shift, ui.KeyEnter)
	h.tt.Type("```")
	step(0, ui.KeyDown)
	step(0, ui.KeyDown)
	h.tt.Type("thanks")
	step(0, ui.KeyEnter)
	for range 20 {
		h.p.run()
		h.tt.Advance(50 * time.Millisecond)
	}
	if s := sentTexts(h); len(s) != 0 {
		t.Errorf("sent %q; want nothing sent with a fence half open over the block", s)
	}
}

// Backspace in an empty block over text that is a fence and a line break
// takes the block away, the text kept, rather than open another from it.
func TestBackspaceDoesntReopenABlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	step := func(mods ui.Modifiers, k ui.Key) { h.tt.Key(mods, k); h.tt.Frame() }
	h.tt.Type("```")
	step(0, ui.KeyEnter)
	step(0, ui.KeyDown)
	step(ui.Shift, ui.KeyEnter)
	step(0, ui.KeyUp)
	h.tt.Type("```")
	h.tt.Frame()
	step(0, ui.KeyUp)
	step(0, ui.KeyBackspace)
	h.tt.Frame()
	if h.a.code.on || h.a.draft != "```\n" {
		t.Errorf("Backspace in the empty block: %v, draft %q; want no blocks, the draft %q", h.a.code, h.a.draft, "```\n")
	}
}

// caretInSight reports whether the caret is in the draft's scroll.
func caretInSight(h *harness) (ui.Rect, ui.Rect, bool) {
	caret, ok := h.tt.TextCaret()
	m, ok2 := h.tt.Find("Model")
	return caret, m, ok && ok2 && caret.Y+caret.H <= m.Y && caret.Y >= m.Y-maxDraftHeight-40
}

// The window narrows: lines wrap, the caret should stay in sight.
func TestBlocksFollowTheCaretAfterAResize(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	for i := range 12 {
		h.tt.Type(fmt.Sprintf("line %d %s", i, strings.Repeat("word ", 25)))
		h.tt.Key(ui.Shift, ui.KeyEnter)
	}
	h.tt.Type("end")
	h.tt.Frame()
	h.tt.Frame()
	if _, _, ok := caretInSight(h); !ok {
		t.Fatal("the caret is out of sight before the window narrows")
	}
	h.tt.SetSize(700, 860)
	h.tt.Advance(time.Second)
	h.tt.Frame()
	if c, m, ok := caretInSight(h); !ok {
		t.Errorf("after narrowing the window the caret, at %v, is out of sight over the controls at %v", c, m)
	}
}

// The picker of a block scrolled to, the focus elsewhere: the draft stays
// where the user scrolled it.
func TestCodePickerLeavesTheScroll(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```go")
	h.tt.Key(0, ui.KeyEnter)
	for i := range 30 {
		h.tt.Type(fmt.Sprintf("line %d", i))
		h.tt.Key(ui.Shift, ui.KeyEnter)
	}
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("second")
	h.tt.Frame()
	h.tt.Frame()
	if blockCount(h.a) != 2 {
		t.Fatalf("draft %v", h.a.code)
	}
	// Up to the first block's top.
	for range 40 {
		h.tt.Key(0, ui.KeyUp)
		h.tt.Frame()
	}
	h.tt.Frame()
	c, _, _ := caretInSight(h)
	// Wheel to the end.
	h.tt.Scroll(c.X, c.Y+40, 0, 5000)
	h.tt.Frame()
	h.tt.Frame()
	end := h.a.codeScroll.Y
	if !h.tt.HasText("Plain text") {
		t.Fatalf("no second picker: %q", h.tt.Texts())
	}
	h.click("Plain text")
	h.tt.Frame()
	h.tt.Frame()
	opened := h.a.codeScroll.Y
	if err := h.tt.ChooseMenuItem("Python"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if opened != end || h.a.codeScroll.Y != end {
		t.Errorf("the draft scrolled from %v to %v as the menu opened, %v after choosing; want it left where the wheel put it", end, opened, h.a.codeScroll.Y)
	}
	h.tt.Type("Q")
	h.tt.Frame()
}

// Two blocks in sight, the caret in the first: the second's picker chosen
// from, and typing after.
func TestCodePickerOfAnotherBlockTakesTheCaret(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```go")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("first")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("second")
	h.tt.Frame()
	// The caret to the first block.
	h.tt.Key(0, ui.KeyUp)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyUp)
	h.tt.Frame()
	h.tt.Frame()
	h.click("Plain text")
	h.tt.Frame()
	if err := h.tt.ChooseMenuItem("Python"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.tt.Advance(200 * time.Millisecond)
	h.tt.Type("Q")
	h.tt.Frame()
	if got := h.a.codeMessage(); got != "```go\nfirst\n```\n```python\nsecondQ\n```" {
		t.Errorf("message %q; want Q typed in the block whose language was just chosen", got)
	}
}

// Typing in a block keeps the wind blowing, as typing in the draft does.
func TestWindFollowsTypingInABlock(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	before := h.a.typed
	h.tt.Type("x := 1")
	h.tt.Frame()
	if !h.a.settings.wind || h.a.typed == before {
		t.Errorf("wind on %v; typed %q before and after typing code in the block: the wind isn't woken", h.a.settings.wind, h.a.typed)
	}
}

// The text over a block, clicked into and emptied, is written anew.
func TestClickedTextEmptiedTakesTyping(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	step := func(m ui.Modifiers, k ui.Key) { h.tt.Key(m, k); h.tt.Frame() }
	typ := func(s string) { h.tt.Type(s); h.tt.Frame() }
	typ("hello")
	step(ui.Shift, ui.KeyEnter)
	typ("```")
	step(0, ui.KeyEnter)
	typ("code")
	g, _ := h.tt.Find("Plain text")
	h.tt.ClickAt(g.X+300, g.Y-20) // the line of text over the block
	h.tt.Frame()
	typ("!")
	if got := h.a.codeMessage(); got != "hello!\n```\ncode\n```" {
		t.Fatalf("clicked into the text over the block, typed !: message %q", got)
	}
	step(ui.Cmd, ui.KeyA)
	step(0, ui.KeyBackspace)
	typ("new text")
	if got := h.a.codeMessage(); got != "new text\n```\ncode\n```" {
		t.Errorf("select all, Backspace, typed \"new text\": draft %v, message %q; want %q", h.a.code, got, "new text\n```\ncode\n```")
	}
}

// The text over a block, clicked into and backspaced away, is written
// anew.
func TestClickedTextBackspacedTakesTyping(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	step := func(m ui.Modifiers, k ui.Key) { h.tt.Key(m, k); h.tt.Frame() }
	typ := func(s string) { h.tt.Type(s); h.tt.Frame() }
	typ("hi")
	step(ui.Shift, ui.KeyEnter)
	typ("```")
	step(0, ui.KeyEnter)
	typ("code")
	g, _ := h.tt.Find("Plain text")
	h.tt.ClickAt(g.X+300, g.Y-20)
	h.tt.Frame()
	step(0, ui.KeyBackspace)
	step(0, ui.KeyBackspace)
	typ("yo")
	if got := h.a.codeMessage(); got != "yo\n```\ncode\n```" {
		t.Errorf("\"hi\" backspaced, \"yo\" typed: draft %v, message %q; want %q", h.a.code, got, "yo\n```\ncode\n```")
	}
}

// Back from Settings, the draft holding a block takes the keys, as a
// draft without does.
func TestBlocksTakeTheFocusBackFromSettings(t *testing.T) {
	for _, block := range []bool{false, true} {
		h := enterReady(t, fake.New(fake.Turn(false)))
		if block {
			h.tt.Type("```")
			h.tt.Key(0, ui.KeyEnter)
			h.tt.Frame()
		}
		h.tt.Type("code")
		h.tt.Frame()
		h.a.router.Push("/settings/general")
		h.tt.Advance(time.Second)
		h.a.router.Push("/")
		h.tt.Advance(time.Second)
		h.tt.Frame()
		h.tt.Type("Q")
		h.tt.Frame()
		if got := draftOf(h.a); got != "codeQ" {
			t.Errorf("block %v: back from Settings, Q typed: draft %q; want %q", block, got, "codeQ")
		}
	}
}

// In the new thread's card, a click in the room under the block keeps
// the draft's focus, as it does in a draft without blocks.
func TestHeroClickUnderTheBlockKeepsTheFocus(t *testing.T) {
	for _, block := range []bool{false, true} {
		h := start(t, inboxDir(t))
		h.click("New thread")
		h.tt.Advance(time.Second)
		if block {
			h.tt.Type("```")
			h.tt.Key(0, ui.KeyEnter)
			h.tt.Frame()
		}
		h.tt.Type("a")
		h.tt.Frame()
		m, _ := h.tt.Find("Model")
		c, _ := h.tt.TextCaret()
		h.tt.ClickAt(c.X+200, (c.Y+c.H+m.Y)/2)
		h.tt.Frame()
		h.tt.Type("Q")
		h.tt.Frame()
		if got := draftOf(h.a) + afterOf(h.a); got != "aQ" {
			t.Errorf("block %v: caret at %v, controls at %v, clicked between, Q typed: draft %v %q; want the Q in the draft", block, c, m, h.a.code, h.a.draft)
		}
	}
}

// The empty line Down showed under the block goes as the caret leaves
// it by a click, as it does by Up.
func TestEmptyLineLeftByAClickGoes(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.tt.Type("code")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	g, _ := h.tt.Find("Plain text")
	h.tt.ClickAt(g.X+300, g.Y+g.H+12) // the code's line
	h.tt.Frame()
	h.tt.Type("Q")
	h.tt.Frame()
	if draftOf(h.a) != "codeQ" {
		t.Fatalf("the click didn't go into the code: %v", h.a.code)
	}
	if belowOf(h.a) {
		t.Errorf("the empty line under the block still shows after the caret left it by a click: %v", h.a.code)
	}
}

// Enter at the end of a wrapped last line of a block higher than the
// draft's scroll: the draft should follow the caret to the new line, as it
// does after an unwrapped one. Every event has frames of its own.
func TestEnterAfterAWrappedLineFollowsTheCaret(t *testing.T) {
	for _, long := range []string{"abcdef", strings.Repeat("abcdefghi ", 20), strings.Repeat("乔乕乖乗乘", 15)} {
		h := enterReady(t, fake.New(fake.Turn(false)))
		step := func(f func()) { f(); h.tt.Frame(); h.tt.Frame() }
		step(func() { h.tt.Type("```") })
		step(func() { h.tt.Key(0, ui.KeyEnter) })
		for range 20 {
			step(func() { h.tt.Type("line") })
			step(func() { h.tt.Key(0, ui.KeyEnter) })
		}
		step(func() { h.tt.Type(long) })
		step(func() { h.tt.Key(0, ui.KeyEnter) })
		for range 5 {
			h.tt.Frame()
		}
		h.tt.Advance(time.Second)
		c, _ := h.tt.TextCaret()
		was := h.a.codeScroll
		// What one more letter scrolls the draft to is where the Enter should have.
		step(func() { h.tt.Type("Q") })
		c2, _ := h.tt.TextCaret()
		if d := h.a.codeScroll.Y - was.Y; d > 1 {
			t.Errorf("last line of %d runes: after Enter the draft is scrolled to %v of %v, the caret's row at y %v..%v; one more letter scrolls it %v further, the row to y %v..%v",
				len([]rune(long)), was.Y, was.MaxY, c.Y, c.Y+c.H, d, c2.Y, c2.Y+c2.H)
		}
		h.a.backend.Close()
	}
}

// A long line in a block, wrapped over more rows than the draft shows:
// Left from the start of the top row in sight, to the end of the row over
// it: the caret stays in sight.
func TestLeftOverAWrapKeepsTheCaretInSight(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	defer h.a.backend.Close()
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.tt.SetClipboard(strings.Repeat("lorem ipsum dolor sit amet ", 120))
	h.tt.Key(ui.Cmd, ui.KeyV)
	h.tt.Frame()
	h.tt.Frame()
	send, _ := h.tt.Find("Send")
	inSight := func(what string) bool {
		h.tt.Frame()
		c, ok := h.tt.TextCaret()
		if !ok || c.Y+c.H > send.Y || c.Y < send.Y-8-240-4 {
			t.Errorf("%s: the caret is at %v (focused %v), out of the draft's 240 high scroll, which ends over Send at y %v", what, c, ok, send.Y)
			return false
		}
		return true
	}
	for i := 0; i < 18; i++ {
		h.tt.Key(0, ui.KeyUp)
		if !inSight("Up") {
			return
		}
	}
	h.tt.Key(0, ui.KeyHome)
	inSight("Home")
	h.tt.Key(0, ui.KeyLeft)
	inSight("Left from the start of the top row in sight")
	h.tt.Type("X")
	inSight("typing after it")
}
