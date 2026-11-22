package acp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// recall runs a thread's turn on a new adapter, as an engine that
// restarted does: nothing of the first one's processes is left.
func recall(t *testing.T, scenario string, between func(log string)) (first, second provider.Agent, events []provider.Event, log string) {
	t.Helper()
	a, log := fake(t, scenario)
	s := spec(t)
	first = open(t, a, s)
	do(t, first, provider.Start{Text: "Remember the word kumquat."})
	turn(t, first)
	closeRun(t, first)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if between != nil {
		between(log)
	}
	b := &Adapter{Profile: Antigravity, Path: a.Path}
	t.Cleanup(func() { b.Close() })
	s.Run, s.Generation, s.Session = api.NewID(), 2, first.(provider.Sessioned).Session()
	second = open(t, b, s)
	do(t, second, provider.Start{Text: "What word did I ask you to remember?"})
	events = turn(t, second)
	closeRun(t, second)
	return first, second, events, log
}

func params(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// A thread's run after a restart resumes the session its first run bound,
// with the same MCP servers session/new had, and remembers the first
// turn. Its first prompt is briefed again: that the session was bound
// doesn't say a prompt of it ever went out.
func TestAThreadResumesItsSession(t *testing.T) {
	first, second, events, log := recall(t, "memory", nil)
	id := first.(provider.Sessioned).Session()
	if id == "" || second.(provider.Sessioned).Session() != id {
		t.Fatalf("sessions %q then %q", id, second.(provider.Sessioned).Session())
	}
	if got := text(events); !strings.Contains(got, "kumquat") {
		t.Fatalf("the resumed turn says %q", got)
	}
	entries := wire(t, log)
	news, resumes := find(t, entries, "session/new"), find(t, entries, "session/resume")
	if len(news) != 1 || len(resumes) != 1 {
		t.Fatalf("calls %v", methods(entries))
	}
	n, r := params(t, news[0].Params), params(t, resumes[0].Params)
	if string(n["mcpServers"]) != string(r["mcpServers"]) || string(r["sessionId"]) != `"`+id+`"` {
		t.Errorf("resume %s, after new %s", resumes[0].Params, news[0].Params)
	}
	prompts := find(t, entries, "session/prompt")
	if !strings.Contains(string(prompts[1].Params), "Use delegate_task.") {
		t.Errorf("the resumed session's first prompt has no instructions: %s", prompts[1].Params)
	}
}

// An agent that can only load a session loads it, and its replay of the
// conversation is not the run's.
func TestAThreadLoadsItsSessionWithoutItsReplay(t *testing.T) {
	_, _, events, log := recall(t, "memory-load-only", nil)
	got := text(events)
	if !strings.Contains(got, "kumquat") || strings.Contains(got, "replayed") {
		t.Fatalf("the loaded turn says %q", got)
	}
	entries := wire(t, log)
	if len(find(t, entries, "session/load")) != 1 || len(find(t, entries, "session/resume")) != 0 {
		t.Fatalf("calls %v", methods(entries))
	}
}

// A session the agent can't take up again, because it can't resume one
// or no longer has it, is replaced by a new one: the run says so, binds
// the new session, and briefs it again.
func TestALostSessionStartsANewOne(t *testing.T) {
	for name, c := range map[string]struct {
		scenario string
		between  func(log string)
	}{
		"no resume": {"memory-no-resume", nil},
		"not found": {"memory", func(log string) { os.Remove(log + ".sessions") }},
	} {
		t.Run(name, func(t *testing.T) {
			first, second, events, log := recall(t, c.scenario, c.between)
			if got := text(events); strings.Contains(got, "kumquat") {
				t.Fatalf("a new session remembers: %q", got)
			}
			if s := second.(provider.Sessioned).Session(); s == "" || s == first.(provider.Sessioned).Session() {
				t.Fatalf("the new run binds %q", s)
			}
			var told bool
			for _, e := range events {
				if w, ok := e.(provider.Work); ok && strings.Contains(w.Title, "no longer has this thread's earlier conversation") {
					told = true
				}
			}
			if !told {
				t.Errorf("the run doesn't say the conversation was lost: %#v", events)
			}
			entries := wire(t, log)
			if len(find(t, entries, "session/new")) != 2 {
				t.Fatalf("calls %v", methods(entries))
			}
			prompts := find(t, entries, "session/prompt")
			if !strings.Contains(string(prompts[1].Params), "Use delegate_task.") {
				t.Errorf("the new session's first prompt has no instructions: %s", prompts[1].Params)
			}
		})
	}
}

// A model the session refuses fails the run, and leaves the thread's
// agent, conversation and all, for its next run.
func TestARefusedModelKeepsTheThreadsAgent(t *testing.T) {
	a, log := fake(t, "reject-model")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "first"})
	turn(t, g)
	closeRun(t, g)
	s.Model.Model = "fake-flash"
	if _, err := a.Open(context.Background(), s); err == nil {
		t.Fatal("the refused model opened")
	}
	s.Model.Model = "fake-pro"
	g = open(t, a, s)
	defer closeRun(t, g)
	if n := len(find(t, wire(t, log), "initialize")); n != 1 {
		t.Fatalf("%d agents for one thread", n)
	}
}

// An agent that won't resume the session but can load it loads it, and
// the thread remembers.
func TestARefusedResumeLoadsTheConversation(t *testing.T) {
	_, second, events, log := recall(t, "memory-resume-fails", nil)
	if got := text(events); !strings.Contains(got, "kumquat") {
		t.Fatalf("the loaded turn says %q", got)
	}
	for _, e := range events {
		if _, ok := e.(provider.Work); ok {
			t.Fatalf("a loaded conversation was reported lost: %#v", events)
		}
	}
	entries := wire(t, log)
	loads := find(t, entries, "session/load")
	if len(find(t, entries, "session/resume")) != 1 || len(loads) != 1 || len(find(t, entries, "session/new")) != 1 {
		t.Fatalf("calls %v", methods(entries))
	}
	if l := params(t, loads[0].Params); string(l["sessionId"]) != `"`+second.(provider.Sessioned).Session()+`"` || !strings.Contains(string(l["mcpServers"]), "glitched") {
		t.Errorf("load %s", loads[0].Params)
	}
}

// A load that replays no message, only news of the session, has not
// found the conversation.
func TestALoadOfOnlyMetadataIsLost(t *testing.T) {
	first, second, events, _ := recall(t, "memory-load-metadata", func(log string) { os.Remove(log + ".sessions") })
	if s := second.(provider.Sessioned).Session(); s == first.(provider.Sessioned).Session() {
		t.Fatalf("the run kept the lost session %q", s)
	}
	var told bool
	for _, e := range events {
		if w, ok := e.(provider.Work); ok && strings.Contains(w.Title, "no longer has") {
			told = true
		}
	}
	if !told {
		t.Fatalf("no notice: %#v", events)
	}
}

// A model the session refuses leaves the thread's conversation where it
// was: the next run, at a model it takes, remembers.
func TestARefusedModelKeepsTheThreadsConversation(t *testing.T) {
	a, log := fake(t, "memory-reject-model")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Remember the word kumquat."})
	turn(t, g)
	closeRun(t, g)
	id := g.(provider.Sessioned).Session()
	s.Model.Model = "fake-flash"
	if _, err := a.Open(context.Background(), s); err == nil {
		t.Fatal("the refused model opened")
	}
	s.Model.Model, s.Session = "fake-pro", id
	g = open(t, a, s)
	defer closeRun(t, g)
	do(t, g, provider.Start{Text: "What word?"})
	if got := text(turn(t, g)); !strings.Contains(got, "kumquat") || g.(provider.Sessioned).Session() != id {
		t.Fatalf("after a refused model the thread says %q on %q", got, g.(provider.Sessioned).Session())
	}
	if n := len(find(t, wire(t, log), "initialize")); n != 1 {
		t.Fatalf("%d agents for one thread", n)
	}
}

// A new session that stands in for a lost one, and whose model is
// refused, is not kept: the thread's next run still says the
// conversation is gone.
func TestARefusedModelOnAStandInKeepsTheNotice(t *testing.T) {
	a, log := fake(t, "memory-reject-model")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Remember the word kumquat."})
	turn(t, g)
	closeRun(t, g)
	a.Close()
	os.Remove(log + ".sessions")
	b := &Adapter{Profile: Antigravity, Path: a.Path}
	t.Cleanup(func() { b.Close() })
	s.Session, s.Model.Model = g.(provider.Sessioned).Session(), "fake-flash"
	if _, err := b.Open(context.Background(), s); err == nil {
		t.Fatal("the refused model opened")
	}
	s.Model.Model = "fake-pro"
	g = open(t, b, s)
	defer closeRun(t, g)
	do(t, g, provider.Start{Text: "What word?"})
	var told bool
	for _, e := range turn(t, g) {
		if w, ok := e.(provider.Work); ok && strings.Contains(w.Title, "no longer has") {
			told = true
		}
	}
	if !told {
		t.Fatal("the run after a refused stand-in doesn't say the conversation was lost")
	}
}

// A thread's parked agent on another session than the one the run asks
// for is not the thread's: the run takes up the session it asked for.
func TestAParkedAgentOnAnotherSessionIsReplaced(t *testing.T) {
	a, _ := fake(t, "memory")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "conversation A"})
	turn(t, g)
	closeRun(t, g)
	other := spec(t)
	h := open(t, a, other)
	do(t, h, provider.Start{Text: "conversation B"})
	turn(t, h)
	closeRun(t, h)
	s.Session = h.(provider.Sessioned).Session()
	g = open(t, a, s)
	defer closeRun(t, g)
	do(t, g, provider.Start{Text: "which?"})
	if got := text(turn(t, g)); !strings.Contains(got, "conversation B") || strings.Contains(got, "conversation A") {
		t.Fatalf("asked for B, the run says %q", got)
	}
}
