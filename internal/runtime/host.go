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

	checkpoints *checkpointStores // where the threads' checkpoints go; nil keeps none

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
	// restoring is the worktrees whose files reverts put back, by the
	// thread reverting, waiting the threads whose start in one waits for
	// them to end, and passing those whose start got past them and isn't
	// tracked yet.
	restoring map[api.ID]*restoreMark
	waiting   map[api.ID]bool
	passing   map[api.ID]bool
	opening   int // provider opens under way, which may have the agent at work
	subs      map[*stream]struct{}
	asks      map[askKey]chan json.RawMessage // credentials requests glitched mcp waits on
	held      map[string]chan struct{}        // providers Hold holds, each closed on its release
	sent      map[api.ID]bool                 // compactions compactLoop started, until their start is performed
	// agents are the runs whose agent may run here: from their start until
	// it is closed, on past the run's own end.
	agents map[*liveRun]bool
	// confirming is the revert commands whose files are being saved
	// before they commit, from before they save them until their submit
	// returns: a submit of the same command waits for it.
	confirming map[api.ID]confirmation
	// creating and followers cancel what reaches a placeholder's machine
	// for it: its create's tries, and its follower.
	creating  map[api.ID]context.CancelFunc
	followers map[api.ID]context.CancelFunc
}

// confirmation is a revert command being confirmed: its thread, the
// checkpoint it is to put back, and done, closed once its submit returns.
type confirmation struct {
	thread api.ID
	to     api.CheckpointRef
	done   chan struct{}
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
		restoring:     map[api.ID]*restoreMark{},
		confirming:    map[api.ID]confirmation{},
		waiting:       map[api.ID]bool{},
		passing:       map[api.ID]bool{},
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
		compactCheck:  make(chan api.ID, 64),
		compactRescan: make(chan struct{}, 1),
		compactDue:    make(chan compactDue, 64),
		creating:      map[api.ID]context.CancelFunc{},
		followers:     map[api.ID]context.CancelFunc{},
		backoff:       remoteBackoff,
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
	if h.checkpoints != nil {
		for _, store := range h.checkpoints.stores() {
			h.checkpoints.measure(ctx, store)
		}
	}
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
	h.wg.Add(1)
	go h.resumeLimits()
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
	if h.checkpoints != nil {
		env.Checkpoints = api.CheckpointStore{Bytes: h.checkpoints.total(), Limit: checkpointLimit}
	}
	return env, nil
}

func (h *Host) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	if link, ok := api.LinkOf(ctx); ok {
		if _, err := h.owned(ctx, link, q.Thread); err != nil {
			return api.Snapshot{}, err
		}
	}
	return h.store.Read(ctx, q)
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
	case api.ThreadRevert:
		return h.revertInWorktree(ctx, c)
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
		h.noteReverting(e, c.Threads)
	}
	for _, th := range c.Threads {
		h.noteWorking(th.ID, works[th.Run])
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
		h.noteWorking(id, works[engine.View(t).Run])
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
	return h.bootReverting(ctx)
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
	return slices.Concat(slices.Collect(maps.Keys(h.busy)), slices.Collect(maps.Keys(h.closing)), slices.Collect(maps.Keys(h.waiting)))
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
	if _, ok := e.Body.(engine.StartTurn); ok && h.waitsOnRevert(e) {
		return
	}
	var lr *liveRun
	if b, ok := e.Body.(engine.StartTurn); ok {
		// Tracked before the check, so a stop that commits after it still
		// cancels the start.
		lr = h.track(b.Run, b.Model.Provider, nil)
		h.mu.Lock()
		lr.silent = b.ResumeOnly
		delete(h.passing, e.Thread)
		h.mu.Unlock()
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
		// An answer the last engine was giving as it stopped may have
		// reached the agent: one this run can no longer take is not known
		// to have failed.
		if f, ok := skip.(engine.DeliveryFailed); ok && e.Body.(engine.DeliverAnswer).Retried {
			f.Error, f.Unconfirmed = "the engine stopped as it gave the answer, so the agent may have it", true
			skip = f
		}
		if b, ok := e.Body.(engine.StartTurn); ok && due && b.ResumeOnly && !h.ownCompaction(b.Run, t) {
			due, skip = false, engine.Ended{Run: b.Run, Outcome: api.OutcomeInterrupted, Error: errCompactNotDue.Error()}
		}
	}
	if !due {
		if lr != nil {
			h.forget(lr.run)
			h.closedAgent(lr)
		}
		if !loaded {
			retry(h.drain, "release effect", func(ctx context.Context) error { return h.store.Release(ctx, e.ID) })
			return
		}
		h.record(e.Thread, skip, e.ID)
		if b, ok := e.Body.(engine.RevertThread); ok && !b.KeepFiles {
			h.revertEnded(e.Thread)
		}
		return
	}
	// Only an answer that is given holds the turn's end.
	var answering *liveRun
	if b, ok := e.Body.(engine.DeliverAnswer); ok && !isAsk(b.Dedup) {
		answering = h.delivering(b.Run)
	}
	o, ok := h.call(e, t, lr)
	if !ok {
		if answering != nil {
			h.delivered(answering)
		}
		return
	}
	if a := answering; a != nil {
		// A turn that ended as its answer went waits for the answer's
		// receipt, tried once, so a store that won't take it doesn't hold
		// the turn. An answer that failed after the agent ended its turn
		// is logged after that end, as it did not stop the turn.
		if _, took := o.(engine.Delivered); took {
			err := h.observe(h.drain, e.Thread, o, e.ID)
			h.delivered(a)
			if err == nil {
				return
			}
		} else if h.delivered(a) {
			<-a.ctx.Done()
		}
	}
	if b, ok := e.Body.(engine.RevertThread); ok && !b.KeepFiles {
		defer h.revertEnded(e.Thread)
	}
	if _, started := o.(engine.Started); !started {
		h.record(e.Thread, o, e.ID)
		return
	}
	// The start's lease is the record that the agent may still run, so it
	// is completed only once the agent is this host's pump's or closed. A
	// closing host leaves the agent running and its pump closes nothing:
	// the lease is then the next host's, to take up or close the agent,
	// whatever the run is by then.
	if h.ctx.Err() != nil {
		return
	}
	var over bool
	if !retry(h.drain, "commit", func(ctx context.Context) error {
		_, err := h.store.Observe(ctx, e.Thread, o, h.clock(), 0)
		if over = errors.Is(err, engine.ErrRefused); over {
			return nil
		}
		return err
	}) {
		return
	}
	switch {
	case over:
		// The run was over before its start was recorded.
		if !h.closeAgent(lr.agent) {
			return
		}
		h.closedAgent(lr)
		h.closed(e.Thread)
	case h.ctx.Err() != nil:
		return
	}
	if !retry(h.drain, "complete start", func(ctx context.Context) error { return h.store.Complete(ctx, e.ID) }) || over {
		return
	}
	close(lr.started)
	h.attach(e.Thread, lr)
}

// call makes the effect's provider call and returns what to record, nil for
// nothing but the completion. ok is false when the host closed mid-call.
func (h *Host) call(e store.Effect, t engine.Thread, lr *liveRun) (o engine.Observation, ok bool) {
	var err error
	switch b := e.Body.(type) {
	case engine.StartTurn:
		h.mu.Lock()
		was := h.anyWorks()
		h.opening++
		h.tellAwake(was)
		h.mu.Unlock()
		var ag provider.Agent
		ag, err = h.open(lr.start, t, b)
		h.mu.Lock()
		if err == nil {
			lr.agent = ag
			if lr.cut {
				h.closing[t.ID] = true
			}
		}
		was = h.anyWorks()
		h.opening--
		h.tellAwake(was)
		h.mu.Unlock()
		if err == nil {
			return engine.Started{Run: b.Run}, true
		}
		outcome := api.OutcomeFailed
		if lr.start.Err() != nil || errors.Is(err, errCompactNotDue) {
			outcome = api.OutcomeInterrupted
		}
		h.forget(b.Run)
		h.closedAgent(lr)
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
			h.skills.closed(e.Thread)
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
		h.skills.closed(e.Thread)
		return nil, true

	case engine.RemoveWorktree:
		return nil, h.removeWorktree(h.ctx, e.Thread, b)

	case engine.MakeWorktree:
		return h.makeWorktree(h.ctx, t, b)

	case engine.RemoteCreate:
		return h.remoteCreate(e, b)

	case engine.CaptureCheckpoint:
		return h.capture(t, b)

	case engine.DropCheckpoints:
		return nil, h.dropCheckpoints(e.Thread)

	case engine.RevertThread:
		return h.revert(t, b)

	case engine.DeliverAnswer:
		if isAsk(b.Dedup) {
			return h.answerAsk(b), true
		}
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
	// Before the call's timeout: an update holds the provider for as long
	// as its CLI takes to replace itself.
	h.mu.Lock()
	held := h.held[b.Model.Provider]
	h.mu.Unlock()
	// A compaction waits for nothing: it is due now or not at all.
	if held != nil && b.ResumeOnly {
		return nil, errCompactNotDue
	}
	if held != nil {
		select {
		case <-held:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	thread := t.ID
	spec := provider.SessionSpec{
		Thread: thread, Run: b.Run, Generation: b.Generation, Workspace: b.Workspace, Model: b.Model, Access: b.Access,
		Session: t.Session, ResumeOnly: b.ResumeOnly, CacheExpires: t.Context.CacheExpires, Tap: h.tap(thread),
	}
	if h.mcp != nil {
		spec.MCP = append(spec.MCP, h.mcp.For(thread, b.Access))
	}
	if h.vault != nil {
		dir, err := h.vault.Prepare(thread)
		if err != nil {
			return nil, err
		}
		spec.Credentials = dir
	}
	for _, s := range h.servers {
		s.Args, s.Env = slices.Clone(s.Args), maps.Clone(s.Env)
		spec.MCP = append(spec.MCP, s)
	}
	// A rewound conversation the thread's runs have yet to take up has
	// nothing of the thread's to compact.
	rewound, err := h.store.Rewound(ctx, thread)
	if err != nil {
		return nil, err
	}
	// A fork whose runs have yet to take its source up does so as a
	// rewind's next run does; one whose first message was a command of
	// the agent's has a conversation of its own already, which the next
	// run goes on in, handed the source's turns.
	forked, err := h.store.Forked(ctx, thread)
	if err != nil {
		return nil, err
	}
	if (rewound != nil || forked != nil) && b.ResumeOnly {
		return nil, errCompactNotDue
	}
	switch {
	case rewound != nil:
		spec.Rewound = &provider.Rewound{From: rewound.From, At: rewound.At}
	case forked != nil && t.Session == "":
		spec.Session, spec.Rewound = h.fork(ctx, thread, b, *forked)
	}
	spec.Skills = h.skills.stage(thread, b.Workspace, t.Project)
	ag, err := h.providers.Open(ctx, spec)
	h.skills.opened(thread, spec.Skills.Root, err == nil)
	if errors.Is(err, provider.ErrDeclined) {
		return nil, errCompactNotDue
	}
	if err != nil {
		return nil, err
	}
	// The session is stored before the prompt goes out, so a crash after
	// it still has the conversation to resume. The first run after a
	// rewind says how it took the conversation up once its message went
	// out, which ends the rewind; an agent that didn't take up the native
	// one gets the kept turns, again if its first message never went.
	session := spec.Session
	if s, ok := ag.(provider.Sessioned); ok && s.Session() != "" {
		session = s.Session()
	}
	text := b.Text
	var took *engine.Bound
	native := false
	if r, ok := ag.(provider.Rewinding); ok {
		native = r.Native()
	}
	switch {
	case rewound != nil && forked != nil && slashCommand(text):
		// A fork's turns would make the command words: the rewind and
		// the fork wait for the next run, which takes both up in the
		// conversation the command ran in.
		if session != t.Session {
			err = h.observe(ctx, thread, engine.Bound{Run: b.Run, Session: session}, 0)
		}
	case rewound != nil:
		took = &engine.Bound{Run: b.Run, Session: session, Rewound: api.RewindPortable}
		if native {
			took.Rewound = api.RewindNative
		}
		if took.Rewound == api.RewindPortable || forked != nil {
			var kept string
			to, frame := rewound.To.ID, rewindFrame
			if len(rewound.Restored) > 0 {
				to, frame = rewound.Restored[len(rewound.Restored)-1], unrewindFrame
			}
			if kept, err = h.handoff(ctx, t, to, frame); err == nil {
				text = kept + text
			}
		}
	case forked != nil && native:
		took = &engine.Bound{Run: b.Run, Session: session, Rewound: api.RewindNative}
	case forked != nil && !slashCommand(text):
		took = &engine.Bound{Run: b.Run, Session: session, Rewound: api.RewindPortable}
		var kept string
		if kept, err = h.forkHandoff(ctx, *forked); err == nil {
			text = kept + text
		}
	case session != t.Session:
		err = h.observe(ctx, thread, engine.Bound{Run: b.Run, Session: session}, 0)
	}
	// What the user did while the agent opened, or the time it took,
	// may have made a compaction due no longer.
	if err == nil && b.ResumeOnly {
		var now engine.Thread
		if now, err = h.store.Thread(ctx, thread); err == nil && !h.stillCompacting(now, b) {
			err = errCompactNotDue
		}
	}
	// A stop while the agent opened means the prompt was never sent: an
	// agent that starts on a context of its own would send it anyway.
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = ag.Do(ctx, provider.Start{Text: text})
	}
	// Sent, the message holds the kept turns whatever a stop does now.
	if err == nil && took != nil {
		err = h.observe(h.drain, thread, *took, 0)
	}
	if err != nil && h.ctx.Err() == nil {
		h.closeAgent(ag)
	}
	if err == nil {
		h.skills.sweep()
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

func (h *Host) track(run api.ID, provider string, ag provider.Agent) *liveRun {
	lr := &liveRun{run: run, provider: provider, agent: ag, started: make(chan struct{})}
	if ag != nil {
		close(lr.started)
	}
	lr.ctx, lr.cancel = context.WithCancel(h.ctx)
	lr.steering, lr.stopSteering = context.WithCancel(lr.ctx)
	lr.start, lr.stopStart = context.WithCancel(lr.ctx)
	h.mu.Lock()
	h.runs[run] = lr
	h.agents[lr] = true
	h.mu.Unlock()
	return lr
}

// delivering is the live run an answer is on its way to, or nil.
func (h *Host) delivering(run api.ID) *liveRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	lr := h.runs[run]
	if lr == nil {
		return nil
	}
	if lr.delivering == 0 {
		lr.settled = make(chan struct{})
	}
	lr.delivering++
	return lr
}

// delivered is that an answer's call is over, and whether lr's agent ended
// its turn before it was.
func (h *Host) delivered(lr *liveRun) (ending bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if lr.delivering--; lr.delivering == 0 {
		close(lr.settled)
	}
	return lr.ending
}

// closedAgent is that lr's agent, if it opened one, is closed.
func (h *Host) closedAgent(lr *liveRun) {
	h.mu.Lock()
	delete(h.agents, lr)
	h.mu.Unlock()
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
