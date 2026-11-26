package client

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"glitched/internal/api"
)

// Transcript is a thread's conversation as its events tell it. Entries are
// in the order things happened. Queue holds the messages still waiting to
// run or to be steered in, in the order they will. Each joins Entries once
// it runs, is steered in or is given up, so a queued message never splits
// the turn it waits behind.
type Transcript struct {
	Entries  []Entry
	Queue    []*UserEntry
	Cursor   api.Cursor           // the last event folded in
	Ended    time.Time            // when the last turn folded in ended
	Reported time.Time            // when the last child's report folded in came
	Deleted  bool                 // the thread's tombstone is folded in; nothing follows it
	starting api.ID               // the run admitted and not yet started, stopped or ended
	began    map[api.ID]time.Time // when each run not yet ended started
	wakes    map[api.ID]*wake
	silent   map[api.ID]*CompactionEntry // silent idle-compaction runs, by run id
	dropped  map[api.ID]bool
	// edits are the indexes of Entries changed in place, in the order
	// they changed, but for the first editsBase, dropped: Edits reads
	// them. An entry changed again before a reader was told of its last
	// change is noted once.
	edits                []int
	editsBase, editsRead int
	// folds counts the snapshots that replaced the transcript, each of
	// which starts its edits again.
	folds int
	// Plan is the plan the thread's agent works to, the latest one said,
	// done or not: nil before any and once the agent cleared it.
	Plan *PlanEntry
}

// ForkBlocked reports whether a legacy rewind removed the run from the conversation.
func (t *Transcript) ForkBlocked(run api.ID) bool { return t.dropped[run] }

// Run is the run that claimed the message, zero before one has.
func (u *UserEntry) Run() api.ID { return u.run }

// An EditMark is where a reader of a transcript's edits got to.
type EditMark struct{ folds, n int }

// Replace replaces the transcript with with, as folding a snapshot does:
// its readers are told all changed.
func (t *Transcript) Replace(with Transcript) {
	folds := t.folds
	*t = with
	t.folds = folds + 1
}

// editsKept is how many edits a transcript keeps: a reader further
// behind is told all changed.
const editsKept = 4096

// Edits is what changed in place among Entries since a reader's mark: the
// indexes of the entries changed, all when the reader is too far behind to
// say, and the mark to pass next time. The indexes are the transcript's,
// to read before it changes again. An entry appended is not an edit; a
// snapshot replacing the transcript changes all.
func (t *Transcript) Edits(mark EditMark) (edited []int, all bool, next EditMark) {
	next = EditMark{t.folds, t.editsBase + len(t.edits)}
	t.editsRead = next.n
	if mark.folds != t.folds || mark.n < t.editsBase || mark.n > next.n {
		return nil, true, next
	}
	return t.edits[mark.n-t.editsBase:], false, next
}

// edited notes that e, one of Entries, changed in place. It looks from the
// end, where events change entries.
func (t *Transcript) edited(e Entry) {
	for i := len(t.Entries) - 1; i >= 0; i-- {
		if t.Entries[i] != e {
			continue
		}
		n := len(t.edits)
		if n > 0 && t.edits[n-1] == i && t.editsBase+n > t.editsRead {
			return
		}
		if n == editsKept {
			t.editsBase, t.edits = t.editsBase+n, t.edits[:0]
		}
		t.edits = append(t.edits, i)
		return
	}
}

// wake is a message the engine queued with a child's report, by its ID:
// the report it carries, and the run that claimed it.
type wake struct {
	report *ReportEntry
	run    api.ID
}

// Entry is sealed: *UserEntry, *AssistantEntry, *WorkEntry, *PlanEntry,
// *RequestEntry, *ReportEntry, *ErrorEntry or *CompactionEntry.
type Entry interface{ entry() }

type MessageState uint8

const (
	Queued MessageState = iota
	Held                // held by a stop, or deferred, until the user sends or resumes
	// Claimed: admitted as its run's message, or bound to a live run that
	// it will be steered into.
	Claimed
	Sent
	Cancelled
	Uncertain // handing it to the agent was cut off
)

type UserEntry struct {
	Message api.ID
	// Seq is where the log queued it, as search's hits say.
	Seq    uint64
	Text   string
	State  MessageState
	Reason string // why it was cancelled, when the engine said
	// At is when it was sent.
	At time.Time
	// End is how the message's own run ended, when it ended saying
	// nothing; the run's last reply holds it otherwise.
	End *TurnEnd
	pos int64
	run api.ID // the run that claimed it
}

type AssistantEntry struct {
	Run api.ID
	// Seq is where the log has its first words, as search's hits say.
	Seq       uint64
	Text      string
	Streaming bool      // more text may follow
	At        time.Time // when its first words came
	// End is how the run ended, on its last reply that says something;
	// nil while it runs.
	End  *TurnEnd
	text strings.Builder
}

// TurnEnd is how a run ended: its outcome, when, how long it ran, zero
// when the transcript didn't see it start, and whether it answered a
// message of the user's, which asking again would send again.
type TurnEnd struct {
	Outcome api.Outcome
	At      time.Time
	Took    time.Duration
	Asked   bool
}

// write appends s. Text shares the builder's bytes, so a long run's deltas
// cost what they hold rather than a copy of all the text before each.
func (a *AssistantEntry) write(s string) {
	a.text.WriteString(s)
	a.Text = a.text.String()
}

// WorkEntry is a run's tool calls with no text between them. Ended is zero
// while one of its items is still going and the run is live.
type WorkEntry struct {
	Run     api.ID
	Items   []WorkStep
	Started time.Time
	Ended   time.Time
}

// WorkStep is one of a run's steps. Started is when the transcript first
// saw it and Ended when it last left WorkStarted; Exit and Output are a
// command's, when the provider says.
type WorkStep struct {
	Item    string
	Kind    string
	Title   string
	Status  api.WorkStatus
	Path    string
	Exit    *int
	Output  string
	Started time.Time
	Ended   time.Time
	// Subagent is the native subagent the step started, with what it did,
	// nil for a step that started none.
	Subagent *Subagent
}

// Subagent is a native subagent's transcript (see api.SubagentDelta):
// what it said and the steps it took, in order, and how it ended.
type Subagent struct {
	Parts   []SubagentPart
	Ended   bool
	Outcome api.Outcome
	Tokens  int64
	// Cut is that its run ended before saying how the subagent did: it
	// is over, how is unknown.
	Cut bool
	// Rev counts its changes.
	Rev int
}

// SubagentPart is a native subagent's text, or one of its steps.
type SubagentPart struct {
	Text string
	Step *WorkStep
}

// Steps counts the subagent's steps.
func (s *Subagent) Steps() int {
	n := 0
	for _, p := range s.Parts {
		if p.Step != nil {
			n++
		}
	}
	return n
}

// PlanEntry is a run's plan, where the run first said one, as it last
// said it: each of its plans replaces the one before.
type PlanEntry struct {
	Run   api.ID
	Steps []api.PlanStep
}

// Done counts the plan's steps done.
func (p *PlanEntry) Done() int {
	n := 0
	for _, s := range p.Steps {
		if s.Status == api.PlanDone {
			n++
		}
	}
	return n
}

type RequestEntry struct {
	Run        api.ID
	Request    api.ID
	Generation uint64
	Kind       string // approval | question
	Body       json.RawMessage
	State      api.RequestState
	Answer     json.RawMessage
	Reason     string    // why it was invalidated, when the engine said
	Opened     time.Time // when the agent asked
}

// ReportEntry is a child thread's report: its run done, failed or blocked
// on the user, and what came of it. Wake is how the report went to the
// parent's agent.
type ReportEntry struct {
	api.ChildReported
	At   time.Time
	Wake WakeState
}

type WakeState uint8

const (
	NoWake      WakeState = iota // a blocked report, or one to a settled parent
	WakeWaiting                  // it goes in with the parent's next turn
	WakeSent
	WakeLost // cancelled, or cut off on its way
)

// ErrorEntry is why a run failed, as the engine or its agent said: that
// Claude Code is not installed or not signed in, say.
type ErrorEntry struct {
	Run   api.ID
	Error string
}

// CompactionEntry represents a quiet background idle-compaction run as one
// divider row instead of a user /compact bubble and reply.
type CompactionEntry struct {
	Run        api.ID
	Running    bool
	Compacted  bool
	PreTokens  int64
	PostTokens int64
	At         time.Time
}

// ForkEntry is where a fork goes on from, the first of its transcript.
// Took is how its first run took the conversation up, once one has.
type ForkEntry struct {
	api.Forked
	At   time.Time
	Took api.ForkKind
}

func (*ForkEntry) entry()       {}
func (*ErrorEntry) entry()      {}
func (*CompactionEntry) entry() {}
func (*ReportEntry) entry()     {}
func (*UserEntry) entry()       {}
func (*AssistantEntry) entry()  {}
func (*WorkEntry) entry()       {}
func (*PlanEntry) entry()       {}
func (*RequestEntry) entry()    {}

// fold builds a transcript from a thread snapshot, which holds every event.
func fold(snap api.Snapshot) Transcript {
	var t Transcript
	for _, e := range snap.Events {
		t.apply(e)
	}
	t.Cursor = snap.Cursor
	return t
}

// Apply folds one event in, as following the thread does.
func (t *Transcript) Apply(e api.Event) { t.apply(e) }

// apply folds one event in, mirroring how the engine's projection reads
// it. An event at or before the cursor is already in. One this client can't
// decode is skipped, and the cursor still moves past it.
func (t *Transcript) apply(e api.Event) {
	if e.Cursor.Log == t.Cursor.Log && e.Cursor.Seq <= t.Cursor.Seq {
		return
	}
	t.Cursor = e.Cursor
	if e.Type == "thread.reverted" {
		var v struct {
			Conversation string   `json:"conversation"`
			Dropped      []api.ID `json:"dropped"`
			Restored     []api.ID `json:"restored"`
		}
		if json.Unmarshal(e.Payload, &v) == nil && (v.Conversation == "native" || v.Conversation == "portable") {
			for _, run := range v.Dropped {
				if t.dropped == nil {
					t.dropped = map[api.ID]bool{}
				}
				t.dropped[run] = true
			}
			for _, run := range v.Restored {
				delete(t.dropped, run)
			}
		}
		return
	}
	body, err := e.Decode()
	if err != nil {
		return
	}
	switch b := body.(type) {
	case api.ThreadCreated:
		if b.Fork != nil {
			t.add(&ForkEntry{Forked: *b.Fork, At: e.At})
		}
	case api.MessageQueued:
		if b.Silent {
			break
		}
		if !b.Report.IsZero() {
			t.queueWake(b)
			break
		}
		state := Queued
		if b.Held {
			state = Held
		}
		t.enqueue(&UserEntry{Message: b.Message, Seq: e.Cursor.Seq, Text: b.Text, State: state, At: e.At, pos: b.Pos})
	case api.MessageClaimed:
		if i := t.waiting(b.Message); i >= 0 {
			t.Queue[i].State, t.Queue[i].run = Claimed, b.Run
		}
		if w := t.wakes[b.Message]; w != nil {
			w.run = b.Run
		}
	case api.TurnAdmitted:
		if b.Silent {
			ce := &CompactionEntry{Run: b.Run, Running: true, At: e.At}
			if t.silent == nil {
				t.silent = map[api.ID]*CompactionEntry{}
			}
			t.silent[b.Run] = ce
			t.add(ce)
			t.starting = b.Run
			break
		}
		if w := t.wakes[b.Run]; w != nil {
			w.run = b.Run
		}
		if i := t.waiting(b.Run); i >= 0 {
			u := t.Queue[i]
			u.State, u.run = Claimed, b.Run
			t.join(i)
		}
		t.starting = b.Run
	case api.TurnStarted:
		t.started(b.Run)
		if t.began == nil {
			t.began = map[api.ID]time.Time{}
		}
		t.began[b.Run] = e.At
	case api.TurnRestored:
		// A run restored past starting is unchanged.
		if b.Run == t.starting {
			t.started(b.Run)
		}
	case api.MessageDispatched:
		t.settle(b.Message, Sent, "")
	case api.MessageCancelled:
		t.settle(b.Message, Cancelled, b.Reason)
	case api.MessageUncertain:
		t.settle(b.Message, Uncertain, "")
	case api.MessageHeld:
		if i := t.waiting(b.Message); i >= 0 {
			t.Queue[i].State = Held
		}
	case api.MessageReleased:
		if i := t.waiting(b.Message); i >= 0 {
			t.Queue[i].State = Queued
		}
	case api.MessageEdited:
		if i := t.waiting(b.Message); i >= 0 {
			t.Queue[i].Text = b.Text
		}
	case api.MessageMoved:
		if i := t.waiting(b.Message); i >= 0 {
			u := t.Queue[i]
			u.pos = b.Pos
			t.Queue = slices.Delete(t.Queue, i, i+1)
			t.enqueue(u)
		}
	case api.ContextReported:
		if ce := t.silent[b.Run]; ce != nil && b.Compacted {
			ce.Compacted = true
			if b.PreTokens > 0 {
				ce.PreTokens = b.PreTokens
			}
			if b.PostTokens > 0 {
				ce.PostTokens = b.PostTokens
			}
			t.edited(ce)
		}
	case api.AssistantDelta:
		if t.silent[b.Run] != nil {
			break
		}
		a, ok := t.last().(*AssistantEntry)
		if !ok || a.Run != b.Run {
			a = &AssistantEntry{Run: b.Run, Seq: e.Cursor.Seq, Streaming: true, At: e.At}
			t.add(a)
		}
		a.write(b.Text)
		t.edited(a)
	case api.WorkItem:
		if t.silent[b.Run] != nil {
			break
		}
		t.work(b, e.At)
	case api.PlanUpdated:
		if t.silent[b.Run] != nil {
			break
		}
		t.plan(b)
	case api.SubagentDelta:
		if s, we := t.subagent(b.Run, b.Parent); s != nil {
			if n := len(s.Parts); n > 0 && s.Parts[n-1].Step == nil {
				s.Parts[n-1].Text += b.Text
			} else {
				s.Parts = append(s.Parts, SubagentPart{Text: b.Text})
			}
			s.Rev++
			t.edited(we)
		}
	case api.SubagentWork:
		if s, we := t.subagent(b.Run, b.Parent); s != nil {
			step := WorkStep{Item: b.Item, Kind: b.Kind, Title: b.Title, Status: b.Status, Path: b.Path, Exit: b.Exit, Output: b.Output, Started: e.At}
			if b.Status != api.WorkStarted {
				step.Ended = e.At
			}
			i := slices.IndexFunc(s.Parts, func(p SubagentPart) bool { return p.Step != nil && p.Step.Item == b.Item })
			if i < 0 {
				s.Parts = append(s.Parts, SubagentPart{Step: &step})
			} else {
				was := s.Parts[i].Step
				step.Started = was.Started
				step.Kind, step.Title, step.Path, step.Output = cmp.Or(step.Kind, was.Kind), cmp.Or(step.Title, was.Title), cmp.Or(step.Path, was.Path), cmp.Or(step.Output, was.Output)
				if step.Exit == nil {
					step.Exit = was.Exit
				}
				s.Parts[i].Step = &step
			}
			s.Rev++
			t.edited(we)
		}
	case api.SubagentEnded:
		if s, we := t.subagent(b.Run, b.Parent); s != nil {
			s.Ended, s.Outcome, s.Tokens = true, b.Outcome, b.Tokens
			s.Rev++
			t.edited(we)
		}
	case api.SessionBound:
		if b.Fork == "" {
			break
		}
		for i := len(t.Entries) - 1; i >= 0; i-- {
			if f, ok := t.Entries[i].(*ForkEntry); ok {
				f.Took = b.Fork
				t.edited(f)
				break
			}
		}
	case api.RequestOpened:
		if t.silent[b.Run] != nil {
			break
		}
		t.add(&RequestEntry{Run: b.Run, Request: b.Request, Generation: b.Generation, Kind: b.Kind, Body: b.Body, State: api.RequestPending, Opened: e.At})
	case api.RequestAnswer:
		if r := t.request(b.Request); r != nil {
			r.State, r.Answer = api.RequestAnswered, b.Answer
			t.edited(r)
		}
	case api.RequestDelivery:
		if r := t.request(b.Request); r != nil {
			r.State = api.RequestDelivered
			t.edited(r)
		}
	case api.RequestInvalidation:
		if r := t.request(b.Request); r != nil {
			r.State, r.Reason = api.RequestInvalidated, b.Reason
			t.edited(r)
		}
	case api.TurnStopping:
		t.starting = api.ID{}
		t.void(b.Run)
	case api.TurnUncertain:
		t.end(b.Run, Uncertain, e.At)
		if ce := t.silent[b.Run]; ce != nil {
			ce.Running = false
			t.edited(ce)
		}
	case api.TurnEnded:
		t.end(b.Run, Cancelled, e.At)
		if ce := t.silent[b.Run]; ce != nil {
			ce.Running = false
			delete(t.began, b.Run)
			t.edited(ce)
			break
		}
		t.Ended = e.At
		end := TurnEnd{Outcome: b.Outcome, At: e.At}
		if began, ok := t.began[b.Run]; ok {
			end.Took = e.At.Sub(began)
			delete(t.began, b.Run)
		}
		t.ended(b.Run, end)
		if b.Outcome == api.OutcomeFailed && b.Error != "" {
			t.add(&ErrorEntry{Run: b.Run, Error: b.Error})
		}
	case api.ThreadDeleted:
		t.Deleted = true
	case api.ChildReported:
		t.add(&ReportEntry{ChildReported: b, At: e.At})
		t.Reported = e.At
	}
}

// started marks the run's own message sent.
func (t *Transcript) started(run api.ID) {
	t.starting = api.ID{}
	t.settle(run, Sent, "")
}

func (t *Transcript) last() Entry {
	if len(t.Entries) == 0 {
		return nil
	}
	return t.Entries[len(t.Entries)-1]
}

// add appends e. The text before e is done streaming.
func (t *Transcript) add(e Entry) {
	if a, ok := t.last().(*AssistantEntry); ok && a.Streaming {
		a.Streaming = false
		t.edited(a)
	}
	t.Entries = append(t.Entries, e)
}

func (t *Transcript) waiting(message api.ID) int {
	return slices.IndexFunc(t.Queue, func(u *UserEntry) bool { return u.Message == message })
}

// enqueue puts u in Queue in its place: after those at its position or
// before it.
func (t *Transcript) enqueue(u *UserEntry) {
	i := slices.IndexFunc(t.Queue, func(q *UserEntry) bool { return q.pos > u.pos })
	if i < 0 {
		i = len(t.Queue)
	}
	t.Queue = slices.Insert(t.Queue, i, u)
}

// join moves Queue[i] into the conversation.
func (t *Transcript) join(i int) {
	u := t.Queue[i]
	t.Queue = slices.Delete(t.Queue, i, i+1)
	t.add(u)
}

// queueWake marks the last report of q's child as going to the agent.
func (t *Transcript) queueWake(q api.MessageQueued) {
	for i := len(t.Entries) - 1; i >= 0; i-- {
		if r, ok := t.Entries[i].(*ReportEntry); ok && r.Child == q.Report {
			r.Wake = WakeWaiting
			t.edited(r)
			if t.wakes == nil {
				t.wakes = map[api.ID]*wake{}
			}
			t.wakes[q.Message] = &wake{report: r}
			return
		}
	}
}

// settle gives a message a state past waiting, moving it into the
// conversation if it was still queued.
func (t *Transcript) settle(message api.ID, state MessageState, reason string) {
	if w := t.wakes[message]; w != nil {
		w.report.Wake = WakeLost
		if state == Sent {
			w.report.Wake = WakeSent
		}
		t.edited(w.report)
		delete(t.wakes, message)
		return
	}
	if i := t.waiting(message); i >= 0 {
		t.Queue[i].State, t.Queue[i].Reason = state, reason
		t.join(i)
		return
	}
	if u := t.message(message); u != nil {
		u.State, u.Reason = state, reason
		t.edited(u)
	}
}

// message finds a user entry wherever it is.
func (t *Transcript) message(id api.ID) *UserEntry {
	if i := t.waiting(id); i >= 0 {
		return t.Queue[i]
	}
	for i := len(t.Entries) - 1; i >= 0; i-- {
		if u, ok := t.Entries[i].(*UserEntry); ok && u.Message == id {
			return u
		}
	}
	return nil
}

func (t *Transcript) request(id api.ID) *RequestEntry {
	for i := len(t.Entries) - 1; i >= 0; i-- {
		if r, ok := t.Entries[i].(*RequestEntry); ok && r.Request == id {
			return r
		}
	}
	return nil
}

// since returns the entries after run's own message, the only place its
// output can be.
func (t *Transcript) since(run api.ID) []Entry {
	for i := len(t.Entries) - 1; i >= 0; i-- {
		if u, ok := t.Entries[i].(*UserEntry); ok && u.Message == run {
			return t.Entries[i+1:]
		}
	}
	return t.Entries
}

// work upserts an item: in the entry that already holds it, else in the
// newest entry when that is the run's work, else in a new one. A field the
// later event leaves out keeps what the earlier one said.
func (t *Transcript) work(w api.WorkItem, at time.Time) {
	step := WorkStep{Item: w.Item, Kind: w.Kind, Title: w.Title, Status: w.Status, Path: w.Path, Exit: w.Exit, Output: w.Output, Started: at}
	if w.Status != api.WorkStarted {
		step.Ended = at
	}
	var into *WorkEntry
	for _, e := range t.since(w.Run) {
		if we, ok := e.(*WorkEntry); ok && we.Run == w.Run {
			if i := slices.IndexFunc(we.Items, func(s WorkStep) bool { return s.Item == w.Item }); i >= 0 {
				into = we
				was := we.Items[i]
				step.Started = was.Started
				step.Kind, step.Title, step.Path, step.Output = cmp.Or(step.Kind, was.Kind), cmp.Or(step.Title, was.Title), cmp.Or(step.Path, was.Path), cmp.Or(step.Output, was.Output)
				if step.Exit == nil {
					step.Exit = was.Exit
				}
				step.Subagent = was.Subagent
				we.Items[i] = step
				t.edited(we)
				break
			}
		}
	}
	if into == nil {
		if we, ok := t.last().(*WorkEntry); ok && we.Run == w.Run {
			into = we
		} else {
			into = &WorkEntry{Run: w.Run, Started: at}
			t.add(into)
		}
		into.Items = append(into.Items, step)
	}
	into.Ended = time.Time{}
	if !slices.ContainsFunc(into.Items, func(s WorkStep) bool { return s.Status == api.WorkStarted }) {
		into.Ended = at
	}
	t.edited(into)
}

// subagent is the native subagent run's step parent started, made when it
// is first told of, and the work entry that holds the step; nil for a step
// the transcript doesn't hold, or a silent run's.
func (t *Transcript) subagent(run api.ID, parent string) (*Subagent, *WorkEntry) {
	if t.silent[run] != nil {
		return nil, nil
	}
	for _, e := range t.since(run) {
		we, ok := e.(*WorkEntry)
		if !ok || we.Run != run {
			continue
		}
		if i := slices.IndexFunc(we.Items, func(s WorkStep) bool { return s.Item == parent }); i >= 0 {
			if we.Items[i].Subagent == nil {
				we.Items[i].Subagent = &Subagent{}
			}
			return we.Items[i].Subagent, we
		}
	}
	return nil, nil
}

// plan replaces the run's plan in the entry that holds it, else adds one,
// and makes it the thread's. A plan with no steps clears the thread's and
// leaves the run's entry as it last said.
func (t *Transcript) plan(p api.PlanUpdated) {
	if len(p.Steps) == 0 {
		t.Plan = nil
		return
	}
	for _, e := range t.since(p.Run) {
		if pe, ok := e.(*PlanEntry); ok && pe.Run == p.Run {
			pe.Steps = p.Steps
			t.Plan = pe
			t.edited(pe)
			return
		}
	}
	t.Plan = &PlanEntry{Run: p.Run, Steps: p.Steps}
	t.add(t.Plan)
}

// ended keeps how run ended on its last reply that says something, or on
// its own message when it said nothing. A run with no message of its own,
// as a child's report waking the agent, keeps it on its last reply alone,
// not asked.
func (t *Transcript) ended(run api.ID, end TurnEnd) {
	u := t.message(run)
	end.Asked = u != nil
	since := t.since(run)
	for i := len(since) - 1; i >= 0; i-- {
		if a, ok := since[i].(*AssistantEntry); ok && a.Run == run && strings.TrimSpace(a.Text) != "" {
			a.End = &end
			t.edited(a)
			return
		}
	}
	if u != nil {
		u.End = &end
		t.edited(u)
	}
}

// void invalidates the run's open requests, as the engine does once the run
// stops.
func (t *Transcript) void(run api.ID) {
	for _, e := range t.since(run) {
		if r, ok := e.(*RequestEntry); ok && r.Run == run && r.State == api.RequestPending {
			r.State = api.RequestInvalidated
			t.edited(r)
		}
	}
}

// end closes out a run. The messages it claimed and never delivered get
// released, its open requests are void, and its text and work are over.
func (t *Transcript) end(run api.ID, released MessageState, at time.Time) {
	t.starting = api.ID{}
	for i := 0; i < len(t.Queue); {
		if u := t.Queue[i]; u.run == run && u.State == Claimed {
			u.State = released
			t.join(i)
			continue
		}
		i++
	}
	if u := t.message(run); u != nil && u.State == Claimed {
		u.State = released
		t.edited(u)
	}
	for id, w := range t.wakes {
		if w.run == run {
			t.settle(id, released, "")
		}
	}
	t.void(run)
	for _, e := range t.since(run) {
		switch e := e.(type) {
		case *AssistantEntry:
			if e.Run == run && e.Streaming {
				e.Streaming = false
				t.edited(e)
			}
		case *WorkEntry:
			if e.Run != run {
				continue
			}
			if e.Ended.IsZero() {
				e.Ended = at
				t.edited(e)
			}
			for _, w := range e.Items {
				if s := w.Subagent; s != nil && !s.Ended && !s.Cut {
					s.Cut = true
					s.Rev++
					t.edited(e)
				}
			}
		}
	}
}
