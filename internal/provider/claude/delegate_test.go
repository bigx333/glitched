package claude_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"glitched/internal/api"
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
	"glitched/internal/transport"
)

// delegatePrompt is the turn the delegate transcript recorded.
const delegatePrompt = `Use the delegate_task tool of the glitched MCP server once, with task "Reply with only: ok" and title "probe". Then reply with only: done`

// firstOnClaude runs the first thread it opens, a test's parent, on
// claude, and every other, the parent's children, on the fake, so a child
// replays no transcript and runs no real claude.
type firstOnClaude struct {
	claude provider.Adapter
	rest   *fake.Adapter

	mu    sync.Mutex
	first api.ID
}

func (a *firstOnClaude) pick(thread api.ID) provider.Adapter {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.first.IsZero() {
		a.first = thread
	}
	if thread == a.first {
		return a.claude
	}
	return a.rest
}

func (a *firstOnClaude) Describe(ctx context.Context) (provider.Info, error) {
	return a.claude.Describe(ctx)
}

func (a *firstOnClaude) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	return a.pick(spec.Thread).Open(ctx, spec)
}

func (a *firstOnClaude) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	return a.pick(id.Thread).Reconcile(ctx, id)
}

// delegateHost is a real engine whose runs get glitched mcp, played by the
// test binary, and which serves its socket for it. Its first thread runs
// on ad, the fake claude when nil.
func delegateHost(t *testing.T, ad *claude.Adapter) *runtime.Host {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	if ad == nil {
		ad = &claude.Adapter{Path: must(os.Executable())}
	}
	sock := transport.Loopback{Network: "unix", Address: filepath.Join(dir, "engine.sock")}
	mux := provider.Mux{"claude": &firstOnClaude{claude: ad, rest: fake.New(fake.Turn(false))}}
	h, err := runtime.New(ctx, st, mux, runtime.WithMCP(mcp.Launcher{Command: must(os.Executable()), Engine: sock.Address}))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	ln, err := sock.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	serving, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&transport.Server{Transport: sock, Endpoint: h}).Serve(serving, ln)
	}()
	t.Cleanup(func() {
		stop()
		<-done
		h.Close()
		st.Close()
	})
	return h
}

// childrenOf lists parent's children as the engine reads them.
func childrenOf(t *testing.T, h *runtime.Host, parent api.ID) []api.Thread {
	t.Helper()
	snap, err := h.Read(ctx, api.Query{})
	if err != nil {
		t.Fatal(err)
	}
	var kids []api.Thread
	for _, th := range snap.Threads {
		if th.Parent == parent {
			kids = append(kids, th)
		}
	}
	return kids
}

// firstMessage is the text thread's first message carried.
func firstMessage(t *testing.T, h *runtime.Host, thread api.ID) string {
	t.Helper()
	snap, err := h.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range snap.Events {
		if q, ok := must(e.Decode()).(api.MessageQueued); ok {
			return q.Text
		}
	}
	return ""
}

// tapeCall is the delegate_task call a transcript's MCP traffic made.
type tapeCall struct {
	Name      string `json:"name"`
	Arguments struct {
		Task, Title string
	} `json:"arguments"`
	Meta map[string]string `json:"_meta"`
}

// mcpTraffic is what a transcript's MCP server got and said, in order.
func mcpTraffic(t *testing.T, tape string) (sent, recv []map[string]json.RawMessage) {
	t.Helper()
	for _, l := range strings.Split(string(must(os.ReadFile(tape))), "\n") {
		var e map[string]map[string]json.RawMessage
		json.Unmarshal([]byte(l), &e)
		if m := e["mcp-sent"]; m != nil {
			sent = append(sent, m)
		}
		if m := e["mcp-recv"]; m != nil {
			recv = append(recv, m)
		}
	}
	return sent, recv
}

// callIn is the delegate_task call in sent.
func callIn(t *testing.T, sent []map[string]json.RawMessage) tapeCall {
	t.Helper()
	var calls []tapeCall
	for _, m := range sent {
		if string(m["method"]) == `"tools/call"` {
			var c tapeCall
			json.Unmarshal(m["params"], &c)
			calls = append(calls, c)
		}
	}
	if len(calls) != 1 || calls[0].Name != "delegate_task" {
		t.Fatalf("the MCP server got calls %+v, want one delegate_task", calls)
	}
	return calls[0]
}

// A Claude Code parent that calls delegate_task starts one child of its
// thread, with the call's task as the child's first message. Its claude
// gets glitched mcp beside the user's own servers, for its thread and run,
// and the server's tools ask nobody.
func TestAClaudeParentDelegatesATask(t *testing.T) {
	argv := replay(t, "delegate", "")
	h := delegateHost(t, nil)
	parent := newThread(t, h, api.AccessAsk)
	got := turn(t, h, parent, delegatePrompt)
	if got.ended.Outcome != api.OutcomeCompleted || len(got.requests) != 0 {
		t.Fatalf("the parent's turn: %+v, want it completed without a request", got)
	}
	sent, _ := mcpTraffic(t, filepath.Join("testdata", "delegate.jsonl"))
	call := callIn(t, sent)
	kids := childrenOf(t, h, parent)
	if len(kids) != 1 {
		t.Fatalf("the parent has %d children, want 1: %+v", len(kids), kids)
	}
	if kids[0].Title != call.Arguments.Title || kids[0].Workspace != got.workspace {
		t.Errorf("the child %+v, want %q in the parent's folder", kids[0], call.Arguments.Title)
	}
	if text := firstMessage(t, h, kids[0].ID); text != call.Arguments.Task {
		t.Errorf("the child's first message %q, want the task %q", text, call.Arguments.Task)
	}

	// The child's report starts the parent's next run, which may have
	// appended its own line by now.
	first, _, _ := strings.Cut(string(must(os.ReadFile(argv))), "\n")
	var args []string
	json.Unmarshal([]byte(first), &args)
	i := slices.Index(args, "--mcp-config")
	if i < 0 {
		t.Fatalf("claude ran with %q, want an --mcp-config", args)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
			Env     map[string]string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(args[i+1]), &cfg); err != nil {
		t.Fatal(err)
	}
	s := cfg.MCPServers["glitched"]
	if len(cfg.MCPServers) != 1 || !slices.Equal(s.Args, []string{"mcp"}) || s.Env[mcp.EnvThread] != parent.String() || s.Env[mcp.EnvRun] == "" || s.Env[mcp.EnvEngine] == "" {
		t.Errorf("--mcp-config %s, want glitched mcp for the parent's thread and run", args[i+1])
	}
	if slices.Contains(args, "--strict-mcp-config") {
		t.Errorf("claude ran with %q, want the user's own servers beside glitched's", args)
	}
	if j := slices.Index(args, "--allowedTools"); j < 0 || args[j+1] != "mcp__glitched__*" {
		t.Errorf("claude ran with %q, want glitched's tools allowed without asking", args)
	}
	if j := slices.Index(args, "--append-system-prompt"); j < 0 || !strings.Contains(args[j+1], "delegate_task") {
		t.Errorf("claude ran with %q, want instructions for delegate_task", args)
	}
}

// A run's claude never inherits the variables that name a glitched mcp
// caller, as an engine started inside another engine's run would have.
// Only its --mcp-config names its own.
func TestClaudeInheritsNoCaller(t *testing.T) {
	replay(t, "hi", "")
	t.Setenv(mcp.EnvThread, api.NewID().String())
	t.Setenv(mcp.EnvRun, api.NewID().String())
	t.Setenv(strings.ToLower(mcp.EnvEngine), "/elsewhere/engine.sock")
	entry := filepath.Join(t.TempDir(), "entry")
	t.Setenv(envEntry, entry)
	h := host(t, nil)
	turn(t, h, newThread(t, h, api.AccessAsk), "Say hi in three words.")
	var got []string
	json.Unmarshal(must(os.ReadFile(entry)), &got)
	if want := []string{"CLAUDE_CODE_ENTRYPOINT=glitched"}; !slices.Equal(got, want) {
		t.Errorf("claude ran with %q, want only %q", got, want)
	}
}

// toollessMCP is an MCP server with no tools, as a project's own server
// beside glitched's.
func toollessMCP() int {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == nil {
			continue
		}
		r := map[string]any{"jsonrpc": "2.0", "id": m.ID}
		switch m.Method {
		case "initialize":
			r["result"] = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "project", "version": "1"}}
		case "tools/list":
			r["result"] = map[string]any{"tools": []any{}}
		case "ping":
			r["result"] = map[string]any{}
		default:
			r["error"] = map[string]any{"code": -32601, "message": "unknown method: " + m.Method}
		}
		b, _ := json.Marshal(r)
		os.Stdout.Write(append(b, '\n'))
	}
	return 0
}
