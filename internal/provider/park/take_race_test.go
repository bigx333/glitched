package park

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
)

type evictedProcess struct {
	*fake
	closing  chan struct{}
	attempts atomic.Int32
}

func (f *evictedProcess) Close(ctx context.Context) error {
	if f.closing != nil && f.attempts.Add(1) == 1 {
		close(f.closing)
	}
	return f.fake.Close(ctx)
}

func TestTakeWaitsForAnEvictionToFinish(t *testing.T) {
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(oldLogger)
	p := New[*evictedProcess](idle, 1)
	defer p.Close(t.Context())
	for i := range 200 {
		release := make(chan struct{})
		th := api.NewID()
		old := &evictedProcess{fake: newFake(release), closing: make(chan struct{})}
		p.Park(th, "old", old)
		p.Park(api.NewID(), "other", &evictedProcess{fake: newFake(nil)})
		<-old.closing
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		got := make(chan error, 1)
		starting := make(chan struct{})
		go func() {
			close(starting)
			_, _, err := p.Take(ctx, th, "new")
			got <- err
		}()
		<-starting
		runtime.Gosched()
		close(release)
		err := <-got
		cancel()
		if err != nil {
			t.Fatalf("iteration %d: Take timed out even though the old process exited: %v", i, err)
		}
	}
}
