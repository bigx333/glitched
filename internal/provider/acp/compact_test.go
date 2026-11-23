package acp

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/acp/acptest"
)

// Initialize advertises session compaction so ACP agents emit compaction
// updates when /compact runs.
func TestInitializeAdvertisesCompactionCapability(t *testing.T) {
	a, log := fake(t, "compact")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	turn(t, g)
	closeRun(t, g)

	inits := find(t, wire(t, log), "initialize")
	if len(inits) != 1 {
		t.Fatalf("got %d initialize calls, want 1", len(inits))
	}
	var initReq struct {
		ClientCapabilities struct {
			Session *struct {
				Compaction json.RawMessage `json:"compaction"`
			} `json:"session"`
		} `json:"clientCapabilities"`
	}
	if err := json.Unmarshal(inits[0].Params, &initReq); err != nil {
		t.Fatal(err)
	}
	if initReq.ClientCapabilities.Session == nil || len(initReq.ClientCapabilities.Session.Compaction) == 0 {
		t.Fatalf("initialize params %s missing clientCapabilities.session.compaction", inits[0].Params)
	}
}

// ResumeOnly refuses an empty session and refuses falling back to session/new
// when the session cannot be resumed or loaded.
func TestResumeOnlyDoesNotFallbackToNewSession(t *testing.T) {
	a, log := fake(t, "memory")
	s := spec(t)
	s.ResumeOnly = true
	if _, err := a.Open(context.Background(), s); err == nil {
		t.Fatal("ResumeOnly with empty session opened, want error")
	}

	// Create a session, then delete the backing store and attempt ResumeOnly.
	s.ResumeOnly = false
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "remember"})
	turn(t, g)
	id := g.(provider.Sessioned).Session()
	closeRun(t, g)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	os.Remove(log + ".sessions")

	b := &Adapter{Profile: Antigravity, Path: a.Path}
	t.Cleanup(func() { b.Close() })
	s.Run, s.Generation, s.Session, s.ResumeOnly = api.NewID(), 2, id, true
	if _, err := b.Open(context.Background(), s); err == nil {
		t.Fatal("ResumeOnly with missing session opened, want error")
	}
	if got := len(find(t, wire(t, log), "session/new")); got != 1 {
		t.Fatalf("session/new called %d times, want 1 (no fallback)", got)
	}
}

// Resuming with ResumeOnly and running /compact sends /compact verbatim without
// prepending instructions, emits Context{Compacted: true}, and still briefs the
// next normal prompt.
func TestSlashCompactSkipsInstructionsAndEmitsCompacted(t *testing.T) {
	t.Setenv(acptest.DefaultModel, "gemini-3-pro")
	a, log := fake(t, "compact")
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
	g := open(t, b, s)
	do(t, g, provider.Start{Text: "/compact"})
	events := turn(t, g)
	closeRun(t, g)

	if !slices.Contains(events, provider.Event(provider.Context{Compacted: true})) {
		t.Fatalf("compact events %#v missing Context{Compacted: true}", events)
	}

	// Follow-up normal prompt on the same parked process must still get instructions.
	s.Run, s.Generation, s.ResumeOnly = api.NewID(), 3, false
	g2 := open(t, b, s)
	do(t, g2, provider.Start{Text: "Continue."})
	turn(t, g2)
	closeRun(t, g2)

	prompts := find(t, wire(t, log), "session/prompt")
	if len(prompts) != 3 {
		t.Fatalf("got %d prompts, want 3", len(prompts))
	}
	promptText := func(raw json.RawMessage) string {
		var p struct{ Prompt []struct{ Text string } }
		json.Unmarshal(raw, &p)
		return p.Prompt[0].Text
	}
	if got := promptText(prompts[1].Params); got != "/compact" {
		t.Errorf("compact prompt = %q, want exact %q (no instructions)", got, "/compact")
	}
	if got := promptText(prompts[2].Params); !strings.Contains(got, "Use delegate_task.") || !strings.HasSuffix(got, "Continue.") {
		t.Errorf("follow-up prompt = %q, want instructions preserved for normal turn", got)
	}
}

// When the agent advertises available commands without "compact", /compact fails
// before sending a prompt.
func TestSlashCompactRefusedWhenCommandUnavailable(t *testing.T) {
	a, log := fake(t, "compact-no-cmd")
	s := spec(t)
	g := open(t, a, s)
	defer closeRun(t, g)
	if err := g.Do(context.Background(), provider.Start{Text: "/compact"}); err == nil {
		t.Fatal("/compact succeeded when compact command was not advertised")
	}
	if got := len(find(t, wire(t, log), "session/prompt")); got != 0 {
		t.Fatalf("sent %d session/prompt calls, want 0", got)
	}
}
