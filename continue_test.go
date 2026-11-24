package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// queuedOn is every message the engine queued on thread, in order.
func queuedOn(t *testing.T, d *daemon.Daemon, thread api.ID) []api.MessageQueued {
	t.Helper()
	snap, err := d.Endpoint().Read(context.Background(), api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	var qs []api.MessageQueued
	for _, e := range snap.Events {
		if b, _ := e.Decode(); b != nil {
			if q, ok := b.(api.MessageQueued); ok {
				qs = append(qs, q)
			}
		}
	}
	return qs
}

// replaced is whether th's run was abandoned, and the one that replaced it
// ended.
func replaced(th thread) bool {
	v := th.View
	return v.Run == api.RunIdle && v.LastTurn != nil && v.LastTurn.Outcome == api.OutcomeCompleted
}

// Continue shows on a thread whose run was left uncertain, and only there:
// it replaces the run with one that picks up where the agent left off.
func TestContinueShowsOnlyOnAnUncertainThread(t *testing.T) {
	dir, d := lostDir(t, fake.New(fake.Turn(false)), "Stuck")
	seed(t, d, "/work/t3code", "Fresh")
	h := start(t, dir)
	h.a.select_(h.thread("Fresh"))
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("Continue"); ok {
		t.Fatalf("Continue shows on a thread with no run: %q", h.tt.Texts())
	}
	th := h.thread("Stuck")
	h.a.select_(th)
	h.until("its transcript", func() bool { return h.tt.HasText("May I edit it?") })
	h.tt.Advance(time.Second)
	h.click("Continue")
	h.until("the run replaced", func() bool { return replaced(th) })
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("Continue"); ok {
		t.Fatalf("Continue still shows once the run is replaced: %q", h.tt.Texts())
	}
	qs := queuedOn(t, d, th.id())
	if last := qs[len(qs)-1]; len(qs) != 2 || last.Text != api.ContinueText || last.Mode != api.RestartActive {
		t.Fatalf("queued %+v, want the first message and then the continue, RestartActive", qs)
	}
}

// Continue hides while the run that replaced the uncertain one works.
func TestContinueHidesWhileTheReplacementRuns(t *testing.T) {
	dir, _ := lostDir(t, slowStop{Adapter: fake.New(fake.Turn(false)), release: make(chan struct{})}, "Stuck")
	h := start(t, dir)
	th := h.thread("Stuck")
	h.a.select_(th)
	h.until("its transcript", func() bool { return h.tt.HasText("May I edit it?") })
	h.tt.Advance(time.Second)
	h.click("Continue")
	h.until("the replacement running", func() bool { return th.View.Run == api.RunRunning })
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("Continue"); ok {
		t.Fatalf("Continue shows while the replacement runs: %q", h.tt.Texts())
	}
}

// A message sent to a thread whose run was left uncertain replaces the run,
// rather than being refused as a steer.
func TestAMessageReplacesAnUncertainRun(t *testing.T) {
	dir, d := lostDir(t, fake.New(fake.Turn(false)), "Stuck")
	h := start(t, dir)
	th := h.thread("Stuck")
	h.a.select_(th)
	h.a.draft = "Keep going."
	h.tt.Advance(time.Second)
	h.click("Send")
	h.until("the run replaced", func() bool { return replaced(th) })
	h.tt.Advance(time.Second)
	if h.a.draft != "" || h.tt.HasText("can't take a message") {
		t.Fatalf("the composer holds %q, the window %q", h.a.draft, h.tt.Texts())
	}
	qs := queuedOn(t, d, th.id())
	if last := qs[len(qs)-1]; len(qs) != 2 || last.Text != "Keep going." || last.Mode != api.RestartActive {
		t.Fatalf("queued %+v, want the message RestartActive", qs)
	}
}

// A Continue sent from a view the engine has moved past, as a second
// client's or a stale Retry, is refused: it never stops the run the first
// Continue started.
func TestAStaleContinueIsRefused(t *testing.T) {
	dir, d := lostDir(t, fake.New(fake.Turn(true)), "Stuck")
	h := start(t, dir)
	th := h.thread("Stuck")
	first, second := h.a.continueRun(th), h.a.continueRun(th)
	refused := func() bool {
		rej := (*api.RejectedError)(nil)
		i := slices.IndexFunc(th.Pending, func(p client.Pending) bool { return p.Command == second })
		return i >= 0 && errors.As(th.Pending[i].Err, &rej)
	}
	h.until("the second refused", refused)
	h.a.backend.Retry(th.env, th.id(), second)
	h.until("the retry refused", refused)
	h.until("the first Continue's run waiting on its approval", func() bool {
		v := th.View
		return v.Turn != nil && v.Turn.Run == first && v.Run == api.RunWaiting
	})
	if qs := queuedOn(t, d, th.id()); len(qs) != 2 {
		t.Fatalf("queued %+v, want the first message and one Continue", qs)
	}
}

// Continue all in the inbox's head continues every thread a restart left
// uncertain, two at a time: a continued run holds its place while it's
// live, waiting on the user's answer too, and the next starts once one
// ends.
func TestContinueAllContinuesTwoAtATime(t *testing.T) {
	titles := []string{"One", "Two", "Three"}
	dir, d := lostDir(t, fake.New(fake.Turn(true)), titles...)
	h := start(t, dir)
	var ths []thread
	for _, title := range titles {
		ths = append(ths, h.thread(title))
	}
	in := func(s api.RunState) []thread {
		return slices.DeleteFunc(slices.Clone(ths), func(th thread) bool { return th.View.Run != s })
	}
	h.tt.Advance(time.Second)
	h.click("Continue all")
	h.until("two continued", func() bool { return len(in(api.RunWaiting)) == 2 })
	h.tt.Advance(time.Second)
	if left := in(api.RunUncertain); len(left) != 1 || !continuable(left[0]) {
		t.Fatalf("%d left uncertain with two continued runs waiting, want 1 not yet continued", len(left))
	}
	if _, ok := h.tt.Find("Continue all"); ok {
		t.Fatal("Continue all still shows with every lost thread continuing")
	}
	answered := in(api.RunWaiting)[0]
	h.a.select_(answered)
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("Continue"); ok {
		t.Fatal("Continue shows on a thread whose continued run waits on its approval")
	}
	r := answered.View.Requests[0]
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: answered.id(), Body: api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: json.RawMessage(`{"allow":true}`)}}); err != nil {
		t.Fatal(err)
	}
	h.until("the third continued", func() bool { return len(in(api.RunUncertain)) == 0 })
	if len(h.a.continues) != 2 {
		t.Fatalf("%d threads held to continue, want the two live", len(h.a.continues))
	}
}

// eventsOn is thread's log on d.
func eventsOn(t *testing.T, d *daemon.Daemon, thread api.ID) []api.Event {
	t.Helper()
	snap, err := d.Endpoint().Read(context.Background(), api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	return snap.Events
}

// engineView waits for thread's view on d to satisfy ok.
func engineView(t *testing.T, d *daemon.Daemon, thread api.ID, what string, ok func(api.Thread) bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if v, _ := viewOn(t, d, thread); ok(v) {
			return
		}
		if time.Now().After(deadline) {
			v, _ := viewOn(t, d, thread)
			t.Fatalf("%s never came: %+v", what, v)
		}
	}
}

// laterSlow runs turns on the fake, and once slow is set on slowStop's
// agents, which stop only once release closes.
type laterSlow struct {
	*fake.Adapter
	slow    atomic.Bool
	release chan struct{}
}

func (g *laterSlow) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	if g.slow.Load() {
		return slowStop{Adapter: g.Adapter, release: g.release}.Open(ctx, spec)
	}
	return g.Adapter.Open(ctx, spec)
}

// A Continue all's place frees once the run its Continue started is over,
// even when the thread is uncertain again by the time the app looks, in a
// run after the next.
func TestContinueAllFreesAPlaceOnceItsRunIsOver(t *testing.T) {
	g := &laterSlow{Adapter: fake.New(fake.Turn(false)), release: make(chan struct{})}
	dir, d := lostDir(t, g, "One", "Two", "Three")
	h := start(t, dir)
	one, two, three := h.thread("One"), h.thread("Two"), h.thread("Three")
	h.a.continueAll([]thread{one, two, three})
	for _, th := range []thread{one, two} {
		engineView(t, d, th.id(), "the continued run's end", func(v api.Thread) bool { return v.Run == api.RunIdle && len(queuedOn(t, d, th.id())) == 2 })
	}
	for _, th := range []thread{one, two} {
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: th.id(), Body: api.MessageDispatch{Text: "Once more.", Mode: api.StartNow}}); err != nil {
			t.Fatal(err)
		}
		engineView(t, d, th.id(), "a run after the continued one", func(v api.Thread) bool { return v.Run == api.RunIdle && len(queuedOn(t, d, th.id())) == 3 })
	}
	g.slow.Store(true)
	laters := map[api.ID]api.ID{}
	for _, th := range []thread{one, two} {
		later := api.NewID()
		laters[th.id()] = later
		for _, b := range []api.Intent{api.MessageDispatch{Text: "Again.", Mode: api.StartNow}, api.TurnInterrupt{}} {
			engineView(t, d, th.id(), "a run to steer", func(v api.Thread) bool {
				return v.Run == api.RunIdle || v.Turn != nil && v.Turn.Run == later && v.Run == api.RunRunning
			})
			id := api.NewID()
			if _, ok := b.(api.MessageDispatch); ok {
				id = later
			}
			if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: id, Target: th.id(), Body: b}); err != nil {
				t.Fatal(err)
			}
		}
		engineView(t, d, th.id(), "the later run told to stop", func(v api.Thread) bool {
			return v.Run == api.RunStopping && slices.ContainsFunc(eventsOn(t, d, th.id()), func(e api.Event) bool {
				b, _ := e.Decode()
				s, ok := b.(api.TurnSignalled)
				return ok && s.Run == later
			})
		})
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: th.id(), Body: api.TurnInterrupt{}}); err != nil {
			t.Fatal(err)
		}
		engineView(t, d, th.id(), "the later run uncertain", func(v api.Thread) bool { return v.Run == api.RunUncertain && v.Turn != nil && v.Turn.Run == later })
	}
	shown := func() bool {
		for _, th := range []thread{one, two} {
			if th.View.Run != api.RunUncertain || th.View.Turn == nil || th.View.Turn.Run != laters[th.id()] {
				return false
			}
		}
		return true
	}
	for deadline := time.After(10 * time.Second); !shown(); {
		if !h.p.run() {
			select {
			case <-h.p.wake:
			case <-deadline:
				t.Fatal("the app never saw the later runs uncertain")
			}
		}
	}
	h.tt.Frame()
	h.until("the third continued", func() bool { return len(queuedOn(t, d, three.id())) == 2 })
}

// lostReply loses the reply to every replace of target's run the engine
// took, once late closes.
type lostReply struct {
	api.Endpoint
	target api.ID
	late   chan struct{}
}

func (e lostReply) Submit(ctx context.Context, c api.Command) (api.Receipt, error) {
	r, err := e.Endpoint.Submit(ctx, c)
	if b, ok := c.Body.(api.MessageDispatch); err == nil && ok && b.Replaces != (api.ID{}) && c.Target == e.target {
		<-e.late
		return api.Receipt{}, errors.New("the reply was lost")
	}
	return r, err
}

// gatedShell holds back the shell's thread updates while hold is set.
type gatedShell struct {
	api.Endpoint
	hold *atomic.Bool
	wake chan struct{}
}

type gatedShellStream struct {
	api.Stream
	s gatedShell
}

func (e gatedShell) Open(ctx context.Context, q api.Subscription) (api.Stream, error) {
	st, err := e.Endpoint.Open(ctx, q)
	if err == nil && q.Thread.IsZero() {
		st = gatedShellStream{st, e}
	}
	return st, err
}

func (s gatedShellStream) Next(ctx context.Context) (api.StreamItem, error) {
	it, err := s.Stream.Next(ctx)
	for err == nil && it.Kind == api.StreamThread && s.s.hold.Load() {
		select {
		case <-s.s.wake:
		case <-ctx.Done():
			return api.StreamItem{}, ctx.Err()
		}
	}
	return it, err
}

// A Continue whose reply was lost holds its place, whether its run shows
// before the loss or after.
func TestContinueAllHoldsAPlaceWhoseReplyWasLost(t *testing.T) {
	for _, shows := range []string{"before", "after"} {
		t.Run("its run shows "+shows, func(t *testing.T) {
			held := fake.New(fake.Turn(false))
			held.HoldStart = true
			_, d := lostDir(t, held, "One", "Two", "Three")
			snap, err := d.Endpoint().Read(context.Background(), api.Query{})
			if err != nil {
				t.Fatal(err)
			}
			var lost api.ID
			for _, v := range snap.Threads {
				if v.Title == "One" {
					lost = v.ID
				}
			}
			late, hold, wake := make(chan struct{}), &atomic.Bool{}, make(chan struct{})
			h := serveApp(t, gatedShell{lostReply{d.Endpoint(), lost, late}, hold, wake})
			one, two, three := h.thread("One"), h.thread("Two"), h.thread("Three")
			placesHeld := func() {
				t.Helper()
				h.tt.Frame()
				if len(three.Pending) != 0 || len(queuedOn(t, d, three.id())) != 1 {
					t.Fatalf("the third was continued (%d sent, %d queued) with both places held", len(three.Pending), len(queuedOn(t, d, three.id())))
				}
			}
			if shows == "after" {
				hold.Store(true)
				close(late)
			}
			h.a.continueAll([]thread{one, two, three})
			if shows == "before" {
				h.until("the first two starting", func() bool { return one.View.Run == api.RunStarting && two.View.Run == api.RunStarting })
				close(late)
			}
			h.until("the first's reply lost", func() bool {
				return slices.ContainsFunc(one.Pending, func(p client.Pending) bool { return p.Err != nil })
			})
			if shows == "after" && one.View.Run != api.RunUncertain {
				t.Fatalf("the first shows %v as its reply is lost, want its uncertain run still", one.View.Run)
			}
			placesHeld()
			hold.Store(false)
			close(wake)
			h.until("the first two starting", func() bool { return one.View.Run == api.RunStarting && two.View.Run == api.RunStarting })
			placesHeld()
		})
	}
}

// switching serves whichever engine it holds, none while it holds none.
type switching struct{ current atomic.Pointer[daemon.Daemon] }

func (s *switching) ep() (api.Endpoint, error) {
	if d := s.current.Load(); d != nil {
		return d.Endpoint(), nil
	}
	return nil, errors.New("the engine is restarting")
}

func (s *switching) Info(ctx context.Context) (api.Environment, error) {
	e, err := s.ep()
	if err != nil {
		return api.Environment{}, err
	}
	return e.Info(ctx)
}

func (s *switching) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	e, err := s.ep()
	if err != nil {
		return api.Snapshot{}, err
	}
	return e.Read(ctx, q)
}

func (s *switching) Submit(ctx context.Context, c api.Command) (api.Receipt, error) {
	e, err := s.ep()
	if err != nil {
		return api.Receipt{}, err
	}
	return e.Submit(ctx, c)
}

func (s *switching) Open(ctx context.Context, q api.Subscription) (api.Stream, error) {
	e, err := s.ep()
	if err != nil {
		return nil, err
	}
	return e.Open(ctx, q)
}

func copyDB(t *testing.T, from, to string) {
	t.Helper()
	for _, ext := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(from + ext)
		if os.IsNotExist(err) {
			if err := os.Remove(to + ext); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to+ext, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// A Continue all asked of an engine whose database is then restored from a
// backup made before it is over: the restored log knows nothing of what
// it continued, so Continue all is offered again.
func TestARestoreEndsAContinueAll(t *testing.T) {
	held := fake.New(fake.Turn(false))
	held.HoldStart = true
	dir, d := lostDir(t, held, "One", "Two", "Three")
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	db, backup := filepath.Join(dir, "engine.db"), filepath.Join(dir, "backup.db")
	copyDB(t, db, backup)
	relay := &switching{}
	relay.current.Store(engine(t, daemon.Config{Dir: dir}, held))
	h := serveApp(t, relay)
	one, two, three := h.thread("One"), h.thread("Two"), h.thread("Three")
	h.a.continueAll([]thread{one, two, three})
	h.until("two continued", func() bool {
		return one.View.Run == api.RunStarting && two.View.Run == api.RunStarting && len(one.Pending)+len(two.Pending) == 0
	})
	old := relay.current.Swap(nil)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	h.until("the engine offline", func() bool { _, off := h.local().Status.(client.Offline); return off })
	copyDB(t, backup, db)
	relay.current.Store(engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false))))
	h.until("the restored engine online, the runs uncertain again", func() bool {
		return h.local().Status == client.Status(client.Online{}) && one.View.Run == api.RunUncertain && two.View.Run == api.RunUncertain
	})
	h.tt.Advance(time.Second)
	if len(h.a.continues) != 0 || len(queuedOn(t, relay.current.Load(), three.id())) != 1 {
		t.Fatalf("%d threads still held to continue, the third queued %d; want the old Continue all over", len(h.a.continues), len(queuedOn(t, relay.current.Load(), three.id())))
	}
	if _, ok := h.tt.Find("Continue all"); !ok {
		t.Fatalf("Continue all isn't offered on the restored threads: %q", h.tt.Texts())
	}
}
