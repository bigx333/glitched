// Package park keeps the agent process of each thread alive between its runs,
// so a run reuses the process the last one left instead of starting its own.
// It knows nothing of the agent: the adapter says which processes fit a run by
// the key it parks them under.
package park

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"glitched/internal/api"
)

// closeTimeout bounds a close nobody waits on: a reap or an eviction.
var closeTimeout = 30 * time.Second

// Process is an agent process a Pool keeps between the runs of its thread.
type Process interface {
	// Done is closed once the process has exited.
	Done() <-chan struct{}
	// Wake readies a parked process for its thread's next run. It is false
	// when the process can't take one, as when it exited or spoke while parked.
	Wake() bool
	// Close ends the process and what it left running, and returns nil once they are gone.
	Close(ctx context.Context) error
}

// Pool holds at most one parked process per thread.
type Pool[P Process] struct {
	idle time.Duration
	max  int

	mu     sync.Mutex
	parked map[api.ID]*parked[P]
	// closing are the closes of a thread's processes under way, each closed
	// once its process is gone. Take waits them out so a thread never has two.
	closing map[api.ID][]chan struct{}
	closed  bool
}

type parked[P Process] struct {
	key   string
	proc  P
	since time.Time
	// left is closed when proc leaves the pool. When the pool itself closes
	// it there, why says why and done is its entry in closing; otherwise
	// whoever took it out closes it.
	left chan struct{}
	why  string
	done chan struct{}
}

// New is a pool that closes a process parked longer than idle, and keeps at most max parked:
// parking one more closes the one parked longest. max < 1 parks nothing.
func New[P Process](idle time.Duration, max int) *Pool[P] {
	return &Pool[P]{idle: idle, max: max, parked: map[api.ID]*parked[P]{}, closing: map[api.ID][]chan struct{}{}}
}

// Take hands back the process parked for thread when it was parked under key and Wake
// says it can take a run; it is no longer parked. A process parked under another key,
// or one Wake refuses, is closed before Take returns, so a thread never has two processes:
// Take also waits for a close of thread's process already under way (reap, eviction).
// ok false with no error means the caller starts a new process; an error, that the
// thread's old process may still run, so the caller must not.
func (p *Pool[P]) Take(ctx context.Context, thread api.ID, key string) (proc P, ok bool, err error) {
	for {
		p.mu.Lock()
		if c := p.closing[thread]; len(c) > 0 {
			p.mu.Unlock()
			select {
			case <-c[0]:
				continue
			case <-ctx.Done():
				return proc, false, ctx.Err()
			}
		}
		e := p.parked[thread]
		if e == nil {
			p.mu.Unlock()
			return proc, false, nil
		}
		p.remove(thread, e, "")
		// Wake runs outside the lock, so a second Take must still see that
		// this thread has a process until it is handed back or gone.
		done := p.begin(thread)
		p.mu.Unlock()
		if e.key == key && e.proc.Wake() {
			p.end(thread, done)
			return e.proc, true, nil
		}
		if err := e.proc.Close(ctx); err != nil {
			slog.Warn("park: closing a parked process that can't take the run", "thread", thread, "err", err)
			go p.closeAgain(thread, e.proc, done)
			return proc, false, err
		}
		p.end(thread, done)
		return proc, false, nil
	}
}

// closeAgain closes proc, whose close failed, until a close succeeds. Its
// close stays under way until then, so Take, Drop and Close wait on it.
func (p *Pool[P]) closeAgain(thread api.ID, proc P, done chan struct{}) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		err := proc.Close(ctx)
		cancel()
		if err == nil {
			p.end(thread, done)
			return
		}
		slog.Warn("park: closing a process again", "thread", thread, "err", err)
		select {
		case <-proc.Done():
		case <-time.After(closeTimeout):
		}
	}
}

// Park keeps proc for thread's next run under key. A process already parked for thread
// is closed first (it should not happen; log it). After Close, Park closes proc instead.
func (p *Pool[P]) Park(thread api.ID, key string, proc P) {
	p.mu.Lock()
	if p.closed || p.max < 1 {
		done := p.begin(thread)
		p.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		if err := proc.Close(ctx); err != nil {
			slog.Warn("park: closing a process the pool can't keep", "thread", thread, "err", err)
		}
		p.end(thread, done)
		return
	}
	if old := p.parked[thread]; old != nil {
		slog.Warn("park: a thread parked a second process; closing the first", "thread", thread)
		p.remove(thread, old, "replaced")
	}
	// An exited process may not have left yet; it must not cost a live one its place.
	var oldest *parked[P]
	var oldestThread api.ID
	for t, e := range p.parked {
		select {
		case <-e.proc.Done():
			p.remove(t, e, "exited")
			continue
		default:
		}
		if oldest == nil || e.since.Before(oldest.since) {
			oldest, oldestThread = e, t
		}
	}
	if len(p.parked) >= p.max {
		p.remove(oldestThread, oldest, "evicted")
	}
	e := &parked[P]{key: key, proc: proc, since: time.Now(), left: make(chan struct{})}
	p.parked[thread] = e
	p.mu.Unlock()
	go p.watch(thread, e)
}

// Drop closes the process parked for thread, if any, and returns once it is gone.
func (p *Pool[P]) Drop(ctx context.Context, thread api.ID) error {
	p.mu.Lock()
	e := p.parked[thread]
	var done chan struct{}
	if e != nil {
		p.remove(thread, e, "")
		done = p.begin(thread)
	}
	waits := slices.Clone(p.closing[thread])
	p.mu.Unlock()
	var err error
	if e != nil {
		err = e.proc.Close(ctx)
		p.end(thread, done)
	}
	return errors.Join(err, wait(ctx, waits))
}

// Close closes every parked process, waits for them, and has later Parks close theirs.
func (p *Pool[P]) Close(ctx context.Context) error {
	type closing struct {
		thread api.ID
		e      *parked[P]
		done   chan struct{}
	}
	p.mu.Lock()
	p.closed = true
	var all []closing
	for t, e := range p.parked {
		p.remove(t, e, "")
		all = append(all, closing{t, e, p.begin(t)})
	}
	var waits []chan struct{}
	for _, c := range p.closing {
		waits = append(waits, c...)
	}
	p.mu.Unlock()
	errs := make([]error, len(all))
	var wg sync.WaitGroup
	for i, c := range all {
		wg.Go(func() {
			errs[i] = c.e.proc.Close(ctx)
			p.end(c.thread, c.done)
		})
	}
	wg.Wait()
	return errors.Join(errors.Join(errs...), wait(ctx, waits))
}

// watch is the one goroutine of a parked process. It closes the process
// when it exits, idles too long or is evicted, and otherwise ends when the
// process leaves the pool.
func (p *Pool[P]) watch(thread api.ID, e *parked[P]) {
	idle := time.NewTimer(p.idle)
	defer idle.Stop()
	var why string
	select {
	case <-e.left:
	case <-e.proc.Done():
		why = "exited"
	case <-idle.C:
		why = "idle"
	}
	p.mu.Lock()
	if why != "" && p.parked[thread] == e {
		p.remove(thread, e, why)
	}
	why, done := e.why, e.done
	p.mu.Unlock()
	if done == nil {
		return
	}
	switch why {
	case "idle":
		slog.Info("park: closing a process parked too long", "thread", thread, "parked", time.Since(e.since))
	case "evicted":
		slog.Info("park: closing the process parked longest to make room", "thread", thread, "parked", time.Since(e.since))
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	if err := e.proc.Close(ctx); err != nil {
		slog.Warn("park: closing a parked process", "thread", thread, "why", why, "err", err)
	}
	p.end(thread, done)
}

// remove takes e out of the pool. A why has its watcher close it; without
// one the caller does. p.mu is held.
func (p *Pool[P]) remove(thread api.ID, e *parked[P], why string) {
	delete(p.parked, thread)
	close(e.left)
	if why != "" {
		e.why, e.done = why, p.begin(thread)
	}
}

// begin records a close of thread's process under way. p.mu is held.
func (p *Pool[P]) begin(thread api.ID) chan struct{} {
	done := make(chan struct{})
	p.closing[thread] = append(p.closing[thread], done)
	return done
}

func (p *Pool[P]) end(thread api.ID, done chan struct{}) {
	p.mu.Lock()
	c := slices.DeleteFunc(p.closing[thread], func(d chan struct{}) bool { return d == done })
	if len(c) == 0 {
		delete(p.closing, thread)
	} else {
		p.closing[thread] = c
	}
	p.mu.Unlock()
	close(done)
}

func wait(ctx context.Context, waits []chan struct{}) error {
	for _, c := range waits {
		select {
		case <-c:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
