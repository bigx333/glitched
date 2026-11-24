package mcp_test

import (
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
)

// wakes is the text of each message the engine queued in thread for a
// report of child's.
func wakes(t *testing.T, h *runtime.Host, thread, child api.ID) []string {
	t.Helper()
	snap, err := h.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range snap.Events {
		if b, err := e.Decode(); err == nil {
			if m, ok := b.(api.MessageQueued); ok && m.Report == child {
				out = append(out, m.Text)
			}
		}
	}
	return out
}

// A child's question wakes its parent's agent with the question and its
// request, the parent's agent answers it, and the child's turn goes on
// and ends, which wakes the parent again. A retry of that answer gets its
// receipt, and another answer is refused.
func TestAChildsQuestionWakesItsParentWhichAnswersIt(t *testing.T) {
	h := engineOf(t, provider.Mux{"fake": fake.New(fake.Asking())})
	o := working(t, h)
	kid := threadOf(t, h, child(t, h, o, "Pick a store").ID)
	if len(kid.Requests) != 1 || kid.Requests[0].Kind != api.KindQuestion {
		t.Fatalf("child waits on %+v, want one question", kid.Requests)
	}
	req := kid.Requests[0].ID.String()
	until(t, h, o.Thread, "the question's wake", func(api.Thread) bool { return len(wakes(t, h, o.Thread, kid.ID)) == 1 })
	w := wakes(t, h, o.Thread, kid.ID)[0]
	for _, want := range []string{`state="waiting_on_question"`, `request="` + req + `"`, "1. Which store should the cache use?", "- SQLite: One file next to the log", "2. Which suites gate the merge? (pick any)", "task_answer"} {
		if !strings.Contains(w, want) {
			t.Fatalf("the wake says %q, want it to say %q", w, want)
		}
	}
	s, err := task(t, h, o, "task_status", `{"task_id":"`+kid.ID.String()+`"}`)
	if err != "" || len(s.Questions) != 1 || s.Questions[0].RequestID != req || len(s.Questions[0].Questions) != 2 {
		t.Fatalf("task_status: %+v %s, want the question and its request", s, err)
	}
	answer := `{"task_id":"` + kid.ID.String() + `","request_id":"` + req + `","answers":[{"picked":["SQLite"]},{"picked":["Unit","Race"]}],"client_request_id":"pick-store"}`
	if _, err := task(t, h, o, "task_answer", answer); err != "" {
		t.Fatalf("task_answer: %s", err)
	}
	until(t, h, kid.ID, "the child's turn ending", func(th api.Thread) bool {
		return th.Run == api.RunIdle && th.LastTurn != nil && th.LastTurn.Outcome == api.OutcomeCompleted
	})
	until(t, h, o.Thread, "the child's report waking it again", func(api.Thread) bool { return len(wakes(t, h, o.Thread, kid.ID)) == 2 })
	if w := wakes(t, h, o.Thread, kid.ID)[1]; !strings.Contains(w, `state="completed"`) {
		t.Fatalf("the second wake says %q, want the child's result", w)
	}
	if _, err := task(t, h, o, "task_answer", answer); err != "" {
		t.Fatalf("task_answer retried: %s, want the first answer's receipt", err)
	}
	if _, err := task(t, h, o, "task_answer", strings.Replace(answer, "pick-store", "pick-again", 1)); !strings.Contains(err, "already answered") {
		t.Fatalf("a second task_answer: %q, want it refused as answered", err)
	}
}

// A child's approval is the user's: it wakes no parent, and task_answer
// refuses it.
func TestAParentCannotAnswerItsChildsApproval(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	kid := threadOf(t, h, child(t, h, o, "Review it").ID)
	if len(kid.Requests) != 1 || kid.Requests[0].Kind != api.KindApproval {
		t.Fatalf("child waits on %+v, want one approval", kid.Requests)
	}
	until(t, h, o.Thread, "the child's report", func(api.Thread) bool { return reported(t, h, o.Thread, kid.ID) })
	if w := wakes(t, h, o.Thread, kid.ID); len(w) != 0 {
		t.Fatalf("an approval woke the parent: %q", w)
	}
	args := `{"task_id":"` + kid.ID.String() + `","request_id":"` + kid.Requests[0].ID.String() + `","answers":[{"other":"allow"}]}`
	if _, err := task(t, h, o, "task_answer", args); !strings.Contains(err, "approvals and credentials are the user's") {
		t.Fatalf("task_answer on an approval: %q, want it refused", err)
	}
	until(t, h, kid.ID, "the child still waiting", func(th api.Thread) bool { return th.Run == api.RunWaiting && len(th.Requests) == 1 })
}

// reported is whether thread's log has a report of child's.
func reported(t *testing.T, h *runtime.Host, thread, child api.ID) bool {
	t.Helper()
	snap, err := h.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range snap.Events {
		if b, err := e.Decode(); err == nil {
			if r, ok := b.(api.ChildReported); ok && r.Child == child {
				return true
			}
		}
	}
	return false
}
