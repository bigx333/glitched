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

	"glitched/internal/api"
	model "glitched/internal/engine"
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
	"glitched/internal/transport"
)

type gateNextOpen struct {
	*gatedAdapter
	opened chan struct{}
}

func (a *gateNextOpen) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	if spec.Generation != 2 {
		return a.gatedAdapter.Open(ctx, spec)
	}
	close(a.opened)
	<-ctx.Done()
	return nil, ctx.Err()
}

type readAfterOpen struct {
	api.Endpoint
	opened <-chan struct{}
}

func (a readAfterOpen) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	select {
	case <-a.opened:
		return a.Endpoint.Read(ctx, q)
	case <-ctx.Done():
		return api.Snapshot{}, ctx.Err()
	}
}

func TestAnEarlierTurnsClaudeCannotDelegateBeforeTheNextOpens(t *testing.T) {
	src := string(must(os.ReadFile(filepath.Join("testdata", "hi.jsonl"))))
	before, _, _ := strings.Cut(src, "{\"eof\":true}")
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	markerJSON := must(json.Marshal(marker))
	path := filepath.Join(dir, "old-delegate.jsonl")
	tape := before + `{"mcp-sent":{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}}
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
	sock := transport.Loopback{Network: "unix", Address: socketPath(t)}
	opened := make(chan struct{})
	active := &gateNextOpen{gatedAdapter: ad, opened: opened}
	mux := provider.Mux{"claude": &firstOnClaude{claude: active, rest: fake.New(fake.Turn(false))}}
	h := must(runtime.New(ctx, st, mux, runtime.WithMCP(mcp.Launcher{Command: must(os.Executable()), Engine: sock.Address})))
	ln := must(sock.Listen(ctx))
	serving, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&transport.Server{Transport: sock, Endpoint: readAfterOpen{Endpoint: h, opened: opened}}).Serve(serving, ln)
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
	release()
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the next Open was not reached")
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old MCP call did not finish")
		}
	}
	p := must(st.Thread(ctx, th))
	if p.Live() == nil || p.Live().State != model.RunStarting {
		t.Fatalf("new turn must still be starting, got %+v", p.Live())
	}
	if kids := childrenOf(t, h, th); len(kids) != 0 {
		t.Fatalf("earlier Ask process created a child before the blocked new Open could return; child access=%v, new turn access=%v, title=%s", kids[0].Access, parent.Live().Access, kids[0].Title)
	}
}

func TestAParkedClaudeThatStartsACommandIsStopped(t *testing.T) {
	_, started := keep(t, []string{"hi"}, nil)
	src := string(must(os.ReadFile(filepath.Join("testdata", "hi.jsonl"))))
	before, _, _ := strings.Cut(src, "{\"eof\":true}")
	path := filepath.Join(t.TempDir(), "unexpected-start.jsonl")
	tape := before + `{"recv":{"type":"command_lifecycle","state":"started","command_uuid":"unexpected-old-command"}}
{"eof":true}
`
	if err := os.WriteFile(path, []byte(tape), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envFake, path)
	a := &claude.Adapter{Path: must(os.Executable())}
	t.Cleanup(func() { a.Close() })
	c, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	spec := provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Generation: 1, Workspace: t.TempDir(), Model: api.ModelRef{Provider: "claude", Model: "haiku"}, Access: api.AccessAsk}
	ag := must(a.Open(c, spec))
	if err := ag.Do(c, provider.Start{Text: firstAsk}); err != nil {
		t.Fatal(err)
	}
	for {
		if e, ok := must(ag.Next(c)).(provider.TurnEnded); ok {
			if e.Outcome != api.OutcomeCompleted {
				t.Fatalf("first turn: %+v", e)
			}
			break
		}
	}
	if err := ag.Close(c); err != nil {
		t.Fatal(err)
	}
	p := started("claude")
	if len(p) != 1 {
		t.Fatalf("started %d processes, want 1", len(p))
	}
	if !exits(p[0], 2*time.Second) {
		t.Fatal("Claude stayed alive after reporting an unexpected command started with no run attached")
	}
}
