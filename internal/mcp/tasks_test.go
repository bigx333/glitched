package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/mcp"
	"glitched/internal/runtime"
)

func call(id int, name, args string) string {
	return `{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + "}}"
}

// task is what one task tool call answered: its state, or its refusal.
func task(t *testing.T, h *runtime.Host, o mcp.Origin, name, args string) (mcp.TaskState, string) {
	t.Helper()
	r := serve(t, h, o, call(1, name, args))[1]
	if r.Error != nil {
		t.Fatalf("%s: %+v", name, r.Error)
	}
	if r.Result.IsError {
		return mcp.TaskState{}, r.Result.Content[0].Text
	}
	var s mcp.TaskState
	json.Unmarshal([]byte(r.Result.Content[0].Text), &s)
	return s, ""
}

// until waits for thread to be as ok says.
func until(t *testing.T, h *runtime.Host, thread api.ID, what string, ok func(api.Thread) bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		snap, err := h.Read(ctx, api.Query{Thread: thread})
		if err == nil && len(snap.Threads) == 1 && ok(snap.Threads[0]) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %+v %v", what, snap.Threads, err)
		}
	}
}

func waiting(th api.Thread) bool { return th.Run == api.RunWaiting }

// child starts a child of o through delegate_task and returns it once its
// turn waits at its approval.
func child(t *testing.T, h *runtime.Host, o mcp.Origin, task string) api.Thread {
	t.Helper()
	if r := serve(t, h, o, delegate(1, `{"task":"`+task+`"}`, ""))[1]; r.Error != nil || r.Result.IsError {
		t.Fatalf("delegate: %+v", r)
	}
	for _, k := range children(t, h, o.Thread) {
		if k.Title == task {
			until(t, h, k.ID, "the child waiting", waiting)
			return k
		}
	}
	t.Fatalf("no child %q", task)
	return api.Thread{}
}

// A parent's agent reads its child, stops it, and sends it on; once the
// user sends the child a message of their own, the child is theirs, and
// the parent's agent can neither stop nor message it.
func TestAParentStopsAndMessagesItsChildTillTheUserTakesItOver(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	kid := child(t, h, o, "Review it")
	id := `{"task_id":"` + kid.ID.String() + `"}`
	if s, err := task(t, h, o, "task_status", id); err != "" || s.Status != "waiting_on_user" || len(s.Waiting) != 1 || s.Waiting[0] != "approval" {
		t.Fatalf("task_status: %+v %s, want it waiting on the user for an approval", s, err)
	}
	if s, err := task(t, h, o, "task_cancel", id); err != "" || (s.Status != "stopping" && s.Status != "stopped") {
		t.Fatalf("task_cancel: %+v %s, want it stopping", s, err)
	}
	until(t, h, kid.ID, "the child stopped", func(th api.Thread) bool { return th.Run == api.RunIdle })
	send := `{"task_id":"` + kid.ID.String() + `","message":"go on"}`
	if _, err := task(t, h, o, "task_send", send); err != "" {
		t.Fatalf("task_send: %s", err)
	}
	until(t, h, kid.ID, "the child at work on the message", waiting)
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: kid.ID, Body: api.MessageDispatch{Text: "mine now", Mode: api.AfterActive}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, args string }{{"task_cancel", id}, {"task_send", send}} {
		if _, err := task(t, h, o, c.name, c.args); !strings.Contains(err, "the user took this thread over") {
			t.Errorf("%s after the user's message: %q, want a refusal that says the user took it over", c.name, err)
		}
	}
	if _, err := task(t, h, o, "task_status", id); err != "" {
		t.Errorf("task_status of a child the user took over: %s, want its state", err)
	}
}

// A cancel stops every thread under the task too.
func TestACancelStopsTheThreadsUnderTheTask(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	kid := child(t, h, o, "Split it")
	snap, err := h.Read(ctx, api.Query{Thread: kid.ID})
	if err != nil {
		t.Fatal(err)
	}
	grandchild := api.NewID()
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: grandchild, Body: api.ThreadCreate{
		Workspace: kid.Workspace, Title: "Part", Model: kid.Model, Parent: kid.ID, Run: snap.Threads[0].Turn.Run, Text: "part",
	}}); err != nil {
		t.Fatal(err)
	}
	until(t, h, grandchild, "the grandchild waiting", waiting)
	id := `{"task_id":"` + kid.ID.String() + `"}`
	if s, err := task(t, h, o, "task_status", id); err != "" || s.Working != 1 {
		t.Fatalf("task_status: %+v %s, want one thread under it at work", s, err)
	}
	if _, err := task(t, h, o, "task_cancel", id); err != "" {
		t.Fatalf("task_cancel: %s", err)
	}
	until(t, h, grandchild, "the grandchild stopped", func(th api.Thread) bool { return th.Run == api.RunIdle })
}

// The task tools act on the caller's own children only, and only while
// the caller's turn runs.
func TestTaskToolsActOnlyOnTheCallersChildrenFromItsLiveTurn(t *testing.T) {
	h := engine(t)
	o, other := working(t, h), working(t, h)
	kid := child(t, h, other, "Theirs")
	id := `{"task_id":"` + kid.ID.String() + `"}`
	for _, name := range []string{"task_status", "task_cancel"} {
		if _, err := task(t, h, o, name, id); !strings.Contains(err, "no task") {
			t.Errorf("%s of another thread's child: %q, want no task", name, err)
		}
	}
	// A command that names a caller is checked in its commit: the
	// caller's run must be live and the thread above the target.
	stale := api.Caller{Thread: other.Thread, Run: api.NewID()}
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: kid.ID, Caller: &stale, Body: api.TurnInterrupt{}}); err == nil || !strings.Contains(err.Error(), "only the live run") {
		t.Errorf("a stop from a run that isn't live: %v, want refused", err)
	}
	snap, err := h.Read(ctx, api.Query{Thread: o.Thread})
	if err != nil {
		t.Fatal(err)
	}
	stranger := api.Caller{Thread: o.Thread, Run: snap.Threads[0].Turn.Run}
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: kid.ID, Caller: &stranger, Body: api.MessageDispatch{Text: "hi", Mode: api.AfterActive}}); err == nil || !strings.Contains(err.Error(), "under its own") {
		t.Errorf("a message from a thread not above the target: %v, want refused", err)
	}
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: kid.ID, Caller: &stranger, Body: api.ThreadRename{Title: "x"}}); err == nil {
		t.Error("a rename that names a caller was taken, want refused")
	}
	if got := serve(t, h, o, call(1, "list_tasks", "{}"))[1]; got.Result.IsError || strings.Contains(got.Result.Content[0].Text, kid.ID.String()) {
		t.Errorf("list_tasks: %+v, want this thread's tasks only", got)
	}
}

// The user's stop takes a child over as their message does.
func TestAChildTheUserStoppedIsTheirs(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	kid := child(t, h, o, "Review it")
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: kid.ID, Body: api.TurnInterrupt{}}); err != nil {
		t.Fatal(err)
	}
	until(t, h, kid.ID, "the child stopped", func(th api.Thread) bool { return th.Run == api.RunIdle })
	send := `{"task_id":"` + kid.ID.String() + `","message":"go on"}`
	if _, err := task(t, h, o, "task_send", send); !strings.Contains(err, "the user took this thread over") {
		t.Errorf("task_send to a child the user stopped: %q, want a refusal that says the user took it over", err)
	}
}
