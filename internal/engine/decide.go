package engine

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"slices"
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
	if c.Caller != nil {
		switch b := c.Body.(type) {
		case api.MessageDispatch:
			if b.Mode != api.AfterActive && b.Mode != api.SteerActive {
				return nil, nil, rejected("an agent's message to a thread under its own goes after its run or into it")
			}
		case api.TurnInterrupt:
			if b.Drop {
				return nil, nil, rejected("an agent can't drop the messages waiting in a thread")
			}
			if r := t.Live(); r != nil && r.State == RunUncertain {
				return nil, nil, rejected("the thread's run didn't answer its stop, and only the user can end it")
			}
		case api.RequestRespond:
			if i := t.requestAt(b.Request); i < 0 {
				return nil, nil, api.ErrNotFound
			} else if t.Requests[i].Kind != api.KindQuestion {
				return nil, nil, rejected("an agent can answer only a question of a thread under its own: approvals and credentials are the user's")
			}
		default:
			return nil, nil, rejected("an agent can only message, stop or answer the question of a thread under its own")
		}
		d.by = c.Caller.Thread
	}
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
		err = d.delete(b)
	case api.ThreadConfigure:
		err = d.configure(b)
	case api.QueueResume:
		err = d.resume()
	case api.QueueEdit:
		err = d.edit(b)
	case api.QueueCancel:
		err = d.cancel(b)
	case api.QueueMove:
		err = d.move(b)
	case api.QueueSteer:
		err = d.steer(b)
	case api.ThreadRevert:
		err = d.revert(c.ID, b)
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
// settles its tree, deleting it deletes its tree, and a configure that
// gives an access narrows its tree to it.
func TakesTree(b api.Intent) bool {
	switch b := b.(type) {
	case api.ThreadDelete:
		return true
	case api.InboxSet:
		return b.Disposition.State == api.Settled
	case api.ThreadConfigure:
		return b.Access != nil
	case api.TurnInterrupt:
		return b.Tree
	}
	return false
}

// Below is what b on a thread decides on t, a thread under it, when b
// takes the tree: a configure narrows t's access to the one it gives, and
// leaves t's model as it is.
func Below(b api.Intent, t Thread) api.Intent {
	if c, ok := b.(api.ThreadConfigure); ok {
		var below api.ThreadConfigure
		if c.Access != nil {
			below.Access = new(min(t.Access, *c.Access))
		}
		return below
	}
	if i, ok := b.(api.TurnInterrupt); ok {
		i.Tree = false
		return i
	}
	return b
}

// CheckConfigure checks c on t, a child thread, against parent, the
// thread it's under: the access c leaves t at can't be wider than its
// parent's.
func CheckConfigure(parent, t Thread, c api.ThreadConfigure) error {
	if _, access := c.Onto(t.Model, t.Access); parent.Exists() && !parent.Deleted && access > parent.Access {
		return rejected("a child thread's access can't be wider than its parent's")
	}
	return nil
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
	resumed bool   // the user resumed the queue: settle starts the wakes after a stop
	by      api.ID // the thread whose agent sent the command; zero for the user
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
	if c.Worktree != nil {
		return rejected("this engine makes no worktrees")
	}
	if (c.Project == "") != (c.Branch == "") {
		return rejected("a thread in a worktree has both its project and its branch")
	}
	if err := d.emit(api.ThreadCreated{Workspace: c.Workspace, Project: c.Project, Branch: c.Branch, Title: c.Title, Model: c.Model, Access: c.Access, Parent: c.Parent}); err != nil {
		return err
	}
	if c.Text == "" {
		return nil
	}
	if err := d.emit(api.MessageQueued{Message: id, Text: c.Text, Mode: api.StartNow, By: c.Parent}); err != nil {
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
// than that run's, nor than its parent's as it is now, which is no wider
// than its own parent's, and the parent is under lim's counts. A line that
// runs past MaxDepth is too deep, cycle or not.
func CheckChild(line []Thread, kids Kids, lim Limits, c api.ThreadCreate) error {
	lim = lim.Effective()
	if len(line) == 0 || !line[0].Exists() || line[0].Deleted {
		return rejected("the parent thread is gone")
	}
	if line[0].settled() {
		return rejected("the parent thread is settled")
	}
	live := line[0].Live()
	if live == nil || live.ID != c.Run || !steerable[live.State] {
		return rejected("only the parent's live run can start a child thread, and not once it is stopping")
	}
	if len(line) > lim.Depth {
		return rejected(fmt.Sprintf("a child thread can be at most %d below a thread of yours", lim.Depth))
	}
	// The run keeps the access it was admitted at, whatever the thread was
	// given since for its next turn.
	if c.Access > live.Access {
		return rejected("a child thread's access can't be wider than the run's that starts it")
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

// CheckCaller checks c, an agent's run, against line, the thread it acts
// on and those above it, and userLast, whether the user sent the thread's
// last message or stop: c's thread is above it and c's run is that
// thread's live run, still steerable and not a silent one, and the user
// hasn't taken the thread over.
func CheckCaller(line []Thread, c api.Caller, userLast bool) error {
	i := slices.IndexFunc(line, func(t Thread) bool { return t.ID == c.Thread })
	if i < 1 {
		return rejected("an agent can only act on a thread under its own")
	}
	if line[i].Deleted || line[i].settled() {
		return rejected("the agent's thread is settled or gone")
	}
	if live := line[i].Live(); live == nil || live.ID != c.Run || !steerable[live.State] || live.Silent {
		return rejected("only the live run of the thread above can act on it, and not once it is stopping")
	}
	if userLast {
		return rejected("the user took this thread over: its last message or stop was theirs, so it's theirs to steer")
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
	if live := d.t.Live(); !m.Replaces.IsZero() && (m.Mode != api.RestartActive || live == nil || live.ID != m.Replaces || live.State != RunUncertain) {
		return rejected("the run this was to replace has already ended")
	}
	if m.Auto && (m.Replaces.IsZero() || !d.t.Parent.IsZero() || d.t.settled() || d.t.Disposition.State == api.Snoozed && d.at.Before(d.t.Disposition.Until)) {
		return rejected("the engine continues only a run it lost, on a thread of the user's neither settled nor snoozed")
	}
	// A run left on a settled thread is the one the settle stopped, and a
	// silent maintenance run (such as idle compaction) cannot be steered.
	// A message sent while either is live queues right behind it.
	if live := d.t.Live(); live != nil && (d.t.settled() || live.Silent) && (m.Mode == api.SteerActive || m.Mode == api.StartNow) {
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
	// The user sending again is their go-ahead for what their stop held.
	if m.Mode != api.Defer {
		if err := d.release(); err != nil {
			return err
		}
	}
	q := api.MessageQueued{Message: id, Text: m.Text, Mode: m.Mode, Pos: d.t.pos(false), Held: m.Mode == api.Defer, By: d.by, Replaces: m.Replaces, Auto: m.Auto}
	switch {
	case m.Mode == api.RestartActive:
		q.Pos = d.t.pos(true)
	case m.Mode == api.StartNow && d.t.Live() != nil:
		// A start now on a busy thread starts the moment the run ends, and
		// stops nothing: it goes ahead of what waits, behind the start
		// nows sent before it.
		pos, err := d.place(id, d.t.afterStarts())
		if err != nil {
			return err
		}
		q.Pos = pos
	}
	if err := d.emit(q); err != nil {
		return err
	}
	live := d.t.Live()
	switch {
	case m.Mode == api.StartNow && live == nil:
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

// afterStarts is the first message waiting its turn that isn't a start
// now ready to go, or a restart's: where a start now sent while the thread
// is busy goes. Zero when there is none.
func (t Thread) afterStarts() api.ID {
	for _, m := range t.line() {
		if m.State != QueueReady || m.Mode != api.StartNow && m.Mode != api.RestartActive {
			return m.ID
		}
	}
	return api.ID{}
}

// place makes room in the line for message just before the message
// before, or last with before zero, and returns the position it takes
// there. Positions are whole numbers: where the two it goes between leave
// none free, those from before on move up one each, as far as they must.
func (d *decision) place(message, before api.ID) (int64, error) {
	if before.IsZero() {
		return d.t.pos(false), nil
	}
	line := slices.DeleteFunc(d.t.line(), func(m Message) bool { return m.ID == message })
	i := index(line, func(m Message) bool { return m.ID == before })
	switch {
	case i < 0:
		return 0, rejected("the message to put it before no longer waits in the queue")
	case i == 0:
		return line[0].Pos - 1, nil
	case line[i].Pos-line[i-1].Pos > 1:
		return line[i-1].Pos + (line[i].Pos-line[i-1].Pos)/2, nil
	}
	at := line[i-1].Pos + 1
	for next := at + 1; i < len(line) && line[i].Pos < next; i, next = i+1, next+1 {
		if err := d.emit(api.MessageMoved{Message: line[i].ID, Pos: next}); err != nil {
			return 0, err
		}
	}
	return at, nil
}

// admit starts message's turn. The wakes that wait go in with it, claimed
// by its run: the reports first, so the user's words come last.
func (d *decision) admit(message api.ID) error {
	m := d.t.Queue[d.t.messageAt(message)]
	first := len(d.t.Runs) == 0
	var texts []string
	var with []api.ID
	for _, w := range d.t.Queue {
		if w.waking() && w.ID != message {
			texts, with = append(texts, w.Text), append(with, w.ID)
		}
	}
	if m.waking() {
		texts = append([]string{m.Text}, texts...)
	} else {
		texts = append(texts, m.Text)
	}
	gen := d.t.Generation + 1
	if err := d.emit(api.TurnAdmitted{Run: message, Generation: gen}); err != nil {
		return err
	}
	for _, id := range with {
		if err := d.emit(api.MessageClaimed{Message: id, Run: message}); err != nil {
			return err
		}
	}
	if first {
		d.checkpoint(message, api.CheckpointBase)
	}
	d.effects = append(d.effects, StartTurn{
		Run: message, Generation: gen, Text: strings.Join(texts, "\n\n"), Workspace: d.t.Workspace, Model: d.t.Model, Access: d.t.Access,
	})
	return nil
}

// checkpoint has the runtime save the files of the thread's worktree for
// run, before anything later in the commit, such as the next turn's start.
// Only a thread of the user's in a worktree of its own keeps checkpoints:
// a child's files are its parent's, and a folder the thread shares is
// never one it can restore. A silent run changes no files.
func (d *decision) checkpoint(run api.ID, phase api.CheckpointPhase) {
	if d.t.checkpointed() && !d.t.silentRun(run) {
		d.effects = append(d.effects, CaptureCheckpoint{Run: run, Phase: phase})
	}
}

// sent marks the wakes that went in with run's first message as handed to
// its agent, as that message is.
func (d *decision) sent(run api.ID) error {
	var ids []api.ID
	for _, m := range d.t.Queue {
		if m.State == QueueClaimed && m.Run == run && m.ID != run && m.Mode == api.AfterActive {
			ids = append(ids, m.ID)
		}
	}
	for _, id := range ids {
		if err := d.emit(api.MessageDispatched{Message: id}); err != nil {
			return err
		}
	}
	return nil
}

// settle starts the next ready message whenever the thread is idle, so
// admission happens in the same commit as whatever freed the thread. A
// settled or deleted thread starts nothing. With no ready message, a wake
// starts a turn of its own, unless the last run was stopped: a stop stays
// a stop, and the wakes go in with the next message the user sends, or
// start once they resume the queue.
func (d *decision) settle() error {
	if d.t.Live() != nil || d.t.settled() || d.t.Deleted {
		return nil
	}
	if m := d.t.next(); m != nil {
		return d.admit(m.ID)
	}
	if d.t.stopped() && !d.resumed {
		return nil
	}
	if i := index(d.t.Queue, Message.waking); i >= 0 {
		return d.admit(d.t.Queue[i].ID)
	}
	return nil
}

// interrupt is the user's stop. Asked again once the agent took the first
// interrupt, the user gives up waiting on a run that ignores it; nobody can
// say what that run is doing. Asked again before, it changes nothing.
//
// A stop goes quiet: what waits its turn is held until the user sends again
// or resumes the queue. One of a silent run holds nothing, as the user's
// messages wait behind it, not on it.
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
	if r != nil && !r.Silent {
		if err := d.emit(api.StopAsked{Run: r.ID, By: d.by}); err != nil {
			return err
		}
	}
	if r != nil && !r.Silent && !drop {
		if err := d.hold(); err != nil {
			return err
		}
	}
	switch {
	case r == nil:
		return nil
	case drop || r.State != RunStopping:
		return d.stop(*r)
	case !d.by.IsZero():
		// An agent stops a run that runs: one already stopping is the
		// user's to give up on.
		return nil
	case r.Signalled:
		return d.lost(*r)
	}
	return nil
}

// lost is that r's agent no longer answers. The user decides what an
// uncertain run did, but a silent one asked them nothing and resumed the
// conversation: it ends, and its agent is closed.
func (d *decision) lost(r Run) error {
	if !r.Silent {
		return d.uncertain(api.TurnUncertain{Run: r.ID, Reason: api.UncertainStopped})
	}
	d.effects = append(d.effects, CloseAgent{Run: r.ID, Generation: r.Generation})
	return d.emit(api.TurnEnded{Run: r.ID, Outcome: api.OutcomeFailed, Error: "the compaction could not be stopped"})
}

func (d *decision) stop(r Run) error {
	switch r.State {
	case RunStopping:
		return nil
	case RunUncertain:
		// Ending an uncertain run is the user's explicit decision to move on.
		if err := d.emit(api.TurnEnded{Run: r.ID, Outcome: api.OutcomeAbandoned}); err != nil {
			return err
		}
		d.checkpoint(r.ID, api.CheckpointRun)
		return nil
	}
	if err := d.emit(api.TurnStopping{Run: r.ID}); err != nil {
		return err
	}
	d.effects = append(d.effects, InterruptTurn{Run: r.ID})
	return nil
}

// hold holds the ready messages, so settle starts none of them.
func (d *decision) hold() error {
	var ready []api.ID
	for _, m := range d.t.Queue {
		if m.State == QueueReady {
			ready = append(ready, m.ID)
		}
	}
	for _, id := range ready {
		if err := d.emit(api.MessageHeld{Message: id}); err != nil {
			return err
		}
	}
	return nil
}

// release readies the messages of the user's that are held: those a stop
// held, and those sent as a Defer.
func (d *decision) release() error {
	var held []api.ID
	for _, m := range d.t.Queue {
		if m.State == QueueHeld && m.Mode == api.Defer {
			held = append(held, m.ID)
		}
	}
	for _, id := range held {
		if err := d.emit(api.MessageReleased{Message: id}); err != nil {
			return err
		}
	}
	return nil
}

// resume is the user's go-ahead after a stop. A live run holds the
// wakes until it ends, and a resume can't wait for that: with nothing of
// the user's held, it's refused, for them to resume once the run ends.
func (d *decision) resume() error {
	if d.t.Live() != nil && !d.t.holding() {
		return rejected("the agent is still at work; resume once its run ends")
	}
	d.resumed = true
	return d.release()
}

// queued is the message of the user's id names, while it waits its turn:
// what the queue's intents act on.
func (d *decision) queued(id api.ID) (Message, error) {
	if m, ok := d.t.Message(id); ok && m.waiting() {
		return m, nil
	}
	return Message{}, rejected("the message no longer waits in the queue")
}

func (d *decision) edit(b api.QueueEdit) error {
	m, err := d.queued(b.Message)
	if err != nil {
		return err
	}
	if strings.TrimSpace(b.Text) == "" {
		return rejected("a message needs words; take it out of the queue instead")
	}
	if b.Text == m.Text {
		return nil
	}
	return d.emit(api.MessageEdited{Message: m.ID, Text: b.Text})
}

func (d *decision) cancel(b api.QueueCancel) error {
	if _, err := d.queued(b.Message); err != nil {
		return err
	}
	return d.emit(api.MessageCancelled{Message: b.Message, Reason: "taken out of the queue"})
}

// move puts a message in the line just before another, or last. One
// already there stays, and nothing is recorded.
func (d *decision) move(b api.QueueMove) error {
	if _, err := d.queued(b.Message); err != nil {
		return err
	}
	if b.Before == b.Message {
		return rejected("a message can't go before itself")
	}
	line := d.t.line()
	i := index(line, func(m Message) bool { return m.ID == b.Message })
	if b.Before.IsZero() && i == len(line)-1 || i+1 < len(line) && line[i+1].ID == b.Before {
		return nil
	}
	pos, err := d.place(b.Message, b.Before)
	if err != nil {
		return err
	}
	return d.emit(api.MessageMoved{Message: b.Message, Pos: pos})
}

// steer sends a message that waits its turn now, as a send does: into the
// live run, as a SteerActive, or with none, as the thread's next turn. It
// stops nothing, so a run that can't take a message refuses it. Like a
// send, it's the user's go-ahead for what their stop held, as a row's
// Steer now is in Zeron.
func (d *decision) steer(b api.QueueSteer) error {
	m, err := d.queued(b.Message)
	if err != nil {
		return err
	}
	live := d.t.Live()
	switch {
	case live != nil && live.Silent:
		return rejected("the agent is compacting its context and can't take a message; it goes once that ends")
	case live != nil && !steerable[live.State]:
		return rejected(fmt.Sprintf("the agent is %s and can't take a message; send it once the run ends", live.State))
	}
	if err := d.release(); err != nil {
		return err
	}
	if live == nil {
		return d.admit(m.ID)
	}
	if err := d.emit(api.MessageClaimed{Message: m.ID, Run: live.ID, Steer: true}); err != nil {
		return err
	}
	d.effects = append(d.effects, SteerTurn{Run: live.ID, Message: m.ID, Text: m.Text})
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
	if r.Kind == api.KindCredentials {
		p, _ := api.ParseProvided(b.Answer)
		b.Answer, _ = json.Marshal(p)
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
// it would win; credentials take the names provided, never values, or
// none; an approval takes any JSON.
func fits(r Request, answer json.RawMessage) error {
	if r.Kind == api.KindCredentials {
		p, err := api.ParseProvided(answer)
		if err != nil {
			return err
		}
		c, err := api.ParseCredentials(r.Body)
		if err != nil {
			return err
		}
		return p.Fit(c)
	}
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
// thread, nothing of it is left running. An idle thread's last run gets the
// close too, for an agent its provider kept for the next run.
//
// One that removes the worktree waits for the tree to be still: an agent
// closing may still write there, after the host looked for changes. It
// removes the worktree of each thread of the tree in one, after its agent's
// close, as an effect a crash leaves for the next host.
func (d *decision) delete(b api.ThreadDelete) error {
	if b.RemoveWorktree && d.t.Live() != nil {
		return rejected("stop the thread, and every thread under it, before its worktree goes")
	}
	if r := d.t.Live(); r != nil {
		if err := d.emit(api.TurnEnded{Run: r.ID, Outcome: api.OutcomeAbandoned}); err != nil {
			return err
		}
		d.effects = append(d.effects, CloseAgent{Run: r.ID, Generation: r.Generation})
	} else if n := len(d.t.Runs); n > 0 {
		r := d.t.Runs[n-1]
		d.effects = append(d.effects, CloseAgent{Run: r.ID, Generation: r.Generation})
	}
	if b.RemoveWorktree && d.t.Branch != "" {
		d.effects = append(d.effects, RemoveWorktree{Workspace: d.t.Workspace, Project: d.t.Project, Branch: d.t.Branch})
	}
	if d.t.checkpointed() {
		d.effects = append(d.effects, DropCheckpoints{})
	}
	return d.emit(api.ThreadDeleted{})
}

// revert has the runtime put the thread's files back as a checkpoint of
// the thread's holds them. A thread with no worktree of its own keeps no
// checkpoints, nor does a child, whose work reaches the user through its
// parent's branch; and a run at work could write the files under the
// revert, so the thread is idle. The runtime finds whether the
// checkpoint was saved, and refuses a tree at work or a folder another
// thread works in before this commits.
func (d *decision) revert(id api.ID, b api.ThreadRevert) error {
	switch {
	case !b.Files:
		return rejected("a revert puts files back; rewinding the conversation is to come")
	case !d.t.Parent.IsZero():
		return rejected("A child thread keeps no checkpoints; revert its parent instead")
	case !d.t.checkpointed():
		return rejected("Revert needs this thread to run in its own worktree")
	case d.t.Live() != nil:
		return rejected("Stop the thread before reverting its files")
	}
	switch b.To.Phase {
	case api.CheckpointBase:
		if len(d.t.Runs) == 0 || d.t.Runs[0].ID != b.To.ID {
			return rejected("the base checkpoint is its first run's")
		}
	case api.CheckpointRun:
		if d.t.runAt(b.To.ID) < 0 || d.t.silentRun(b.To.ID) {
			return rejected("no run of the thread's has that checkpoint")
		}
	case api.CheckpointRecovery:
		if b.To.ID.IsZero() {
			return rejected("a recovery checkpoint is named by its revert")
		}
	default:
		return rejected("unknown checkpoint")
	}
	if err := d.emit(api.ThreadReverting{Revert: id, To: b.To}); err != nil {
		return err
	}
	d.effects = append(d.effects, RevertThread{Revert: id, To: b.To})
	return nil
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

// configure sets what b gives of the thread's model and access, on the
// ones the thread has now: what b leaves out stays, whatever its sender
// last saw of it. The provider is none of b's to give.
func (d *decision) configure(b api.ThreadConfigure) error {
	m, access := b.Onto(d.t.Model, d.t.Access)
	if access > ceiling() {
		return rejected("unknown access level")
	}
	if m.Model == "" && d.t.Model.Model != "" {
		return rejected("a thread needs a model")
	}
	if m == d.t.Model && access == d.t.Access {
		return nil
	}
	return d.emit(api.ThreadConfigured{Model: m, Access: access})
}

func holds[T comparable](held *T, v T) bool { return held != nil && *held == v }

func (d *decision) observe(o Observation) error {
	live := d.t.Live()
	isLive := func(run api.ID) bool { return live != nil && live.ID == run }
	switch o := o.(type) {
	case Bound:
		// A run that is no longer the thread's binds nothing.
		if isLive(o.Run) && o.Session != "" && o.Session != d.t.Session {
			return d.emit(api.SessionBound{Run: o.Run, Session: o.Session})
		}
	case Started:
		if !isLive(o.Run) {
			return nil
		}
		if live.State != RunStarting {
			// Interrupted while starting, but the agent did get the message.
			if err := d.emit(api.MessageDispatched{Message: o.Run}); err != nil {
				return err
			}
			return d.sent(o.Run)
		}
		if err := d.emit(api.TurnStarted{Run: o.Run}); err != nil {
			return err
		}
		return d.sent(o.Run)
	case Ended:
		// Only the user ends an uncertain run.
		if isLive(o.Run) && live.State != RunUncertain {
			stopped := live.State == RunStopping
			silent := live.Silent
			// A run that ended while it stopped was stopped, whichever
			// came first, its end or its interrupt's word: a stop stays a
			// stop.
			if stopped && !live.Signalled {
				if err := d.emit(api.TurnSignalled{Run: o.Run}); err != nil {
					return err
				}
			}
			if err := d.emit(api.TurnEnded{Run: o.Run, Outcome: o.Outcome, Error: o.Error, Tokens: o.Tokens}); err != nil {
				return err
			}
			d.checkpoint(o.Run, api.CheckpointRun)
			// A run being stopped or a silent maintenance run reports nothing.
			switch {
			case stopped || silent:
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
	case Context:
		if isLive(o.Run) {
			return d.emit(api.ContextReported{
				Run: o.Run, Used: o.Used, Window: o.Window, Model: o.Model, Compacted: o.Compacted,
				CacheExpires: o.CacheExpires, PreTokens: o.PreTokens, PostTokens: o.PostTokens,
			})
		}
	case Granted:
		// A confirmation the thread holds already says nothing new.
		if isLive(o.Run) && !holds(d.t.Confirmed.Access, api.ConfirmedAccess{Run: o.Run, Asked: live.Access, Access: o.Access, Mode: o.Mode, Why: o.Why}) {
			return d.emit(api.AccessGranted{Run: o.Run, Access: o.Access, Mode: o.Mode, Why: o.Why})
		}
	case Ran:
		if isLive(o.Run) && !holds(d.t.Confirmed.Model, api.ConfirmedModel{Run: o.Run, Asked: live.Model, Model: o.Model, Effort: o.Effort, Why: o.Why}) {
			return d.emit(api.ModelConfirmed{Run: o.Run, Model: o.Model, Effort: o.Effort, Why: o.Why})
		}
	case Work:
		if isLive(o.Item.Run) {
			return d.emit(o.Item)
		}
	case Subagent:
		if isLive(o.Run) {
			return d.emit(o.Event)
		}
	case Plan:
		if isLive(o.Plan.Run) {
			return d.emit(o.Plan)
		}
	case Captured:
		if d.t.runAt(o.Checkpoint.Run) >= 0 {
			return d.emit(o.Checkpoint)
		}
	case Reverted:
		return d.emit(o.Revert)
	case Surfaced:
		dup := index(d.t.Requests, func(r Request) bool { return r.Run == o.Run && r.Dedup == o.Dedup }) >= 0
		if isLive(o.Run) && !dup && live.Silent {
			// Nobody watches a maintenance run to answer it, and the
			// user's next message waits behind it: it stops instead.
			return d.stop(*live)
		}
		if isLive(o.Run) && !dup {
			if err := d.emit(api.RequestOpened{
				Request: o.Request, Run: o.Run, Generation: live.Generation, Dedup: o.Dedup, Kind: o.Kind, Body: o.Body,
			}); err != nil {
				return err
			}
			d.report(o.Run, api.ReportBlocked, "", &api.Request{ID: o.Request, Kind: o.Kind, Body: o.Body})
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
			return d.lost(*live)
		}
	case Reconciled:
		if !isLive(o.Run) {
			return nil
		}
		if o.Restored {
			if err := d.emit(api.TurnRestored{Run: o.Run}); err != nil {
				return err
			}
			return d.sent(o.Run)
		}
		if o.Failed {
			d.effects = append(d.effects, CloseAgent{Run: o.Run, Generation: live.Generation})
		}
		// A maintenance run resumed the conversation and asked the user
		// nothing, so there is nothing for them to decide: it ends.
		if live.Silent {
			return d.emit(api.TurnEnded{Run: o.Run, Outcome: api.OutcomeFailed, Error: "the engine restarted during the compaction"})
		}
		switch {
		case o.Asked:
			return d.uncertain(api.TurnUncertain{Run: o.Run, Reason: api.UncertainStopped})
		case o.Failed:
			return d.uncertain(api.TurnUncertain{Run: o.Run, Reason: api.UncertainUnconfirmed})
		}
		worked := live.State == RunRunning || live.State == RunStarting
		return d.uncertain(api.TurnUncertain{Run: o.Run, Reason: api.UncertainRestarted, Continue: o.Continue && worked && d.t.Parent.IsZero()})
	case Rewound:
		return d.rewound()
	case Reported:
		return d.reported(o)
	case IdleCompact:
		return d.idleCompact(o)
	}
	return nil
}

// idleCompact starts a silent /compact run when the thread is still idle,
// unchanged since AfterRun, and inside its idle compaction window.
func (d *decision) idleCompact(o IdleCompact) error {
	afterRun, start, end, ok := IdleCompactWindow(d.t, d.at)
	if !ok || afterRun != o.AfterRun || d.at.Before(start) || !d.at.Before(end) {
		return fmt.Errorf("%w: thread is not eligible for idle compaction", ErrRefused)
	}
	if o.Run.IsZero() {
		return fmt.Errorf("%w: idle compaction needs a run id", ErrRefused)
	}
	if err := d.emit(api.MessageQueued{Message: o.Run, Text: "/compact", Mode: api.StartNow, Pos: d.t.pos(false), Silent: true}); err != nil {
		return err
	}
	gen := d.t.Generation + 1
	if err := d.emit(api.TurnAdmitted{Run: o.Run, Generation: gen, Silent: true}); err != nil {
		return err
	}
	d.effects = append(d.effects, StartTurn{
		Run: o.Run, Generation: gen, Text: "/compact", Workspace: d.t.Workspace, Model: d.t.Model, Access: d.t.Access, ResumeOnly: true,
	})
	return nil
}

// reported logs a child's report. It brings a snoozed parent, and only its
// parent, back to the inbox. A done or failed one queues a wake, which
// gives the parent's agent the child's result with its next turn (see
// settle), and so does a question the child waits on, with a wake of its
// own, for the parent's agent to answer or leave to the user. An approval
// or credentials are the user's alone, and wake nothing. A settled parent stays settled and takes no wake: settling
// halted the tree, so this is a run that ended before its stop landed, and
// the user is done with the thread. The report is logged either way. A
// report the parent already took a wake for is a replay, and changes
// nothing.
func (d *decision) reported(o Reported) error {
	id, text := wakeID(o.Report.Run), wakeText(o, d.t.Branch)
	if !o.Question.IsZero() {
		id, text = askWakeID(o.Question), askText(o)
	}
	if d.t.messageAt(id) >= 0 {
		return nil
	}
	if err := d.emit(o.Report); err != nil {
		return err
	}
	if d.t.Disposition.State == api.Snoozed {
		if err := d.emit(api.DispositionChanged{Disposition: api.Disposition{}}); err != nil {
			return err
		}
	}
	if o.Report.Kind == api.ReportBlocked && o.Question.IsZero() || d.t.settled() {
		return nil
	}
	return d.emit(api.MessageQueued{
		Message: id, Text: text, Mode: api.AfterActive, Pos: d.t.pos(false), Held: true, Report: o.Report.Child,
	})
}

// wakeID is the wake for a child's run, the same each time it reports, so
// a report replayed finds the wake it took. It keeps the run's time; the
// rest is a hash of the run.
func wakeID(run api.ID) api.ID {
	sum := sha256.Sum256(append([]byte("glitched wake "), run[:]...))
	var id api.ID
	copy(id[:6], run[:6])
	copy(id[6:], sum[:])
	return id
}

// askWakeID is the wake for a child's question, one per question request,
// apart from its run's: the run's own report still wakes the parent when
// it ends.
func askWakeID(request api.ID) api.ID {
	sum := sha256.Sum256(append([]byte("glitched ask wake "), request[:]...))
	var id api.ID
	copy(id[:6], request[:6])
	copy(id[6:], sum[:])
	return id
}

// askText gives the parent's agent a child's question, in the task tags
// wakeText uses, with the request task_answer names. A body no card can
// show is the user's to dismiss.
func askText(o Reported) string {
	var b strings.Builder
	q, err := api.ParseQuestions(o.Ask)
	if err != nil {
		b.WriteString(o.Report.Outcome + "\n")
	}
	for i, x := range q.Questions {
		fmt.Fprintf(&b, "%d. %s", i+1, x.Text)
		if x.Multi {
			b.WriteString(" (pick any)")
		}
		b.WriteString("\n")
		for _, opt := range x.Options {
			fmt.Fprintf(&b, "   - %s", opt.Label)
			if opt.About != "" {
				fmt.Fprintf(&b, ": %s", opt.About)
			}
			b.WriteString("\n")
		}
	}
	return fmt.Sprintf("<task id=\"%s\" title=\"%s\" state=\"waiting_on_question\" request=\"%s\">\n<task_question>\n%s</task_question>\n</task>\nAnswer it with task_answer, or leave it to the user, who sees it too.",
		o.Report.Child, html.EscapeString(o.Report.Title), o.Question, b.String())
}

// wakeText gives the parent's agent a child's result, in the tags Zeron
// gives a task's: which child, how it ended, and what it said last. A
// child on a branch other than the parent's, in a worktree of its own,
// names it, for the parent to merge.
func wakeText(o Reported, parentBranch string) string {
	state := "completed"
	if o.Report.Kind == api.ReportFailed {
		state = "failed"
	}
	reply := cmp.Or(strings.TrimSpace(o.Reply), o.Report.Outcome)
	branch := ""
	if o.Branch != "" && o.Branch != parentBranch {
		branch = fmt.Sprintf(" branch=\"%s\"", html.EscapeString(o.Branch))
	}
	return fmt.Sprintf("<task id=\"%s\" title=\"%s\" state=\"%s\"%s>\n<task_result>\n%s\n</task_result>\n</task>",
		o.Report.Child, html.EscapeString(o.Report.Title), state, branch, reply)
}

// report has a child's run report to its parent, after the commit, as how
// it went, with the request a blocked run waits on. A run the user stopped
// reports nothing: they asked for it.
func (d *decision) report(run api.ID, kind api.ReportKind, failure string, ask *api.Request) {
	if d.t.Parent.IsZero() {
		return
	}
	r := ReportToParent{Parent: d.t.Parent, Child: d.t.ID, Run: run, Kind: kind, Title: d.t.Title, Error: failure, Branch: d.t.Branch}
	if ask != nil {
		r.Ask = ask.Body
		if ask.Kind == api.KindQuestion {
			r.Question = ask.ID
		}
	}
	d.effects = append(d.effects, r)
}

// uncertain is that nobody can prove what u's run's agent is doing. A
// child's parent hears it waits on the user, as it does of an approval,
// unless it was stopped: that was asked for.
func (d *decision) uncertain(u api.TurnUncertain) error {
	if err := d.emit(u); err != nil {
		return err
	}
	if u.Reason != api.UncertainStopped {
		d.report(u.Run, api.ReportBlocked, "", nil)
	}
	return nil
}

func (d *decision) rewound() error {
	// A run already uncertain is so again for the rewind, which no restart
	// continues: the history it was to go on from may be lost.
	switch live := d.t.Live(); {
	case live == nil:
	case live.State == RunUncertain:
		if err := d.emit(api.TurnUncertain{Run: live.ID, Reason: api.UncertainRewound}); err != nil {
			return err
		}
	default:
		if err := d.uncertain(api.TurnUncertain{Run: live.ID, Reason: api.UncertainRewound}); err != nil {
			return err
		}
	}
	var queued []api.ID
	for _, m := range d.t.Queue {
		if m.waiting() {
			queued = append(queued, m.ID)
		}
	}
	for _, id := range queued {
		if err := d.emit(api.MessageCancelled{Message: id, Reason: "the database was restored; send again if it never ran"}); err != nil {
			return err
		}
	}
	return nil
}
