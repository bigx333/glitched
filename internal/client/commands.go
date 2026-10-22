package client

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"glitched/internal/api"
)

// Pending is a command sent from here that hasn't landed yet. A message
// stays pending until its thread's open transcript shows it, so the row
// never blinks out before the entry replaces it. Err is why the submit
// failed. A failed row stays until Retry sends the command again under the
// same ID, or Dismiss drops it.
type Pending struct {
	Command api.ID
	Body    api.Intent
	Err     error
	sent    bool // committed; waiting for the transcript to show it
}

// Terminal is true when sending the command again can only fail the same
// way: the engine refused it, or the request or thread it acts on is gone.
// Offer Dismiss for it, not Retry.
func (p Pending) Terminal() bool {
	if rejected := (*api.RejectedError)(nil); errors.As(p.Err, &rejected) {
		return true
	}
	switch p.Body.(type) {
	case api.RequestRespond, api.TurnInterrupt:
		return errors.Is(p.Err, api.ErrNotFound)
	}
	return false
}

var errNoEngine = errors.New("client: not connected to the engine")

// Create adds a thread, shown at once with the given fields until the
// engine's view of it arrives, and returns its ID.
func (m *Manager) Create(e *Env, workspace, title string, model api.ModelRef, access api.Access) api.ID {
	id := api.NewID()
	e.Threads[id] = &Thread{View: api.Thread{ID: id, Workspace: workspace, Title: title, Model: model, Access: access, UpdatedAt: time.Now()}}
	m.submit(e, id, api.ThreadCreate{Workspace: workspace, Title: title, Model: model, Access: access})
	return id
}

// Send dispatches a message and returns its ID, which the engine also
// gives the message and the run it starts.
func (m *Manager) Send(e *Env, thread api.ID, text string, mode api.DispatchMode) api.ID {
	return m.submit(e, thread, api.MessageDispatch{Text: text, Mode: mode})
}

func (m *Manager) Interrupt(e *Env, thread api.ID) api.ID {
	return m.submit(e, thread, api.TurnInterrupt{})
}

// Respond answers a request with {"allow":allow}, as the CLI does.
func (m *Manager) Respond(e *Env, thread, request api.ID, allow bool) api.ID {
	th := e.Threads[thread]
	if th == nil {
		return api.ID{}
	}
	var gen uint64
	if i := slices.IndexFunc(th.View.Requests, func(r api.Request) bool { return r.ID == request }); i >= 0 {
		gen = th.View.Requests[i].Generation
	} else if th.Transcript != nil {
		if r := th.Transcript.request(request); r != nil {
			gen = r.Generation
		}
	}
	answer, _ := json.Marshal(map[string]bool{"allow": allow})
	return m.submit(e, thread, api.RequestRespond{Request: request, Generation: gen, Answer: answer})
}

func (m *Manager) SetDisposition(e *Env, thread api.ID, d api.Disposition) api.ID {
	return m.submit(e, thread, api.InboxSet{Disposition: d})
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
	}
}

func (m *Manager) submit(e *Env, thread api.ID, body api.Intent) api.ID {
	th := e.Threads[thread]
	if th == nil {
		return api.ID{}
	}
	cmd := api.Command{ID: api.NewID(), Target: thread, Body: body}
	th.Pending = append(th.Pending, Pending{Command: cmd.ID, Body: body})
	m.send(e, th, cmd)
	return cmd.ID
}

// send submits cmd once the thread's previous submit has returned, so a
// message never overtakes the create before it.
func (m *Manager) send(e *Env, th *Thread, cmd api.Command) {
	if e.session == nil {
		m.submitted(e, cmd, errNoEngine)
		return
	}
	ep := e.session.ep
	prev, done := th.last, make(chan struct{})
	th.last = done
	ctx := e.ctx
	m.wg.Go(func() {
		defer close(done)
		if prev != nil {
			select {
			case <-prev:
			case <-ctx.Done():
				return
			}
		}
		sctx, cancel := context.WithTimeout(ctx, submitTimeout)
		_, err := ep.Submit(sctx, cmd)
		cancel()
		m.post(ctx, func() { m.submitted(e, cmd, err) })
	})
}

func (m *Manager) submitted(e *Env, cmd api.Command, err error) {
	th := e.Threads[cmd.Target]
	if th == nil {
		return
	}
	i := slices.IndexFunc(th.Pending, func(p Pending) bool { return p.Command == cmd.ID })
	if i < 0 {
		return
	}
	_, message := cmd.Body.(api.MessageDispatch)
	switch {
	case err == nil && message && th.open && th.Transcript.message(cmd.ID) == nil:
		th.Pending[i].sent = true
	case err == nil, errors.Is(err, api.ErrAlreadyAnswered):
		th.Pending = slices.Delete(th.Pending, i, i+1)
	default:
		th.Pending[i].Err = err
	}
}
