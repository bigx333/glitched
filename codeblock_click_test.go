package main

import (
	"strings"
	"testing"
	"time"

	"glitched/internal/provider/fake"

	"github.com/egoist/mygo/ui"
)

// Double click a word in the code while the caret is in the empty text
// under the block (Down): the word clicked is the one replaced.
func TestDoubleClickIntoTheBlockFromEmptyText(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.tt.Type("alpha beta")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("gamma delta")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("eps zeta")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyUp)
	h.tt.Frame()
	c, _ := h.tt.TextCaret() // end of "gamma delta", nothing under the block
	g0, _ := h.tt.Find("Plain text")
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown) // to the empty text under the block
	h.tt.Frame()
	if !belowOf(h.a) {
		t.Fatalf("no text under the block: %v", h.a.code)
	}
	g1, _ := h.tt.Find("Plain text")
	shift := g0.Y - g1.Y // the code as it shows now, over the empty line
	x, y := c.X-12, c.Y+c.H/2-shift
	h.tt.ClickAt(x, y)
	h.tt.ClickAt(x, y)
	h.tt.Frame()
	h.tt.Type("X")
	h.tt.Frame()
	if got := draftOf(h.a); got != "alpha beta\ngamma X\neps zeta" {
		t.Errorf("double clicked delta, typed X: code %q; want %q (draft %v)", got, "alpha beta\ngamma X\neps zeta", h.a.code)
	}
}

// Drag over "gamma delta" in the code while the caret is in the empty
// text under the block: the line dragged over is the one replaced.
func TestDragSelectIntoTheBlockFromEmptyText(t *testing.T) {
	for _, below := range []bool{false, true} {
		h := enterReady(t, fake.New(fake.Turn(false)))
		h.tt.Type("```")
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Frame()
		h.tt.Type("alpha beta")
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Type("gamma delta")
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Type("eps zeta")
		h.tt.Frame()
		h.tt.Key(0, ui.KeyUp)
		h.tt.Frame()
		end, _ := h.tt.TextCaret() // end of "gamma delta"
		h.tt.Key(0, ui.KeyHome)
		h.tt.Frame()
		home, _ := h.tt.TextCaret()
		if home.X >= end.X {
			h.tt.Key(ui.Cmd, ui.KeyLeft)
			h.tt.Frame()
			home, _ = h.tt.TextCaret()
		}
		g0, _ := h.tt.Find("Plain text")
		h.tt.Key(0, ui.KeyDown)
		h.tt.Frame()
		shift := float32(0)
		if below {
			h.tt.Key(0, ui.KeyEnd)
			h.tt.Frame()
			h.tt.Key(0, ui.KeyDown) // to the empty text under the block
			h.tt.Frame()
			if !belowOf(h.a) {
				t.Fatalf("no text under the block: %v", h.a.code)
			}
			g1, _ := h.tt.Find("Plain text")
			shift = g0.Y - g1.Y
		}
		y := home.Y + home.H/2 - shift
		h.tt.Press(home.X+1, y)
		h.tt.Frame()
		for i := 1; i <= 4; i++ {
			h.tt.Move(home.X+1+(end.X+60-home.X-1)*float32(i)/4, y)
			h.tt.Frame()
		}
		h.tt.Release(end.X+60, y)
		h.tt.Frame()
		h.tt.Type("X")
		h.tt.Frame()
		if got := draftOf(h.a); got != "alpha beta\nX\neps zeta" {
			t.Errorf("empty line under the block %v: dragged over gamma delta, typed X: code %q; want %q", below, got, "alpha beta\nX\neps zeta")
		}
	}
}

// In the new thread's card, a click on "delta" in the code, the pointer
// moving a pixel before it's released, while the caret is in the empty
// text over the block (Up): the caret goes there, nothing selected.
func TestHeroClickIntoTheBlockFromEmptyText(t *testing.T) {
	for _, above := range []bool{false, true} {
		h := start(t, inboxDir(t))
		h.click("New thread")
		h.tt.Advance(time.Second)
		h.tt.Type("```")
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Frame()
		h.tt.Type("alpha beta")
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Type("gamma delta")
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Type("eps zeta")
		h.tt.Frame()
		h.tt.Key(0, ui.KeyUp)
		h.tt.Frame()
		c, _ := h.tt.TextCaret() // end of "gamma delta"
		g0, _ := h.tt.Find("Plain text")
		shift := float32(0)
		if above {
			h.tt.Key(0, ui.KeyUp)
			h.tt.Frame()
			h.tt.Key(0, ui.KeyUp) // to the empty text over the block
			h.tt.Frame()
			if !proseOf(h.a) {
				t.Fatalf("no text over the block: %v", h.a.code)
			}
			g1, _ := h.tt.Find("Plain text")
			shift = g1.Y - g0.Y
		} else {
			h.tt.Key(0, ui.KeyDown)
			h.tt.Frame()
		}
		x, y := c.X-14, c.Y+c.H/2+shift
		h.tt.Press(x, y)
		h.tt.Frame()
		h.tt.Move(x+1, y)
		h.tt.Frame()
		h.tt.Release(x+1, y)
		h.tt.Frame()
		h.tt.Type("X")
		h.tt.Frame()
		if got := draftOf(h.a); strings.Replace(got, "X", "", 1) != "alpha beta\ngamma delta\neps zeta" {
			t.Errorf("empty line over the block %v (shift %v): clicked in delta, typed X: code %q", above, shift, got)
		}
	}
}

// A single click on "delta" in the code, the pointer moving a pixel
// before it's released, as a hand's does, while the caret is in the empty
// text under the block: the caret goes there, nothing selected.
func TestClickIntoTheBlockFromEmptyText(t *testing.T) {
	for _, below := range []bool{false, true} {
		h := enterReady(t, fake.New(fake.Turn(false)))
		h.tt.Type("```")
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Frame()
		h.tt.Type("alpha beta")
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Type("gamma delta")
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Type("eps zeta")
		h.tt.Frame()
		h.tt.Key(0, ui.KeyUp)
		h.tt.Frame()
		c, _ := h.tt.TextCaret() // end of "gamma delta"
		g0, _ := h.tt.Find("Plain text")
		h.tt.Key(0, ui.KeyDown)
		h.tt.Frame()
		shift := float32(0)
		if below {
			h.tt.Key(0, ui.KeyDown)
			h.tt.Frame()
			if !belowOf(h.a) {
				t.Fatalf("no text under the block: %v", h.a.code)
			}
			g1, _ := h.tt.Find("Plain text")
			shift = g0.Y - g1.Y
		}
		x, y := c.X-14, c.Y+c.H/2-shift
		h.tt.Press(x, y)
		h.tt.Frame()
		h.tt.Move(x+1, y)
		h.tt.Frame()
		h.tt.Release(x+1, y)
		h.tt.Frame()
		h.tt.Type("X")
		h.tt.Frame()
		if got := draftOf(h.a); strings.Replace(got, "X", "", 1) != "alpha beta\ngamma delta\neps zeta" {
			t.Errorf("empty line under the block %v: clicked in delta, typed X: code %q", below, got)
		}
	}
}

// An empty line a click left goes as a key moves the caret on, out of the
// piece clicked into too.
func TestEmptyLineLeftByAClickGoesAsAnArrowLeaves(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	defer h.a.backend.Close()
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.tt.Type("code")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("more")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	if !belowOf(h.a) {
		t.Fatal("no empty line under the block")
	}
	g, _ := h.tt.Find("Plain text")
	h.tt.ClickAt(g.X+20, g.Y+g.H+12) // the code's first line
	h.tt.Frame()
	h.tt.Key(0, ui.KeyUp)
	h.tt.Frame()
	h.tt.Frame()
	if belowOf(h.a) {
		t.Errorf("Up out of the code after a click: the empty line under the block still shows: %v", h.a.code)
	}
}

// Text the caret left by a click, holding only line breaks, goes as an
// arrow would have it go, once the caret's piece is written in.
func TestBlankTextLeftByAClickGoes(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	defer h.a.backend.Close()
	h.tt.Type("```")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.tt.Type("code")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Frame()
	g, _ := h.tt.Find("Plain text")
	h.tt.ClickAt(g.X+300, g.Y+g.H+12) // the code's line
	h.tt.Frame()
	h.tt.Type("Q")
	h.tt.Frame()
	if draftOf(h.a) != "codeQ" {
		t.Fatalf("Q went elsewhere: %v", h.a.code)
	}
	if belowOf(h.a) {
		t.Errorf("text left holding only a line break still shows: %q", afterOf(h.a))
	}
}
