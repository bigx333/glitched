package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/client"
)

// lastReply is the text of the last reply in t's transcript.
func lastReply(t *testing.T, th thread) string {
	t.Helper()
	for i := len(th.Transcript.Entries) - 1; i >= 0; i-- {
		if e, ok := th.Transcript.Entries[i].(*client.AssistantEntry); ok && e.End != nil {
			return e.Text
		}
	}
	t.Fatal("no reply in the transcript")
	return ""
}

// forkEntry is the fork entry t's transcript starts with, nil for none.
func forkEntry(th thread) *client.ForkEntry {
	if th.Transcript == nil || len(th.Transcript.Entries) == 0 {
		return nil
	}
	f, _ := th.Transcript.Entries[0].(*client.ForkEntry)
	return f
}

// A turn's reply forks the thread from its toolbar into a new thread,
// which opens; its timeline starts with the thread it goes on from, whose
// title opens it, and says how its first run took the conversation up.
// The source's transcript stays as it was.
func TestAReplyForksTheThread(t *testing.T) {
	h, src := checked(t)
	src = h.a.current()
	before := len(src.Transcript.Entries)
	pointAt(t, h, strings.TrimSpace(lastReply(t, src)))
	h.click("Fork from here")
	name := src.View.Title + " fork"
	fork := h.thread(name)
	if h.a.current().id() != fork.id() || fork.View.Model != src.View.Model || fork.View.Workspace != src.View.Workspace {
		t.Fatalf("the fork %+v isn't shown, on the source's model and folder", fork.View)
	}
	h.until("the fork's first row", func() bool { fork = h.a.current(); return forkEntry(fork) != nil })
	if f := forkEntry(fork); f.Thread != src.id() || f.Run != src.View.LastTurn.Run || f.Title != src.View.Title {
		t.Fatalf("the fork goes on from %+v", f.Forked)
	}
	h.tt.Frame()
	if !h.tt.HasText("Forked from") || !h.tt.HasText(src.View.Title) {
		t.Fatalf("the fork's timeline doesn't say where it goes on from: %q", h.tt.Texts())
	}
	h.a.draft = "Go on from there"
	h.click("Send")
	h.until("the fork's first turn", func() bool {
		fork = h.a.current()
		return !fork.View.Live() && fork.View.LastTurn != nil && forkEntry(fork) != nil && forkEntry(fork).Took != ""
	})
	h.tt.Advance(time.Second)
	if !h.tt.HasText("Portable") {
		t.Fatalf("the fork's first row doesn't say how its run took the conversation up: %q", h.tt.Texts())
	}
	if src, _ = h.a.find(src.id()); src.Transcript != nil && len(src.Transcript.Entries) != before {
		t.Fatalf("the source has %d entries, %d before the fork", len(src.Transcript.Entries), before)
	}
	if err := h.tt.Click("Open " + src.View.Title); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	if h.a.current().id() != src.id() {
		t.Fatal("the fork's first row doesn't open its source")
	}
}

// A thread row's menu forks it at its last turn, and continues it on
// another provider, which the menu lists without the thread's own.
func TestAThreadsMenuForksItOrContinuesItElsewhere(t *testing.T) {
	h, src := checked(t)
	if err := h.tt.RightClick(src.View.Title); err != nil {
		t.Fatal(err)
	}
	if m := h.tt.Menu(); !slices.Contains(m, "Fork") || !slices.Contains(m, "Continue on") {
		t.Fatalf("the menu %q has no Fork and Continue on", m)
	}
	other, name := "codex", "Codex"
	if src.View.Model.Provider == "codex" {
		other, name = "claude", "Claude"
	}
	if err := h.tt.ChooseMenuItem("Continue on", name); err != nil {
		t.Fatalf("%v; the menu has %q", err, h.tt.Menu())
	}
	fork := h.thread(src.View.Title + " on " + name)
	if fork.View.Model.Provider != other {
		t.Fatalf("the thread continued on %+v, want %s", fork.View.Model, other)
	}
	h.until("the fork's first row", func() bool { fork = h.a.current(); return forkEntry(fork) != nil })
	h.tt.Frame()
	if !h.tt.HasText("Continued from") {
		t.Fatalf("the thread's timeline doesn't say where it goes on from: %q", h.tt.Texts())
	}
	if f := forkEntry(fork); f.Run != src.View.LastTurn.Run || f.Provider != src.View.Model.Provider {
		t.Fatalf("it goes on from %+v", f.Forked)
	}
	h.tt.Advance(time.Second)
	if err := h.tt.RightClick(src.View.Title); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Fork"); err != nil {
		t.Fatal(err)
	}
	if fork := h.thread(src.View.Title + " fork"); fork.View.Model.Provider != src.View.Model.Provider {
		t.Fatalf("the fork is on %+v", fork.View.Model)
	}
}
