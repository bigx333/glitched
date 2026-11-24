package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/store"
	"glitched/internal/transport"
)

// remotes is engine A, whose agents place children on engine B through a
// link B minted for A, as two daemons with homes of their own on this
// machine, over loopback. Both work in one repository, which is A's
// parent's folder and a checkout of B's person's.
type remotes struct {
	t          *testing.T
	repo       string
	dirA, dirB string
	addrB      string // where B listens, the same across its restarts
	adA, adB   *fake.Adapter
	model      api.ModelRef
	a, b       *Daemon
	token      string
	link       api.Link
	engineB    api.ID
	parent     api.ID
	limits     engine.Limits
	continueB  bool // B continues the runs its restart lost
	steps      func(step string, placeholder api.ID)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// newRemotes starts B with adB as its provider, under the name the
// model's provider gives, mints the link, and starts A with machines.json
// naming B as "b" at server, B's address unless given.
func newRemotes(t *testing.T, adB *fake.Adapter, provider string, limits engine.Limits) *remotes {
	t.Helper()
	quiet(t)
	p := &remotes{t: t, dirA: stateDir(t), dirB: stateDir(t), addrB: freeAddr(t), adA: fake.New(fake.Turn(false)), adB: adB, model: api.ModelRef{Provider: provider, Model: provider}, limits: limits}
	p.repo = t.TempDir()
	gitIn(t, p.repo, "init", "-q", "-b", "main")
	gitIn(t, p.repo, "commit", "-q", "--allow-empty", "-m", "first")
	allowChildren(t, p.dirB, api.AccessAutoEdits)
	var err error
	if p.token, p.link, err = PairLink(p.dirB, "a", api.Link{Access: api.AccessAutoEdits}); err != nil {
		t.Fatal(err)
	}
	p.startB()
	env, err := p.b.Endpoint().Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p.engineB = env.ID
	if _, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Workspace: p.repo, Title: "mine", Model: p.model}}); err != nil {
		t.Fatal(err)
	}
	p.machine(p.addrB)
	p.startA()
	p.parent = api.NewID()
	if _, err := p.a.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: p.parent, Body: api.ThreadCreate{Workspace: p.repo, Title: "parent on a", Model: p.model, Access: api.AccessAutoEdits}}); err != nil {
		t.Fatal(err)
	}
	return p
}

// machine has A reach B at server.
func (p *remotes) machine(server string) {
	p.t.Helper()
	b, err := json.Marshal([]Machine{{Label: "b", Server: server, Engine: p.engineB, Link: p.link.ID, Token: p.token}})
	if err != nil {
		p.t.Fatal(err)
	}
	writeMachines(p.t, p.dirA, b)
}

func (p *remotes) startB() {
	p.t.Helper()
	d, err := Start(context.Background(), Config{Dir: p.dirB, Hub: true, Epoch: 1, Listen: []string{p.addrB}, ContinueAfterRestart: p.continueB, Providers: provider.Mux{p.model.Provider: p.adB}})
	if err != nil {
		p.t.Fatal(err)
	}
	p.b = d
	p.t.Cleanup(func() { d.Close() })
}

func (p *remotes) stopB() {
	p.t.Helper()
	if err := p.b.Close(); err != nil {
		p.t.Fatal(err)
	}
}

// fast is the backoff the tests retry with: short, so a test waits on
// what it proves, and growing, as the engine's does.
func fast(n int) time.Duration {
	return min(20*time.Millisecond*time.Duration(n+1), 200*time.Millisecond)
}

func (p *remotes) startA() {
	p.t.Helper()
	d, err := Start(context.Background(), Config{Dir: p.dirA, Hub: true, Epoch: 1, Children: p.limits, RemoteBackoff: fast, RemoteSteps: p.steps, Providers: provider.Mux{p.model.Provider: p.adA}, Inhibit: func(ctx context.Context) error { <-ctx.Done(); return nil }})
	if err != nil {
		p.t.Fatal(err)
	}
	p.a = d
	p.t.Cleanup(func() { d.Close() })
}

func (p *remotes) stopA() {
	p.t.Helper()
	if err := p.a.Close(); err != nil {
		p.t.Fatal(err)
	}
}

// place has a turn of A's parent start a child on b, and returns its
// placeholder.
func (p *remotes) place(title string) api.ID {
	p.t.Helper()
	ids, errs := spawn(p.t, p.a.Endpoint(), p.adA, p.parent, fake.Child{Title: title, Task: "do it there", Remote: "b"})
	if errs[0] != nil {
		p.t.Fatalf("the remote child: %v", errs[0])
	}
	return ids[0]
}

func (p *remotes) placeholder(id api.ID) engine.Thread {
	p.t.Helper()
	th, err := p.a.store.Thread(context.Background(), id)
	if err != nil {
		p.t.Fatal(err)
	}
	return th
}

func eventsOf(t *testing.T, ep api.Endpoint, thread api.ID) []api.Event {
	t.Helper()
	snap, err := ep.Read(context.Background(), api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	return snap.Events
}

// reports are the reports of child in A's parent's log, and the wakes
// they queued.
func (p *remotes) reports(child api.ID) (reports []api.ChildReported, wakes []api.MessageQueued) {
	p.t.Helper()
	for _, e := range eventsOf(p.t, p.a.Endpoint(), p.parent) {
		b, err := e.Decode()
		if err != nil {
			p.t.Fatal(err)
		}
		switch b := b.(type) {
		case api.ChildReported:
			if b.Child == child {
				reports = append(reports, b)
			}
		case api.MessageQueued:
			if b.Report == child {
				wakes = append(wakes, b)
			}
		}
	}
	return reports, wakes
}

// there are B's threads that came through the link, by title.
func (p *remotes) there() []api.Thread {
	p.t.Helper()
	snap, err := p.b.Endpoint().Read(context.Background(), api.Query{})
	if err != nil {
		p.t.Fatal(err)
	}
	var out []api.Thread
	for _, th := range snap.Threads {
		if th.Title != "mine" {
			out = append(out, th)
		}
	}
	return out
}

// parentIdle waits for A's parent to have no turn live or queued, such
// as the one a report woke: a stop of A while it is live leaves it lost,
// and the parent's next turn queued behind it.
func (p *remotes) parentIdle() {
	p.t.Helper()
	until(p.t, "the parent idle", func() bool {
		snap, _ := p.a.Endpoint().Read(context.Background(), api.Query{Thread: p.parent})
		return len(snap.Threads) == 1 && snap.Threads[0].Run == api.RunIdle && snap.Threads[0].Queued == 0
	})
}

func (p *remotes) bound(id api.ID) engine.Remote {
	p.t.Helper()
	until(p.t, "the placeholder bound", func() bool { return p.placeholder(id).Remote.State == engine.RemoteBoundState })
	return *p.placeholder(id).Remote
}

// lost waits for the placeholder to be lost, and returns why.
func (p *remotes) lost(id api.ID) string {
	p.t.Helper()
	until(p.t, "the placeholder lost", func() bool { return p.placeholder(id).Remote.State == engine.RemoteLostState })
	return p.placeholder(id).Remote.Lost
}

// folded checks that A's stored threads are its log's fold.
func folded(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	log, err := st.ReadLog(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	fold := map[api.ID]engine.Thread{}
	for _, e := range log {
		body, err := e.Decode()
		if err != nil || body == nil {
			t.Fatalf("decode %s: %v", e.Type, err)
		}
		fold[e.Thread] = engine.Apply(fold[e.Thread], engine.Event{Thread: e.Thread, Seq: e.Cursor.Seq, At: e.At, Body: body})
	}
	for id, want := range fold {
		got, err := st.Thread(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if want.Remote != nil && !jsonEqual(got.Remote, want.Remote) {
			t.Fatalf("thread %s: stored remote %+v; the log folds to %+v", id, got.Remote, want.Remote)
		}
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// A child placed while its machine is down waits for it, taking a place
// at work, and is created there once the machine is up; its done run
// wakes the parent once with what it said there. Idle, it takes no place.
func TestARemoteChildIsCreatedOnceItsMachineIsUpAndReportsBack(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{Working: 1})
	p.stopB()
	kid := p.place("there")
	until(t, "a try at the create", func() bool { return p.placeholder(kid).Remote.Attempted })
	time.Sleep(300 * time.Millisecond)
	if r := p.placeholder(kid).Remote; r.State != engine.RemoteCreating {
		t.Fatalf("with b down the placeholder is %+v; want it creating", r)
	}
	_, errs := spawn(t, p.a.Endpoint(), p.adA, p.parent, fake.Child{Title: "here", Task: "go"})
	refused(t, "a second child while the remote one is being created", errs[0], "1 child threads of this thread are at work")

	p.startB()
	r := p.bound(kid)
	until(t, "the parent woken", func() bool { _, wakes := p.reports(kid); return len(wakes) > 0 })
	time.Sleep(300 * time.Millisecond)
	reports, wakes := p.reports(kid)
	if len(reports) != 1 || reports[0].Kind != api.ReportDone || len(wakes) != 1 {
		t.Fatalf("reports %+v, wakes %d; want one done report and one wake", reports, len(wakes))
	}
	if !strings.Contains(wakes[0].Text, "<task_result>\nDone.\n</task_result>") || reports[0].Outcome != "Done." {
		t.Fatalf("the wake %q and outcome %q; want b's reply in them", wakes[0].Text, reports[0].Outcome)
	}
	if there := p.there(); len(there) != 1 || there[0].ID != r.Thread {
		t.Fatalf("b's threads from the link: %+v; want the one bound, %v", there, r.Thread)
	}
	until(t, "the child idle there", func() bool {
		return p.placeholder(kid).Remote.Slot == api.RunIdle && p.placeholder(kid).Remote.Run != (api.ID{})
	})
	p.parentIdle()
	if _, errs := spawn(t, p.a.Endpoint(), p.adA, p.parent, fake.Child{Title: "here", Task: "go"}); errs[0] != nil {
		t.Fatalf("a child once the remote one is idle there: %v; want it started", errs[0])
	}
	folded(t, p.a.store)
	for _, name := range []string{"engine.db", "engine.db-wal"} {
		if db, err := os.ReadFile(filepath.Join(p.dirA, name)); err == nil && strings.Contains(string(db), p.token) {
			t.Fatalf("A's %s holds the link's token", name)
		}
	}
}

// proxy stands in front of B's engine on a loopback address of its own,
// with B's link grants, and passes every call to B's own endpoint but
// those a test has fail: as B's handlers would, over the wire.
type proxy struct {
	t    *testing.T
	p    *remotes // B as it is now, across its restarts
	addr string
	dirB string

	mu        sync.Mutex
	submitted chan struct{}   // closed once a create reached B, which it then never answers
	hold      bool            // hold each create's answer until its call ends
	openErr   error           // what a subscribe fails with
	replyErr  func(int) error // what the nth child.reply fails with, from 1; nil passes it
	replies   int             // child.reply calls
	snapshots int             // snapshots B sent through the proxy
	refused   int             // handshakes refused
	infos     int             // calls of info, one each dial makes
	cancel    context.CancelFunc
	served    chan struct{}
}

func newProxy(t *testing.T, p *remotes) *proxy {
	t.Helper()
	x := &proxy{t: t, p: p, addr: freeAddr(t), dirB: p.dirB, submitted: make(chan struct{})}
	x.start()
	t.Cleanup(x.stop)
	return x
}

func (x *proxy) inner() endpoint { return endpoint{x.p.b.host, x.p.b, true} }

func (x *proxy) start() {
	x.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	tn := transport.Tailnet{Address: x.addr}
	ln, err := tn.Listen(ctx)
	if err != nil {
		x.t.Fatal(err)
	}
	g := &grants{dir: x.dirB, base: ctx}
	go g.watch(ctx)
	authorize := func(r *http.Request, proved func(transport.Key) bool) (transport.Grant, error) {
		gr, err := g.authorize(r, proved)
		if err != nil {
			x.mu.Lock()
			x.refused++
			x.mu.Unlock()
		}
		return gr, err
	}
	srv := &transport.Server{Transport: tn, Endpoint: x, Replies: x, Authorize: authorize}
	served := make(chan struct{})
	go func() { defer close(served); srv.Serve(ctx, ln) }()
	x.mu.Lock()
	x.cancel, x.served = cancel, served
	x.mu.Unlock()
}

func (x *proxy) stop() {
	x.mu.Lock()
	cancel, served := x.cancel, x.served
	x.cancel = nil
	x.mu.Unlock()
	if cancel != nil {
		cancel()
		<-served
	}
}

func (x *proxy) Info(ctx context.Context) (api.Environment, error) {
	x.mu.Lock()
	x.infos++
	x.mu.Unlock()
	return x.inner().Info(ctx)
}
func (x *proxy) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	return x.inner().Read(ctx, q)
}

func (x *proxy) Submit(ctx context.Context, c api.Command) (api.Receipt, error) {
	r, err := x.inner().Submit(ctx, c)
	x.mu.Lock()
	hold := x.hold
	x.mu.Unlock()
	if !hold {
		return r, err
	}
	select {
	case <-x.submitted:
	default:
		close(x.submitted)
	}
	<-ctx.Done()
	return api.Receipt{}, ctx.Err()
}

func (x *proxy) Open(ctx context.Context, s api.Subscription) (api.Stream, error) {
	x.mu.Lock()
	err := x.openErr
	x.mu.Unlock()
	if err != nil {
		return nil, err
	}
	st, err := x.inner().Open(ctx, s)
	if err != nil {
		return nil, err
	}
	return snapshotCounted{st, x}, nil
}

type snapshotCounted struct {
	api.Stream
	x *proxy
}

func (c snapshotCounted) Next(ctx context.Context) (api.StreamItem, error) {
	it, err := c.Stream.Next(ctx)
	if err == nil && it.Kind == api.StreamSnapshot {
		c.x.mu.Lock()
		c.x.snapshots++
		c.x.mu.Unlock()
	}
	return it, err
}

func (x *proxy) ChildReply(ctx context.Context, q api.ChildReplyQuery) (api.ChildReply, error) {
	x.mu.Lock()
	x.replies++
	n, fail := x.replies, x.replyErr
	x.mu.Unlock()
	if fail != nil {
		if err := fail(n); err != nil {
			return api.ChildReply{}, err
		}
	}
	return x.inner().ChildReply(ctx, q)
}

func (x *proxy) count() (replies, snapshots int) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.replies, x.snapshots
}

// A crash after A's create reached B and before A heard B's receipt
// leaves the create leased; the next boot releases it, and its replay
// finds B's receipt: one thread there.
func TestACrashBeforeBsReceiptMakesOneThreadThere(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	x := newProxy(t, p)
	x.hold = true
	p.machine(x.addr)
	kid := p.place("there")
	select {
	case <-x.submitted:
	case <-time.After(30 * time.Second):
		t.Fatal("the create never reached b")
	}
	if there := p.there(); len(there) != 1 {
		t.Fatalf("b's threads once the create reached it: %+v; want one", there)
	}
	p.stopA()
	st, err := store.Open(filepath.Join(p.dirA, "engine.db"))
	if err != nil {
		t.Fatal(err)
	}
	leased, err := st.Leased(context.Background())
	st.Close()
	if err != nil || len(leased) != 1 || engine.EffectKind(leased[0].Body) != "remote.create" {
		t.Fatalf("after the crash, leased %+v, %v; want the remote create", leased, err)
	}

	p.machine(p.addrB)
	p.startA()
	r := p.bound(kid)
	if there := p.there(); len(there) != 1 || there[0].ID != r.Thread {
		t.Fatalf("b's threads after the replay: %+v; want only %v", there, r.Thread)
	}
	until(t, "the report", func() bool { reports, _ := p.reports(kid); return len(reports) == 1 })
	folded(t, p.a.store)
}

// A child on B that asks a question wakes A's parent once, saying it is
// answered on B; a restart of A delivers it no second time.
func TestABlockedRemoteChildWakesItsParentOnce(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	kid := p.place("asks")
	until(t, "the wake", func() bool { _, wakes := p.reports(kid); return len(wakes) == 1 })
	reports, wakes := p.reports(kid)
	if len(reports) != 1 || reports[0].Kind != api.ReportBlocked || !strings.HasPrefix(reports[0].Outcome, "Needs you on b: Asks: Which store") {
		t.Fatalf("reports %+v; want one blocked, needing you on b", reports)
	}
	if !strings.Contains(wakes[0].Text, "waiting_on_question") || !strings.Contains(wakes[0].Text, "It runs on b") {
		t.Fatalf("the wake: %q", wakes[0].Text)
	}
	if r := p.placeholder(kid).Remote; r.Slot != api.RunWaiting {
		t.Fatalf("the child as A has it: %+v; want waiting", r)
	}
	p.stopA()
	p.startA()
	until(t, "the follower back", func() bool {
		_, err := p.a.store.Thread(context.Background(), kid)
		return err == nil
	})
	time.Sleep(time.Second)
	if reports, wakes := p.reports(kid); len(reports) != 1 || len(wakes) != 1 {
		t.Fatalf("after a restart of A: reports %+v, %d wakes; want one each", reports, len(wakes))
	}
	folded(t, p.a.store)
}

// A restart of A between a silent admission on B and its end reports
// nothing for that run: A stored that it was silent.
func TestARestartBetweenASilentAdmissionAndItsEndReportsNothing(t *testing.T) {
	release, held := make(chan struct{}), make(chan struct{})
	var once sync.Once
	adB := fake.New(fake.Turn(false))
	adB.Confirms = func(spec provider.SessionSpec) []provider.Event {
		if spec.ResumeOnly {
			once.Do(func() { close(held) })
			<-release
			return nil
		}
		return []provider.Event{provider.Context{Used: 10, Window: 100, Model: spec.Model.Model, CacheExpires: time.Now().Add(engine.ClaudeCacheTTL/10 + time.Second)}}
	}
	p := newRemotes(t, adB, "claude", engine.Limits{})
	defer once.Do(func() {})
	kid := p.place("there")
	until(t, "the first report", func() bool { reports, _ := p.reports(kid); return len(reports) == 1 })
	select {
	case <-held:
	case <-time.After(30 * time.Second):
		t.Fatal("b never compacted the child's thread")
	}
	until(t, "A's sight of the silent run", func() bool { r := p.placeholder(kid).Remote; return r.Silent && r.Slot == api.RunStarting })
	r := p.bound(kid)
	p.stopA()
	close(release)
	until(t, "the compaction's end on b", func() bool {
		for _, e := range eventsOf(t, p.b.Endpoint(), r.Thread) {
			if e.Type == "turn.ended" {
				if b, _ := e.Decode(); b.(api.TurnEnded).Run == r.Run {
					return true
				}
			}
		}
		return false
	})
	p.startA()
	until(t, "A's sight of the silent run's end", func() bool { return p.placeholder(kid).Remote.Slot == api.RunIdle })
	time.Sleep(300 * time.Millisecond)
	if reports, wakes := p.reports(kid); len(reports) != 1 || len(wakes) != 1 {
		t.Fatalf("after the silent run: reports %+v, %d wakes; want the first only", reports, len(wakes))
	}
	folded(t, p.a.store)
}

// renames has B's person rename B's child n times: events of the child's
// that report nothing.
func (p *remotes) renames(thread api.ID, n int) {
	p.t.Helper()
	for i := range n {
		if _, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadRename{Title: "renamed " + string(rune('a'+i%26)) + strings.Repeat("!", i/26)}}); err != nil {
			p.t.Fatal(err)
		}
	}
}

// message has B's person send B's child another turn, which ends done.
func (p *remotes) message(thread api.ID) {
	p.t.Helper()
	if _, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "and again", Mode: api.AfterActive}}); err != nil {
		p.t.Fatal(err)
	}
}

// A gap too large to replay, with no report in it, is read from one
// snapshot whose fence A commits; the report that follows live is
// delivered once, and no second snapshot is asked for.
func TestAnOversizedGapReportsOnceAndSnapshotsOnce(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	x := newProxy(t, p)
	p.machine(x.addr)
	kid := p.place("there")
	until(t, "the first report", func() bool { reports, _ := p.reports(kid); return len(reports) == 1 })
	there := p.bound(kid).Thread
	until(t, "the child idle there", func() bool { return p.placeholder(kid).Remote.Slot == api.RunIdle })
	p.stopA()
	p.renames(there, 200)
	_, before := x.count()
	p.startA()
	until(t, "the snapshot's fence", func() bool {
		snap, _ := p.b.Endpoint().Read(context.Background(), api.Query{Thread: there})
		return p.placeholder(kid).Remote.Cursor.Seq >= snap.Cursor.Seq
	})
	p.message(there)
	until(t, "the live report", func() bool { reports, _ := p.reports(kid); return len(reports) == 2 })
	time.Sleep(300 * time.Millisecond)
	reports, wakes := p.reports(kid)
	if len(reports) != 2 || len(wakes) != 2 {
		t.Fatalf("reports %+v, %d wakes; want the first and the live one", reports, len(wakes))
	}
	if _, after := x.count(); after-before != 1 {
		t.Fatalf("%d snapshots since A came back; want one", after-before)
	}
	folded(t, p.a.store)
}

// copyFiles copies the database files of dir's engine to backup, or back.
func copyFiles(t *testing.T, from, to string) {
	t.Helper()
	for _, name := range []string{"engine.db", "engine.db-wal", "engine.db-shm"} {
		os.Remove(filepath.Join(to, name))
		in, err := os.Open(filepath.Join(from, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.Create(filepath.Join(to, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(out, in); err != nil {
			t.Fatal(err)
		}
		in.Close()
		out.Close()
	}
}

// A restore of B's database gives its log a new id, and A reads the
// child again from a snapshot: what it delivered it delivers no second
// time, and a run after is still reported.
func TestARestoreOfBReportsNothingTwice(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	kid := p.place("there")
	until(t, "the first report", func() bool { reports, _ := p.reports(kid); return len(reports) == 1 })
	there := p.bound(kid).Thread
	until(t, "the child idle there", func() bool { return p.placeholder(kid).Remote.Slot == api.RunIdle })
	backup := t.TempDir()
	p.stopB()
	copyFiles(t, p.dirB, backup)
	p.startB()
	p.renames(there, 3)
	p.stopB()
	copyFiles(t, backup, p.dirB)
	p.startB()
	env, err := p.b.Endpoint().Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	until(t, "A's sight of b's new log", func() bool { return p.placeholder(kid).Remote.Cursor.Log == env.Log })
	time.Sleep(300 * time.Millisecond)
	if reports, _ := p.reports(kid); len(reports) != 1 {
		t.Fatalf("reports after the restore: %+v; want the one before it", reports)
	}
	p.message(there)
	until(t, "a report after the restore", func() bool { reports, _ := p.reports(kid); return len(reports) == 2 })
	folded(t, p.a.store)
}

// The child's lost report: one, failed, saying why.
func (p *remotes) lostOnce(kid api.ID, why string) {
	p.t.Helper()
	if reason := p.lost(kid); !strings.Contains(reason, why) {
		p.t.Fatalf("lost for %q; want %q", reason, why)
	}
	until(p.t, "the lost report", func() bool {
		reports, _ := p.reports(kid)
		return len(reports) > 0 && reports[len(reports)-1].Kind == api.ReportFailed
	})
	time.Sleep(300 * time.Millisecond)
	reports, _ := p.reports(kid)
	failed := 0
	for _, r := range reports {
		if r.Kind == api.ReportFailed {
			failed++
			if !strings.Contains(r.Outcome, why) {
				p.t.Fatalf("the failed report %+v; want it to say %q", r, why)
			}
		}
	}
	if failed != 1 {
		p.t.Fatalf("reports %+v; want one failed", reports)
	}
	folded(p.t, p.a.store)
}

// Three internal errors in a row from B's handler of child.reply lose the
// child, as one that may still run there.
func TestThreeInternalErrorsFromBLoseTheChild(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	x := newProxy(t, p)
	x.replyErr = func(int) error { return errors.New("b broke") }
	p.machine(x.addr)
	kid := p.place("there")
	p.lostOnce(kid, "failed repeatedly, so it may still run there")
	if replies, _ := x.count(); replies != 3 {
		t.Fatalf("%d child.reply calls; want 3", replies)
	}
}

// A refusal of the link's scope by B, on a subscribe or a child.reply,
// loses the child at once.
func TestAScopeRefusalIsFinal(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*proxy)
	}{
		{"subscribe", func(x *proxy) { x.openErr = api.ErrNotAllowed }},
		{"child.reply", func(x *proxy) { x.replyErr = func(int) error { return api.ErrNotAllowed } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
			x := newProxy(t, p)
			tc.set(x)
			p.machine(x.addr)
			kid := p.place("there")
			p.lostOnce(kid, "no longer lets this machine's link reach the child")
			if replies, _ := x.count(); tc.name == "child.reply" && replies != 1 {
				t.Fatalf("%d child.reply calls; want one", replies)
			}
		})
	}
}

// Two internal errors, a network failure, and two more lose nothing: the
// network failure starts the count over, and the reply after is reported.
func TestNetworkFailuresBetweenDefectsResetTheCount(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	x := newProxy(t, p)
	down := make(chan struct{})
	x.replyErr = func(n int) error {
		if n == 2 {
			close(down)
		}
		if n <= 4 {
			return errors.New("b broke")
		}
		return nil
	}
	p.machine(x.addr)
	kid := p.place("there")
	select {
	case <-down:
	case <-time.After(30 * time.Second):
		t.Fatal("no second child.reply")
	}
	x.stop()
	time.Sleep(time.Second)
	x.start()
	until(t, "the report", func() bool { reports, _ := p.reports(kid); return len(reports) == 1 })
	reports, wakes := p.reports(kid)
	if reports[0].Kind != api.ReportDone || len(wakes) != 1 || p.placeholder(kid).Remote.State != engine.RemoteBoundState {
		t.Fatalf("reports %+v, %d wakes, remote %+v; want one done and the child followed", reports, len(wakes), p.placeholder(kid).Remote)
	}
	if replies, _ := x.count(); replies != 5 {
		t.Fatalf("%d child.reply calls; want 5", replies)
	}
}

// A revoke on B while the child runs closes A's socket, and A's dials are
// refused from then on: the second refusal loses the child.
func TestARevokeOnBLosesTheChildAfterTwoRefusedDials(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	x := newProxy(t, p)
	p.machine(x.addr)
	kid := p.place("asks")
	until(t, "the blocked report", func() bool { reports, _ := p.reports(kid); return len(reports) == 1 })
	if err := Revoke(p.dirB, "a"); err != nil {
		t.Fatal(err)
	}
	p.lostOnce(kid, "b no longer accepts this machine's link")
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.refused != 2 {
		t.Fatalf("%d handshakes refused; want 2", x.refused)
	}
}

// A machine whose address now answers as another engine, or that is no
// longer in machines.json, loses the child at once.
func TestAMachineThatIsAnotherOrGoneLosesTheChild(t *testing.T) {
	t.Run("another", func(t *testing.T) {
		p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
		kid := p.place("asks")
		until(t, "the blocked report", func() bool { reports, _ := p.reports(kid); return len(reports) == 1 })
		dirC, addrC := stateDir(t), freeAddr(t)
		allowChildren(t, dirC, api.AccessAutoEdits)
		tokenC, _, err := PairLink(dirC, "a", api.Link{Access: api.AccessAutoEdits})
		if err != nil {
			t.Fatal(err)
		}
		c, err := Start(context.Background(), Config{Dir: dirC, Hub: true, Epoch: 1, Listen: []string{addrC}, Providers: provider.Mux{"fake": fake.New(nil)}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		b, err := json.Marshal([]Machine{{Label: "b", Server: addrC, Engine: p.engineB, Link: p.link.ID, Token: tokenC}})
		if err != nil {
			t.Fatal(err)
		}
		writeMachines(t, p.dirA, b)
		p.stopB()
		p.lostOnce(kid, "b is now another machine")
	})
	t.Run("gone", func(t *testing.T) {
		p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
		kid := p.place("asks")
		until(t, "the blocked report", func() bool { reports, _ := p.reports(kid); return len(reports) == 1 })
		writeMachines(t, p.dirA, []byte("[]"))
		p.stopB()
		p.lostOnce(kid, "its machine was removed from this machine's list")
	})
}

// A worktree B can't make for the child reports it failed, once, and the
// child takes no place at work: it never runs there.
func TestAWorktreeBCantMakeReportsFailedOnce(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{Working: 1})
	if err := os.WriteFile(filepath.Join(p.dirB, "worktrees"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	kid := p.place("there")
	there := p.bound(kid).Thread
	until(t, "the failed report", func() bool { reports, _ := p.reports(kid); return len(reports) == 1 })
	p.parentIdle()
	p.stopA()
	p.renames(there, 140)
	p.startA()
	snap, err := p.b.Endpoint().Read(context.Background(), api.Query{Thread: there})
	if err != nil {
		t.Fatal(err)
	}
	until(t, "A's read of the snapshot", func() bool { return p.placeholder(kid).Remote.Cursor.Seq >= snap.Cursor.Seq })
	time.Sleep(300 * time.Millisecond)
	reports, wakes := p.reports(kid)
	if len(reports) != 1 || reports[0].Kind != api.ReportFailed || !strings.Contains(reports[0].Outcome, "worktree") || len(wakes) != 1 {
		t.Fatalf("reports %+v, %d wakes; want one failed, about the worktree", reports, len(wakes))
	}
	if _, errs := spawn(t, p.a.Endpoint(), p.adA, p.parent, fake.Child{Title: "here", Task: "go"}); errs[0] != nil {
		t.Fatalf("a child once the remote one failed: %v; want it started", errs[0])
	}
	folded(t, p.a.store)
}

// A child bound on B takes its place at work before A has read anything
// of it there.
func TestABoundChildIsAtWorkBeforeItsFirstRead(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{Working: 1})
	x := newProxy(t, p)
	x.openErr = api.ErrProtocol
	p.machine(x.addr)
	kid := p.place("first")
	p.bound(kid)
	_, errs := spawn(t, p.a.Endpoint(), p.adA, p.parent, fake.Child{Title: "second", Task: "another"})
	refused(t, "a second child while the bound one hasn't been read", errs[0], "1 child threads of this thread are at work")
}

// A machine removed from machines.json while A follows a child there
// loses the child at the next thing A would take from it.
func TestARemovedMachineLosesTheChildItFollows(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	kid := p.place("there")
	there := p.bound(kid).Thread
	until(t, "the first report", func() bool { reports, _ := p.reports(kid); return len(reports) == 1 })
	if err := RemoveMachine(p.dirA, "b"); err != nil {
		t.Fatal(err)
	}
	p.message(there)
	p.lostOnce(kid, "its machine was removed from this machine's list")
	if reports, _ := p.reports(kid); len(reports) != 2 {
		t.Fatalf("reports %+v; want the first and the lost one", reports)
	}
}

// A restore of B while A asks it for a run's reply leaves B without that
// run: A reads the child again from B's restored log and keeps it.
func TestARestoreOfBWhileAReplyIsAskedKeepsTheChild(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	kid := p.place("there")
	until(t, "the first report", func() bool { reports, _ := p.reports(kid); return len(reports) == 1 })
	there := p.bound(kid).Thread
	until(t, "the child idle there", func() bool { return p.placeholder(kid).Remote.Slot == api.RunIdle })
	p.stopA()
	p.stopB()
	backup := t.TempDir()
	copyFiles(t, p.dirB, backup)
	p.startB()
	x := newProxy(t, p)
	asked, restored := make(chan struct{}), make(chan struct{})
	x.replyErr = func(n int) error {
		if n == 1 {
			close(asked)
			<-restored
		}
		return nil
	}
	p.machine(x.addr)
	p.startA()
	p.message(there)
	select {
	case <-asked:
	case <-time.After(30 * time.Second):
		t.Fatal("A never asked for the reply")
	}
	p.stopB()
	copyFiles(t, backup, p.dirB)
	p.startB()
	close(restored)
	env, err := p.b.Endpoint().Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	until(t, "A's read of b's restored log", func() bool { return p.placeholder(kid).Remote.Cursor.Log == env.Log })
	time.Sleep(300 * time.Millisecond)
	if r := p.placeholder(kid).Remote; r.State != engine.RemoteBoundState {
		t.Fatalf("after the restore the placeholder is %+v; want it followed", r)
	}
	if reports, _ := p.reports(kid); len(reports) != 1 {
		t.Fatalf("reports %+v; want only the one before the restore", reports)
	}
	p.message(there)
	until(t, "a report after the restore", func() bool { reports, _ := p.reports(kid); return len(reports) == 2 })
	folded(t, p.a.store)
}

// A child B settles before its first run takes no place at work here.
func TestAChildSettledOnBBeforeItsFirstRunTakesNoPlace(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{Working: 1})
	gate := filepath.Join(t.TempDir(), "gate")
	defer os.WriteFile(gate, nil, 0600)
	hook := "#!/bin/sh\nexec >/dev/null 2>&1\ntouch '" + gate + ".held'\nwhile [ ! -f '" + gate + "' ]; do sleep 0.02; done\n"
	if err := os.WriteFile(filepath.Join(p.repo, ".git", "hooks", "post-checkout"), []byte(hook), 0755); err != nil {
		t.Fatal(err)
	}
	kid := p.place("settled")
	r := p.bound(kid)
	until(t, "B inside git checkout", func() bool { _, err := os.Stat(gate + ".held"); return err == nil })
	if _, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: r.Thread, Body: api.InboxSet{Disposition: api.Disposition{State: api.Settled}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	until(t, "B settled and worktree ready", func() bool {
		th, e := p.b.store.Thread(context.Background(), r.Thread)
		return e == nil && th.Disposition.State == api.Settled && th.Workspace != "" && len(th.Runs) == 0
	})
	p.stopA()
	p.renames(r.Thread, 140)
	p.startA()
	s, e := p.b.Endpoint().Read(context.Background(), api.Query{Thread: r.Thread})
	if e != nil {
		t.Fatal(e)
	}
	until(t, "A consumed the complete settled snapshot", func() bool { return p.placeholder(kid).Remote.Cursor.Seq >= s.Cursor.Seq })
	_, errs := spawn(t, p.a.Endpoint(), p.adA, p.parent, fake.Child{Title: "another", Task: "go"})
	if errs[0] != nil {
		t.Fatalf("B settled, no run was admitted, A folded the snapshot and still refuses the next child: %v", errs[0])
	}
}

// A machine removed while its child waits on a question loses the child
// though nothing more happens there, and frees its place.
func TestARemovedMachineFreesItsWaitingChildsPlace(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{Working: 1})
	kid := p.place("asks")
	until(t, "waiting on a question", func() bool { return p.placeholder(kid).Remote.Slot == api.RunWaiting })
	if err := RemoveMachine(p.dirA, "b"); err != nil {
		t.Fatal(err)
	}
	p.renames(p.bound(kid).Thread, 3)
	time.Sleep(1200 * time.Millisecond)
	_, es := spawn(t, p.a.Endpoint(), p.adA, p.parent, fake.Child{Title: "another", Task: "go"})
	if es[0] != nil {
		t.Fatalf("removed B, received no further run edge, child remains %s, next child refused: %v", p.placeholder(kid).Remote.State, es[0])
	}
}

// B's restart with continue-after-restart on continues its person's run
// it lost, and never a link's child: continue-after-restart applies to no
// child, whichever machine its parent is on.
func TestBContinuesNoLinkChildAfterARestart(t *testing.T) {
	adB := fake.New(fake.Turn(false))
	adB.HoldStart = true
	p := newRemotes(t, adB, "fake", engine.Limits{})
	snap, err := p.b.Endpoint().Read(context.Background(), api.Query{})
	if err != nil {
		t.Fatal(err)
	}
	var mine api.ID
	for _, th := range snap.Threads {
		if th.Title == "mine" {
			mine = th.ID
		}
	}
	if _, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: mine, Body: api.MessageDispatch{Text: "go", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	kid := p.place("there")
	there := p.bound(kid).Thread
	until(t, "both runs started on b", func() bool { return adB.Starts() >= 2 })
	p.stopA()
	p.stopB()
	next := fake.New(fake.Turn(false))
	next.HoldStart = true
	p.adB, p.continueB = next, true
	p.startB()
	until(t, "b's person's run continued", func() bool { return next.Starts() >= 1 })
	time.Sleep(time.Second)
	if n := next.Starts(); n != 1 {
		t.Fatalf("%d runs started on b after its restart; want only its person's", n)
	}
	for _, e := range eventsOf(t, p.b.Endpoint(), there) {
		if b, _ := e.Decode(); b != nil {
			if u, ok := b.(api.TurnUncertain); ok && u.Continue {
				t.Fatalf("the link's child was lost as one to continue: %+v", u)
			}
		}
	}
}
