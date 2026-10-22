// Package runtime wires the store, the engine and a provider adapter into a
// Host that serves api.Endpoint.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/store"
)

const Protocol = 1

var errNoAgent = errors.New("runtime: the run has no live agent")

type Host struct {
	store   *store.Store
	adapter provider.Adapter
	name    string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	wake   chan struct{}

	mu     sync.Mutex
	agents map[api.ID]provider.Agent // by run
	subs   map[*stream]struct{}
}

var _ api.Endpoint = (*Host)(nil)

// New reconciles what the last process left in flight, then starts the
// effect worker. The caller keeps owning st.
func New(ctx context.Context, st *store.Store, ad provider.Adapter) (*Host, error) {
	name, _ := os.Hostname()
	h := &Host{
		store: st, adapter: ad, name: name,
		wake:   make(chan struct{}, 1),
		agents: map[api.ID]provider.Agent{},
		subs:   map[*stream]struct{}{},
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
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *Host) observe(thread api.ID, o engine.Observation, done int64) error {
	_, err := h.store.Observe(h.ctx, thread, o, time.Now(), done)
	return err
}

// boot settles what a dead process left behind. A leased effect may have
// reached the agent: replay-safe kinds run again, a start is settled by
// reconciling its run, and a steer becomes uncertain. Every other live run
// is reconciled too, except one whose start was never attempted.
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
			err = h.observe(e.Thread, engine.SteerLost{Message: b.Message}, e.ID)
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
		rec, err := h.adapter.Reconcile(ctx, provider.RuntimeIdentity{Thread: id, Run: r.ID, Generation: r.Generation})
		if err != nil {
			rec = provider.Lost{Reason: err.Error()}
		}
		restored, ok := rec.(provider.Restored)
		if err := h.observe(id, engine.Reconciled{Run: r.ID, Restored: ok}, lease); err != nil {
			return err
		}
		if ok {
			h.attach(id, r.ID, restored.Agent)
		}
	}
	for _, lease := range starts {
		if err := h.store.Complete(ctx, lease); err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) work() {
	defer h.wg.Done()
	for h.ctx.Err() == nil {
		e, ok, err := h.store.Lease(h.ctx)
		if err == nil && ok {
			err = h.perform(e)
		}
		if err != nil && h.ctx.Err() == nil {
			slog.Error("runtime: effect", "err", err)
		}
		if ok && err == nil {
			continue
		}
		select {
		case <-h.wake:
		case <-time.After(time.Second):
		case <-h.ctx.Done():
		}
	}
}

// perform checks the effect against the thread as it is now, acts on the
// agent, then commits what happened together with the effect's completion.
// When the host is closing it commits nothing, so the lease survives for
// boot to reconcile.
func (h *Host) perform(e store.Effect) error {
	ctx := h.ctx
	t, err := h.store.Thread(ctx, e.Thread)
	if err != nil {
		return err
	}
	var o engine.Observation
	switch b := e.Body.(type) {
	case engine.StartTurn:
		if r := t.Live(); r == nil || r.ID != b.Run || r.State != engine.RunStarting {
			return h.store.Complete(ctx, e.ID)
		}
		ag, err := h.adapter.Open(ctx, provider.SessionSpec{
			Thread: e.Thread, Run: b.Run, Generation: b.Generation, Workspace: b.Workspace, Model: b.Model, Access: b.Access,
		})
		if err == nil {
			err = ag.Do(ctx, provider.Start{Text: b.Text})
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if ag != nil {
				ag.Close(ctx)
			}
			return h.observe(e.Thread, engine.Ended{Run: b.Run, Outcome: api.OutcomeFailed, Error: err.Error()}, e.ID)
		}
		if err := h.observe(e.Thread, engine.Started{Run: b.Run}, e.ID); err != nil {
			return err
		}
		h.attach(e.Thread, b.Run, ag)
		return nil

	case engine.SteerTurn:
		if m, ok := t.Message(b.Message); !ok || m.State != engine.QueueClaimed {
			return h.store.Complete(ctx, e.ID)
		}
		o = engine.Steered{Message: b.Message}
		if err := h.act(b.Run, provider.Steer{Text: b.Text}); err != nil {
			o = engine.SteerFailed{Message: b.Message, Error: err.Error()}
		}

	case engine.InterruptTurn:
		if r := t.Live(); r == nil || r.ID != b.Run || r.State != engine.RunStopping {
			return h.store.Complete(ctx, e.ID)
		}
		err := h.act(b.Run, provider.Interrupt{})
		if err == nil {
			// The agent reports its own end through the pump.
			return h.store.Complete(ctx, e.ID)
		}
		if ag := h.detach(b.Run); ag != nil {
			ag.Close(ctx)
		}
		o = engine.Ended{Run: b.Run, Outcome: api.OutcomeInterrupted}
		if !errors.Is(err, errNoAgent) {
			o = engine.Ended{Run: b.Run, Outcome: api.OutcomeInterrupted, Error: err.Error()}
		}

	case engine.DeliverAnswer:
		if r, ok := t.Request(b.Request); !ok || r.State != api.RequestAnswered {
			return h.store.Complete(ctx, e.ID)
		}
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

func (h *Host) act(run api.ID, a provider.Action) error {
	h.mu.Lock()
	ag := h.agents[run]
	h.mu.Unlock()
	if ag == nil {
		return errNoAgent
	}
	return ag.Do(h.ctx, a)
}

func (h *Host) attach(thread, run api.ID, ag provider.Agent) {
	h.mu.Lock()
	h.agents[run] = ag
	h.mu.Unlock()
	h.wg.Add(1)
	go h.pump(thread, run, ag)
}

func (h *Host) detach(run api.ID) provider.Agent {
	h.mu.Lock()
	defer h.mu.Unlock()
	ag := h.agents[run]
	delete(h.agents, run)
	return ag
}
