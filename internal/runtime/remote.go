package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/store"
	"glitched/internal/transport"
)

// A remote child runs on another machine's engine, which this one reaches
// through a link (docs/daemon/child-threads.md, Remote children). Its
// placeholder here creates it there with its frozen command, then follows
// it, and reports what it does to its parent as a local child would.

// A Machine is another machine's engine as a placeholder reaches it.
type Machine interface {
	api.Endpoint
	ChildReply(ctx context.Context, q api.ChildReplyQuery) (api.ChildReply, error)
	Close() error
}

// Dial reaches the machine at names, through its link, as this engine
// has the link now: the machine's label, for people, and a client of it.
// ErrNoMachine is that this engine no longer holds the link.
type Dial func(at api.Remote) (label string, m Machine, err error)

// ErrNoMachine is a link this engine no longer holds: its machine was
// removed from the list.
var ErrNoMachine = errors.New("runtime: the machine was removed from this machine's list")

// WithRemotes has the host reach remote children's machines through dial.
// Without it, a placeholder's create waits, and nothing is followed.
func WithRemotes(dial Dial) Option { return func(h *Host) { h.dial = dial } }

// WithRemoteBackoff has the host wait backoff(n) before its nth retry,
// from 0, of a call to a remote child's machine that failed.
func WithRemoteBackoff(backoff func(n int) time.Duration) Option {
	return func(h *Host) { h.backoff = backoff }
}

// WithRemoteSteps has the host call step as a remote child's create
// reaches each of its steps: "due" once its effect is found due, before
// its first try is recorded, and "try" before each try at its machine.
// Tests hold the create there.
func WithRemoteSteps(step func(step string, placeholder api.ID)) Option {
	return func(h *Host) { h.step = step }
}

// remoteBackoff is T3 Code's: from 2s, doubling, at most 5 minutes.
func remoteBackoff(n int) time.Duration { return min(2*time.Second<<min(n, 8), 5*time.Minute) }

// How a call to a remote child's machine went, for what comes next.
type class uint8

const (
	network          class = iota // try again after the backoff, forever
	catchUp                       // read again from the cursor stored, at once
	obsolete                      // the placeholder moved on: look at it again, at once
	handshakeRefused              // the handshake was refused: final the second time in a row
	final                         // the child is lost
	defect                        // count it: three in a row lose the child
)

var (
	errNoDial         = errors.New("runtime: this engine reaches no other machine")
	errAnotherMachine = errors.New("runtime: the machine is another")
	errObsolete       = errors.New("runtime: the placeholder moved on")
	errDefect         = errors.New("runtime: a defect")
)

// classify is the one place a failed call to a remote child's machine,
// labelled label, is judged, whichever call it was: its class, and the
// reason a final one loses the child for.
func classify(err error, label string) (class, string) {
	var rej *api.RejectedError
	var remote *transport.RemoteError
	switch {
	case errors.Is(err, errObsolete):
		return obsolete, ""
	case errors.Is(err, api.ErrOverflow):
		return catchUp, ""
	case errors.Is(err, transport.ErrRefused):
		return handshakeRefused, label + " no longer accepts this machine's link, so what runs there is its person's to stop by hand"
	case errors.As(err, &rej):
		return final, label + " refused the child: " + rej.Reason
	case errors.Is(err, api.ErrNotAllowed):
		return final, label + " no longer lets this machine's link reach the child"
	case errors.Is(err, api.ErrConflict):
		return final, label + " holds another command under this one's id"
	case errors.Is(err, api.ErrNotFound):
		return final, "deleted on " + label
	case errors.Is(err, errAnotherMachine):
		return final, label + " is now another machine"
	case errors.Is(err, ErrNoMachine):
		return final, "its machine was removed from this machine's list"
	case errors.Is(err, errDefect), errors.As(err, &remote):
		return defect, ""
	}
	return network, ""
}

// persist calls attempt until it succeeds, or its child is lost, or ctx
// ends: then ok is false. Network failures wait out the backoff and never
// end it. A refused handshake loses the child the second time in a row,
// and three defects in a row lose it as a child that may still run
// there: T3 Code's wording. An attempt that made progress, as a commit,
// starts the counts and the backoff over, a network failure or a refusal
// between defects starts their count over, and a network failure or a
// defect between refusals starts theirs. A panic is a defect.
func (h *Host) persist(ctx context.Context, attempt func(context.Context) (label string, progressed bool, err error)) (lost, label string, ok bool) {
	var n, defects, refusals int
	for {
		var progressed bool
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("%w: %v", errDefect, p)
				}
			}()
			label, progressed, err = attempt(ctx)
		}()
		if ctx.Err() != nil {
			return "", label, false
		}
		if err == nil {
			return "", label, true
		}
		if progressed {
			n, defects, refusals = 0, 0, 0
		}
		c, reason := classify(err, label)
		switch c {
		case final:
			return reason, label, true
		case handshakeRefused:
			if refusals++; refusals >= 2 {
				return reason, label, true
			}
			defects = 0
		case defect:
			refusals = 0
			if defects++; defects >= 3 {
				return fmt.Sprintf("following the child on %s failed repeatedly, so it may still run there: %v", label, err), label, true
			}
		case network:
			defects, refusals = 0, 0
		case catchUp, obsolete:
			refusals = 0
			continue
		}
		slog.Warn("runtime: a remote child's machine", "machine", label, "err", err)
		select {
		case <-time.After(h.backoff(n)):
		case <-ctx.Done():
			return "", label, false
		}
		n++
	}
}

// reach dials the machine at names, and checks it is still the engine and
// the link the placeholder was placed on.
func (h *Host) reach(ctx context.Context, at api.Remote) (Machine, string, error) {
	if h.dial == nil {
		return nil, "", errNoDial
	}
	label, m, err := h.dial(at)
	if err != nil {
		return nil, label, err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	env, err := m.Info(ctx)
	if err == nil && (env.ID != at.Engine || env.Link != at.Link) {
		err = errAnotherMachine
	}
	if err != nil {
		m.Close()
		return nil, label, err
	}
	return m, label, nil
}

// remoteCreate performs b, the create of a placeholder's child on its
// machine. Its first try is recorded first, and none is made once the
// placeholder refuses that, as it does once settled or deleted. Then it
// sends the frozen command until the machine answers: a receipt binds
// the placeholder, a refusal loses it. ok is false when the host closed
// first, which leaves the lease for the next host to replay.
func (h *Host) remoteCreate(e store.Effect, b engine.RemoteCreate) (engine.Observation, bool) {
	ctx, done := h.reachFor(e.Thread)
	defer done()
	if h.dial == nil {
		// Nothing tried, the lease waits for a host that reaches the
		// machine, unless the placeholder is deleted first.
		<-ctx.Done()
		return nil, h.ctx.Err() == nil
	}
	h.step("due", e.Thread)
	tried := false
	if !retry(ctx, "record a remote create's try", func(ctx context.Context) error {
		_, err := h.store.Observe(ctx, e.Thread, engine.RemoteAttempted{Create: b.Command}, h.clock(), 0)
		if errors.Is(err, engine.ErrRefused) {
			return nil
		}
		tried = err == nil
		return err
	}) {
		return nil, h.ctx.Err() == nil
	}
	if !tried {
		return nil, true
	}
	lost, label, ok := h.persist(ctx, func(ctx context.Context) (string, bool, error) {
		h.step("try", e.Thread)
		m, label, err := h.reach(ctx, api.Remote{Engine: b.Engine, Link: b.Link})
		if err != nil {
			return label, false, err
		}
		defer m.Close()
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, err = m.Submit(ctx, b.Command)
		return label, false, err
	})
	switch {
	case !ok:
		// Settled or deleted, it binds nothing, and its stop sends it
		// again; else the host closed.
		return nil, h.ctx.Err() == nil
	case lost != "":
		return engine.RemoteLost{Reason: lost, On: label}, true
	}
	return engine.RemoteBound{Thread: b.Command.Target}, true
}

// startFollowing follows placeholder from now on, unless it already is
// or the host closes. h.mu is held.
func (h *Host) startFollowing(placeholder api.ID) {
	if h.followers[placeholder] != nil || h.ctx.Err() != nil || h.dial == nil {
		return
	}
	ctx, cancel := context.WithCancel(h.ctx)
	h.followers[placeholder] = cancel
	h.wg.Add(1)
	go h.followRemote(ctx, placeholder)
}

// stopFollowing stops what reaches placeholder's machine for it but its
// stop: its follower, and the tries stopReaching ends. h.mu is held.
func (h *Host) stopFollowing(placeholder api.ID) {
	h.stopReaching(placeholder)
	if cancel := h.followers[placeholder]; cancel != nil {
		cancel()
	}
	delete(h.heard, placeholder)
}

// stopReaching ends the tries of placeholder's create, message or
// narrowing, so its stop, after them in its lane, goes next. h.mu is
// held.
func (h *Host) stopReaching(placeholder api.ID) {
	if cancel := h.reaching[placeholder]; cancel != nil {
		cancel()
	}
}

// reachFor is the context of a try to reach placeholder's machine for it
// that stopReaching ends, and done, which forgets it.
func (h *Host) reachFor(placeholder api.ID) (ctx context.Context, done func()) {
	ctx, cancel := context.WithCancel(h.ctx)
	h.mu.Lock()
	h.reaching[placeholder] = cancel
	h.mu.Unlock()
	return ctx, func() {
		cancel()
		h.mu.Lock()
		delete(h.reaching, placeholder)
		h.mu.Unlock()
	}
}

// hear records that placeholder's follower heard from its machine now.
func (h *Host) hear(placeholder api.ID) {
	h.mu.Lock()
	h.heard[placeholder] = h.clock()
	h.mu.Unlock()
}

// remoteCommand sends c, a command of a placeholder's settle, delete,
// stop, message or narrowing, which what names, to its child on its
// machine at, until the machine answers, with each try classified as a
// create's or a follower's is. Create, when set, is the placeholder's
// create made settled, and goes first: the machine makes the child
// settled, or already has it and takes c; one it refuses left nothing
// there. A refusal of c is logged and ends it, as a refused stop of a
// local child records nothing: the child there stays as it is. A child
// lost on the way is lost here too, the stop that didn't reach it said:
// what runs there is its person's to stop. A message or a narrowing is
// ended by a settle or a delete, which the stop after it takes up.
func (h *Host) remoteCommand(e store.Effect, what string, at api.Remote, create *api.Command, c api.Command, narrow *api.Command) (engine.Observation, bool) {
	ctx := h.ctx
	if what == "message" {
		var done func()
		ctx, done = h.reachFor(e.Thread)
		defer done()
	}
	if h.dial == nil {
		<-ctx.Done()
		return nil, h.ctx.Err() == nil
	}
	var refused string
	var last error
	lost, label, ok := h.persist(ctx, func(ctx context.Context) (string, bool, error) {
		m, label, err := h.reach(ctx, at)
		if err != nil {
			last = err
			return label, false, err
		}
		defer m.Close()
		send := func(c api.Command) error {
			ctx, cancel := context.WithTimeout(ctx, callTimeout)
			defer cancel()
			_, err := m.Submit(ctx, c)
			return err
		}
		var rej *api.RejectedError
		if create != nil {
			// The machine makes the child settled, or settles the one the
			// first create made, in the commit it takes either in. It
			// refuses this create only once the link or the setting is
			// gone, which loses the child.
			if err = send(*create); err != nil {
				last = err
				return label, false, err
			}
		}
		if err = send(c); errors.As(err, &rej) {
			refused = rej.Reason
			return label, false, nil
		}
		if err == nil && narrow != nil {
			if err = send(*narrow); errors.As(err, &rej) {
				refused = rej.Reason
				return label, false, nil
			}
		}
		last = err
		return label, false, err
	})
	switch {
	case !ok:
		return nil, h.ctx.Err() == nil
	case refused != "":
		slog.Warn("runtime: a remote child's machine refused this machine's "+what, "machine", label, "thread", e.Thread, "reason", refused)
		return nil, true
	case lost != "":
		if !errors.Is(last, api.ErrNotFound) {
			lost = fmt.Sprintf("%s; this machine's %s didn't reach the child there", lost, what)
		}
		slog.Warn("runtime: a remote child's machine can't be reached for this machine's "+what, "machine", label, "thread", e.Thread, "reason", lost)
		return engine.RemoteLost{Reason: lost, On: label}, true
	}
	return nil, true
}

// followRemote follows placeholder's child on its machine while the
// placeholder is bound, from the cursor it stores, and loses it when
// persist says. A placeholder lost, deleted or gone ends it.
func (h *Host) followRemote(ctx context.Context, placeholder api.ID) {
	defer h.wg.Done()
	defer func() {
		h.mu.Lock()
		delete(h.followers, placeholder)
		h.mu.Unlock()
	}()
	lost, label, ok := h.persist(ctx, func(ctx context.Context) (string, bool, error) {
		t, err := h.store.Thread(ctx, placeholder)
		if err != nil {
			return "", false, err
		}
		if !t.Exists() || t.Deleted || t.Remote == nil || t.Remote.State != engine.RemoteBoundState {
			return "", false, nil
		}
		m, label, err := h.reach(ctx, api.Remote{Engine: t.Remote.Engine, Link: t.Remote.Link})
		if err != nil {
			return label, false, err
		}
		defer m.Close()
		h.hear(placeholder)
		progressed, err := h.readRemote(ctx, m, t, label)
		return label, progressed, err
	})
	if !ok || lost == "" {
		return
	}
	retry(ctx, "record a remote child lost", func(ctx context.Context) error {
		_, err := h.store.Observe(ctx, placeholder, engine.RemoteLost{Reason: lost, On: label}, h.clock(), 0)
		if errors.Is(err, engine.ErrRefused) {
			return nil
		}
		return err
	})
}

// readRemote reads t's child on m, labelled label, from t's cursor until
// the stream fails. It folds what it read up to each gap in the stream,
// a snapshot whole, asks m what each done run said, and commits it, with
// the reports it holds, whenever it moved the cursor past a snapshot, or
// changed the child's run or brought a report. progressed is that it
// committed. A machine removed from the list ends it within a second,
// whatever the child does.
func (h *Host) readRemote(ctx context.Context, m Machine, t engine.Thread, label string) (progressed bool, err error) {
	r := *t.Remote
	at := api.Remote{Engine: r.Engine, Link: r.Link}
	ctx, removed := context.WithCancelCause(ctx)
	defer removed(nil)
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if _, listed, err := h.dial(at); errors.Is(err, ErrNoMachine) {
				removed(err)
				return
			} else if err == nil {
				listed.Close()
			}
		}
	}()
	// A child followed before this engine kept its runs as admitted there
	// and how they ended is read from the start, its reports kept by key,
	// until a run it reads has ended.
	after := r.Cursor
	if (r.Turn == nil || r.Ended == nil) && !r.Run.IsZero() {
		after = api.Cursor{}
	}
	st, err := m.Open(ctx, api.Subscription{Thread: r.Thread, After: after})
	if err != nil {
		return false, err
	}
	defer st.Close()
	now, cancel := context.WithCancel(ctx)
	cancel()
	for {
		it, err := st.Next(ctx)
		if cause := context.Cause(ctx); cause != nil && ctx.Err() != nil && errors.Is(cause, ErrNoMachine) {
			return progressed, cause
		}
		if err != nil {
			return progressed, err
		}
		h.hear(t.ID)
		items := []api.StreamItem{it}
		var later error
		for {
			it, err := st.Next(now)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					later = err
				}
				break
			}
			items = append(items, it)
		}
		next, snapshot := r, false
		var reports []engine.RemoteReport
		for _, it := range items {
			var events []api.Event
			fresh := false
			switch {
			case it.Kind == api.StreamSnapshot && it.Snapshot != nil:
				events, fresh, snapshot = it.Snapshot.Events, true, true
				next.Cursor = it.Snapshot.Cursor
			case it.Kind == api.StreamEvent && it.Event != nil:
				events = []api.Event{*it.Event}
				next.Cursor = it.Event.Cursor
			default:
				continue
			}
			var got []engine.RemoteReport
			var gone bool
			if next, got, gone, err = engine.Follow(next, events, fresh); err != nil {
				return progressed, fmt.Errorf("%w: %w", errDefect, err)
			}
			for _, p := range got {
				if !slices.ContainsFunc(reports, func(q engine.RemoteReport) bool { return q.Key == p.Key }) {
					reports = append(reports, p)
				}
			}
			if gone {
				later = api.ErrNotFound
			}
		}
		for i, p := range reports {
			if p.Kind != api.ReportDone {
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, callTimeout)
			reply, err := m.ChildReply(rctx, api.ChildReplyQuery{Thread: r.Thread, Run: p.Run})
			cancel()
			if errors.Is(err, api.ErrNotFound) {
				return progressed, fmt.Errorf("the reply of run %s, which its machine no longer has: %v", p.Run, err)
			}
			if err != nil {
				return progressed, err
			}
			reports[i].Reply = reply.Text
		}
		if snapshot || next.Run != r.Run || next.Silent != r.Silent || next.Slot != r.Slot || !slices.Equal(next.Pending, r.Pending) || next.Taken != r.Taken || next.Ended != r.Ended || next.Model != r.Model || next.Access != r.Access || next.Turn != r.Turn || !next.Hold.Equal(r.Hold) || len(reports) > 0 {
			_, listed, err := h.dial(at)
			if err != nil {
				return progressed, err
			}
			listed.Close()
			o := engine.RemoteSeen{Cursor: next.Cursor, Run: next.Run, Silent: next.Silent, Slot: next.Slot, Pending: next.Pending, Taken: next.Taken, Ended: next.Ended, Model: next.Model, Access: next.Access, Turn: next.Turn, Hold: next.Hold, Reports: reports, On: label}
			_, err = h.store.Observe(ctx, t.ID, o, h.clock(), 0)
			if errors.Is(err, engine.ErrRefused) {
				return progressed, errObsolete
			}
			if err != nil {
				return progressed, err
			}
			progressed = true
			for _, p := range reports {
				next.Reported = append(slices.Clip(next.Reported), p.Key)
			}
			r = next
		}
		if later != nil {
			return progressed, later
		}
	}
}

// createRemote commits b, a remote child's create, with the repository
// its machine starts it from, its parent's. What goes to another machine
// holds no value of the parent's vault, which never leaves this one,
// however short. A create already committed gets its receipt.
func (h *Host) createRemote(ctx context.Context, c api.Command, b api.ThreadCreate) (api.Receipt, error) {
	if done, err := h.store.Committed(ctx, c.ID); err != nil || done {
		if err != nil {
			return api.Receipt{}, err
		}
		return h.store.Submit(ctx, c, h.clock())
	}
	parent, err := h.store.Thread(ctx, b.Parent)
	if err != nil {
		return api.Receipt{}, err
	}
	for _, v := range h.credentials(b.Parent) {
		if v != "" && (strings.Contains(b.Text, v) || strings.Contains(b.Title, v) || strings.Contains(parent.Title, v)) {
			return api.Receipt{}, &api.RejectedError{Reason: "the task holds a credential value; name the variable instead"}
		}
	}
	in := b
	in.Repository = repositoryOf(ctx, parent.Workspace)
	// A worktree made for a local create of the same thread commits, or
	// is given back, before the placeholder can.
	h.making.Lock()
	defer h.making.Unlock()
	return h.store.SubmitResolved(ctx, c, in, h.clock())
}

// leavesNoValue refuses text, a message to thread, when thread is a
// remote child's placeholder and text holds a value of its parent's
// vault, which never leaves this machine.
func (h *Host) leavesNoValue(ctx context.Context, thread api.ID, text string) error {
	t, err := h.store.Thread(ctx, thread)
	if err != nil || t.Remote == nil {
		return err
	}
	for _, v := range h.credentials(t.Parent) {
		if v != "" && strings.Contains(text, v) {
			return &api.RejectedError{Reason: "the message holds a credential value; name the variable instead"}
		}
	}
	return nil
}

// repositoryOf is the repository a remote child of a thread in dir starts
// from on its machine: its root commit, and the branch dir is on; nil
// when dir is on no branch of a repository with a commit, which the
// create's decision refuses.
func repositoryOf(ctx context.Context, dir string) *api.Repository {
	if dir == "" {
		return nil
	}
	roots, err := git(ctx, dir, "rev-list", "--max-parents=0", "HEAD")
	if err != nil || len(strings.Fields(roots)) == 0 {
		return nil
	}
	branch, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch == "" {
		return nil
	}
	return &api.Repository{Root: strings.Fields(roots)[0], Branch: branch}
}
