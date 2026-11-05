package main

import (
	"context"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"

	"github.com/egoist/mygo/ui"
)

// enterReady is the app on a thread whose composer has the focus, on an
// engine whose turns are adapter's.
func enterReady(t *testing.T, adapter provider.Adapter, messages ...string) *harness {
	t.Helper()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, adapter)
	id := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	for _, m := range messages {
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: m}}); err != nil {
			t.Fatal(err)
		}
	}
	h := start(t, dir)
	h.a.select_(h.thread("Ship the sidebar sort"))
	h.tt.Advance(time.Second)
	return h
}

// sentTexts is what the user sent in the thread shown, as its transcript
// has it.
func sentTexts(h *harness) []string {
	var out []string
	if tr := h.a.current().Transcript; tr != nil {
		for _, e := range tr.Entries {
			if u, ok := e.(*client.UserEntry); ok {
				out = append(out, u.Text)
			}
		}
	}
	return out
}

// Enter sends the draft, as T3 Code's composer does, and clears it;
// Shift+Enter breaks the line, and neither it nor Alt+Enter sends.
func TestEnterSendsShiftEnterBreaksLine(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("hello")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.tt.Type("world")
	h.tt.Frame()
	if h.a.draft != "hello\nworld" || len(sentTexts(h)) != 0 {
		t.Fatalf("before Enter: draft %q, sent %q; want the lines broken, nothing sent", h.a.draft, sentTexts(h))
	}
	h.tt.Key(0, ui.KeyEnter)
	h.until("the message", func() bool { return slices.Contains(sentTexts(h), "hello\nworld") })
	if h.a.draft != "" {
		t.Errorf("after Enter: draft %q; want it cleared", h.a.draft)
	}
	// The text area was cleared too: what's typed next is all it holds.
	h.tt.Type("next")
	h.tt.Frame()
	if h.a.draft != "next" {
		t.Errorf("typed after Enter: draft %q; want %q", h.a.draft, "next")
	}
}

// Enter on an empty draft sends nothing and breaks no line; with the agent
// working, it doesn't stop it, as ⌘⏎ would.
func TestEnterOnEmptyDraftDoesNothing(t *testing.T) {
	h := enterReady(t, stalled{fake.New(midTurn), 1}, "go")
	h.until("working", func() bool { return h.a.current().View.Working() })
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Type("   ")
	h.tt.Key(0, ui.KeyEnter)
	for range 5 {
		h.tt.Advance(100 * time.Millisecond)
	}
	if !h.a.current().View.Live() {
		t.Error("Enter on an empty draft stopped the agent")
	}
	if h.a.draft != "   " || len(sentTexts(h)) != 1 {
		t.Errorf("draft %q, sent %q; want only the spaces typed, only the first message sent", h.a.draft, sentTexts(h))
	}
}

// With suggestions showing, Enter takes the one chosen rather than send.
func TestEnterTakesTheSuggestionFirst(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)))
	h.tt.Type("/mod")
	h.tt.Frame()
	if len(h.a.suggestions) == 0 {
		t.Fatal("no suggestions for /mod")
	}
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Advance(100 * time.Millisecond)
	if h.a.draft != "/model " || len(sentTexts(h)) != 0 {
		t.Errorf("draft %q, sent %q; want the suggestion taken, nothing sent", h.a.draft, sentTexts(h))
	}
}

// The keybindings in Settings say Enter sends and Shift+Enter breaks the
// line.
func TestKeybindingsSayEnterSends(t *testing.T) {
	want := map[string][]string{"Send message": {"↩"}, "New line in the message": {"⇧", "↩"}}
	for _, k := range keybindings {
		if w, ok := want[k.label]; ok {
			if !slices.Equal(k.keys, w) {
				t.Errorf("%s: %q, want %q", k.label, k.keys, w)
			}
			delete(want, k.label)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing keybindings: %v", want)
	}
}
