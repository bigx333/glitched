package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

var linkModel = api.ModelRef{Provider: "fake", Model: "fake"}

// linked is engine B with a link minted for engine A, a checkout of a
// repository its person works in, and A's client through the link: what
// A's engine holds of B. A never dials B in this PR, so the test calls B
// as A would, over B's authenticated loopback socket.
type linked struct {
	t      *testing.T
	dirB   string
	repo   string
	root   string
	gate   string // a file whose absence holds git's checkout of a worktree, when gated
	ad     *fake.Adapter
	b      *Daemon
	close  func()
	token  string
	link   api.Link
	ep     *transport.EndpointClient
	origin api.Origin
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=a@b", "-c", "user.name=a"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newLinked starts A and B, mints scope on B for A, and has B's person
// start a thread in a repository. gated holds every checkout of a
// worktree of it until the test opens the gate.
func newLinked(t *testing.T, scope api.Link, gated bool) *linked {
	t.Helper()
	quiet(t)
	l := &linked{t: t, dirB: stateDir(t), ad: fake.New(fake.Turn(false))}
	allowChildren(t, l.dirB, api.AccessAutoEdits)
	var err error
	if l.token, l.link, err = PairLink(l.dirB, "a", scope); err != nil {
		t.Fatal(err)
	}
	l.repo = filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(l.repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, l.repo, "init", "-q", "-b", "main")
	gitIn(t, l.repo, "commit", "-q", "--allow-empty", "-m", "first")
	l.root = gitIn(t, l.repo, "rev-list", "--max-parents=0", "HEAD")
	if gated {
		l.gate = filepath.Join(t.TempDir(), "gate")
		hook := "#!/bin/sh\nexec >/dev/null 2>&1\ntouch '" + l.gate + ".held'\nwhile [ ! -f '" + l.gate + "' ]; do sleep 0.02; done\n"
		if err := os.WriteFile(filepath.Join(l.repo, ".git", "hooks", "post-checkout"), []byte(hook), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(l.open)
	}
	a := start(t, Config{Dir: stateDir(t)})
	envA, err := a.Endpoint().Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parent := api.NewID()
	if _, err := a.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: parent, Body: api.ThreadCreate{Workspace: "/src", Title: "parent on a", Model: linkModel}}); err != nil {
		t.Fatal(err)
	}
	l.origin = api.Origin{Engine: envA.ID, Thread: parent, Title: "parent on a", Depth: 1, Access: api.AccessFull}
	l.startB()
	if _, err := l.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Workspace: l.repo, Title: "mine", Model: linkModel}}); err != nil {
		t.Fatal(err)
	}
	return l
}

// startB starts B on its folder, and A's client through the link to it.
func (l *linked) startB() {
	l.t.Helper()
	d, err := Start(context.Background(), Config{Dir: l.dirB, Hub: true, Epoch: 1, Listen: []string{"127.0.0.1:0"}, Providers: provider.Mux{"fake": l.ad}})
	if err != nil {
		l.t.Fatal(err)
	}
	var once sync.Once
	l.b = d
	l.ep = transport.NewEndpointClient(transport.Tailnet{Address: d.Addrs()[0].String(), Token: l.token}, api.ID{})
	ep := l.ep
	l.close = func() {
		once.Do(func() {
			ep.Close()
			if err := d.Close(); err != nil {
				l.t.Errorf("close b: %v", err)
			}
		})
	}
	l.t.Cleanup(l.close)
}

func (l *linked) open() {
	if l.gate != "" {
		os.WriteFile(l.gate, nil, 0o644)
	}
}

// client is a client of B's with token.
func (l *linked) client(token string) *transport.EndpointClient {
	ep := transport.NewEndpointClient(transport.Tailnet{Address: l.b.Addrs()[0].String(), Token: token}, api.ID{})
	l.t.Cleanup(func() { ep.Close() })
	return ep
}

func (l *linked) create(access api.Access, depth int) api.Command {
	o := l.origin
	o.Depth = depth
	return api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{
		Title: "from a", Model: linkModel, Access: access, Text: "go",
		Origin: &o, Repository: &api.Repository{Root: l.root, Branch: "main"},
	}}
}

// mustCreate creates a thread through the link and returns it.
func (l *linked) mustCreate(access api.Access) api.ID {
	l.t.Helper()
	c := l.create(access, 1)
	if _, err := l.ep.Submit(context.Background(), c); err != nil {
		l.t.Fatalf("a link's create: %v", err)
	}
	return c.Target
}

func (l *linked) send(thread api.ID, body api.Intent) error {
	_, err := l.ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: body})
	return err
}

// events are the types of thread's events, as B's person reads them.
func (l *linked) events(thread api.ID) []string {
	l.t.Helper()
	snap, err := l.b.Endpoint().Read(context.Background(), api.Query{Thread: thread})
	if err != nil {
		l.t.Fatal(err)
	}
	var types []string
	for _, e := range snap.Events {
		types = append(types, e.Type)
	}
	return types
}

func (l *linked) count(thread api.ID, kind string) int {
	n := 0
	for _, e := range l.events(thread) {
		if e == kind {
			n++
		}
	}
	return n
}

func (l *linked) waitFor(thread api.ID, kind string, n int) {
	l.t.Helper()
	if snap, err := l.b.Endpoint().Read(context.Background(), api.Query{Thread: thread}); err == nil {
		for _, e := range snap.Events {
			if e.Type == "worktree.failed" && kind != "worktree.failed" {
				l.t.Fatalf("the worktree failed: %s", e.Payload)
			}
		}
	}
	for deadline := time.Now().Add(20 * time.Second); l.count(thread, kind) < n; time.Sleep(10 * time.Millisecond) {
		if kind != "worktree.failed" && l.count(thread, "worktree.failed") > 0 {
			snap, _ := l.b.Endpoint().Read(context.Background(), api.Query{Thread: thread})
			for _, e := range snap.Events {
				if e.Type == "worktree.failed" {
					l.t.Fatalf("waiting for %s, the worktree failed: %s", kind, e.Payload)
				}
			}
		}
		if time.Now().After(deadline) {
			l.t.Fatalf("no %d %s on %s; its events: %v", n, kind, thread, l.events(thread))
		}
	}
}

func (l *linked) view(thread api.ID) api.Thread {
	l.t.Helper()
	snap, err := l.b.Endpoint().Read(context.Background(), api.Query{})
	if err != nil {
		l.t.Fatal(err)
	}
	for _, th := range snap.Threads {
		if th.ID == thread {
			return th
		}
	}
	l.t.Fatalf("no thread %s on b", thread)
	return api.Thread{}
}

// making waits until git, making thread's worktree, holds at the gate
// after its checkout, and returns the worktree's folder.
func (l *linked) making(thread api.ID) string {
	l.t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		_, held := os.Stat(l.gate + ".held")
		if dirs, _ := filepath.Glob(filepath.Join(l.dirB, "worktrees", "*", strings.ToLower(thread.String()))); len(dirs) == 1 && held == nil {
			return dirs[0]
		}
		if time.Now().After(deadline) {
			l.t.Fatalf("no worktree of %s is being made", thread)
		}
	}
}

func notAllowed(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, api.ErrNotAllowed) {
		t.Fatalf("%s: %v, want api.ErrNotAllowed", what, err)
	}
}

// A link reads, follows, asks the reply of and sends the allowed commands
// to only the threads it started; every other intent, a query with no
// thread and a thread of B's person's are refused. A child B's agent
// starts under the link's thread is reached only through it.
func TestALinkReachesOnlyTheThreadsItStarted(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, false)
	ctx := context.Background()
	snap, err := l.b.Endpoint().Read(ctx, api.Query{})
	if err != nil {
		t.Fatal(err)
	}
	person := snap.Threads[0].ID

	_, err = l.ep.Read(ctx, api.Query{})
	notAllowed(t, "a read of every thread", err)
	_, err = l.ep.Open(ctx, api.Subscription{})
	notAllowed(t, "a subscribe to every thread", err)
	_, err = l.ep.Read(ctx, api.Query{Thread: person})
	notAllowed(t, "a read of the person's thread", err)
	_, err = l.ep.Open(ctx, api.Subscription{Thread: person})
	notAllowed(t, "a subscribe to the person's thread", err)
	_, err = l.ep.Read(ctx, api.Query{Thread: api.NewID()})
	notAllowed(t, "a read of a thread that isn't there", err)
	_, err = l.ep.ChildReply(ctx, api.ChildReplyQuery{Thread: person, Run: api.NewID()})
	notAllowed(t, "child.reply of the person's thread", err)
	notAllowed(t, "a message to the person's thread", l.send(person, api.MessageDispatch{Text: "hi"}))
	notAllowed(t, "a settle of the person's thread", l.send(person, api.InboxSet{Disposition: api.Disposition{State: api.Settled}}))

	l.ad.Spawns(l.b.Endpoint(), "split", fake.Child{Title: "kid", Task: "work"})
	c := l.create(api.AccessAutoEdits, 1)
	b := c.Body.(api.ThreadCreate)
	b.Text = "split"
	c.Body = b
	r, err := l.ep.Submit(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Command != c.ID {
		t.Fatalf("the receipt names %s, want the link's own id %s", r.Command, c.ID)
	}
	thread := c.Target
	l.waitFor(thread, "turn.ended", 1)
	var kid api.ID
	for deadline := time.Now().Add(10 * time.Second); kid.IsZero(); time.Sleep(10 * time.Millisecond) {
		snap, err := l.b.Endpoint().Read(ctx, api.Query{})
		if err != nil {
			t.Fatal(err)
		}
		for _, th := range snap.Threads {
			if th.Parent == thread {
				kid = th.ID
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the link's thread started no child")
		}
	}
	if _, err := l.ep.Read(ctx, api.Query{Thread: thread}); err != nil {
		t.Fatalf("a read of the link's thread: %v", err)
	}
	sub, err := l.ep.Open(ctx, api.Subscription{Thread: thread})
	if err != nil {
		t.Fatalf("a subscribe to the link's thread: %v", err)
	}
	sub.Close()
	_, err = l.ep.Read(ctx, api.Query{Thread: kid})
	notAllowed(t, "a read of the child under the link's thread", err)
	notAllowed(t, "a stop of the child under the link's thread", l.send(kid, api.TurnInterrupt{}))

	refused := []api.Intent{
		api.RequestRespond{Request: api.NewID(), Answer: []byte("{}")},
		api.ThreadRename{Title: "mine now"},
		api.ThreadDelete{},
		api.ThreadDelete{RemoveWorktree: true},
		api.QueueResume{},
		api.QueueEdit{Message: api.NewID(), Text: "x"},
		api.QueueCancel{Message: api.NewID()},
		api.QueueMove{Message: api.NewID()},
		api.QueueSteer{Message: api.NewID()},
		api.ThreadRevert{To: api.CheckpointRef{ID: api.NewID(), Phase: api.CheckpointRun}, Files: true},
		api.ResumeCancel{Run: api.NewID()},
		api.ThreadConfigure{Model: new("other")},
		api.ThreadConfigure{Access: new(api.AccessFull)},
		api.ThreadConfigure{},
		api.MessageDispatch{Text: "go on", Auto: true},
		api.ThreadCreate{Title: "again", Model: linkModel, Text: "go", Origin: &l.origin, Repository: &api.Repository{Root: l.root, Branch: "main"}},
	}
	for _, body := range refused {
		notAllowed(t, api.IntentType(body)+" on the link's thread", l.send(thread, body))
	}
	_, err = l.ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "hi"}, Caller: &api.Caller{Thread: api.NewID(), Run: api.NewID()}})
	notAllowed(t, "a command naming a caller", err)

	runs := l.count(thread, "turn.admitted")
	if err := l.send(thread, api.MessageDispatch{Text: "more", Mode: api.StartNow}); err != nil {
		t.Fatalf("a message to the link's thread: %v", err)
	}
	l.waitFor(thread, "turn.ended", runs+1)
	if err := l.send(thread, api.TurnInterrupt{Tree: true}); err != nil {
		t.Fatalf("a stop of the link's tree: %v", err)
	}
	if err := l.send(thread, api.ThreadConfigure{Access: new(api.AccessAsk)}); err != nil {
		t.Fatalf("a narrowing of the link's thread: %v", err)
	}
	if got := l.view(kid).Access; got != api.AccessAsk {
		t.Fatalf("the child under the narrowed thread runs at %d, want ask", got)
	}
	snap, err = l.ep.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	var last api.ID
	for _, e := range snap.Events {
		if e.Type == "assistant.delta" {
			b, _ := e.Decode()
			last = b.(api.AssistantDelta).Run
		}
	}
	reply, err := l.ep.ChildReply(ctx, api.ChildReplyQuery{Thread: thread, Run: last})
	if err != nil || reply.Text != " Done." {
		t.Fatalf("child.reply of the link's last run %s: %+v %v, want what its agent said last; events %v", last, reply, err, l.events(thread))
	}
	_, err = l.ep.ChildReply(ctx, api.ChildReplyQuery{Thread: thread, Run: api.NewID()})
	if !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("child.reply of a run the thread doesn't have: %v, want api.ErrNotFound", err)
	}
	otherToken, _, err := PairLink(l.dirB, "c", api.Link{Access: api.AccessAsk})
	if err != nil {
		t.Fatal(err)
	}
	other := l.client(otherToken)
	_, err = other.ChildReply(ctx, api.ChildReplyQuery{Thread: thread, Run: last})
	notAllowed(t, "child.reply of another link's thread", err)
	_, err = other.Read(ctx, api.Query{Thread: thread})
	notAllowed(t, "a read of another link's thread", err)

	if err := l.send(thread, api.InboxSet{Disposition: api.Disposition{State: api.Settled}}); err != nil {
		t.Fatalf("a settle of the link's thread: %v", err)
	}
	if got := l.view(kid).Disposition.State; got != api.Settled {
		t.Fatalf("the child under the settled thread is %d, want settled with it", got)
	}
}

// A link's create is closed and checked in its commit: a field B picks
// is refused, as is an origin from anyone but a link, a depth out of
// range, an access over the cap, a create past the link's count and a
// repository or branch B doesn't have one checkout of. A replay gets the
// stored receipt, and another link's command under the same id its own.
func TestALinksCreateIsClosedAndChecked(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAutoEdits, All: 1}, false)
	ctx := context.Background()
	rejected := func(what string, c api.Command, want string) {
		t.Helper()
		var rej *api.RejectedError
		if _, err := l.ep.Submit(ctx, c); !errors.As(err, &rej) || !strings.Contains(rej.Reason, want) {
			t.Fatalf("%s: %v, want refused with %q", what, err, want)
		}
	}
	with := func(change func(*api.ThreadCreate)) api.Command {
		c := l.create(api.AccessAsk, 1)
		b := c.Body.(api.ThreadCreate)
		change(&b)
		c.Body = b
		return c
	}
	for name, change := range map[string]func(*api.ThreadCreate){
		"a workspace": func(b *api.ThreadCreate) { b.Workspace = l.repo },
		"a worktree":  func(b *api.ThreadCreate) { b.Worktree = &api.NewWorktree{} },
		"a parent":    func(b *api.ThreadCreate) { b.Parent = api.NewID() },
		"a project":   func(b *api.ThreadCreate) { b.Project = l.repo },
		"a branch":    func(b *api.ThreadCreate) { b.Branch = "glitched/x" },
		"a fork":      func(b *api.ThreadCreate) { b.Fork = &api.Fork{Thread: api.NewID()} },
		"a remote":    func(b *api.ThreadCreate) { b.Remote = "c" },
	} {
		rejected("a create with "+name, with(change), "gives only its origin")
	}
	rejected("a create with no origin", with(func(b *api.ThreadCreate) { b.Origin = nil }), "names its origin")
	rejected("a create with no repository", with(func(b *api.ThreadCreate) { b.Repository = nil }), "names its origin")
	rejected("depth 0", with(func(b *api.ThreadCreate) { b.Origin.Depth = 0 }), "1 to 4")
	rejected("depth 5", with(func(b *api.ThreadCreate) { b.Origin.Depth = 5 }), "1 to 4")
	rejected("full under an auto-edits link", with(func(b *api.ThreadCreate) { b.Access = api.AccessFull }), "no wider than its ceiling")
	rejected("wider than its origin", with(func(b *api.ThreadCreate) { b.Access, b.Origin.Access = api.AccessAutoEdits, api.AccessAsk }), "wider than its parent there")
	rejected("another repository", with(func(b *api.ThreadCreate) { b.Repository.Root = strings.Repeat("a", 40) }), "the checkouts here are: ")
	rejected("a branch B lacks", with(func(b *api.ThreadCreate) { b.Repository.Branch = "nope" }), "has no branch nope")
	if _, err := l.b.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Workspace: l.repo, Title: "x", Model: linkModel, Origin: &l.origin}}); err == nil {
		t.Fatal("B's person made a thread with an origin")
	}

	c := l.create(api.AccessAsk, 1)
	first, err := l.ep.Submit(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	again, err := l.ep.Submit(ctx, c)
	if err != nil || again != first {
		t.Fatalf("the replayed create: %+v %v, want the stored receipt %+v", again, err, first)
	}
	rejected("a second past the link's All of 1", l.create(api.AccessAsk, 1), "the most it may")

	otherToken, _, err := PairLink(l.dirB, "c", api.Link{Access: api.AccessAsk})
	if err != nil {
		t.Fatal(err)
	}
	same := l.create(api.AccessAsk, 1)
	same.ID = c.ID
	theirs, err := l.client(otherToken).Submit(ctx, same)
	if err != nil || theirs.Committed == first.Committed {
		t.Fatalf("another link's create under the same id: %+v %v, want a receipt of its own", theirs, err)
	}

	clone := filepath.Join(t.TempDir(), "clone")
	gitIn(t, l.repo, "clone", "-q", l.repo, clone)
	if _, err := l.b.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Workspace: clone, Title: "clone", Model: linkModel}}); err != nil {
		t.Fatal(err)
	}
	rejected("a repository with two checkouts", l.create(api.AccessAsk, 1), "several checkouts")
}

// A create on a link's socket that outlives its revoke, as one does for
// up to a second, is refused by its own commit, which reads tokens.json.
func TestALinksCreateOnARevokedSocketIsRefusedAtCommit(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, false)
	ctx := context.Background()
	for try := 0; ; try++ {
		if _, err := l.ep.Info(ctx); err != nil {
			t.Fatal(err)
		}
		if err := Revoke(l.dirB, "a"); err != nil {
			t.Fatal(err)
		}
		_, err := l.ep.Submit(ctx, l.create(api.AccessAsk, 1))
		var rej *api.RejectedError
		if errors.As(err, &rej) {
			if !strings.Contains(rej.Reason, "revoked") {
				t.Fatalf("the create on a revoked link's live socket: %v, want refused as revoked", err)
			}
			return
		}
		if err == nil {
			t.Fatal("a create on a revoked link's live socket was taken")
		}
		// The second's reload closed the socket first: try on another.
		if try == 5 {
			t.Fatalf("each revoked socket closed before its create: %v", err)
		}
		l.ep.Close()
		label := "a"
		if l.token, l.link, err = PairLink(l.dirB, label, api.Link{Access: api.AccessAutoEdits}); err != nil {
			t.Fatal(err)
		}
		l.ep = l.client(l.token)
	}
}

// Lowering the ceiling while the worktree is made admits no turn above
// it once the worktree is ready; a narrowing then starts the turn.
func TestALoweredCeilingBeforeTheWorktreeAdmitsNoTurnAboveIt(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, true)
	thread := l.mustCreate(api.AccessAutoEdits)
	l.making(thread)
	allowChildren(t, l.dirB, api.AccessAsk)
	l.open()
	l.waitFor(thread, "worktree.ready", 1)
	time.Sleep(200 * time.Millisecond)
	if n := l.count(thread, "turn.admitted"); n != 0 {
		t.Fatalf("%d turns admitted at auto-edits under a setting lowered to ask", n)
	}
	if v := l.view(thread); v.Queued != 1 {
		t.Fatalf("the thread queues %d, want its first message waiting", v.Queued)
	}
	if err := l.send(thread, api.ThreadConfigure{Access: new(api.AccessAsk)}); err != nil {
		t.Fatal(err)
	}
	l.waitFor(thread, "turn.ended", 1)
	if v := l.view(thread); v.LastTurn == nil || v.LastTurn.Access != api.AccessAsk {
		t.Fatalf("the turn after the narrowing: %+v, want it at ask", v.LastTurn)
	}
}

// A settle while the worktree is made leaves the thread nothing to start.
func TestASettleBeforeTheWorktreeAdmitsNoTurn(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, true)
	thread := l.mustCreate(api.AccessAsk)
	l.making(thread)
	if err := l.send(thread, api.InboxSet{Disposition: api.Disposition{State: api.Settled}}); err != nil {
		t.Fatal(err)
	}
	l.open()
	l.waitFor(thread, "worktree.ready", 1)
	time.Sleep(200 * time.Millisecond)
	if n := l.count(thread, "turn.admitted"); n != 0 {
		t.Fatalf("%d turns admitted on a thread settled before its worktree", n)
	}
}

// A settle while git makes the worktree, then a kill, leaves a worktree
// the next engine finishes for the thread, which starts nothing.
func TestASettleAndAKillWhileTheWorktreeIsMadeFinishIt(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, true)
	thread := l.mustCreate(api.AccessAsk)
	dir := l.making(thread)
	if err := l.send(thread, api.InboxSet{Disposition: api.Disposition{State: api.Settled}}); err != nil {
		t.Fatal(err)
	}
	l.close()
	l.open()
	l.startB()
	l.waitFor(thread, "worktree.ready", 1)
	if v := l.view(thread); v.Workspace != dir || l.count(thread, "turn.admitted") != 0 {
		t.Fatalf("the settled thread after the replay: workspace %q, %d turns; want its worktree and none", v.Workspace, l.count(thread, "turn.admitted"))
	}
	if _, err := os.Stat(filepath.Join(l.repo, ".git", "worktrees", filepath.Base(dir), "locked")); !os.IsNotExist(err) {
		t.Fatalf("the finished worktree is still locked: %v", err)
	}
}

// A configure above the cap is refused from B's person too, and a
// message over a cap lowered since waits.
func TestALinksThreadStaysUnderItsCap(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAsk}, false)
	ctx := context.Background()
	thread := l.mustCreate(api.AccessAsk)
	l.waitFor(thread, "turn.ended", 1)
	var rej *api.RejectedError
	_, err := l.b.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadConfigure{Access: new(api.AccessAutoEdits)}})
	if !errors.As(err, &rej) {
		t.Fatalf("B's person's configure above the link's ceiling: %v, want refused", err)
	}

	l2 := newLinked(t, api.Link{Access: api.AccessAutoEdits}, false)
	thread = l2.mustCreate(api.AccessAutoEdits)
	l2.waitFor(thread, "turn.ended", 1)
	allowChildren(t, l2.dirB, api.AccessAsk)
	if err := l2.send(thread, api.MessageDispatch{Text: "more", Mode: api.StartNow}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := l2.count(thread, "turn.admitted"); n != 1 {
		t.Fatalf("%d turns admitted, want the first alone: the message over the lowered cap waits", n)
	}
	if v := l2.view(thread); v.Queued != 1 {
		t.Fatalf("the thread queues %d, want the message waiting", v.Queued)
	}
}

// A kill as git makes the worktree, before the owner file is written and
// after, leaves the effect leased; the next engine's replay finishes the
// worktree it finds locked for the thread, and the turn starts.
func TestAKilledWorktreeMakeIsFinishedByTheReplay(t *testing.T) {
	for _, kill := range []string{"before git wrote the worktree", "twice before git wrote the worktree", "before the owner write", "after the owner write"} {
		t.Run(kill, func(t *testing.T) {
			l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, true)
			thread := l.mustCreate(api.AccessAsk)
			dir := l.making(thread)
			l.close()
			l.open()
			own := filepath.Join(l.repo, ".git", "worktrees", filepath.Base(dir))
			if lock, err := os.ReadFile(filepath.Join(own, "locked")); err != nil || !strings.Contains(string(lock), thread.String()) {
				t.Fatalf("the killed make's lock: %q %v, want it naming the thread", lock, err)
			}
			switch kill {
			case "before git wrote the worktree", "twice before git wrote the worktree":
				// What git leaves when killed as it begins: the folder,
				// empty, and the branch it made; a second kill as the
				// replay began leaves the long branch too.
				branch := gitIn(t, dir, "branch", "--show-current")
				gitIn(t, l.repo, "worktree", "remove", "-f", "-f", dir)
				if kill == "twice before git wrote the worktree" {
					gitIn(t, l.repo, "branch", strings.ToLower(strings.TrimSuffix(branch, branch[len(branch)-6:])+thread.String()), "main")
				}
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			case "after the owner write":
				if err := os.WriteFile(filepath.Join(own, "glitched-thread"), []byte(thread.String()), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			l.startB()
			l.waitFor(thread, "worktree.ready", 1)
			l.waitFor(thread, "turn.ended", 1)
			if _, err := os.Stat(filepath.Join(own, "locked")); !os.IsNotExist(err) {
				t.Fatalf("the finished worktree is still locked: %v", err)
			}
			if v := l.view(thread); v.Workspace != dir {
				t.Fatalf("the thread runs in %q, want its worktree %q", v.Workspace, dir)
			}
		})
	}
}

// A link's narrowing is checked against the thread as its commit has it,
// so one sent as B's person narrows further never widens the thread back.
func TestALinksNarrowingNeverUndoesBsNarrowing(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, false)
	ctx := context.Background()
	thread := l.mustCreate(api.AccessAutoEdits)
	l.waitFor(thread, "turn.ended", 1)
	configure := func(access api.Access) api.Command {
		return api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadConfigure{Access: &access}}
	}
	for round := range 40 {
		if _, err := l.b.Endpoint().Submit(ctx, configure(api.AccessAutoEdits)); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var linkErr, bErr error
		wg.Go(func() { _, linkErr = l.ep.Submit(ctx, configure(api.AccessAutoEdits)) })
		wg.Go(func() { _, bErr = l.b.Endpoint().Submit(ctx, configure(api.AccessAsk)) })
		wg.Wait()
		if bErr != nil || linkErr != nil && !errors.Is(linkErr, api.ErrNotAllowed) {
			t.Fatalf("round %d: the link's configure %v, B's %v", round, linkErr, bErr)
		}
		if v := l.view(thread); v.Access != api.AccessAsk {
			t.Fatalf("round %d: the thread is at %d after B narrowed it to ask and the link sent no widening", round, v.Access)
		}
	}
}
