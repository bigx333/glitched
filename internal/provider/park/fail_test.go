package park

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
)

func TestAFailedCloseKeepsTheProcessDroppable(t *testing.T) {
	p := newPool(t, 4)
	thread := api.NewID()
	releases := make(chan struct{})
	f := newFake(releases)
	defer close(releases)
	p.Park(thread, "access-ask", f)
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if _, ok, _ := p.Take(expired, thread, "access-full"); ok {
		t.Fatal("mismatched key unexpectedly returned old process")
	}
	// It was not killed; a caller with a fresh context must still be able
	// to find and drop it, or a later Open can start alongside it.
	finish := make(chan error, 1)
	ctx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	go func() { finish <- p.Drop(ctx, thread) }()
	if err := <-finish; err == nil {
		select {
		case <-f.Done():
		default:
			t.Fatalf("Drop returned success after failed Take cleanup but live process was forgotten (Close called %d time(s))", f.closes.Load())
		}
	}
}
