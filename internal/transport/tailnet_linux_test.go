package transport

import (
	"context"
	"os/exec"
	"sync/atomic"
	"testing"
)

// A closed listener frees its address for the next listen at once, though
// the process starts programs meanwhile: a forked child shares the
// listener's fd until it execs, and must not keep the port.
func TestAClosedListenerFreesItsAddressWhileTheProcessForks(t *testing.T) {
	var stop atomic.Bool
	t.Cleanup(func() { stop.Store(true) })
	for range 2 {
		go func() {
			for !stop.Load() {
				exec.Command("true").Run()
			}
		}()
	}
	tn := Tailnet{Address: "127.0.0.1:0"}
	ln, err := tn.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tn.Address = ln.Addr().String()
	for i := range 1000 {
		ln.Close()
		if ln, err = tn.Listen(context.Background()); err != nil {
			t.Fatalf("listen %d after a close: %v", i, err)
		}
	}
	ln.Close()
}
