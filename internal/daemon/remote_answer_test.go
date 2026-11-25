package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/mcp"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// taskTool has the parent's agent on A call one task tool, as its glitched
// mcp would, and returns its text and whether it was an error.
func taskTool(t *testing.T, ep api.Endpoint, o mcp.Origin, name string, args any) (string, bool) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := mcp.Serve(context.Background(), bytes.NewReader(append(b, '\n')), &out, &mcp.Tools{Engine: ep, Origin: o}); err != nil {
		t.Fatal(err)
	}
	var r struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || len(r.Result.Content) == 0 {
		t.Fatalf("%s: %q %v", name, out.String(), err)
	}
	return r.Result.Content[0].Text, r.Result.IsError
}

// A parent's agent on A answers its child's question on B with
// task_answer, with the request id its wake named: the answer reaches B,
// where the child takes it and its turn goes on and ends, which wakes the
// parent with its result. An approval of B's stays B's person's.
func TestAParentAnswersItsRemoteChildsQuestion(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	// A's parent keeps a turn live, waiting on its own approval, so its
	// agent can act for it, as one does while it works.
	p.stopA()
	p.adA = fake.New(fake.Turn(true))
	p.startA()
	parent := api.NewID()
	if _, err := p.a.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: parent, Body: api.ThreadCreate{Workspace: p.repo, Title: "asking parent", Model: p.model, Access: api.AccessAsk}}); err != nil {
		t.Fatal(err)
	}
	p.parent = parent
	kid := p.place("asks")
	b := p.bound(kid).Thread
	until(t, "the question's wake on A", func() bool { _, wakes := p.reports(kid); return len(wakes) == 1 })
	_, wakes := p.reports(kid)
	there := p.onB(b)
	if len(there.Requests) != 1 || there.Requests[0].Kind != api.KindQuestion {
		t.Fatalf("B's child waits on %+v, want one question", there.Requests)
	}
	question := there.Requests[0].ID
	if !strings.Contains(wakes[0].Text, `request="`+question.String()+`"`) || !strings.Contains(wakes[0].Text, "task_answer") {
		t.Fatalf("the wake: %q, want it to name the question and task_answer", wakes[0].Text)
	}
	until(t, "A's parent turn live", func() bool {
		v := engine.View(p.placeholder(parent))
		return v.Run == api.RunWaiting
	})
	o := mcp.Origin{Thread: parent, Access: api.AccessAsk}
	answer := map[string]any{"task_id": kid.String(), "request_id": question.String(), "answers": []map[string]any{{"picked": []string{"SQLite"}}, {"picked": []string{"Unit"}}}, "client_request_id": "pick"}
	if text, failed := taskTool(t, p.a.Endpoint(), o, "task_answer", answer); failed || !strings.Contains(text, `"sent"`) {
		t.Fatalf("task_answer: %s, want it sent", text)
	}
	until(t, "B's child answered, and its turn done", func() bool {
		th := p.onB(b)
		r, _ := th.Request(question)
		v := engine.View(th)
		return r.State == api.RequestDelivered && th.Live() == nil && v.LastTurn != nil && v.LastTurn.Outcome == api.OutcomeCompleted
	})
	if r, _ := p.onB(b).Request(question); !strings.Contains(string(r.Answer), "SQLite") {
		t.Fatalf("B's child got %s, want the parent's answer", r.Answer)
	}
	until(t, "the child's result waking A's parent", func() bool { _, wakes := p.reports(kid); return len(wakes) == 2 })
	if text, failed := taskTool(t, p.a.Endpoint(), o, "task_answer", answer); failed {
		t.Fatalf("task_answer retried: %s, want its first receipt", text)
	}
	answer["client_request_id"] = "again"
	if text, failed := taskTool(t, p.a.Endpoint(), o, "task_answer", answer); !failed || !strings.Contains(text, "waits on no request") {
		t.Fatalf("a second answer: %q, want it refused", text)
	}
}

// A parent's agent on A can't answer an approval its child on B waits on:
// A refuses it, and a link's answer to it is refused on B too.
func TestAParentCannotAnswerItsRemoteChildsApproval(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(true)), "fake", engine.Limits{})
	p.stopA()
	p.adA = fake.New(fake.Turn(true))
	p.startA()
	parent := api.NewID()
	if _, err := p.a.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: parent, Body: api.ThreadCreate{Workspace: p.repo, Title: "approving parent", Model: p.model, Access: api.AccessAsk}}); err != nil {
		t.Fatal(err)
	}
	p.parent = parent
	kid := p.place("edits")
	b := p.bound(kid).Thread
	until(t, "the approval on B, followed by A", func() bool {
		th := p.onB(b)
		return len(th.Requests) == 1 && slices.Contains(p.placeholder(kid).Remote.Pending, th.Requests[0].ID)
	})
	approval := p.onB(b).Requests[0]
	if _, err := p.a.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: parent, Body: api.MessageDispatch{Text: "go on", Mode: api.AfterActive}}); err != nil {
		t.Fatal(err)
	}
	until(t, "A's parent turn live", func() bool { return engine.View(p.placeholder(parent)).Run == api.RunWaiting })
	text, failed := taskTool(t, p.a.Endpoint(), mcp.Origin{Thread: parent, Access: api.AccessAsk}, "task_answer", map[string]any{"task_id": kid.String(), "request_id": approval.ID.String(), "answers": []map[string]any{{"picked": []string{"yes"}}}})
	if !failed || !strings.Contains(text, "approvals and credentials are the user's") {
		t.Fatalf("task_answer of an approval: %q, want it refused", text)
	}
	if _, err := p.a.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: kid, Body: api.RequestRespond{Request: approval.ID, Answer: []byte(`{"allow":true}`)}}); err == nil {
		t.Fatal("A's person answered the approval on B through its placeholder")
	}
	ep := transport.NewEndpointClient(transport.Tailnet{Address: p.addrB, Token: p.token}, api.ID{})
	defer ep.Close()
	_, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: b, Body: api.RequestRespond{Request: approval.ID, Answer: []byte(`{"allow":true}`)}})
	if err == nil || !strings.Contains(err.Error(), "approvals and credentials are the user's") {
		t.Fatalf("a link's answer of an approval: %v, want it refused", err)
	}
	if r, _ := p.onB(b).Request(approval.ID); r.State != api.RequestPending {
		t.Fatalf("B's approval is %v, want it still pending", r.State)
	}
}

// Once B's person took the child over, by a message of their own while it
// waits on its question, A's parent's agent can't answer it: A refuses at
// once, and the question stays B's person's.
func TestARemoteChildTakenOverThereTakesNoAnswerFromItsParent(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	p.stopA()
	p.adA = fake.New(fake.Turn(true))
	p.startA()
	parent := api.NewID()
	if _, err := p.a.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: parent, Body: api.ThreadCreate{Workspace: p.repo, Title: "asking parent", Model: p.model, Access: api.AccessAsk}}); err != nil {
		t.Fatal(err)
	}
	p.parent = parent
	kid := p.place("asks")
	b := p.bound(kid).Thread
	until(t, "the question's wake on A", func() bool { _, wakes := p.reports(kid); return len(wakes) == 1 })
	question := p.onB(b).Requests[0].ID
	if _, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: b, Body: api.MessageDispatch{Text: "mine now", Mode: api.AfterActive}}); err != nil {
		t.Fatal(err)
	}
	until(t, "A sees the takeover", func() bool { return p.placeholder(kid).Remote.Taken })
	until(t, "A's parent turn live", func() bool { return engine.View(p.placeholder(parent)).Run == api.RunWaiting })
	text, failed := taskTool(t, p.a.Endpoint(), mcp.Origin{Thread: parent, Access: api.AccessAsk}, "task_answer", map[string]any{"task_id": kid.String(), "request_id": question.String(), "answers": []map[string]any{{"picked": []string{"SQLite"}}, {"picked": []string{"Unit"}}}})
	if !failed || !strings.Contains(text, "took it over") {
		t.Fatalf("task_answer after a takeover: %q, want it refused", text)
	}
	if r, _ := p.onB(b).Request(question); r.State != api.RequestPending {
		t.Fatalf("B's question is %v, want it still pending", r.State)
	}
}
