package client

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"glitched/internal/api"
	"glitched/internal/transport"
)

// Pending is a command sent from here that hasn't landed yet. A message
// stays pending until its thread's open transcript shows it, so the row
// never blinks out before the entry replaces it, and an answer the engine
// took until its request stops waiting, so the request never shows
// waiting again in between. A configure, a rename or a move between the
// inbox's sections the engine took stays until the thread's view is at
// the revision its receipt gives, so the thread shows what was chosen from
// when it's chosen, not what it was for as long as its receipt comes
// before that view. Err is
// why the submit failed. A failed row stays until Retry sends the command
// again under the same ID, or Dismiss drops it, or, for an answer, its
// request stops waiting, or, for a configure, a rename or a move, another
// is chosen of something it gave: one that fails with such a one chosen
// after it leaves no row.
type Pending struct {
	Command api.ID
	Body    api.Intent
	Err     error
	sent    bool   // committed; waiting for the thread to show it
	log     api.ID // of a committed change the view shows: the log that took it,
	at      uint64 // and the thread's revision there that has it
}

// Terminal is true when sending the command again can only fail the same
// way, as the request or thread it acts on is gone. A rejection is not: the
// engine may take the same command once the thread's state moves on, as a
// StartNow does once the live run ends.
func (p Pending) Terminal() bool {
	switch p.Body.(type) {
	case api.RequestRespond, api.TurnInterrupt:
		return errors.Is(p.Err, api.ErrNotFound)
	case api.QueueEdit, api.QueueCancel, api.QueueMove:
		// A message that no longer waits its turn never waits again.
		return errors.Is(p.Err, api.ErrNotFound) || errors.As(p.Err, new(*api.RejectedError))
	}
	return false
}

var errNoEngine = errors.New("client: not connected to the engine")

// Create adds a thread, shown at once with the given fields until the
// engine's view of it arrives, and returns its ID. It returns the zero ID
// on a duplicate, which shows no threads. With a worktree, the thread
// shows under workspace's project until the engine has made it.
func (m *Manager) Create(e *Env, workspace, title string, model api.ModelRef, access api.Access, worktree *api.NewWorktree) api.ID {
	if m.ShownBy(e) != e {
		return api.ID{}
	}
	id := api.NewID()
	e.Threads[id] = &Thread{View: api.Thread{ID: id, Workspace: workspace, Title: title, Model: model, Access: access, UpdatedAt: time.Now()}}
	e.Membership++
	m.submit(e, id, api.ThreadCreate{Workspace: workspace, Title: title, Model: model, Access: access, Worktree: worktree})
	return id
}

// Send dispatches a message and returns its ID, which the engine also
// gives the message and the run it starts.
func (m *Manager) Send(e *Env, thread api.ID, text string, mode api.DispatchMode) api.ID {
	return m.submit(e, thread, api.MessageDispatch{Text: text, Mode: mode})
}

// Replace sends text in place of the thread's uncertain run as its view
// shows it, RestartActive: the engine refuses it once that run is no
// longer the live one, as when another client replaced it first, rather
// than stop the newer run.
func (m *Manager) Replace(e *Env, thread api.ID, text string) api.ID {
	th := e.Threads[thread]
	if th == nil || th.View.Turn == nil {
		return api.ID{}
	}
	return m.submit(e, thread, api.MessageDispatch{Text: text, Mode: api.RestartActive, Replaces: th.View.Turn.Run})
}

func (m *Manager) Interrupt(e *Env, thread api.ID) api.ID {
	return m.submit(e, thread, api.TurnInterrupt{})
}

// Resume starts what a stop held.
func (m *Manager) Resume(e *Env, thread api.ID) api.ID {
	return m.submit(e, thread, api.QueueResume{})
}

// EditQueued gives a message that waits its turn new words.
func (m *Manager) EditQueued(e *Env, thread, message api.ID, text string) api.ID {
	return m.submit(e, thread, api.QueueEdit{Message: message, Text: text})
}

// CancelQueued takes a message out of the queue.
func (m *Manager) CancelQueued(e *Env, thread, message api.ID) api.ID {
	return m.submit(e, thread, api.QueueCancel{Message: message})
}

// MoveQueued puts a queued message just before another, or last with
// before zero.
func (m *Manager) MoveQueued(e *Env, thread, message, before api.ID) api.ID {
	return m.submit(e, thread, api.QueueMove{Message: message, Before: before})
}

// SteerQueued sends a queued message now: into the agent's turn, or as the
// next turn of an idle thread.
func (m *Manager) SteerQueued(e *Env, thread, message api.ID) api.ID {
	return m.submit(e, thread, api.QueueSteer{Message: message})
}

// Answer is how the user answers an approval.
type Answer uint8

const (
	Deny Answer = iota
	AllowOnce
	// AllowAlways allows the call, and has the agent allow such calls
	// from now on by its own rules: only where the request offers it.
	AllowAlways
)

// Respond answers an approval with {"allow":true} or {"allow":false}, as
// the CLI does.
func (m *Manager) Respond(e *Env, thread, request api.ID, a Answer) api.ID {
	answer, _ := json.Marshal(map[string]bool{"allow": a == AllowOnce})
	if a == AllowAlways {
		answer, _ = json.Marshal(map[string]bool{"allow": true, "always": true})
	}
	return m.respond(e, thread, request, answer)
}

// AnswerQuestions answers a question request; no answers dismisses it.
func (m *Manager) AnswerQuestions(e *Env, thread, request api.ID, a api.Answers) api.ID {
	answer, _ := json.Marshal(a)
	return m.respond(e, thread, request, answer)
}

func (m *Manager) respond(e *Env, thread, request api.ID, answer json.RawMessage) api.ID {
	th := e.Threads[thread]
	if th == nil {
		return api.ID{}
	}
	return m.submit(e, thread, api.RequestRespond{Request: request, Generation: th.generation(request), Answer: answer})
}

// generation is the run generation request belongs to, as the thread shows
// it.
func (th *Thread) generation(request api.ID) uint64 {
	if i := slices.IndexFunc(th.View.Requests, func(r api.Request) bool { return r.ID == request }); i >= 0 {
		return th.View.Requests[i].Generation
	}
	if th.Transcript != nil {
		if r := th.Transcript.request(request); r != nil {
			return r.Generation
		}
	}
	return 0
}

// credentialer is an engine that keeps the credentials users give agents.
type credentialer interface {
	ProvideCredentials(ctx context.Context, p api.Provide) error
	ForgetCredentials(ctx context.Context, thread api.ID) error
}

var errNoVault = errors.New("client: this engine keeps no credentials")

// ProvideCredentials hands the values the user typed for a credentials
// request to its thread's engine, which keeps them in the thread's vault
// and answers the request with their names. Unlike a command, nothing of
// it is kept here to retry: done gets how it went, on the UI thread.
func (m *Manager) ProvideCredentials(e *Env, thread, request api.ID, values map[string]string, done func(error)) {
	th := e.Threads[thread]
	if th == nil {
		done(api.ErrNotFound)
		return
	}
	p := api.Provide{Thread: thread, Request: request, Generation: th.generation(request), Values: values}
	m.credentials(e, done, func(ctx context.Context, c credentialer) error { return c.ProvideCredentials(ctx, p) })
}

// DeclineCredentials answers a credentials request with no values.
func (m *Manager) DeclineCredentials(e *Env, thread, request api.ID) api.ID {
	return m.respond(e, thread, request, json.RawMessage(`{}`))
}

// ForgetCredentials deletes the values thread's vault holds.
func (m *Manager) ForgetCredentials(e *Env, thread api.ID, done func(error)) {
	m.credentials(e, done, func(ctx context.Context, c credentialer) error { return c.ForgetCredentials(ctx, thread) })
}

func (m *Manager) credentials(e *Env, done func(error), call func(context.Context, credentialer) error) {
	s := e.session
	if s == nil {
		done(errNoEngine)
		return
	}
	c, ok := s.ep.(credentialer)
	if !ok {
		done(errNoVault)
		return
	}
	ectx := e.ctx
	m.wg.Go(func() {
		ctx, cancel := context.WithTimeout(ectx, submitTimeout)
		defer cancel()
		err := call(ctx, c)
		m.post(ectx, func() { done(err) })
	})
}

// cliEngine is an engine that says what its providers are and updates
// their CLIs.
type cliEngine interface {
	Providers(ctx context.Context, fresh, check bool) ([]transport.ProviderInfo, error)
	UpdateProvider(ctx context.Context, name string, whenIdle bool) (transport.UpdateResult, error)
}

var errNoCLIs = errors.New("client: this engine can't say what its providers are")

const (
	providersTimeout = time.Minute
	// An update when idle waits for the provider's turn to end first.
	updateTimeout = 30 * time.Minute
)

// Providers asks e's engine for every provider it serves, each asked its
// program again when fresh, and, when check, whether its CLI has a newer
// version. done gets the answer on the UI thread.
func (m *Manager) Providers(e *Env, fresh, check bool, done func([]transport.ProviderInfo, error)) {
	cliCall(m, e, providersTimeout, done, func(ctx context.Context, c cliEngine) ([]transport.ProviderInfo, error) {
		return c.Providers(ctx, fresh, check)
	})
}

// UpdateProvider has e's engine update the CLI of its provider name,
// refused while a turn of it runs there unless whenIdle has the engine
// wait for its end. done gets how it went on the UI thread.
func (m *Manager) UpdateProvider(e *Env, name string, whenIdle bool, done func(transport.UpdateResult, error)) (cancel func()) {
	return cliCall(m, e, updateTimeout, done, func(ctx context.Context, c cliEngine) (transport.UpdateResult, error) {
		return c.UpdateProvider(ctx, name, whenIdle)
	})
}

// cliCall calls the env's engine off the Post thread and hands done what
// it said. cancel gives up the call, as the env's removal does: done then
// gets the context's error.
func cliCall[T any](m *Manager, e *Env, timeout time.Duration, done func(T, error), call func(context.Context, cliEngine) (T, error)) (cancel func()) {
	var zero T
	s := e.session
	if s == nil {
		done(zero, errNoEngine)
		return func() {}
	}
	c, ok := s.ep.(cliEngine)
	if !ok {
		done(zero, errNoCLIs)
		return func() {}
	}
	ectx := e.ctx
	ctx, stop := context.WithTimeout(ectx, timeout)
	m.wg.Go(func() {
		defer stop()
		out, err := call(ctx, c)
		m.post(ectx, func() { done(out, err) })
	})
	return stop
}

func (m *Manager) SetDisposition(e *Env, thread api.ID, d api.Disposition) api.ID {
	return m.submit(e, thread, api.InboxSet{Disposition: d})
}

func (m *Manager) Rename(e *Env, thread api.ID, title string) api.ID {
	return m.submit(e, thread, api.ThreadRename{Title: title})
}

// ConfigureThread changes the model and access the thread's next turn runs
// at: what c gives of them, which is what was chosen, the rest staying as
// the engine has it.
func (m *Manager) ConfigureThread(e *Env, thread api.ID, c api.ThreadConfigure) api.ID {
	return m.submit(e, thread, c)
}

// inView is whether what body does shows in its thread's view.
func inView(body api.Intent) bool {
	switch body.(type) {
	case api.ThreadConfigure, api.InboxSet, api.ThreadRename:
		return true
	}
	return false
}

// replaces is whether p chooses anything body chooses: of the two, the
// one sent last undoes the other's choice of it. A configure chooses what
// it gives; a move, the thread's section; a rename, its title.
func replaces(p Pending, body api.Intent) bool {
	switch c := body.(type) {
	case api.ThreadConfigure:
		b, ok := p.Body.(api.ThreadConfigure)
		return ok && (b.Model != nil && c.Model != nil || b.Effort != nil && c.Effort != nil ||
			b.Fast != nil && c.Fast != nil || b.Access != nil && c.Access != nil)
	case api.InboxSet:
		_, ok := p.Body.(api.InboxSet)
		return ok
	case api.ThreadRename:
		_, ok := p.Body.(api.ThreadRename)
		return ok
	}
	return false
}

// Delete deletes the thread. The engine ends its run with it.
func (m *Manager) Delete(e *Env, thread api.ID, removeWorktree bool) api.ID {
	return m.submit(e, thread, api.ThreadDelete{RemoveWorktree: removeWorktree})
}

// Retry sends a failed command again under its ID, so one that did land
// before the failure is not applied twice.
func (m *Manager) Retry(e *Env, thread, command api.ID) {
	th := e.Threads[thread]
	if th == nil {
		return
	}
	i := slices.IndexFunc(th.Pending, func(p Pending) bool { return p.Command == command && p.Err != nil })
	if i < 0 {
		return
	}
	th.Pending[i].Err = nil
	m.edits++
	m.send(e, th, api.Command{ID: command, Target: thread, Body: th.Pending[i].Body})
}

// Dismiss drops a failed command's row. Dismissing a failed create also
// drops its thread, unless the engine has shown it after all.
func (m *Manager) Dismiss(e *Env, thread, command api.ID) {
	th := e.Threads[thread]
	if th == nil {
		return
	}
	i := slices.IndexFunc(th.Pending, func(p Pending) bool { return p.Command == command && p.Err != nil })
	if i < 0 {
		return
	}
	_, create := th.Pending[i].Body.(api.ThreadCreate)
	th.Pending = slices.Delete(th.Pending, i, i+1)
	if create && !th.known {
		delete(e.Threads, thread)
		e.Membership++
	}
}

func (m *Manager) submit(e *Env, thread api.ID, body api.Intent) api.ID {
	th := e.Threads[thread]
	if th == nil || m.ShownBy(e) != e {
		return api.ID{}
	}
	cmd := api.Command{ID: api.NewID(), Target: thread, Body: body}
	// A failed one of what this chooses would undo it by its Retry.
	th.Pending = slices.DeleteFunc(th.Pending, func(p Pending) bool { return p.Err != nil && replaces(p, body) })
	th.Pending = append(th.Pending, Pending{Command: cmd.ID, Body: body})
	m.edits++
	m.send(e, th, cmd)
	return cmd.ID
}

// send submits cmd once the thread's previous submit has returned, so a
// message never overtakes the create before it. If e's session ends first,
// as a redial or a re-attach ends it, the command goes out on the next one
// under the same ID, which the engine applies once.
func (m *Manager) send(e *Env, th *Thread, cmd api.Command) {
	s := e.session
	if s == nil {
		m.submitted(e, cmd, api.Receipt{}, errNoEngine)
		return
	}
	prev, done := th.last, make(chan struct{})
	th.last = done
	ectx := e.ctx
	m.wg.Go(func() {
		defer close(done)
		if prev != nil {
			select {
			case <-prev:
			case <-ectx.Done():
				return
			}
		}
		select {
		case e.slots <- struct{}{}:
		case <-ectx.Done():
			return
		}
		defer func() { <-e.slots }()
		ctx, cancel := context.WithTimeout(ectx, submitTimeout)
		defer cancel()
		var r api.Receipt
		var err error
		for {
			if s.ctx.Err() != nil {
				if s = m.next(ctx, e); s == nil {
					err = errNoEngine
					break
				}
			}
			if r, err = s.ep.Submit(ctx, cmd); err == nil || s.ctx.Err() == nil {
				break
			}
		}
		m.post(ectx, func() { m.submitted(e, cmd, r, err) })
	})
}

// next returns e's live session once there is one, or nil if ctx ends
// first.
func (m *Manager) next(ctx context.Context, e *Env) *session {
	got := make(chan *session, 1)
	m.post(ctx, func() {
		if e.session != nil && e.session.ctx.Err() == nil {
			got <- e.session
		} else {
			e.waiting = append(e.waiting, got)
		}
	})
	select {
	case s := <-got:
		return s
	case <-ctx.Done():
		return nil
	}
}

// submitted takes the answer to cmd: its receipt, or why it failed.
func (m *Manager) submitted(e *Env, cmd api.Command, receipt api.Receipt, err error) {
	th := e.Threads[cmd.Target]
	if th == nil {
		return
	}
	i := slices.IndexFunc(th.Pending, func(p Pending) bool { return p.Command == cmd.ID })
	if i < 0 {
		return
	}
	_, message := cmd.Body.(api.MessageDispatch)
	r, answer := cmd.Body.(api.RequestRespond)
	taken := err == nil || errors.Is(err, api.ErrAlreadyAnswered)
	switch {
	case err == nil && message && th.open && th.Transcript.message(cmd.ID) == nil:
		th.Pending[i].sent = true
	case taken && answer && th.asks(r.Request):
		th.Pending[i].sent = true
	case err == nil && inView(cmd.Body) && receipt.Committed.Log == e.log && receipt.Revision > th.View.Revision:
		// The receipt came before the view that has the change: until
		// that view, the thread shows what the change gave, as the
		// engine took it, unless one chosen after gives the same. A
		// receipt of another log than the views held, or one stored
		// without its revision, names no view to wait for.
		th.Pending[i].sent, th.Pending[i].log, th.Pending[i].at = true, e.log, receipt.Revision
	case taken, slices.ContainsFunc(th.Pending[i+1:], func(p Pending) bool { return replaces(p, cmd.Body) }):
		// A change that fails with another chosen after it, of something
		// it gave, leaves no row: its Retry would undo that one. With none
		// such, its row stays: what it chose went with no other.
		th.Pending = slices.Delete(th.Pending, i, i+1)
	default:
		th.Pending[i].Err = err
		th.dropUnasked()
	}
}
