package park

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"glitched/internal/api"
)

var errToolsLeft = errors.New("a descendant is still running")

type flakyClose struct {
	*fake
	failFirst int32
	calls     atomic.Int32
}

func (f *flakyClose) Close(ctx context.Context) error {
	if f.calls.Add(1) <= f.failFirst {
		return errToolsLeft
	}
	return f.fake.Close(ctx)
}

func TestARetriedCloseWaitsBetweenTries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := New[*flakyClose](idle, 1)
		defer p.Close(t.Context())
		th := api.NewID()
		old := &flakyClose{fake: newFake(nil), failFirst: 16}
		p.Park(th, "old", old)
		synctest.Wait()
		if _, _, err := p.Take(t.Context(), th, "new"); !errors.Is(err, errToolsLeft) {
			t.Fatalf("Take err = %v, want failed cleanup", err)
		}
		old.exited()
		start := time.Now()
		synctest.Wait()
		if n := old.calls.Load(); n > 3 {
			t.Fatalf("Close ran %d times in %v with unfinished descendant cleanup and an already-exited main process; retries should back off", n, time.Since(start))
		}
	})
}

func TestEveryFailedCloseKeepsTheThreadsProcess(t *testing.T) {
	for _, action := range []string{"idle", "evict", "drop"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := New[*flakyClose](idle, 1)
				defer p.Close(t.Context())
				th := api.NewID()
				release := make(chan struct{})
				defer close(release)
				old := &flakyClose{fake: newFake(release), failFirst: 1}
				p.Park(th, "old", old)
				synctest.Wait()
				switch action {
				case "idle":
					time.Sleep(idle)
				case "evict":
					p.Park(api.NewID(), "other", &flakyClose{fake: newFake(nil)})
				case "drop":
					ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
					defer cancel()
					done := make(chan error, 1)
					go func() { done <- p.Drop(ctx, th) }()
					if err := <-done; !errors.Is(err, errToolsLeft) {
						t.Fatalf("Drop err = %v, want failed cleanup", err)
					}
				}
				synctest.Wait()
				if n := old.calls.Load(); n == 0 {
					t.Fatal("old process never received a close attempt")
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
				defer cancel()
				_, ok, err := p.Take(ctx, th, "new")
				select {
				case <-old.Done():
					t.Fatal("test needs old process to still be running")
				default:
				}
				if !ok && err == nil {
					t.Fatalf("%s: Take said a new process can launch while old process is still running (Close attempts=%d)", action, old.calls.Load())
				}
			})
		})
	}
}
