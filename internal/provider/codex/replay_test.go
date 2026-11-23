package codex

import (
	"encoding/json"
	"path/filepath"
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
// second goes to the same app-server and remembers the first.
func TestCodex0171TurnsReplay(t *testing.T) {
	a := replay(t, "codex-0.171.0-turns.wire")
	s := spec(t)
	for _, want := range []string{"The glitched tools are for handing tasks to another agent; kumquat.", "kumquat"} {
		s.Run = api.NewID()
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "the captured prompt"})
		events := turn(t, g)
		closeRun(t, g)
		if end := ended(events); end.Outcome != api.OutcomeCompleted || text(events) != want {
			t.Errorf("the turn said %q and ended %+v, want %q", text(events), end, want)
		}
	}
}

// A command codex 0.171.0 asked to run in the read-only sandbox, captured
// on Praetor: it shows on Glitched's card, and the allowed command ran.
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
	do(t, g, provider.Respond{Request: req.ID, Answer: json.RawMessage(`{"allow":true}`)})
	events := turn(t, g)
	closeRun(t, g)
	ws := works(events)
	if end := ended(events); end.Outcome != api.OutcomeCompleted || len(ws) == 0 || ws[len(ws)-1].Status != api.WorkCompleted || ws[len(ws)-1].Exit == nil || *ws[len(ws)-1].Exit != 0 {
		t.Errorf("the turn %+v", events)
	}
}
