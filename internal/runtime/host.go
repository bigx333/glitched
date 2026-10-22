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

type Host struct {
	store   *store.Store
	adapter provider.Adapter
	name    string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	wake   chan struct{}

	mu   sync.Mutex
	busy map[api.ID]bool     // threads with an effect in flight
	runs map[api.ID]*liveRun // by run
	subs map[*stream]struct{}
}

// liveRun is a run this host drives. Cancelling ctx ends the run's provider
// calls and its pump.
type liveRun struct {
	run    api.ID
	ctx    context.Context
	cancel context.CancelFunc
	agent  provider.Agent // nil until the start succeeds
}

var _ api.Endpoint = (*Host)(nil)

// New reconciles what the last process left in flight, then starts the
// effect worker. The caller keeps owning st.
func New(ctx context.Context, st *store.Store, ad provider.Adapter) (*Host, error) {
	name, _ := os.Hostname()
	h := &Host{
		store: st, adapter: ad, name: name,
		wake: make(chan struct{}, 1),
		busy: map[api.ID]bool{},
		runs: map[api.ID]*liveRun{},
		subs: map[*stream]struct{}{},
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	st.OnCommit(h.committed)
	if err := h.boot(ctx); err != nil {
		h.Close()
		return nil, err
	}
	h.wg.Add(1)
	go h.work()
	return h, nil
}

// Close stops the worker and the agent pumps and records nothing on the way
// out. Agents keep running; the next Host reconciles them.
func (h *Host) Close() error {
	h.cancel()
	h.wg.Wait()
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

// follow stops driving a run the log moved past: a stop cancels a start
// still in flight, and an uncertain or finished run gets no more calls. h.mu
// is held.
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
		if lr := h.runs[b.Run]; lr != nil && lr.agent == nil {
			lr.cancel()
		}
	case api.TurnUncertain:
		h.forgetLocked(b.Run)
	case api.TurnEnded:
		h.forgetLocked(b.Run)
	}
}

func (h *Host) observe(thread api.ID, o engine.Observation, done int64) error {
	_, err := h.store.Observe(h.ctx, thread, o, time.Now(), done)
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
			if engine.Due(t, b) {
				err = h.observe(e.Thread, engine.SteerLost{Message: b.Message}, e.ID)
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
		if err := h.observe(id, engine.Reconciled{Run: r.ID, Restored: ok}, lease); err != nil {
			return err
		}
		if ok {
			h.attach(id, h.track(r.ID), restored.Agent)
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
		e, ok, err := h.store.Lease(h.ctx, h.busyThreads()...)
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

func (h *Host) busyThreads() []api.ID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Collect(maps.Keys(h.busy))
}

func (h *Host) lane(e store.Effect) {
	defer h.wg.Done()
	if err := h.perform(e); err != nil && h.ctx.Err() == nil {
		slog.Error("runtime: effect", "kind", engine.EffectKind(e.Body), "err", err)
	}
	h.mu.Lock()
	delete(h.busy, e.Thread)
	h.mu.Unlock()
	h.poke()
}

// perform checks the effect against the thread as it is now, acts on the
// agent, then commits what happened together with the effect's completion.
// When the host is closing it commits nothing, so the lease survives for
// boot to reconcile.
func (h *Host) perform(e store.Effect) error {
	ctx := h.ctx
	var lr *liveRun
	if b, ok := e.Body.(engine.StartTurn); ok {
		// Tracked before the check, so a stop that commits after it still
		// cancels the start.
		lr = h.track(b.Run)
	}
	t, err := h.store.Thread(ctx, e.Thread)
	due := err == nil && engine.Due(t, e.Body)
	if !due && lr != nil {
		h.forget(lr)
	}
	if err != nil {
		return err
	}
	if !due {
		return h.store.Complete(ctx, e.ID)
	}
	var o engine.Observation
	switch b := e.Body.(type) {
	case engine.StartTurn:
		ag, err := h.open(lr.ctx, e.Thread, b)
		if h.ctx.Err() != nil {
			return h.ctx.Err()
		}
		if err != nil {
			outcome := api.OutcomeFailed
			if lr.ctx.Err() != nil {
				outcome = api.OutcomeInterrupted
			}
			h.forget(lr)
			return h.observe(e.Thread, engine.Ended{Run: b.Run, Outcome: outcome, Error: err.Error()}, e.ID)
		}
		if err := h.observe(e.Thread, engine.Started{Run: b.Run}, e.ID); err != nil {
			return err
		}
		h.attach(e.Thread, lr, ag)
		return nil

	case engine.SteerTurn:
		o = engine.Steered{Message: b.Message}
		if err := h.act(b.Run, provider.Steer{Text: b.Text}); errors.Is(err, context.DeadlineExceeded) {
			o = engine.SteerLost{Message: b.Message}
		} else if err != nil {
			o = engine.SteerFailed{Message: b.Message, Error: err.Error()}
		}

	case engine.InterruptTurn:
		err := h.act(b.Run, provider.Interrupt{})
		if err == nil {
			// The agent reports its own end through the pump.
			return h.store.Complete(ctx, e.ID)
		}
		if h.ctx.Err() != nil {
			return h.ctx.Err()
		}
		h.mu.Lock()
		h.forgetLocked(b.Run)
		h.mu.Unlock()
		o = engine.Ended{Run: b.Run, Outcome: api.OutcomeInterrupted}
		if !errors.Is(err, errNoAgent) {
			o = engine.Ended{Run: b.Run, Outcome: api.OutcomeInterrupted, Error: err.Error()}
		}

	case engine.DeliverAnswer:
		o = engine.Delivered{Request: b.Request}
		if err := h.act(b.Run, provider.Respond{Request: b.Dedup, Answer: b.Answer}); err != nil {
			o = engine.DeliveryFailed{Request: b.Request, Error: err.Error()}
		}

	default:
		return fmt.Errorf("runtime: unknown effect %T", e.Body)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return h.observe(e.Thread, o, e.ID)
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
			closeAgent(ag)
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
	ctx, cancel := context.WithTimeout(lr.ctx, callTimeout)
	defer cancel()
	return lr.agent.Do(ctx, a)
}

func closeAgent(ag provider.Agent) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	ag.Close(ctx)
}

func (h *Host) track(run api.ID) *liveRun {
	lr := &liveRun{run: run}
	lr.ctx, lr.cancel = context.WithCancel(h.ctx)
	h.mu.Lock()
	h.runs[run] = lr
	h.mu.Unlock()
	return lr
}

func (h *Host) attach(thread api.ID, lr *liveRun, ag provider.Agent) {
	h.mu.Lock()
	lr.agent = ag
	h.mu.Unlock()
	h.wg.Add(1)
	go h.pump(thread, lr)
}

// forget stops driving lr. Its pump, if any, closes the agent.
func (h *Host) forget(lr *liveRun) {
	h.mu.Lock()
	defer h.mu.Unlock()
	lr.cancel()
	if h.runs[lr.run] == lr {
		delete(h.runs, lr.run)
	}
}

func (h *Host) forgetLocked(run api.ID) {
	if lr := h.runs[run]; lr != nil {
		lr.cancel()
		delete(h.runs, run)
	}
}
