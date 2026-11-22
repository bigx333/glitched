package main

import (
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
)

// agy's question reaches Glitched's own card in an Antigravity thread, its
// options as agy offers them, and the turn waits for the answer, which agy
// gets as the option it offered.
func TestAnAntigravityQuestionIsAskedOnTheCard(t *testing.T) {
	h := startWith(t, tempDir(t), withAntigravity(t, "question"))
	allAnswered(h)
	h.until("agy's models", func() bool { return len(h.a.models.reported["antigravity"]) == 2 })
	h.a.model = modelKey{"antigravity", "fake-pro"}
	sendNew(h, "Ask me a question.")
	h.until("the card", func() bool { return h.tt.HasText("What is your favourite colour?") })
	for range 20 {
		h.tt.Advance(100 * time.Millisecond)
	}
	for _, want := range []string{"(Recommended) Blue", "Green", "Red", "Yellow", "Purple"} {
		if !h.tt.HasText(want) {
			t.Fatalf("the card shows no %q: %q", want, h.tt.Texts())
		}
	}
	if !h.a.current().View.Working() {
		t.Fatal("the turn ended without the answer")
	}
	shoot(t, h, "question")
	th := h.a.current()
	var asking *client.RequestEntry
	for _, e := range th.Transcript.Entries {
		if r, ok := e.(*client.RequestEntry); ok && r.State == api.RequestPending {
			asking = r
		}
	}
	if asking == nil {
		t.Fatal("no question waits on the user")
	}
	h.a.answerQuestions(th, asking, api.Answers{Answers: []api.Answer{{Picked: []string{"Red"}}}})
	h.until("the turn's end", func() bool { return !h.a.current().View.Working() })
	for range 50 {
		h.tt.Advance(100 * time.Millisecond)
	}
	if got := strings.Join(h.tt.Texts(), " "); !strings.Contains(got, `"q1":"3"`) {
		t.Fatalf("agy got no Red: %q", got)
	}
	shoot(t, h, "question-answered")
}
