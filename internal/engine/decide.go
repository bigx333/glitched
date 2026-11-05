package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	if t.Deleted {
		return nil, nil, api.ErrNotFound
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
		err = d.create(c.ID, b)
	case api.MessageDispatch:
		err = d.dispatch(c.ID, b)
	case api.TurnInterrupt:
		err = d.interrupt(b.Drop)
	case api.RequestRespond:
		err = d.respond(b)
	case api.InboxSet:
		err = d.inbox(b)
	case api.ThreadRename:
		err = d.rename(b)
	case api.ThreadDelete:
		err = d.delete()
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

// TakesTree is whether b on a thread applies to every thread under it
// too, in the same commit, as the store commits it: settling a thread
// settles its tree, and deleting it deletes its tree.
func TakesTree(b api.Intent) bool {
	switch b := b.(type) {
	case api.ThreadDelete:
		return true
	case api.InboxSet:
		return b.Disposition.State == api.Settled
	}
	return false
}

// ErrRefused is an observation the tables refuse: it arrived after the
// thread moved on, so it records nothing.
var ErrRefused = errors.New("engine: observation refused")

// Observe records what an agent or the adapter did. An observation the
// tables refuse returns ErrRefused.
func Observe(t Thread, o Observation, now time.Time) ([]Event, []Effect, error) {
	if !t.Exists() {
		return nil, nil, api.ErrNotFound
	}
	if t.Deleted {
		// What the agent did after, as an effect's leftover, records
		// nothing on a thread no one can read.
		return nil, nil, fmt.Errorf("%w: %T: the thread was deleted", ErrRefused, o)
	}
	d := &decision{t: t, thread: t.ID, at: now.UTC().Round(0)}
	err := d.observe(o)
	if err == nil {
		err = d.settle()
	}
	if te := (*TransitionError)(nil); errors.As(err, &te) {
		return nil, nil, fmt.Errorf("%w: %T: %w", ErrRefused, o, err)
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

// create makes the thread, and with a text, queues it as the first
// message and starts its turn in the same commit, under the command's ID,
// so a crash leaves both or neither.
func (d *decision) create(id api.ID, c api.ThreadCreate) error {
	if c.Access > api.AccessFull {
		return rejected("unknown access level")
	}
	if !c.Run.IsZero() && c.Parent.IsZero() {
		return rejected("a run starts only a child thread")
	}
	if err := d.emit(api.ThreadCreated{Workspace: c.Workspace, Title: c.Title, Model: c.Model, Access: c.Access, Parent: c.Parent}); err != nil {
		return err
	}
	if c.Text == "" {
		return nil
	}
	if err := d.emit(api.MessageQueued{Message: id, Text: c.Text, Mode: api.StartNow}); err != nil {
		return err
	}
	return d.admit(id)
}

// Limits cap the children agents start: Working at work at once under one
// thread, All under it that aren't deleted, and Depth below a thread
// of the user's. A field of zero or less takes DefaultLimits', and Depth is
// at most api.MaxDepth.
type Limits struct {
	Working int `json:"working,omitempty"`
	All     int `json:"all,omitempty"`
	Depth   int `json:"depth,omitempty"`
}

var DefaultLimits = Limits{Working: 5, All: 20, Depth: api.MaxDepth}

// Effective is l as the engine applies it.
func (l Limits) Effective() Limits {
	or := func(v, def int) int {
		if v <= 0 {
			return def
		}
		return v
	}
	return Limits{
		Working: or(l.Working, DefaultLimits.Working),
		All:     or(l.All, DefaultLimits.All),
		Depth:   min(or(l.Depth, DefaultLimits.Depth), api.MaxDepth),
	}
}

// Kids counts a thread's children that aren't deleted: those whose agent
// works, with a live run that isn't uncertain, and all of them. An
// uncertain run has no agent at work; only the user ends it.
type Kids struct{ Working, All int }

// CheckChild checks a child's create against its parent's line, the
// parent first and its top last, as far up as MaxDepth+1 threads, and the
// parent's children: the parent is there, its live run asks and can still
// take work, the child is no deeper than lim's Depth, its access no wider
// than its parent's, which is no wider than its own parent's, and the
// parent is under lim's counts. A line that runs past MaxDepth is too
// deep, cycle or not.
func CheckChild(line []Thread, kids Kids, lim Limits, c api.ThreadCreate) error {
	lim = lim.Effective()
	if len(line) == 0 || !line[0].Exists() || line[0].Deleted {
		return rejected("the parent thread is gone")
	}
	if line[0].settled() {
		return rejected("the parent thread is settled")
	}
	if live := line[0].Live(); live == nil || live.ID != c.Run || !steerable[live.State] {
		return rejected("only the parent's live run can start a child thread, and not once it is stopping")
	}
	if len(line) > lim.Depth {
		return rejected(fmt.Sprintf("a child thread can be at most %d below a thread of yours", lim.Depth))
	}
	if c.Access > line[0].Access {
		return rejected("a child thread's access can't be wider than its parent's")
	}
	if kids.Working >= lim.Working {
		return rejected(fmt.Sprintf("%d child threads of this thread are at work, the most at once; start another once one of them stops", lim.Working))
	}
	if kids.All >= lim.All {
		return rejected(fmt.Sprintf("this thread has %d child threads, the most it may; start another once one is deleted", lim.All))
	}
	return nil
}

func ceiling() api.Access { return api.AccessFull }

func (d *decision) dispatch(id api.ID, m api.MessageDispatch) error {
	if m.Mode > api.Defer {
		return rejected("unknown dispatch mode")
	}
	if d.t.Access > ceiling() {
		return rejected("thread access is above the client's ceiling")
	}
	// A run left on a settled thread is the one the settle stopped. A
	// message there steers nothing and can't start before that run ends, so
	// it waits for it.
	if d.t.settled() && d.t.Live() != nil && (m.Mode == api.SteerActive || m.Mode == api.StartNow) {
		m.Mode = api.AfterActive
	}
	// A steer never waits its turn: one the live run can't take now would
	// start a turn of its own once the run ends, after the user moved on.
	if live := d.t.Live(); m.Mode == api.SteerActive && live != nil && !steerable[live.State] {
		return rejected(fmt.Sprintf("the agent is %s and can't take a message; send it once the run ends", live.State))
	}
	// A message is new work: a settled thread comes back to the inbox with
	// it, whichever client sent it. What waited before the settle stays
	// cancelled.
	if d.t.settled() {
		if err := d.emit(api.DispositionChanged{Disposition: api.Disposition{}}); err != nil {
			return err
		}
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
	// settle, which starts the latter at once; a Defer message is held
	// until something promotes it.
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
// admission happens in the same commit as whatever freed the thread. A
// settled or deleted thread starts nothing.
func (d *decision) settle() error {
	if d.t.Live() != nil || d.t.settled() || d.t.Deleted {
		return nil
	}
	if m := d.t.next(); m != nil {
		return d.admit(m.ID)
	}
	return nil
}

// interrupt is the user's stop. Asked again once the agent took the first
// interrupt, the user gives up waiting on a run that ignores it; nobody can
// say what that run is doing. Asked again before, it changes nothing.
//
// A halt, with drop, cancels what waits its turn and stops the run. It is
// not the user giving up on a run: on one already stopping it waits for
// the run's end, as the first stop does. Settling a thread halts it.
func (d *decision) interrupt(drop bool) error {
	if drop {
		if err := d.drop("the thread was settled or deleted"); err != nil {
			return err
		}
	}
	r := d.t.Live()
	switch {
	case r == nil:
		return nil
	case drop || r.State != RunStopping:
		return d.stop(*r)
	case r.Signalled:
		return d.emit(api.TurnUncertain{Run: r.ID})
	}
	return nil
}

func (d *decision) stop(r Run) error {
	switch r.State {
	case RunStopping:
		return nil
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

// drop cancels the messages waiting their turn, held or ready, so settle
// starts none of them. One the live run claimed, a steer, goes with it.
func (d *decision) drop(reason string) error {
	var waiting []api.ID
	for _, m := range d.t.Queue {
		if m.State == QueueReady || m.State == QueueHeld {
			waiting = append(waiting, m.ID)
		}
	}
	for _, id := range waiting {
		if err := d.emit(api.MessageCancelled{Message: id, Reason: reason}); err != nil {
			return err
		}
	}
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
	if err := fits(r, b.Answer); err != nil {
		return rejected(err.Error())
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

// fits is nil when answer answers r. A question takes its answers or a
// dismissal, and refuses a misfit before the race for the request, which
// it would win; an approval takes any JSON.
func fits(r Request, answer json.RawMessage) error {
	if r.Kind != api.KindQuestion {
		return nil
	}
	a, err := api.ParseAnswers(answer)
	if err != nil {
		return err
	}
	q, _ := api.ParseQuestions(r.Body) // a body no card can show takes only a dismissal
	return a.Fit(q)
}

// inbox sets the user's disposition. Settled is done with, as archived
// was: in the same commit, what waits its turn is cancelled and the live
// run is stopped, and settle starts nothing on the thread until it's back
// in the inbox.
func (d *decision) inbox(b api.InboxSet) error {
	v := b.Disposition
	v.Until = v.Until.UTC().Round(0)
	if v.State > api.Settled || (v.State == api.Snoozed) == v.Until.IsZero() {
		return rejected("only a snoozed thread has an until, and it must")
	}
	if v.State == d.t.Disposition.State && v.Until.Equal(d.t.Disposition.Until) {
		return nil
	}
	if err := d.emit(api.DispositionChanged{Disposition: v}); err != nil {
		return err
	}
	if v.State != api.Settled {
		return nil
	}
	return d.interrupt(true)
}

// delete tombstones the thread. A live run ends with it, abandoned, in the
// same commit, with an effect that closes its agent, which a crash or a
// shutdown leaves for the next host: whatever a client last saw of the
// thread, nothing of it is left running.
func (d *decision) delete() error {
	if r := d.t.Live(); r != nil {
		if err := d.emit(api.TurnEnded{Run: r.ID, Outcome: api.OutcomeAbandoned}); err != nil {
			return err
		}
		d.effects = append(d.effects, CloseAgent{Run: r.ID, Generation: r.Generation})
	}
	return d.emit(api.ThreadDeleted{})
}

func (d *decision) rename(b api.ThreadRename) error {
	title := strings.TrimSpace(b.Title)
	if title == "" {
		return rejected("a thread needs a title")
	}
	if title == d.t.Title {
		return nil
	}
	return d.emit(api.ThreadRenamed{Title: title})
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
			stopped := live.State == RunStopping
			if err := d.emit(api.TurnEnded{Run: o.Run, Outcome: o.Outcome, Error: o.Error, Tokens: o.Tokens}); err != nil {
				return err
			}
			// A run being stopped reports nothing, however it ends.
			switch {
			case stopped:
			case o.Outcome == api.OutcomeCompleted:
				d.report(o.Run, api.ReportDone, "", nil)
			case o.Outcome == api.OutcomeFailed:
				d.report(o.Run, api.ReportFailed, o.Error, nil)
			}
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
			if err := d.emit(api.RequestOpened{
				Request: o.Request, Run: o.Run, Generation: live.Generation, Dedup: o.Dedup, Kind: o.Kind, Body: o.Body,
			}); err != nil {
				return err
			}
			d.report(o.Run, api.ReportBlocked, "", o.Body)
		}
	case Withdrawn:
		// An answer already given is left to its delivery.
		i := index(d.t.Requests, func(r Request) bool { return r.Run == o.Run && r.Dedup == o.Dedup })
		if i >= 0 && d.t.Requests[i].State == api.RequestPending {
			return d.emit(api.RequestInvalidation{Request: d.t.Requests[i].ID, Reason: o.Reason})
		}
	case Delivered:
		return d.emit(api.RequestDelivery{Request: o.Request})
	case Unheard:
		return d.emit(api.RequestInvalidation{Request: o.Request, Reason: o.Reason})
	case DeliveryFailed:
		if err := d.emit(api.RequestInvalidation{Request: o.Request, Reason: o.Error}); err != nil {
			return err
		}
		// An answer that can't be delivered is never assumed; the run that
		// waits for it is stopped instead. Only the user ends an uncertain
		// run.
		if r := d.t.Requests[d.t.requestAt(o.Request)]; isLive(r.Run) && live.State != RunUncertain {
			return d.stop(*d.t.Live())
		}
	case Steered:
		return d.emit(api.MessageDispatched{Message: o.Message})
	case SteerFailed:
		return d.emit(api.MessageCancelled{Message: o.Message, Reason: o.Error})
	case SteerLost:
		return d.emit(api.MessageUncertain{Message: o.Message})
	case InterruptSent:
		if isLive(o.Run) && live.State == RunStopping {
			return d.emit(api.TurnSignalled{Run: o.Run})
		}
	case InterruptLost:
		if isLive(o.Run) && live.State == RunStopping {
			return d.emit(api.TurnUncertain{Run: o.Run})
		}
	case Reconciled:
		if !isLive(o.Run) {
			return nil
		}
		if o.Restored {
			return d.emit(api.TurnRestored{Run: o.Run})
		}
		if o.Failed {
			d.effects = append(d.effects, CloseAgent{Run: o.Run, Generation: live.Generation})
		}
		return d.emit(api.TurnUncertain{Run: o.Run})
	case Rewound:
		return d.rewound()
	case Reported:
		if err := d.emit(o.Report); err != nil {
			return err
		}
		// A report wakes a snoozed parent, and only its parent: back in the
		// inbox. A settled one stays settled: settling halted the tree, so
		// this is a run that ended before its stop landed, and the user is
		// done with the thread. The report is logged either way.
		if d.t.Disposition.State == api.Snoozed {
			return d.emit(api.DispositionChanged{Disposition: api.Disposition{}})
		}
	}
	return nil
}

// report has a child's run report to its parent, after the commit, as how
// it went. A run the user stopped reports nothing: they asked for it.
func (d *decision) report(run api.ID, kind api.ReportKind, failure string, ask json.RawMessage) {
	if d.t.Parent.IsZero() {
		return
	}
	d.effects = append(d.effects, ReportToParent{
		Parent: d.t.Parent, Child: d.t.ID, Run: run, Kind: kind, Title: d.t.Title, Error: failure, Ask: ask,
	})
}

func (d *decision) rewound() error {
	live := d.t.Live()
	if live == nil {
		return nil
	}
	if live.State != RunUncertain {
		if err := d.emit(api.TurnUncertain{Run: live.ID}); err != nil {
			return err
		}
	}
	var ready []api.ID
	for _, m := range d.t.Queue {
		if m.State == QueueReady {
			ready = append(ready, m.ID)
		}
	}
	for _, id := range ready {
		if err := d.emit(api.MessageCancelled{Message: id, Reason: "the database was restored; send again if it never ran"}); err != nil {
			return err
		}
	}
	return nil
}
