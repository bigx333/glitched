package runtime_test

import (
	"context"
	"os"
	goruntime "runtime"
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

// waitFor waits for ch to close or be sent on.
func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// lostBatch is size threads whose runs a host killed as they started, on
// the database at path.
func lostBatch(t *testing.T, path string, size int) []api.ID {
	t.Helper()
	ad := held()
	n := start(t, path, ad)
	var ids []api.ID
	for range size {
		id := newThread(t, n.h)
		submit(t, n.h, id, dispatch("initial work", api.StartNow))
		ids = append(ids, id)
	}
	startsReach(t, ad, size)
	n.kill()
	return ids
}

func batchStates(t *testing.T, n node, ids []api.ID) (wait, lost []api.Thread) {
	t.Helper()
	for _, id := range ids {
		th := until(t, n.h, id, "read", func(api.Thread) bool { return true })
		switch {
		case waiting(th):
			wait = append(wait, th)
		case uncertain(th):
			lost = append(lost, th)
		}
	}
	return wait, lost
}

func twoSlotsTaken(t *testing.T, n node, ids []api.ID) (wait, lost []api.Thread) {
	t.Helper()
	until(t, n.h, ids[0], "two continued runs waiting and one waiting for a slot", func(api.Thread) bool {
		wait, lost = batchStates(t, n, ids)
		return len(wait) == 2 && len(lost) == 1
	})
	return wait, lost
}

func TestARunLeftForASlotIsContinuedAfterAnotherRestart(t *testing.T) {
	path := dbPath(t)
	ids := lostBatch(t, path, 3)
	first := fake.New(fake.Turn(true))
	n := autoHost(t, path, provider.Mux{"fake": first})
	_, lost := twoSlotsTaken(t, n, ids)
	pending := lost[0].ID
	closed := make(chan struct{})
	go func() { n.stop(); close(closed) }()
	waitFor(t, closed, "Close while the continuation worker is full")
	waitFor(t, n.h.LostContinued(), "continuation worker exit")

	second := fake.New(fake.Turn(true))
	n = autoHost(t, path, provider.Mux{"fake": second})
	waitFor(t, n.h.LostContinued(), "second boot's continuation decisions")
	got := until(t, n.h, pending, "read", func(api.Thread) bool { return true })
	if got.Run == api.RunUncertain {
		t.Fatalf("still-eligible third thread after the second boot: state=%v reason=%d new provider starts=%d; want the deferred continuation admitted", got.Run, lostFor(t, n.h, pending), second.Starts())
	}
}

func TestASlotFreesOnceTheContinuedRunEnds(t *testing.T) {
	path := dbPath(t)
	ids := lostBatch(t, path, 3)
	ad := fake.New(fake.Turn(true))
	n := autoHost(t, path, provider.Mux{"fake": ad})
	wait, lost := twoSlotsTaken(t, n, ids)
	finished, pending := wait[0], lost[0].ID
	next := submit(t, n.h, finished.ID, dispatch("ordinary follow-up", api.AfterActive)).Command
	respond(t, n.h, finished.ID, finished.Requests[0])
	until(t, n.h, finished.ID, "ordinary follow-up waiting", func(th api.Thread) bool {
		return waiting(th) && th.Requests[0].Run == next
	})
	until(t, n.h, pending, "the third continued as the unrelated follow-up waits", waiting)
	waitFor(t, n.h.LostContinued(), "every due run continued")
}

func TestRestoredContinuedRunsTakeTheirSlots(t *testing.T) {
	path := dbPath(t)
	ids := lostBatch(t, path, 2)
	kept, lost := fake.New(fake.Turn(true)), held()
	n := autoHost(t, path, provider.Mux{"fake": kept, "lost": lost})
	waitFor(t, n.h.LostContinued(), "first boot's continuation decisions")
	for _, id := range ids {
		until(t, n.h, id, "continued approval", waiting)
	}
	extra := threadOn(t, n.h, "lost")
	submit(t, n.h, extra, dispatch("new work", api.StartNow))
	startsReach(t, lost, 1)
	n.kill()

	replacement := fake.New(fake.Turn(true))
	n = autoHost(t, path, provider.Mux{"fake": kept, "lost": replacement})
	waitFor(t, n.h.ContinuesFull(), "the restored continued runs to take both slots")
	wait, left := batchStates(t, n, append(ids, extra))
	if len(wait) != 2 || len(left) != 1 || replacement.Starts() != 0 {
		t.Fatalf("second boot: %d waiting, %d uncertain, %d started; want 2 restored and 1 left for a slot", len(wait), len(left), replacement.Starts())
	}
}

func TestASnoozeBeforeTheContinueCommitsWins(t *testing.T) {
	path := dbPath(t)
	id := lostBatch(t, path, 1)[0]
	reached, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var reachedOnce sync.Once

	unblock := func() { once.Do(func() { close(proceed) }) }
	now := time.Now().UTC()
	clock := func() time.Time {
		var pcs [10]uintptr
		size := goruntime.Callers(2, pcs[:])
		frames := goruntime.CallersFrames(pcs[:size])
		for i := 0; i < 3; i++ {
			f, more := frames.Next()
			if strings.HasSuffix(f.Function, "(*Host).continueLost") {
				reachedOnce.Do(func() { close(reached) })
				<-proceed
				break
			}
			if !more {
				break
			}
		}
		return now
	}
	ad := fake.New(fake.Turn(true))
	n := serveOpts(t, path, provider.Mux{"fake": ad}, runtime.WithAutoContinue(true), runtime.WithClock(clock), runtime.WithIdleCompaction(false))
	defer unblock()
	waitFor(t, reached, "scheduler after its SQLite read")
	submit(t, n.h, id, api.InboxSet{Disposition: api.Disposition{State: api.Snoozed, Until: now.Add(time.Hour)}})
	unblock()
	waitFor(t, n.h.LostContinued(), "scheduler after the snooze committed")
	th := until(t, n.h, id, "read", func(api.Thread) bool { return true })
	if th.Run != api.RunUncertain || ad.Starts() != 0 {
		t.Fatalf("a snooze committed before auto-dispatch: disposition=%v run=%v admitted=%d; want snoozed and uncertain with only the original admission", th.Disposition.State, th.Run, count(events(t, n.h, id), "turn.admitted"))
	}
}

func TestTheChainCountsWhenEachContinueWasAskedFor(t *testing.T) {
	path := dbPath(t)
	id := lostBatch(t, path, 1)[0]
	start := time.Now().UTC()
	boot := func(min time.Duration) (node, *fake.Adapter) {
		ad := held()
		n := serveOpts(t, path, provider.Mux{"fake": ad}, runtime.WithAutoContinue(true), runtime.WithClock(func() time.Time {
			return start.Add(min * time.Minute)
		}), runtime.WithIdleCompaction(false))
		waitFor(t, n.h.LostContinued(), "continuation decision")
		return n, ad
	}
	for _, min := range []time.Duration{0, 4, 5} {
		n, ad := boot(min)
		startsReach(t, ad, 1)
		n.kill()
	}
	n, ad := boot(6)
	var requested, ended []time.Duration
	for _, e := range events(t, n.h, id) {
		b, _ := e.Decode()
		switch b := b.(type) {
		case api.MessageQueued:
			if b.Text == api.ContinueText {
				requested = append(requested, e.At.Sub(start))
			}
		case api.TurnEnded:
			ended = append(ended, e.At.Sub(start))
		}
	}
	if len(requested) != 4 {
		th := until(t, n.h, id, "read", func(api.Thread) bool { return true })
		t.Fatalf("at +6m: Continue requests=%v; abandonments=%v; state=%v starts=%d; want a new Continue (only +4m and +5m requests are in the last five minutes)", requested, ended, th.Run, ad.Starts())
	}
}

func TestAStoppedChildLostAtARestartReportsNothingMore(t *testing.T) {
	path := dbPath(t)
	old := deaf{fake.New(fake.Turn(true))}
	n := start(t, path, old)
	parent := newThread(t, n.h)
	pr := submit(t, n.h, parent, dispatch("parent", api.StartNow)).Command
	until(t, n.h, parent, "parent waiting", waiting)
	child := api.NewID()
	cr := submit(t, n.h, child, api.ThreadCreate{
		Parent: parent, Run: pr, Text: "child task",
		Title: "child", Workspace: "/src/app", Model: api.ModelRef{Provider: "fake", Model: "fake"},
	}).Command
	until(t, n.h, child, "child waiting", waiting)
	until(t, n.h, parent, "original request report", func(api.Thread) bool {
		return count(events(t, n.h, parent), "child.reported") == 1
	})
	submit(t, n.h, child, api.TurnInterrupt{})
	until(t, n.h, child, "Stop acknowledged but not ended", func(th api.Thread) bool {
		return th.Run == api.RunStopping && count(events(t, n.h, child), "turn.signalled") == 1
	})
	n.kill()

	n = autoHost(t, path, provider.Mux{"fake": fake.New(fake.Turn(true))})
	waitFor(t, n.h.LostContinued(), "restart decisions")
	db := sideDB(t, path)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		var pending int
		if err := db.QueryRow("SELECT count(*) FROM effects WHERE kind = 'child.report' AND state <> 'done'").Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child report effect did not drain")
		}
	}
	var reports []string
	for _, e := range events(t, n.h, parent) {
		b, _ := e.Decode()
		if q, ok := b.(api.ChildReported); ok && q.Run == cr && q.Kind == api.ReportBlocked {
			reports = append(reports, q.Outcome)
		}
	}
	if len(reports) != 1 {
		t.Fatalf("parent reports for child run after acknowledged Stop and restart: %q (child uncertain reason=%d); want only the original approval report", reports, lostFor(t, n.h, child))
	}
}

type failedReconcile struct{ *fake.Adapter }

func (failedReconcile) Reconcile(context.Context, provider.RuntimeIdentity) (provider.Recovery, error) {
	return nil, os.ErrPermission
}

func TestNoContinueForARewindAnUnconfirmedLossOrAnOldLog(t *testing.T) {
	for _, kind := range []string{"rewound", "unconfirmed", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			path := dbPath(t)
			id := lostBatch(t, path, 1)[0]
			ad := fake.New(fake.Turn(true))
			var adapter provider.Adapter = ad
			want := api.UncertainUnknown
			if kind == "unconfirmed" {
				adapter, want = failedReconcile{ad}, api.UncertainUnconfirmed
			} else {
				st, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "rewound" {
					err, want = st.Rewind(ctx), api.UncertainRewound
				} else {
					th, e := st.Thread(ctx, id)
					if e != nil {
						t.Fatal(e)
					}
					_, err = st.Observe(ctx, id, engine.Reconciled{Run: th.Live().ID}, time.Now(), 0)
				}
				if err != nil {
					t.Fatal(err)
				}
				st.Close()
				if kind == "legacy" {
					db := sideDB(t, path)
					exec(t, db, "UPDATE events SET payload = CAST(json_remove(CAST(payload AS TEXT), '$.reason') AS BLOB) WHERE type = 'turn.uncertain'")
				}
			}
			n := autoHost(t, path, provider.Mux{"fake": adapter})
			waitFor(t, n.h.LostContinued(), "boot's continuation decisions")
			v := until(t, n.h, id, "read", func(api.Thread) bool { return true })
			if v.Run != api.RunUncertain || ad.Starts() != 0 || lostFor(t, n.h, id) != want {
				t.Fatalf("%s: run=%v starts=%d reason=%d; want uncertain, no starts, reason=%d", kind, v.Run, ad.Starts(), lostFor(t, n.h, id), want)
			}
			assertReplay(t, n.st)
		})
	}
}

type interruptedBoot struct {
	*fake.Adapter
	order  []api.ID
	cancel context.CancelFunc
}

func (a *interruptedBoot) Reconcile(_ context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	a.order = append(a.order, id.Thread)
	if len(a.order) == 2 {
		a.cancel()
	}
	return provider.Lost{}, nil
}

func TestALossSurvivesABootCutShort(t *testing.T) {
	path := dbPath(t)
	lostBatch(t, path, 2)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bootCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cut := &interruptedBoot{Adapter: held(), cancel: cancel}
	h, err := runtime.New(bootCtx, st, provider.Mux{"fake": cut}, runtime.WithAutoContinue(true))
	if h != nil || err == nil || len(cut.order) != 2 {
		t.Fatalf("expected interrupted boot after two reconcile calls, host=%v err=%v calls=%d", h != nil, err, len(cut.order))
	}
	first, err := st.Thread(ctx, cut.order[0])
	if err != nil || first.Live().State != engine.RunUncertain {
		t.Fatalf("the first loss did not commit before interrupted boot: err=%v", err)
	}
	st.Close()

	ad := fake.New(fake.Turn(true))
	n := autoHost(t, path, provider.Mux{"fake": ad})
	waitFor(t, n.h.LostContinued(), "next boot's continuation decisions")
	until(t, n.h, cut.order[1], "second lost run's continued approval", waiting)
	got := until(t, n.h, cut.order[0], "read", func(api.Thread) bool { return true })
	if got.Run == api.RunUncertain {
		t.Fatalf("boot interrupted after persisting its first loss: first run state=%v reason=%d; next boot only starts=%d of 2 eligible continuations", got.Run, lostFor(t, n.h, cut.order[0]), ad.Starts())
	}
}

func TestContinuedRunsNotYetStartedTakeTheirSlots(t *testing.T) {
	path := dbPath(t)
	ids := lostBatch(t, path, 4)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	leases, err := st.Leased(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids[:2] {
		var lease store.Effect
		for _, e := range leases {
			if e.Thread == id {
				lease = e
				break
			}
		}
		start, ok := lease.Body.(engine.StartTurn)
		if !ok {
			t.Fatalf("fixture has no leased original start for thread %s", id)
		}
		if _, err := st.Observe(ctx, id, engine.Reconciled{Run: start.Run}, time.Now(), lease.ID); err != nil {
			t.Fatal(err)
		}
		submit(t, st2endpoint{st}, id, api.MessageDispatch{Text: api.ContinueText, Mode: api.RestartActive, Replaces: start.Run, Auto: true})
	}
	st.Close()

	ad := fake.New(fake.Turn(true))
	n := autoHost(t, path, provider.Mux{"fake": ad})
	waitFor(t, n.h.ContinuesFull(), "the continued runs not yet started to take both slots")
	for _, id := range ids[:2] {
		until(t, n.h, id, "carried-over continue's approval", waiting)
	}
	if _, lost := batchStates(t, n, ids); len(lost) != 2 || ad.Starts() != 2 {
		t.Fatalf("second boot: %d uncertain, %d started; want 2 left for a slot and only the 2 carried over started", len(lost), ad.Starts())
	}
}

func TestAContinueTheUserSentFirstWins(t *testing.T) {
	path := dbPath(t)
	id := lostBatch(t, path, 1)[0]
	reached, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(proceed) }) }
	clock := func() time.Time {
		var pcs [10]uintptr
		size := goruntime.Callers(2, pcs[:])
		frames := goruntime.CallersFrames(pcs[:size])
		for i := 0; i < 3; i++ {
			f, more := frames.Next()
			if strings.HasSuffix(f.Function, "(*Host).continueLost") {
				select {
				case <-reached:
				default:
					close(reached)
				}
				<-proceed
				break
			}
			if !more {
				break
			}
		}
		return time.Now()
	}
	ad := fake.New(fake.Turn(true))
	n := serveOpts(t, path, provider.Mux{"fake": ad}, runtime.WithAutoContinue(true), runtime.WithClock(clock), runtime.WithIdleCompaction(false))
	defer unblock()
	waitFor(t, reached, "scheduler after its SQLite read")
	th := until(t, n.h, id, "uncertain before the manual continue", uncertain)
	manual := submit(t, n.h, id, api.MessageDispatch{Text: api.ContinueText, Mode: api.RestartActive, Replaces: th.Turn.Run})
	unblock()
	waitFor(t, n.h.LostContinued(), "scheduler after the manual dispatch")
	continued := until(t, n.h, id, "manual continuation waiting", waiting)
	if got := count(events(t, n.h, id), "turn.admitted"); got != 2 || continued.Turn.Run != manual.Command || ad.Starts() != 1 {
		t.Fatalf("manual-vs-auto race: admissions=%d starts=%d, got the manual run=%v", got, ad.Starts(), continued.Turn.Run == manual.Command)
	}
	assertReplay(t, n.st)
}

func TestChildrenStoppedAndSettledRunsAreNotContinued(t *testing.T) {
	path := dbPath(t)
	working := held()
	old := deaf{fake.New(fake.Turn(true))}
	n := serve(t, path, provider.Mux{"fake": working, "old": old})
	parent := threadOn(t, n.h, "old")
	parentRun := submit(t, n.h, parent, dispatch("parent", api.StartNow)).Command
	until(t, n.h, parent, "parent waiting", waiting)
	child := api.NewID()
	childRun := submit(t, n.h, child, api.ThreadCreate{
		Parent: parent, Run: parentRun, Text: "child task",
		Title: "child", Workspace: "/src/app", Model: api.ModelRef{Provider: "fake", Model: "fake"},
	}).Command
	startsReach(t, working, 1)
	stopped := threadOn(t, n.h, "old")
	settled := threadOn(t, n.h, "old")
	for _, id := range []api.ID{stopped, settled} {
		submit(t, n.h, id, dispatch("initial", api.StartNow))
		until(t, n.h, id, "top-level approval", waiting)
	}
	submit(t, n.h, stopped, api.TurnInterrupt{})
	submit(t, n.h, settled, api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	for _, id := range []api.ID{stopped, settled} {
		until(t, n.h, id, "acknowledged stop", func(th api.Thread) bool {
			return th.Run == api.RunStopping && count(events(t, n.h, id), "turn.signalled") == 1
		})
	}
	n.kill()
	for i := range 2 {
		ad := fake.New(fake.Turn(true))
		n = autoHost(t, path, provider.Mux{"fake": ad, "old": ad})
		waitFor(t, n.h.LostContinued(), "continuation decisions")
		for _, id := range []api.ID{child, stopped, settled, parent} {
			got := until(t, n.h, id, "read", func(api.Thread) bool { return true })
			if got.Run != api.RunUncertain || ad.Starts() != 0 || count(events(t, n.h, id), "turn.admitted") != 1 {
				t.Fatalf("boot %d: ineligible thread %s run=%v, adapter starts=%d; want uncertain and no new starts", i+1, id, got.Run, ad.Starts())
			}
		}
		until(t, n.h, parent, "blocked report", func(api.Thread) bool {
			return count(events(t, n.h, parent), "child.reported") > 0
		})
		evs := events(t, n.h, parent)
		var reports []api.ChildReported
		for _, e := range evs {
			b, _ := e.Decode()
			if r, ok := b.(api.ChildReported); ok && r.Run == childRun {
				reports = append(reports, r)
			}
		}
		if len(reports) != 1 || reports[0].Kind != api.ReportBlocked {
			t.Fatalf("boot %d: reports=%+v, want exactly one blocked report", i+1, reports)
		}
		assertReplay(t, n.st)
		n.kill()
	}
}
