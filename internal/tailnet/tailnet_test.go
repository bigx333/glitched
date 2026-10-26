package tailnet

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"tailscale.com/ipn/ipnstate"
)

// A stalled lookup holds one goroutine, however often Detect asks while it
// stalls, and each Detect still returns by its deadline.
func TestAStalledLookupIsWaitedOnNotRepeated(t *testing.T) {
	release := make(chan struct{})
	was := installedStatus
	installedStatus = func(context.Context) (*ipnstate.Status, error) {
		<-release
		return nil, errors.New("stalled")
	}
	t.Cleanup(func() { installedStatus = was })
	before := runtime.NumGoroutine()
	for range 50 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		start := time.Now()
		_, err := Detect(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
			t.Fatalf("Detect during a stall: %v after %v, want the deadline", err, time.Since(start))
		}
	}
	if n := runtime.NumGoroutine() - before; n > 1 {
		t.Errorf("%d goroutines more after 50 calls during one stall, want the one lookup", n)
	}
	close(release)
	Detect(context.Background()) // waits out the lookup, before the cleanup swaps installedStatus back
}
