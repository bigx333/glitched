package engine_test

import (
	"errors"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// A thread's next turn starts at the model and access a configure set.
func TestAConfiguredThreadsNextTurnRunsAtIt(t *testing.T) {
	f := newThread(t)
	sonnet := api.ModelRef{Model: "Claude Sonnet 5.5", Effort: "low", Fast: true}
	f.must(api.ThreadConfigure{Model: sonnet, Access: api.AccessFull})
	if f.th.Model != sonnet || f.th.Access != api.AccessFull {
		t.Fatalf("after the configure: %+v at %v", f.th.Model, f.th.Access)
	}
	_, effects, err := f.submit(api.MessageDispatch{Text: "go", Mode: api.StartNow})
	if err != nil {
		t.Fatal(err)
	}
	var start *engine.StartTurn
	for _, e := range effects {
		if s, ok := e.(engine.StartTurn); ok {
			start = &s
		}
	}
	if start == nil || start.Model != sonnet || start.Access != api.AccessFull {
		t.Fatalf("the turn starts at %+v, not the configured model and access", start)
	}
}

// A configure that changes nothing records nothing, and one the engine
// can't run is refused.
func TestAConfigureIsChecked(t *testing.T) {
	f := newThread(t)
	same := api.ThreadConfigure{Model: api.ModelRef{Model: "m"}, Access: f.th.Access}
	f.must(same)
	rev := f.th.Revision
	if f.must(same); f.th.Revision != rev {
		t.Fatalf("the same configure again moved the revision from %d to %d", rev, f.th.Revision)
	}
	for name, c := range map[string]api.ThreadConfigure{
		"another provider": {Model: api.ModelRef{Provider: "codex", Model: "m"}},
		"unknown access":   {Model: api.ModelRef{Model: "m"}, Access: api.AccessFull + 1},
	} {
		var re *api.RejectedError
		if _, _, err := f.submit(c); !errors.As(err, &re) {
			t.Errorf("%s: %v, not refused", name, err)
		}
	}
}
