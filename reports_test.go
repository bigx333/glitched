package main

import (
	"context"
	"encoding/json"
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
// views and reports reach the app: the shell sends the views it is shown,
// and a read of a thread returns the events reported to it.
type script struct {
	log, host api.ID
	views     chan api.Thread

	mu     sync.Mutex
	shell  []api.Thread
	events map[api.ID][]api.Event
	seq    uint64
	at     time.Time
}

// play serves a script on the socket the app in dir attaches to, its
// shell starting from views, and starts the app.
func play(t *testing.T, views ...api.Thread) (*script, *harness) {
	t.Helper()
	s := newScript(views...)
	return s, serveApp(t, s)
}

func newScript(views ...api.Thread) *script {
	return &script{
		log: api.NewID(), host: api.NewID(), views: make(chan api.Thread, 16),
		shell: views, events: map[api.ID][]api.Event{}, at: time.Unix(1000, 0),
	}
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

// commit appends a report of child's to parent at at, as an engine commits
// one, without showing parent's view. The shell's snapshot keeps the view
// current for a reconnect.
func (s *script) commit(parent *api.Thread, child api.ID, kind api.ReportKind, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	payload, _ := json.Marshal(api.ChildReported{Child: child, Run: api.NewID(), Kind: kind, Title: "child"})
	s.events[parent.ID] = append(s.events[parent.ID], api.Event{
		Cursor: api.Cursor{Log: s.log, Seq: s.seq}, Thread: parent.ID,
		Type: api.EventType(api.ChildReported{}), At: at, Payload: payload,
	})
	parent.Reported, parent.UpdatedAt = at, at
	parent.Revision++
	for i := range s.shell {
		if s.shell[i].ID == parent.ID {
			s.shell[i] = *parent
		}
	}
}

func (s *script) Info(context.Context) (api.Environment, error) {
	return api.Environment{ID: s.host, Name: "script", Protocol: api.Protocol, Log: s.log}, nil
}

func (s *script) Read(_ context.Context, q api.Query) (api.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := api.Snapshot{Cursor: api.Cursor{Log: s.log, Seq: s.seq}}
	for _, e := range s.events[q.Thread] {
		if q.After.Log != s.log || e.Cursor.Seq > q.After.Seq {
			snap.Events = append(snap.Events, e)
		}
	}
	return snap, nil
}

func (s *script) Submit(context.Context, api.Command) (api.Receipt, error) {
	return api.Receipt{}, errors.New("script: commands are not played")
}

func (s *script) Open(_ context.Context, sub api.Subscription) (api.Stream, error) {
	if !sub.Thread.IsZero() {
		return idle{}, nil
	}
	s.mu.Lock()
	snap := api.Snapshot{Cursor: api.Cursor{Log: s.log, Seq: s.seq}, Threads: slices.Clone(s.shell)}
	s.mu.Unlock()
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
	shown := h.thread("Ship the sidebar sort")
	h.until("the Done report", func() bool { return shown.View.Reported.Equal(parent.Reported) })
	if len(*got) != 0 {
		t.Fatalf("the Done report told %q; want nothing", *got)
	}
	s.report(&parent, a.ID, api.ReportBlocked)
	h.until("the Blocked report's notification", func() bool { return len(*got) > 0 })
	if !slices.Equal(*got, []string{"Write the tests: Needs you"}) {
		t.Fatalf("notifications on the parent: %q; want only the asker's own report", *got)
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
		wg.Go(func() {
			if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "go"}}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	h.until("both reports' notifications", func() bool { return len(*got) >= 2 })
	slices.Sort(*got)
	if want := []string{"Run the linter: Needs you", "Write the tests: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want one for each child", *got)
	}
}

// holding is an endpoint whose reads of a thread each wait for the test to
// answer them. A read takes the log as it is when the read arrives or, late,
// once it is answered. A send on cut ends the shell stream open then.
type holding struct {
	api.Endpoint
	reads chan *held
	late  bool
	cut   chan struct{}
}

// held is a read waiting on the test: an error fails it, nil lets it return.
type held struct {
	ctx    context.Context
	answer chan error
}

func hold(ep api.Endpoint) *holding {
	return &holding{Endpoint: ep, reads: make(chan *held, 16), cut: make(chan struct{}, 1)}
}

// playHeld is play with each read of a thread held for the test.
func playHeld(t *testing.T, late bool, views ...api.Thread) (*script, *holding, *harness) {
	t.Helper()
	s := newScript(views...)
	g := hold(s)
	g.late = late
	return s, g, serveApp(t, g)
}

func (g *holding) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	var snap api.Snapshot
	var err error
	if !g.late {
		snap, err = g.Endpoint.Read(ctx, q)
	}
	r := &held{ctx: ctx, answer: make(chan error, 1)}
	select {
	case g.reads <- r:
	case <-ctx.Done():
		return api.Snapshot{}, ctx.Err()
	}
	select {
	case failed := <-r.answer:
		if failed != nil {
			return api.Snapshot{}, failed
		}
	case <-ctx.Done():
		return api.Snapshot{}, ctx.Err()
	}
	if g.late {
		snap, err = g.Endpoint.Read(ctx, q)
	}
	return snap, err
}

func (g *holding) Open(ctx context.Context, sub api.Subscription) (api.Stream, error) {
	st, err := g.Endpoint.Open(ctx, sub)
	if err != nil || !sub.Thread.IsZero() {
		return st, err
	}
	return cuttable{st, g.cut}, nil
}

// next runs what the manager posts until the app reads a thread, and is
// that read for the test to answer.
func (g *holding) next(h *harness) *held {
	h.t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		if h.p.run() {
			h.tt.Frame()
		}
		select {
		case r := <-g.reads:
			return r
		case <-h.p.wake:
		case <-deadline:
			h.t.Fatal("timed out waiting for the app to read a thread")
		}
	}
}

// pass lets every read the test does not take return, until the test ends.
func (g *holding) pass(t *testing.T) {
	go func() {
		for {
			select {
			case r := <-g.reads:
				r.answer <- nil
			case <-t.Context().Done():
				return
			}
		}
	}()
}

// cuttable is a shell stream that a send on cut ends.
type cuttable struct {
	api.Stream
	cut <-chan struct{}
}

func (c cuttable) Next(ctx context.Context) (api.StreamItem, error) {
	next, cancel := context.WithCancel(ctx)
	cut := make(chan bool, 1)
	go func() {
		select {
		case <-c.cut:
			cut <- true
			cancel()
		case <-next.Done():
			cut <- false
		}
	}()
	it, err := c.Stream.Next(next)
	cancel()
	if <-cut {
		return api.StreamItem{}, errors.New("script: the connection was cut")
	}
	return it, err
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

// Two reports logged at one time are each told of, in the log's order.
func TestReportsAtOneTimeAreEachToldOf(t *testing.T) {
	parent := view("Parent", api.ID{})
	a, b := view("A", parent.ID), view("B", parent.ID)
	s, h := play(t, parent, a, b)
	h.thread("B")
	got := told(h, parent.ID)
	at := time.Unix(1001, 0)
	s.commit(&parent, a.ID, api.ReportBlocked, at)
	first := parent
	s.commit(&parent, b.ID, api.ReportBlocked, at)
	s.show(first) // A's view arrives once the log holds B's report too
	h.until("the reports' notifications", func() bool { return len(*got) > 0 })
	if want := []string{"A: Needs you", "B: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want %q", *got, want)
	}
}

// A Done report logged at a Blocked report's time, before the Blocked one is
// read, hides nothing.
func TestADoneReportAtABlockedOnesTimeHidesNothing(t *testing.T) {
	parent := view("Parent", api.ID{})
	a, b := view("Asker", parent.ID), view("Finisher", parent.ID)
	s, g, h := playHeld(t, true, parent, a, b)
	h.thread("Finisher")
	got := told(h, parent.ID)
	at := time.Unix(1001, 0)
	s.commit(&parent, a.ID, api.ReportBlocked, at)
	s.show(parent)
	r := g.next(h)
	s.commit(&parent, b.ID, api.ReportDone, at)
	s.show(parent)
	shown(h, parent)
	r.answer <- nil
	g.pass(t)
	h.until("the Blocked report's notification", func() bool { return len(*got) > 0 })
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

// A read of a report that fails is read again, with nothing else to prompt
// it.
func TestAFailedReadIsReadAgain(t *testing.T) {
	parent := view("Parent", api.ID{})
	kid := view("Asker", parent.ID)
	s, g, h := playHeld(t, false, parent, kid)
	h.thread("Asker")
	got := told(h, parent.ID)
	s.report(&parent, kid.ID, api.ReportBlocked)
	g.next(h).answer <- errors.New("script: the read failed")
	g.next(h).answer <- nil
	h.until("the Blocked report's notification", func() bool { return len(*got) > 0 })
	if want := []string{"Asker: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want %q", *got, want)
	}
}

// A failed read of an engine reached over TCP is read again, and the report
// it missed is told of with the one after.
func TestAFailedReadOverTCPIsReadAgain(t *testing.T) {
	parent := view("Parent", api.ID{})
	a, b := view("A", parent.ID), view("B", parent.ID)
	s := newScript(parent, a, b)
	g := hold(s)
	addr := serveOn(t, transport.Loopback{Network: "tcp", Address: "127.0.0.1:0"}, g)
	tcp := transport.NewEndpointClient(transport.Loopback{Network: "tcp", Address: addr}, api.ID{})
	t.Cleanup(func() { tcp.Close() })
	h := serveApp(t, tcp)
	h.thread("B")
	got := told(h, parent.ID)
	s.report(&parent, a.ID, api.ReportBlocked)
	g.next(h).answer <- errors.New("script: the read failed")
	s.report(&parent, b.ID, api.ReportBlocked)
	g.pass(t)
	h.until("both reports' notifications", func() bool { return len(*got) >= 2 })
	if want := []string{"A: Needs you", "B: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want %q", *got, want)
	}
}

// A read cut off by a disconnect is read again once the shell reconnects,
// though the parent's view comes back as it was.
func TestAReadCutByADisconnectIsReadAgainOnReconnect(t *testing.T) {
	parent := view("Parent", api.ID{})
	kid := view("Asker", parent.ID)
	s, g, h := playHeld(t, false, parent, kid)
	h.thread("Asker")
	got := told(h, parent.ID)
	s.report(&parent, kid.ID, api.ReportBlocked)
	r := g.next(h)
	g.cut <- struct{}{}
	select {
	case <-r.ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the read outlived the shell stream")
	}
	g.next(h).answer <- nil
	h.until("the Blocked report's notification", func() bool { return len(*got) > 0 })
	if want := []string{"Asker: Needs you"}; !slices.Equal(*got, want) {
		t.Fatalf("notifications on the parent: %q; want %q", *got, want)
	}
}

// A report to a parent deleted while the report is read tells of nothing.
func TestAReportToAParentDeletedDuringItsReadTellsNothing(t *testing.T) {
	parent := view("Parent", api.ID{})
	kid := view("Asker", parent.ID)
	s, g, h := playHeld(t, true, parent, kid)
	h.thread("Asker")
	got := told(h, parent.ID)
	s.report(&parent, kid.ID, api.ReportBlocked)
	r := g.next(h)
	parent.Deleted = true
	s.show(parent)
	shown(h, parent)
	r.answer <- nil
	h.settle(func() bool { return len(*got) > 0 })
	if len(*got) != 0 {
		t.Fatalf("notifications on the deleted parent: %q; want none", *got)
	}
}

// The same on a real engine, whose read is taken before the delete.
func TestAReportToAParentDeletedDuringItsReadOnARealEngineTellsNothing(t *testing.T) {
	d := engine(t, daemon.Config{Dir: tempDir(t)}, fake.New([]provider.Event{provider.RequestOpened{ID: "ask", Kind: "approval"}}))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	g := hold(d.Endpoint())
	h := serveApp(t, g)
	h.thread("Write the tests")
	got := told(h, top)
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: kid, Body: api.MessageDispatch{Text: "go"}}); err != nil {
		t.Fatal(err)
	}
	r := g.next(h)
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: top, Body: api.ThreadDelete{}}); err != nil {
		t.Fatal(err)
	}
	h.until("the parent's delete", func() bool { return h.local().Threads[top] == nil })
	r.answer <- nil
	h.settle(func() bool { return len(*got) > 0 })
	if len(*got) != 0 {
		t.Fatalf("notifications on the deleted parent: %q; want none", *got)
	}
}

// counted is an endpoint that counts the events, and the reports among
// them, that its reads of one thread return.
type counted struct {
	api.Endpoint
	thread api.ID

	mu              sync.Mutex
	events, reports int
}

func (c *counted) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	snap, err := c.Endpoint.Read(ctx, q)
	if err == nil && q.Thread == c.thread {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.events += len(snap.Events)
		for _, e := range snap.Events {
			if e.Type == api.EventType(api.ChildReported{}) {
				c.reports++
			}
		}
	}
	return snap, err
}

// logged is how many events the thread's log holds.
func logged(t *testing.T, d *daemon.Daemon, thread api.ID) int {
	t.Helper()
	snap, err := d.Endpoint().Read(context.Background(), api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	return len(snap.Events)
}

// Reports to a parent never opened read only the events logged since the
// app started, each once, however long the parent's log was before.
func TestReportsToANeverOpenedParentReadOnlyWhatsNew(t *testing.T) {
	var turn []provider.Event
	for i := range 30 {
		item := fmt.Sprint("read-", i)
		turn = append(turn,
			provider.Work{Item: item, Kind: "read", Title: "Read internal/client/client.go", Status: api.WorkStarted},
			provider.Work{Item: item, Kind: "read", Title: "Read internal/client/client.go", Status: api.WorkCompleted})
	}
	d := engine(t, daemon.Config{Dir: tempDir(t)}, fake.New(turn))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort", "a", "b", "c")
	kids := make([]api.ID, 10)
	for i := range kids {
		kids[i] = seedChild(t, d, top, fmt.Sprint("Kid ", i))
	}
	ep := &counted{Endpoint: d.Endpoint(), thread: top}
	h := serveApp(t, ep)
	parent := h.thread("Ship the sidebar sort")
	told(h, top)
	before := logged(t, d, top)
	for _, id := range kids {
		at := parent.View.Reported
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "go"}}); err != nil {
			t.Fatal(err)
		}
		h.until("the report", func() bool { return !parent.View.Reported.Equal(at) })
	}
	read := func() (int, int) {
		ep.mu.Lock()
		defer ep.mu.Unlock()
		return ep.events, ep.reports
	}
	h.until("each report read", func() bool { _, n := read(); return n >= len(kids) })
	events, reports := read()
	if since := logged(t, d, top) - before; reports != len(kids) || events > since {
		t.Fatalf("the reads of the parent returned %d events, %d of them reports; want at most the %d logged since the app started, and each of the %d reports once", events, reports, since, len(kids))
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
			wg.Go(func() {
				if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "go"}}); err != nil {
					t.Error(err)
				}
			})
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
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: kid, Body: api.MessageDispatch{Text: "go"}}); err != nil {
		t.Fatal(err)
	}
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
