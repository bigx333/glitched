//go:build unix

package claude_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/runtime"
	"glitched/internal/store"
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
		"other tool": {map[string]any{"tool_name": "mcp__db__exec", "input": map[string]any{"sql": "DROP TABLE users", "params": []int{1}}},
			`{"operation":"use mcp__db__exec","args":"params: [1]\nsql: \"DROP TABLE users\""}`},
		"subagent": {map[string]any{"tool_name": "Task", "input": map[string]any{"description": "Tidy up", "prompt": "Delete every file\nunder ~", "subagent_type": "general-purpose"}},
			`{"operation":"use Task","args":"description: \"Tidy up\"\nprompt: |\n  Delete every file\n  under ~\nsubagent_type: \"general-purpose\""}`},
		"null": {map[string]any{"tool_name": "mcp__x__set", "input": map[string]any{"policy": nil}},
			`{"operation":"use mcp__x__set","args":"policy: null"}`},
		"null input": {map[string]any{"tool_name": "mcp__x__set", "input": nil},
			`{"operation":"use mcp__x__set","args":"null"}`},
		"line break in a key": {map[string]any{"tool_name": "mcp__db__exec", "input": map[string]any{"sql: SELECT 1\nx": "y", "sql": "DROP TABLE users"}},
			`{"operation":"use mcp__db__exec","args":"sql: \"DROP TABLE users\"\n\"sql: SELECT 1\\nx\": \"y\""}`},
		"escape in a key": {map[string]any{"tool_name": "mcp__db__exec", "input": map[string]any{`sql: SELECT 1\u{A}x`: "y", "sql": "DROP TABLE users"}},
			`{"operation":"use mcp__db__exec","args":"sql: \"DROP TABLE users\"\n\"sql: SELECT 1\\\\u{A}x\": \"y\""}`},
		"unsandboxed": {map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "ls", "dangerouslyDisableSandbox": true}},
			`{"command":"ls","cwd":"$WORKSPACE","unsandboxed":true}`},
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

// sessionAnswer is Allow for this session.
var sessionAnswer = json.RawMessage(`{"allow":true,"session":true}`)

// offersSession is whether a card's body offers Allow for this session.
func offersSession(body json.RawMessage) bool {
	var b struct{ Session bool }
	json.Unmarshal(body, &b)
	return b.Session
}

// Where Claude Code suggests how to stop asking, the card offers Allow for
// this session, and that answer sends Claude Code's own suggestions back,
// each kept to the session, never to the settings it names.
func TestASessionGrantSendsClaudesSuggestionsForTheSession(t *testing.T) {
	bash := map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "npm test"}}
	for name, tc := range map[string]struct {
		request     map[string]any
		suggestions []any
		want        string
	}{
		"edits": {write(1), []any{map[string]any{"type": "setMode", "mode": "acceptEdits", "destination": "session"}},
			`[{"type":"setMode","mode":"acceptEdits","destination":"session"}]`},
		"a command's rule": {bash, []any{map[string]any{"type": "addRules", "rules": []any{map[string]any{"toolName": "Bash", "ruleContent": "npm test:*"}}, "behavior": "allow", "destination": "localSettings"}},
			`[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"npm test:*"}],"behavior":"allow","destination":"session"}]`},
	} {
		t.Run(name, func(t *testing.T) {
			tc.request["permission_suggestions"] = tc.suggestions
			asks(t, tc.request, false)
			replies := stdin(t)
			ag := open(t)
			if err := ag.Do(ctx, provider.Start{Text: "go"}); err != nil {
				t.Fatal(err)
			}
			r := nextRequest(t, ag)
			if !offersSession(r.Body) {
				t.Errorf("card %s offers no session grant", r.Body)
			}
			if err := ag.Do(ctx, provider.Respond{Request: r.ID, Answer: sessionAnswer}); err != nil {
				t.Fatal(err)
			}
			if nextRequest(t, ag) != nil {
				t.Fatal("claude asked again")
			}
			ag.Close(ctx)
			got := replies()[r.ID]
			if len(got) != 1 || got[0].Behavior != "allow" || !sameJSON(got[0].UpdatedPermissions, json.RawMessage(tc.want)) {
				t.Errorf("Claude Code got %+v, want an allow with updatedPermissions %s", got, tc.want)
			}
		})
	}
}

// A prompt Claude Code suggests nothing for offers no session grant: an
// answer for the session fails, and Allow once sends no permissions.
func TestNoSessionGrantWithoutClaudesSuggestions(t *testing.T) {
	asks(t, write(1), false)
	replies := stdin(t)
	ag := open(t)
	if err := ag.Do(ctx, provider.Start{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	r := nextRequest(t, ag)
	if offersSession(r.Body) {
		t.Errorf("card %s offers a session grant", r.Body)
	}
	if err := ag.Do(ctx, provider.Respond{Request: r.ID, Answer: sessionAnswer}); err == nil {
		t.Error("an answer for the session went through")
	}
	if err := ag.Do(ctx, provider.Respond{Request: r.ID, Answer: json.RawMessage(`{"allow":true}`)}); err != nil {
		t.Fatal(err)
	}
	if nextRequest(t, ag) != nil {
		t.Fatal("claude asked again")
	}
	ag.Close(ctx)
	if got := replies()[r.ID]; len(got) != 1 || got[0].Behavior != "allow" || got[0].UpdatedPermissions != nil {
		t.Errorf("Claude Code got %+v, want one allow with no permissions", got)
	}
}

// An answer that reaches the adapter once Claude Code stopped waiting on
// its prompt goes nowhere, and says so.
func TestAnAnswerClaudeCodeNoLongerWaitsOnFails(t *testing.T) {
	for name, stopped := range map[string]string{
		"cancelled":  `{"recv":{"type":"control_cancel_request","request_id":"ask"}}`,
		"turn ended": `{"recv":{"type":"result","subtype":"success"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			asks(t, write(1), true)
			tape := os.Getenv(envFake)
			lines := strings.Split(string(must(os.ReadFile(tape))), "\n")
			lines = append(lines[:len(lines)-1], stopped, `{"hang":true}`)
			if err := os.WriteFile(tape, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
				t.Fatal(err)
			}
			replies := stdin(t)
			ag := open(t)
			if err := ag.Do(ctx, provider.Start{Text: "write big.txt"}); err != nil {
				t.Fatal(err)
			}
			r := nextRequest(t, ag)
			waitCtx, cancel := context.WithTimeout(ctx, turnWait)
			defer cancel()
			for {
				e, err := ag.Next(waitCtx)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := e.(provider.RequestWithdrawn); ok {
					break
				}
				if _, ok := e.(provider.TurnEnded); ok {
					break
				}
			}
			if err := ag.Do(ctx, provider.Respond{Request: r.ID, Answer: json.RawMessage(`{"allow":true}`)}); !errors.Is(err, provider.ErrWithdrawn) {
				t.Errorf("an answer Claude Code no longer waited on returned %v, want ErrWithdrawn", err)
			}
			closeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			ag.Close(closeCtx)
			if got := replies(); len(got) != 0 {
				t.Errorf("Claude Code got %+v, want nothing", got)
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

// lateAdapter holds back each withdrawal its agents read until an answer
// went to them, so the answer loses the race to Claude Code's cancel.
type lateAdapter struct {
	*claude.Adapter
	answered chan struct{}
}

func (a lateAdapter) Open(ctx context.Context, s provider.SessionSpec) (provider.Agent, error) {
	ag, err := a.Adapter.Open(ctx, s)
	if err != nil {
		return nil, err
	}
	return lateAgent{ag, a.answered}, nil
}

type lateAgent struct {
	provider.Agent
	answered chan struct{}
}

func (g lateAgent) Next(ctx context.Context) (provider.Event, error) {
	e, err := g.Agent.Next(ctx)
	if _, ok := e.(provider.RequestWithdrawn); ok {
		select {
		case <-g.answered:
		case <-ctx.Done():
		}
	}
	return e, err
}

func (g lateAgent) Do(ctx context.Context, a provider.Action) error {
	err := g.Agent.Do(ctx, a)
	if _, ok := a.(provider.Respond); ok {
		close(g.answered)
	}
	return err
}

// An answer that loses the race to Claude Code's cancel, as when the
// user's own hook decided first, voids the request and leaves the run at
// work: Claude Code goes on with the tool.
func TestAnAnswerThatLosesToACancelLeavesTheRunGoing(t *testing.T) {
	asks(t, write(1), true)
	tape := os.Getenv(envFake)
	lines := strings.Split(string(must(os.ReadFile(tape))), "\n")
	lines = append(lines[:len(lines)-1], `{"recv":{"type":"control_cancel_request","request_id":"ask"}}`, `{"hang":true}`)
	if err := os.WriteFile(tape, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	processes(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h, err := runtime.New(ctx, st, provider.Mux{"claude": lateAdapter{&claude.Adapter{Path: must(os.Executable())}, make(chan struct{})}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	thread, run := newThread(t, h, api.AccessAsk), api.NewID()
	if _, err := h.Submit(ctx, api.Command{ID: run, Target: thread, Body: api.MessageDispatch{Text: "write big.txt", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	await := func(what string, done func(promptLog) bool) promptLog {
		t.Helper()
		for deadline := time.Now().Add(turnWait); ; time.Sleep(5 * time.Millisecond) {
			if l, _ := readLog(t, h, thread, run); done(l) {
				return l
			}
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
		}
	}
	r := await("Claude Code's prompt never reached the thread", func(l promptLog) bool { return len(l.requests) > 0 }).requests[0].opened
	submit(t, h, thread, api.RequestRespond{Request: r.Request, Generation: r.Generation, Answer: json.RawMessage(`{"allow":true}`)})
	await("the answer Claude Code no longer waited on never voided its request", func(l promptLog) bool {
		return l.requests[0].state == api.RequestInvalidated
	})
	if th := must(h.Read(ctx, api.Query{Thread: thread})).Threads[0]; th.Run != api.RunRunning {
		t.Fatalf("the run is %v after a late answer, want running: Claude Code is still at work", th.Run)
	}
}
