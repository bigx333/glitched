package park

import (
	"context"
	"testing"
	"testing/synctest"

	"glitched/internal/api"
)

func TestDropAndCloseWaitForAFailedTakesClose(t *testing.T) {
	for _, name := range []string{"Drop", "Close"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := New[*fake](idle, 1)
				thread := api.NewID()
				release := make(chan struct{})
				f := newFake(release)
				t.Cleanup(func() { f.Close(context.Background()); p.Close(context.Background()) })
				defer close(release)
				p.Park(thread, "old", f)

				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				taken := make(chan bool, 1)
				go func() {
					_, ok, _ := p.Take(ctx, thread, "new")
					taken <- ok
				}()
				synctest.Wait()
				closes(t, f, 1)

				cleaned := make(chan error, 1)
				go func() {
					if name == "Drop" {
						cleaned <- p.Drop(t.Context(), thread)
					} else {
						cleaned <- p.Close(t.Context())
					}
				}()
				synctest.Wait()
				cancel()
				if <-taken {
					t.Fatal("Take returned a mismatched process")
				}
				synctest.Wait()
				select {
				case err := <-cleaned:
					if err == nil {
						select {
						case <-f.Done():
						default:
							t.Fatalf("%s returned nil while old process was still running (Close attempts=%d)", name, f.closes.Load())
						}
					}
				default:
				}
			})
		})
	}
}

func TestAFailedTakeCloseTakesNoPlaceInThePool(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 1)
		thread, other := api.NewID(), api.NewID()
		release := make(chan struct{})
		old, next := newFake(release), newFake(nil)
		defer close(release)
		p.Park(thread, "old", old)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go p.Take(ctx, thread, "new")
		synctest.Wait()
		closes(t, old, 1)
		p.Park(other, "other", next)
		cancel()
		synctest.Wait()
		p.mu.Lock()
		n := len(p.parked)
		p.mu.Unlock()
		if n > 1 && old.closes.Load() == 1 && next.closes.Load() == 0 {
			t.Fatalf("pool with Max=1 kept %d entries after Take's Close failed, with no eviction scheduled", n)
		}
	})
}

func TestAFailedCloseIsRetriedOnceTheProcessExits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 1)
		thread := api.NewID()
		release := make(chan struct{})
		f := newFake(release)
		p.Park(thread, "old", f)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, ok, _ := p.Take(ctx, thread, "new"); ok {
			t.Fatal("Take returned a mismatched process")
		}
		closes(t, f, 1)
		close(release)
		f.exited()
		synctest.Wait()
		closes(t, f, 2)
	})
}
