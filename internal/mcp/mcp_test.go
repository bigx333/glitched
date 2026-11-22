package mcp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

var ctx = context.Background()

// engine is a real host whose fake agents stop at an approval, so a run
// stays live for its agent's calls.
func engine(t *testing.T) *runtime.Host {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(true))})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.Close()
		st.Close()
	})
	return h
}

// working is a thread of the user's with a live run, and that run.
func working(t *testing.T, h *runtime.Host) mcp.Origin {
	t.Helper()
	return workingAt(t, h, api.AccessAsk)
}

// workingAt is working, its thread created at access.
func workingAt(t *testing.T, h *runtime.Host, access api.Access) mcp.Origin {
	t.Helper()
	thread := api.NewID()
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{
		Workspace: "/src/app", Title: "fix it", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Access: access,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "split it", Mode: api.AfterActive}}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		snap, err := h.Read(ctx, api.Query{Thread: thread})
		if err == nil && snap.Threads[0].Run == api.RunWaiting {
			return mcp.Origin{Thread: thread}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run never waited: %+v %v", snap.Threads, err)
		}
	}
}

type reply struct {
	ID     int `json:"id"`
	Result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent mcp.TaskView `json:"structuredContent"`
	} `json:"result"`
	Error *struct {
		Code int `json:"code"`
	} `json:"error"`
}

// serve has the server for o take lines, as a client sends them at once,
// and returns its replies by id.
func serve(t *testing.T, h *runtime.Host, o mcp.Origin, lines ...string) map[int]reply {
	t.Helper()
	var out bytes.Buffer
	if err := mcp.Serve(ctx, strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, &mcp.Tools{Engine: h, Origin: o}); err != nil {
		t.Fatal(err)
	}
	got := map[int]reply{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r reply
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("reply %q: %v", l, err)
		}
		if _, dup := got[r.ID]; dup {
			t.Fatalf("two replies to request %d", r.ID)
		}
		got[r.ID] = r
	}
	return got
}

func delegate(id int, args, meta string) string {
	s := `{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"tools/call","params":{"name":"delegate_task","arguments":` + args
	if meta != "" {
		s += `,"_meta":` + meta
	}
	return s + "}}"
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// configure gives thread what c holds of a model and access, for its
// turns from the next on.
func configure(t *testing.T, h *runtime.Host, thread api.ID, c api.ThreadConfigure) {
	t.Helper()
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: c}); err != nil {
		t.Fatal(err)
	}
}

func children(t *testing.T, h *runtime.Host, parent api.ID) []api.Thread {
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

// Claude Code asks server/discover before initialize. The server answers
// a method it doesn't know as JSON-RPC's not found, and nothing at all to
// a notification.
func TestAnUnknownMethodIsNotFound(t *testing.T) {
	h := engine(t)
	got := serve(t, h, mcp.Origin{},
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"2.1.318"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/list"}`,
	)
	if len(got) != 5 {
		t.Fatalf("%d replies, want one to each of the 5 requests: %+v", len(got), got)
	}
	for _, id := range []int{1, 4} {
		if e := got[id].Error; e == nil || e.Code != -32601 {
			t.Errorf("request %d: error %+v, want -32601", id, e)
		}
	}
	for _, id := range []int{2, 3, 5} {
		if got[id].Error != nil {
			t.Errorf("request %d: error %+v", id, got[id].Error)
		}
	}
}

// One call starts one child of the caller's thread, with the task as its
// first message. A retry with the call's id, the agent's or else Claude
// Code's tool use id, finds the same child, even when the tries race.
func TestADelegateStartsOneChildForEachCall(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	task := `{"task":"Add table tests for parseRange","title":"Write the tests","client_request_id":"tests-1"}`
	got := serve(t, h, o,
		delegate(1, task, ""),
		delegate(2, task, `{"claudecode/toolUseId":"toolu_other"}`),
		delegate(3, `{"task":"Check the callers"}`, `{"claudecode/toolUseId":"toolu_A"}`),
		delegate(4, `{"task":"Check the callers"}`, `{"claudecode/toolUseId":"toolu_A"}`),
	)
	for id, r := range got {
		if r.Error != nil || r.Result.IsError {
			t.Fatalf("call %d: %+v", id, r)
		}
	}
	if a, b := got[1].Result.StructuredContent, got[2].Result.StructuredContent; a.TaskID != b.TaskID || a.Status != "working" {
		t.Errorf("tries of tests-1 gave %+v and %+v, want one working child", a, b)
	}
	if got[3].Result.StructuredContent.TaskID != got[4].Result.StructuredContent.TaskID {
		t.Errorf("tries of toolu_A gave two children")
	}
	kids := children(t, h, o.Thread)
	if len(kids) != 2 {
		t.Fatalf("the caller has %d children, want 2: %+v", len(kids), kids)
	}
	for _, k := range kids {
		if k.ID.String() == got[1].Result.StructuredContent.TaskID && (k.Title != "Write the tests" || k.Workspace != "/src/app" || k.Model.Provider != "fake") {
			t.Errorf("the child %+v, want Write the tests in the caller's folder on its provider", k)
		}
	}
	evs, err := h.Read(ctx, api.Query{Thread: kids[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, e := range evs.Events {
		if b, err := e.Decode(); err == nil {
			if q, ok := b.(api.MessageQueued); ok {
				texts = append(texts, q.Text)
			}
		}
	}
	if len(texts) != 1 || texts[0] != "Add table tests for parseRange" && texts[0] != "Check the callers" {
		t.Errorf("the child's messages: %q, want its task alone", texts)
	}
}

// Only the server's environment names the caller. Arguments that name a
// thread are refused, and nothing starts under that thread or the caller.
func TestACallCannotNameAnotherCaller(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	other := working(t, h)
	got := serve(t, h, o,
		delegate(1, `{"task":"x","parent":"`+other.Thread.String()+`"}`, ""),
		delegate(2, `{"task":"x","thread":"`+other.Thread.String()+`","run":"`+api.NewID().String()+`"}`, ""),
	)
	for _, id := range []int{1, 2} {
		r := got[id]
		if !r.Result.IsError || len(r.Result.Content) == 0 || !strings.Contains(r.Result.Content[0].Text, "unknown field") {
			t.Errorf("call %d: %+v, want an error for the unknown field", id, r)
		}
	}
	if n, m := len(children(t, h, other.Thread)), len(children(t, h, o.Thread)); n != 0 || m != 0 {
		t.Errorf("children: %d under the named thread, %d under the caller; want none", n, m)
	}
}

// A call while the thread has no turn running starts no child, and the
// agent reads why.
func TestAThreadWithNoTurnRunningStartsNoChild(t *testing.T) {
	h := engine(t)
	thread := api.NewID()
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{
		Workspace: "/src/app", Title: "idle", Model: api.ModelRef{Provider: "fake", Model: "fake"},
	}}); err != nil {
		t.Fatal(err)
	}
	got := serve(t, h, mcp.Origin{Thread: thread}, delegate(1, `{"task":"x"}`, ""))
	if r := got[1]; !r.Result.IsError || !strings.Contains(r.Result.Content[0].Text, "no turn running") {
		t.Errorf("a call with no turn running: %+v, want a refusal that says so", r)
	}
	if n := len(children(t, h, thread)); n != 0 {
		t.Errorf("%d children, want none", n)
	}
}

// An agent's process, and the glitched mcp it started, can outlive a run
// and serve the thread's next one. Each call acts for the turn running when
// it is made: its child records that run as its parent's, at that turn's
// model, and a server that named its first run would see the engine refuse
// the second turn's call as one from a run no longer live.
func TestOneServerDelegatesForEachTurnOfItsThread(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		mcp.Serve(ctx, inR, outW, &mcp.Tools{Engine: h, Origin: o})
		outW.Close()
	}()
	t.Cleanup(func() { inW.Close() })
	replies := bufio.NewReader(outR)
	call := func(id int, task string) reply {
		t.Helper()
		fmt.Fprintln(inW, delegate(id, `{"task":"`+task+`"}`, ""))
		line, err := replies.ReadBytes('\n')
		var r reply
		if err := errors.Join(err, json.Unmarshal(line, &r)); err != nil {
			t.Fatal(err)
		}
		return r
	}
	read := func() api.Thread {
		s, _ := h.Read(ctx, api.Query{Thread: o.Thread})
		return s.Threads[0]
	}
	if r := call(1, "first"); r.Error != nil || r.Result.IsError {
		t.Fatalf("turn 1: %+v", r)
	}
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: o.Thread, Body: api.TurnInterrupt{}}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); read().Run != api.RunIdle; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("turn 1 never stopped: %+v", read())
		}
	}
	configure(t, h, o.Thread, api.ThreadConfigure{Effort: new("high")})
	r2, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: o.Thread, Body: api.MessageDispatch{Text: "again", Mode: api.AfterActive}})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); read().Run != api.RunWaiting; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("turn 2 never waited: %+v", read())
		}
	}
	if turn := read().Turn; turn == nil || turn.Run != r2.Command {
		t.Fatalf("turn 2 is %+v, want run %s", turn, r2.Command)
	}
	configure(t, h, o.Thread, api.ThreadConfigure{Effort: new("low")})
	if r := call(2, "second"); r.Error != nil || r.Result.IsError {
		t.Fatalf("turn 2, on the server turn 1 started: %+v, want a child", r)
	}
	kids := children(t, h, o.Thread)
	i := slices.IndexFunc(kids, func(k api.Thread) bool { return k.Title == "second" })
	if i < 0 || kids[i].Model.Effort != "high" {
		t.Errorf("children %+v, want turn 2's child at the effort turn 2 was admitted at", kids)
	}
}

// A client_request_id an agent reuses for a new task in a later turn gets
// a reason it can act on, and starts nothing.
func TestAReusedRequestIDFindsItsTaskOrSaysToPickANewOne(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	if r := serve(t, h, o, delegate(1, `{"task":"Write tests for parse","client_request_id":"task-1"}`, ""))[1]; r.Result.IsError {
		t.Fatalf("turn 1: %+v", r)
	}
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: o.Thread, Body: api.TurnInterrupt{}}); err != nil {
		t.Fatal(err)
	}
	read := func() api.Thread {
		s, _ := h.Read(ctx, api.Query{Thread: o.Thread})
		return s.Threads[0]
	}
	for deadline := time.Now().Add(5 * time.Second); read().Run != api.RunIdle; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("turn 1 never stopped: %+v", read())
		}
	}
	_, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: o.Thread, Body: api.MessageDispatch{Text: "now the docs", Mode: api.AfterActive}})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); read().Run != api.RunWaiting; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("turn 2 never waited: %+v", read())
		}
	}
	r := serve(t, h, o, delegate(1, `{"task":"Update the README","client_request_id":"task-1"}`, ""))[1]
	if !r.Result.IsError || len(r.Result.Content) == 0 || !strings.Contains(r.Result.Content[0].Text, "use a new client_request_id") {
		t.Errorf("turn 2 reply %+v, want an error that says to use a new client_request_id", r)
	}
	if n := len(children(t, h, o.Thread)); n != 1 {
		t.Errorf("%d children, want 1", n)
	}
	// The same task, retried with its id in a later turn, finds its child.
	r = serve(t, h, o, delegate(2, `{"task":"Write tests for parse","client_request_id":"task-1"}`, ""))[2]
	kids := children(t, h, o.Thread)
	if r.Result.IsError || len(kids) != 1 || len(r.Result.Content) == 0 || !strings.Contains(r.Result.Content[0].Text, kids[0].ID.String()) {
		t.Errorf("turn 2 retry of the same task: %+v with %d children; want the first child back", r, len(kids))
	}
}

// A running turn's child starts at the model and access the turn was
// admitted at: what its thread was given since is for its next turn.
func TestARunningTurnDelegatesAtItsOwnModelAndAccess(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	configure(t, h, o.Thread, api.ThreadConfigure{Effort: new("high"), Fast: new(true), Access: new(api.AccessFull)})
	if r := serve(t, h, o, delegate(1, `{"task":"rm -rf build"}`, ""))[1]; r.Error != nil || r.Result.IsError {
		t.Fatalf("delegate: %+v", r)
	}
	kids := children(t, h, o.Thread)
	if len(kids) != 1 {
		t.Fatalf("%d children, want 1", len(kids))
	}
	if k, turn := kids[0], (api.ModelRef{Provider: "fake", Model: "fake"}); k.Access != api.AccessAsk || k.Model != turn {
		t.Errorf("a turn admitted at %+v and Ask started a child at %+v and %v", turn, k.Model, k.Access)
	}
}

// A thread narrowed under its running turn narrows the children the turn
// starts from then on.
func TestAThreadNarrowedUnderItsTurnNarrowsTheTurnsNewChildren(t *testing.T) {
	h := engine(t)
	o := workingAt(t, h, api.AccessFull)
	configure(t, h, o.Thread, api.ThreadConfigure{Access: new(api.AccessAutoEdits)})
	if r := serve(t, h, o, delegate(1, `{"task":"Check the callers"}`, ""))[1]; r.Error != nil || r.Result.IsError {
		t.Fatalf("delegate: %+v", r)
	}
	if kids := children(t, h, o.Thread); len(kids) != 1 || kids[0].Access != api.AccessAutoEdits {
		t.Errorf("the children of a turn admitted at Full, its thread narrowed to AutoEdits since: %+v, want one at AutoEdits", kids)
	}
}

// A retry of a tool use finds the child its first try made, whatever the
// thread was given in between.
func TestARetriedToolUseFindsItsChildAfterAConfigure(t *testing.T) {
	for name, c := range map[string]api.ThreadConfigure{
		"effort":   {Effort: new("low")},
		"widened":  {Access: new(api.AccessFull)},
		"narrowed": {Access: new(api.AccessAsk)},
	} {
		t.Run(name, func(t *testing.T) {
			h := engine(t)
			o := workingAt(t, h, api.AccessAutoEdits)
			call := delegate(1, `{"task":"Check the callers"}`, `{"claudecode/toolUseId":"toolu_A"}`)
			first := serve(t, h, o, call)[1]
			if first.Error != nil || first.Result.IsError {
				t.Fatalf("the first try: %+v", first)
			}
			configure(t, h, o.Thread, c)
			again := serve(t, h, o, call)[1]
			if again.Error != nil || again.Result.IsError || again.Result.StructuredContent.TaskID != first.Result.StructuredContent.TaskID {
				t.Errorf("the retry gave %+v, want the first try's child %s", again.Result, first.Result.StructuredContent.TaskID)
			}
			if n := len(children(t, h, o.Thread)); n != 1 {
				t.Errorf("%d children, want 1", n)
			}
		})
	}
}
