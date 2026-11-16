package engine_test

import (
	"errors"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// whole is the configure that gives all a configure can: m's name, effort
// and fast mode, and a.
func whole(m api.ModelRef, a api.Access) api.ThreadConfigure {
	return api.ThreadConfigure{Model: new(m.Model), Effort: new(m.Effort), Fast: new(m.Fast), Access: new(a)}
}

// A thread's next turn starts at the model and access a configure set.
func TestAConfiguredThreadsNextTurnRunsAtIt(t *testing.T) {
	f := newThread(t)
	sonnet := api.ModelRef{Model: "Claude Sonnet 5.5", Effort: "low", Fast: true}
	f.must(whole(sonnet, api.AccessFull))
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

// A configure sets what it gives and leaves the rest as the thread has it
// when the engine decides, whatever its sender last saw: one of the
// effort alone, decided after another narrowed the thread, leaves it
// narrowed. The event it records has the thread's whole model and access,
// and its provider as it was.
func TestAConfigureLeavesWhatItDoesNotGive(t *testing.T) {
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high", Fast: true}
	for name, tc := range map[string]struct {
		c      api.ThreadConfigure
		model  api.ModelRef
		access api.Access
	}{
		"the effort":    {api.ThreadConfigure{Effort: new("low")}, api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "low", Fast: true}, api.AccessAutoEdits},
		"the model":     {api.ThreadConfigure{Model: new("Claude Sonnet 5.5")}, api.ModelRef{Provider: "claude", Model: "Claude Sonnet 5.5", Effort: "high", Fast: true}, api.AccessAutoEdits},
		"fast mode off": {api.ThreadConfigure{Fast: new(false)}, api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}, api.AccessAutoEdits},
		"the access":    {api.ThreadConfigure{Access: new(api.AccessAsk)}, opus, api.AccessAsk},
		"no effort":     {api.ThreadConfigure{Effort: new("")}, api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Fast: true}, api.AccessAutoEdits},
	} {
		t.Run(name, func(t *testing.T) {
			evs, _, err := engine.Decide(engine.Thread{}, api.Command{ID: api.NewID(), Target: api.NewID(),
				Body: api.ThreadCreate{Title: "t", Model: opus, Access: api.AccessFull}}, now)
			if err != nil {
				t.Fatal(err)
			}
			f := &thread{t: t}
			f.fold(evs)
			// Another client's narrowing, which the sender of c hasn't seen.
			f.must(api.ThreadConfigure{Access: new(api.AccessAutoEdits)})
			id, _, err := f.submit(tc.c)
			if err != nil {
				t.Fatal(err)
			}
			if f.th.Model != tc.model || f.th.Access != tc.access {
				t.Errorf("the thread is at %+v, %v; want %+v, %v", f.th.Model, f.th.Access, tc.model, tc.access)
			}
			evs, _, err = engine.Decide(f.th, api.Command{ID: id, Target: f.th.ID, Body: tc.c}, now)
			if err != nil || len(evs) != 0 {
				t.Errorf("decided again on the thread it made, it records %d events (%v), want none", len(evs), err)
			}
		})
	}
}

// A configure that changes nothing records nothing, whether it gives
// nothing or what the thread has, and one the engine can't run is
// refused, the thread left as it was.
func TestAConfigureIsChecked(t *testing.T) {
	f := newThread(t)
	f.must(api.ThreadConfigure{Model: new("m")})
	rev := f.th.Revision
	for name, same := range map[string]api.ThreadConfigure{
		"nothing given":       {},
		"what the thread has": whole(f.th.Model, f.th.Access),
	} {
		if f.must(same); f.th.Revision != rev {
			t.Fatalf("%s: the configure moved the revision from %d to %d", name, rev, f.th.Revision)
		}
	}
	was := f.th
	for name, c := range map[string]api.ThreadConfigure{
		"no model":       {Model: new("")},
		"unknown access": {Access: new(api.AccessFull + 1)},
		"one of both":    {Effort: new("low"), Access: new(api.AccessFull + 1)},
	} {
		var re *api.RejectedError
		if _, _, err := f.submit(c); !errors.As(err, &re) {
			t.Errorf("%s: %v, not refused", name, err)
		}
		if f.th.Model != was.Model || f.th.Access != was.Access || f.th.Revision != rev {
			t.Errorf("%s: refused, it left the thread at %+v, %v", name, f.th.Model, f.th.Access)
		}
	}
}

// A configure takes the threads under its own only for the access it
// gives: each is narrowed to it, none widened, and nothing else of the
// thread goes down to them. A child's own configure can't leave it wider
// than its parent, whether it gives the access or not.
func TestAConfigureGivesTheThreadsBelowOnlyItsAccess(t *testing.T) {
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	child := engine.Thread{ID: api.NewID(), Revision: 1, Model: opus, Access: api.AccessAutoEdits}
	on := func(c api.ThreadConfigure) (api.ModelRef, api.Access) {
		t.Helper()
		below, ok := engine.Below(c, child).(api.ThreadConfigure)
		if !ok {
			t.Fatalf("below %+v the child is given %T", c, engine.Below(c, child))
		}
		return below.Onto(child.Model, child.Access)
	}
	for name, tc := range map[string]struct {
		c     api.ThreadConfigure
		takes bool
		want  api.Access
	}{
		"narrower":           {api.ThreadConfigure{Access: new(api.AccessAsk)}, true, api.AccessAsk},
		"wider":              {api.ThreadConfigure{Access: new(api.AccessFull)}, true, api.AccessAutoEdits},
		"narrower, and more": {api.ThreadConfigure{Model: new("Claude Sonnet 5.5"), Effort: new("low"), Fast: new(true), Access: new(api.AccessAsk)}, true, api.AccessAsk},
		"no access":          {api.ThreadConfigure{Model: new("Claude Sonnet 5.5"), Effort: new("low"), Fast: new(true)}, false, api.AccessAutoEdits},
	} {
		if got := engine.TakesTree(tc.c); got != tc.takes {
			t.Errorf("%s: takes the tree: %v, want %v", name, got, tc.takes)
		}
		if m, a := on(tc.c); m != opus || a != tc.want {
			t.Errorf("%s: the child under it is left at %+v, %v; want its own model at %v", name, m, a, tc.want)
		}
	}

	parent := engine.Thread{ID: api.NewID(), Revision: 1, Model: opus, Access: api.AccessAutoEdits}
	var re *api.RejectedError
	for name, tc := range map[string]struct {
		child   api.Access
		c       api.ThreadConfigure
		refused bool
	}{
		"as wide as its parent":             {api.AccessAsk, api.ThreadConfigure{Access: new(api.AccessAutoEdits)}, false},
		"wider than its parent":             {api.AccessAsk, api.ThreadConfigure{Access: new(api.AccessFull)}, true},
		"the effort, within its parent":     {api.AccessAutoEdits, api.ThreadConfigure{Effort: new("low")}, false},
		"the effort, wider than its parent": {api.AccessFull, api.ThreadConfigure{Effort: new("low")}, true},
		"narrowed to within its parent's":   {api.AccessFull, api.ThreadConfigure{Access: new(api.AccessAsk)}, false},
	} {
		kid := engine.Thread{ID: api.NewID(), Revision: 1, Model: opus, Access: tc.child, Parent: parent.ID}
		if err := engine.CheckConfigure(parent, kid, tc.c); errors.As(err, &re) != tc.refused || err != nil && !tc.refused {
			t.Errorf("%s: %v; want refused: %v", name, err, tc.refused)
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
			f.must(whole(opus, tc.admitted))
			run := f.must(dispatch("go", api.StartNow))
			f.observe(engine.Started{Run: run})
			f.must(whole(sonnet, tc.since))

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
