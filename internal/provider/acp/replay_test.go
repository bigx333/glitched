package acp

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/acp/acptest"
)

func contexts(events []provider.Event) []provider.Context {
	var out []provider.Context
	for _, e := range events {
		if c, ok := e.(provider.Context); ok {
			out = append(out, c)
		}
	}
	return out
}

// A thread's two turns as agy 1.5.14 ran them, signed in, through
// `glitched run -provider antigravity`: "say hi and list the files here",
// whose ls asked, then a follow-up the same session remembers. The
// capture, redacted, replays through the fake.
func TestAgy1514Replays(t *testing.T) {
	a, log := fake(t, "replay")
	capture, err := filepath.Abs("testdata/agy-1.5.14-hi.wire")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(acptest.Replay, capture)
	s := spec(t)
	s.Model = api.ModelRef{Provider: "antigravity", Model: "gemini-3.8-flash"}

	g := open(t, a, s)
	do(t, g, provider.Start{Text: "say hi and list the files here"})
	var events []provider.Event
	for {
		e := next(t, g)
		events = append(events, e)
		if r, ok := e.(provider.RequestOpened); ok {
			var body map[string]string
			json.Unmarshal(r.Body, &body)
			if body["command"] != "ls -la" || body["reason"] != "List directory contents" || body["cwd"] != s.Workspace {
				t.Fatalf("the card %s", r.Body)
			}
			do(t, g, provider.Respond{Request: r.ID, Answer: json.RawMessage(`{"allow":true}`)})
		}
		if _, ok := e.(provider.TurnEnded); ok {
			break
		}
	}
	closeRun(t, g)
	if got, want := text(events), "Hi! Here are the files in the current directory:\n\n- [hello.txt](file:///work/ws/hello.txt)\n- [notes.md](file:///work/ws/notes.md)"; got != want {
		t.Fatalf("text %q, want %q", got, want)
	}
	i := slices.IndexFunc(events, func(e provider.Event) bool { w, ok := e.(provider.Work); return ok && w.Status == api.WorkCompleted })
	if i < 0 {
		t.Fatalf("no finished step: %+v", events)
	}
	if w := events[i].(provider.Work); w.Kind != "command" || w.Title != "ls -la" || w.Exit == nil || *w.Exit != 0 {
		t.Fatalf("step %+v", w)
	}
	if e := ended(t, events); e.Outcome != api.OutcomeCompleted {
		t.Fatalf("ended %+v", e)
	}
	if got, want := contexts(events), []provider.Context{{Used: 10756, Window: 1048576}, {Used: 11469, Window: 1048576}}; !slices.Equal(got, want) {
		t.Fatalf("the first turn's context %+v, want %+v: the session's empty start reports nothing", got, want)
	}
	var answer string
	json.Unmarshal(find(t, wire(t, log), "permission answered")[0].Params, &answer)
	if answer != "allow" {
		t.Fatalf("agy heard %q", answer)
	}

	g = open(t, a, s)
	do(t, g, provider.Start{Text: "In one short sentence, which two files did you just list? Do not run any tool."})
	second := turn(t, g)
	if got, want := text(second), "I just listed [hello.txt](file:///work/ws/hello.txt) and [notes.md](file:///work/ws/notes.md)."; got != want {
		t.Fatalf("second text %q, want %q", got, want)
	}
	if got, want := contexts(second), []provider.Context{{Used: 11858, Window: 1048576}}; !slices.Equal(got, want) {
		t.Fatalf("the second turn's context %+v, want %+v", got, want)
	}
	closeRun(t, g)
	if n := len(find(t, wire(t, log), "initialize")); n != 1 {
		t.Fatalf("%d starts for one thread's two turns", n)
	}
}
