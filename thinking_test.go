package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/acp/acptest"
	"glitched/internal/provider/fake"
)

// held plays a provider whose agents say nothing until gate closes, as a
// model does while it thinks before its first word.
type held struct {
	*fake.Adapter
	gate chan struct{}
}

func (h held) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := h.Adapter.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &heldAgent{Agent: ag, gate: h.gate}, nil
}

type heldAgent struct {
	provider.Agent
	gate chan struct{}
}

func (g *heldAgent) Next(ctx context.Context) (provider.Event, error) {
	select {
	case <-g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return g.Agent.Next(ctx)
}

// thinks is whether the thread's transcript says the agent is at work.
func thinks(h *harness) bool {
	for range 5 {
		h.tt.Advance(100 * time.Millisecond)
	}
	return h.tt.HasText("Thinking…")
}

// A Claude thread says it is thinking from the moment its message is taken
// until the turn ends, though the agent says nothing for a while.
func TestAClaudeThreadShowsItThinksBeforeItsFirstWord(t *testing.T) {
	gate := make(chan struct{})
	h := startWith(t, tempDir(t), func(cfg *daemon.Config) {
		cfg.Providers["claude"] = held{fake.New([]provider.Event{provider.TextDelta{Text: "Here is the plan."}}), gate}
	})
	sendNew(h, "Plan it.")
	h.until("the run", func() bool { return h.a.current().View.Working() })
	if p := h.a.current().View.Model.Provider; p != "claude" {
		t.Fatalf("the turn runs on %q", p)
	}
	if !thinks(h) {
		t.Fatalf("a Claude thread with no word yet shows no sign of work: %q", h.tt.Texts())
	}
	shoot(t, h, "claude-thinking")
	close(gate)
	h.until("the turn's end", func() bool { return !h.a.current().View.Working() })
	if thinks(h) {
		t.Fatalf("the thread still says it thinks after its turn: %q", h.tt.Texts())
	}
}

// An Antigravity thread says it is thinking from its message until its turn
// ends: before its first word whether or not agy sends thoughts, and
// between its steps, where its steps card no longer shimmers.
func TestAnAntigravityThreadShowsItThinks(t *testing.T) {
	for _, scenario := range []string{"thinking", "thinking-silent", "thinking-tools"} {
		t.Run(scenario, func(t *testing.T) {
			gate := filepath.Join(t.TempDir(), "gate")
			t.Setenv(acptest.Gate, gate)
			h := startWith(t, tempDir(t), withAntigravity(t, scenario))
			allAnswered(h)
			h.until("agy's models", func() bool { return len(h.a.models.reported["antigravity"]) == 2 })
			h.a.model = modelKey{"antigravity", "fake-pro"}
			sendNew(h, "Plan it.")
			h.until("the run", func() bool { return h.a.current().View.Working() })
			if scenario == "thinking-tools" {
				h.until("the first step", func() bool { return h.tt.HasText("Read notes.txt") || h.tt.HasText("Worked for 0s") })
			}
			if !thinks(h) {
				t.Fatalf("an Antigravity thread at work shows no sign of it: %q", h.tt.Texts())
			}
			shoot(t, h, "antigravity-"+scenario)
			if err := os.WriteFile(gate, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			h.until("the turn's end", func() bool { return !h.a.current().View.Working() })
			if thinks(h) {
				t.Fatalf("the thread still says it thinks after its turn: %q", h.tt.Texts())
			}
		})
	}
}
