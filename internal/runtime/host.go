// Package runtime wires the store, the engine and the provider adapters into
// a Host that serves api.Endpoint.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/providerlog"
	"glitched/internal/secrets"
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
	making    sync.Mutex           // held while a worktree is made, so a create sent twice at once makes one
	vault     *secrets.Vault       // the credentials users give agents; nil keeps none
	skills    *skillStage          // the skills Glitched gives agents; nil gives none
	wire      *providerlog.Log     // the provider log; nil keeps none
	now       func() time.Time     // wall clock; defaults to time.Now
	dial      Dial                 // how remote children's machines are reached; nil reaches none
	backoff   func(n int) time.Duration
	step      func(step string, placeholder api.ID)

	idleCompact atomic.Bool
	// awake hears whether any agent of the host's works, each time that
	// changes; working is the threads whose run does.
	awake   func(bool)
	working map[api.ID]bool
	// autoContinue has the runs a restart cut off as they worked on the
	// user's threads continued unasked, these and those of earlier boots.
	autoContinue bool
	continued    chan struct{} // poked at each commit while continueLost runs
	full         chan struct{} // poked as continueLost waits for a continued run to end
	lostDone     chan struct{} // closed once continueLost has been through every run due
	// limitResume has the engine continue a thread once the usage limit
	// that ended its last turn resets; limits is poked at each commit
	// that may change which thread is due.
	limitResume   atomic.Bool
	limitMu       sync.Mutex // orders a switch off before the worker's next continue
	limits        chan struct{}
	sweeps        chan struct{}
	compactCheck  chan api.ID
	compactRescan chan struct{}
	compactDue    chan compactDue

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
	closing map[api.ID]bool     // threads whose run is over but whose agent is not closed yet, true while that agent may still work
	opening int                 // provider opens under way, which may have the agent at work
	subs    map[*stream]struct{}
	asks    map[askKey]chan json.RawMessage // credentials requests glitched mcp waits on
	held    map[string]chan struct{}        // providers Hold holds, each closed on its release
	sent    map[api.ID]bool                 // compactions compactLoop started, until their start is performed
	// agents are the runs whose agent may run here: from their start until
	// it is closed, on past the run's own end.
	agents map[*liveRun]bool
	// reaching and followers cancel what reaches a placeholder's machine
	// for it: the tries of its create, a message or a narrowing, which a
	// settle or a delete ends, and its follower, which a delete ends.
	// heard is when each follower last heard from its child's machine.
	reaching  map[api.ID]context.CancelFunc
	followers map[api.ID]context.CancelFunc
	heard     map[api.ID]time.Time
}

// liveRun is a run this host drives. Cancelling ctx ends the run's provider
// calls and its pump; cancelling steering ends only its steers and answers.
type liveRun struct {
	run          api.ID
	provider     string
	ctx          context.Context
	cancel       context.CancelFunc
	steering     context.Context
	stopSteering context.CancelFunc
	agent        provider.Agent // nil until the start succeeds
	cut          bool           // the run was cut short as its agent opened, which may have set it to work
	// start is the start's own context: a stop cancels it, and the run
	// lives on, as the start may take anyway.
	start     context.Context
	stopStart context.CancelFunc
	// started is closed once the run's start is recorded: its agent may
	// call back through glitched mcp before then.
	started chan struct{}
	// delivering counts the answers on their way to the agent, from the
	// call that gives one until its receipt is tried; settled is closed as
	// the count comes back to none. ending is set as the pump reads the
	// agent's end of its turn. Set under h.mu.
	delivering int
	settled    chan struct{}
	ending     bool
	silent     bool // it runs a compaction; set under h.mu
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

// WithIdleCompaction enables or disables automatic idle compaction before
// prompt cache expiry. Enabled by default.
func WithIdleCompaction(enabled bool) Option {
	return func(h *Host) { h.idleCompact.Store(enabled) }
}

// WithAwake has awake told whether any of the host's agents works, each
// time that changes, in commit order: a run starting, running or stopping
// works; one waiting on the user, uncertain or ended doesn't.
func WithAwake(awake func(bool)) Option {
	return func(h *Host) { h.awake = awake }
}

// WithAutoContinue has the host continue, at boot, the runs of the user's
// threads the restart lost, as Continue does, two at a time.
func WithAutoContinue(on bool) Option {
	return func(h *Host) { h.autoContinue = on }
}

// WithName names the engine as its clients show it, when name isn't "";
// otherwise it is this machine's hostname.
func WithName(name string) Option {
	return func(h *Host) {
		if name != "" {
			h.name = name
		}
	}
}

// WithClock overrides the host's wall clock, for deterministic tests.
func WithClock(now func() time.Time) Option {
	return func(h *Host) { h.now = now }
}

// New reconciles what the last process left in flight, then starts the
// effect worker. The caller keeps owning st.
func New(ctx context.Context, st *store.Store, providers provider.Mux, opts ...Option) (*Host, error) {
	h := &Host{
		store: st, providers: providers,
		wake:          make(chan struct{}, 1),
		busy:          map[api.ID]bool{},
		runs:          map[api.ID]*liveRun{},
		closing:       map[api.ID]bool{},
		agents:        map[*liveRun]bool{},
		subs:          map[*stream]struct{}{},
		asks:          map[askKey]chan json.RawMessage{},
		held:          map[string]chan struct{}{},
		sent:          map[api.ID]bool{},
		working:       map[api.ID]bool{},
		continued:     make(chan struct{}, 1),
		full:          make(chan struct{}, 1),
		lostDone:      make(chan struct{}),
		limits:        make(chan struct{}, 1),
		sweeps:        make(chan struct{}, 1),
		compactCheck:  make(chan api.ID, 64),
		compactRescan: make(chan struct{}, 1),
		compactDue:    make(chan compactDue, 64),
		reaching:      map[api.ID]context.CancelFunc{},
		followers:     map[api.ID]context.CancelFunc{},
		heard:         map[api.ID]time.Time{},
		backoff:       remoteBackoff,
		step:          func(string, api.ID) {},
	}
	h.idleCompact.Store(true)
	for _, o := range opts {
		o(h)
	}
	if h.name == "" {
		h.name, _ = os.Hostname()
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.drain, h.stopDrain = context.WithCancel(context.Background())
	st.OnCommit(h.committed)
	if err := h.boot(ctx); err != nil {
		h.Close()
		return nil, err
	}
	if h.wire != nil {
		if err := h.forgetGone(ctx); err != nil {
			h.Close()
			return nil, err
		}
	}
	h.wg.Add(2)
	go h.work()
	go h.compactLoop()
	if h.autoContinue {
		h.wg.Add(1)
		go h.continueLost()
	}
	h.wg.Add(2)
	go h.resumeLimits()
	go h.runSweeps()
	bound, err := st.Bound(ctx)
	if err != nil {
		h.Close()
		return nil, err
	}
	h.mu.Lock()
	for _, id := range bound {
		h.startFollowing(id)
	}
	h.mu.Unlock()
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
	env := api.Environment{ID: h.store.HostID(), Name: h.name, Protocol: api.Protocol, Log: h.store.LogID()}
	return env, nil
}

func (h *Host) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	if link, ok := api.LinkOf(ctx); ok {
		if _, err := h.owned(ctx, link, q.Thread); err != nil {
			return api.Snapshot{}, err
		}
		return h.store.Read(ctx, q)
	}
	snap, err := h.store.Read(ctx, q)
	if err != nil || h.dial == nil {
		return snap, err
	}
	for i, t := range snap.Threads {
		r := t.Remote
		if r == nil {
			continue
		}
		at, err := h.store.Thread(ctx, t.ID)
		if err != nil {
			return api.Snapshot{}, err
		}
		if label, m, err := h.dial(api.Remote{Engine: at.Remote.Engine, Link: at.Remote.Link}); err == nil {
			m.Close()
			r.Machine = label
		}
		h.mu.Lock()
		r.Heard = h.heard[t.ID]
		h.mu.Unlock()
		snap.Threads[i].Remote = r
	}
	return snap, nil
}

// Submit commits c. A create in a new worktree makes the worktree first,
// and a delete that removes one leaves its removal to the commit's
// effects. A create in a folder under the worktrees commits while no
// worktree is made or removed, so a removal sees every thread in its
// worktree.
func (h *Host) Submit(ctx context.Context, c api.Command) (api.Receipt, error) {
	if link, ok := api.LinkOf(ctx); ok {
		return h.submitFromLink(ctx, link, c)
	}
	switch b := c.Body.(type) {
	case api.ThreadCreate:
		if b.Origin != nil || b.Repository != nil {
			return api.Receipt{}, &api.RejectedError{Reason: "only another machine's link starts a thread from there"}
		}
		if b.Remote != "" {
			return h.createRemote(ctx, c, b)
		}
		if b.Worktree != nil {
			return h.createInWorktree(ctx, c, b)
		}
		if h.worktrees != "" && under(h.worktrees, b.Workspace) {
			h.making.Lock()
			defer h.making.Unlock()
			if _, err := os.Stat(b.Workspace); err != nil {
				if t, terr := h.store.Thread(ctx, c.Target); terr != nil || !t.Exists() {
					return api.Receipt{}, &api.RejectedError{Reason: fmt.Sprintf("Can't start a thread in %s: it is gone", b.Workspace)}
				}
			}
		}
	case api.ThreadDelete:
		if b.RemoveWorktree {
			return h.deleteInWorktree(ctx, c)
		}
	}
	return h.store.Submit(ctx, c, h.clock())
}

func (h *Host) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
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
	for _, th := range c.Threads {
		h.noteWorking(th.ID, works[th.Run] && th.Remote == nil)
	}
	h.mu.Unlock()
	for _, th := range c.Threads {
		h.noteCompact(th.ID)
	}
	select {
	case h.continued <- struct{}{}:
	default:
	}
	// A turn's end, a cancel or a thread that moves on, as a run that
	// leaves the slot does, can change which usage limit is due.
	if slices.ContainsFunc(c.Threads, func(t api.Thread) bool { return t.LastTurn != nil && t.LastTurn.Limit != nil || !t.Live() }) {
		h.pokeLimits()
	}
	if movesSweeps(c.Threads) {
		h.pokeSweeps()
	}
	h.poke()
}

// works is the run states whose agent works.
var works = map[api.RunState]bool{api.RunStarting: true, api.RunRunning: true, api.RunStopping: true}

// noteWorking notes whether thread's run works, and tells awake whether
// any agent does when that changes. h.mu is held.
func (h *Host) noteWorking(thread api.ID, working bool) {
	was := h.anyWorks()
	if working {
		h.working[thread] = true
	} else {
		delete(h.working, thread)
	}
	h.tellAwake(was)
}

// anyWorks is whether any run works, a provider is opening an agent, or
// an agent whose run was cut short as it worked is not closed yet. h.mu
// is held.
func (h *Host) anyWorks() bool {
	return len(h.working) > 0 || h.opening > 0 || slices.Contains(slices.Collect(maps.Values(h.closing)), true)
}

func (h *Host) tellAwake(was bool) {
	if now := h.anyWorks(); now != was && h.awake != nil {
		h.awake(now)
	}
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
	threadDeleted = api.EventType(api.ThreadDeleted{})
	remoteBound   = api.EventType(api.RemoteBound{})
	remoteLost    = api.EventType(api.RemoteLost{})
	disposition   = api.EventType(api.DispositionChanged{})
)

// follow stops driving a run the log moved past: a stop cancels a start,
// steer or answer still in flight, and an uncertain or finished run gets no
// more calls. h.mu is held.
func (h *Host) follow(e api.Event) {
	switch e.Type {
	case remoteBound:
		h.startFollowing(e.Thread)
	case remoteLost, threadDeleted:
		h.stopFollowing(e.Thread)
	case disposition:
		if b, err := e.Decode(); err == nil && b.(api.DispositionChanged).Disposition.State == api.Settled {
			h.stopReaching(e.Thread)
		}
	}
	if e.Type == threadDeleted {
		h.skills.deleted(e.Thread)
		if h.wire != nil {
			if err := h.wire.Forget(e.Thread); err != nil {
				slog.Warn("the provider log keeps a deleted thread's files until its next sweep", "thread", e.Thread, "err", err)
			}
		}
		if h.vault != nil {
			go h.vault.Delete(e.Thread)
		}
		return
	}
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
				lr.stopStart()
			}
		}
	case api.TurnUncertain:
		h.leaveLocked(e.Thread, b.Run, false)
	case api.TurnEnded:
		h.leaveLocked(e.Thread, b.Run, b.Outcome == api.OutcomeAbandoned)
		h.skills.ended(e.Thread)
	}
}

// observe commits o with the completion of the effect done. A refused o
// arrived after the thread moved on: it is logged, and done is complete.
func (h *Host) observe(ctx context.Context, thread api.ID, o engine.Observation, done int64) error {
	_, err := h.store.Observe(ctx, thread, o, h.clock(), done)
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
		case engine.DeliverAnswer:
			// Kept in the effect, so a crash before it runs again doesn't
			// lose that the agent may have the answer.
			b.Retried = true
			err = h.store.Requeue(ctx, e.ID, b)
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
	var survivors []api.ID // threads an agent the last engine started may still run for
	for _, id := range threads {
		t, err := h.store.Thread(ctx, id)
		if err != nil {
			return err
		}
		h.mu.Lock()
		h.noteWorking(id, works[engine.View(t).Run] && t.Remote == nil)
		h.mu.Unlock()
		r := t.Live()
		lease, attempted := starts[r.ID]
		if r.State == engine.RunUncertain {
			survivors = append(survivors, id)
		}
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
		asked := false
		if r.State == engine.RunStopping {
			if asked, err = h.stopAsked(ctx, id, r.ID); err != nil {
				return err
			}
		}
		if err := h.observe(ctx, id, engine.Reconciled{Run: r.ID, Restored: ok, Failed: failed, Continue: h.autoContinue, Asked: asked}, lease); err != nil {
			return err
		}
		if ok {
			h.attach(id, h.track(r.ID, t.Model.Provider, restored.Agent))
		}
		if ok || failed {
			survivors = append(survivors, id)
		}
	}
	closing, err := h.store.Closing(ctx)
	if err != nil {
		return err
	}
	h.skills.boot(append(survivors, closing...))
	return nil
}

// ErrBusy is Hold's error while a run of the provider is live.
var ErrBusy = errors.New("runtime: a run of the provider is live")

// holdPoll is how often a Hold that waits looks for the provider's runs to
// end.
var holdPoll = 250 * time.Millisecond

// Hold has the host start no run of provider until release, once none is
// live: at once, else ErrBusy, unless wait has it wait for them to end
// while ctx lasts. A run that starts while held waits for release before
// its agent opens. One Hold of a provider at a time: another waits for
// its release. A compaction outranks no update: Hold stops one and waits
// for it to end, wait or not, and starts none while it holds.
func (h *Host) Hold(ctx context.Context, provider string, wait bool) (release func(), err error) {
	t := time.NewTicker(holdPoll)
	defer t.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		busy, compacting, err := h.unsettled(ctx, provider)
		if err != nil {
			return nil, err
		}
		h.mu.Lock()
		held := h.held[provider]
		for lr := range h.agents {
			switch {
			case lr.provider != provider:
			case lr.silent:
				compacting[lr.run] = true
			default:
				busy = busy || !compacting[lr.run]
			}
		}
		if held == nil && !busy && len(compacting) == 0 {
			done := make(chan struct{})
			h.held[provider] = done
			h.mu.Unlock()
			return sync.OnceFunc(func() {
				h.mu.Lock()
				delete(h.held, provider)
				h.mu.Unlock()
				close(done)
			}), nil
		}
		h.mu.Unlock()
		if busy && !wait {
			return nil, ErrBusy
		}
		select {
		case <-held:
		case <-t.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// unsettled is whether the store holds a run of provider name that may
// be running, or an agent's close of it not done, and the compactions of
// name's that are live, which it stops. The close counts what this host
// doesn't drive but whose process may still run: a run whose reconcile
// failed at boot, before and after it is stopped.
func (h *Host) unsettled(ctx context.Context, name string) (busy bool, compacting map[api.ID]bool, err error) {
	live, err := h.store.LiveThreads(ctx)
	if err != nil {
		return false, nil, err
	}
	closing, err := h.store.Closing(ctx)
	if err != nil {
		return false, nil, err
	}
	compacting = map[api.ID]bool{}
	for _, id := range append(live, closing...) {
		t, err := h.store.Thread(ctx, id)
		if err != nil {
			return false, nil, err
		}
		p := t.Model.Provider
		if r := t.Live(); r != nil {
			p = r.Model.Provider
			if p == name && r.State == engine.RunUncertain && !slices.Contains(closing, id) {
				if h.gone(ctx, id, *r) {
					continue
				}
			}
			if p == name && r.Silent && !slices.Contains(closing, id) {
				compacting[r.ID] = true
				if r.State != engine.RunStopping {
					if _, err := h.store.Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: api.TurnInterrupt{}}, h.clock()); err != nil && !errors.Is(err, engine.ErrRefused) {
						return false, nil, err
					}
				}
				continue
			}
		}
		if p == name {
			busy = true
		}
	}
	return busy, compacting, nil
}

// gone is whether the process of r, an uncertain run with no close to
// stop it, is gone, as far as the store's runs go: one this host drives
// is its agents' to count. Any other is one an earlier host left that
// boot didn't reconcile, as one whose interrupt failed before that host
// stopped, so its adapter is asked. An agent it restores is one nobody
// drives, so it is closed.
func (h *Host) gone(ctx context.Context, thread api.ID, r engine.Run) bool {
	h.mu.Lock()
	driven := slices.ContainsFunc(slices.Collect(maps.Keys(h.agents)), func(lr *liveRun) bool { return lr.run == r.ID })
	h.mu.Unlock()
	if driven {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	rec, err := h.providers.Reconcile(ctx, provider.RuntimeIdentity{Thread: thread, Run: r.ID, Generation: r.Generation, Provider: r.Model.Provider})
	if err != nil {
		return false
	}
	if restored, ok := rec.(provider.Restored); ok {
		restored.Agent.Close(ctx)
		return false
	}
	return true
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
// admits from being leased first. One cut short as it worked, as a
// deleted thread's is, works on until its agent closes.
func (h *Host) leaveLocked(thread, run api.ID, cut bool) {
	switch lr := h.runs[run]; {
	case lr == nil:
	case lr.agent != nil:
		h.closing[thread] = cut && h.working[thread]
	default:
		lr.cut = cut && h.working[thread]
	}
	h.forgetLocked(run)
}

func (h *Host) closed(thread api.ID) {
	h.mu.Lock()
	was := h.anyWorks()
	delete(h.closing, thread)
	h.tellAwake(was)
	h.mu.Unlock()
	h.poke()
}
