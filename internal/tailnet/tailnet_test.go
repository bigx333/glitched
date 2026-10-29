package tailnet

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/safesocket"
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

// Without Tailscale, Detect fails before its deadline even in a process
// that just started, as the engine's first one runs, so a missing Tailscale
// never reads as a slow one. The check runs in a fresh process.
func TestAMissingTailscaleFailsAtOnceInANewProcess(t *testing.T) {
	if os.Getenv("GLITCHED_DETECT_CHILD") == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
		cmd.Env = append(os.Environ(), "GLITCHED_DETECT_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("in a new process: %v\n%s", err, out)
		}
		return
	}
	if _, _, err := safesocket.LocalTCPPortAndToken(); err == nil {
		t.Skip("the macOS Tailscale app answers on this machine")
	}
	tailscaledSocket = filepath.Join(t.TempDir(), "tailscaled.sock")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := Detect(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("Detect without Tailscale: %v, want a failure before the deadline", err)
	}
}
