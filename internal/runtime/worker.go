package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/secrets"
	"glitched/internal/store"
)

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
	return slices.Concat(slices.Collect(maps.Keys(h.busy)), slices.Collect(maps.Keys(h.closing)))
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
		lr = h.track(b.Run, b.Model.Provider, nil)
		h.mu.Lock()
		lr.silent = b.ResumeOnly
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
		return
	}
	// Only an answer that is given, or a steer, holds the turn's end.
	var answering *liveRun
	var ready chan struct{}
	switch b := e.Body.(type) {
	case engine.DeliverAnswer:
		if !isAsk(b.Dedup) {
			answering = h.delivering(b.Run)
		}
	case engine.SteerTurn:
		if answering = h.delivering(b.Run); answering != nil {
			ready = h.awaitSteer(answering)
		}
	}
	o, ok := h.call(e, t, lr)
	if !ok {
		if answering != nil {
			h.delivered(answering)
		}
		return
	}
	if _, steer := e.Body.(engine.SteerTurn); steer && answering != nil {
		// The message is dispatched only once what a marking agent said
		// before it is shown, and before the turn's end can cancel it.
		a := answering
		if _, took := o.(engine.Steered); took && ready != nil {
			h.untilShown(a, ready)
		}
		h.record(e.Thread, o, e.ID)
		h.mu.Lock()
		a.steerReady = nil
		h.mu.Unlock()
		h.delivered(a)
		return
	} else if a := answering; a != nil {
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
			o = engine.SteerFailed{Message: b.Message, Error: secrets.Redact(err.Error(), h.credentials(e.Thread))}
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

	case engine.RemoteStop:
		return h.remoteCommand(e, "stop", api.Remote{Engine: b.Engine, Link: b.Link}, b.Create, b.Command, b.Narrow)

	case engine.RemoteSend:
		return h.remoteCommand(e, "message", api.Remote{Engine: b.Engine, Link: b.Link}, nil, b.Command, nil)

	case engine.RemoteConfigure:
		return h.remoteCommand(e, "narrowing", api.Remote{Engine: b.Engine, Link: b.Link}, nil, b.Command, nil)

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
		spec.MCP = append(spec.MCP, h.mcp.ForThread(thread, b.Access, t.Role, !t.UserOwned()))
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
		if s.Name == "glitched-browser" {
			if s.Env == nil {
				s.Env = map[string]string{}
			}
			s.Env["GLITCHED_THREAD"] = thread.String()
		}
		spec.MCP = append(spec.MCP, s)
	}
	// A fork that first sent a slash command already has a session.
	forked, err := h.store.Forked(ctx, thread)
	if err != nil {
		return nil, err
	}
	if forked != nil && b.ResumeOnly {
		return nil, errCompactNotDue
	}
	if forked != nil && t.Session == "" {
		spec.Session, spec.Fork = h.fork(ctx, thread, b, *forked)
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
	// it still has the conversation to resume. A fork records how it took
	// up the source conversation only after sending its first prompt.
	session := spec.Session
	if s, ok := ag.(provider.Sessioned); ok && s.Session() != "" {
		session = s.Session()
	}
	text := b.Text
	var took *engine.Bound
	native := false
	if r, ok := ag.(provider.NativeFork); ok {
		native = r.Native()
	}
	switch {
	case forked != nil && native:
		took = &engine.Bound{Run: b.Run, Session: session, Fork: api.ForkNative}
	case forked != nil && !slashCommand(text):
		took = &engine.Bound{Run: b.Run, Session: session, Fork: api.ForkPortable}
		var kept string
		if kept, err = h.forkHandoff(ctx, *forked); err == nil {
			text = kept + text
		}
	case session != t.Session:
		err = h.observe(ctx, thread, engine.Bound{Run: b.Run, Session: session}, 0)
	}
	// A turn that failed before its agent took it gave the agent no docs.
	delivered := slices.ContainsFunc(t.Queue, func(m engine.Message) bool { return m.State == engine.QueueDispatched })
	if forked == nil && !delivered && !b.ResumeOnly && !slashCommand(text) {
		if docs := loadCoordinatorDocs(b.Workspace, t.Project, t.Role, !t.Parent.IsZero() || t.Origin != nil); docs != "" {
			text = docs + text
		}
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
	lr := &liveRun{run: run, provider: provider, agent: ag, started: make(chan struct{}),
		pumped: make(chan struct{})}
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

// awaitSteer is what closes once lr's pump showed all that came before
// the steer on its way, for an agent that marks where it goes; nil for
// one that doesn't.
func (h *Host) awaitSteer(lr *liveRun) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m, ok := lr.agent.(provider.SteerMarker); !ok || !m.MarksSteer() {
		return nil
	}
	lr.steerReady = make(chan struct{})
	return lr.steerReady
}

// untilShown returns once lr's pump showed what a marking agent said
// before a steer, or once the pump stops.
func (h *Host) untilShown(lr *liveRun, ready chan struct{}) {
	select {
	case <-ready:
	case <-lr.pumped:
	case <-h.drain.Done():
	}
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
