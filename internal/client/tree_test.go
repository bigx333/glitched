package client

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// bridge is a real engine behind a link the test can break: it can fail
// the next submit, or lose its answer, hold the shell after its snapshot,
// drop it, and be out of reach. It counts the commands that reach the
// engine, by target.
type bridge struct {
	api.Endpoint
	mu      sync.Mutex
	fail    bool          // the next submit fails, as one the network lost
	lose    bool          // the next submit fails once the engine took it, as one whose answer the network lost
	down    bool          // out of reach: Info and Submit fail
	refused int           // Infos failed while out of reach
	hold    chan struct{} // the shell waits after its snapshot until closed
	shells  []api.Stream  // closed on a drop
	sent    map[api.ID]int
}

var errDown = errors.New("bridge: out of reach")

func (b *bridge) Info(ctx context.Context) (api.Environment, error) {
	b.mu.Lock()
	down := b.down
	if down {
		b.refused++
	}
	b.mu.Unlock()
	if down {
		return api.Environment{}, errDown
	}
	return b.Endpoint.Info(ctx)
}

func (b *bridge) Submit(ctx context.Context, c api.Command) (api.Receipt, error) {
	b.mu.Lock()
	fail := b.fail || b.down
	b.fail = false
	lose := b.lose && !fail
	if !fail {
		b.sent[c.Target]++
		b.lose = false
	}
	b.mu.Unlock()
	if fail {
		return api.Receipt{}, errDown
	}
	r, err := b.Endpoint.Submit(ctx, c)
	if lose && err == nil {
		return api.Receipt{}, errDown
	}
	return r, err
}

func (b *bridge) Open(ctx context.Context, sub api.Subscription) (api.Stream, error) {
	st, err := b.Endpoint.Open(ctx, sub)
	if err != nil || !sub.Thread.IsZero() {
		return st, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.shells = append(b.shells, st)
	return &held{Stream: st, hold: b.hold}, nil
}

func (b *bridge) count(id api.ID) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sent[id]
}

// drop cuts the shell and keeps the engine out of reach until back.
func (b *bridge) drop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.down = true
	for _, st := range b.shells {
		st.Close()
	}
	b.shells = nil
}

func (b *bridge) back() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.down = false
}

type held struct {
	api.Stream
	hold <-chan struct{}
	past bool
}

func (h *held) Next(ctx context.Context) (api.StreamItem, error) {
	if h.past {
		select {
		case <-h.hold:
		case <-ctx.Done():
			return api.StreamItem{}, ctx.Err()
		}
	}
	h.past = true
	return h.Stream.Next(ctx)
}

// treeFixture is a GUI online to a real engine whose fake agents wait on
// an approval, with a thread of the user's on it, and the shell held
// after its snapshot.
func treeFixture(t *testing.T) (*bridge, *fake.Adapter, *Manager, *gui, *Env, api.ID) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ad := fake.New(fake.Turn(true))
	host, err := runtime.New(context.Background(), st, provider.Mux{"fake": ad})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close() })
	top := api.NewID()
	if _, err := host.Submit(context.Background(), api.Command{ID: api.NewID(), Target: top,
		Body: api.ThreadCreate{Workspace: "/work", Title: "top", Model: fakeModel}}); err != nil {
		t.Fatal(err)
	}
	dir := stateDir(t)
	b := &bridge{Endpoint: host, hold: make(chan struct{}), sent: map[api.ID]int{}}
	serveOn(t, dir, b)
	m, g := manager(t, dir)
	e := m.Envs()[0]
	online(t, g, e)
	return b, ad, m, g, e, top
}

// spawnUnder has a turn of parent's start a child at work on the engine,
// as an agent's delegate_task call does, and returns it once the child
// waits on its approval and the parent's turn ended.
func spawnUnder(t *testing.T, b *bridge, ad *fake.Adapter, parent api.ID, title string) api.ID {
	t.Helper()
	ad.Spawns(b.Endpoint, title, fake.Child{Title: title, Task: "work"})
	run := api.NewID()
	if _, err := b.Endpoint.Submit(context.Background(), api.Command{ID: run, Target: parent, Body: api.MessageDispatch{Text: title, Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	child := fake.ChildOf(run, 0)
	engineUntil(t, b, "the child at work", func() bool {
		k, kok := engineView(t, b, child)
		p, _ := engineView(t, b, parent)
		return kok && k.Run == api.RunWaiting && p.Run == api.RunIdle
	})
	return child
}

// engineView is the engine's view of a thread, and whether it's there.
func engineView(t *testing.T, b *bridge, id api.ID) (api.Thread, bool) {
	t.Helper()
	snap, err := b.Endpoint.Read(context.Background(), api.Query{Thread: id})
	if errors.Is(err, api.ErrNotFound) {
		return api.Thread{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return snap.Threads[0], true
}

func engineUntil(t *testing.T, b *bridge, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// runOf is the run thread's log last admitted.
func runOf(t *testing.T, b *bridge, thread api.ID) api.ID {
	t.Helper()
	snap, err := b.Endpoint.Read(context.Background(), api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	var run api.ID
	for _, ev := range snap.Events {
		if body, err := ev.Decode(); err == nil {
			if a, ok := body.(api.TurnAdmitted); ok {
				run = a.Run
			}
		}
	}
	return run
}

// taken is nil once the engine took the child with its tree: gone after
// a delete; settled, its run stopped, after a settle.
func taken(t *testing.T, b *bridge, child api.ID, action string) error {
	t.Helper()
	v, there := engineView(t, b, child)
	switch {
	case action == "delete" && there:
		return fmt.Errorf("the child is still there: %v, run %v", v.Disposition.State, v.Run)
	case action == "settle" && (!there || v.Disposition.State != api.Settled || v.Live()):
		return fmt.Errorf("the child: there %v, %v, run %v; want settled and stopped", there, v.Disposition.State, v.Run)
	}
	return nil
}

func act(m *Manager, e *Env, top api.ID, action string) {
	if action == "settle" {
		m.SetDisposition(e, top, api.Disposition{State: api.Settled})
	} else {
		m.Delete(e, top)
	}
}

// A settle or delete whose submit failed, sent again with Retry, takes a
// child the GUI hadn't seen when the user chose it: the engine settles or
// deletes the child in the retry's commit, stops its agent, and the GUI
// shows the child as the engine has it. Sol's High 1 on PR 74.
func TestARetriedTreeActionTakesAnUnseenChild(t *testing.T) {
	for _, action := range []string{"settle", "delete"} {
		t.Run(action, func(t *testing.T) {
			b, ad, m, g, e, top := treeFixture(t)
			child := spawnUnder(t, b, ad, top, "child")
			run := runOf(t, b, child)
			if e.Threads[child] != nil {
				t.Fatal("the GUI saw the child; this needs its view still on the way")
			}
			b.mu.Lock()
			b.fail = true
			b.mu.Unlock()
			act(m, e, top, action)
			g.until(t, "the failure", func() bool { p := e.Threads[top].Pending; return len(p) == 1 && p[0].Err != nil })
			m.Retry(e, top, e.Threads[top].Pending[0].Command)
			engineUntil(t, b, "the retry", func() bool {
				v, there := engineView(t, b, top)
				return !there || v.Disposition.State == api.Settled
			})
			if _, there := engineView(t, b, child); action == "delete" && there {
				t.Fatal("the child is there after the delete's commit")
			}
			engineUntil(t, b, "the child "+action+"d and stopped", func() bool { return taken(t, b, child, action) == nil })
			engineUntil(t, b, "the child's agent gone", func() bool {
				rec, err := ad.Reconcile(context.Background(), provider.RuntimeIdentity{Thread: child, Run: run})
				_, lost := rec.(provider.Lost)
				return lost && err == nil
			})
			close(b.hold)
			g.until(t, "the GUI shows the child as the engine has it", func() bool {
				th := e.Threads[child]
				if action == "delete" {
					return th == nil && e.Threads[top] == nil
				}
				return th != nil && th.View.Disposition.State == api.Settled && !th.View.Live()
			})
			if n := b.count(child); n != 0 {
				t.Errorf("%d commands sent to the child; the top's takes it", n)
			}
		})
	}
}

// A settle or delete the user chose while the engine was out of reach,
// then dismissed, leaves alone the children the parent's agent starts
// after, before and after the GUI is back. Sol's High 2 and Opus's Medium
// on PR 74.
func TestADismissedTreeActionTakesNoLaterChild(t *testing.T) {
	for _, action := range []string{"settle", "delete"} {
		t.Run(action, func(t *testing.T) {
			b, ad, m, g, e, top := treeFixture(t)
			close(b.hold)
			b.drop()
			g.until(t, "offline", func() bool { _, off := e.Status.(Offline); return off })
			act(m, e, top, action)
			g.until(t, "the failure", func() bool { p := e.Threads[top].Pending; return len(p) == 1 && p[0].Err != nil })
			m.Dismiss(e, top, e.Threads[top].Pending[0].Command)
			early := spawnUnder(t, b, ad, top, "early")
			b.back()
			online(t, g, e)
			g.until(t, "the early child shown", func() bool { return e.Threads[early] != nil })
			late := spawnUnder(t, b, ad, top, "late")
			g.until(t, "the late child shown", func() bool { return e.Threads[late] != nil && e.Threads[late].View.Run == api.RunWaiting })
			for name, id := range map[string]api.ID{"top": top, "early": early, "late": late} {
				v, there := engineView(t, b, id)
				if !there || v.Disposition.State == api.Settled {
					t.Errorf("%s: there %v, %v; want open", name, there, v.Disposition.State)
				}
				if n := b.count(id); id != top && n != 0 {
					t.Errorf("%s: %d commands sent; want none", name, n)
				}
				if th := e.Threads[id]; th == nil || len(th.Pending) != 0 {
					t.Errorf("%s: shown %v; want shown with nothing pending", name, th != nil)
				}
			}
		})
	}
}

// A settle that landed before the shell dropped leaves nothing behind
// across the reconnect: a child started after another client brought the
// thread back to the inbox is not settled, and this GUI sent one command,
// to the thread the user settled. Opus's Low on PR 74.
func TestASettleLeavesNothingAcrossAReconnect(t *testing.T) {
	b, ad, m, g, e, top := treeFixture(t)
	close(b.hold)
	kid := spawnUnder(t, b, ad, top, "kid")
	g.until(t, "the kid shown", func() bool { return e.Threads[kid] != nil })
	m.SetDisposition(e, top, api.Disposition{State: api.Settled})
	g.until(t, "the tree settled", func() bool {
		k, tp := e.Threads[kid], e.Threads[top]
		return len(tp.Pending) == 0 && k.View.Disposition.State == api.Settled && tp.View.Disposition.State == api.Settled
	})
	b.drop()
	g.until(t, "offline", func() bool { _, off := e.Status.(Offline); return off })
	after := spawnUnder(t, b, ad, top, "after")
	b.back()
	online(t, g, e)
	g.until(t, "the child after shown", func() bool { return e.Threads[after] != nil })
	if v, _ := engineView(t, b, after); v.Disposition.State == api.Settled || v.Run != api.RunWaiting {
		t.Errorf("the child started after: %v, run %v; want open and at work", v.Disposition.State, v.Run)
	}
	if b.count(top) != 1 || b.count(kid) != 0 || b.count(after) != 0 {
		t.Errorf("commands sent: top %d, kid %d, after %d; want only the top's settle", b.count(top), b.count(kid), b.count(after))
	}
}
