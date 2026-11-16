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
		"no model":         {Access: f.th.Access},
		"unknown access":   {Model: api.ModelRef{Model: "m"}, Access: api.AccessFull + 1},
	} {
		var re *api.RejectedError
		if _, _, err := f.submit(c); !errors.As(err, &re) {
			t.Errorf("%s: %v, not refused", name, err)
		}
	}
}

// A turn already running keeps the model and access it was admitted at,
// which its view says, and starts no child wider than itself or than its
// thread as configured since.
func TestARunningTurnKeepsItsModelAndAccess(t *testing.T) {
	opus := api.ModelRef{Model: "Claude Opus 5.5", Effort: "high"}
	sonnet := api.ModelRef{Model: "Claude Sonnet 5.5", Effort: "low"}
	for _, tc := range []struct {
		name            string
		admitted, since api.Access
	}{{"widened since", api.AccessAutoEdits, api.AccessFull}, {"narrowed since", api.AccessFull, api.AccessAutoEdits}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newThread(t)
			f.must(api.ThreadConfigure{Model: opus, Access: tc.admitted})
			run := f.must(dispatch("go", api.StartNow))
			f.observe(engine.Started{Run: run})
			f.must(api.ThreadConfigure{Model: sonnet, Access: tc.since})

			v := engine.View(f.th)
			if v.Model != sonnet || v.Access != tc.since {
				t.Fatalf("the thread is at %+v, %v after the configure", v.Model, v.Access)
			}
			if want := (api.Turn{Run: run, Model: opus, Access: tc.admitted}); v.Turn == nil || *v.Turn != want {
				t.Errorf("the view's turn is %+v, want %+v as admitted", v.Turn, want)
			}
			child := func(a api.Access) error {
				return engine.CheckChild([]engine.Thread{f.th}, engine.Kids{}, engine.Limits{}, api.ThreadCreate{Parent: f.th.ID, Run: run, Access: a})
			}
			if err := child(api.AccessAutoEdits); err != nil {
				t.Errorf("a child as wide as the narrower of the turn and the thread: %v", err)
			}
			var rej *api.RejectedError
			if err := child(api.AccessFull); !errors.As(err, &rej) {
				t.Errorf("a child wider than the narrower of the turn and the thread: %v, want refused", err)
			}

			f.observe(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
			if v := engine.View(f.th); v.Turn != nil {
				t.Errorf("the view of the idle thread has a turn: %+v", v.Turn)
			}
			next := f.must(dispatch("again", api.StartNow))
			if v, want := engine.View(f.th), (api.Turn{Run: next, Model: sonnet, Access: tc.since}); v.Turn == nil || *v.Turn != want {
				t.Errorf("the next turn is %+v, want %+v as configured", v.Turn, want)
			}
		})
	}
}
