package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

var ctx = context.Background()

type node struct {
	st *store.Store
	h  *runtime.Host
}

func start(t *testing.T, path string, ad provider.Adapter) node {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, ad)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	n := node{st, h}
	t.Cleanup(n.kill)
	return n
}

// kill stops the host the way a killed process stops, recording nothing
// more, then closes the database.
func (n node) kill() {
	n.h.Kill()
	n.st.Close()
}

// stop closes the host gracefully, so what it already took from agents
// still commits, then closes the database.
func (n node) stop() {
	n.h.Close()
	n.st.Close()
}

func dbPath(t *testing.T) string { return filepath.Join(t.TempDir(), "host.db") }

func submit(t *testing.T, ep api.Endpoint, thread api.ID, body api.Intent) api.Receipt {
	t.Helper()
	r, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: body})
	if err != nil {
		t.Fatalf("%s: %v", api.IntentType(body), err)
	}
	return r
}

func newThread(t *testing.T, ep api.Endpoint) api.ID {
	t.Helper()
	id := api.NewID()
	submit(t, ep, id, api.ThreadCreate{Workspace: "/src/app", Title: "fix it", Model: api.ModelRef{Provider: "fake", Model: "fake"}})
	return id
}

func dispatch(text string, mode api.DispatchMode) api.MessageDispatch {
	return api.MessageDispatch{Text: text, Mode: mode}
}

func until(t *testing.T, ep api.Endpoint, thread api.ID, what string, ok func(api.Thread) bool) api.Thread {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, err := ep.Read(ctx, api.Query{Thread: thread})
		if err == nil && ok(snap.Threads[0]) {
			return snap.Threads[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %+v %v", what, snap.Threads, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func idle(th api.Thread) bool    { return th.Run == api.RunIdle }
func waiting(th api.Thread) bool { return th.Run == api.RunWaiting && len(th.Requests) == 1 }

func events(t *testing.T, ep api.Endpoint, thread api.ID) []api.Event {
	t.Helper()
	snap, err := ep.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	return snap.Events
}

func count(evs []api.Event, typ string) int {
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func transcript(t *testing.T, evs []api.Event) string {
	t.Helper()
	var b strings.Builder
	for _, e := range evs {
		if e.Type == "assistant.delta" {
			d, err := e.Decode()
			if err != nil {
				t.Fatal(err)
			}
			b.WriteString(d.(api.AssistantDelta).Text)
		}
	}
	return b.String()
}

func respond(t *testing.T, ep api.Endpoint, thread api.ID, r api.Request) {
	t.Helper()
	submit(t, ep, thread, api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: json.RawMessage(`{"allow":true}`)})
}

// assertReplay folds the whole log through engine.Apply and expects exactly
// the projections the store holds.
func assertReplay(t *testing.T, st *store.Store) {
	t.Helper()
	log, err := st.ReadLog(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	folded := map[api.ID]engine.Thread{}
	for i, e := range log {
		if e.Cursor.Seq != uint64(i+1) {
			t.Fatalf("log is not dense: event %d has seq %d", i, e.Cursor.Seq)
		}
		body, err := e.Decode()
		if err != nil || body == nil {
			t.Fatalf("decode %s: %v", e.Type, err)
		}
		folded[e.Thread] = engine.Apply(folded[e.Thread], engine.Event{Thread: e.Thread, At: e.At, Body: body})
	}
	if len(folded) == 0 {
		t.Fatal("empty log")
	}
	for id, want := range folded {
		got, err := st.Thread(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			g, _ := json.MarshalIndent(got, "", " ")
			w, _ := json.MarshalIndent(want, "", " ")
			t.Fatalf("thread %s: stored projection\n%s\nreplayed log\n%s", id, g, w)
		}
	}
}

func TestTurnStreamsIntoTheLog(t *testing.T) {
	ad := fake.New(fake.Turn(false))
	n := start(t, dbPath(t), ad)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("look", api.StartNow))
	until(t, n.h, thread, "turn end", idle)

	evs := events(t, n.h, thread)
	var got []string
	for _, e := range evs {
		if e.Type != "assistant.delta" && e.Type != "work.item" {
			got = append(got, e.Type)
		}
	}
	want := []string{"thread.created", "message.queued", "turn.admitted", "turn.started", "turn.ended"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle events %v, want %v", got, want)
	}
	if text := transcript(t, evs); text != "Looking at the code. Done." {
		t.Fatalf("transcript %q", text)
	}
	if w := count(evs, "work.item"); w != 4 {
		t.Fatalf("%d work item events, want 4", w)
	}
	end, _ := evs[len(evs)-1].Decode()
	if end.(api.TurnEnded).Outcome != api.OutcomeCompleted {
		t.Fatalf("turn ended %+v", end)
	}
	if ad.Starts() != 1 {
		t.Fatalf("%d starts", ad.Starts())
	}
}

func TestKillBetweenCommitAndEffect(t *testing.T) {
	path := dbPath(t)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	thread := newThread(t, st2endpoint{st})
	submit(t, st2endpoint{st}, thread, dispatch("go", api.StartNow))
	st.Close() // dies with the turn.start effect committed and never run

	ad := fake.New(fake.Turn(false))
	n := start(t, path, ad)
	until(t, n.h, thread, "turn end", idle)
	evs := events(t, n.h, thread)
	if a, s := count(evs, "turn.admitted"), count(evs, "turn.started"); a != 1 || s != 1 || ad.Starts() != 1 {
		t.Fatalf("admitted %d, started %d, adapter starts %d; want one each", a, s, ad.Starts())
	}

	n.kill()
	n = start(t, path, ad)
	submit(t, n.h, thread, dispatch("again", api.StartNow))
	until(t, n.h, thread, "second turn end", func(th api.Thread) bool { return idle(th) && ad.Starts() == 2 })
	if got := count(events(t, n.h, thread), "turn.started"); got != 2 || ad.Starts() != 2 {
		t.Fatalf("after restart: %d turn.started, %d adapter starts; want 2 and 2", got, ad.Starts())
	}
	assertReplay(t, n.st)
}

// st2endpoint submits straight to a store with no host, so nothing performs
// the effects a command commits.
type st2endpoint struct{ st *store.Store }

func (e st2endpoint) Info(context.Context) (api.Environment, error) { return api.Environment{}, nil }
func (e st2endpoint) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	return e.st.Read(ctx, q)
}
func (e st2endpoint) Submit(ctx context.Context, c api.Command) (api.Receipt, error) {
	return e.st.Submit(ctx, c, time.Now())
}
func (e st2endpoint) Open(context.Context, api.Subscription) (api.Stream, error) {
	return nil, errors.New("no host")
}

func TestRespondRaceHasOneWinner(t *testing.T) {
	n := start(t, dbPath(t), fake.New(fake.Turn(true)))
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("edit", api.StartNow))
	req := until(t, n.h, thread, "approval", waiting).Requests[0]
	if text := transcript(t, events(t, n.h, thread)); text != "Looking at the code. May I edit it?" {
		t.Fatalf("text before the approval %q, want it flushed ahead of the request", text)
	}

	const clients = 8
	errs := make([]error, clients)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			_, errs[i] = n.h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.RequestRespond{
				Request: req.ID, Generation: req.Generation, Answer: json.RawMessage(`{"client":` + string(rune('0'+i)) + `}`),
			}})
		}()
	}
	close(gate)
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, api.ErrAlreadyAnswered):
			t.Fatalf("loser got %v, want ErrAlreadyAnswered", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d winners", won)
	}
	until(t, n.h, thread, "turn end", idle)
	evs := events(t, n.h, thread)
	if count(evs, "request.answered") != 1 || count(evs, "request.delivered") != 1 {
		t.Fatalf("answered %d, delivered %d", count(evs, "request.answered"), count(evs, "request.delivered"))
	}

	_, err := n.h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.RequestRespond{
		Request: req.ID, Generation: req.Generation + 1, Answer: json.RawMessage(`{}`),
	}})
	if rej := (*api.RejectedError)(nil); !errors.As(err, &rej) {
		t.Fatalf("answer for another generation: %v, want rejected", err)
	}
	assertReplay(t, n.st)
}

func TestSubmitIsIdempotent(t *testing.T) {
	ad := fake.New(fake.Turn(false))
	n := start(t, dbPath(t), ad)
	thread := newThread(t, n.h)
	c := api.Command{ID: api.NewID(), Target: thread, Body: dispatch("once", api.StartNow)}
	first, err := n.h.Submit(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	until(t, n.h, thread, "turn end", idle)
	again, err := n.h.Submit(ctx, c)
	if err != nil || again != first {
		t.Fatalf("resubmit: %+v %v, want %+v", again, err, first)
	}
	c.Body = dispatch("twice", api.StartNow)
	if _, err := n.h.Submit(ctx, c); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("changed body: %v, want ErrConflict", err)
	}
	if q := count(events(t, n.h, thread), "message.queued"); q != 1 || ad.Starts() != 1 {
		t.Fatalf("%d messages and %d starts, want 1 and 1", q, ad.Starts())
	}
}

func TestDispatchModes(t *testing.T) {
	n := start(t, dbPath(t), fake.New(fake.Turn(true)))
	driveModes(t, n.h)
	assertReplay(t, n.st)
}

// driveModes walks one thread through every dispatch mode.
func driveModes(t *testing.T, ep api.Endpoint) api.ID {
	t.Helper()
	thread := newThread(t, ep)
	submit(t, ep, thread, dispatch("first", api.StartNow))
	until(t, ep, thread, "approval", waiting)

	_, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: dispatch("now", api.StartNow)})
	if rej := (*api.RejectedError)(nil); !errors.As(err, &rej) {
		t.Fatalf("StartNow on a busy thread: %v, want rejected", err)
	}

	submit(t, ep, thread, dispatch("hurry", api.SteerActive))
	until(t, ep, thread, "steer delivered", func(api.Thread) bool {
		return strings.Contains(transcript(t, events(t, ep, thread)), "steered: hurry")
	})

	submit(t, ep, thread, dispatch("later", api.Defer))
	submit(t, ep, thread, dispatch("next", api.AfterActive))
	if th := until(t, ep, thread, "two queued", func(th api.Thread) bool { return th.Queued == 2 }); th.Run != api.RunWaiting {
		t.Fatalf("queueing changed the run: %v", th.Run)
	}
	respond(t, ep, thread, until(t, ep, thread, "approval", waiting).Requests[0])

	next := until(t, ep, thread, "next starts by itself while later stays held", func(th api.Thread) bool { return waiting(th) && th.Queued == 1 })
	submit(t, ep, thread, dispatch("instead", api.RestartActive))
	until(t, ep, thread, "restart waiting", func(th api.Thread) bool {
		return waiting(th) && th.Requests[0].Run != next.Requests[0].Run
	})
	submit(t, ep, thread, api.TurnInterrupt{})
	th := until(t, ep, thread, "interrupted", idle)
	if th.Queued != 1 {
		t.Fatalf("held message left the queue: %d queued", th.Queued)
	}

	submit(t, ep, thread, api.InboxSet{Disposition: api.Disposition{State: api.Snoozed, Until: time.Now().Add(time.Hour)}})
	submit(t, ep, thread, api.InboxSet{Disposition: api.Disposition{State: api.Settled}})

	var ends []api.Outcome
	for _, e := range events(t, ep, thread) {
		if e.Type == "turn.ended" {
			b, _ := e.Decode()
			ends = append(ends, b.(api.TurnEnded).Outcome)
		}
	}
	want := []api.Outcome{api.OutcomeCompleted, api.OutcomeInterrupted, api.OutcomeInterrupted}
	if !reflect.DeepEqual(ends, want) {
		t.Fatalf("turn outcomes %v, want %v", ends, want)
	}
	return thread
}

func TestCrashMidTurn(t *testing.T) {
	t.Run("same agent is restored", func(t *testing.T) {
		path, ad := dbPath(t), fake.New(fake.Turn(true))
		n := start(t, path, ad)
		thread := newThread(t, n.h)
		submit(t, n.h, thread, dispatch("go", api.StartNow))
		seen := until(t, n.h, thread, "approval", waiting)
		n.kill()

		n = start(t, path, ad)
		req := until(t, n.h, thread, "still waiting", waiting).Requests[0]
		if _, err := n.h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Revision: seen.Revision, Body: api.RequestRespond{
			Request: req.ID, Generation: req.Generation, Answer: json.RawMessage(`{"allow":true}`),
		}}); err != nil {
			t.Fatalf("answer at the revision read before the restart: %v", err)
		}
		until(t, n.h, thread, "turn end", idle)
		evs := events(t, n.h, thread)
		if count(evs, "turn.restored") != 1 || count(evs, "request.opened") != 1 || ad.Starts() != 1 {
			t.Fatalf("restored %d, requests %d, starts %d; want 1 each",
				count(evs, "turn.restored"), count(evs, "request.opened"), ad.Starts())
		}
		if end, _ := evs[len(evs)-1].Decode(); end.(api.TurnEnded).Outcome != api.OutcomeCompleted || !strings.HasSuffix(transcript(t, evs), " Done.") {
			t.Fatalf("restored turn ended %+v with %q, want it to finish its script", end, transcript(t, evs))
		}
		assertReplay(t, n.st)
	})

	t.Run("lost agent makes the run uncertain", func(t *testing.T) {
		path, ad := dbPath(t), fake.New(fake.Turn(true))
		n := start(t, path, ad)
		thread := newThread(t, n.h)
		submit(t, n.h, thread, dispatch("go", api.StartNow))
		until(t, n.h, thread, "approval", waiting)
		n.kill()

		fresh := fake.New(fake.Turn(false))
		n = start(t, path, fresh)
		th := until(t, n.h, thread, "uncertain", func(th api.Thread) bool { return th.Run == api.RunUncertain })
		if len(th.Requests) != 0 {
			t.Fatalf("uncertain run kept requests: %+v", th.Requests)
		}
		submit(t, n.h, thread, dispatch("retry", api.AfterActive))
		if th := until(t, n.h, thread, "queued", func(th api.Thread) bool { return th.Queued == 1 }); th.Run != api.RunUncertain || fresh.Starts() != 0 {
			t.Fatalf("an uncertain run let a turn start: %v, %d starts", th.Run, fresh.Starts())
		}
		submit(t, n.h, thread, api.TurnInterrupt{})
		until(t, n.h, thread, "retry ran", func(th api.Thread) bool { return idle(th) && fresh.Starts() == 1 })
		if ad.Starts() != 1 {
			t.Fatalf("first turn started %d times", ad.Starts())
		}
		assertReplay(t, n.st)
	})

	t.Run("crash during start is never started twice", func(t *testing.T) {
		path, ad := dbPath(t), fake.New(fake.Turn(false))
		ad.HoldStart = true
		n := start(t, path, ad)
		thread := newThread(t, n.h)
		submit(t, n.h, thread, dispatch("go", api.StartNow))
		deadline := time.Now().Add(5 * time.Second)
		for ad.Starts() == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		n.kill()

		fresh := fake.New(fake.Turn(false))
		n = start(t, path, fresh)
		until(t, n.h, thread, "uncertain", func(th api.Thread) bool { return th.Run == api.RunUncertain })
		if ad.Starts() != 1 || fresh.Starts() != 0 {
			t.Fatalf("starts: %d before the crash, %d after; want 1 and 0", ad.Starts(), fresh.Starts())
		}
		assertReplay(t, n.st)
	})
}

func TestReplayMatchesProjections(t *testing.T) {
	path, ad := dbPath(t), fake.New(fake.Turn(true))
	n := start(t, path, ad)
	driveModes(t, n.h)
	other := newThread(t, n.h)
	submit(t, n.h, other, dispatch("go", api.StartNow))
	until(t, n.h, other, "approval", waiting)
	n.kill()
	n = start(t, path, fake.New(fake.Turn(false)))
	until(t, n.h, other, "uncertain", func(th api.Thread) bool { return th.Run == api.RunUncertain })
	submit(t, n.h, other, api.TurnInterrupt{})
	assertReplay(t, n.st)
}

func next(t *testing.T, s api.Stream) api.StreamItem {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	it, err := s.Next(c)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

// drain reads events up to StreamSynchronized and returns them.
func drain(t *testing.T, s api.Stream) []api.Event {
	t.Helper()
	var evs []api.Event
	for {
		it := next(t, s)
		switch it.Kind {
		case api.StreamSynchronized:
			return evs
		case api.StreamSnapshot:
			t.Fatal("unexpected snapshot")
		}
		evs = append(evs, *it.Event)
	}
}

func TestStreamResync(t *testing.T) {
	n := start(t, dbPath(t), fake.New(fake.Turn(false)))
	thread := newThread(t, n.h)

	s, err := n.h.Open(ctx, api.Subscription{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	first := next(t, s)
	if first.Kind != api.StreamSnapshot || first.Snapshot.Threads[0].ID != thread || first.Snapshot.Cursor.Seq != 1 {
		t.Fatalf("first item %+v, want a snapshot of the new thread at seq 1", first)
	}
	if it := next(t, s); it.Kind != api.StreamSynchronized {
		t.Fatalf("second item %v, want synchronized", it.Kind)
	}
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	var live []api.Event
	for len(live) == 0 || live[len(live)-1].Type != "turn.ended" {
		it := next(t, s)
		if it.Kind != api.StreamEvent {
			t.Fatalf("live item %v, want an event", it.Kind)
		}
		live = append(live, *it.Event)
	}
	for i, e := range live {
		if e.Cursor.Seq != first.Snapshot.Cursor.Seq+uint64(i)+1 {
			t.Fatalf("live event %d has seq %d after fence %d", i, e.Cursor.Seq, first.Snapshot.Cursor.Seq)
		}
	}
	s.Close()

	seen := live[len(live)-1].Cursor
	submit(t, n.h, thread, dispatch("again", api.StartNow))
	until(t, n.h, thread, "second turn", func(th api.Thread) bool {
		return idle(th) && count(events(t, n.h, thread), "turn.ended") == 2
	})
	s, err = n.h.Open(ctx, api.Subscription{Thread: thread, After: seen})
	if err != nil {
		t.Fatal(err)
	}
	replay := drain(t, s)
	if len(replay) == 0 || replay[0].Cursor.Seq != seen.Seq+1 || replay[len(replay)-1].Type != "turn.ended" {
		t.Fatalf("replay from %d: %d events, first %+v", seen.Seq, len(replay), replay[0].Cursor)
	}
	submit(t, n.h, thread, api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	if it := next(t, s); it.Kind != api.StreamEvent || it.Event.Type != "disposition.changed" {
		t.Fatalf("live after replay: %+v", it)
	}
	s.Close()

	foreign, err := n.h.Open(ctx, api.Subscription{Thread: thread, After: api.Cursor{Log: api.NewID(), Seq: seen.Seq}})
	if err != nil {
		t.Fatal(err)
	}
	if it := next(t, foreign); it.Kind != api.StreamSnapshot {
		t.Fatalf("foreign log: %v, want a snapshot", it.Kind)
	}
	foreign.Close()

	head := events(t, n.h, thread)
	for i := range 130 {
		state := api.Open
		if i%2 == 0 {
			state = api.Snoozed
		}
		var until time.Time
		if state == api.Snoozed {
			until = time.Now().Add(time.Hour)
		}
		submit(t, n.h, thread, api.InboxSet{Disposition: api.Disposition{State: state, Until: until}})
	}
	far, err := n.h.Open(ctx, api.Subscription{Thread: thread, After: head[len(head)-1].Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if it := next(t, far); it.Kind != api.StreamSnapshot {
		t.Fatalf("gap of 130 events: %v, want a snapshot", it.Kind)
	}
	far.Close()
}

func TestInterruptDuringDeliveryLogsWhatTheAgentGot(t *testing.T) {
	g := newGated(fake.Turn(true))
	entered, release := make(chan struct{}, 1), make(chan struct{})
	g.setGate(func(_ api.ID, a provider.Action) <-chan struct{} {
		if _, ok := a.(provider.Respond); ok {
			entered <- struct{}{}
			// Taken in spite of the stop's cancel, like an answer already
			// half written to the agent.
			<-release
		}
		return nil
	})
	n := start(t, dbPath(t), g)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("edit", api.StartNow))
	respond(t, n.h, thread, until(t, n.h, thread, "approval", waiting).Requests[0])
	<-entered
	submit(t, n.h, thread, api.TurnInterrupt{})
	close(release)
	until(t, n.h, thread, "turn end", idle)

	evs := events(t, n.h, thread)
	if got := g.count(provider.Respond{}); got != 1 {
		t.Fatalf("the agent took %d answers, want 1", got)
	}
	stored, err := n.st.Thread(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	if count(evs, "request.delivered") != 1 || stored.Requests[0].State != api.RequestDelivered {
		t.Fatalf("the agent took the answer but the log has %d request.delivered and the request is %v",
			count(evs, "request.delivered"), stored.Requests[0].State)
	}
	assertReplay(t, n.st)
}
