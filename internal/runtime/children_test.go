package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// spawner is a fake whose turns can start children.
type spawner interface {
	Spawns(ep api.Endpoint, text string, kids ...fake.Child)
	Spawned(child api.ID) (bool, error)
}

var spawnTexts atomic.Int64

// newChild has a turn of parent's start a child at access, as an agent's
// delegate_task call does, and returns it once that turn ended, with the
// engine's refusal if it refused.
func newChild(t *testing.T, ep api.Endpoint, ad spawner, parent api.ID, access api.Access, kids ...fake.Child) (api.ID, error) {
	t.Helper()
	text := fmt.Sprint("start child ", spawnTexts.Add(1))
	if len(kids) == 0 {
		kids = []fake.Child{{Title: "child", Access: access, Wider: true}}
	}
	ad.Spawns(ep, text, kids...)
	run := submit(t, ep, parent, dispatch(text, api.AfterActive)).Command
	until(t, ep, parent, "the spawning turn", func(th api.Thread) bool {
		return th.Run == api.RunIdle && th.Queued == 0 && ended(t, ep, parent, run)
	})
	child := fake.ChildOf(run, 0)
	answered, err := ad.Spawned(child)
	if !answered {
		t.Fatalf("the spawning turn ended before the engine answered its create")
	}
	return child, err
}

// ended is whether run's turn on thread ended.
func ended(t *testing.T, ep api.Endpoint, thread, run api.ID) bool {
	t.Helper()
	for _, e := range events(t, ep, thread) {
		if b, err := e.Decode(); err == nil {
			if te, ok := b.(api.TurnEnded); ok && te.Run == run {
				return true
			}
		}
	}
	return false
}

// createUnder submits a child's create as a client that names run would.
func createUnder(ep api.Endpoint, parent, run api.ID) error {
	_, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{
		Workspace: "/src/app", Title: "child", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Parent: parent, Run: run,
	}})
	return err
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
	c, err := newChild(t, n.h, ad, parent, api.AccessAsk)
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
	ad := fake.New(fake.Turn(false))
	n := start(t, dbPath(t), ad)
	parent := newThread(t, n.h)
	c, err := newChild(t, n.h, ad, parent, api.AccessAsk)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, parent, api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	submit(t, n.h, c, dispatch("fix it", api.StartNow))
	v := until(t, n.h, parent, "the report", func(th api.Thread) bool { return !th.Reported.IsZero() })
	if turns := count(events(t, n.h, parent), "turn.admitted"); v.Disposition.State != api.Settled || v.Run != api.RunIdle || turns != 1 {
		t.Errorf("the parent after a late report: %v, run %v, %d turns; want settled, idle, only the turn that started the child", v.Disposition.State, v.Run, turns)
	}
}

// The engine refuses a child wider than its parent, deeper than MaxDepth,
// or under a parent it doesn't have.
func TestTheEngineKeepsChildrenInBounds(t *testing.T) {
	ad := fake.New(fake.Turn(false))
	n := start(t, dbPath(t), ad)
	parent := newThread(t, n.h) // Ask
	var rej *api.RejectedError
	if _, err := newChild(t, n.h, ad, parent, api.AccessFull); !errors.As(err, &rej) {
		t.Errorf("a child wider than its parent: %v, want refused", err)
	}
	if err := createUnder(n.h, api.NewID(), api.NewID()); !errors.As(err, &rej) {
		t.Errorf("a child of no thread: %v, want refused", err)
	}
	at := parent
	for depth := 1; depth <= api.MaxDepth; depth++ {
		next, err := newChild(t, n.h, ad, at, api.AccessAsk)
		if err != nil {
			t.Fatalf("a child at depth %d: %v", depth, err)
		}
		at = next
	}
	if _, err := newChild(t, n.h, ad, at, api.AccessAsk); !errors.As(err, &rej) {
		t.Errorf("a child below depth %d: %v, want refused", api.MaxDepth, err)
	}
}

// Deleting a parent deletes its child with it: the child takes no new
// message, and the parent's line takes no new child.
func TestDeletingAParentDeletesItsChild(t *testing.T) {
	ad := fake.New(fake.Turn(false))
	n := start(t, dbPath(t), ad)
	parent := newThread(t, n.h)
	c, err := newChild(t, n.h, ad, parent, api.AccessAsk)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, parent, api.ThreadDelete{})
	if _, err := n.h.Read(ctx, api.Query{Thread: c}); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("the child after its parent's delete: %v, want not found", err)
	}
	if _, err := n.h.Submit(ctx, api.Command{ID: api.NewID(), Target: c, Body: dispatch("go", api.AfterActive)}); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("a message to the deleted child: %v, want not found", err)
	}
	if err := createUnder(n.h, c, api.NewID()); err == nil {
		t.Error("a child of the deleted child was created")
	}
}

// Deleting a thread in a line deletes the line below it, and leaves the
// line above it to start children.
func TestDeletingAThreadInALineDeletesWhatsBelow(t *testing.T) {
	ad := fake.New(fake.Turn(false))
	n := start(t, dbPath(t), ad)
	line := []api.ID{newThread(t, n.h)}
	for range api.MaxDepth {
		c, err := newChild(t, n.h, ad, line[len(line)-1], api.AccessAsk)
		if err != nil {
			t.Fatal(err)
		}
		line = append(line, c)
	}
	submit(t, n.h, line[1], api.ThreadDelete{})
	for i, id := range line[1:] {
		if _, err := n.h.Read(ctx, api.Query{Thread: id}); !errors.Is(err, api.ErrNotFound) {
			t.Errorf("thread %d of the line after the delete: %v, want not found", i+1, err)
		}
	}
	if _, err := newChild(t, n.h, ad, line[0], api.AccessAsk); err != nil {
		t.Errorf("a child of the thread above the delete: %v", err)
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
	c, err := newChild(t, n.h, g, parent, api.AccessAsk)
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

// runOf is the run thread's log last admitted.
func runOf(t *testing.T, ep api.Endpoint, thread api.ID) api.ID {
	t.Helper()
	var run api.ID
	for _, e := range events(t, ep, thread) {
		if b, err := e.Decode(); err == nil {
			if a, ok := b.(api.TurnAdmitted); ok {
				run = a.Run
			}
		}
	}
	return run
}

// Deleting a thread deletes its child, and a host that crashes or shuts
// down before it closed either agent leaves both closes to the next one:
// its reconcile finds each agent of the deleted tree and closes it.
func TestADeletedTreesAgentsCloseAcrossARestart(t *testing.T) {
	for how, end := range map[string]func(node){"crash": node.kill, "shutdown": node.stop} {
		t.Run(how, func(t *testing.T) {
			ad := &unreachable{Adapter: fake.New(fake.Turn(true)), tried: make(chan struct{}, 1)}
			path := dbPath(t)
			n := start(t, path, ad)
			parent := newThread(t, n.h)
			child, err := newChild(t, n.h, ad, parent, api.AccessAsk, fake.Child{Title: "child", Task: "work"})
			if err != nil {
				t.Fatal(err)
			}
			until(t, n.h, child, "the child's approval", waiting)
			run := submit(t, n.h, parent, dispatch("go on", api.StartNow)).Command
			until(t, n.h, parent, "the parent's approval", waiting)
			childRun := runOf(t, n.h, child)
			ad.fail.Store(true)
			submit(t, n.h, parent, api.ThreadDelete{})
			<-ad.tried
			end(n)
			ad.fail.Store(false)
			n = start(t, path, ad)
			if _, err := n.h.Read(ctx, api.Query{Thread: child}); !errors.Is(err, api.ErrNotFound) {
				t.Errorf("the child after its parent's delete: %v, want not found", err)
			}
			gone(t, ad, parent, run)
			gone(t, ad, child, childRun)
		})
	}
}
