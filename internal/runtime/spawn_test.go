package runtime_test

import (
	"errors"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/store"
)

// children lists parent's children as the engine reads them.
func children(t *testing.T, ep api.Endpoint, parent api.ID) []api.Thread {
	t.Helper()
	snap, err := ep.Read(ctx, api.Query{})
	if err != nil {
		t.Fatal(err)
	}
	var kids []api.Thread
	for _, th := range snap.Threads {
		if th.Parent == parent {
			kids = append(kids, th)
		}
	}
	return kids
}

// A turn that asks for a child gets one, with its task as the child's
// first message and its turn started in the same commit. The call's
// second try, as a client resends one whose reply it lost, finds the same
// child and starts nothing more.
func TestASpawnStartsOneChild(t *testing.T) {
	ad := fake.New(fake.Turn(false))
	n := start(t, dbPath(t), ad)
	parent := newThread(t, n.h)
	kid, err := newChild(t, n.h, ad, parent, api.AccessAsk,
		fake.Child{Title: "Write the tests", Task: "Add table tests for parseRange in range.go", Tries: 2})
	if err != nil {
		t.Fatal(err)
	}
	v := until(t, n.h, kid, "the child's turn", func(th api.Thread) bool { return th.Run == api.RunIdle && th.LastTurn != nil })
	if kids := children(t, n.h, parent); len(kids) != 1 || kids[0].ID != kid {
		t.Fatalf("the parent's children: %+v; want the one child", kids)
	}
	if v.Title != "Write the tests" || v.LastTurn.Outcome != api.OutcomeCompleted {
		t.Errorf("the child: %q, last turn %+v; want Write the tests, completed", v.Title, v.LastTurn)
	}
	evs := events(t, n.h, kid)
	if c, a := count(evs, "thread.created"), count(evs, "turn.admitted"); c != 1 || a != 1 {
		t.Fatalf("the child: created %d, admitted %d; want 1 and 1", c, a)
	}
	if evs[0].Cursor.Seq+1 != evs[1].Cursor.Seq || evs[1].Type != "message.queued" || evs[2].Type != "turn.admitted" {
		t.Errorf("the child's log opens %s, %s, %s; want its create, task and turn in one commit", evs[0].Type, evs[1].Type, evs[2].Type)
	}
	if b, _ := evs[1].Decode(); b.(api.MessageQueued).Text != "Add table tests for parseRange in range.go" {
		t.Errorf("the child's first message: %+v", b)
	}
	until(t, n.h, parent, "the child's report and the turn it woke", func(th api.Thread) bool {
		return !th.Reported.IsZero() && th.Run == api.RunIdle && count(events(t, n.h, parent), "turn.admitted") == 2
	})
	if got := ad.Starts(); got != 3 {
		t.Errorf("adapter starts %d; want 3: the parent's, the child's and the turn its report woke", got)
	}
	assertReplay(t, n.st)
}

// The engine refuses a child past the limits it was given: at work at
// once and in all under one thread, and depth below a thread of the
// user's.
func TestASpawnPastALimitIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		script []provider.Event
		lim    engine.Limits
		want   string
	}{
		{"at work at once", fake.Turn(true), engine.Limits{Working: 1}, "1 child threads of this thread are at work"},
		{"in all", fake.Turn(false), engine.Limits{All: 1}, "has 1 child threads"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ad := fake.New(tc.script)
			n := start(t, dbPath(t), ad)
			n.st.SetLimits(tc.lim)
			parent := newThread(t, n.h)
			first, err := newChild(t, n.h, ad, parent, api.AccessAsk, fake.Child{Title: "first", Task: "go"})
			if err != nil {
				t.Fatal(err)
			}
			until(t, n.h, first, "the first child's turn", func(th api.Thread) bool { return th.Run != api.RunStarting })
			var rej *api.RejectedError
			if _, err := newChild(t, n.h, ad, parent, api.AccessAsk, fake.Child{Title: "second", Task: "go"}); !errors.As(err, &rej) || !strings.Contains(rej.Reason, tc.want) {
				t.Fatalf("the second child: %v; want refused with %q", err, tc.want)
			}
			if kids := children(t, n.h, parent); len(kids) != 1 {
				t.Errorf("%d children; want the first only", len(kids))
			}
		})
	}

	t.Run("depth", func(t *testing.T) {
		ad := fake.New(fake.Turn(false))
		n := start(t, dbPath(t), ad)
		n.st.SetLimits(engine.Limits{Depth: 1})
		parent := newThread(t, n.h)
		kid, err := newChild(t, n.h, ad, parent, api.AccessAsk)
		if err != nil {
			t.Fatal(err)
		}
		var rej *api.RejectedError
		if _, err := newChild(t, n.h, ad, kid, api.AccessAsk); !errors.As(err, &rej) || !strings.Contains(rej.Reason, "at most 1 below") {
			t.Fatalf("a grandchild under depth 1: %v; want refused", err)
		}
	})
}

// Only the parent's live run starts a child, and not once it is
// stopping: a create that names no run, another run, or a run being
// stopped is refused.
func TestOnlyTheParentsLiveRunStartsAChild(t *testing.T) {
	g := newGated(fake.Turn(true))
	release := make(chan struct{})
	defer close(release)
	g.setGate(func(_ api.ID, a provider.Action) <-chan struct{} {
		if _, ok := a.(provider.Interrupt); ok {
			return release
		}
		return nil
	})
	n := start(t, dbPath(t), g)
	parent := newThread(t, n.h)
	run := submit(t, n.h, parent, dispatch("go", api.StartNow)).Command
	until(t, n.h, parent, "the approval", waiting)
	var rej *api.RejectedError
	for what, r := range map[string]api.ID{"no run": {}, "another run": api.NewID()} {
		if err := createUnder(n.h, parent, r); !errors.As(err, &rej) {
			t.Errorf("a create naming %s: %v; want refused", what, err)
		}
	}
	if err := createUnder(n.h, parent, run); err != nil {
		t.Fatalf("a create from the live run: %v", err)
	}
	submit(t, n.h, parent, api.TurnInterrupt{})
	until(t, n.h, parent, "the stop", func(th api.Thread) bool { return th.Run == api.RunStopping })
	if err := createUnder(n.h, parent, run); !errors.As(err, &rej) || !strings.Contains(rej.Reason, "stopping") {
		t.Errorf("a create from a stopping run: %v; want refused", err)
	}
	if kids := children(t, n.h, parent); len(kids) != 1 {
		t.Errorf("%d children; want the one the live run asked for", len(kids))
	}
}

// A spawn's commit is the child, its task and its turn's start, so a
// crash before the turn started leaves it to the next host, and one while
// the start was in flight leaves the run to Reconcile. Either way there is
// one child, started at most once, and the call's retry finds it.
func TestASpawnSurvivesACrash(t *testing.T) {
	spawn := func(t *testing.T, ep api.Endpoint, parent, run api.ID) api.Command {
		t.Helper()
		c := api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{
			Workspace: "/src/app", Title: "child", Model: api.ModelRef{Provider: "fake", Model: "fake"},
			Parent: parent, Run: run, Text: "write the tests",
		}}
		if _, err := ep.Submit(ctx, c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	retry := func(t *testing.T, ep api.Endpoint, c api.Command, parent api.ID) {
		t.Helper()
		if _, err := ep.Submit(ctx, c); err != nil {
			t.Fatalf("the call's retry: %v", err)
		}
		if kids := children(t, ep, parent); len(kids) != 1 || kids[0].ID != c.Target {
			t.Fatalf("after the retry: %+v; want the one child", kids)
		}
	}

	t.Run("before the child's turn started", func(t *testing.T) {
		path := dbPath(t)
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		parent := newThread(t, st2endpoint{st})
		run := submit(t, st2endpoint{st}, parent, dispatch("go", api.StartNow)).Command
		c := spawn(t, st2endpoint{st}, parent, run)
		st.Close()

		ad := fake.New(fake.Turn(false))
		n := start(t, path, ad)
		until(t, n.h, c.Target, "the child's turn", func(th api.Thread) bool { return th.Run == api.RunIdle && th.LastTurn != nil })
		retry(t, n.h, c, parent)
		if a := count(events(t, n.h, c.Target), "turn.started"); a != 1 {
			t.Errorf("the child's turn started %d times; want once", a)
		}
		until(t, n.h, parent, "the parent's turn and the child's report", func(th api.Thread) bool {
			return th.Run == api.RunIdle && !th.Reported.IsZero()
		})
		assertReplay(t, n.st)
	})

	t.Run("while the child's start was in flight", func(t *testing.T) {
		path, g := dbPath(t), newGated(fake.Turn(true))
		n := start(t, path, g)
		parent := newThread(t, n.h)
		run := submit(t, n.h, parent, dispatch("go", api.StartNow)).Command
		until(t, n.h, parent, "the approval", waiting)
		entered, never := make(chan struct{}, 1), make(chan struct{})
		g.setGate(func(thread api.ID, a provider.Action) <-chan struct{} {
			if _, ok := a.(provider.Start); ok && thread != parent {
				entered <- struct{}{}
				return never
			}
			return nil
		})
		c := spawn(t, n.h, parent, run)
		<-entered
		n.kill()
		g.setGate(nil)

		n = start(t, path, g)
		v := until(t, n.h, c.Target, "the child's reconciled run", func(th api.Thread) bool { return th.Run == api.RunUncertain })
		if g.count(provider.Start{}) != 1 {
			t.Errorf("%d starts reached the agents; want the parent's only, as the child's never arrived", g.count(provider.Start{}))
		}
		until(t, n.h, parent, "the parent, restored", waiting)
		retry(t, n.h, c, parent)
		if v.Title != "child" {
			t.Errorf("the child: %+v", v)
		}
		assertReplay(t, n.st)
	})
}

// A child left uncertain, as every live one is after an engine restart
// under Claude, has no agent at work, so it takes no place under the
// limit at once: the resumed parent starts another.
func TestAnUncertainChildIsntAtWork(t *testing.T) {
	path := dbPath(t)
	ad := fake.New(fake.Turn(true))
	n := start(t, path, ad)
	n.st.SetLimits(engine.Limits{Working: 1})
	parent := newThread(t, n.h)
	kid, err := newChild(t, n.h, ad, parent, api.AccessAsk, fake.Child{Title: "first", Task: "go"})
	if err != nil {
		t.Fatal(err)
	}
	until(t, n.h, kid, "the child at work", waiting)
	n.kill()

	ad = fake.New(fake.Turn(true))
	n = start(t, path, ad)
	n.st.SetLimits(engine.Limits{Working: 1})
	until(t, n.h, kid, "the child, uncertain", func(th api.Thread) bool { return th.Run == api.RunUncertain })
	second, err := newChild(t, n.h, ad, parent, api.AccessAsk, fake.Child{Title: "second", Task: "go"})
	if err != nil {
		t.Fatalf("a parent whose only child is uncertain: %v; want a second child", err)
	}
	until(t, n.h, second, "the second child at work", waiting)
	var rej *api.RejectedError
	if _, err := newChild(t, n.h, ad, parent, api.AccessAsk, fake.Child{Title: "third", Task: "go"}); !errors.As(err, &rej) || !strings.Contains(rej.Reason, "1 child threads of this thread are at work") {
		t.Fatalf("a third child while the second works: %v; want refused", err)
	}
}

// A deleted child no longer counts against the limit in all, so a thread
// that lives long can go on starting children as the user deletes them.
func TestADeletedChildFreesItsPlaceInAll(t *testing.T) {
	ad := fake.New(fake.Turn(false))
	n := start(t, dbPath(t), ad)
	n.st.SetLimits(engine.Limits{All: 1})
	parent := newThread(t, n.h)
	first, err := newChild(t, n.h, ad, parent, api.AccessAsk, fake.Child{Title: "first", Task: "go"})
	if err != nil {
		t.Fatal(err)
	}
	until(t, n.h, first, "the first child's turn", func(th api.Thread) bool { return th.Run == api.RunIdle && th.LastTurn != nil })
	submit(t, n.h, first, api.ThreadDelete{})
	if _, err := newChild(t, n.h, ad, parent, api.AccessAsk, fake.Child{Title: "second", Task: "go"}); err != nil {
		t.Fatalf("a child after the only one was deleted: %v; want it started", err)
	}
	var rej *api.RejectedError
	if _, err := newChild(t, n.h, ad, parent, api.AccessAsk, fake.Child{Title: "third", Task: "go"}); !errors.As(err, &rej) || !strings.Contains(rej.Reason, "has 1 child threads") {
		t.Fatalf("a third child beside the second: %v; want refused", err)
	}
}

// Once a parent's settle or delete commits, its run starts no child,
// even with its stop still in flight: a cascade that follows the commit
// meets every child the parent has.
func TestASettledOrDeletedParentStartsNoChild(t *testing.T) {
	for _, tc := range []struct {
		name string
		body api.Intent
		want string
	}{
		{"settled", api.InboxSet{Disposition: api.Disposition{State: api.Settled}}, "settled"},
		{"deleted", api.ThreadDelete{}, "gone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGated(fake.Turn(true))
			release := make(chan struct{})
			defer close(release)
			g.setGate(func(_ api.ID, a provider.Action) <-chan struct{} {
				if _, ok := a.(provider.Interrupt); ok {
					return release
				}
				return nil
			})
			n := start(t, dbPath(t), g)
			parent := newThread(t, n.h)
			run := submit(t, n.h, parent, dispatch("go", api.StartNow)).Command
			until(t, n.h, parent, "the approval", waiting)
			submit(t, n.h, parent, tc.body)
			var rej *api.RejectedError
			if err := createUnder(n.h, parent, run); !errors.As(err, &rej) || !strings.Contains(rej.Reason, tc.want) {
				t.Fatalf("a create from the run of a %s parent: %v; want refused with %q", tc.name, err, tc.want)
			}
		})
	}
}
