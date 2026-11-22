package park

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"glitched/internal/api"
)

func TestCloseReturnsAtItsDeadlineWhileACloseIsRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPool(t, 1)
		th := api.NewID()
		release := make(chan struct{})
		f := newFake(release)
		p.Park(th, "old", f)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, _, err := p.Take(ctx, th, "new"); !errors.Is(err, context.Canceled) {
			t.Fatalf("failed Take returned %v", err)
		}
		synctest.Wait()
		before := time.Now()
		ctx, cancel = context.WithTimeout(t.Context(), time.Millisecond)
		defer cancel()
		if err := p.Close(ctx); !errors.Is(err, context.DeadlineExceeded) || time.Since(before) != time.Millisecond {
			t.Fatalf("Close returned %v after %v, want its 1ms deadline", err, time.Since(before))
		}

		beforeCalls := f.closes.Load()
		time.Sleep(2 * closeTimeout)
		synctest.Wait()
		afterCalls := f.closes.Load()
		close(release)
		if err := p.Close(t.Context()); err != nil {
			t.Fatalf("Close after release: %v", err)
		}
		select {
		case <-f.Done():
		default:
			t.Fatal("final Close returned but process wasn't reaped")
		}
		t.Logf("Close honored its deadline, background cleanup continued (%d to %d attempts), and a later Close completed", beforeCalls, afterCalls)
	})
}
