package daemon

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// awakeGrace bridges runs back to back, as T3 Code's keep-awake does, so a
// queue of turns doesn't let the machine sleep between two of them.
var awakeGrace = time.Minute

// awake holds the machine awake with hold while an agent works and the
// setting is on, and for awakeGrace after the last stops working. Turned
// off, it lets go at once.
type awake struct {
	hold             func(context.Context) error
	grace            time.Duration
	after            func(time.Duration) <-chan time.Time
	working, enabled atomic.Bool
	poke             chan struct{}
}

func newAwake(hold func(context.Context) error, enabled bool) *awake {
	w := &awake{hold: hold, grace: awakeGrace, after: time.After, poke: make(chan struct{}, 1)}
	w.enabled.Store(enabled)
	return w
}

// work is the host's: whether any agent works.
func (w *awake) work(working bool) {
	w.working.Store(working)
	w.wake()
}

func (w *awake) enable(on bool) {
	w.enabled.Store(on)
	w.wake()
}

func (w *awake) wake() {
	select {
	case w.poke <- struct{}{}:
	default:
	}
}

// run holds and lets go as wanted until ctx ends.
func (w *awake) run(ctx context.Context) {
	var release context.CancelFunc
	var released chan struct{}
	var grace <-chan time.Time
	letGo := func() {
		release()
		<-released
		release, grace = nil, nil
	}
	for {
		on := w.enabled.Load()
		switch {
		case on && w.working.Load():
			grace = nil
			if release == nil {
				var hctx context.Context
				hctx, release = context.WithCancel(ctx)
				released = make(chan struct{})
				go w.keep(hctx, released)
			}
		case release != nil && !on:
			letGo()
		case release != nil && grace == nil:
			grace = w.after(w.grace)
		}
		select {
		case <-ctx.Done():
			if release != nil {
				letGo()
			}
			return
		case <-w.poke:
		case <-grace:
			grace = nil
			if !w.enabled.Load() || !w.working.Load() {
				letGo()
			}
		}
	}
}

// keep runs hold until ctx ends, again after a backoff when it stops
// early, as an inhibitor killed from outside does.
func (w *awake) keep(ctx context.Context, done chan struct{}) {
	defer close(done)
	for wait := 5 * time.Second; ; wait = min(2*wait, 10*time.Minute) {
		err := w.hold(ctx)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("keep-awake: the sleep inhibitor stopped, so the machine may sleep while agents work", "err", err, "retry", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
