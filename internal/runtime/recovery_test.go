package runtime_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/store"
)

// crashDuring brings a thread to an approval, holds the first provider call
// of a's type that body causes, and crashes the host while it is in flight,
// so the effect is left leased.
func crashDuring(t *testing.T, path string, g *gated, a provider.Action, body func(n node, thread api.ID)) api.ID {
	t.Helper()
	n := start(t, path, g)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "approval", waiting)
	entered, never := make(chan struct{}, 1), make(chan struct{})
	g.setGate(func(_ api.ID, act provider.Action) <-chan struct{} {
		if reflect.TypeOf(act) == reflect.TypeOf(a) {
			select {
			case entered <- struct{}{}:
			default:
			}
			return never
		}
		return nil
	})
	body(n, thread)
	<-entered
	n.kill()
	g.setGate(nil)
	return thread
}

// A run's plans reach the log once each as its agent said them, across a
// crash while it waits on the user, and the log folds to the projection
// the store holds.
func TestPlansOutliveACrash(t *testing.T) {
	plan := func(first, second api.PlanStatus) provider.Plan {
		return provider.Plan{Steps: []api.PlanStep{{Text: "Read main.go", Status: first}, {Text: "Edit main.go", Status: second}}}
	}
	script := append([]provider.Event{plan(api.PlanActive, api.PlanPending)}, fake.Turn(true)...)
	script = append(script, plan(api.PlanDone, api.PlanDone))
	path, g := dbPath(t), newGated(script)
	thread := crashDuring(t, path, g, provider.Respond{}, func(n node, thread api.ID) {
		respond(t, n.h, thread, until(t, n.h, thread, "approval", waiting).Requests[0])
	})
	n := start(t, path, g)
	until(t, n.h, thread, "turn end", idle)
	var got [][]api.PlanStep
	for _, e := range events(t, n.h, thread) {
		if b, _ := e.Decode(); e.Type == "plan.updated" {
			got = append(got, b.(api.PlanUpdated).Steps)
		}
	}
	want := [][]api.PlanStep{plan(api.PlanActive, api.PlanPending).Steps, plan(api.PlanDone, api.PlanDone).Steps}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the log's plans %+v, want %+v", got, want)
	}
	assertReplay(t, n.st)
}

// untilEffectsDone waits for the host to finish every effect of kind: a
// run can end on its agent's word before the host records what its call
// to that agent did.
func untilEffectsDone(t *testing.T, path, kind string) {
	t.Helper()
	db := sideDB(t, path)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		var open int
		if err := db.QueryRow("SELECT COUNT(*) FROM effects WHERE kind = ? AND state <> 'done'", kind).Scan(&open); err != nil {
			t.Fatal(err)
		}
		if open == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d %s effects still open", open, kind)
		}
	}
}

func TestCrashWithALeasedEffect(t *testing.T) {
	t.Run("steer becomes uncertain and is never sent again", func(t *testing.T) {
		path, g := dbPath(t), newGated(fake.Turn(true))
		thread := crashDuring(t, path, g, provider.Steer{}, func(n node, thread api.ID) {
			submit(t, n.h, thread, dispatch("hurry", api.SteerActive))
		})
		n := start(t, path, g)
		respond(t, n.h, thread, until(t, n.h, thread, "restored and waiting", waiting).Requests[0])
		until(t, n.h, thread, "turn end", idle)
		evs := events(t, n.h, thread)
		if count(evs, "message.uncertain") != 1 || g.count(provider.Steer{}) != 0 || strings.Contains(transcript(t, evs), "steered") {
			t.Fatalf("message.uncertain %d, steers taken %d, transcript %q; want the steer uncertain and never resent",
				count(evs, "message.uncertain"), g.count(provider.Steer{}), transcript(t, evs))
		}
		assertReplay(t, n.st)
	})

	t.Run("interrupt runs again", func(t *testing.T) {
		path, g := dbPath(t), newGated(fake.Turn(true))
		thread := crashDuring(t, path, g, provider.Interrupt{}, func(n node, thread api.ID) {
			submit(t, n.h, thread, api.TurnInterrupt{})
		})
		n := start(t, path, g)
		until(t, n.h, thread, "turn end", idle)
		untilEffectsDone(t, path, "turn.interrupt")
		if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeInterrupted || g.count(provider.Interrupt{}) != 1 {
			t.Fatalf("turn ended %v after %d interrupts reached the agent, want interrupted after 1", got, g.count(provider.Interrupt{}))
		}
		assertReplay(t, n.st)
	})

	t.Run("answer is delivered again", func(t *testing.T) {
		path, g := dbPath(t), newGated(fake.Turn(true))
		thread := crashDuring(t, path, g, provider.Respond{}, func(n node, thread api.ID) {
			respond(t, n.h, thread, until(t, n.h, thread, "approval", waiting).Requests[0])
		})
		n := start(t, path, g)
		until(t, n.h, thread, "turn end", idle)
		untilEffectsDone(t, path, "request.deliver")
		evs := events(t, n.h, thread)
		if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeCompleted || count(evs, "request.delivered") != 1 || g.count(provider.Respond{}) != 1 {
			t.Fatalf("turn ended %v with %d request.delivered after %d answers reached the agent, want completed, 1 and 1",
				got, count(evs, "request.delivered"), g.count(provider.Respond{}))
		}
		assertReplay(t, n.st)
	})

	t.Run("answer for a lost agent is closed as unconfirmed", func(t *testing.T) {
		path, g := dbPath(t), newGated(fake.Turn(true))
		thread := crashDuring(t, path, g, provider.Respond{}, func(n node, thread api.ID) {
			respond(t, n.h, thread, until(t, n.h, thread, "approval", waiting).Requests[0])
		})
		n := start(t, path, fake.New(fake.Turn(true)))
		until(t, n.h, thread, "invalidated", func(api.Thread) bool {
			return count(events(t, n.h, thread), "request.invalidated") == 1
		})
		snap, err := n.h.Read(ctx, api.Query{Thread: thread})
		if err != nil {
			t.Fatal(err)
		}
		if snap.Threads[0].Run != api.RunUncertain || count(snap.Events, "request.delivered") != 0 {
			t.Fatalf("run %v with %d request.delivered, want uncertain and none", snap.Threads[0].Run, count(snap.Events, "request.delivered"))
		}
		if got := invalidations(t, snap.Events); len(got) != 1 || !got[0].Unconfirmed {
			t.Fatalf("invalidations %+v, want one saying the agent may have the answer", got)
		}
		assertReplay(t, n.st)
	})
}

// invalidations is the request invalidations in evs.
func invalidations(t *testing.T, evs []api.Event) []api.RequestInvalidation {
	t.Helper()
	var out []api.RequestInvalidation
	for _, e := range evs {
		if b, _ := e.Decode(); b != nil {
			if i, ok := b.(api.RequestInvalidation); ok {
				out = append(out, i)
			}
		}
	}
	return out
}

// copyDB copies a closed database file by file, the way a user restores a
// backup of it.
func copyDB(t *testing.T, from, to string) {
	t.Helper()
	for _, sfx := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(from + sfx)
		if errors.Is(err, fs.ErrNotExist) {
			os.Remove(to + sfx)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to+sfx, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func uncertain(th api.Thread) bool { return th.Run == api.RunUncertain }

func TestARestoredDatabaseNeverStartsATurnAgain(t *testing.T) {
	path := dbPath(t)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	thread := newThread(t, st2endpoint{st})
	submit(t, st2endpoint{st}, thread, dispatch("go", api.StartNow))
	st.Close()
	copyDB(t, path, path+".bak") // the backup holds the start, not yet run

	ad := fake.New(fake.Turn(false))
	n := start(t, path, ad)
	until(t, n.h, thread, "turn end", idle)
	n.kill()

	copyDB(t, path+".bak", path)
	n = start(t, path, ad)
	until(t, n.h, thread, "the run from the backup is uncertain", uncertain)
	time.Sleep(50 * time.Millisecond)
	if ad.Starts() != 1 {
		t.Fatalf("the turn started %d times, want once: the backup's start already ran", ad.Starts())
	}
	assertReplay(t, n.st)
}

func TestARestoredDatabaseSendsNothingItHeldInFlight(t *testing.T) {
	path, g := dbPath(t), newGated(fake.Turn(true))
	var steer api.ID
	thread := crashDuring(t, path, g, provider.Steer{}, func(n node, thread api.ID) {
		req := until(t, n.h, thread, "approval", waiting).Requests[0]
		steer = api.NewID()
		if _, err := n.h.Submit(ctx, api.Command{ID: steer, Target: thread, Body: dispatch("hurry", api.SteerActive)}); err != nil {
			t.Fatal(err)
		}
		respond(t, n.h, thread, req)
	})
	copyDB(t, path, path+".bak") // a steer leased, an answer and the run in flight

	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, st2endpoint{st}, thread, api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	st.Close()
	copyDB(t, path+".bak", path)

	n := start(t, path, g)
	until(t, n.h, thread, "the run from the backup is uncertain", uncertain)
	stored, err := n.st.Thread(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	until(t, n.h, thread, "the answer fails", func(api.Thread) bool {
		stored, err = n.st.Thread(ctx, thread)
		return err == nil && stored.Requests[0].State == api.RequestInvalidated
	})
	m, _ := stored.Message(steer)
	if g.count(provider.Steer{}) != 0 || g.count(provider.Respond{}) != 0 || m.State != engine.QueueUncertain || g.Starts() != 1 {
		t.Fatalf("after the restore: %d steers and %d answers reached the agent, steer %v, %d starts; want none, none, uncertain and 1",
			g.count(provider.Steer{}), g.count(provider.Respond{}), m.State, g.Starts())
	}
	assertReplay(t, n.st)
}

// endsFirst opens the fake's agents to end their turn before the call that
// answered them returns, as an agent that acts on an answer at once does.
type endsFirst struct {
	*fake.Adapter
}

func (a endsFirst) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	g, err := a.Adapter.Open(ctx, spec)
	return &endsFirstAgent{Agent: g, ended: make(chan struct{})}, err
}

type endsFirstAgent struct {
	provider.Agent
	ended chan struct{}
	once  sync.Once
}

func (g *endsFirstAgent) Next(ctx context.Context) (provider.Event, error) {
	ev, err := g.Agent.Next(ctx)
	if _, ok := ev.(provider.TurnEnded); ok {
		g.once.Do(func() { close(g.ended) })
	}
	return ev, err
}

func (g *endsFirstAgent) Do(ctx context.Context, act provider.Action) error {
	err := g.Agent.Do(ctx, act)
	if _, ok := act.(provider.Respond); ok && err == nil {
		<-g.ended
	}
	return err
}

// An answer the agent took is logged as delivered before the turn the
// agent ended on it, though the agent ended before the call returned.
func TestADeliveryIsLoggedBeforeTheTurnItEnded(t *testing.T) {
	path := dbPath(t)
	n := start(t, path, endsFirst{fake.New(fake.Turn(true))})
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("edit", api.StartNow))
	respond(t, n.h, thread, until(t, n.h, thread, "approval", waiting).Requests[0])
	until(t, n.h, thread, "turn end", idle)
	untilEffectsDone(t, path, "request.deliver")
	var order []string
	for _, e := range events(t, n.h, thread) {
		if e.Type == "request.delivered" || e.Type == "turn.ended" {
			order = append(order, e.Type)
		}
	}
	if !slices.Equal(order, []string{"request.delivered", "turn.ended"}) {
		t.Fatalf("logged %v, want the delivery before the turn's end", order)
	}
	assertReplay(t, n.st)
}

// holdReceipts makes the store refuse to finish any answer's effect, which
// the receipt of an answer does.
const holdReceipts = `CREATE TRIGGER hold_receipts BEFORE UPDATE OF state ON effects
	WHEN NEW.kind = 'request.deliver' AND NEW.state = 'done' BEGIN SELECT RAISE(ABORT, 'held'); END`

// An answer's receipt the store won't take doesn't hold the turn that ended
// on it; the receipt follows once the store takes it.
func TestATurnEndsThoughItsAnswersReceiptWontLog(t *testing.T) {
	path := dbPath(t)
	n := start(t, path, endsFirst{fake.New(fake.Turn(true))})
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("edit", api.StartNow))
	req := until(t, n.h, thread, "approval", waiting).Requests[0]
	db := sideDB(t, path)
	exec(t, db, holdReceipts)
	respond(t, n.h, thread, req)
	until(t, n.h, thread, "turn end", idle)
	if got := count(events(t, n.h, thread), "request.delivered"); got != 0 {
		t.Fatalf("%d request.delivered while the store refuses them", got)
	}
	exec(t, db, "DROP TRIGGER hold_receipts")
	untilEffectsDone(t, path, "request.deliver")
	evs := events(t, n.h, thread)
	if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeCompleted || count(evs, "request.delivered") != 1 || len(invalidations(t, evs)) != 0 {
		t.Fatalf("turn ended %v with %d request.delivered and %d invalidations, want completed, 1 and none",
			got, count(evs, "request.delivered"), len(invalidations(t, evs)))
	}
	assertReplay(t, n.st)
}

// An answer a crash cut off stays one the agent may have through a second
// crash before it is given again.
func TestAnAnswerACrashCutOffStaysUnconfirmedThroughAnotherCrash(t *testing.T) {
	path := dbPath(t)
	ad := endsFirst{fake.New(fake.Turn(true))}
	n := start(t, path, ad)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("edit", api.StartNow))
	req := until(t, n.h, thread, "approval", waiting).Requests[0]
	db := sideDB(t, path)
	exec(t, db, holdReceipts)
	respond(t, n.h, thread, req)
	until(t, n.h, thread, "turn end", idle)
	n.kill()
	exec(t, db, "DROP TRIGGER hold_receipts")
	exec(t, db, `CREATE TRIGGER hold_leases BEFORE UPDATE OF state ON effects
		WHEN NEW.kind = 'request.deliver' AND NEW.state = 'leased' BEGIN SELECT RAISE(ABORT, 'held'); END`)
	n = start(t, path, ad)
	var state string
	if err := db.QueryRow("SELECT state FROM effects WHERE kind = 'request.deliver'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "pending" {
		t.Fatalf("the answer's effect is %s after recovery, want pending", state)
	}
	n.kill()
	exec(t, db, "DROP TRIGGER hold_leases")
	n = start(t, path, ad)
	untilEffectsDone(t, path, "request.deliver")
	evs := events(t, n.h, thread)
	if got := invalidations(t, evs); len(got) != 1 || !got[0].Unconfirmed || count(evs, "request.delivered") != 0 {
		t.Fatalf("invalidations %+v with %d request.delivered, want one saying the agent may have the answer and none",
			got, count(evs, "request.delivered"))
	}
	assertReplay(t, n.st)
}

// failsAfterEnd opens the fake's agents to end their turn, completed, as an
// answer reaches them, and only then fail the call that gave it, as a reply
// whose transport broke after the agent went on does.
type failsAfterEnd struct {
	*fake.Adapter
}

func (a failsAfterEnd) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	g, err := a.Adapter.Open(ctx, spec)
	return &failsAfterEndAgent{Agent: g, past: make(chan struct{})}, err
}

type failsAfterEndAgent struct {
	provider.Agent
	ended bool          // read only by the pump's reader
	past  chan struct{} // closed as the reader asks for what follows the end
	once  sync.Once
}

func (g *failsAfterEndAgent) Next(ctx context.Context) (provider.Event, error) {
	if g.ended {
		g.once.Do(func() { close(g.past) })
	}
	ev, err := g.Agent.Next(ctx)
	if _, ok := ev.(provider.TurnEnded); ok {
		g.ended = true
		ev = provider.TurnEnded{Outcome: api.OutcomeCompleted}
	}
	return ev, err
}

func (g *failsAfterEndAgent) Do(ctx context.Context, act provider.Action) error {
	if _, ok := act.(provider.Respond); !ok {
		return g.Agent.Do(ctx, act)
	}
	if err := g.Agent.Do(ctx, provider.Interrupt{}); err != nil {
		return err
	}
	select {
	case <-g.past:
		return errors.New("the reply's transport broke")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// An answer that fails after the agent ended its turn doesn't stop that
// turn: it ends as the agent ended it, and a child's parent hears so.
func TestAnAnswerThatFailsAfterTheTurnEndedLeavesTheTurnsEnd(t *testing.T) {
	path := dbPath(t)
	ad := failsAfterEnd{fake.New(fake.Turn(true))}
	n := start(t, path, ad)
	parent := newThread(t, n.h)
	child, err := newChild(t, n.h, ad, parent, api.AccessAsk)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, parent, api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	submit(t, n.h, child, dispatch("edit", api.StartNow))
	respond(t, n.h, child, until(t, n.h, child, "approval", waiting).Requests[0])
	until(t, n.h, child, "turn end", idle)
	untilEffectsDone(t, path, "request.deliver")
	untilEffectsDone(t, path, "child.report")
	var order []string
	for _, e := range events(t, n.h, child) {
		if e.Type == "request.invalidated" || e.Type == "turn.ended" || e.Type == "turn.stopping" {
			order = append(order, e.Type)
		}
	}
	if got := outcomes(t, n.h, child); len(got) != 1 || got[0] != api.OutcomeCompleted || !slices.Equal(order, []string{"turn.ended", "request.invalidated"}) {
		t.Fatalf("turn ended %v, logged %v, want completed and its end before the failed answer", got, order)
	}
	done := 0
	for _, e := range events(t, n.h, parent) {
		if b, _ := e.Decode(); b != nil {
			if r, ok := b.(api.ChildReported); ok && r.Child == child && r.Kind == api.ReportDone {
				done++
			}
		}
	}
	if done != 1 {
		t.Fatalf("parent heard the child was done %d times, want once", done)
	}
	assertReplay(t, n.st)
}
