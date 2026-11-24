package runtime_test

import (
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// A native subagent's events land in its run's log under the step that
// started it, and stay out of everything that is the run's: the reply its
// parent's report gives, and the tokens its turn used.
func TestANativeSubagentsEventsAreKeptApartFromItsRun(t *testing.T) {
	nest := func(e provider.Event) provider.Event { return provider.Nested{Parent: "task-1", Event: e} }
	ad := fake.New([]provider.Event{
		provider.Work{Item: "task-1", Kind: "tool", Title: "Agent Find the bug", Status: api.WorkStarted},
		nest(provider.TextDelta{Text: "The subagent's own words."}),
		nest(provider.Work{Item: "cmd-1", Kind: "command", Title: "go test ./...", Status: api.WorkCompleted}),
		nest(provider.TurnEnded{Outcome: api.OutcomeCompleted, Tokens: 7000}),
		provider.Work{Item: "task-1", Kind: "tool", Title: "Agent Find the bug", Status: api.WorkCompleted},
		provider.TextDelta{Text: "The bug is in the cache."},
		nest(provider.TextDelta{Text: "Said after the run's last words."}),
	})
	ad.Tokens = 1500
	n := start(t, dbPath(t), ad)
	parent := newThread(t, n.h)
	c, err := newChild(t, n.h, ad, parent, api.AccessAsk)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, c, dispatch("find it", api.StartNow))
	until(t, n.h, parent, "the report", func(th api.Thread) bool { return !th.Reported.IsZero() })
	for _, e := range events(t, n.h, parent) {
		if b, err := e.Decode(); err == nil {
			if r, ok := b.(api.ChildReported); ok && r.Outcome != "The bug is in the cache." {
				t.Errorf("the report says %q, want the run's own last words", r.Outcome)
			}
		}
	}
	if cv := until(t, n.h, c, "the child's turn", idle); cv.Tokens != 1500 {
		t.Errorf("the child's turn used %d tokens, want its own 1500 without its subagent's", cv.Tokens)
	}
	var texts []string
	var work []api.SubagentWork
	var end []api.SubagentEnded
	for _, e := range events(t, n.h, c) {
		b, err := e.Decode()
		if err != nil {
			t.Fatal(err)
		}
		switch b := b.(type) {
		case api.SubagentDelta:
			if b.Parent == "task-1" {
				texts = append(texts, b.Text)
			}
		case api.SubagentWork:
			work = append(work, b)
		case api.SubagentEnded:
			end = append(end, b)
		}
	}
	if len(texts) != 2 || texts[0] != "The subagent's own words." {
		t.Errorf("the log has the subagent saying %q, want both its texts", texts)
	}
	if len(work) != 1 || work[0].Parent != "task-1" || work[0].Item != "cmd-1" || work[0].Status != api.WorkCompleted {
		t.Errorf("the log has the subagent's steps %+v, want its one command under task-1", work)
	}
	if len(end) != 1 || end[0].Tokens != 7000 || end[0].Outcome != api.OutcomeCompleted {
		t.Errorf("the log has the subagent ending %+v, want completed with its 7000 tokens", end)
	}
}
