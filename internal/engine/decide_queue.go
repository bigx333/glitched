package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"glitched/internal/api"
)

func (d *decision) dispatch(id api.ID, m api.MessageDispatch) error {
	if m.Mode > api.Defer {
		return rejected("unknown dispatch mode")
	}
	if d.t.Access > api.AccessFull {
		return rejected("thread access is above the client's ceiling")
	}
	if o := d.t.Origin; o != nil && !o.Making && d.t.Workspace == "" {
		return rejected("this thread's worktree couldn't be made, so it runs nothing")
	}
	if o := d.t.Origin; o != nil && d.by == o.Thread && m.Fence < o.Fence {
		return rejected("the link's message was sent before a settle that came after it")
	}
	if live := d.t.Live(); !m.Replaces.IsZero() && (m.Mode != api.RestartActive || live == nil || live.ID != m.Replaces || live.State != RunUncertain) {
		return rejected("the run this was to replace has already ended")
	}
	// The engine's own continue is of a run it lost, or of a turn a usage
	// limit ended, once the limit reset, as api.Thread.LimitResume says.
	if m.Auto && m.Replaces.IsZero() {
		if at, ok := View(d.t).LimitResume(d.at); !ok || at.After(d.at) {
			return rejected("the engine continues a turn a usage limit ended only once it resets, on a thread of the user's that waits on nothing else")
		}
	}
	if m.Auto && (d.t.child() || d.t.settled() || d.t.Disposition.State == api.Snoozed && d.at.Before(d.t.Disposition.Until)) {
		return rejected("the engine continues only a run it lost, or a turn a usage limit ended, on a thread of the user's neither settled nor snoozed")
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
	if live := d.t.Live(); m.Mode == api.SteerActive && live != nil && !d.under(live.Access) {
		return rejected("the run was admitted wider than this thread may run at now, so it takes no message")
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
		return d.admit(id, d.t.UpdatedAt)
	case m.Mode == api.SteerActive && live != nil && steerable[live.State]:
		if err := d.raise(*live); err != nil {
			return err
		}
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
// by its run: the reports first, so the user's words come last. A wake's
// run keeps prior, the thread's UpdatedAt before it, for a dismissal.
func (d *decision) admit(message api.ID, prior time.Time) error {
	if !d.runs() {
		return nil
	}
	m := d.t.Queue[d.t.messageAt(message)]
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
	admitted := api.TurnAdmitted{Run: message, Generation: gen}
	if m.waking() {
		admitted.Wake, admitted.Prior = api.WakeReport, prior
	}
	if err := d.emit(admitted); err != nil {
		return err
	}
	for _, id := range with {
		if err := d.emit(api.MessageClaimed{Message: id, Run: message}); err != nil {
			return err
		}
	}
	d.effects = append(d.effects, StartTurn{
		Run: message, Generation: gen, Text: strings.Join(texts, "\n\n"), Workspace: d.t.Workspace, Model: d.t.Model, Access: d.t.Access,
	})
	return nil
}

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
// settled or deleted thread starts nothing, nor does a remote child's
// placeholder, ever. With no ready message, a wake
// starts a turn of its own, unless the last run was stopped: a stop stays
// a stop, and the wakes go in with the next message the user sends, or
// start once they resume the queue.
func (d *decision) settle(prior time.Time) error {
	if d.t.Live() != nil || d.t.settled() || d.t.Deleted || d.t.Remote != nil {
		return nil
	}
	if m := d.t.next(); m != nil {
		return d.admit(m.ID, prior)
	}
	if d.t.stopped() && !d.resumed {
		return nil
	}
	if i := index(d.t.Queue, Message.waking); i >= 0 {
		return d.admit(d.t.Queue[i].ID, prior)
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
	case r == nil && !drop && d.t.Origin != nil:
		// A stop of a link's thread with nothing running holds what
		// waits, as its first message before its worktree is made or
		// while its access is over the cap, and stops its last run, so
		// its wakes wait as a stopped run's do. With no run yet the stop
		// is still asked, so whose it was is kept.
		var last api.ID
		for i := len(d.t.Runs) - 1; i >= 0; i-- {
			if r := d.t.Runs[i]; !r.Silent {
				last = r.ID
				break
			}
		}
		if err := d.emit(api.StopAsked{Run: last, By: d.by}); err != nil {
			return err
		}
		return d.hold()
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
// cancelResume stops the engine continuing the thread's last turn once the
// usage limit that ended it resets.
func (d *decision) cancelResume(c api.ResumeCancel) error {
	r, ok := d.t.lastTurn()
	if !ok || r.ID != c.Run || r.Limit == nil || r.ResumeCancelled {
		return rejected("no usage limit's reset is waited on")
	}
	return d.emit(api.ResumeCancelled{Run: r.ID})
}

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
	case live != nil && !d.under(live.Access):
		return rejected("the run was admitted wider than this thread may run at now, so it takes no message")
	}
	if err := d.release(); err != nil {
		return err
	}
	if live == nil {
		return d.admit(m.ID, d.t.UpdatedAt)
	}
	if err := d.raise(*live); err != nil {
		return err
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
// none; an approval keeps its grant always or for the session only where
// its body offers that, and takes any other JSON.
func fits(r Request, answer json.RawMessage) error {
	if r.Kind == api.KindApproval {
		var a, offered struct{ Always, Session bool }
		json.Unmarshal(answer, &a)
		json.Unmarshal(r.Body, &offered)
		switch {
		case a.Always && a.Session:
			return errors.New("an answer keeps its grant always or for the session, not both")
		case a.Always && !offered.Always:
			return errors.New("the agent offers no way to always allow this")
		case a.Session && !offered.Session:
			return errors.New("the agent offers no way to allow this for the session")
		}
		return nil
	}
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
// in the inbox. A Manager stays in the inbox: only Open is set on one,
// which brings back one a store kept snoozed or settled from before.
func (d *decision) inbox(b api.InboxSet) error {
	v := b.Disposition
	v.Until = v.Until.UTC().Round(0)
	if v.State > api.Settled || (v.State == api.Snoozed) == v.Until.IsZero() {
		return rejected("only a snoozed thread has an until, and it must")
	}
	if d.t.Role == api.RoleManager && v.State != api.Open {
		return rejected("a manager thread cannot be snoozed or settled")
	}
	if v.State == d.t.Disposition.State && v.Until.Equal(d.t.Disposition.Until) {
		// A link's settle of a thread settled already still fences what
		// the link sent before it.
		if o := d.t.Origin; o != nil && b.Fence > o.Fence {
			return d.emit(api.DispositionChanged{Disposition: v, Fence: b.Fence})
		}
		return nil
	}
	if err := d.emit(api.DispositionChanged{Disposition: v, Fence: b.Fence}); err != nil {
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

// configure sets what b gives of the thread's model and access, on the
// ones the thread has now: what b leaves out stays, whatever its sender
// last saw of it. The provider is none of b's to give.
func (d *decision) configure(b api.ThreadConfigure) error {
	m, access := b.Onto(d.t.Model, d.t.Access)
	if access > api.AccessFull {
		return rejected("unknown access level")
	}
	if access > d.t.Access && !d.under(access) {
		return rejected("this thread came from another machine, and may run no wider than its parent there, its link and this machine's setting allow")
	}
	if m.Model == "" && d.t.Model.Model != "" {
		return rejected("a thread needs a model")
	}
	var newSweep *api.SweepSchedule
	sweepChanged := false
	if b.Sweep != nil {
		if b.Sweep.IntervalSeconds < 0 {
			return rejected("sweep interval must be non-negative")
		}
		if b.Sweep.IntervalSeconds > 0 && d.t.Role != api.RoleManager {
			return rejected("only a manager thread can schedule sweeps")
		}
		newSweep = normalizeSweep(b.Sweep)
		sweepChanged = !sweepEqual(d.t.Sweep, newSweep)
	}
	if m == d.t.Model && access == d.t.Access && !sweepChanged {
		return nil
	}
	return d.emit(api.ThreadConfigured{Model: m, Access: access, Sweep: newSweep, SweepSet: sweepChanged})
}

func (d *decision) dismissWake(b api.WakeDismiss) error {
	if d.t.Role != api.RoleManager || d.t.child() {
		return rejected("only a Manager thread dismisses its wakes")
	}
	live := d.t.Live()
	if live == nil {
		return rejected("no live run to dismiss")
	}
	if !b.Run.IsZero() && b.Run != live.ID {
		return rejected("that run is no longer live")
	}
	if live.Wake == api.NotWake {
		return rejected("only a wake a worker's report or a sweep started can be dismissed, not a turn the user asked for or joined")
	}
	if live.Dismissed {
		return nil
	}
	e := api.WakeDismissed{Run: live.ID, Reason: strings.TrimSpace(b.Reason), UpdatedAt: live.Prior}
	kept := d.t.Reported
	if live.Wake == api.WakeReport && !d.t.Reported.After(live.Woke) {
		e.Reports, e.Reported = true, d.t.Alerted
		kept = d.t.Alerted
	}
	if kept.After(e.UpdatedAt) {
		e.UpdatedAt = kept
	}
	return d.emit(e)
}

// runSweep starts a sweep the user asked for now, under the command's ID,
// whether or not sweeps are scheduled. A Manager a store kept settled from
// before comes back to the inbox with it.
func (d *decision) runSweep(id api.ID, b api.SweepRun) error {
	if d.t.Role != api.RoleManager || d.t.child() {
		return rejected("only a manager thread can run a sweep")
	}
	if d.t.Live() != nil {
		return rejected("the manager is already working")
	}
	if !d.runs() {
		return rejected("the thread may run nothing now")
	}
	if d.t.settled() {
		if err := d.emit(api.DispositionChanged{Disposition: api.Disposition{}}); err != nil {
			return err
		}
	}
	return d.startSweep(id, b.Prompt)
}

// raise makes a wake the user's turn: something in it now needs them.
func (d *decision) raise(r Run) error {
	if r.Wake == api.NotWake {
		return nil
	}
	return d.emit(api.WakeRaised{Run: r.ID})
}

func holds[T comparable](held *T, v T) bool { return held != nil && *held == v }
