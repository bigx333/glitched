package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/provider"
)

// lastRequest is the thread's last request, as its transcript has it.
func lastRequest(th thread) *client.RequestEntry {
	es := entriesOf(th)
	for i := len(es) - 1; i >= 0; i-- {
		if es[i].request != nil {
			return es[i].request
		}
	}
	return nil
}

// The card says what the agent wants to run, where and why, and the work
// before it is paused.
func TestApprovalCardSaysWhatAndWhy(t *testing.T) {
	h, _ := asking(t, runTests)
	for _, s := range []string{"bun run test -- Sidebar", "/work/t3code", "Runs the sidebar's tests to check the new sort.", "Paused for approval"} {
		if !h.tt.HasText(s) {
			t.Errorf("no %q on the waiting card; texts %q", s, h.tt.Texts())
		}
	}
}

// With the composer empty, ⌘⏎ allows the request once and the turn goes on.
func TestCmdEnterAllows(t *testing.T) {
	h, th := asking(t, runTests)
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.until("the turn to finish", func() bool {
		return strings.Contains(texts(entriesOf(th)), "The tests pass.") && !h.a.current().View.Working()
	})
	h.tt.Advance(time.Second)
	if !h.tt.HasText("The tests pass.") || !h.tt.HasText("Allowed") {
		t.Errorf("the finished turn shows %q", h.tt.Texts())
	}
	if a := answerOf(lastRequest(th).Answer); a != client.AllowOnce {
		t.Errorf("⌘⏎ answered %v, want allow once", a)
	}
}

// ⇧⌘D denies the request.
func TestShiftCmdDDenies(t *testing.T) {
	h, th := asking(t, runTests)
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyD)
	h.until("the denial", func() bool { return h.tt.HasText("Denied") })
	if a := answerOf(lastRequest(th).Answer); a != client.Deny {
		t.Errorf("⇧⌘D answered %v, want deny", a)
	}
}

// The card offers Allow once and Deny only, even for a request that names
// a rule: no provider lets one through without asking again.
func TestNoAlwaysAllow(t *testing.T) {
	h, _ := asking(t, []provider.Event{
		provider.RequestOpened{ID: "run-1", Kind: "approval", Body: json.RawMessage(`{"operation":"run","command":"bun run test","rule":"bun run test:*"}`)},
	})
	if !h.tt.HasText("Allow once") || !h.tt.HasText("Deny") || h.tt.HasText("Always allow") || h.tt.HasText("bun run test:*") {
		t.Errorf("the card's answers: %q", h.tt.Texts())
	}
}

// With a draft, ⌘⏎ sends it, which steers the agent, and leaves the
// request waiting.
func TestCmdEnterWithADraftSteers(t *testing.T) {
	h, th := asking(t, runTests)
	h.a.draft = "use the other runner"
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.until("the steer", func() bool { return h.tt.HasText("steered:") })
	if r := lastRequest(th); r.State != api.RequestPending {
		t.Errorf("the request is %v after a steer, want still waiting", r.State)
	}
}
