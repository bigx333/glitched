//go:build codexlive

package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// These run the user's real, signed-in codex, and spend a few prompts of
// the account's quota on its cheapest model at its lowest effort:
//
//	GLITCHED_CODEX_WIRE=/tmp/codex.wire go test -tags codexlive -run Live ./internal/provider/codex/

func real(t *testing.T) (*Adapter, provider.Model) {
	t.Helper()
	a := &Adapter{}
	t.Cleanup(func() { a.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	info, err := a.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("codex %s, %d models", info.Version, len(info.Models))
	for _, m := range info.Models {
		t.Logf("  %s (%s) efforts %v", m.ID, m.Name, m.Efforts)
	}
	cheap := info.Models[0]
	for _, m := range info.Models {
		if strings.Contains(m.ID, "luna") {
			cheap = m
			break
		}
	}
	return a, cheap
}

func liveSpec(t *testing.T, m provider.Model) provider.SessionSpec {
	effort := ""
	if len(m.Efforts) > 0 {
		effort = m.Efforts[0].ID
	}
	return provider.SessionSpec{
		Thread: api.NewID(), Run: api.NewID(), Workspace: t.TempDir(),
		Model: api.ModelRef{Provider: "codex", Model: m.ID, Effort: effort}, Access: api.AccessAsk,
		MCP: []provider.MCPServer{{Name: "glitched", Command: "/usr/bin/true", Instructions: "The glitched tools start child threads: call delegate_task to hand a task to another agent.", Trusted: true}},
	}
}

func logEvents(t *testing.T, events []provider.Event) {
	for _, e := range events {
		t.Logf("  %T %+v", e, e)
	}
}

func TestLiveDescribe(t *testing.T) {
	real(t)
}

// Two turns in one thread: the second reuses the app-server and
// remembers the first; developerInstructions reach the model.
func TestLiveTurns(t *testing.T) {
	a, m := real(t)
	s := liveSpec(t, m)
	for i, prompt := range []string{
		"In one short sentence, say what your developer instructions say the glitched tools are for. Then say the word kumquat.",
		"What word did you just say at the end? Answer with that one word only.",
	} {
		s.Run = api.NewID()
		g := open(t, a, s)
		start := time.Now()
		do(t, g, provider.Start{Text: prompt})
		events := turn(t, g)
		closeRun(t, g)
		t.Logf("turn %d in %s: %q", i+1, time.Since(start), text(events))
		logEvents(t, events)
		if end := ended(events); end.Outcome != api.OutcomeCompleted {
			t.Fatalf("turn %d ended %+v", i+1, end)
		}
	}
}

// A file write in the read-only sandbox asks on Glitched's card, and the
// allowed write lands.
func TestLiveApproval(t *testing.T) {
	a, m := real(t)
	s := liveSpec(t, m)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Create a file named hello.txt in the current directory containing exactly: hi. The sandbox is read-only, so request approval to write it; do not give up. Do nothing else."})
	var events []provider.Event
	for {
		e := next(t, g)
		events = append(events, e)
		if r, ok := e.(provider.RequestOpened); ok {
			t.Logf("card %s %s", r.Kind, r.Body)
			if r.Kind == api.KindApproval {
				do(t, g, provider.Respond{Request: r.ID, Answer: json.RawMessage(`{"allow":true}`)})
			}
		}
		if _, ok := e.(provider.TurnEnded); ok {
			break
		}
	}
	closeRun(t, g)
	logEvents(t, events)
	b, err := os.ReadFile(filepath.Join(s.Workspace, "hello.txt"))
	t.Logf("hello.txt: %q, %v", b, err)
}
