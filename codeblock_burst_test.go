package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"

	"github.com/egoist/mygo/ui"
)

// qharness is a harness whose view first runs the events queued in
// *ahead, as events that came before the frame, the frame's elements
// still the last's.
type qharness struct {
	*harness
	ahead *[]func()
}

// burst delivers fns as events arriving before one frame, then runs it.
func (h qharness) burst(fns ...func()) {
	*h.ahead = append(*h.ahead, fns...)
	h.tt.Frame()
}

func enterReadyQ(t *testing.T, adapter provider.Adapter, messages ...string) qharness {
	t.Helper()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, adapter)
	id := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	for _, m := range messages {
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: m}}); err != nil {
			t.Fatal(err)
		}
	}
	h := startQ(t, dir)
	h.a.select_(h.thread("Ship the sidebar sort"))
	h.tt.Advance(time.Second)
	return h
}
func startQ(t *testing.T, dir string) qharness {
	t.Helper()
	p := &posts{wake: make(chan struct{}, 1)}
	a := testApp(t, dir, nil, p.post)
	if err := a.backend.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.backend.Close() })
	ahead := &[]func(){}
	view := func(c *ui.Context) {
		q := *ahead
		*ahead = nil
		for _, f := range q {
			f()
		}
		a.view(c)
	}
	h := &harness{t, a, ui.NewTester(view, 1320, 860), p}
	h.until("this machine's engine", func() bool { return a.backend.Envs()[0].Status == client.Status(client.Online{}) })
	return qharness{h, ahead}
}

func (h qharness) typ(s string) func()                 { return func() { h.tt.Type(s) } }
func (h qharness) key(m ui.Modifiers, k ui.Key) func() { return func() { h.tt.Key(m, k) } }

// ``` Enter, and code typed before the next frame: it lands in the block.
func TestFastCodeAfterOpening(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Frame()
	h.burst(h.key(0, ui.KeyEnter), h.typ("abc"))
	h.tt.Frame()
	h.tt.Frame()
	if !h.a.code.on || draftOf(h.a) != "abc" {
		t.Errorf("draft %v; want abc in the block", h.a.code)
	}
}

// ``` Enter, then code with an Enter in it, typed before the next frame:
// the Enter is the block's, a line break.
func TestFastEnterAfterOpening(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Frame()
	h.burst(h.key(0, ui.KeyEnter), h.typ("a"), h.key(0, ui.KeyEnter), h.typ("b"))
	h.tt.Frame()
	h.tt.Frame()
	if !h.a.code.on || draftOf(h.a) != "a\nb" || len(sentTexts(h.harness)) != 0 {
		t.Errorf("draft %v, sent %q; want a\\nb in the block, nothing sent", h.a.code, sentTexts(h.harness))
	}
}

// The same, under text: the fast Enter mustn't send the text over the
// block and drop the block.
func TestFastEnterAfterOpeningUnderText(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("hello")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Frame()
	h.burst(h.key(0, ui.KeyEnter), h.typ("x"), h.key(0, ui.KeyEnter), h.typ("y"))
	for range 20 {
		h.p.run()
		h.tt.Advance(20 * time.Millisecond)
	}
	if !h.a.code.on || draftOf(h.a) != "x\ny" || len(sentTexts(h.harness)) != 0 {
		t.Errorf("draft %v, sent %q; want hello over a block holding x\\ny, nothing sent", h.a.code, sentTexts(h.harness))
	}
}

// ``` and Enter at the end of the code close the block; text typed before
// the next frame goes under it, not into the code.
func TestFastTypingAfterClosingFence(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("a := 1")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Frame()
	h.burst(h.key(0, ui.KeyEnter), h.typ("thanks"))
	h.tt.Frame()
	h.tt.Frame()
	if draftOf(h.a) != "a := 1" || afterOf(h.a) != "thanks" {
		t.Errorf("draft %v; want a := 1 in the block, thanks under it", h.a.code)
	}
}

// Down out of the code's last line, then typing before the next frame:
// the text goes under the block, where the focus went.
func TestFastTypingAfterDown(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("code")
	h.tt.Frame()
	h.burst(h.key(0, ui.KeyDown), h.typ("after"))
	h.tt.Frame()
	h.tt.Frame()
	if draftOf(h.a) != "code" || afterOf(h.a) != "after" {
		t.Errorf("draft %v; want code in the block, after under it", h.a.code)
	}
}

// ``` Enter in text under a block, and code typed before the next frame:
// it lands in the second block.
func TestFastCodeAfterOpeningFromText(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("a")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	h.tt.Type("```go")
	h.tt.Frame()
	h.burst(h.key(0, ui.KeyEnter), h.typ("abc"))
	h.tt.Frame()
	h.tt.Frame()
	if blockCount(h.a) != 2 || h.a.codeMessage() != "```\na\n```\n```go\nabc\n```" {
		t.Errorf("draft %v; want abc in the second block", h.a.code)
	}
}

// Backspace in the empty text under a block, then typing before the next
// frame: it goes at the end of the code.
func TestFastTypingAfterBackspaceInEmptyText(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("a")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	h.burst(h.key(0, ui.KeyBackspace), h.typ("b"))
	h.tt.Frame()
	h.tt.Frame()
	if draftOf(h.a) != "ab" || belowOf(h.a) {
		t.Errorf("draft %v; want ab in the block", h.a.code)
	}
}

// ``` Enter in text under a block, then Enter and more, before the next
// frame: the second block holds the line break.
func TestFastEnterAfterOpeningFromText(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("a")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	h.tt.Type("```")
	h.tt.Frame()
	h.burst(h.key(0, ui.KeyEnter), h.typ("x"), h.key(0, ui.KeyEnter), h.typ("y"))
	for range 20 {
		h.p.run()
		h.tt.Advance(20 * time.Millisecond)
	}
	if got := h.a.codeMessage(); got != "```\na\n```\n```\nx\ny\n```" || len(sentTexts(h.harness)) != 0 {
		t.Errorf("message %q, sent %q", got, sentTexts(h.harness))
	}
}

// Up out of empty text under a block, which goes as it's left, then
// typing before the next frame: it goes in the code, where the focus went.
func TestFastTypingAfterUpFromEmptyText(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("a")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	h.burst(h.key(0, ui.KeyUp), h.typ("b"))
	h.tt.Frame()
	h.tt.Frame()
	if draftOf(h.a) != "ab" || belowOf(h.a) {
		t.Errorf("draft %v; want ab in the block", h.a.code)
	}
}

// ``` Shift+Enter, the way to the next line, then code typed before the
// next frame: it lands in the block.
func TestFastCodeAfterShiftEnter(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("see:")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("```go")
	h.tt.Frame()
	h.burst(h.key(ui.Shift, ui.KeyEnter), h.typ("fmt.Println(1)"))
	h.tt.Frame()
	h.tt.Frame()
	if !h.a.code.on || draftOf(h.a) != "fmt.Println(1)" {
		t.Errorf("draft %v; want fmt.Println(1) in the Go block", h.a.code)
	}
}

func burstBlock(t *testing.T, code string) qharness {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.tt.Type(code)
	h.tt.Frame()
	return h
}

func settleQ(h qharness) {
	for range 5 {
		h.tt.Frame()
	}
}

func textUnderBlock(t *testing.T, s string) qharness {
	h := burstBlock(t, "abc")
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	h.tt.Type(s)
	h.tt.Frame()
	return h
}

// Down out of the code into empty text, then Backspace (back to the code):
// the code is untouched.
func TestFastBackspaceAfterDown(t *testing.T) {
	h := burstBlock(t, "abc")
	h.burst(h.key(0, ui.KeyDown), h.key(0, ui.KeyBackspace))
	settleQ(h)
	if draftOf(h.a) != "abc" {
		t.Errorf("draft %v; want abc intact in the block", h.a.code)
	}
}

// Closing fence and Enter, then Backspace before the next frame.
func TestFastBackspaceAfterClosingFence(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("a := 1")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Frame()
	h.burst(h.key(0, ui.KeyEnter), h.key(0, ui.KeyBackspace))
	settleQ(h)
	if draftOf(h.a) != "a := 1" {
		t.Errorf("draft %v; want a := 1 intact in the block", h.a.code)
	}
}

// Up from text under the block, then Backspace: it deletes in the code.
func TestFastBackspaceAfterUp(t *testing.T) {
	h := textUnderBlock(t, "hello world")
	h.burst(h.key(0, ui.KeyUp), h.key(0, ui.KeyBackspace))
	settleQ(h)
	if draftOf(h.a) != "ab" || afterOf(h.a) != "hello world" {
		t.Errorf("draft %v; want ab in the block, hello world under", h.a.code)
	}
}

// Up from text under the block, then Cmd+Z: undo in the code, never the text.
func TestFastUndoAfterUp(t *testing.T) {
	h := textUnderBlock(t, "hello world")
	h.burst(h.key(0, ui.KeyUp), h.key(ui.Cmd, ui.KeyZ))
	settleQ(h)
	if afterOf(h.a) != "hello world" {
		t.Errorf("draft %v; want hello world kept under the block", h.a.code)
	}
}

// ``` Enter, then Backspace before the next frame: the empty block goes.
func TestFastBackspaceAfterOpening(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Frame()
	h.burst(h.key(0, ui.KeyEnter), h.key(0, ui.KeyBackspace))
	settleQ(h)
	if h.a.code.on {
		t.Errorf("draft %v, %q; want no block", h.a.code, h.a.draft)
	}
}

// Backspace in empty block under text, then Backspace again: deletes in the text.
func TestFastBackspacesClosingABlock(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("hello")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.burst(h.key(0, ui.KeyBackspace), h.key(0, ui.KeyBackspace))
	settleQ(h)
	if h.a.code.on || h.a.draft != "hell" {
		t.Errorf("draft %v %q; want hell, no block", h.a.code, h.a.draft)
	}
}

// ``` Enter, then a long paste before the next frame: it's code in the
// block, as pasted into the block, not an attachment.
func TestFastLongPasteAfterOpening(t *testing.T) {
	h := enterReadyQ(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Frame()
	paste := strings.Repeat("x := 1\n", longPaste/7+1)
	h.tt.SetClipboard(paste)
	h.burst(h.key(0, ui.KeyEnter), h.key(ui.Cmd, ui.KeyV))
	settleQ(h)
	if !h.a.code.on || draftOf(h.a) != paste || len(h.a.attachments) != 0 {
		t.Errorf("block on %v, code %d bytes, %d attachments; want the paste as the code, no attachment", h.a.code.on, len(draftOf(h.a)), len(h.a.attachments))
	}
}

// A word or line Backspace after typing that came as the focus moved
// takes nothing from the piece left: the code stays as it was.
func TestFastWordBackspaceAfterDown(t *testing.T) {
	for _, m := range []ui.Modifiers{ui.Alt, ui.Ctrl, ui.Cmd} {
		h := burstBlock(t, "abc")
		h.burst(h.key(0, ui.KeyDown), h.typ("xy"), h.key(m, ui.KeyBackspace))
		settleQ(h)
		if draftOf(h.a) != "abc" {
			t.Errorf("mods %v: draft %v; want abc kept in the block", m, h.a.code)
		}
	}
}
