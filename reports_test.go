package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// script is an engine the test plays, so that it decides the order in which
// views reach the app: the shell sends the views it is shown, after a
// snapshot of the views as they last were.
type script struct {
	views chan api.Thread
	cut   chan struct{} // a send ends the shell stream open then

	mu    sync.Mutex
	log   api.ID
	host  api.ID
	shell []api.Thread
	seq   uint64
	at    time.Time
	opens int // shell streams opened
}

// play serves a script on the socket the app in dir attaches to, its
// shell starting from views, and starts the app.
func play(t *testing.T, views ...api.Thread) (*script, *harness) {
	t.Helper()
	s := &script{
		views: make(chan api.Thread, 16), cut: make(chan struct{}, 1),
		log: api.NewID(), host: api.NewID(), shell: views, at: time.Unix(1000, 0),
	}
	return s, serveApp(t, s)
}

// serveApp serves ep on the socket of a new state directory, and starts the
// app there.
func serveApp(t *testing.T, ep api.Endpoint) *harness {
	t.Helper()
	dir := tempDir(t)
	serveOn(t, daemon.Socket(dir), ep)
	return start(t, dir)
}

// serveOn serves ep on tr until the test ends, and is where it listens.
func serveOn(t *testing.T, tr transport.Transport, ep api.Endpoint) string {
	t.Helper()
	ln, err := tr.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (&transport.Server{Transport: tr, Endpoint: ep}).Serve(ctx, ln) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return ln.Addr().String()
}

// view is a thread of the script's: under parent unless that is zero.
func view(title string, parent api.ID) api.Thread {
	return api.Thread{ID: api.NewID(), Workspace: "/work/t3code", Title: title, Model: fakeModel, Parent: parent}
}

// show sends v on the shell, as a commit that moved it would.
func (s *script) show(v api.Thread) { s.views <- v }

// report commits a report of child's to parent, a second after the last,
// and shows parent's view after it.
func (s *script) report(parent *api.Thread, child api.ID, kind api.ReportKind) {
	s.mu.Lock()
	s.at = s.at.Add(time.Second)
	at := s.at
	s.mu.Unlock()
	s.commit(parent, child, kind, at)
	s.show(*parent)
}

// commit logs a report of child's to parent at at, as an engine commits
// one, without showing parent's view. The shell's snapshot keeps the view
// current for a reconnect.
func (s *script) commit(parent *api.Thread, child api.ID, kind api.ReportKind, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	parent.LastReport = &api.Report{Child: child, Kind: kind, Cursor: api.Cursor{Log: s.log, Seq: s.seq}}
	parent.Reported, parent.UpdatedAt = at, at
	parent.Revision++
	s.keep(*parent)
}

// keep makes v the shell snapshot's view of its thread.
func (s *script) keep(v api.Thread) {
	for i := range s.shell {
		if s.shell[i].ID == v.ID {
			s.shell[i] = v
		}
	}
}

// restore stands for the engine's database put back from a copy: a new
// log, its views as they are in the copy.
func (s *script) restore(views ...api.Thread) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = api.NewID()
	for _, v := range views {
		s.keep(v)
	}
}

func (s *script) streams() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opens
}

func (s *script) Info(context.Context) (api.Environment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return api.Environment{ID: s.host, Name: "script", Protocol: api.Protocol, Log: s.log}, nil
}

func (s *script) Read(context.Context, api.Query) (api.Snapshot, error) {
	return api.Snapshot{}, errors.New("script: reads are not played")
}

func (s *script) Submit(context.Context, api.Command) (api.Receipt, error) {
	return api.Receipt{}, errors.New("script: commands are not played")
}

func (s *script) Open(_ context.Context, sub api.Subscription) (api.Stream, error) {
	if !sub.Thread.IsZero() {
		return idle{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens++
	snap := api.Snapshot{Cursor: api.Cursor{Log: s.log, Seq: s.seq}, Threads: slices.Clone(s.shell)}
	return &shellStream{s, &snap}, nil
}

type shellStream struct {
	s    *script
	snap *api.Snapshot
}

func (st *shellStream) Next(ctx context.Context) (api.StreamItem, error) {
	if snap := st.snap; snap != nil {
		st.snap = nil
		return api.StreamItem{Kind: api.StreamSnapshot, Snapshot: snap}, nil
	}
	select {
	case v := <-st.s.views:
		return api.StreamItem{Kind: api.StreamThread, Thread: &v}, nil
	case <-st.s.cut:
		return api.StreamItem{}, errors.New("script: the connection was cut")
	case <-ctx.Done():
		return api.StreamItem{}, ctx.Err()
	}
}

func (*shellStream) Close() error { return nil }

// idle is a thread's subscription that sends nothing.
type idle struct{}

func (idle) Next(ctx context.Context) (api.StreamItem, error) {
	<-ctx.Done()
	return api.StreamItem{}, ctx.Err()
}

func (idle) Close() error { return nil }

// told is the bodies of the notifications on parent so far.
func told(h *harness, parent api.ID) *[]string {
	got := new([]string)
	h.a.notify = func(o mygo.NotificationOptions) {
		if o.ID == "thread:"+parent.String() {
			*got = append(*got, o.Body)
		}
	}
	return got
}

// shown waits until the app holds v's revision, or holds no v once v is
// deleted.
func shown(h *harness, v api.Thread) {
	h.t.Helper()
	h.until(v.Title+"'s view", func() bool {
		th := h.local().Threads[v.ID]
		if v.Deleted {
			return th == nil
		}
		return th != nil && th.View.Revision == v.Revision
	})
}

// reconnect cuts the shell stream and waits for the app to be back on a
// new one, with its snapshot taken in: the post that takes it tells of
// its reports too.
func reconnect(h *harness, s *script) {
	h.t.Helper()
	n := s.streams()
	s.cut <- struct{}{}
	h.until("the shell's reconnect", func() bool {
		return s.streams() > n && h.local().Status == client.Status(client.Online{})
	})
}

// A child that asked before the app started still has its Blocked report
// told of: the app learns what the report says, not what was pending when
// it first saw the child.
func TestABlockedReportOfAChildThatAskedBeforeTheAppStarted(t *testing.T) {
	parent := view("Ship the sidebar sort", api.ID{})
	kid := view("Write the tests", parent.ID)
	kid.Requests = []api.Request{{ID: api.NewID(), Kind: "approval"}}
	s, h := play(t, parent, kid)
	h.thread("Write the tests")
	got := told(h, parent.ID)
	s.report(&parent, kid.ID, api.ReportBlocked)
	h.until("the Blocked report's notification", func() bool { return len(*got) > 0 })
	if !slices.Equal(*got, []string{"Write the tests: Needs you"}) {
		t.Fatalf("notifications on the parent: %q; want the child that asked", *got)
	}
}

// Two children asking before either reports are each named by their own
// report, not both by the one that asked last.
func TestOverlappingAsksAreEachToldOfByTheirReport(t *testing.T) {
	parent := view("Ship the sidebar sort", api.ID{})
	a, b := view("Write the tests", parent.ID), view("Run the linter", parent.ID)
	s, h := play(t, parent, a, b)
	shownA, shownB := h.thread("Write the tests"), h.thread("Run the linter")
	got := told(h, parent.ID)
	a.Requests, a.UpdatedAt = []api.Request{{ID: api.NewID(), Kind: "approval"}}, time.Unix(1001, 0)
	s.show(a)
	b.Requests, b.UpdatedAt = []api.Request{{ID: api.NewID(), Kind: "approval"}}, time.Unix(1002, 0)
	s.show(b)
	h.until("both asks", func() bool {
		return len(shownA.View.Requests) == 1 && len(shownB.View.Requests) == 1
	})
	s.report(&parent, a.ID, api.ReportBlocked)
	h.until("the first report's notification", func() bool { return len(*got) == 1 })
	s.report(&parent, b.ID, api.ReportBlocked)
	h.until("the second report's notification", func() bool { return len(*got) == 2 })
	if want := []string{"Write the tests: Needs you", "Run the linter: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want %q", *got, want)
	}
}

// A Done report that reaches the parent between a sibling's ask and that
// sibling's own report tells of nothing: only the sibling's report does.
// This is issue #14.
func TestADoneReportRacingASiblingsAskTellsNothing(t *testing.T) {
	parent := view("Ship the sidebar sort", api.ID{})
	a, b := view("Write the tests", parent.ID), view("Run the linter", parent.ID)
	s, h := play(t, parent, a, b)
	asker := h.thread("Write the tests")
	got := told(h, parent.ID)
	a.Requests, a.UpdatedAt = []api.Request{{ID: api.NewID(), Kind: "approval"}}, time.Unix(1001, 0)
	s.show(a)
	h.until("the ask", func() bool { return len(asker.View.Requests) == 1 })
	s.report(&parent, b.ID, api.ReportDone)
	shown(h, parent)
	if len(*got) != 0 {
		t.Fatalf("the Done report told %q; want nothing", *got)
	}
	s.report(&parent, a.ID, api.ReportBlocked)
	h.until("the Blocked report's notification", func() bool { return len(*got) > 0 })
	if !slices.Equal(*got, []string{"Write the tests: Needs you"}) {
		t.Fatalf("notifications on the parent: %q; want only the asker's own report", *got)
	}
}

// A child asks, is answered and asks again: each Blocked report is told
// of, and a Done one after, or a grandchild's report to its own parent,
// tells nothing on this one.
func TestAskAnswerAskThenDoneTellsEachAskOnce(t *testing.T) {
	parent := view("Parent", api.ID{})
	kid := view("A", parent.ID)
	grand := view("Grandchild", kid.ID)
	s, h := play(t, parent, kid, grand)
	h.thread("Grandchild")
	got := told(h, parent.ID)
	for range 2 {
		s.report(&parent, kid.ID, api.ReportBlocked)
	}
	s.report(&parent, kid.ID, api.ReportDone)
	s.report(&kid, grand.ID, api.ReportBlocked)
	shown(h, parent)
	shown(h, kid)
	if want := []string{"A: Needs you", "A: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want %q", *got, want)
	}
}

// Two children of a real engine asking at once are each named, once each.
func TestTwoChildrenAskingAtOnceAreEachToldOf(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New([]provider.Event{provider.RequestOpened{ID: "ask", Kind: "approval"}}))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kids := []api.ID{seedChild(t, d, top, "Write the tests"), seedChild(t, d, top, "Run the linter")}
	h := start(t, dir)
	h.thread("Write the tests")
	h.thread("Run the linter")
	got := told(h, top)
	var wg sync.WaitGroup
	for _, id := range kids {
		wg.Go(func() { dispatch(t, d, id) })
	}
	wg.Wait()
	h.until("both reports' notifications", func() bool { return len(*got) >= 2 })
	slices.Sort(*got)
	if want := []string{"Run the linter: Needs you", "Write the tests: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want one for each child", *got)
	}
}

// dispatch starts a turn on a thread of d's.
func dispatch(t *testing.T, d *daemon.Daemon, thread api.ID) {
	t.Helper()
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "go"}}); err != nil {
		t.Error(err)
	}
}

// Two reports logged at one time are each told of, in the log's order.
func TestReportsAtOneTimeAreEachToldOf(t *testing.T) {
	parent := view("Parent", api.ID{})
	a, b := view("A", parent.ID), view("B", parent.ID)
	s, h := play(t, parent, a, b)
	h.thread("B")
	got := told(h, parent.ID)
	at := time.Unix(1001, 0)
	s.commit(&parent, a.ID, api.ReportBlocked, at)
	s.show(parent)
	s.commit(&parent, b.ID, api.ReportBlocked, at)
	s.show(parent)
	h.until("the reports' notifications", func() bool { return len(*got) == 2 })
	if want := []string{"A: Needs you", "B: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want %q", *got, want)
	}
}

// A Done report logged at a Blocked report's time hides nothing.
func TestADoneReportAtABlockedOnesTimeHidesNothing(t *testing.T) {
	parent := view("Parent", api.ID{})
	a, b := view("Asker", parent.ID), view("Finisher", parent.ID)
	s, h := play(t, parent, a, b)
	h.thread("Finisher")
	got := told(h, parent.ID)
	at := time.Unix(1001, 0)
	s.commit(&parent, a.ID, api.ReportBlocked, at)
	s.show(parent)
	s.commit(&parent, b.ID, api.ReportDone, at)
	s.show(parent)
	shown(h, parent)
	if want := []string{"Asker: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want %q", *got, want)
	}
}

// The engine stamps a report before it takes the log's lock, so a report can
// land after one stamped later. Both are told of.
func TestAReportStampedBeforeTheOneAheadIsToldOf(t *testing.T) {
	parent := view("Parent", api.ID{})
	a, b := view("A", parent.ID), view("B", parent.ID)
	s, h := play(t, parent, a, b)
	h.thread("B")
	got := told(h, parent.ID)
	s.commit(&parent, a.ID, api.ReportBlocked, time.Unix(1002, 0))
	s.show(parent)
	h.until("A's notification", func() bool { return len(*got) == 1 })
	s.commit(&parent, b.ID, api.ReportBlocked, time.Unix(1001, 0))
	s.show(parent)
	h.until("B's notification", func() bool { return len(*got) == 2 })
	if want := []string{"A: Needs you", "B: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want %q", *got, want)
	}
}

// A report made while the shell was cut off is told of once it reconnects,
// from the snapshot alone, and a later reconnect does not tell it again.
func TestAReportMadeWhileDisconnectedIsToldOfOnReconnect(t *testing.T) {
	parent := view("Parent", api.ID{})
	kid := view("Asker", parent.ID)
	s, h := play(t, parent, kid)
	h.thread("Asker")
	got := told(h, parent.ID)
	n := s.streams()
	s.cut <- struct{}{}
	h.until("the disconnect", func() bool {
		_, off := h.local().Status.(client.Offline)
		return off
	})
	s.commit(&parent, kid.ID, api.ReportBlocked, time.Unix(1001, 0))
	h.until("the reconnect's notification", func() bool { return len(*got) > 0 })
	if s.streams() != n+1 {
		t.Fatalf("%d shell streams opened since the cut; want 1", s.streams()-n)
	}
	reconnect(h, s)
	if want := []string{"Asker: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want %q", *got, want)
	}
}

// A database put back from a copy comes with a new log: what its views
// report is not told of, as the app can't tell it from what it told
// already. The next report on the new log is.
func TestARestoredLogTellsOnlyReportsMadeAfter(t *testing.T) {
	parent := view("Parent", api.ID{})
	a, b := view("A", parent.ID), view("B", parent.ID)
	s, h := play(t, parent, a, b)
	h.thread("B")
	got := told(h, parent.ID)
	s.report(&parent, a.ID, api.ReportBlocked)
	h.until("A's notification", func() bool { return len(*got) == 1 })
	copied := parent
	s.commit(&copied, b.ID, api.ReportBlocked, time.Unix(1002, 0))
	s.restore(copied)
	reconnect(h, s)
	shown(h, copied)
	if want := []string{"A: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("after the restore: %q; want %q", *got, want)
	}
	s.report(&copied, b.ID, api.ReportBlocked)
	h.until("B's notification", func() bool { return len(*got) == 2 })
	if want := []string{"A: Needs you", "B: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want %q", *got, want)
	}
}

// A report to a parent deleted before the app sees either tells nothing:
// the snapshot it reconnects to has no parent to tell of.
func TestAReportToAParentDeletedMeanwhileTellsNothing(t *testing.T) {
	parent := view("Parent", api.ID{})
	kid := view("Asker", parent.ID)
	s, h := play(t, parent, kid)
	h.thread("Asker")
	got := told(h, parent.ID)
	s.cut <- struct{}{}
	h.until("the disconnect", func() bool {
		_, off := h.local().Status.(client.Offline)
		return off
	})
	s.commit(&parent, kid.ID, api.ReportBlocked, time.Unix(1001, 0))
	s.mu.Lock()
	s.shell = slices.DeleteFunc(s.shell, func(v api.Thread) bool { return v.ID == parent.ID })
	s.mu.Unlock()
	h.until("the reconnect without the parent", func() bool { return h.local().Threads[parent.ID] == nil })
	if len(*got) != 0 {
		t.Fatalf("notifications on the deleted parent: %q; want none", *got)
	}
}

// The real engine reports nothing to a deleted parent, so its child asking
// tells nothing on it.
func TestAChildOfADeletedParentAskingTellsNothing(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New([]provider.Event{provider.RequestOpened{ID: "ask", Kind: "approval"}}))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	h := start(t, dir)
	child := h.thread("Write the tests")
	got := told(h, top)
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: top, Body: api.ThreadDelete{}}); err != nil {
		t.Fatal(err)
	}
	h.until("the parent's delete", func() bool { return h.local().Threads[top] == nil })
	dispatch(t, d, kid)
	h.until("the child's ask", func() bool { return len(child.View.Requests) == 1 })
	h.settle(func() bool { return len(*got) > 0 })
	if len(*got) != 0 {
		t.Fatalf("notifications on the deleted parent: %q; want none", *got)
	}
}

// Pairs of children of a real engine that ask at once are each told of,
// whatever times the engine stamps their reports with.
func TestPairsOfChildrenAskingAtOnceAreEachToldOf(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New([]provider.Event{provider.RequestOpened{ID: "ask", Kind: "approval"}}))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	pairs := make([][2]api.ID, 30)
	for i := range pairs {
		pairs[i] = [2]api.ID{seedChild(t, d, top, "Write the tests"), seedChild(t, d, top, "Run the linter")}
	}
	h := start(t, dir)
	h.thread("Ship the sidebar sort")
	got := told(h, top)
	for i, pair := range pairs {
		var wg sync.WaitGroup
		for _, id := range pair {
			wg.Go(func() { dispatch(t, d, id) })
		}
		wg.Wait()
		h.until(fmt.Sprint("pair ", i, "'s notifications"), func() bool { return len(*got) >= 2*(i+1) })
	}
	if len(*got) != 2*len(pairs) {
		t.Fatalf("%d notifications on the parent; want %d, one for each child", len(*got), 2*len(pairs))
	}
}

// A child of a remote engine that asks, is answered and asks again is told
// of twice, with the parent never opened.
func TestARemoteChildAskingTwiceIsToldOfTwice(t *testing.T) {
	rdir := tempDir(t)
	token, err := daemon.Pair(rdir, "remote")
	if err != nil {
		t.Fatal(err)
	}
	d := engine(t, daemon.Config{Dir: rdir, Listen: []string{"127.0.0.1:0"}}, fake.New([]provider.Event{
		provider.RequestOpened{ID: "ask-1", Kind: "approval"},
		provider.RequestOpened{ID: "ask-2", Kind: "approval"},
	}))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	dir := tempDir(t)
	engine(t, daemon.Config{Dir: dir}, fake.New(nil))
	h := start(t, dir)
	if err := h.addServer(client.Server{Label: "remote", Addr: d.Addrs()[0].String(), Token: token}); err != nil {
		t.Fatal(err)
	}
	parent := h.thread("Ship the sidebar sort")
	child := h.thread("Write the tests")
	got := told(h, top)
	dispatch(t, d, kid)
	for n := 1; n <= 2; n++ {
		h.until(fmt.Sprint("ask ", n, "'s notification"), func() bool { return len(*got) == n && len(child.View.Requests) == 1 })
		r := child.View.Requests[0]
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: kid, Body: api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: []byte(`{"allow":true}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	h.until("the child's turn's end", func() bool {
		return child.View.LastTurn != nil && child.View.Run == api.RunIdle && len(child.View.Requests) == 0
	})
	if want := []string{"Write the tests: Needs you", "Write the tests: Needs you"}; parent.Transcript != nil || !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want %q, with the parent never opened", *got, want)
	}
}

// paused is a real engine whose shell stream stops after the first view
// of parent that carries a report, until resume closes: the app falls
// behind the log while children are created and report.
type paused struct {
	api.Endpoint
	parent api.ID
	resume chan struct{}
}

func (p *paused) Open(ctx context.Context, sub api.Subscription) (api.Stream, error) {
	st, err := p.Endpoint.Open(ctx, sub)
	if err != nil || !sub.Thread.IsZero() {
		return st, err
	}
	return &pausedStream{Stream: st, p: p}, nil
}

type pausedStream struct {
	api.Stream
	p    *paused
	held bool
}

func (s *pausedStream) Next(ctx context.Context) (api.StreamItem, error) {
	if s.held {
		select {
		case <-s.p.resume:
		case <-ctx.Done():
			return api.StreamItem{}, ctx.Err()
		}
	}
	it, err := s.Stream.Next(ctx)
	if it.Thread != nil && it.Thread.ID == s.p.parent && it.Thread.LastReport != nil {
		s.held = true
	}
	return it, err
}

// reports is how many child.reported events thread's log holds.
func reports(t *testing.T, d *daemon.Daemon, thread api.ID) int {
	t.Helper()
	snap, err := d.Endpoint().Read(context.Background(), api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range snap.Events {
		if e.Type == api.EventType(api.ChildReported{}) {
			n++
		}
	}
	return n
}

// awaitReports waits until thread's log holds n reports.
func awaitReports(t *testing.T, d *daemon.Daemon, thread api.ID, n int) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); reports(t, d, thread) < n; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the log never held %d reports", n)
		}
	}
}

// A child created while the app's shell lags behind the log has its report
// told of once the shell catches up, as do those before and after it.
func TestAReportOfAChildCreatedWhileTheShellLagsIsToldOf(t *testing.T) {
	d := engine(t, daemon.Config{Dir: tempDir(t)}, fake.New([]provider.Event{provider.RequestOpened{ID: "ask", Kind: "approval"}}))
	p := seed(t, d, "/work/t3code", "Parent")
	a := seedChild(t, d, p, "A")
	ep := &paused{Endpoint: d.Endpoint(), parent: p, resume: make(chan struct{})}
	h := serveApp(t, ep)
	h.thread("A")
	got := told(h, p)
	dispatch(t, d, a)
	h.until("A's notification", func() bool { return len(*got) == 1 })
	b := seedChild(t, d, p, "B")
	dispatch(t, d, b)
	awaitReports(t, d, p, 2)
	if h.p.run(); h.local().Threads[b] != nil {
		t.Fatal("the app holds B's view; this test needs the shell behind the log")
	}
	close(ep.resume)
	h.until("B's notification", func() bool { return len(*got) == 2 })
	c := seedChild(t, d, p, "C")
	h.thread("C")
	dispatch(t, d, c)
	h.until("C's notification", func() bool { return len(*got) == 3 })
	if want := []string{"A: Needs you", "B: Needs you", "C: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want %q", *got, want)
	}
}

// notifyReported tells only of a Blocked report, with notifications on,
// of a child the app holds.
func TestOnlyABlockedReportOfAHeldChildNotifies(t *testing.T) {
	p := view("Parent", api.ID{})
	a := view("A", p.ID)
	_, h := play(t, p, a)
	h.thread("A")
	got := told(h, p.ID)
	e := h.local()
	h.a.settings.notifications = false
	h.a.notifyReported(e, p.ID, api.Report{Child: a.ID, Kind: api.ReportBlocked})
	h.a.settings.notifications = true
	h.a.notifyReported(e, p.ID, api.Report{Child: a.ID, Kind: api.ReportDone})
	h.a.notifyReported(e, p.ID, api.Report{Child: a.ID, Kind: api.ReportFailed})
	h.a.notifyReported(e, p.ID, api.Report{Child: api.NewID(), Kind: api.ReportBlocked})
	if len(*got) != 0 {
		t.Fatalf("notifications: %q; want none", *got)
	}
	h.a.notifyReported(e, p.ID, api.Report{Child: a.ID, Kind: api.ReportBlocked})
	if want := []string{"A: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications: %q; want %q", *got, want)
	}
}
