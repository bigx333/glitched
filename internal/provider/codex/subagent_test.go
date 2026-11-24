package codex

import (
	"reflect"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// An agent Codex spawns works on a thread of its own. Its text, its steps
// and its end, with what its thread spent, nest under the item that
// spawned it; none of it is the run's text or tokens, and a thread the
// run never spawned is nobody's.
func TestASpawnedAgentNestsUnderItsSpawn(t *testing.T) {
	a, _ := fake(t, "subagent")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "spawn one"})
	events := turn(t, g)
	closeRun(t, g)
	if got := text(events); got != "the agent is done" {
		t.Errorf("the run said %q, want only its own text", got)
	}
	nest := func(e provider.Event) provider.Event { return provider.Nested{Parent: "call-spawn", Event: e} }
	exit := 0
	want := []provider.Event{
		provider.Work{Item: "call-spawn", Kind: "tool", Title: "Agent alpha", Status: api.WorkCompleted},
		provider.Work{Item: "call-spawn", Kind: "tool", Title: "Agent alpha", Status: api.WorkCompleted},
		nest(provider.TextDelta{Text: "Looking."}),
		nest(provider.Work{Item: "cmd-alpha", Kind: "command", Title: "go test ./...", Status: api.WorkStarted}),
		nest(provider.Work{Item: "cmd-alpha", Kind: "command", Title: "go test ./...", Status: api.WorkCompleted, Exit: &exit, Output: "ok\n"}),
		nest(provider.TurnEnded{Outcome: api.OutcomeCompleted, Tokens: 5000}),
	}
	var got []provider.Event
	for _, e := range events {
		switch e := e.(type) {
		case provider.Nested:
			got = append(got, e)
		case provider.Work:
			if e.Item == "call-spawn" {
				got = append(got, e)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read\n%+v\nwant\n%+v", got, want)
	}
}
