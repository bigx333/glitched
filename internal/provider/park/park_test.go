package park

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"glitched/internal/api"
)

type fake struct {
	done    chan struct{}
	exit    sync.Once
	wake    bool
	closes  atomic.Int32
	release chan struct{}
}

// newFake is a live process that Wake accepts. A non-nil release holds its
// Close until it is closed.
func newFake(release chan struct{}) *fake {
	return &fake{done: make(chan struct{}), wake: true, release: release}
}

func (f *fake) Done() <-chan struct{} { return f.done }

func (f *fake) Wake() bool {
	select {
	case <-f.done:
		return false
	default:
		return f.wake
	}
}

func (f *fake) Close(ctx context.Context) error {
	f.closes.Add(1)
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.exit.Do(func() { close(f.done) })
	return nil
}

func (f *fake) exited() { f.exit.Do(func() { close(f.done) }) }

const idle = time.Minute

// newPool closes the pool when the test ends. synctest.Test then fails on any
// goroutine left running, so every test also checks that none outlives its process.
func newPool(t *testing.T, max int) *Pool[*fake] {
	p := New[*fake](idle, max)
	t.Cleanup(func() {
		if err := p.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return p
}

func closes(t *testing.T, f *fake, want int32) {
	t.Helper()
	if got := f.closes.Load(); got != want {
		t.Fatalf("closed %d times, want %d", got, want)
	}
}

func TestTakeSameKeyHandsItBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 4)
		thread, f := api.NewID(), newFake(nil)
		p.Park(thread, "k", f)
		got, ok, _ := p.Take(t.Context(), thread, "k")
		if !ok || got != f {
			t.Fatalf("Take = %v, %v; want the parked process", got, ok)
		}
		if _, ok, _ := p.Take(t.Context(), thread, "k"); ok {
			t.Fatal("a taken process is still parked")
		}
		time.Sleep(2 * idle)
		synctest.Wait()
		closes(t, f, 0)
	})
}

func TestTakeOtherKeyClosesIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 4)
		thread, f := api.NewID(), newFake(nil)
		p.Park(thread, "k", f)
		if _, ok, _ := p.Take(t.Context(), thread, "other"); ok {
			t.Fatal("Take under another key hit")
		}
		closes(t, f, 1)
	})
}

func TestTakeWakeRefusedClosesIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 4)
		thread, f := api.NewID(), newFake(nil)
		f.wake = false
		p.Park(thread, "k", f)
		if _, ok, _ := p.Take(t.Context(), thread, "k"); ok {
			t.Fatal("Take hit a process Wake refused")
		}
		closes(t, f, 1)
	})
}

func TestIdleReap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 4)
		thread, f := api.NewID(), newFake(nil)
		p.Park(thread, "k", f)
		time.Sleep(idle - time.Second)
		synctest.Wait()
		closes(t, f, 0)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		closes(t, f, 1)
		if _, ok, _ := p.Take(t.Context(), thread, "k"); ok {
			t.Fatal("Take hit a reaped process")
		}
	})
}

func TestParkPastMaxEvictsOldest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 1)
		t1, f1 := api.NewID(), newFake(nil)
		t2, f2 := api.NewID(), newFake(nil)
		p.Park(t1, "k", f1)
		time.Sleep(time.Second)
		p.Park(t2, "k", f2)
		synctest.Wait()
		closes(t, f1, 1)
		closes(t, f2, 0)
		if _, ok, _ := p.Take(t.Context(), t1, "k"); ok {
			t.Fatal("Take hit an evicted process")
		}
		if got, ok, _ := p.Take(t.Context(), t2, "k"); !ok || got != f2 {
			t.Fatal("Take missed the process parked last")
		}
	})
}

func TestExitedFreesItsPlace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 2)
		older, live := api.NewID(), newFake(nil)
		gone, dead := api.NewID(), newFake(nil)
		p.Park(older, "k", live)
		time.Sleep(time.Second)
		p.Park(gone, "k", dead)
		dead.exited()
		// No synctest.Wait: the third Park must not count dead even before
		// its own watcher has seen it exit.
		p.Park(api.NewID(), "k", newFake(nil))
		synctest.Wait()
		closes(t, live, 0)
		closes(t, dead, 1)
		if got, ok, _ := p.Take(t.Context(), older, "k"); !ok || got != live {
			t.Fatal("an exited process cost a live one its place")
		}
	})
}

func TestExitedLeavesAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 4)
		thread, f := api.NewID(), newFake(nil)
		p.Park(thread, "k", f)
		f.exited()
		synctest.Wait()
		closes(t, f, 1)
	})
}

func TestTakeWaitsForCloseUnderWay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 4)
		release := make(chan struct{})
		thread, f := api.NewID(), newFake(release)
		p.Park(thread, "k", f)
		time.Sleep(idle + time.Second)
		synctest.Wait()
		closes(t, f, 1)
		var returned atomic.Bool
		go func() {
			p.Take(t.Context(), thread, "k")
			returned.Store(true)
		}()
		synctest.Wait()
		if returned.Load() {
			t.Fatal("Take returned while the reap was still closing the process")
		}
		close(release)
		synctest.Wait()
		if !returned.Load() {
			t.Fatal("Take still waits after the reap closed the process")
		}
	})
}

func TestTakeWaitEndsWithContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 4)
		release := make(chan struct{})
		defer close(release)
		thread := api.NewID()
		p.Park(thread, "k", newFake(release))
		time.Sleep(idle + time.Second)
		synctest.Wait()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, ok, _ := p.Take(ctx, thread, "k"); ok {
			t.Fatal("Take hit a process being reaped")
		}
		if ctx.Err() == nil {
			t.Fatal("Take returned before the close or its context ended")
		}
	})
}

func TestCloseClosesAllAndLaterParks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 4)
		f1, f2 := newFake(nil), newFake(nil)
		p.Park(api.NewID(), "k", f1)
		p.Park(api.NewID(), "k", f2)
		if err := p.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		closes(t, f1, 1)
		closes(t, f2, 1)
		f3 := newFake(nil)
		thread := api.NewID()
		p.Park(thread, "k", f3)
		closes(t, f3, 1)
		if _, ok, _ := p.Take(t.Context(), thread, "k"); ok {
			t.Fatal("a Park after Close kept its process")
		}
	})
}

func TestCloseWaitsForReap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 4)
		release := make(chan struct{})
		p.Park(api.NewID(), "k", newFake(release))
		time.Sleep(idle + time.Second)
		synctest.Wait()
		var returned atomic.Bool
		go func() {
			p.Close(t.Context())
			returned.Store(true)
		}()
		synctest.Wait()
		if returned.Load() {
			t.Fatal("Close returned while a reap was still closing a process")
		}
		close(release)
		synctest.Wait()
		if !returned.Load() {
			t.Fatal("Close still waits after the reap finished")
		}
	})
}

func TestDrop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 4)
		thread, other := api.NewID(), api.NewID()
		f, kept := newFake(nil), newFake(nil)
		p.Park(thread, "k", f)
		p.Park(other, "k", kept)
		if err := p.Drop(t.Context(), thread); err != nil {
			t.Fatal(err)
		}
		closes(t, f, 1)
		closes(t, kept, 0)
		if _, ok, _ := p.Take(t.Context(), thread, "k"); ok {
			t.Fatal("Take hit a dropped process")
		}
		if err := p.Drop(t.Context(), api.NewID()); err != nil {
			t.Fatalf("Drop with nothing parked: %v", err)
		}
	})
}

func TestMaxBelowOneParksNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 0)
		thread, f := api.NewID(), newFake(nil)
		p.Park(thread, "k", f)
		closes(t, f, 1)
		if _, ok, _ := p.Take(t.Context(), thread, "k"); ok {
			t.Fatal("max 0 parked a process")
		}
	})
}
