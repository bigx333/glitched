package engine

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"reflect"
	"slices"
	"strings"
	"time"

	"glitched/internal/api"
)

// ErrRefused is an observation the tables refuse: it arrived after the
// thread moved on, so it records nothing.
var ErrRefused = errors.New("engine: observation refused")

// Observe records what an agent or the adapter did. An observation the
// tables refuse returns ErrRefused.
func Observe(t Thread, o Observation, now time.Time) ([]Event, []Effect, error) {
	return ObserveWith(t, o, Env{}, now)
}

// ObserveWith is Observe in env.
func ObserveWith(t Thread, o Observation, env Env, now time.Time) ([]Event, []Effect, error) {
	if !t.Exists() {
		return nil, nil, api.ErrNotFound
	}
	if t.Deleted {
		// What the agent did after, as an effect's leftover, records
		// nothing on a thread no one can read.
		return nil, nil, fmt.Errorf("%w: %T: the thread was deleted", ErrRefused, o)
	}
	d := &decision{t: t, thread: t.ID, at: now.UTC().Round(0), env: env}
	err := d.observe(o)
	if err == nil {
		err = d.settle(d.t.UpdatedAt)
	}
	if te := (*TransitionError)(nil); errors.As(err, &te) {
		return nil, nil, fmt.Errorf("%w: %T: %w", ErrRefused, o, err)
	}
	if err != nil {
		return nil, nil, err
	}
	return d.events, d.effects, nil
}

func (d *decision) observe(o Observation) error {
	live := d.t.Live()
	isLive := func(run api.ID) bool { return live != nil && live.ID == run }
	switch o := o.(type) {
	case Bound:
		// A run that is no longer the thread's binds nothing.
		if isLive(o.Run) && (o.Fork != "" || o.Session != "" && o.Session != d.t.Session) {
			return d.emit(api.SessionBound{Run: o.Run, Session: o.Session, Fork: o.Fork})
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
			// A run the user stopped holds the thread for no usage limit,
			// whatever ended it first.
			limit := o.Limit
			if stopped {
				limit = nil
			}
			// A quiet wake that failed is the user's to see.
			if o.Outcome == api.OutcomeFailed && !stopped && !silent && live.Wake != api.NotWake && live.Dismissed {
				if err := d.raise(*live); err != nil {
					return err
				}
			}
			if err := d.emit(api.TurnEnded{Run: o.Run, Outcome: o.Outcome, Error: o.Error, Tokens: o.Tokens, Limit: limit, Head: o.Head}); err != nil {
				return err
			}
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
	case Surfaced:
		dup := index(d.t.Requests, func(r Request) bool { return r.Run == o.Run && r.Dedup == o.Dedup }) >= 0
		if isLive(o.Run) && !dup && live.Silent {
			// Nobody watches a maintenance run to answer it, and the
			// user's next message waits behind it: it stops instead.
			return d.stop(*live)
		}
		if isLive(o.Run) && !dup {
			if err := d.raise(*live); err != nil {
				return err
			}
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
		if err := d.emit(api.RequestInvalidation{Request: o.Request, Reason: o.Error, Unconfirmed: o.Unconfirmed}); err != nil {
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
		return d.uncertain(api.TurnUncertain{Run: o.Run, Reason: api.UncertainRestarted, Continue: o.Continue && worked && !d.t.child()})
	case Rewound:
		return d.rewound()
	case Reported:
		return d.reported(o)
	case IdleCompact:
		return d.idleCompact(o)
	case SweepDue:
		return d.sweepDue(o)
	case WorktreeReady:
		return d.emit(api.WorktreeReady{Workspace: o.Workspace, Branch: o.Branch})
	case WorktreeFailed:
		if err := d.emit(api.WorktreeFailed{Error: o.Error}); err != nil {
			return err
		}
		return d.drop("Can't make a worktree: " + o.Error)
	case RemoteAttempted, RemoteBound, RemoteSeen, RemoteLost:
		return d.remote(o)
	}
	return nil
}

// remote records what the runtime saw of a placeholder's child on its
// machine. A first try at its create is refused once the placeholder is
// settled, so nothing reaches the machine after that; one tried before
// goes on. What the child reports reaches its parent as a local child's
// report does, once per key, whatever the machine's log replays. A lost
// child reports it failed, once: lost is where it ends.
func (d *decision) remote(o Observation) error {
	r := d.t.Remote
	if r == nil {
		return fmt.Errorf("%w: %T: the thread runs here", ErrRefused, o)
	}
	switch o := o.(type) {
	case RemoteAttempted:
		switch {
		case r.State != RemoteCreating || d.t.settled() && !r.Attempted:
			return fmt.Errorf("%w: the placeholder is settled, or its child no longer being created", ErrRefused)
		case r.Attempted:
			return nil
		}
		return d.emit(api.RemoteAttempted{Create: &o.Create})
	case RemoteBound:
		return d.emit(api.RemoteBound{Thread: o.Thread})
	case RemoteLost:
		if err := d.emit(api.RemoteLost{Reason: o.Reason}); err != nil {
			return err
		}
		d.remoteReport(o.On, RemoteReport{Run: lostRun(d.t.ID), Kind: api.ReportFailed, Error: o.Reason})
		return nil
	case RemoteSeen:
		if r.State != RemoteBoundState {
			return fmt.Errorf("%w: the placeholder isn't following its child", ErrRefused)
		}
		var keys []api.ID
		var reports []RemoteReport
		for _, p := range o.Reports {
			if !slices.Contains(r.Reported, p.Key) && !slices.Contains(keys, p.Key) {
				keys, reports = append(keys, p.Key), append(reports, p)
			}
		}
		if o.Cursor == r.Cursor && o.Run == r.Run && o.Silent == r.Silent && o.Slot == r.Slot && slices.Equal(o.Pending, r.Pending) && slices.Equal(o.Questions, r.Questions) && o.Taken == r.Taken && reflect.DeepEqual(o.Ended, r.Ended) && o.Model == r.Model && o.Access == r.Access && reflect.DeepEqual(o.Turn, r.Turn) && o.Held == r.Held && len(keys) == 0 {
			return nil
		}
		if err := d.emit(api.RemoteSeen{Cursor: o.Cursor, Run: o.Run, Silent: o.Silent, State: o.Slot, Pending: o.Pending, Questions: o.Questions, Taken: o.Taken, Ended: o.Ended, Model: o.Model, Access: o.Access, Turn: o.Turn, Held: o.Held, Keys: keys}); err != nil {
			return err
		}
		for _, p := range reports {
			d.remoteReport(o.On, p)
		}
	}
	return nil
}

// remoteReport has the placeholder report p to its parent, as report
// does for a local child, with what its machine, labelled on, said. What
// a child its machine's person took over asks is theirs, as report has it.
func (d *decision) remoteReport(on string, p RemoteReport) {
	if d.t.Remote.Taken {
		p.Question, p.Approval = api.ID{}, api.ID{}
	}
	r := ReportToParent{Parent: d.t.Parent, Child: d.t.ID, Run: p.Run, Kind: p.Kind, Title: d.t.Title, Error: p.Error, Ask: p.Ask, Question: p.Question, Approval: p.Approval, On: on}
	if p.Kind == api.ReportDone {
		r.Reply = &p.Reply
	}
	d.effects = append(d.effects, r)
}

// lostRun is the run a lost placeholder reports failed under: there may
// be no run of its child's to name, and it is lost once.
func lostRun(placeholder api.ID) api.ID {
	sum := sha256.Sum256(append([]byte("glitched remote lost "), placeholder[:]...))
	var id api.ID
	copy(id[:6], placeholder[:6])
	copy(id[6:], sum[:])
	return id
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
	if !d.runs() {
		return fmt.Errorf("%w: the thread may run nothing now", ErrRefused)
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

func (d *decision) sweepDue(o SweepDue) error {
	if at, ok := NextSweepAt(d.t, d.at); !ok || d.at.Before(at) {
		return fmt.Errorf("%w: thread is not due for a sweep", ErrRefused)
	}
	if o.Run.IsZero() {
		return fmt.Errorf("%w: sweep needs a run id", ErrRefused)
	}
	if !d.runs() {
		return fmt.Errorf("%w: the thread may run nothing now", ErrRefused)
	}
	return d.startSweep(o.Run, "")
}

// startSweep admits run as a sweep wake on the idle Manager, on prompt, or
// the thread's sweep prompt, or the default one. The wake keeps the
// thread's UpdatedAt from before it, for a dismissal to go back to.
func (d *decision) startSweep(run api.ID, prompt string) error {
	text := strings.TrimSpace(prompt)
	if text == "" && d.t.Sweep != nil {
		text = strings.TrimSpace(d.t.Sweep.Prompt)
	}
	if text == "" {
		text = api.DefaultSweepPrompt
	}
	prior := d.t.UpdatedAt
	if err := d.emit(api.MessageQueued{Message: run, Text: text, Mode: api.StartNow, Pos: d.t.pos(false), Auto: true, Sweep: true}); err != nil {
		return err
	}
	gen := d.t.Generation + 1
	if err := d.emit(api.TurnAdmitted{Run: run, Generation: gen, Wake: api.WakeSweep, Prior: prior, Quiet: d.t.Role == api.RoleManager}); err != nil {
		return err
	}
	if err := d.emit(api.SweepStarted{Run: run}); err != nil {
		return err
	}
	d.effects = append(d.effects, StartTurn{
		Run: run, Generation: gen, Text: text, Workspace: d.t.Workspace, Model: d.t.Model, Access: d.t.Access,
	})
	return nil
}

// reported logs a child's report. It brings a snoozed parent, and only its
// parent, back to the inbox. A done or failed one queues a wake, which
// gives the parent's agent the child's result with its next turn (see
// settle), and so do a question and an approval the child waits on, each
// with a wake of its own: the parent's agent answers a question or takes
// it to the user, and vets an approval, which only the user grants.
// Credentials wake nothing: a child asks its parent for them in its
// report. A settled parent stays settled and takes no wake: settling
// halted the tree, so this is a run that ended before its stop landed, and
// the user is done with the thread. The report is logged either way. A
// report the parent already took a wake for is a replay, and changes
// nothing.
func (d *decision) reported(o Reported) error {
	id, text := wakeID(o.Report.Run), wakeText(o, d.t.Branch)
	switch {
	case !o.Question.IsZero():
		id, text = askWakeID(o.Question), askText(o)
	case !o.Approval.IsZero():
		id, text = askWakeID(o.Approval), approvalText(o)
	}
	if d.t.messageAt(id) >= 0 {
		return nil
	}
	prior := d.t.UpdatedAt
	if err := d.emit(o.Report); err != nil {
		return err
	}
	wakes := !(o.Report.Kind == api.ReportBlocked && o.Question.IsZero() && o.Approval.IsZero() || d.t.settled())
	if d.t.Disposition.State == api.Snoozed {
		if err := d.emit(api.DispositionChanged{Disposition: api.Disposition{}}); err != nil {
			return err
		}
	}
	if !wakes {
		return nil
	}
	if err := d.emit(api.MessageQueued{
		Message: id, Text: text, Mode: api.AfterActive, Pos: d.t.pos(false), Held: true, Report: o.Report.Child,
	}); err != nil {
		return err
	}
	return d.settle(prior)
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

// askWakeID is the wake for a child's question or approval, one per
// request, apart from its run's: the run's own report still wakes the
// parent when it ends.
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
	answer := "The question is yours to triage first: answer it with task_answer, steer or stop the child with task_send or task_cancel, or ask the user yourself if only they can answer."
	if o.On != "" {
		answer = "It runs on " + o.On + ". " + answer
	}
	return fmt.Sprintf("<task id=\"%s\" title=\"%s\" state=\"waiting_on_question\" request=\"%s\">\n<task_question>\n%s</task_question>\n</task>\n%s",
		o.Report.Child, html.EscapeString(o.Report.Title), o.Question, b.String(), answer)
}

// approvalText gives the parent's agent an approval its child waits on, in
// the task tags askText uses: what the child wants to do (from the full
// request body, unclipped, including cwd, unsandboxed, reason and
// args/permissions), and that only the user grants it.
func approvalText(o Reported) string {
	var b struct {
		Operation   string `json:"operation"`
		Path        string `json:"path"`
		Command     string `json:"command"`
		Cwd         string `json:"cwd"`
		Reason      string `json:"reason"`
		Args        string `json:"args"`
		Unsandboxed bool   `json:"unsandboxed"`
	}
	var lines []string
	if json.Unmarshal(o.Ask, &b) == nil {
		switch {
		case strings.TrimSpace(b.Command) != "":
			lines = append(lines, "Wants to run "+strings.TrimSpace(b.Command))
		case strings.TrimSpace(b.Operation) != "":
			lines = append(lines, strings.TrimSpace("Wants to "+strings.TrimSpace(b.Operation)+" "+strings.TrimSpace(b.Path)))
		}
		if s := strings.TrimSpace(b.Cwd); s != "" {
			lines = append(lines, "Cwd: "+s)
		}
		if b.Unsandboxed {
			lines = append(lines, "Unsandboxed: true")
		}
		if s := strings.TrimSpace(b.Reason); s != "" {
			lines = append(lines, "Reason: "+s)
		}
		if s := strings.TrimSpace(b.Args); s != "" {
			lines = append(lines, "Details: "+s)
		}
	}
	what := strings.Join(lines, "\n")
	if what == "" {
		what = strings.TrimSpace(o.Report.Outcome)
	}
	if what == "" {
		what = "Waits on an approval"
	}
	note := "Only the user can grant it: task_answer refuses approvals. Stop a request that is bogus or off its task with task_cancel, or steer the child with task_send; if the work does need it, tell the user what to approve and why."
	if o.On != "" {
		note = "It runs on " + o.On + ". " + note
	}
	return fmt.Sprintf("<task id=\"%s\" title=\"%s\" state=\"waiting_on_approval\" request=\"%s\">\n<task_approval>\n%s\n</task_approval>\n</task>\n%s",
		o.Report.Child, html.EscapeString(o.Report.Title), o.Approval, what, note)
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
// reports nothing: they asked for it. The question or approval a child the
// user took over asks is theirs, so it wakes no parent.
func (d *decision) report(run api.ID, kind api.ReportKind, failure string, ask *api.Request) {
	if d.t.Parent.IsZero() {
		return
	}
	r := ReportToParent{Parent: d.t.Parent, Child: d.t.ID, Run: run, Kind: kind, Title: d.t.Title, Error: failure, Branch: d.t.Branch}
	if ask != nil {
		r.Ask = ask.Body
	}
	if ask != nil && !d.t.TakenOver {
		switch ask.Kind {
		case api.KindQuestion:
			r.Question = ask.ID
		case api.KindApproval:
			r.Approval = ask.ID
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
