//go:build unix

package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/provider"
)

// nextRequest reads ag's events up to its next request, or nil once its
// turn ends.
func nextRequest(t *testing.T, ag provider.Agent) *provider.RequestOpened {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, turnWait)
	defer cancel()
	for {
		e, err := ag.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch e := e.(type) {
		case provider.RequestOpened:
			return &e
		case provider.TurnEnded:
			return nil
		}
	}
}

// The runtime retries a Respond whose outcome it never recorded, and may
// run two at once: Claude Code still gets one reply.
func TestARetriedAnswerSendsOneReply(t *testing.T) {
	replay(t, approve.name, "")
	replies := stdin(t)
	ag := open(t)
	if err := ag.Do(ctx, provider.Start{Text: approve.prompt}); err != nil {
		t.Fatal(err)
	}
	if err := ag.Do(ctx, provider.Respond{Request: "never-asked", Answer: json.RawMessage(`{"allow":true}`)}); err == nil {
		t.Error("an answer to a request Claude Code never made went through")
	}
	asked := 0
	for r := nextRequest(t, ag); r != nil; r = nextRequest(t, ag) {
		asked++
		var wg sync.WaitGroup
		for range clients {
			wg.Go(func() {
				if err := ag.Do(ctx, provider.Respond{Request: r.ID, Answer: json.RawMessage(`{"allow":true}`)}); err != nil {
					t.Errorf("a repeated answer to %s failed: %v", r.ID, err)
				}
			})
		}
		wg.Wait()
	}
	ag.Close(ctx)
	got := replies()
	if asked < 2 || len(got) != asked {
		t.Fatalf("Claude Code asked %d times and got replies to %d", asked, len(got))
	}
	for id, rs := range got {
		if len(rs) != 1 {
			t.Errorf("Claude Code got %d replies to %s, want 1", len(rs), id)
		}
	}
}

// asks is a claude that makes request, a can_use_tool. A deaf one then
// reads nothing more; another takes one reply and ends the turn.
func asks(t *testing.T, request map[string]any, deaf bool) {
	t.Helper()
	request["subtype"] = "can_use_tool"
	r, _ := json.Marshal(map[string]any{"recv": map[string]any{"type": "control_request", "request_id": "ask", "request": request}})
	tape := []string{
		`{"sent":{"type":"control_request","request":{"subtype":"initialize"}}}`,
		`{"recv":{"type":"control_response","response":{"subtype":"success","request_id":"glitched-init","response":{}}}}`,
		`{"sent":{"type":"user"}}`,
		string(r),
	}
	if deaf {
		tape = append(tape, `{"hang":true}`)
	} else {
		tape = append(tape, `{"sent":{"type":"control_response"}}`, `{"recv":{"type":"result","subtype":"success"}}`, `{"eof":true}`)
	}
	path := filepath.Join(t.TempDir(), "asks.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(tape, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envFake, path)
}

func write(size int) map[string]any {
	return map[string]any{"tool_name": "Write", "tool_use_id": "write",
		"input": map[string]string{"file_path": "$WORKSPACE/big.txt", "content": strings.Repeat("x", size)}}
}

// Each prompt reaches the inbox as the ask the approval card reads.
func TestAPromptBecomesTheCardsAsk(t *testing.T) {
	for name, tc := range map[string]struct {
		request map[string]any
		want    string
	}{
		"command": {map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "go test ./...", "description": "Run the tests"}},
			`{"command":"go test ./...","cwd":"$WORKSPACE","reason":"Run the tests"}`},
		"edit": {write(1), `{"operation":"edit","path":"big.txt"}`},
		"fetch": {map[string]any{"tool_name": "WebFetch", "input": map[string]any{"url": "https://go.dev", "prompt": "summarize"}},
			`{"operation":"read","path":"https://go.dev"}`},
		"other tool": {map[string]any{"tool_name": "mcp__docs__lookup", "input": map[string]any{"q": "x"}},
			`{"operation":"use mcp__docs__lookup"}`},
		"why it asks": {map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "rm -rf build"},
			"decision_reason": "\x1b[1mDangerous rm\x1b[0m operation not ruled out"},
			`{"command":"rm -rf build","cwd":"$WORKSPACE","reason":"Dangerous rm operation not ruled out"}`},
	} {
		t.Run(name, func(t *testing.T) {
			asks(t, tc.request, true)
			ws := t.TempDir()
			ag := openIn(t, ws)
			defer func() {
				closeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
				ag.Close(closeCtx)
			}()
			if err := ag.Do(ctx, provider.Start{Text: "go"}); err != nil {
				t.Fatal(err)
			}
			r := nextRequest(t, ag)
			if want := strings.ReplaceAll(tc.want, "$WORKSPACE", ws); r.Kind != "approval" || string(r.Body) != want {
				t.Errorf("request %s %s, want approval %s", r.Kind, r.Body, want)
			}
		})
	}
}

// A reply cut short by its deadline leaves half a line on Claude Code's
// stdin, so every later write must fail rather than seem to succeed.
func TestAReplyCutShortFailsEveryLaterWrite(t *testing.T) {
	asks(t, write(1<<20), true)
	pid := processes(t)
	ag := open(t)
	defer func() {
		closeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		ag.Close(closeCtx)
		exits(pid("claude"), 3*time.Second)
	}()
	if err := ag.Do(ctx, provider.Start{Text: "write big.txt"}); err != nil {
		t.Fatal(err)
	}
	r := nextRequest(t, ag)
	allow := provider.Respond{Request: r.ID, Answer: json.RawMessage(`{"allow":true}`)}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := within(t, 2*time.Second, "a reply claude never reads", func() error { return ag.Do(short, allow) }); err == nil {
		t.Fatal("a reply claude never read went through")
	}
	later, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := within(t, 2*time.Second, "a write after a cut one", func() error { return ag.Do(later, allow) }); err == nil {
		t.Error("a write after a cut one succeeded")
	}
}

// A reply that never started, because its deadline had passed, leaves
// the stream whole: the next one goes through.
func TestAReplyThatNeverStartedLeavesTheStreamWhole(t *testing.T) {
	asks(t, write(10), false)
	replies := stdin(t)
	ag := open(t)
	if err := ag.Do(ctx, provider.Start{Text: "write big.txt"}); err != nil {
		t.Fatal(err)
	}
	r := nextRequest(t, ag)
	allow := provider.Respond{Request: r.ID, Answer: json.RawMessage(`{"allow":true}`)}
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if err := ag.Do(gone, allow); err == nil {
		t.Error("a reply past its deadline went through")
	}
	if err := ag.Do(ctx, allow); err != nil {
		t.Errorf("the reply after one that never started failed: %v", err)
	}
	if nextRequest(t, ag) != nil {
		t.Fatal("claude asked again")
	}
	ag.Close(ctx)
	if got := replies()[r.ID]; len(got) != 1 {
		t.Errorf("Claude Code got replies %+v, want the one that went", got)
	}
}
