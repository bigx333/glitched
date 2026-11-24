package codex

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/codex/codextest"
)

// replay is an adapter whose codex answers as the app-server that made
// the capture did.
func replay(t *testing.T, capture string) *Adapter {
	t.Helper()
	a, _ := fake(t, "replay")
	path, err := filepath.Abs(filepath.Join("testdata", capture))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(codextest.Replay, path)
	return a
}

// Two turns of codex 0.171.0 on gpt-6-luna, captured on Praetor: the
// second goes to the same app-server and remembers the first. Each says
// how full the context is as of its last request, 21,958 of the second's
// thread total of 43,887.
func TestCodex0171TurnsReplay(t *testing.T) {
	a := replay(t, "codex-0.171.0-turns.wire")
	s := spec(t)
	for _, want := range []struct {
		text    string
		context provider.Context
		tokens  int64
	}{
		{"The glitched tools are for handing tasks to another agent; kumquat.", provider.Context{Used: 21929, Window: 258400}, 21929},
		{"kumquat", provider.Context{Used: 21958, Window: 258400}, 21958},
	} {
		s.Run = api.NewID()
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "the captured prompt"})
		events := turn(t, g)
		closeRun(t, g)
		if end := ended(events); end.Outcome != api.OutcomeCompleted || text(events) != want.text {
			t.Errorf("the turn said %q and ended %+v, want %q", text(events), end, want.text)
		}
		var got []provider.Context
		for _, e := range events {
			if c, ok := e.(provider.Context); ok {
				got = append(got, c)
			}
		}
		if len(got) != 1 || got[0] != want.context {
			t.Errorf("the turn reported its context as %+v, want %+v", got, want.context)
		}
		if end := ended(events); end.Tokens != want.tokens {
			t.Errorf("the turn used %d tokens, want Codex's %d", end.Tokens, want.tokens)
		}
		reports, _ := confirmed(events)
		if want := []provider.Event{provider.Granted{Access: api.AccessAsk, Mode: "read-only, on-request, user"}, provider.Ran{Model: "gpt-6-luna", Effort: "low"}}; !slices.Equal(reports, want) {
			t.Errorf("the turn reported %+v, want %+v", reports, want)
		}
	}
}

// A command codex 0.171.0 asked to run in the read-only sandbox, captured
// on Praetor: it shows on Glitched's card, without the session grant its
// decisions leave out, and the allowed command ran.
func TestCodex0171ApprovalReplays(t *testing.T) {
	a := replay(t, "codex-0.171.0-approval.wire")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "the captured prompt"})
	_, req := until[provider.RequestOpened](t, g)
	var b map[string]string
	json.Unmarshal(req.Body, &b)
	if req.Kind != api.KindApproval || b["command"] != `/bin/zsh -lc "touch hello.txt && printf 'hi' > hello.txt"` || b["cwd"] != "/tmp/workspace" || b["reason"] == "" {
		t.Errorf("card %s %s", req.Kind, req.Body)
	}
	if offersSession(req.Body) {
		t.Errorf("card %s offers a session grant", req.Body)
	}
	if err := g.Do(context.Background(), provider.Respond{Request: req.ID, Answer: json.RawMessage(sessionAnswer)}); err == nil {
		t.Error("an answer for the session went through")
	}
	do(t, g, provider.Respond{Request: req.ID, Answer: json.RawMessage(`{"allow":true}`)})
	events := turn(t, g)
	closeRun(t, g)
	ws := works(events)
	if end := ended(events); end.Outcome != api.OutcomeCompleted || len(ws) == 0 || ws[len(ws)-1].Status != api.WorkCompleted || ws[len(ws)-1].Exit == nil || *ws[len(ws)-1].Exit != 0 {
		t.Errorf("the turn %+v", events)
	}
}
