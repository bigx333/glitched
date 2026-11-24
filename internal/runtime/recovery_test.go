package runtime_test

import (
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strings"
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
		// The agent can finish its turn before the call that answered it
		// returns, so the answer's delivery may land after the turn's end.
		until(t, n.h, thread, "turn end and the answer's delivery", func(th api.Thread) bool {
			return idle(th) && count(events(t, n.h, thread), "request.delivered") > 0
		})
		evs := events(t, n.h, thread)
		if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeCompleted || count(evs, "request.delivered") != 1 || g.count(provider.Respond{}) != 1 {
			t.Fatalf("turn ended %v with %d request.delivered after %d answers reached the agent, want completed, 1 and 1",
				got, count(evs, "request.delivered"), g.count(provider.Respond{}))
		}
		assertReplay(t, n.st)
	})

	t.Run("answer for a lost agent is invalidated", func(t *testing.T) {
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
		assertReplay(t, n.st)
	})
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
