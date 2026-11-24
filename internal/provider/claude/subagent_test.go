package claude

import (
	"reflect"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// events is every event tn reads of ms.
func events(tn *turn, ms ...message) []provider.Event {
	var out []provider.Event
	for _, m := range ms {
		out = append(out, tn.read(m)...)
	}
	return out
}

// A native subagent's messages nest under the Agent step that started it:
// its text, its steps as they open and finish, and how it ended with the
// tokens it used. None of it is the run's own text, context or tokens, and
// a message under a parent the turn never started is nobody's.
func TestANativeSubagentNestsUnderItsStep(t *testing.T) {
	got := events(newTurn(t.TempDir()),
		line(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_A","name":"Agent","input":{"description":"Find the bug","prompt":"Look"}}]}}`),
		line(t, `{"type":"stream_event","parent_tool_use_id":"toolu_A","event":{"type":"message_start","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":90000}}}}`),
		line(t, `{"type":"assistant","parent_tool_use_id":"toolu_A","message":{"model":"claude-haiku-5-5","content":[{"type":"text","text":"Looking."},{"type":"tool_use","id":"toolu_B","name":"Bash","input":{"command":"go test ./..."}}],"usage":{"input_tokens":90000}}}`),
		line(t, `{"type":"user","parent_tool_use_id":"toolu_A","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_B","content":"ok"}]}}`),
		line(t, `{"type":"assistant","parent_tool_use_id":"toolu_A","message":{"content":[{"type":"text","text":"Found it."}]}}`),
		line(t, `{"type":"assistant","parent_tool_use_id":"toolu_Z","message":{"content":[{"type":"text","text":"Nobody's."}]}}`),
		line(t, `{"type":"system","subtype":"task_notification","tool_use_id":"toolu_A","status":"completed","usage":{"total_tokens":11738,"tool_uses":1}}`),
		line(t, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_A","content":"Found it."}]}}`),
		line(t, `{"type":"result","subtype":"success","usage":{"input_tokens":18,"output_tokens":288}}`))
	nest := func(e provider.Event) provider.Event { return provider.Nested{Parent: "toolu_A", Event: e} }
	want := []provider.Event{
		provider.Work{Item: "toolu_A", Kind: "tool", Title: "Agent Find the bug", Status: api.WorkStarted},
		nest(provider.TextDelta{Text: "Looking."}),
		nest(provider.Work{Item: "toolu_B", Kind: "command", Title: "Bash go test ./...", Status: api.WorkStarted}),
		nest(provider.Work{Item: "toolu_B", Kind: "command", Title: "Bash go test ./...", Status: api.WorkCompleted, Output: "ok"}),
		nest(provider.TextDelta{Text: "\n\n"}),
		nest(provider.TextDelta{Text: "Found it."}),
		nest(provider.TurnEnded{Outcome: api.OutcomeCompleted, Tokens: 11738}),
		provider.Work{Item: "toolu_A", Kind: "tool", Title: "Agent Find the bug", Status: api.WorkCompleted, Output: "Found it."},
		provider.TurnEnded{Outcome: api.OutcomeCompleted, Tokens: 306},
	}
	var shown []provider.Event
	for _, e := range got {
		switch e.(type) {
		case provider.Context:
			t.Errorf("the turn measured its context from %+v; a subagent's request isn't the run's", e)
		default:
			shown = append(shown, e)
		}
	}
	if !reflect.DeepEqual(shown, want) {
		t.Errorf("read\n%+v\nwant\n%+v", shown, want)
	}
}

// A turn that ends while its subagent is still at a step fails the step,
// under the subagent, as it fails the run's own.
func TestATurnEndingFailsItsSubagentsOpenSteps(t *testing.T) {
	got := events(newTurn(t.TempDir()),
		line(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_A","name":"Task","input":{"description":"Wait"}}]}}`),
		line(t, `{"type":"assistant","parent_tool_use_id":"toolu_A","message":{"content":[{"type":"tool_use","id":"toolu_B","name":"Bash","input":{"command":"sleep 60"}}]}}`),
		line(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"usage":{}}`))
	want := []provider.Event{
		provider.Nested{Parent: "toolu_A", Event: provider.Work{Item: "toolu_B", Kind: "command", Title: "Bash sleep 60", Status: api.WorkFailed}},
		provider.Work{Item: "toolu_A", Kind: "tool", Title: "Task Wait", Status: api.WorkFailed},
	}
	var ended []provider.Event
	for _, e := range got[2:] {
		if _, end := e.(provider.TurnEnded); !end {
			ended = append(ended, e)
		}
	}
	if !reflect.DeepEqual(ended, want) {
		t.Errorf("the turn's end read %+v, want %+v", ended, want)
	}
}
