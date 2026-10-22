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
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
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

// until runs posts until ok holds. state describes what the test saw when
// it gives up.
func (g *gui) until(t *testing.T, what string, ok func() bool, state ...func() string) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		g.mu.Lock()
		q := g.q
		g.q = nil
		g.mu.Unlock()
		for _, fn := range q {
			fn()
		}
		if ok() {
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

func engine(t *testing.T, cfg daemon.Config) *daemon.Daemon {
	t.Helper()
	cfg.Hub, cfg.Adapter = true, fake.New(fake.Turn(true))
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
	engine(t, daemon.Config{Dir: dir})
}

func TestStartAttachesToTheEngineThatAnswers(t *testing.T) {
	dir := stateDir(t)
	d := engine(t, daemon.Config{Dir: dir})
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

func TestATurnRunsFromCreateThroughAnApproval(t *testing.T) {
	m, g := manager(t, stateDir(t))
	env := m.Envs()[0]
	online(t, g, env)

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
	m.Respond(env, id, req.Request, true)

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

	answer := m.Respond(env, id, api.NewID(), true)
	failed := func() bool {
		p := env.Threads[id].Pending
		return len(p) == 1 && p[0].Command == answer && errors.Is(p[0].Err, api.ErrNotFound)
	}
	g.until(t, "an answer to an unknown request to fail", failed)
	m.Retry(env, id, answer)
	if p := env.Threads[id].Pending; len(p) != 1 || p[0].Err != nil {
		t.Fatalf("right after Retry the row is %+v, want it in flight again", p)
	}
	g.until(t, "the retry to fail the same way", failed)
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
