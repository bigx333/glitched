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
// never blinks out before the entry replaces it, and an answer the engine
// took until its request stops waiting, so the request never shows
// waiting again in between. Err is why the submit
// failed. A failed row stays until Retry sends the command again under the
// same ID, or Dismiss drops it, or, for an answer, its request stops
// waiting.
type Pending struct {
	Command api.ID
	Body    api.Intent
	Err     error
	sent    bool // committed; waiting for the thread to show it
	seen    bool // a configure the thread's view showed before its receipt came
}

// Terminal is true when sending the command again can only fail the same
// way, as the request or thread it acts on is gone. A rejection is not: the
// engine may take the same command once the thread's state moves on, as a
// StartNow does once the live run ends.
func (p Pending) Terminal() bool {
	switch p.Body.(type) {
	case api.RequestRespond, api.TurnInterrupt:
		return errors.Is(p.Err, api.ErrNotFound)
	}
	return false
}

var errNoEngine = errors.New("client: not connected to the engine")

// Create adds a thread, shown at once with the given fields until the
// engine's view of it arrives, and returns its ID. It returns the zero ID
// on a duplicate, which shows no threads.
func (m *Manager) Create(e *Env, workspace, title string, model api.ModelRef, access api.Access) api.ID {
	if m.ShownBy(e) != e {
		return api.ID{}
	}
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

// Answer is how the user answers an approval.
type Answer uint8

const (
	Deny Answer = iota
	AllowOnce
)

// Respond answers an approval with {"allow":true} or {"allow":false}, as
// the CLI does.
func (m *Manager) Respond(e *Env, thread, request api.ID, a Answer) api.ID {
	answer, _ := json.Marshal(map[string]bool{"allow": a == AllowOnce})
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
	var gen uint64
	if i := slices.IndexFunc(th.View.Requests, func(r api.Request) bool { return r.ID == request }); i >= 0 {
		gen = th.View.Requests[i].Generation
	} else if th.Transcript != nil {
		if r := th.Transcript.request(request); r != nil {
			gen = r.Generation
		}
	}
	return m.submit(e, thread, api.RequestRespond{Request: request, Generation: gen, Answer: answer})
}

func (m *Manager) SetDisposition(e *Env, thread api.ID, d api.Disposition) api.ID {
	return m.submit(e, thread, api.InboxSet{Disposition: d})
}

func (m *Manager) Rename(e *Env, thread api.ID, title string) api.ID {
	return m.submit(e, thread, api.ThreadRename{Title: title})
}

// ConfigureThread sets the model and access the thread's next turn runs at.
// A configure the same as the last, failed, goes no more, as a held
// slider would send it each frame: its row offers to retry it. Any other
// drops the failed ones before it, whose retry would undo it.
func (m *Manager) ConfigureThread(e *Env, thread api.ID, model api.ModelRef, access api.Access) api.ID {
	c := api.ThreadConfigure{Model: model, Access: access}
	if th := e.Threads[thread]; th != nil {
		for i := len(th.Pending) - 1; i >= 0; i-- {
			p := th.Pending[i]
			if last, ok := p.Body.(api.ThreadConfigure); ok {
				if p.Err != nil && last == c {
					return p.Command
				}
				break
			}
		}
		th.Pending = slices.DeleteFunc(th.Pending, func(p Pending) bool {
			_, ok := p.Body.(api.ThreadConfigure)
			return ok && p.Err != nil
		})
	}
	return m.submit(e, thread, c)
}

// Delete deletes the thread. The engine ends its run with it.
func (m *Manager) Delete(e *Env, thread api.ID) api.ID {
	return m.submit(e, thread, api.ThreadDelete{})
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
	if th == nil || m.ShownBy(e) != e {
		return api.ID{}
	}
	cmd := api.Command{ID: api.NewID(), Target: thread, Body: body}
	th.Pending = append(th.Pending, Pending{Command: cmd.ID, Body: body})
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
		m.submitted(e, cmd, errNoEngine)
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
		ctx, cancel := context.WithTimeout(ectx, submitTimeout)
		defer cancel()
		var err error
		for {
			if s.ctx.Err() != nil {
				if s = m.next(ctx, e); s == nil {
					err = errNoEngine
					break
				}
			}
			if _, err = s.ep.Submit(ctx, cmd); err == nil || s.ctx.Err() == nil {
				break
			}
		}
		m.post(ectx, func() { m.submitted(e, cmd, err) })
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
	r, answer := cmd.Body.(api.RequestRespond)
	c, configure := cmd.Body.(api.ThreadConfigure)
	taken := err == nil || errors.Is(err, api.ErrAlreadyAnswered)
	switch {
	case err == nil && message && th.open && th.Transcript.message(cmd.ID) == nil:
		th.Pending[i].sent = true
	case taken && answer && th.asks(r.Request):
		th.Pending[i].sent = true
	case err == nil && configure && !th.shows(c) && !th.Pending[i].seen:
		// The receipt can come before the view that shows it: until then
		// the thread's model and access are the configure's, and the next
		// configure builds on them.
		th.Pending[i].sent = true
	case taken:
		th.Pending = slices.Delete(th.Pending, i, i+1)
	default:
		th.Pending[i].Err = err
		th.dropUnasked()
	}
}
