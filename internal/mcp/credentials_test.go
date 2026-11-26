package mcp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/mcp"
)

type asker struct {
	got []api.Ask
	out api.Asked
}

func (a *asker) AskCredentials(_ context.Context, in api.Ask) (api.Asked, error) {
	a.got = append(a.got, in)
	return a.out, nil
}

func rawServe(t *testing.T, tools *mcp.Tools, lines ...string) []map[string]json.RawMessage {
	t.Helper()
	var out bytes.Buffer
	if err := mcp.Serve(ctx, strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, tools); err != nil {
		t.Fatal(err)
	}
	var replies []map[string]json.RawMessage
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r map[string]json.RawMessage
		json.Unmarshal([]byte(l), &r)
		replies = append(replies, r)
	}
	return replies
}

// request_credentials is listed for every agent, loaded up front, and asks
// for the server's own thread and access with Claude Code's tool use id
// when the agent gives none. What it returns names files, never values.
func TestRequestCredentialsAsksForItsOrigin(t *testing.T) {
	o := mcp.Origin{Thread: api.NewID(), Access: api.AccessFull}
	a := &asker{out: api.Asked{Status: api.AskProvided, Names: []string{"GLX_PROBE"}, Env: "/v/env", Files: map[string]string{"GLX_PROBE": "/v/GLX_PROBE"}}}
	replies := rawServe(t, &mcp.Tools{Origin: o, Secrets: a},
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"request_credentials","arguments":{"reason":"probe","fields":[{"name":"GLX_PROBE"}]},"_meta":{"claudecode/toolUseId":"toolu_1"}}}`,
	)
	byID := map[string]map[string]json.RawMessage{}
	for _, r := range replies {
		byID[string(r["id"])] = r
	}
	var list struct {
		Tools []struct {
			Name string
			Meta map[string]any `json:"_meta"`
		}
	}
	json.Unmarshal(byID["1"]["result"], &list)
	found := false
	for _, tool := range list.Tools {
		if tool.Name == "request_credentials" {
			found = tool.Meta["anthropic/alwaysLoad"] == true
		}
	}
	if !found {
		t.Errorf("tools/list %s, want request_credentials loaded up front", byID["1"]["result"])
	}
	if len(a.got) != 1 || a.got[0].Thread != o.Thread || a.got[0].Access != o.Access || a.got[0].Call != "toolu_1" || a.got[0].Credentials.Names()[0] != "GLX_PROBE" {
		t.Fatalf("asked %+v", a.got)
	}
	var res struct {
		IsError           bool
		StructuredContent struct{ Status, Env, Note string }
	}
	json.Unmarshal(byID["2"]["result"], &res)
	if res.IsError || res.StructuredContent.Status != api.AskProvided || !strings.Contains(res.StructuredContent.Note, "/v/env") {
		t.Errorf("the call returned %s", byID["2"]["result"])
	}
}

// A call with a field the tool doesn't take, or to an engine that keeps
// no credentials, fails without asking.
func TestRequestCredentialsRefusals(t *testing.T) {
	a := &asker{}
	for _, tools := range []*mcp.Tools{{Secrets: a}, {}} {
		replies := rawServe(t, tools,
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"request_credentials","arguments":{"reason":"r","fields":[{"name":"A"}],"value":"x"}}}`,
		)
		var res struct{ IsError bool }
		json.Unmarshal(replies[0]["result"], &res)
		if !res.IsError {
			t.Errorf("the call returned %s, want an error", replies[0]["result"])
		}
	}
	if len(a.got) != 0 {
		t.Errorf("asked %+v", a.got)
	}
}

// listed is whether tools/list names tool.
func listed(t *testing.T, tools *mcp.Tools, tool string) bool {
	t.Helper()
	r := rawServe(t, tools, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	return strings.Contains(string(r[0]["result"]), `"`+tool+`"`)
}

// refused asks for a secret through tools, and is whether the call was
// refused.
func refused(t *testing.T, tools *mcp.Tools) bool {
	t.Helper()
	r := rawServe(t, tools, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"request_credentials","arguments":{"reason":"probe","fields":[{"name":"GLX_PROBE"}]}}}`)
	var res struct{ IsError bool }
	json.Unmarshal(r[0]["result"], &res)
	return res.IsError
}

// A child the user hasn't taken over has no request_credentials: it asks
// its parent, which its instructions say. The engine's word on whether the
// user took it over wins over the flag it started with.
func TestAChildAsksItsParentForSecrets(t *testing.T) {
	a := &asker{out: api.Asked{Status: api.AskDeclined}}
	kid := &mcp.Tools{Origin: mcp.Origin{Thread: api.NewID(), Access: api.AccessFull, Child: true}, Secrets: a}
	if listed(t, kid, "request_credentials") || !refused(t, kid) || len(a.got) != 0 {
		t.Fatalf("a child: listed %v, asked %+v; want it hidden and refused", listed(t, kid, "request_credentials"), a.got)
	}
	init := rawServe(t, kid, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if s := string(init[0]["result"]); !strings.Contains(s, "report the names of the secrets you need") || strings.Contains(s, "ask with request_credentials") {
		t.Fatalf("a child's instructions: %s", s)
	}

	h := engine(t)
	o := working(t, h)
	c := child(t, h, o, "Deploy it")
	live := &mcp.Tools{Engine: h, Origin: mcp.Origin{Thread: c.ID, Access: o.Access}, Secrets: a}
	if listed(t, live, "request_credentials") || !refused(t, live) {
		t.Fatal("a child the engine has answering to its parent lists or takes request_credentials")
	}
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: c.ID, Body: api.MessageDispatch{Text: "mine now", Mode: api.AfterActive}}); err != nil {
		t.Fatal(err)
	}
	until(t, h, c.ID, "the takeover", func(th api.Thread) bool { return th.TakenOver })
	live.Origin.Child = true
	if !listed(t, live, "request_credentials") || refused(t, live) || len(a.got) != 1 {
		t.Fatalf("a child the user took over: listed %v, asked %+v; want request_credentials its own", listed(t, live, "request_credentials"), a.got)
	}
	top := &mcp.Tools{Engine: h, Origin: o, Secrets: a}
	if !listed(t, top, "request_credentials") || refused(t, top) {
		t.Fatal("a thread of the user's lacks request_credentials")
	}
	s := (mcp.Launcher{Command: "glitched", Engine: "/tmp/sock"}).ForThread(c.ID, api.AccessAsk, api.RoleDefault, true)
	if s.Env[mcp.EnvChild] != "1" || strings.Contains(s.Instructions, "ask with request_credentials") || strings.Contains(s.Instructions, "env script") || !strings.Contains(s.Instructions, "take over this thread on its host") {
		t.Fatalf("a child's MCP server: env %+v, instructions %q", s.Env, s.Instructions)
	}
	if s := (mcp.Launcher{}).For(o.Thread, api.AccessAsk); s.Env[mcp.EnvChild] != "" || !strings.Contains(s.Instructions, "ask with request_credentials") || !strings.Contains(s.Instructions, "never the env script") || !strings.Contains(s.Instructions, "take over that child on its host") {
		t.Fatalf("a top-level thread's MCP server: env %+v, instructions %q", s.Env, s.Instructions)
	}
}

// A live MCP session for a child thread advertises tools.listChanged, emits
// notifications/tools/list_changed as soon as the user takes the child over,
// and lists request_credentials on the refreshed tools/list.
func TestLiveTakeoverEmitsToolsListChanged(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	c := child(t, h, o, "Deploy it")

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- mcp.Serve(ctx, inR, outW, &mcp.Tools{
			Engine:  h,
			Origin:  mcp.Origin{Thread: c.ID, Access: o.Access, Child: true},
			Secrets: &asker{out: api.Asked{Status: api.AskDeclined}},
		})
		outW.Close()
	}()
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	nextLine := func(what string) string {
		t.Helper()
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream closed while waiting for %s", what)
			}
			return l
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
			return ""
		}
	}

	io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n")
	initLine := nextLine("initialize")
	if !strings.Contains(initLine, `"listChanged":true`) {
		t.Fatalf("initialize = %s, want listChanged:true", initLine)
	}
	io.WriteString(inW, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n")
	listBefore := nextLine("tools/list before takeover")
	if strings.Contains(listBefore, `"request_credentials"`) {
		t.Fatalf("tools/list before takeover exposed request_credentials: %s", listBefore)
	}

	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: c.ID, Body: api.MessageDispatch{Text: "mine now", Mode: api.AfterActive}}); err != nil {
		t.Fatal(err)
	}
	notif := nextLine("notifications/tools/list_changed")
	if !strings.Contains(notif, `"method":"notifications/tools/list_changed"`) {
		t.Fatalf("expected notifications/tools/list_changed, got %s", notif)
	}

	io.WriteString(inW, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`+"\n")
	listAfter := nextLine("tools/list after takeover")
	if !strings.Contains(listAfter, `"request_credentials"`) {
		t.Fatalf("tools/list after takeover missed request_credentials: %s", listAfter)
	}

	inW.Close()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned %v", err)
	}
}

type scriptedStream struct {
	items []api.StreamItem
	err   error
	idx   int
	block bool
}

func (s *scriptedStream) Next(ctx context.Context) (api.StreamItem, error) {
	if s.idx < len(s.items) {
		it := s.items[s.idx]
		s.idx++
		return it, nil
	}
	if s.err != nil {
		err := s.err
		s.err = nil
		return api.StreamItem{}, err
	}
	if s.block {
		<-ctx.Done()
		return api.StreamItem{}, ctx.Err()
	}
	return api.StreamItem{}, io.EOF
}

func (s *scriptedStream) Close() error { return nil }

type scriptedEndpoint struct {
	mu         sync.Mutex
	thread     api.Thread
	reads      []api.Thread
	readErr    error
	readCount  atomic.Int32
	streams    []*scriptedStream
	opens      int
	afterClose atomic.Bool
	closed     atomic.Bool
	readDelay  time.Duration
}

func (e *scriptedEndpoint) Info(context.Context) (api.Environment, error) {
	return api.Environment{}, nil
}
func (e *scriptedEndpoint) Submit(context.Context, api.Command) (api.Receipt, error) {
	return api.Receipt{}, nil
}
func (e *scriptedEndpoint) Read(ctx context.Context, _ api.Query) (api.Snapshot, error) {
	if e.closed.Load() {
		e.afterClose.Store(true)
	}
	if e.readDelay > 0 {
		select {
		case <-time.After(e.readDelay):
		case <-ctx.Done():
			return api.Snapshot{}, ctx.Err()
		}
	}
	n := int(e.readCount.Add(1)) - 1
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.readErr != nil {
		return api.Snapshot{}, e.readErr
	}
	th := e.thread
	if n < len(e.reads) {
		th = e.reads[n]
	} else if len(e.reads) > 0 {
		th = e.reads[len(e.reads)-1]
	}
	return api.Snapshot{Threads: []api.Thread{th}}, nil
}
func (e *scriptedEndpoint) Open(_ context.Context, _ api.Subscription) (api.Stream, error) {
	if e.closed.Load() {
		e.afterClose.Store(true)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	i := e.opens
	e.opens++
	if i < len(e.streams) {
		return e.streams[i], nil
	}
	return &scriptedStream{block: true}, nil
}

// The ownership watcher handles ownership changes delivered in StreamSnapshot,
// re-subscribes after ErrOverflow and consumes the new stream, stops on
// ErrNotFound without polling, and never outlives Serve at EOF.
func TestOwnershipWatcherHandlesSnapshotOverflowAndShutdown(t *testing.T) {
	childID, parentID := api.NewID(), api.NewID()
	initial := api.Thread{ID: childID, Parent: parentID, TakenOver: false}
	taken := api.Thread{ID: childID, Parent: parentID, TakenOver: true}
	handedBack := api.Thread{ID: childID, Parent: parentID, TakenOver: false}
	ep := &scriptedEndpoint{
		// Read 0 (initialize) & Read 1 (watch start): initial (TakenOver=false).
		// Read 2 (reconnect after ErrOverflow): still taken (TakenOver=true), so
		// the second list_changed can only come from consuming stream 2's handedBack.
		reads: []api.Thread{initial, initial, taken},
		streams: []*scriptedStream{
			// First stream delivers takeover via StreamSnapshot, then overflows.
			{
				items: []api.StreamItem{{Kind: api.StreamSnapshot, Snapshot: &api.Snapshot{Threads: []api.Thread{taken}}}},
				err:   api.ErrOverflow,
			},
			// Second stream (after reconnect) delivers handback via StreamThread, then blocks until ctx done.
			{
				items: []api.StreamItem{{Kind: api.StreamThread, Thread: &handedBack}},
				block: true,
			},
		},
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- mcp.Serve(ctx, inR, outW, &mcp.Tools{
			Engine: ep,
			Origin: mcp.Origin{Thread: childID, Access: api.AccessAsk, Child: true},
		})
		outW.Close()
	}()
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	nextLine := func(what string) string {
		t.Helper()
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("closed waiting for %s", what)
			}
			return l
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
			return ""
		}
	}

	io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n")
	_ = nextLine("initialize")
	// 1) Takeover via StreamSnapshot
	if l := nextLine("snapshot list_changed"); !strings.Contains(l, `"notifications/tools/list_changed"`) {
		t.Fatalf("got %s, want notifications/tools/list_changed from snapshot", l)
	}
	// 2) After ErrOverflow, reconnect Read still reports taken; watcher must
	// consume stream 2's handedBack item to emit the second list_changed.
	if l := nextLine("post-overflow list_changed"); !strings.Contains(l, `"notifications/tools/list_changed"`) {
		t.Fatalf("got %s, want notifications/tools/list_changed after overflow", l)
	}
	ep.mu.Lock()
	opens := ep.opens
	ep.mu.Unlock()
	if opens < 2 {
		t.Fatalf("expected reconnect to open stream 2, got opens=%d", opens)
	}
	inW.Close()
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	ep.closed.Store(true)
	time.Sleep(30 * time.Millisecond)
	if ep.afterClose.Load() {
		t.Fatal("ownership watcher called Endpoint after Serve returned")
	}

	// Terminal ErrNotFound on stream ends the watcher without polling Read again.
	gone := &scriptedEndpoint{
		thread:  initial,
		streams: []*scriptedStream{{err: api.ErrNotFound}},
	}
	inR2, inW2 := io.Pipe()
	outR2, outW2 := io.Pipe()
	done2 := make(chan error, 1)
	go func() {
		done2 <- mcp.Serve(ctx, inR2, outW2, &mcp.Tools{
			Engine: gone,
			Origin: mcp.Origin{Thread: childID, Access: api.AccessAsk, Child: true},
		})
		outW2.Close()
	}()
	sc2 := bufio.NewScanner(outR2)
	io.WriteString(inW2, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n")
	if !sc2.Scan() {
		t.Fatal("no initialize reply for gone stream test")
	}
	time.Sleep(150 * time.Millisecond)
	if reads := gone.readCount.Load(); reads > 2 {
		t.Fatalf("watcher kept polling after ErrNotFound: readCount=%d, want <= 2", reads)
	}
	inW2.Close()
	if err := <-done2; err != nil {
		t.Fatalf("Serve (gone): %v", err)
	}

	// Shutdown race: EOF arrives while initialize is still inside Read.
	slow := &scriptedEndpoint{
		thread:    api.Thread{ID: childID, Parent: parentID},
		readDelay: 40 * time.Millisecond,
	}
	var out bytes.Buffer
	if err := mcp.Serve(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n"), &out, &mcp.Tools{
		Engine: slow,
		Origin: mcp.Origin{Thread: childID, Access: api.AccessAsk, Child: true},
	}); err != nil {
		t.Fatalf("slow Serve: %v", err)
	}
	slow.closed.Store(true)
	time.Sleep(80 * time.Millisecond)
	if slow.afterClose.Load() {
		t.Fatal("watcher started after EOF and called Endpoint after Serve returned")
	}
}
