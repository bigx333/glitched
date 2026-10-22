package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"glitched/internal/api"
)

// Decide checks a client command against the thread and returns what to
// commit. A command the tables refuse comes back as *api.RejectedError.
func Decide(t Thread, c api.Command, now time.Time) ([]Event, []Effect, error) {
	now = now.UTC().Round(0)
	if c.ID.IsZero() || c.Target.IsZero() {
		return nil, nil, rejected("a command needs an id and a target")
	}
	if !c.Expires.IsZero() && now.After(c.Expires) {
		return nil, nil, api.ErrExpired
	}
	if _, creating := c.Body.(api.ThreadCreate); !creating && !t.Exists() {
		return nil, nil, api.ErrNotFound
	}
	if c.Revision != 0 && c.Revision != t.Revision {
		return nil, nil, api.ErrRevision
	}
	d := &decision{t: t, thread: c.Target, at: now}
	var err error
	switch b := c.Body.(type) {
	case api.ThreadCreate:
		err = d.create(b)
	case api.MessageDispatch:
		err = d.dispatch(c.ID, b)
	case api.TurnInterrupt:
		if r := d.t.Live(); r != nil {
			err = d.stop(*r)
		}
	case api.RequestRespond:
		err = d.respond(b)
	case api.InboxSet:
		err = d.inbox(b)
	default:
		err = rejected("unknown intent")
	}
	if err == nil {
		err = d.settle()
	}
	if te := (*TransitionError)(nil); errors.As(err, &te) {
		err = rejected(te.Error())
	}
	if err != nil {
		return nil, nil, err
	}
	return d.events, d.effects, nil
}

// Observe records what an agent or the adapter did. An observation the
// tables refuse arrived after the thread moved on; it is logged and dropped.
func Observe(t Thread, o Observation, now time.Time) ([]Event, []Effect, error) {
	if !t.Exists() {
		return nil, nil, api.ErrNotFound
	}
	d := &decision{t: t, thread: t.ID, at: now.UTC().Round(0)}
	err := d.observe(o)
	if err == nil {
		err = d.settle()
	}
	if te := (*TransitionError)(nil); errors.As(err, &te) {
		slog.Warn("engine: observation refused", "thread", t.ID, "observation", fmt.Sprintf("%T", o), "err", err)
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return d.events, d.effects, nil
}

type decision struct {
	t       Thread
	thread  api.ID
	at      time.Time
	events  []Event
	effects []Effect
}

func (d *decision) emit(b api.EventBody) error {
	e := Event{Thread: d.thread, At: d.at, Body: b}
	t, err := apply(d.t, e)
	if err != nil {
		return err
	}
	d.t, d.events = t, append(d.events, e)
	return nil
}

func rejected(reason string) error { return &api.RejectedError{Reason: reason} }

func (d *decision) create(c api.ThreadCreate) error {
	if c.Access > api.AccessFull {
		return rejected("unknown access level")
	}
	return d.emit(api.ThreadCreated{Workspace: c.Workspace, Title: c.Title, Model: c.Model, Access: c.Access})
}

// ceiling is the most access the submitting client may use. There are no
// grants yet, so it is everything; internal/auth will supply the real one.
func ceiling() api.Access { return api.AccessFull }

func (d *decision) dispatch(id api.ID, m api.MessageDispatch) error {
	if m.Mode > api.Defer {
		return rejected("unknown dispatch mode")
	}
	if d.t.Access > ceiling() {
		return rejected("thread access is above the client's ceiling")
	}
	q := api.MessageQueued{Message: id, Text: m.Text, Mode: m.Mode, Pos: d.t.pos(false), Held: m.Mode == api.Defer}
	if m.Mode == api.RestartActive {
		q.Pos = d.t.pos(true)
	}
	if err := d.emit(q); err != nil {
		return err
	}
	live := d.t.Live()
	switch {
	case m.Mode == api.StartNow:
		return d.admit(id)
	case m.Mode == api.SteerActive && live != nil && steerable[live.State]:
		if err := d.emit(api.MessageClaimed{Message: id, Run: live.ID}); err != nil {
			return err
		}
		d.effects = append(d.effects, SteerTurn{Run: live.ID, Message: id, Text: m.Text})
	case m.Mode == api.RestartActive && live != nil:
		return d.stop(*live)
	}
	// AfterActive, and steering a thread with nothing to steer, wait for
	// settle; a Defer message is held until something promotes it.
	return nil
}

// pos is a queue position before or after every message on the thread.
func (t Thread) pos(front bool) int64 {
	if len(t.Queue) == 0 {
		return 0
	}
	lo, hi := t.Queue[0].Pos, t.Queue[0].Pos
	for _, m := range t.Queue {
		lo, hi = min(lo, m.Pos), max(hi, m.Pos)
	}
	if front {
		return lo - 1
	}
	return hi + 1
}

func (d *decision) admit(message api.ID) error {
	gen := d.t.Generation + 1
	if err := d.emit(api.TurnAdmitted{Run: message, Generation: gen}); err != nil {
		return err
	}
	m := d.t.Queue[d.t.messageAt(message)]
	d.effects = append(d.effects, StartTurn{
		Run: message, Generation: gen, Text: m.Text, Workspace: d.t.Workspace, Model: d.t.Model, Access: d.t.Access,
	})
	return nil
}

// settle starts the next ready message whenever the thread is idle, so
// admission happens in the same commit as whatever freed the thread.
func (d *decision) settle() error {
	if d.t.Live() != nil {
		return nil
	}
	if m := d.t.next(); m != nil {
		return d.admit(m.ID)
	}
	return nil
}

func (d *decision) stop(r Run) error {
	switch r.State {
	case RunStopping:
		// Asked again, the user gives up waiting on a run that ignores its
		// interrupt; nobody can say what that run is doing.
		return d.emit(api.TurnUncertain{Run: r.ID})
	case RunUncertain:
		// Ending an uncertain run is the user's explicit decision to move on.
		return d.emit(api.TurnEnded{Run: r.ID, Outcome: api.OutcomeAbandoned})
	}
	if err := d.emit(api.TurnStopping{Run: r.ID}); err != nil {
		return err
	}
	d.effects = append(d.effects, InterruptTurn{Run: r.ID})
	return nil
}

func (d *decision) respond(b api.RequestRespond) error {
	i := d.t.requestAt(b.Request)
	if i < 0 {
		return api.ErrNotFound
	}
	r := d.t.Requests[i]
	if r.Generation != b.Generation {
		return rejected("the request belongs to another run")
	}
	if !json.Valid(b.Answer) {
		return rejected("the answer is not JSON")
	}
	if err := d.emit(api.RequestAnswer{Request: r.ID, Answer: b.Answer}); err != nil {
		if r.State == api.RequestAnswered || r.State == api.RequestDelivered {
			return api.ErrAlreadyAnswered
		}
		return err
	}
	d.effects = append(d.effects, DeliverAnswer{Run: r.Run, Request: r.ID, Dedup: r.Dedup, Answer: b.Answer})
	return nil
}

func (d *decision) inbox(b api.InboxSet) error {
	v := b.Disposition
	v.Until = v.Until.UTC().Round(0)
	if v.State > api.Settled || (v.State == api.Snoozed) == v.Until.IsZero() {
		return rejected("only a snoozed thread has an until, and it must")
	}
	if v.State == d.t.Disposition.State && v.Until.Equal(d.t.Disposition.Until) {
		return nil
	}
	return d.emit(api.DispositionChanged{Disposition: v})
}

func (d *decision) observe(o Observation) error {
	live := d.t.Live()
	isLive := func(run api.ID) bool { return live != nil && live.ID == run }
	switch o := o.(type) {
	case Started:
		if !isLive(o.Run) {
			return nil
		}
		if live.State != RunStarting {
			// Interrupted while starting, but the agent did get the message.
			return d.emit(api.MessageDispatched{Message: o.Run})
		}
		return d.emit(api.TurnStarted{Run: o.Run})
	case Ended:
		// Only the user ends an uncertain run.
		if isLive(o.Run) && live.State != RunUncertain {
			return d.emit(api.TurnEnded{Run: o.Run, Outcome: o.Outcome, Error: o.Error})
		}
	case Delta:
		if isLive(o.Run) {
			return d.emit(api.AssistantDelta{Run: o.Run, Text: o.Text})
		}
	case Work:
		if isLive(o.Item.Run) {
			return d.emit(o.Item)
		}
	case Surfaced:
		dup := index(d.t.Requests, func(r Request) bool { return r.Run == o.Run && r.Dedup == o.Dedup }) >= 0
		if isLive(o.Run) && !dup {
			return d.emit(api.RequestOpened{
				Request: o.Request, Run: o.Run, Generation: live.Generation, Dedup: o.Dedup, Kind: o.Kind, Body: o.Body,
			})
		}
	case Delivered:
		return d.emit(api.RequestDelivery{Request: o.Request})
	case DeliveryFailed:
		if err := d.emit(api.RequestInvalidation{Request: o.Request, Reason: o.Error}); err != nil {
			return err
		}
		// An answer that can't be delivered is never assumed; the run that
		// waits for it is stopped instead.
		if r := d.t.Requests[d.t.requestAt(o.Request)]; isLive(r.Run) {
			return d.stop(*d.t.Live())
		}
	case Steered:
		return d.emit(api.MessageDispatched{Message: o.Message})
	case SteerFailed:
		return d.emit(api.MessageCancelled{Message: o.Message, Reason: o.Error})
	case SteerLost:
		return d.emit(api.MessageUncertain{Message: o.Message})
	case Reconciled:
		if !isLive(o.Run) {
			return nil
		}
		if o.Restored {
			return d.emit(api.TurnRestored{Run: o.Run})
		}
		return d.emit(api.TurnUncertain{Run: o.Run})
	}
	return nil
}
