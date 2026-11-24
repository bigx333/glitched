package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/store"
)

// links is a link table a test changes as a person would tokens.json.
type links struct {
	mu sync.Mutex
	at map[api.ID]api.Link
}

func (l *links) set(link api.Link) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.at == nil {
		l.at = map[api.ID]api.Link{}
	}
	l.at[link.ID] = link
}

func (l *links) revoke(id api.ID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.at, id)
}

func (l *links) find(id api.ID) (api.Link, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	link, ok := l.at[id]
	if !ok {
		return api.Link{}, errors.New("the link was revoked")
	}
	return link, nil
}

// linkCreate is a link's create as the host resolves it.
func linkCreate(link api.ID, access api.Access, depth int) api.Command {
	return api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{
		Title: "from a", Model: api.ModelRef{Provider: "fake", Model: "m"}, Access: access, Text: "go",
		Origin:     &api.Origin{Engine: api.NewID(), Thread: api.NewID(), Title: "parent", Depth: depth, Access: api.AccessFull, Link: link},
		Repository: &api.Repository{Root: "root", Branch: "main"},
		Project:    "/src", Worktree: &api.NewWorktree{Base: "abc"},
	}}
}

// fold is the thread its log folds to.
func fold(t *testing.T, s *store.Store, id api.ID) engine.Thread {
	t.Helper()
	log, err := s.ReadLog(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var th engine.Thread
	for _, e := range log {
		if e.Thread != id {
			continue
		}
		body, err := e.Decode()
		if err != nil {
			t.Fatal(err)
		}
		th = engine.Apply(th, engine.Event{Thread: e.Thread, Seq: e.Cursor.Seq, At: e.At, Body: body})
	}
	return th
}

func sameAsFold(t *testing.T, s *store.Store, id api.ID, what string) engine.Thread {
	t.Helper()
	got, want := threadOf(t, s, id), fold(t, s, id)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: the row\n%+v\nwant its log's fold\n%+v", what, got, want)
	}
	return got
}

// leaseMake leases the thread's worktree.make and returns its id.
func leaseMake(t *testing.T, s *store.Store, thread api.ID) int64 {
	t.Helper()
	e, ok, err := s.Lease(context.Background(), func() []api.ID { return nil })
	if err != nil || !ok || e.Thread != thread {
		t.Fatalf("lease: %+v %v %v, want the thread's effect", e, ok, err)
	}
	if _, isMake := e.Body.(engine.MakeWorktree); !isMake {
		t.Fatalf("leased %T, want a worktree.make", e.Body)
	}
	return e.ID
}

// A link's thread is committed with its origin and its first message, and
// no turn, then a worktree.ready gives it its folder and starts the turn;
// one whose worktree fails cancels the message. Each row is its log's
// fold, through a reopen.
func TestALinksThreadFoldsToItsRow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	l := &links{}
	link := api.Link{ID: api.NewID(), Label: "a", Access: api.AccessFull}
	l.set(link)
	s.SetLinks(l.find)

	ok, failed := linkCreate(link.ID, api.AccessAutoEdits, 1), linkCreate(link.ID, api.AccessAsk, 2)
	for _, c := range []api.Command{ok, failed} {
		if _, err := s.Submit(ctx, c, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	th := sameAsFold(t, s, ok.Target, "after the create")
	if th.Origin == nil || !th.Origin.Making || th.Origin.Link != link.ID || th.Workspace != "" || th.Live() != nil || len(th.Queue) != 1 {
		t.Fatalf("the created thread: %+v; want its origin, making, no folder, its message and no run", th)
	}
	if _, err := s.Observe(ctx, ok.Target, engine.WorktreeReady{Workspace: "/wt/x", Branch: "glitched/x"}, time.Now(), leaseMake(t, s, ok.Target)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, failed.Target, engine.WorktreeFailed{Error: "no"}, time.Now(), leaseMake(t, s, failed.Target)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = open(t, path)
	s.SetLinks(l.find)
	th = sameAsFold(t, s, ok.Target, "after its worktree was made")
	if th.Origin.Making || th.Workspace != "/wt/x" || th.Branch != "glitched/x" || th.Live() == nil {
		t.Fatalf("the thread once its worktree was made: %+v; want its folder, branch and a run", th)
	}
	th = sameAsFold(t, s, failed.Target, "after its worktree failed")
	if th.Origin.Making || th.Workspace != "" || th.Live() != nil || th.Queue[0].State != engine.QueueCancelled {
		t.Fatalf("the thread whose worktree failed: %+v; want no folder, no run and its message cancelled", th)
	}
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: failed.Target, Body: api.MessageDispatch{Text: "again"}}, time.Now()); err == nil {
		t.Fatal("a message to a thread whose worktree failed was taken")
	}
}

// The create's commit checks the link as it is then: there, its depth,
// its access against its origin's and the link's, and the link's counts.
func TestALinksCreateIsCheckedInItsCommit(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	l := &links{}
	link := api.Link{ID: api.NewID(), Label: "a", Access: api.AccessAutoEdits, Working: 1, All: 2}
	l.set(link)
	s.SetLinks(l.find)
	refused := func(what string, c api.Command, want string) {
		t.Helper()
		var rej *api.RejectedError
		if _, err := s.Submit(ctx, c, time.Now()); !errors.As(err, &rej) || !strings.Contains(rej.Reason, want) {
			t.Fatalf("%s: %v, want refused with %q", what, err, want)
		}
	}
	refused("depth 0", linkCreate(link.ID, api.AccessAsk, 0), "1 to 4")
	refused("depth 5", linkCreate(link.ID, api.AccessAsk, 5), "1 to 4")
	refused("above the link's ceiling", linkCreate(link.ID, api.AccessFull, 1), "no wider than its ceiling")
	over := linkCreate(link.ID, api.AccessAutoEdits, 1)
	over.Body.(api.ThreadCreate).Origin.Access = api.AccessAsk
	refused("above its origin's access", over, "wider than its parent there")

	first := linkCreate(link.ID, api.AccessAsk, 1)
	if _, err := s.Submit(ctx, first, time.Now()); err != nil {
		t.Fatal(err)
	}
	refused("a second while one is at work", linkCreate(link.ID, api.AccessAsk, 1), "the most at once")
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: first.Target, Body: api.InboxSet{Disposition: api.Disposition{State: api.Settled}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	second := linkCreate(link.ID, api.AccessAsk, 1)
	if _, err := s.Submit(ctx, second, time.Now()); err != nil {
		t.Fatalf("a second once the first was settled: %v", err)
	}
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: second.Target, Body: api.InboxSet{Disposition: api.Disposition{State: api.Settled}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	refused("a third past All", linkCreate(link.ID, api.AccessAsk, 1), "the most it may")

	other := api.Link{ID: api.NewID(), Label: "c", Access: api.AccessAsk}
	l.set(other)
	if _, err := s.Submit(ctx, linkCreate(other.ID, api.AccessAsk, 1), time.Now()); err != nil {
		t.Fatalf("another link's create beside a full one: %v", err)
	}
	l.revoke(other.ID)
	refused("a revoked link's", linkCreate(other.ID, api.AccessAsk, 1), "revoked")
}

// A link's thread runs no wider than its ceiling as it is at each commit:
// a turn over a ceiling lowered since waits, a widening is refused, and a
// narrowing lets the turn start.
func TestALoweredCeilingHoldsALinksThreadsTurn(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	l := &links{}
	link := api.Link{ID: api.NewID(), Label: "a", Access: api.AccessAutoEdits}
	l.set(link)
	s.SetLinks(l.find)
	c := linkCreate(link.ID, api.AccessAutoEdits, 1)
	if _, err := s.Submit(ctx, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	link.Access = api.AccessAsk
	l.set(link)
	if _, err := s.Observe(ctx, c.Target, engine.WorktreeReady{Workspace: "/wt/x", Branch: "glitched/x"}, time.Now(), leaseMake(t, s, c.Target)); err != nil {
		t.Fatal(err)
	}
	if th := sameAsFold(t, s, c.Target, "ready over a lowered ceiling"); th.Live() != nil || th.Queue[0].State != engine.QueueReady {
		t.Fatalf("a thread over its lowered ceiling: %+v; want its message waiting and no run", th)
	}
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: c.Target, Body: api.ThreadConfigure{Access: new(api.AccessFull)}}, time.Now()); err == nil {
		t.Fatal("a configure above the ceiling was taken")
	}
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: c.Target, Body: api.ThreadConfigure{Access: new(api.AccessAsk)}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	th := sameAsFold(t, s, c.Target, "narrowed")
	if r := th.Live(); r == nil || r.Access != api.AccessAsk {
		t.Fatalf("the narrowed thread: %+v; want a run at ask", th)
	}
}
