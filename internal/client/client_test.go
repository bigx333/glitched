package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/transport"
)

// gui stands in for the GUI's thread: Post queues, and the test runs the
// queue between its reads of the manager's state.
type gui struct {
	mu   sync.Mutex
	q    []func()
	wake chan struct{}
}

func (g *gui) post(fn func()) {
	g.mu.Lock()
	g.q = append(g.q, fn)
	g.mu.Unlock()
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

// until runs posts until ok holds, checking it after each as a GUI could
// look between them, and leaves the posts after that one queued. state
// describes what the test saw when it gives up.
func (g *gui) until(t *testing.T, what string, ok func() bool, state ...func() string) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		g.mu.Lock()
		q := g.q
		g.q = nil
		g.mu.Unlock()
		for i, fn := range q {
			fn()
			if ok() {
				g.mu.Lock()
				g.q = append(q[i+1:], g.q...)
				g.mu.Unlock()
				return
			}
		}
		if len(q) == 0 && ok() {
			return
		}
		select {
		case <-g.wake:
		case <-deadline:
			var seen []string
			for _, s := range state {
				seen = append(seen, s())
			}
			t.Fatalf("timed out waiting for %s\n%s", what, strings.Join(seen, "\n"))
		}
	}
}

// stateDir is short: unix socket paths are limited to about 100 bytes.
func stateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// engine serves dir as `glitched serve -listen` would.
func engine(t *testing.T, dir string, listen ...string) *daemon.Daemon {
	t.Helper()
	cfg, err := daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen = listen
	d, err := daemon.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func manager(t *testing.T, dir string) (*Manager, *gui) {
	t.Helper()
	g := &gui{wake: make(chan struct{}, 1)}
	m := New(Config{Dir: dir, Post: g.post})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m, g
}

func online(t *testing.T, g *gui, e *Env) {
	t.Helper()
	g.until(t, e.Name+" online", func() bool { return e.Status == Status(Online{}) }, func() string { return describeStatus(e.Status) })
}

func describeStatus(s Status) string {
	if o, ok := s.(Offline); ok {
		return "offline: " + o.Err.Error()
	}
	return fmt.Sprintf("%T", s)
}

func timeout(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

var fakeModel = api.ModelRef{Provider: "fake", Model: "fake"}

func TestStartEmbedsAnEngineWhenNoneAnswers(t *testing.T) {
	before := runtime.NumGoroutine()
	dir := stateDir(t)
	g := &gui{wake: make(chan struct{}, 1)}
	m := New(Config{Dir: dir, Post: g.post})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	env := m.Envs()[0]
	if env.Kind != Local {
		t.Fatalf("the first env is %v, want the local one", env.Kind)
	}
	online(t, g, env)
	id := m.Create(env, "/work", "open", fakeModel, api.AccessAsk)
	m.OpenThread(env, id)
	g.until(t, "the open thread's transcript", func() bool { return env.Threads[id].Transcript.Cursor.Seq > 0 })

	cli := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	info, err := cli.Info(timeout(t))
	cli.Close()
	if err != nil || info.ID != env.ID || info.Name != env.Name {
		t.Fatalf("the socket answers %+v %v, want the embedded engine %s %q", info, err, env.ID, env.Name)
	}

	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return: a goroutine outlived the manager")
	}
	cli = transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_, err = cli.Info(ctx)
	cancel()
	cli.Close()
	if err == nil {
		t.Fatal("the socket still answers after Close, want the embedded engine gone")
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("%d goroutines after Close, %d before:\n%s", runtime.NumGoroutine(), before, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Close released the lock.
	engine(t, dir)
}

// Two GUIs start on one Dir together. One embeds the engine; the other
// waits for it to answer instead of showing the engine offline.
func TestTwoGUIsStartTogether(t *testing.T) {
	for range 5 {
		dir := stateDir(t)
		a, ga := manager(t, dir)
		b, gb := manager(t, dir)
		for _, g := range []struct {
			*gui
			env *Env
		}{{ga, a.Envs()[0]}, {gb, b.Envs()[0]}} {
			g.until(t, "online", func() bool {
				if o, ok := g.env.Status.(Offline); ok {
					t.Fatalf("offline on the way: %v", o.Err)
				}
				return g.env.Status == Status(Online{})
			})
		}
	}
}

func TestStartAttachesToTheEngineThatAnswers(t *testing.T) {
	dir := stateDir(t)
	d := engine(t, dir)
	m, g := manager(t, dir)
	env := m.Envs()[0]
	online(t, g, env)
	info, _ := d.Endpoint().Info(timeout(t))
	if env.ID != info.ID {
		t.Fatalf("attached to host %s, want the running engine %s", env.ID, info.ID)
	}

	id := m.Create(env, "/work", "attached", fakeModel, api.AccessAsk)
	g.until(t, "the engine's view of the thread", func() bool {
		th := env.Threads[id]
		return th.View.Revision > 0 && len(th.Pending) == 0
	})
	if snap, err := d.Endpoint().Read(timeout(t), api.Query{Thread: id}); err != nil || snap.Threads[0].Title != "attached" {
		t.Fatalf("the running engine holds %+v %v, want the thread created through the manager", snap.Threads, err)
	}

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	cli := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer cli.Close()
	if _, err := cli.Info(timeout(t)); err != nil {
		t.Fatalf("the engine the manager attached to stopped with it: %v", err)
	}
}

// The GUI embeds an engine where `glitched serve -epoch 2` ran before. The
// hub fences an epoch below the last it saw, so it takes the embedded log
// only at serve's epoch.
func TestAnEmbeddedEngineUploadsToTheHubServeUsed(t *testing.T) {
	dir := stateDir(t)
	cfg, err := daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Epoch = 2
	served, err := daemon.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := served.Endpoint().Submit(timeout(t), api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Workspace: "/w", Title: "served", Model: fakeModel}}); err != nil {
		t.Fatal(err)
	}
	waitUploaded(t, dir)
	served.Close()

	m, g := manager(t, dir)
	env := m.Envs()[0]
	online(t, g, env)
	id := m.Create(env, "/w", "embedded", fakeModel, api.AccessAsk)
	g.until(t, "the create confirmed", func() bool { th := env.Threads[id]; return th.known && len(th.Pending) == 0 })
	waitUploaded(t, dir)
}

// waitUploaded returns once the hub under dir holds the whole log of the
// engine serving dir.
func waitUploaded(t *testing.T, dir string) {
	t.Helper()
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer ep.Close()
	hc := transport.NewHubClient(daemon.Socket(dir), api.ID{})
	defer hc.Close()
	info, err := ep.Info(timeout(t))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ep.Read(timeout(t), api.Query{})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		n, err := hc.Acked(timeout(t), info.Log)
		if err == nil && n >= snap.Cursor.Seq {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the hub holds %d of the log's %d events (%v)", n, snap.Cursor.Seq, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The engine the GUI attached to stops. Until the GUI embeds its own, a
// command has nowhere to go and fails at once; once it has, a retry lands.
func TestACommandWaitsForTheNextEngine(t *testing.T) {
	dir := stateDir(t)
	d := engine(t, dir)
	m, g := manager(t, dir)
	env := m.Envs()[0]
	online(t, g, env)
	id := m.Create(env, "/work", "hello", fakeModel, api.AccessAsk)
	g.until(t, "the create confirmed", func() bool { th := env.Threads[id]; return th.known && len(th.Pending) == 0 })

	d.Close()
	g.until(t, "offline", func() bool { _, ok := env.Status.(Offline); return ok })
	msg := m.Send(env, id, "hello", api.AfterActive)
	g.until(t, "the message failed", func() bool { p := env.Threads[id].Pending; return len(p) == 1 && p[0].Err != nil })
	if p := env.Threads[id].Pending[0]; !errors.Is(p.Err, errNoEngine) || p.Terminal() {
		t.Fatalf("with no engine the message failed with %v, want %v and worth a retry", p.Err, errNoEngine)
	}

	online(t, g, env)
	m.Retry(env, id, msg)
	g.until(t, "the retry landed", func() bool { return len(env.Threads[id].Pending) == 0 }, func() string { return describePending(env.Threads[id].Pending) })
}

// longThread is an engine holding one thread with a long log.
type longThread struct {
	view api.Thread
	log  api.Snapshot
}

func (l *longThread) Info(context.Context) (api.Environment, error) {
	return api.Environment{ID: api.NewID(), Name: "long", Protocol: api.Protocol, Log: testLog}, nil
}

func (l *longThread) Read(context.Context, api.Query) (api.Snapshot, error) {
	return api.Snapshot{}, errors.ErrUnsupported
}

func (l *longThread) Submit(context.Context, api.Command) (api.Receipt, error) {
	return api.Receipt{}, errors.ErrUnsupported
}

func (l *longThread) Open(_ context.Context, s api.Subscription) (api.Stream, error) {
	if s.Thread.IsZero() {
		return &items{{Kind: api.StreamSnapshot, Snapshot: &api.Snapshot{Cursor: l.log.Cursor, Threads: []api.Thread{l.view}}}}, nil
	}
	return &items{{Kind: api.StreamSnapshot, Snapshot: &l.log}}, nil
}

// serveOn serves ep on the socket under dir, as the engine there would,
// holding its lock.
func serveOn(t *testing.T, dir string, ep api.Endpoint) {
	holdEngineLock(t, dir)
	sock := daemon.Socket(dir)
	ln, err := sock.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		(&transport.Server{Transport: sock, Endpoint: ep}).Serve(ctx, ln)
	}()
	t.Cleanup(func() { stop(); <-served })
}

// items streams its items, then nothing until the stream closes.
type items []api.StreamItem

func (s *items) Next(ctx context.Context) (api.StreamItem, error) {
	if len(*s) == 0 {
		<-ctx.Done()
		return api.StreamItem{}, ctx.Err()
	}
	it := (*s)[0]
	*s = (*s)[1:]
	return it, nil
}

func (s *items) Close() error { return nil }

// Opening a thread with a long log leaves the GUI's thread free: the fold
// happens before the post, which only swaps the transcript in.
func TestOpeningALongThreadLeavesThePostThreadFree(t *testing.T) {
	var evs []api.Event
	for turn := range 20 {
		for _, b := range longRun(500) {
			if q, ok := b.(api.MessageQueued); ok {
				q.Pos = int64(turn)
				b = q
			}
			evs = append(evs, event(len(evs)+1, b))
		}
	}
	id := api.NewID()
	eng := &longThread{view: api.Thread{ID: id, Title: "long"}, log: api.Snapshot{Cursor: evs[len(evs)-1].Cursor, Events: evs}}
	before := allocated()
	fold(eng.log)
	folding := allocated() - before

	dir := stateDir(t)
	serveOn(t, dir, eng)
	g := &gui{wake: make(chan struct{}, 1)}
	var worst uint64
	m := New(Config{Dir: dir, Post: func(fn func()) {
		g.post(func() {
			before := allocated()
			fn()
			worst = max(worst, allocated()-before)
		})
	}})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	env := m.Envs()[0]
	online(t, g, env)
	m.OpenThread(env, id)
	g.until(t, "the transcript", func() bool { return env.Threads[id].Transcript.Cursor == eng.log.Cursor })
	if len(env.Threads[id].Transcript.Entries) != 40 {
		t.Fatalf("the transcript has %d entries, want the 20 turns' messages and texts", len(env.Threads[id].Transcript.Entries))
	}
	if worst > folding/20 {
		t.Fatalf("a post allocated %d bytes, against %d to fold the log: the fold ran on the GUI's thread", worst, folding)
	}
}

// olderEngine speaks protocol 1, which has no deleted threads.
type olderEngine struct{ *longThread }

func (olderEngine) Info(context.Context) (api.Environment, error) {
	return api.Environment{ID: api.NewID(), Name: "older", Protocol: 1, Log: testLog}, nil
}

func TestAnEngineOfAnotherProtocolIsNotShown(t *testing.T) {
	dir := stateDir(t)
	serveOn(t, dir, olderEngine{&longThread{view: api.Thread{ID: api.NewID(), Title: "older"}}})
	m, g := manager(t, dir)
	env := m.Envs()[0]
	g.until(t, "an answer", func() bool { return env.Status != Status(Connecting{}) })
	if s, ok := env.Status.(Incompatible); !ok || s.Theirs != 1 || s.Ours != api.Protocol || len(env.Threads) != 0 {
		t.Fatalf("an engine of protocol 1 left the env %s with %d threads, want incompatible and none shown", describeStatus(env.Status), len(env.Threads))
	}
}

type unnamedEngine struct{ *longThread }

func (unnamedEngine) Info(context.Context) (api.Environment, error) {
	return api.Environment{Name: "unnamed", Protocol: api.Protocol, Log: testLog}, nil
}

// An engine that reports no ID claims none, and shows its threads.
func TestAnEngineWithNoIDShowsItsThreads(t *testing.T) {
	dir := stateDir(t)
	id := api.NewID()
	serveOn(t, dir, unnamedEngine{&longThread{view: api.Thread{ID: id, Title: "unnamed"}}})
	m, g := manager(t, dir)
	env := m.Envs()[0]
	online(t, g, env)
	if env.Threads[id] == nil {
		t.Fatalf("an engine with no ID shows %d threads, want its one", len(env.Threads))
	}
}

// finishTurn creates a thread, sends it a message and approves the edit
// the turn asks for, checking what the GUI sees on the way. It returns once
// the turn is over and the transcript shows all of it.
func finishTurn(t *testing.T, m *Manager, g *gui, env *Env) api.ID {
	t.Helper()
	id := m.Create(env, "/work", "hello", fakeModel, api.AccessAsk)
	th := env.Threads[id]
	if th == nil || th.View.Title != "hello" || len(th.Pending) != 1 {
		t.Fatalf("right after Create the thread is %+v, want it shown with its create pending", th)
	}
	m.OpenThread(env, id)
	msg := m.Send(env, id, "hello", api.AfterActive)
	if p := th.Pending[1]; p.Command != msg || p.Body != (api.MessageDispatch{Text: "hello", Mode: api.AfterActive}) {
		t.Fatalf("right after Send the pending row is %+v, want the message", p)
	}

	transcript := func() string { return strings.Join(lines(env.Threads[id].Transcript), "\n") }
	var req *RequestEntry
	g.until(t, "the approval", func() bool {
		for _, e := range env.Threads[id].Transcript.Entries {
			if r, ok := e.(*RequestEntry); ok && r.State == api.RequestPending {
				req = r
			}
		}
		return req != nil
	}, transcript)
	m.Respond(env, id, req.Request, AllowOnce)

	want := []string{
		`user "hello" sent`,
		`assistant "Looking at the code."`,
		`work read-1:completed`,
		`assistant " May I edit it?"`,
		`request approval delivered {"allow":true}`,
		`work edit-1:completed`,
		`assistant " Done."`,
	}
	g.until(t, "the finished turn", func() bool {
		th := env.Threads[id]
		return slices.Equal(lines(th.Transcript), want) && th.View.LastTurn != nil && th.View.Run == api.RunIdle && len(th.Pending) == 0
	}, transcript, func() string { return describePending(env.Threads[id].Pending) })
	if th := env.Threads[id]; th.View.LastTurn.Outcome != api.OutcomeCompleted {
		t.Fatalf("the turn ended %v, want completed", th.View.LastTurn.Outcome)
	}
	return id
}

// fresh is the transcript the engine at ep holds for the thread, folded anew.
func fresh(t *testing.T, ep api.Endpoint, thread api.ID) []string {
	t.Helper()
	snap, err := ep.Read(timeout(t), api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	tr := fold(snap)
	return lines(&tr)
}

// The second GUI on a Dir attaches to the engine the first embedded. When
// the first quits, the second embeds one on the same state, and the thread
// it has open carries on from the transcript it held.
func TestTheSecondGUIOnADirCarriesOnWhenTheFirstQuits(t *testing.T) {
	dir := stateDir(t)
	a, ga := manager(t, dir)
	online(t, ga, a.Envs()[0])
	b, g := manager(t, dir)
	env := b.Envs()[0]
	online(t, g, env)
	id := finishTurn(t, b, g, env)
	host := env.ID

	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	g.until(t, "offline", func() bool { _, ok := env.Status.(Offline); return ok })
	online(t, g, env)
	if env.ID != host {
		t.Fatalf("after the first GUI quit the env is host %s, want %s on the same state", env.ID, host)
	}
	b.Send(env, id, "again", api.AfterActive)
	var req *RequestEntry
	g.until(t, "the second approval", func() bool {
		req = nil
		for _, e := range env.Threads[id].Transcript.Entries {
			if r, ok := e.(*RequestEntry); ok && r.State == api.RequestPending {
				req = r
			}
		}
		return req != nil
	})
	b.Respond(env, id, req.Request, AllowOnce)
	cli := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer cli.Close()
	g.until(t, "the second turn in the transcript as the engine holds it", func() bool {
		th := env.Threads[id]
		return th.View.Run == api.RunIdle && len(th.Pending) == 0 && slices.Equal(lines(th.Transcript), fresh(t, cli, id))
	}, func() string { return strings.Join(lines(env.Threads[id].Transcript), "\n") })
}

func TestATurnRunsFromCreateThroughAnApproval(t *testing.T) {
	m, g := manager(t, stateDir(t))
	env := m.Envs()[0]
	online(t, g, env)
	id := finishTurn(t, m, g, env)

	until := time.Now().Add(time.Hour).Truncate(time.Second)
	m.SetDisposition(env, id, api.Disposition{State: api.Snoozed, Until: until})
	g.until(t, "the snooze", func() bool {
		th := env.Threads[id]
		return th.View.Disposition.State == api.Snoozed && th.View.Disposition.Until.Equal(until) && len(th.Pending) == 0
	})
	m.SetDisposition(env, id, api.Disposition{State: api.Settled})
	g.until(t, "the settle", func() bool {
		th := env.Threads[id]
		return th.View.Disposition == (api.Disposition{State: api.Settled}) && len(th.Pending) == 0
	})
	m.Rename(env, id, "Websocket loop")
	g.until(t, "the rename", func() bool {
		th := env.Threads[id]
		return th.View.Title == "Websocket loop" && len(th.Pending) == 0
	})
	m.Delete(env, id) // while its transcript is open

	g.until(t, "the delete", func() bool { return env.Threads[id] == nil })

}

// A StartNow is refused while a run is live. The same command lands once
// the run is over, so its failed row offers a retry.
func TestARefusedStartNowLandsOnRetryOnceTheRunEnds(t *testing.T) {
	m, g := manager(t, stateDir(t))
	env := m.Envs()[0]
	online(t, g, env)
	id := m.Create(env, "/work", "busy", fakeModel, api.AccessAsk)
	m.Send(env, id, "first", api.AfterActive)
	g.until(t, "the first run waiting on its approval", func() bool {
		return env.Threads[id].View.Run == api.RunWaiting && len(env.Threads[id].Pending) == 0
	})
	now := m.Send(env, id, "second", api.StartNow)
	g.until(t, "the StartNow refused", func() bool { p := env.Threads[id].Pending; return len(p) == 1 && p[0].Err != nil })
	if p := env.Threads[id].Pending[0]; !errors.As(p.Err, new(*api.RejectedError)) || p.Terminal() {
		t.Fatalf("a StartNow on a busy thread failed with %v (terminal %v), want a rejection worth a retry", p.Err, p.Terminal())
	}

	m.Interrupt(env, id)
	g.until(t, "the first run over", func() bool {
		return env.Threads[id].View.Run == api.RunIdle && len(env.Threads[id].Pending) == 1
	})
	m.Retry(env, id, now)
	g.until(t, "the retry landed", func() bool { return len(env.Threads[id].Pending) == 0 }, func() string { return describePending(env.Threads[id].Pending) })
}

// Interrupting a run voids the request it asked. An answer sent after that
// is refused, and leaves no row: the request shows how it ended.
func TestAnAnswerTooLateLeavesNoRow(t *testing.T) {
	m, g := manager(t, stateDir(t))
	env := m.Envs()[0]
	online(t, g, env)
	id := m.Create(env, "/work", "hello", fakeModel, api.AccessAsk)
	m.OpenThread(env, id)
	m.Send(env, id, "hello", api.AfterActive)
	var req *RequestEntry
	g.until(t, "the approval", func() bool {
		for _, e := range env.Threads[id].Transcript.Entries {
			if r, ok := e.(*RequestEntry); ok && r.State == api.RequestPending {
				req = r
			}
		}
		return req != nil
	})
	m.Interrupt(env, id)
	g.until(t, "the run over", func() bool {
		th := env.Threads[id]
		return th.View.Run == api.RunIdle && th.View.LastTurn != nil && len(th.Pending) == 0 && th.Transcript.request(req.Request).State != api.RequestPending
	})
	m.Respond(env, id, req.Request, AllowOnce)
	g.until(t, "the refused answer dropped", func() bool { return len(env.Threads[id].Pending) == 0 }, func() string { return describePending(env.Threads[id].Pending) })
}

// A failed answer stays while the open transcript, behind the thread's
// view, still shows its request waiting. Closing the thread leaves only
// the view, which no longer asks, so the row goes.
func TestClosingAThreadDropsAnAnswerOnlyItsTranscriptAsked(t *testing.T) {
	request, id := api.NewID(), api.NewID()
	th := &Thread{
		View:       api.Thread{ID: id, Requests: []api.Request{{ID: request}}},
		Transcript: &Transcript{Entries: []Entry{&RequestEntry{Request: request, State: api.RequestPending}}},
		Pending:    []Pending{{Command: api.NewID(), Body: api.RequestRespond{Request: request}, Err: errNoEngine}},
		open:       true,
	}
	env := &Env{Threads: map[api.ID]*Thread{id: th}}
	m := &Manager{}
	m.view(env, api.Thread{ID: id})
	if len(th.Pending) != 1 {
		t.Fatalf("with the transcript still asking the rows are %s, want the failed answer", describePending(th.Pending))
	}
	m.CloseThread(env, id)
	if len(th.Pending) != 0 {
		t.Fatalf("closed, the thread keeps %s, an answer to a request nothing shows", describePending(th.Pending))
	}
}

// An answer the engine took stays a row while the thread still shows its
// request waiting, as the view or the transcript that says it was
// answered comes after the command lands: the request never shows waiting
// again in between. The view that no longer asks drops it, as does the
// transcript of an open thread.
func TestATakenAnswerStaysUntilItsRequestStopsWaiting(t *testing.T) {
	for _, open := range []bool{false, true} {
		request, id, cmd := api.NewID(), api.NewID(), api.NewID()
		body := api.RequestRespond{Request: request}
		th := &Thread{
			View:    api.Thread{ID: id, Requests: []api.Request{{ID: request}}},
			Pending: []Pending{{Command: cmd, Body: body}},
			open:    open,
		}
		if open {
			th.Transcript = &Transcript{Entries: []Entry{&RequestEntry{Request: request, State: api.RequestPending}}}
		}
		env := &Env{Threads: map[api.ID]*Thread{id: th}}
		m := &Manager{}
		m.submitted(env, api.Command{ID: cmd, Target: id, Body: body}, api.Receipt{}, nil)
		if len(th.Pending) != 1 {
			t.Fatalf("open %v: taken, with its request still waiting, the rows are %s; want the answer", open, describePending(th.Pending))
		}
		m.view(env, api.Thread{ID: id})
		if open {
			if len(th.Pending) != 1 {
				t.Fatalf("with the transcript still asking the rows are %s, want the answer", describePending(th.Pending))
			}
			th.Transcript.request(request).State = api.RequestAnswered
			th.dropUnasked()
		}
		if len(th.Pending) != 0 {
			t.Fatalf("open %v: with its request no longer waiting the thread keeps %s", open, describePending(th.Pending))
		}
	}
}

// A thread created before the engine answered fails to reach it. Dismissing
// its create drops the thread, which no engine has.
func TestDismissingAFailedCreateDropsItsThread(t *testing.T) {
	m, _ := manager(t, stateDir(t))
	env := m.Envs()[0]
	id := m.Create(env, "/work", "never", fakeModel, api.AccessAsk)
	p := env.Threads[id].Pending
	if len(p) != 1 || p[0].Err == nil || p[0].Terminal() {
		t.Fatalf("with no engine the create is %s, want failed and worth a retry", describePending(p))
	}
	m.Dismiss(env, id, p[0].Command)
	if env.Threads[id] != nil {
		t.Fatal("the thread whose create was dismissed is still shown")
	}
}

func describePending(ps []Pending) string {
	var out []string
	for _, p := range ps {
		out = append(out, api.IntentType(p.Body)+" "+errString(p.Err))
	}
	return "pending: " + strings.Join(out, ", ")
}

func errString(err error) string {
	if err == nil {
		return "in flight"
	}
	return err.Error()
}

// tombstoned is an engine whose one thread was deleted: its log ends at
// the tombstone, and every open of it after that finds it no more.
type tombstoned struct {
	*longThread
	opens atomic.Int64
}

func (e *tombstoned) Open(ctx context.Context, s api.Subscription) (api.Stream, error) {
	if s.Thread.IsZero() {
		return e.longThread.Open(ctx, s)
	}
	if e.opens.Add(1) > 1 {
		return nil, api.ErrNotFound
	}
	return &ending{items{{Kind: api.StreamSnapshot, Snapshot: &e.log}}, api.ErrNotFound}, nil
}

// ending streams its items, then fails with err.
type ending struct {
	items
	err error
}

func (s *ending) Next(ctx context.Context) (api.StreamItem, error) {
	if len(s.items) == 0 {
		return api.StreamItem{}, s.err
	}
	return s.items.Next(ctx)
}

// Past a thread's tombstone its stream ends for good: the manager doesn't
// reopen it on backoff.
func TestADeletedThreadsStreamIsNotReopened(t *testing.T) {
	id, a := api.NewID(), api.NewID()
	evs := []api.Event{event(1, api.ThreadCreated{}), event(2, api.TurnEnded{Run: a}), event(3, api.ThreadDeleted{})}
	eng := &tombstoned{longThread: &longThread{view: api.Thread{ID: id, Title: "gone"}, log: api.Snapshot{Cursor: evs[2].Cursor, Events: evs}}}
	dir := stateDir(t)
	serveOn(t, dir, eng)
	m, g := manager(t, dir)
	env := m.Envs()[0]
	online(t, g, env)
	m.OpenThread(env, id)
	g.until(t, "the tombstone", func() bool { return env.Threads[id].Transcript.Deleted })
	time.Sleep(3 * minBackoff)
	if n := eng.opens.Load(); n != 1 {
		t.Fatalf("the deleted thread's stream was opened %d times, want once", n)
	}
}
