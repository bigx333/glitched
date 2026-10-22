// Package runtime wires the store, the engine and a provider adapter into a
// Host that serves api.Endpoint.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/store"
)

const Protocol = 1

var errNoAgent = errors.New("runtime: the run has no live agent")

// callTimeout bounds every provider call but Next.
var callTimeout = 30 * time.Second

// closeGrace is how long Close waits for what was already taken from agents
// to commit.
var closeGrace = 5 * time.Second

type Host struct {
	store   *store.Store
	adapter provider.Adapter
	name    string

	ctx    context.Context
	cancel context.CancelFunc
	// drain outlives ctx by closeGrace, so commits of what was already taken
	// from an agent still land while the host closes.
	drain     context.Context
	stopDrain context.CancelFunc
	wg        sync.WaitGroup
	wake      chan struct{}

	mu      sync.Mutex
	busy    map[api.ID]bool     // threads with an effect in flight
	runs    map[api.ID]*liveRun // by run
	closing map[api.ID]bool     // threads whose run is over but whose agent is not closed yet
	subs    map[*stream]struct{}
}

// liveRun is a run this host drives. Cancelling ctx ends the run's provider
// calls and its pump; cancelling steering ends only its steers and answers.
type liveRun struct {
	run          api.ID
	ctx          context.Context
	cancel       context.CancelFunc
	steering     context.Context
	stopSteering context.CancelFunc
	agent        provider.Agent // nil until the start succeeds
}

var _ api.Endpoint = (*Host)(nil)

// New reconciles what the last process left in flight, then starts the
// effect worker. The caller keeps owning st.
func New(ctx context.Context, st *store.Store, ad provider.Adapter) (*Host, error) {
	name, _ := os.Hostname()
	h := &Host{
		store: st, adapter: ad, name: name,
		wake:    make(chan struct{}, 1),
		busy:    map[api.ID]bool{},
		runs:    map[api.ID]*liveRun{},
		closing: map[api.ID]bool{},
		subs:    map[*stream]struct{}{},
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.drain, h.stopDrain = context.WithCancel(context.Background())
	st.OnCommit(h.committed)
	if err := h.boot(ctx); err != nil {
		h.Close()
		return nil, err
	}
	h.wg.Add(1)
	go h.work()
	return h, nil
}

// Close stops leasing effects and reading agents, commits what was already
// taken from them, and records nothing else. Agents keep running; the next
// Host reconciles them.
func (h *Host) Close() error {
	h.cancel()
	grace := time.AfterFunc(closeGrace, h.stopDrain)
	h.wg.Wait()
	grace.Stop()
	h.stopDrain()
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		s.fail(errStreamClosed)
	}
	return nil
}

func (h *Host) Info(context.Context) (api.Environment, error) {
	return api.Environment{ID: h.store.HostID(), Name: h.name, Protocol: Protocol, Log: h.store.LogID()}, nil
}

func (h *Host) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	return h.store.Read(ctx, q)
}

func (h *Host) Submit(ctx context.Context, c api.Command) (api.Receipt, error) {
	return h.store.Submit(ctx, c, time.Now())
}

// committed runs inside the store's commit order.
func (h *Host) committed(events []api.Event) {
	h.mu.Lock()
	for s := range h.subs {
		s.publish(events)
	}
	for _, e := range events {
		h.follow(e)
	}
	h.mu.Unlock()
	h.poke()
}

func (h *Host) poke() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

var (
	turnStopping  = api.EventType(api.TurnStopping{})
	turnUncertain = api.EventType(api.TurnUncertain{})
	turnEnded     = api.EventType(api.TurnEnded{})
)

// follow stops driving a run the log moved past: a stop cancels a start,
// steer or answer still in flight, and an uncertain or finished run gets no
// more calls. h.mu is held.
func (h *Host) follow(e api.Event) {
	if e.Type != turnStopping && e.Type != turnUncertain && e.Type != turnEnded {
		return
	}
	body, err := e.Decode()
	if err != nil {
		return
	}
	switch b := body.(type) {
	case api.TurnStopping:
		if lr := h.runs[b.Run]; lr != nil {
			lr.stopSteering()
			if lr.agent == nil {
				lr.cancel()
			}
		}
	case api.TurnUncertain:
		h.leaveLocked(e.Thread, b.Run)
	case api.TurnEnded:
		h.leaveLocked(e.Thread, b.Run)
	}
}

// observe commits o with the completion of the effect done. A refused o
// arrived after the thread moved on: it is logged, and done is complete.
func (h *Host) observe(ctx context.Context, thread api.ID, o engine.Observation, done int64) error {
	_, err := h.store.Observe(ctx, thread, o, time.Now(), done)
	if errors.Is(err, engine.ErrRefused) {
		slog.Warn("runtime: observation refused", "thread", thread, "err", err)
		return nil
	}
	return err
}

// boot settles what a dead process left behind. A leased effect may have
// reached the agent: replay-safe kinds run again, a start is settled by
// reconciling its run, and a steer still due becomes uncertain. Every other
// live run is reconciled too, except one whose start was never attempted.
func (h *Host) boot(ctx context.Context) error {
	leased, err := h.store.Leased(ctx)
	if err != nil {
		return err
	}
	starts := map[api.ID]int64{}
	for _, e := range leased {
		switch b := e.Body.(type) {
		case engine.StartTurn:
			starts[b.Run] = e.ID
		case engine.SteerTurn:
			var t engine.Thread
			if t, err = h.store.Thread(ctx, e.Thread); err != nil {
				break
			}
			if m, _ := t.Message(b.Message); m.State == engine.QueueClaimed {
				err = h.observe(ctx, e.Thread, engine.SteerLost{Message: b.Message}, e.ID)
			} else {
				err = h.store.Complete(ctx, e.ID)
			}
		default:
			if !engine.ReplaySafe(e.Body) {
				return fmt.Errorf("runtime: no recovery for leased %s", engine.EffectKind(e.Body))
			}
			err = h.store.Release(ctx, e.ID)
		}
		if err != nil {
			return err
		}
	}
	threads, err := h.store.LiveThreads(ctx)
	if err != nil {
		return err
	}
	for _, id := range threads {
		t, err := h.store.Thread(ctx, id)
		if err != nil {
			return err
		}
		r := t.Live()
		lease, attempted := starts[r.ID]
		if r.State == engine.RunUncertain || (r.State == engine.RunStarting && !attempted) {
			continue
		}
		delete(starts, r.ID)
		rctx, cancel := context.WithTimeout(ctx, callTimeout)
		rec, err := h.adapter.Reconcile(rctx, provider.RuntimeIdentity{Thread: id, Run: r.ID, Generation: r.Generation})
		cancel()
		if err != nil {
			rec = provider.Lost{Reason: err.Error()}
		}
		restored, ok := rec.(provider.Restored)
		if err := h.observe(ctx, id, engine.Reconciled{Run: r.ID, Restored: ok}, lease); err != nil {
			return err
		}
		if ok {
			h.attach(id, h.track(r.ID, restored.Agent))
		}
	}
	for _, lease := range starts {
		if err := h.store.Complete(ctx, lease); err != nil {
			return err
		}
	}
	return nil
}

// work leases effects for threads that have none in flight, so one thread's
// effects run in order and threads never wait on each other.
func (h *Host) work() {
	defer h.wg.Done()
	for h.ctx.Err() == nil {
		e, ok, err := h.store.Lease(h.ctx, h.busyThreads)
		if err != nil && h.ctx.Err() == nil {
			slog.Error("runtime: lease", "err", err)
		}
		if ok {
			h.mu.Lock()
			h.busy[e.Thread] = true
			h.mu.Unlock()
			h.wg.Add(1)
			go h.lane(e)
			continue
		}
		select {
		case <-h.wake:
		case <-time.After(time.Second):
		case <-h.ctx.Done():
		}
	}
}

// busyThreads are those with an effect in flight or an agent still closing.
func (h *Host) busyThreads() []api.ID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append(slices.Collect(maps.Keys(h.busy)), slices.Collect(maps.Keys(h.closing))...)
}

func (h *Host) lane(e store.Effect) {
	defer h.wg.Done()
	h.perform(e)
	h.mu.Lock()
	delete(h.busy, e.Thread)
	h.mu.Unlock()
	h.poke()
}

// perform checks the effect against the thread as it is now, acts on the
// agent, then commits what happened together with the effect's completion.
// Until a call reaches the agent a failure is retried as if nothing had
// happened, and a closing host hands the lease back. After the call only the
// commit is retried, never the call; if the host closes mid-call the lease
// stays for boot to reconcile.
func (h *Host) perform(e store.Effect) {
	var lr *liveRun
	if b, ok := e.Body.(engine.StartTurn); ok {
		// Tracked before the check, so a stop that commits after it still
		// cancels the start.
		lr = h.track(b.Run, nil)
	}
	var t engine.Thread
	loaded := retry(h.ctx, "load thread", func(ctx context.Context) (err error) {
		t, err = h.store.Thread(ctx, e.Thread)
		return err
	})
	var due bool
	var skip engine.Observation
	if loaded {
		due, skip = engine.Due(t, e.Body)
	}
	if !due {
		if lr != nil {
			h.forget(lr.run)
		}
		if !loaded {
			retry(h.drain, "release effect", func(ctx context.Context) error { return h.store.Release(ctx, e.ID) })
			return
		}
		h.record(e.Thread, skip, e.ID)
		return
	}
	o, ok := h.call(e, lr)
	if !ok {
		return
	}
	if _, started := o.(engine.Started); h.record(e.Thread, o, e.ID) && started {
		h.attach(e.Thread, lr)
	}
}

// call makes the effect's provider call and returns what to record, nil for
// nothing but the completion. ok is false when the host closed mid-call.
func (h *Host) call(e store.Effect, lr *liveRun) (o engine.Observation, ok bool) {
	var err error
	switch b := e.Body.(type) {
	case engine.StartTurn:
		var ag provider.Agent
		if ag, err = h.open(lr.ctx, e.Thread, b); err == nil {
			h.mu.Lock()
			lr.agent = ag
			h.mu.Unlock()
			return engine.Started{Run: b.Run}, true
		}
		outcome := api.OutcomeFailed
		if lr.ctx.Err() != nil {
			outcome = api.OutcomeInterrupted
		}
		h.forget(b.Run)
		o = engine.Ended{Run: b.Run, Outcome: outcome, Error: err.Error()}

	case engine.SteerTurn:
		o = engine.Steered{Message: b.Message}
		if err = h.act(b.Run, provider.Steer{Text: b.Text}); errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			o = engine.SteerLost{Message: b.Message}
		} else if err != nil {
			o = engine.SteerFailed{Message: b.Message, Error: err.Error()}
		}

	case engine.InterruptTurn:
		switch err = h.act(b.Run, provider.Interrupt{}); {
		case err == nil:
			// The agent reports its own end through the pump.
			return engine.InterruptSent{Run: b.Run}, true
		case errors.Is(err, errNoAgent):
			// No agent ran the turn on this host, so there is nothing to stop.
			o = engine.Ended{Run: b.Run, Outcome: api.OutcomeInterrupted}
		default:
			slog.Warn("runtime: interrupt failed", "run", b.Run, "err", err)
			o = engine.InterruptLost{Run: b.Run}
		}

	case engine.DeliverAnswer:
		o = engine.Delivered{Request: b.Request}
		if err = h.act(b.Run, provider.Respond{Request: b.Dedup, Answer: b.Answer}); err != nil {
			o = engine.DeliveryFailed{Request: b.Request, Error: err.Error()}
		}

	default:
		slog.Error("runtime: unknown effect", "type", fmt.Sprintf("%T", e.Body))
		return nil, false
	}
	return o, err == nil || h.ctx.Err() == nil
}

// record commits o, or only the completion of the effect done when o is nil,
// retrying until it lands or the host's drain ends.
func (h *Host) record(thread api.ID, o engine.Observation, done int64) bool {
	return retry(h.drain, "commit", func(ctx context.Context) error {
		if o == nil {
			return h.store.Complete(ctx, done)
		}
		return h.observe(ctx, thread, o, done)
	})
}

// retry runs fn until it succeeds or ctx ends, backing off between tries.
func retry(ctx context.Context, what string, fn func(context.Context) error) bool {
	for wait := 10 * time.Millisecond; ; wait = min(2*wait, time.Second) {
		err := fn(ctx)
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		slog.Error("runtime: "+what, "err", err, "retry", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return false
		}
	}
}

// open starts the run's agent. A start that fails while the host is up
// closes what it opened.
func (h *Host) open(ctx context.Context, thread api.ID, b engine.StartTurn) (provider.Agent, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	ag, err := h.adapter.Open(ctx, provider.SessionSpec{
		Thread: thread, Run: b.Run, Generation: b.Generation, Workspace: b.Workspace, Model: b.Model, Access: b.Access,
	})
	if err == nil {
		if err = ag.Do(ctx, provider.Start{Text: b.Text}); err != nil && h.ctx.Err() == nil {
			h.closeAgent(ag)
		}
	}
	return ag, err
}

func (h *Host) act(run api.ID, a provider.Action) error {
	h.mu.Lock()
	lr := h.runs[run]
	h.mu.Unlock()
	if lr == nil || lr.agent == nil {
		return errNoAgent
	}
	parent := lr.steering
	if _, ok := a.(provider.Interrupt); ok {
		parent = lr.ctx
	}
	ctx, cancel := context.WithTimeout(parent, callTimeout)
	defer cancel()
	return lr.agent.Do(ctx, a)
}

// closeAgent retries a failed close until it succeeds or the host closes,
// and reports whether ag closed. The thread starts no other agent until then,
// since this one may still be working in its workspace.
func (h *Host) closeAgent(ag provider.Agent) bool {
	return retry(h.ctx, "close agent", func(context.Context) error {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		return ag.Close(ctx)
	})
}

func (h *Host) track(run api.ID, ag provider.Agent) *liveRun {
	lr := &liveRun{run: run, agent: ag}
	lr.ctx, lr.cancel = context.WithCancel(h.ctx)
	lr.steering, lr.stopSteering = context.WithCancel(lr.ctx)
	h.mu.Lock()
	h.runs[run] = lr
	h.mu.Unlock()
	return lr
}

func (h *Host) attach(thread api.ID, lr *liveRun) {
	h.wg.Add(1)
	go h.pump(thread, lr)
}

// forget stops driving the run. Its pump, if any, closes the agent.
func (h *Host) forget(run api.ID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.forgetLocked(run)
}

func (h *Host) forgetLocked(run api.ID) {
	if lr := h.runs[run]; lr != nil {
		lr.cancel()
		delete(h.runs, run)
	}
}

// leaveLocked forgets a run that is over for this host. A thread whose run
// leaves an agent behind takes no effect until the pump closes it. Marking
// that here, in the commit that ends the run, keeps a start the same commit
// admits from being leased first.
func (h *Host) leaveLocked(thread, run api.ID) {
	if lr := h.runs[run]; lr != nil && lr.agent != nil {
		h.closing[thread] = true
	}
	h.forgetLocked(run)
}

// closed lets the thread take effects again once its last agent closed.
func (h *Host) closed(thread api.ID) {
	h.mu.Lock()
	delete(h.closing, thread)
	h.mu.Unlock()
	h.poke()
}
