package client

import (
	"slices"
	"testing"

	"glitched/internal/api"
)

// A native subagent's events fold into the step that started it, in the
// order they came: its text runs between its steps, each step as it last
// was, and how it ended. None of it is the run's own: its reply and its
// steps stay as they were, the step keeps its subagent as the step moves
// on, and a subagent under a step the run never took is nobody's.
func TestFoldNestsASubagentUnderItsStep(t *testing.T) {
	a := api.NewID()
	exit := 0
	task := api.WorkItem{Run: a, Item: "task-1", Kind: "tool", Title: "Agent alpha", Status: api.WorkStarted}
	cmd := api.WorkItem{Run: a, Item: "c-1", Kind: "command", Title: "ls", Status: api.WorkStarted}
	tr := play(slices.Concat(start(a, "go", 0), []api.EventBody{
		task,
		api.SubagentDelta{Run: a, Parent: "task-1", Text: "Looking"},
		api.SubagentDelta{Run: a, Parent: "task-1", Text: " around."},
		api.SubagentWork{Parent: "task-1", WorkItem: cmd},
		api.SubagentDelta{Run: a, Parent: "nobody", Text: "lost"},
		api.SubagentWork{Parent: "task-1", WorkItem: api.WorkItem{Run: a, Item: "c-1", Status: api.WorkCompleted, Exit: &exit}},
		api.SubagentDelta{Run: a, Parent: "task-1", Text: "Found it."},
		api.WorkItem{Run: a, Item: "task-1", Kind: "tool", Title: "Agent alpha", Status: api.WorkCompleted},
		api.SubagentEnded{Run: a, Parent: "task-1", Outcome: api.OutcomeCompleted, Tokens: 11738},
		api.AssistantDelta{Run: a, Text: "Alpha found it."},
	})...)
	expect(t, tr,
		`user "go" sent`,
		`work task-1:completed`,
		`assistant "Alpha found it." streaming`,
	)
	s := tr.Entries[1].(*WorkEntry).Items[0].Subagent
	if s == nil {
		t.Fatal("the Agent step has no subagent")
	}
	if len(s.Parts) != 3 || s.Parts[0].Text != "Looking around." || s.Parts[2].Text != "Found it." {
		t.Fatalf("the subagent's parts are %+v, want its text, its step, then its text", s.Parts)
	}
	if st := s.Parts[1].Step; st == nil || st.Title != "ls" || st.Kind != "command" || st.Status != api.WorkCompleted || st.Exit == nil || *st.Exit != 0 {
		t.Fatalf("the subagent's step is %+v, want ls completed with exit 0, its title and kind kept", s.Parts[1].Step)
	}
	if !s.Ended || s.Outcome != api.OutcomeCompleted || s.Tokens != 11738 || s.Steps() != 1 {
		t.Fatalf("the subagent ended %v with %v and %d tokens over %d steps, want completed with 11738 over 1", s.Ended, s.Outcome, s.Tokens, s.Steps())
	}
}

// Each change to a subagent tells a reader of the transcript's edits that
// its step's entry changed, so a view of the entry draws it again.
func TestASubagentsChangeIsAnEditOfItsStepsEntry(t *testing.T) {
	a := api.NewID()
	tr := play(slices.Concat(start(a, "go", 0), []api.EventBody{
		api.WorkItem{Run: a, Item: "task-1", Kind: "tool", Title: "Agent alpha", Status: api.WorkStarted},
	})...)
	_, _, mark := tr.Edits(EditMark{})
	for i, ev := range []api.EventBody{
		api.SubagentDelta{Run: a, Parent: "task-1", Text: "Hi"},
		api.SubagentWork{Parent: "task-1", WorkItem: api.WorkItem{Run: a, Item: "c-1", Kind: "command", Title: "ls", Status: api.WorkStarted}},
		api.SubagentEnded{Run: a, Parent: "task-1", Outcome: api.OutcomeFailed},
	} {
		tr.apply(event(5+i, ev))
		edited, all, next := tr.Edits(mark)
		if all || !slices.Equal(edited, []int{1}) {
			t.Fatalf("after %T the edits are %v (all %v), want the work entry's alone", ev, edited, all)
		}
		mark = next
	}
}
