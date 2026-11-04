package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"glitched/internal/api"
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

// play serves s on the socket the app in dir attaches to, its shell
// starting from views, and starts the app.
func play(t *testing.T, views ...api.Thread) (*script, *harness) {
	t.Helper()
	s := &script{
		log: api.NewID(), host: api.NewID(), views: make(chan api.Thread, 16),
		shell: views, events: map[api.ID][]api.Event{}, at: time.Unix(1000, 0),
	}
	dir := tempDir(t)
	sock := daemon.Socket(dir)
	ln, err := sock.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (&transport.Server{Transport: sock, Endpoint: s}).Serve(ctx, ln) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return s, start(t, dir)
}

// view is a thread of the script's: under parent unless that is zero.
func view(title string, parent api.ID) api.Thread {
	return api.Thread{ID: api.NewID(), Workspace: "/work/t3code", Title: title, Model: fakeModel, Parent: parent}
}

// show sends v on the shell, as a commit that moved it would.
func (s *script) show(v api.Thread) { s.views <- v }

// report commits a report of child's to parent and shows parent's view
// after it.
func (s *script) report(parent *api.Thread, child api.ID, kind api.ReportKind) {
	s.mu.Lock()
	s.seq++
	s.at = s.at.Add(time.Second)
	payload, _ := json.Marshal(api.ChildReported{Child: child, Run: api.NewID(), Kind: kind, Title: "child"})
	s.events[parent.ID] = append(s.events[parent.ID], api.Event{
		Cursor: api.Cursor{Log: s.log, Seq: s.seq}, Thread: parent.ID,
		Type: api.EventType(api.ChildReported{}), At: s.at, Payload: payload,
	})
	parent.Reported, parent.UpdatedAt = s.at, s.at
	s.mu.Unlock()
	s.show(*parent)
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
	snap := api.Snapshot{Cursor: api.Cursor{Log: s.log, Seq: s.seq}, Threads: s.shell}
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
