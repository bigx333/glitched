package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

func newChild(t *testing.T, ep api.Endpoint, parent api.ID, access api.Access) (api.ID, error) {
	t.Helper()
	id := api.NewID()
	_, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: api.ThreadCreate{
		Workspace: "/src/app", Title: "child", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Access: access, Parent: parent,
	}})
	return id, err
}

// A child's finished run lands in its parent's log as a report: done, with
// the last paragraph of what it said, and the parent marked reported. The
// child keeps the tokens its turn used.
func TestAChildReportsBackToItsParent(t *testing.T) {
	ad := fake.New([]provider.Event{
		provider.TextDelta{Text: "Looking at the flicker.\n\n"},
		provider.TextDelta{Text: "Switched the terminal to the fit addon; resizing no longer flickers."},
	})
	ad.Tokens = 1500
	n := start(t, dbPath(t), ad)
	parent := newThread(t, n.h)
	c, err := newChild(t, n.h, parent, api.AccessAsk)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, c, dispatch("fix it", api.StartNow))
	v := until(t, n.h, parent, "the report", func(th api.Thread) bool { return !th.Reported.IsZero() })
	if v.Parent != (api.ID{}) {
		t.Errorf("the parent has a parent: %v", v.Parent)
	}
	var got *api.ChildReported
	for _, e := range events(t, n.h, parent) {
		if b, err := e.Decode(); err == nil {
			if r, ok := b.(api.ChildReported); ok {
				got = &r
			}
		}
	}
	want := "Switched the terminal to the fit addon; resizing no longer flickers."
	if got == nil || got.Child != c || got.Kind != api.ReportDone || got.Title != "child" || got.Outcome != want {
		t.Fatalf("the parent's report: %+v; want done, from the child, saying %q", got, want)
	}
	if cv := until(t, n.h, c, "the child's turn", idle); cv.Parent != parent || cv.Tokens != 1500 {
		t.Errorf("the child: parent %v, tokens %d; want %v, 1500", cv.Parent, cv.Tokens, parent)
	}
}

// A run that ends under a settled parent, as one stopping can before its
// stop lands, still reports, but the parent stays settled and starts
// nothing.
func TestALateReportLeavesASettledParent(t *testing.T) {
	n := start(t, dbPath(t), fake.New(fake.Turn(false)))
	parent := newThread(t, n.h)
	c, err := newChild(t, n.h, parent, api.AccessAsk)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, parent, api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	submit(t, n.h, c, dispatch("fix it", api.StartNow))
	v := until(t, n.h, parent, "the report", func(th api.Thread) bool { return !th.Reported.IsZero() })
	if v.Disposition.State != api.Settled || v.Run != api.RunIdle || v.LastTurn != nil {
		t.Errorf("the parent after a late report: %v, run %v, last turn %v; want settled, idle, no turn", v.Disposition.State, v.Run, v.LastTurn)
	}
}

// The engine refuses a child wider than its parent, deeper than MaxDepth,
// or under a parent it doesn't have.
func TestTheEngineKeepsChildrenInBounds(t *testing.T) {
	n := start(t, dbPath(t), fake.New(fake.Turn(false)))
	parent := newThread(t, n.h) // Ask
	var rej *api.RejectedError
	if _, err := newChild(t, n.h, parent, api.AccessFull); !errors.As(err, &rej) {
		t.Errorf("a child wider than its parent: %v, want refused", err)
	}
	if _, err := newChild(t, n.h, api.NewID(), api.AccessAsk); !errors.As(err, &rej) {
		t.Errorf("a child of no thread: %v, want refused", err)
	}
	at := parent
	for depth := 1; depth <= api.MaxDepth; depth++ {
		next, err := newChild(t, n.h, at, api.AccessAsk)
		if err != nil {
			t.Fatalf("a child at depth %d: %v", depth, err)
		}
		at = next
	}
	if _, err := newChild(t, n.h, at, api.AccessAsk); !errors.As(err, &rej) {
		t.Errorf("a child below depth %d: %v, want refused", api.MaxDepth, err)
	}
}

// A child whose parent was deleted reports to no one, and its thread goes
// on: its next turn runs.
func TestAnOrphansReportGoesNowhere(t *testing.T) {
	n := start(t, dbPath(t), fake.New(fake.Turn(false)))
	parent := newThread(t, n.h)
	c, err := newChild(t, n.h, parent, api.AccessAsk)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, parent, api.ThreadDelete{})
	for i := range 2 {
		submit(t, n.h, c, dispatch("go", api.AfterActive))
		until(t, n.h, c, "the child's turn", func(th api.Thread) bool {
			return th.Run == api.RunIdle && th.LastTurn != nil && th.Queued == 0 && count(events(t, n.h, c), "turn.ended") == i+1
		})
	}
}

// A deleted ancestor ends a child's line: under a thread whose grandparent
// was deleted, a child is as deep as the GUI shows it, and is created.
func TestADeletedAncestorEndsTheLine(t *testing.T) {
	n := start(t, dbPath(t), fake.New(fake.Turn(false)))
	line := []api.ID{newThread(t, n.h)}
	for range api.MaxDepth {
		c, err := newChild(t, n.h, line[len(line)-1], api.AccessAsk)
		if err != nil {
			t.Fatal(err)
		}
		line = append(line, c)
	}
	submit(t, n.h, line[1], api.ThreadDelete{})
	if _, err := newChild(t, n.h, line[len(line)-1], api.AccessAsk); err != nil {
		t.Errorf("a child under a line cut by a deleted thread: %v", err)
	}
	if _, err := newChild(t, n.h, line[1], api.AccessAsk); err == nil {
		t.Error("a child of a deleted thread was created")
	}
}

// A follow-up another client queues while a thread is being stopped, just
// before it's settled, never starts: the settled thread is done with.
func TestASettledThreadStartsNothingQueuedBeforeIt(t *testing.T) {
	g := newGated(fake.Turn(true))
	release := make(chan struct{})
	g.setGate(func(_ api.ID, a provider.Action) <-chan struct{} {
		if _, ok := a.(provider.Interrupt); ok {
			return release
		}
		return nil
	})
	n := start(t, dbPath(t), g)
	parent := newThread(t, n.h)
	c, err := newChild(t, n.h, parent, api.AccessAsk)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, c, dispatch("go", api.StartNow))
	until(t, n.h, c, "the approval", waiting)
	submit(t, n.h, c, api.TurnInterrupt{Drop: true})
	submit(t, n.h, c, dispatch("then this", api.AfterActive))
	submit(t, n.h, c, api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	close(release)
	until(t, n.h, c, "the stop", func(api.Thread) bool { return count(events(t, n.h, c), "turn.ended") == 1 })
	time.Sleep(200 * time.Millisecond)
	v := until(t, n.h, c, "the child", func(api.Thread) bool { return true })
	if admitted := count(events(t, n.h, c), "turn.admitted"); admitted != 1 || v.Run != api.RunIdle || v.Disposition.State != api.Settled {
		t.Fatalf("child settled=%v, run %v, admitted turns=%d; want settled, idle, one turn",
			v.Disposition.State == api.Settled, v.Run, admitted)
	}
}

// Deleting a thread whose agent works ends the run and closes the agent:
// nothing of the thread is left running.
func TestDeletingAWorkingThreadClosesItsAgent(t *testing.T) {
	ad := fake.New(fake.Turn(true))
	n := start(t, dbPath(t), ad)
	thread := newThread(t, n.h)
	run := submit(t, n.h, thread, dispatch("go", api.StartNow)).Command
	until(t, n.h, thread, "the approval", waiting)
	submit(t, n.h, thread, api.ThreadDelete{})
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, err := ad.Reconcile(ctx, provider.RuntimeIdentity{Thread: thread, Run: run})
		if _, lost := rec.(provider.Lost); lost && err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the deleted thread's agent still runs: %T %v", rec, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// unreachable wraps the fake so its agents' Close fails while fail is
// set, as a CLI the host can't reach for a moment; tried says one failed.
type unreachable struct {
	*fake.Adapter
	fail  atomic.Bool
	tried chan struct{}
}

type unreachableAgent struct {
	provider.Agent
	u *unreachable
}

func (u *unreachable) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := u.Adapter.Open(ctx, spec)
	return &unreachableAgent{ag, u}, err
}

func (u *unreachable) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	rec, err := u.Adapter.Reconcile(ctx, id)
	if r, ok := rec.(provider.Restored); ok {
		rec = provider.Restored{Agent: &unreachableAgent{r.Agent, u}}
	}
	return rec, err
}

func (a *unreachableAgent) Close(ctx context.Context) error {
	if a.u.fail.Load() {
		select {
		case a.u.tried <- struct{}{}:
		default:
		}
		return errors.New("unreachable")
	}
	return a.Agent.Close(ctx)
}

// gone waits for the run's agent to be closed or lost.
func gone(t *testing.T, ad provider.Adapter, thread, run api.ID) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, err := ad.Reconcile(ctx, provider.RuntimeIdentity{Thread: thread, Run: run})
		if _, lost := rec.(provider.Lost); lost && err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the agent still runs after the restart: %T %v", rec, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// A host that crashes or shuts down after a delete or a settle commits,
// before it stopped the agent, leaves the stop to the next host: nothing
// of a deleted or settled thread runs on after the restart.
func TestADeletedOrSettledThreadsAgentStopsAcrossARestart(t *testing.T) {
	ends := map[string]func(node){"crash": node.kill, "shutdown": node.stop}
	for how, end := range ends {
		t.Run("delete, then a "+how, func(t *testing.T) {
			ad := &unreachable{Adapter: fake.New(fake.Turn(true)), tried: make(chan struct{}, 1)}
			path := dbPath(t)
			n := start(t, path, ad)
			thread := newThread(t, n.h)
			run := submit(t, n.h, thread, dispatch("go", api.StartNow)).Command
			until(t, n.h, thread, "the approval", waiting)
			ad.fail.Store(true)
			submit(t, n.h, thread, api.ThreadDelete{})
			<-ad.tried
			end(n)
			ad.fail.Store(false)
			start(t, path, ad)
			gone(t, ad, thread, run)
		})
		t.Run("settle, then a "+how, func(t *testing.T) {
			g := newGated(fake.Turn(true))
			path := dbPath(t)
			n := start(t, path, g)
			thread := newThread(t, n.h)
			run := submit(t, n.h, thread, dispatch("go", api.StartNow)).Command
			until(t, n.h, thread, "the approval", waiting)
			entered, never := make(chan struct{}, 1), make(chan struct{})
			g.setGate(func(_ api.ID, a provider.Action) <-chan struct{} {
				if _, ok := a.(provider.Interrupt); ok {
					entered <- struct{}{}
					return never
				}
				return nil
			})
			submit(t, n.h, thread, api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
			<-entered
			end(n)
			g.setGate(nil)
			n = start(t, path, g)
			v := until(t, n.h, thread, "the settled thread idle", idle)
			if v.Disposition.State != api.Settled {
				t.Errorf("the thread is %v after the restart, want settled", v.Disposition.State)
			}
			gone(t, g, thread, run)
		})
	}
}
