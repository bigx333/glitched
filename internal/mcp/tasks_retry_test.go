package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/mcp"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

func submitTo(t *testing.T, h *runtime.Host, th api.ID, body api.Intent) api.ID {
	t.Helper()
	id := api.NewID()
	if _, err := h.Submit(ctx, api.Command{ID: id, Target: th, Body: body}); err != nil {
		t.Fatal(err)
	}
	return id
}

func threadOf(t *testing.T, ep api.Endpoint, thread api.ID) api.Thread {
	t.Helper()
	s, err := ep.Read(ctx, api.Query{Thread: thread})
	if err != nil || len(s.Threads) != 1 {
		t.Fatalf("read %s: %v", thread, err)
	}
	return s.Threads[0]
}

func turnOf(t *testing.T, h *runtime.Host, thread api.ID) api.ID {
	t.Helper()
	th := threadOf(t, h, thread)
	if th.Turn == nil {
		t.Fatalf("no live turn in %s", thread)
	}
	return th.Turn.Run
}

func taskArgs(id api.ID, extra string) string {
	return `{"task_id":"` + id.String() + `"` + extra + `}`
}

type readGate struct {
	api.Endpoint
	got, release chan struct{}
	once         sync.Once
}

func (g *readGate) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	s, err := g.Endpoint.Read(ctx, q)
	if q.Thread.IsZero() {
		g.once.Do(func() {
			close(g.got)
			<-g.release
		})
	}
	return s, err
}

func replaceRun(t *testing.T, h *runtime.Host, o mcp.Origin) api.ID {
	t.Helper()
	old := turnOf(t, h, o.Thread)
	submitTo(t, h, o.Thread, api.TurnInterrupt{})
	until(t, h, o.Thread, "first parent run stopped", func(th api.Thread) bool { return th.Run == api.RunIdle })
	next := submitTo(t, h, o.Thread, api.MessageDispatch{Text: "new parent turn", Mode: api.AfterActive})
	until(t, h, o.Thread, "second parent run waiting", waiting)
	th := threadOf(t, h, o.Thread)
	if old == next || th.Turn.Run != next || th.Turn.Access != o.Access {
		t.Fatalf("expected a replacement turn at the same access: %+v", th.Turn)
	}
	return next
}

func TestATaskCallActsForTheTurnItStartedIn(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	kid := child(t, h, o, "original child")
	g := &readGate{Endpoint: h, got: make(chan struct{}), release: make(chan struct{})}
	var b bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- mcp.Serve(ctx, strings.NewReader(call(1, "task_send", taskArgs(kid.ID, `,"message":"from the old turn"`))+"\n"), &b, &mcp.Tools{Engine: g, Origin: o})
	}()
	<-g.got
	defer func() {
		select {
		case <-g.release:
		default:
			close(g.release)
		}
	}()
	next := replaceRun(t, h, o)
	close(g.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var r reply
	if err := json.Unmarshal(b.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if !r.Result.IsError {
		t.Fatalf("an in-flight call from the ended turn queued a message under replacement run %s: %s", next, b.String())
	}
}

func TestASendRetriedFromTheNextTurnGetsItsFirstReceipt(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	kid := child(t, h, o, "retry target")
	args := taskArgs(kid.ID, `,"message":"exactly one","client_request_id":"one-send"`)
	if _, err := task(t, h, o, "task_send", args); err != "" {
		t.Fatal(err)
	}
	if _, err := task(t, h, o, "task_send", args); err != "" {
		t.Fatalf("same-turn retry: %s", err)
	}
	replaceRun(t, h, o)
	_, retryErr := task(t, h, o, "task_send", args)
	s, err := h.Read(ctx, api.Query{Thread: kid.ID})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range s.Events {
		if b, _ := e.Decode(); b != nil {
			if q, ok := b.(api.MessageQueued); ok && q.Text == "exactly one" {
				count++
			}
		}
	}
	if retryErr != "" || count != 1 {
		t.Fatalf("cross-turn retry: %q; %d queued copies, want the original success and exactly one copy", retryErr, count)
	}
}

func reviewGrandchild(t *testing.T, h *runtime.Host, kid api.Thread) api.ID {
	t.Helper()
	return reviewCreateGrandchild(t, h, kid, turnOf(t, h, kid.ID))
}

func reviewCreateGrandchild(t *testing.T, h *runtime.Host, kid api.Thread, run api.ID) api.ID {
	t.Helper()
	grand := api.NewID()
	submitTo(t, h, grand, api.ThreadCreate{Workspace: kid.Workspace, Title: "late grandchild", Model: kid.Model, Parent: kid.ID, Run: run, Text: "later work"})
	until(t, h, grand, "late grandchild waiting", waiting)
	return grand
}

func TestACancelStopsTheTreeAsItIsAtItsCommit(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	kid := child(t, h, o, "root of cancel")
	g := &readGate{Endpoint: h, got: make(chan struct{}), release: make(chan struct{})}
	var b bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- mcp.Serve(ctx, strings.NewReader(call(1, "task_cancel", taskArgs(kid.ID, `,"client_request_id":"stop-once"`))+"\n"), &b, &mcp.Tools{Engine: g, Origin: o})
	}()
	<-g.got
	defer func() {
		select {
		case <-g.release:
		default:
			close(g.release)
		}
	}()
	grand := reviewGrandchild(t, h, kid)
	close(g.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var r reply
	if err := json.Unmarshal(b.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Result.IsError {
		t.Fatalf("task_cancel: %s", b.String())
	}
	th := threadOf(t, h, grand)
	if th.Run == api.RunWaiting {
		t.Fatalf("successful cancel left grandchild %s waiting; it was live before the first stop committed: %s", grand, b.String())
	}
}

func TestARetriedCancelStopsNothingNew(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	kid := child(t, h, o, "root of cancel")
	args := taskArgs(kid.ID, `,"client_request_id":"stop-once"`)
	if _, err := task(t, h, o, "task_cancel", args); err != "" {
		t.Fatal(err)
	}
	until(t, h, kid.ID, "child stopped", func(th api.Thread) bool { return th.Run == api.RunIdle })
	if _, err := task(t, h, o, "task_send", taskArgs(kid.ID, `,"message":"start again"`)); err != "" {
		t.Fatal(err)
	}
	until(t, h, kid.ID, "child restarted", waiting)
	grand := reviewGrandchild(t, h, kid)
	if _, err := task(t, h, o, "task_cancel", args); err != "" {
		t.Fatalf("retry: %s", err)
	}
	th := threadOf(t, h, grand)
	if th.Run != api.RunWaiting {
		t.Fatalf("replaying a completed cancel stopped new work, grandchild run state: %v", th.Run)
	}
}

func TestTaskToolsAnswerWithObjects(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	kid := child(t, h, o, "schema target")
	for _, q := range []struct{ name, args string }{
		{"list_tasks", "{}"},
		{"task_status", taskArgs(kid.ID, "")},
		{"task_send", taskArgs(kid.ID, `,"message":"queue me"`)},
		{"task_cancel", taskArgs(kid.ID, "")},
	} {
		var b bytes.Buffer
		if err := mcp.Serve(ctx, strings.NewReader(call(1, q.name, q.args)+"\n"), &b, &mcp.Tools{Engine: h, Origin: o}); err != nil {
			t.Fatal(err)
		}
		var r struct {
			Result struct {
				Structured map[string]json.RawMessage `json:"structuredContent"`
				Error      bool                       `json:"isError"`
				Content    []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(b.Bytes(), &r); err != nil || r.Result.Error || len(r.Result.Structured) == 0 || len(r.Result.Content) != 1 {
			t.Fatalf("%s: %s; decode %v", q.name, b.String(), err)
		}
		if q.name == "list_tasks" {
			var list []mcp.TaskState
			if err := json.Unmarshal(r.Result.Structured["tasks"], &list); err != nil || len(list) != 1 || list[0].TaskID != kid.ID.String() {
				t.Fatalf("list_tasks: %s; %v", b.String(), err)
			}
		}
	}
}

type reviewStoreEndpoint struct {
	api.Endpoint
	*store.Store
}

func (ep reviewStoreEndpoint) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	return ep.Store.Read(ctx, q)
}
func (ep reviewStoreEndpoint) Submit(ctx context.Context, c api.Command) (api.Receipt, error) {
	return ep.Store.Submit(ctx, c, time.Now())
}

func TestASendRetriedAfterTheUserTookOverQueuesNothingMore(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	kid := child(t, h, o, "retry target")
	args := taskArgs(kid.ID, `,"message":"exactly one","client_request_id":"one-send"`)
	if _, err := task(t, h, o, "task_send", args); err != "" {
		t.Fatal(err)
	}
	submitTo(t, h, kid.ID, api.MessageDispatch{Text: "mine now", Mode: api.AfterActive})
	before := threadOf(t, h, kid.ID)
	if _, err := task(t, h, o, "task_send", args); err != "" {
		t.Fatalf("same command did not replay its receipt: %s", err)
	}
	after := threadOf(t, h, kid.ID)
	if before.Revision != after.Revision || before.Queued != after.Queued {
		t.Fatalf("replay changed child's state: before %+v, after %+v", before, after)
	}
}
