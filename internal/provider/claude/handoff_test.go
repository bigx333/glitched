package claude_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
	"glitched/internal/transport"
)

type gatedAdapter struct {
	*claude.Adapter
	gate *closeGate
}
type closeGate struct {
	once             sync.Once
	entered, proceed chan struct{}
}
type gatedAgent struct {
	provider.Agent
	gate *closeGate
}

func (a *gatedAdapter) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := a.Adapter.Open(ctx, spec)
	if err != nil || spec.Generation != 1 {
		return ag, err
	}
	return &gatedAgent{ag, a.gate}, nil
}
func (a *gatedAgent) Close(ctx context.Context) error {
	a.gate.once.Do(func() { close(a.gate.entered) })
	select {
	case <-a.gate.proceed:
	case <-ctx.Done():
		return ctx.Err()
	}
	return a.Agent.Close(ctx)
}

func TestDeletingAThreadBeforeItsNextTurnOpensStopsItsClaude(t *testing.T) {
	_, started := keep(t, []string{"hi"}, nil)
	ad := &gatedAdapter{&claude.Adapter{Path: must(os.Executable())}, &closeGate{entered: make(chan struct{}), proceed: make(chan struct{})}}
	path := filepath.Join(t.TempDir(), "store.db")
	st := must(store.Open(path))
	h := must(runtime.New(ctx, st, provider.Mux{"claude": ad}))
	t.Cleanup(func() { h.Close(); ad.Close(); st.Close() })
	release := sync.OnceFunc(func() { close(ad.gate.proceed) })
	defer release()

	th := newThread(t, h, api.AccessAsk)
	first := turn(t, h, th, firstAsk)
	if first.ended.Outcome != api.OutcomeCompleted {
		t.Fatalf("first: %+v", first.ended)
	}
	select {
	case <-ad.gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first close never reached the gate")
	}

	submit(t, h, th, api.MessageDispatch{Text: "second", Mode: api.StartNow})
	submit(t, h, th, api.ThreadDelete{})
	release()

	db := must(sql.Open("sqlite", path))
	defer db.Close()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM effects WHERE thread = ? AND state != 'done'", th[:]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d incomplete outbox effects", n)
		}
	}
	pids := started("claude")
	if len(pids) != 1 {
		t.Fatalf("launched %v; want only first turn to start", pids)
	}
	if !gone(pids[0]) {
		t.Fatalf("thread was deleted and all its effects completed, but Claude %d is still alive", pids[0])
	}
}

// A kept claude's result that finishes only an earlier run's messages, as
// one it sent late would, leaves the run it lands in running.
func TestAResultForAnEarlierRunsMessagesIsIgnored(t *testing.T) {
	tape := string(must(os.ReadFile(filepath.Join("testdata", "hi.jsonl"))))
	before, _, _ := strings.Cut(tape, "{\"eof\":true}")
	before = strings.Replace(before, `"session_id":"","type":"user"}}`, `"session_id":"","type":"user","uuid":"uuid-of-the-first-message"}}`, 1)
	path := filepath.Join(t.TempDir(), "late.jsonl")
	lines := before + `{"sent":{"type":"user","uuid":"uuid-of-the-second-message","message":{"content":"second"}}}
{"recv":{"type":"result","subtype":"success","result":"late","user_message_uuids":["uuid-of-the-first-message"],"usage":{"input_tokens":901,"output_tokens":1}}}
{"sleep":"100ms"}
{"recv":{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"actual second answer"}}}}
{"recv":{"type":"result","subtype":"success","result":"actual second answer","user_message_uuids":["uuid-of-the-second-message"],"usage":{"input_tokens":2,"output_tokens":1}}}
{"eof":true}
`
	if err := os.WriteFile(path, []byte(lines), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envFake, path)
	h := host(t, nil)
	th := newThread(t, h, api.AccessAsk)
	if first := turn(t, h, th, firstAsk); first.ended.Outcome != api.OutcomeCompleted {
		t.Fatalf("first: %+v", first)
	}
	got := turn(t, h, th, "second")
	if got.text != "actual second answer" || got.ended.Tokens != 3 {
		t.Fatalf("second run committed text=%q tokens=%d, want actual second answer and 3 tokens", got.text, got.ended.Tokens)
	}
}

func TestALifecycleLineAfterTheResultKeepsTheClaude(t *testing.T) {
	_, started := keep(t, []string{"ask-dismiss", "again-resume"}, []string{"again-resume"})
	ad := &claude.Adapter{Path: must(os.Executable())}
	t.Cleanup(func() { ad.Close() })
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	spec := provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Generation: 1, Workspace: t.TempDir(), Model: api.ModelRef{Provider: "claude", Model: "haiku"}, Access: api.AccessAsk}
	ag := must(ad.Open(bounded, spec))
	if err := ag.Do(bounded, provider.Start{Text: dismissed.prompt}); err != nil {
		t.Fatal(err)
	}
	for {
		switch e := must(ag.Next(bounded)).(type) {
		case provider.RequestOpened:
			if err := ag.Do(bounded, provider.Respond{Request: e.ID, Answer: answersJSON()}); err != nil {
				t.Fatal(err)
			}
		case provider.TurnEnded:
			if e.Outcome != api.OutcomeCompleted {
				t.Fatalf("first turn: %+v", e)
			}
			if err := ag.Close(bounded); err != nil {
				t.Fatal(err)
			}
			pids := started("claude")
			if len(pids) != 1 {
				t.Fatalf("first turn started PIDs %v", pids)
			}
			if exits(pids[0], 3*time.Second) {
				t.Fatal("recorded post-result command_lifecycle killed otherwise reusable Claude")
			}
			return
		}
	}
}

func TestAClosingClaudeCannotDelegateForTheNextTurn(t *testing.T) {
	src := string(must(os.ReadFile(filepath.Join("testdata", "hi.jsonl"))))
	before, _, _ := strings.Cut(src, "{\"eof\":true}")
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	markerJSON := must(json.Marshal(marker))
	path := filepath.Join(dir, "old-delegate.jsonl")
	tape := before + `{"sleep":"1s"}
{"mcp-sent":{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}}
{"mcp-recv":{}}
{"mcp-sent":{"jsonrpc":"2.0","method":"notifications/initialized"}}
{"mcp-sent":{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"delegate_task","arguments":{"title":"Earlier turn","task":"Reply with ok","client_request_id":"earlier-turn"}}}}
{"mcp-recv":{}}
{"take":` + string(markerJSON) + `}
{"eof":true}
`
	if err := os.WriteFile(path, []byte(tape), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envFake, path)
	t.Setenv(envResume, filepath.Join(must(os.Getwd()), "testdata", "again-resume.jsonl"))
	ad := &gatedAdapter{&claude.Adapter{Path: must(os.Executable())}, &closeGate{entered: make(chan struct{}), proceed: make(chan struct{})}}
	st := must(store.Open(filepath.Join(dir, "db")))
	sock := transport.Loopback{Network: "unix", Address: filepath.Join(dir, "s")}
	mux := provider.Mux{"claude": &firstOnClaude{claude: ad, rest: fake.New(fake.Turn(false))}}
	h := must(runtime.New(ctx, st, mux, runtime.WithMCP(mcp.Launcher{Command: must(os.Executable()), Engine: sock.Address})))
	ln := must(sock.Listen(ctx))
	serving, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&transport.Server{Transport: sock, Endpoint: h}).Serve(serving, ln)
	}()
	release := sync.OnceFunc(func() { close(ad.gate.proceed) })
	defer release()
	t.Cleanup(func() { stop(); <-done; h.Close(); ad.Close(); st.Close() })

	th := newThread(t, h, api.AccessAsk)
	first := turn(t, h, th, firstAsk)
	if first.ended.Outcome != api.OutcomeCompleted {
		t.Fatalf("first: %+v", first.ended)
	}
	select {
	case <-ad.gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first close never reached the gate")
	}
	submit(t, h, th, api.ThreadConfigure{Access: new(api.AccessFull)})
	submit(t, h, th, api.MessageDispatch{Text: "second", Mode: api.StartNow})
	parent := must(st.Thread(ctx, th))
	if live := parent.Live(); live == nil || live.Generation != 2 {
		t.Fatalf("test needs a queued second turn, got %+v", live)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old MCP call did not finish")
		}
	}
	if kids := childrenOf(t, h, th); len(kids) != 0 {
		t.Fatalf("earlier Ask process created a child before new turn's Open; child access=%v, new turn access=%v, title=%s", kids[0].Access, parent.Live().Access, kids[0].Title)
	}
}

func TestEachTurnOnAKeptClaudeGetsItsOwnApprovals(t *testing.T) {
	_, started := keep(t, []string{"prompt-approve", "prompt-approve"}, nil)
	h := host(t, nil)
	th := newThread(t, h, api.AccessAsk)
	for i := range 2 {
		got := playOn(t, h, th, approve.prompt, func(r requestLog) bool {
			submit(t, h, th, api.RequestRespond{Request: r.opened.Request, Generation: r.opened.Generation, Answer: approve.answer(r)})
			return false
		})
		approve.check(t, got)
		for _, r := range got.requests {
			if r.opened.Generation != uint64(i+1) {
				t.Fatalf("request generation: %d, want %d", r.opened.Generation, i+1)
			}
		}
	}
	if p := started("claude"); len(p) != 1 {
		t.Fatalf("got %d Claude processes, want 1", len(p))
	}
}

func TestAResumeOfAMissingSessionStartsIt(t *testing.T) {
	argv, _ := keep(t, []string{"hi"}, nil)
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	if err := os.WriteFile(missing, []byte("{\"stderr\":\"No conversation found with session ID\"}\n{\"exit\":1}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envResume, missing)
	ad := &claude.Adapter{Path: must(os.Executable())}
	t.Cleanup(func() { ad.Close() })
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	spec := provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Generation: 2, Workspace: t.TempDir(), Model: api.ModelRef{Provider: "claude", Model: "haiku"}, Access: api.AccessAsk}
	ag := must(ad.Open(bounded, spec))
	defer ag.Close(bounded)
	if got := launches(t, argv); len(got) != 2 || got[0][0] != "--resume" || got[1][0] != "--session-id" || got[0][1] != got[1][1] {
		t.Fatalf("attempts=%v, want resume followed by creation for the same session", got)
	}
	if err := ag.Do(bounded, provider.Start{Text: firstAsk}); err != nil {
		t.Fatal(err)
	}
	for {
		switch e := must(ag.Next(bounded)).(type) {
		case provider.TurnEnded:
			if e.Outcome != api.OutcomeCompleted {
				t.Fatalf("turn: %+v", e)
			}
			return
		}
	}
}

func TestACrashReportsItsOwnRunsStderr(t *testing.T) {
	tape := string(must(os.ReadFile(filepath.Join("testdata", "hi.jsonl"))))
	before, _, _ := strings.Cut(tape, "{\"eof\":true}")
	lines := before + `{"stderr":"diagnostic from the completed first turn"}
{"sent":{"type":"user","uuid":"22222222-2222-2222-2222-222222222222","message":{"content":"second"}}}
{"exit":7}
`
	path := filepath.Join(t.TempDir(), "old-stderr.jsonl")
	if err := os.WriteFile(path, []byte(lines), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envFake, path)
	h := host(t, nil)
	th := newThread(t, h, api.AccessAsk)
	if first := turn(t, h, th, firstAsk); first.ended.Outcome != api.OutcomeCompleted {
		t.Fatalf("first: %+v", first)
	}
	if got := turn(t, h, th, "second"); got.ended.Outcome != api.OutcomeFailed || !strings.Contains(got.ended.Error, "exit status 7") {
		t.Fatalf("second run error=%q outcome=%v, want exit status 7", got.ended.Error, got.ended.Outcome)
	}
}
