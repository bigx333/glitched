package acp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/acp/acptest"
)

// resumeFor runs one turn on a new session of the fake's scenario, then
// resumes it, ResumeOnly, on another process, as idle compaction does.
func resumeFor(t *testing.T, scenario string) (provider.Agent, string) {
	t.Helper()
	t.Setenv(acptest.DefaultModel, "gemini-3-pro")
	a, log := fake(t, scenario)
	s := spec(t)
	first := open(t, a, s)
	do(t, first, provider.Start{Text: "Remember kumquat."})
	turn(t, first)
	id := first.(provider.Sessioned).Session()
	closeRun(t, first)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b := &Adapter{Profile: Antigravity, Path: a.Path}
	t.Cleanup(func() { b.Close() })
	s.Run, s.Generation, s.Session, s.ResumeOnly = api.NewID(), 2, id, true
	return open(t, b, s), log
}

// agy lists its commands only after the resume answers, and compacts with
// no compaction update and no text: /compact waits for the list, goes out
// as it is, and its quiet end is the compaction. Its usage reading, of
// the summarizing request, is no reading of the session.
func TestSlashCompactOnAgyWaitsForItsCommandsAndEndsCompacted(t *testing.T) {
	g, log := resumeFor(t, "compact-agy")
	do(t, g, provider.Start{Text: "/compact"})
	events := turn(t, g)
	closeRun(t, g)
	if !slices.Contains(events, provider.Event(provider.Context{Compacted: true})) {
		t.Errorf("events %#v, want Context{Compacted: true}", events)
	}
	for _, e := range events {
		if c, ok := e.(provider.Context); ok && c.Used > 0 {
			t.Errorf("the compaction reported a reading %+v", c)
		}
		if end, ok := e.(provider.TurnEnded); ok && end.Outcome != api.OutcomeCompleted {
			t.Errorf("the compaction ended %+v, want completed", end)
		}
	}
	prompts := find(t, wire(t, log), "session/prompt")
	var last struct{ Prompt []struct{ Text string } }
	json.Unmarshal(prompts[len(prompts)-1].Params, &last)
	if got := last.Prompt[0].Text; got != "/compact" {
		t.Errorf("the compaction sent %q, want /compact as it is", got)
	}
}

// An agent that answers /compact with text did not compact: the run
// fails, and says nothing compacted.
func TestSlashCompactAnsweredWithTextFails(t *testing.T) {
	g, _ := resumeFor(t, "compact-replies")
	do(t, g, provider.Start{Text: "/compact"})
	events := turn(t, g)
	closeRun(t, g)
	for _, e := range events {
		if c, ok := e.(provider.Context); ok && c.Compacted {
			t.Error("an answered /compact reported a compaction")
		}
		if end, ok := e.(provider.TurnEnded); ok && end.Outcome != api.OutcomeFailed {
			t.Errorf("an answered /compact ended %+v, want failed", end)
		}
	}
}

// A session that lists no commands takes no compaction: it is declined
// at open, and /compact never goes as text into the conversation.
func TestSlashCompactWithoutAListIsNotSent(t *testing.T) {
	was := commandWait
	commandWait = 300 * time.Millisecond
	t.Cleanup(func() { commandWait = was })
	t.Setenv(acptest.DefaultModel, "gemini-3-pro")
	a, log := fake(t, "compact-unlisted")
	s := spec(t)
	first := open(t, a, s)
	do(t, first, provider.Start{Text: "Remember kumquat."})
	turn(t, first)
	id := first.(provider.Sessioned).Session()
	closeRun(t, first)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b := &Adapter{Profile: Antigravity, Path: a.Path}
	t.Cleanup(func() { b.Close() })
	s.Run, s.Generation, s.Session, s.ResumeOnly = api.NewID(), 2, id, true
	if g, err := b.Open(context.Background(), s); !errors.Is(err, provider.ErrDeclined) {
		if g != nil {
			closeRun(t, g)
		}
		t.Fatalf("a compaction on a session that listed no commands opened: %v, want it declined", err)
	}
	if got := len(find(t, wire(t, log), "session/prompt")); got != 1 {
		t.Errorf("sent %d prompts, want only the first turn's", got)
	}
}

// A first prompt that starts with a path is no command: it still carries
// the MCP servers' instructions.
func TestAPromptStartingWithAPathIsBriefed(t *testing.T) {
	a, log := fake(t, "compact")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "/tmp/example.txt is the input."})
	turn(t, g)
	closeRun(t, g)
	prompts := find(t, wire(t, log), "session/prompt")
	var p struct{ Prompt []struct{ Text string } }
	json.Unmarshal(prompts[0].Params, &p)
	if got := p.Prompt[0].Text; !strings.Contains(got, "Use delegate_task.") {
		t.Errorf("the prompt went as %q, want the MCP instructions in front", got)
	}
}
