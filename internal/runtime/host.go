// Package runtime wires the store, the engine and the provider adapters into
// a Host that serves api.Endpoint.
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
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/store"
)

var errNoAgent = errors.New("runtime: the run has no live agent")

// callTimeout bounds every provider call but Next.
var callTimeout = 30 * time.Second

// closeGrace is how long Close waits for what was already taken from agents
// to commit.
var closeGrace = 5 * time.Second

type Host struct {
	store     *store.Store
	providers provider.Mux
	name      string
	mcp       *mcp.Launcher        // how each run's agent starts glitched mcp; nil runs it without
	servers   []provider.MCPServer // the engine's own MCP servers, which every run's agent gets after glitched mcp
	worktrees string               // where the threads' worktrees go; "" makes none

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

// An Option changes how New's host runs.
type Option func(*Host)

// WithMCP gives each run's agent glitched mcp, started as l says.
func WithMCP(l mcp.Launcher) Option { return func(h *Host) { h.mcp = &l } }

// WithMCPServers gives each run's agent servers too, after glitched mcp:
// the engine's own, the same for every run, as the app's browser.
func WithMCPServers(servers ...provider.MCPServer) Option {
	return func(h *Host) { h.servers = servers }
}

// New reconciles what the last process left in flight, then starts the
// effect worker. The caller keeps owning st.
func New(ctx context.Context, st *store.Store, providers provider.Mux, opts ...Option) (*Host, error) {
	name, _ := os.Hostname()
	h := &Host{
		store: st, providers: providers, name: name,
		wake:    make(chan struct{}, 1),
		busy:    map[api.ID]bool{},
		runs:    map[api.ID]*liveRun{},
		closing: map[api.ID]bool{},
		subs:    map[*stream]struct{}{},
	}
	for _, o := range opts {
		o(h)
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
	return api.Environment{ID: h.store.HostID(), Name: h.name, Protocol: api.Protocol, Log: h.store.LogID()}, nil
}

func (h *Host) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	return h.store.Read(ctx, q)
}

// Submit commits c. A create in a new worktree makes the worktree first,
// and a delete that removes one removes it once the delete commits.
func (h *Host) Submit(ctx context.Context, c api.Command) (api.Receipt, error) {
	switch b := c.Body.(type) {
	case api.ThreadCreate:
		if b.Worktree != nil {
			return h.createInWorktree(ctx, c, b)
		}
	case api.ThreadDelete:
		if b.Worktree != api.KeepWorktree {
			return h.deleteInWorktree(ctx, c, b.Worktree)
		}
	}
	return h.store.Submit(ctx, c, time.Now())
}

// committed runs inside the store's commit order.
func (h *Host) committed(c store.Commit) {
	h.mu.Lock()
	for s := range h.subs {
		s.publish(c)
	}
	for _, e := range c.Events {
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
// A reconcile that fails leaves the run uncertain with a close that retries
// it. The lease of a start whose run is no longer live, or whose reconcile
// failed, stays as the store's record that an agent may run, until the run's
// close reconciles it.
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
		rctx, cancel := context.WithTimeout(ctx, callTimeout)
		rec, err := h.providers.Reconcile(rctx, provider.RuntimeIdentity{Thread: id, Run: r.ID, Generation: r.Generation, Provider: t.Model.Provider})
		cancel()
		failed := err != nil
		if failed {
			// The run's close retries the reconcile, and completes the lease.
			lease = 0
		}
		restored, ok := rec.(provider.Restored)
		if err := h.observe(ctx, id, engine.Reconciled{Run: r.ID, Restored: ok, Failed: failed}, lease); err != nil {
			return err
		}
		if ok {
			h.attach(id, h.track(r.ID, restored.Agent))
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
	// A report reaches no agent: it goes to the parent's log.
	if b, ok := e.Body.(engine.ReportToParent); ok {
		h.report(e, b)
		return
	}
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
	o, ok := h.call(e, t, lr)
	if !ok {
		return
	}
	if _, started := o.(engine.Started); h.record(e.Thread, o, e.ID) && started {
		h.attach(e.Thread, lr)
	}
}

// call makes the effect's provider call and returns what to record, nil for
// nothing but the completion. ok is false when the host closed mid-call.
func (h *Host) call(e store.Effect, t engine.Thread, lr *liveRun) (o engine.Observation, ok bool) {
	var err error
	switch b := e.Body.(type) {
	case engine.StartTurn:
		var ag provider.Agent
		if ag, err = h.open(lr.ctx, t, b); err == nil {
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

	case engine.CloseAgent:
		var start int64
		if !retry(h.ctx, "find start", func(ctx context.Context) (err error) {
			start, err = h.startLease(ctx, b.Run)
			return err
		}) {
			return nil, false
		}
		// A thread's first run whose start never ran has no agent, as boot's
		// rule says: its message was never dispatched, and no dead host left
		// its start leased. A later run's may: its provider can keep the
		// agent of an earlier run, and an earlier start can leave a process
		// whatever its message reached.
		if m, _ := t.Message(b.Run); m.State == engine.QueueCancelled && start == 0 && t.Generation <= 1 {
			return nil, true
		}
		// This host's pump closed the agent it held before the thread took
		// this effect, so an agent found now outlived an earlier host, or was
		// kept by its provider for the thread's next run.
		var rec provider.Recovery
		if !retry(h.ctx, "find agent", func(ctx context.Context) (err error) {
			ctx, cancel := context.WithTimeout(ctx, callTimeout)
			defer cancel()
			rec, err = h.providers.Reconcile(ctx, provider.RuntimeIdentity{Thread: e.Thread, Run: b.Run, Generation: b.Generation, Provider: t.Model.Provider})
			return err
		}) {
			return nil, false
		}
		if r, found := rec.(provider.Restored); found && !h.closeAgent(r.Agent) {
			return nil, false
		}
		if start != 0 && !retry(h.ctx, "complete start", func(ctx context.Context) error { return h.store.Complete(ctx, start) }) {
			return nil, false
		}
		return nil, true

	case engine.DeliverAnswer:
		o = engine.Delivered{Request: b.Request}
		switch err = h.act(b.Run, provider.Respond{Request: b.Dedup, Answer: b.Answer}); {
		case errors.Is(err, provider.ErrWithdrawn):
			o, err = engine.Unheard{Request: b.Request, Reason: err.Error()}, nil
		case err != nil:
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

// retryLogEvery is how often retry logs an error that has not changed.
var retryLogEvery = 10 * time.Minute

func retry(ctx context.Context, what string, fn func(context.Context) error) bool {
	var said string
	var saidAt time.Time
	for wait := 10 * time.Millisecond; ; wait = min(2*wait, time.Second) {
		err := fn(ctx)
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if msg := err.Error(); msg != said || time.Since(saidAt) >= retryLogEvery {
			slog.Error("runtime: "+what, "err", err, "retry", wait)
			said, saidAt = msg, time.Now()
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return false
		}
	}
}

// open starts the run's agent. A start that fails while the host is up
// closes what it opened.
func (h *Host) open(ctx context.Context, t engine.Thread, b engine.StartTurn) (provider.Agent, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	thread := t.ID
	spec := provider.SessionSpec{
		Thread: thread, Run: b.Run, Generation: b.Generation, Workspace: b.Workspace, Model: b.Model, Access: b.Access,
		Session: t.Session,
	}
	if h.mcp != nil {
		spec.MCP = append(spec.MCP, h.mcp.For(thread, b.Access))
	}
	for _, s := range h.servers {
		s.Args, s.Env = slices.Clone(s.Args), maps.Clone(s.Env)
		spec.MCP = append(spec.MCP, s)
	}
	ag, err := h.providers.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	// The session is stored before the prompt goes out, so a crash after
	// it still has the conversation to resume.
	if s, ok := ag.(provider.Sessioned); ok && s.Session() != "" && s.Session() != t.Session {
		err = h.observe(ctx, thread, engine.Bound{Run: b.Run, Session: s.Session()}, 0)
	}
	if err == nil {
		err = ag.Do(ctx, provider.Start{Text: b.Text})
	}
	if err != nil && h.ctx.Err() == nil {
		h.closeAgent(ag)
	}
	return ag, err
}

// startLease is the effect id of run's start when a dead host left it
// leased, or 0.
func (h *Host) startLease(ctx context.Context, run api.ID) (int64, error) {
	leased, err := h.store.Leased(ctx)
	if err != nil {
		return 0, err
	}
	for _, e := range leased {
		if b, ok := e.Body.(engine.StartTurn); ok && b.Run == run {
			return e.ID, nil
		}
	}
	return 0, nil
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

func (h *Host) closed(thread api.ID) {
	h.mu.Lock()
	delete(h.closing, thread)
	h.mu.Unlock()
	h.poke()
}
