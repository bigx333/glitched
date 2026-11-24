// Package engine is the pure core of a host: Decide turns a command and the
// thread's state into events and effects, and Apply folds an event into the
// state. Projections are Apply over the log; nothing else writes them.
package engine

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	"glitched/internal/api"
)

type RunState string

const (
	// RunIdle is the thread's run slot when no run is live; no run row holds it.
	RunIdle      RunState = "idle"
	RunStarting  RunState = "starting"
	RunRunning   RunState = "running"
	RunWaiting   RunState = "waiting"
	RunStopping  RunState = "stopping"
	RunUncertain RunState = "uncertain"
	RunFinished  RunState = "finished"
)

type QueueState string

const (
	QueueReady      QueueState = "ready"
	QueueHeld       QueueState = "held"
	QueueClaimed    QueueState = "claimed"
	QueueDispatched QueueState = "dispatched"
	QueueCancelled  QueueState = "cancelled"
	QueueUncertain  QueueState = "uncertain"
)

// Thread is the aggregate Decide reads and Apply folds. Runs, Queue and
// Requests only grow, and an element never changes index, so a store can
// diff two states by position.
type Thread struct {
	ID          api.ID
	Workspace   string
	Project     string // the folder a thread in a worktree came from; "" in its own
	Branch      string // the worktree's
	Title       string
	Model       api.ModelRef
	Access      api.Access
	Revision    uint64
	OwnerEpoch  uint64
	Generation  uint64 // of the latest run
	Disposition api.Disposition
	Deleted     bool // the tombstone: nothing reaches the thread again
	UpdatedAt   time.Time
	Parent      api.ID      // the thread that started this one; zero for the user's
	Tokens      int64       // what the thread's own turns used
	Context     api.Context // how full its context is; zero until an agent says
	Session     string      // the provider's id for the thread's conversation; "" until a run binds one
	Reported    time.Time   // when a child last reported back
	LastReport  *api.Report // never written through; its Cursor has no Log
	Confirmed   api.Confirmed
	Remote      *Remote // set on a remote child's placeholder, which runs nothing here
	Runs        []Run
	Queue       []Message
	Requests    []Request
}

type Run struct {
	ID         api.ID // the admitted message's ID
	Generation uint64
	State      RunState
	Signalled  bool        // a stop took: an interrupt reached the agent, or it ended stopping
	Outcome    api.Outcome // set when Finished
	Error      string
	Ended      time.Time // set when Finished
	// Model and Access are the thread's as the run was admitted: a
	// configure since changes the thread's, and leaves the run's.
	Model  api.ModelRef
	Access api.Access
	// Silent marks a background maintenance run, such as idle compaction,
	// that should not mark the thread unread, notify, or bump UpdatedAt.
	Silent bool
	// Limit is the usage limit that ended the run, and ResumeCancelled that
	// the user stopped the engine continuing it once the limit resets.
	Limit           *api.UsageLimit
	ResumeCancelled bool
	// Dropped marks a run a rewind took out of the thread's conversation:
	// it stays in the timeline, and the agent no longer has it.
	Dropped bool
}

// Remote is a placeholder's: the machine its child was placed on, as
// api.Remote, and where that child stands there, as far as this engine
// knows.
type Remote struct {
	Engine api.ID      `json:"engine"`
	Link   api.ID      `json:"link"`
	State  RemoteState `json:"state"`
}

type RemoteState string

// RemoteCreating is a placeholder whose child the machine hasn't
// confirmed yet.
const RemoteCreating RemoteState = "creating"

type Message struct {
	ID    api.ID
	Text  string
	Mode  api.DispatchMode
	Pos   int64
	State QueueState
	Run   api.ID // set once claimed
}

// waking is whether m is a wake that waits: a child's report the engine
// queued for the thread's next turn. A wake is held in AfterActive, which
// no client's message is: a client's held message is a Defer.
func (m Message) waking() bool { return m.State == QueueHeld && m.Mode == api.AfterActive }

// waiting is whether m is the user's and waits its turn: ready, or held
// by a stop or as a Defer. A wake is the engine's, and one claimed, sent
// or cancelled waits no more.
func (m Message) waiting() bool {
	return m.State == QueueReady || m.State == QueueHeld && m.Mode == api.Defer
}

type Request struct {
	ID         api.ID
	Run        api.ID
	Generation uint64
	Dedup      string
	Kind       string
	Body       json.RawMessage
	Answer     json.RawMessage
	State      api.RequestState
}

func (t Thread) Exists() bool { return !t.ID.IsZero() }

// Live is the run that holds the thread, or nil. At most one run is live and
// it is always the last.
func (t Thread) Live() *Run {
	if n := len(t.Runs); n > 0 && t.Runs[n-1].State != RunFinished {
		return &t.Runs[n-1]
	}
	return nil
}

// lastTurn is the thread's last finished run that isn't silent.
func (t Thread) lastTurn() (Run, bool) {
	for i := len(t.Runs) - 1; i >= 0; i-- {
		if r := t.Runs[i]; r.State == RunFinished && !r.Silent {
			return r, true
		}
	}
	return Run{}, false
}

func (t Thread) settled() bool { return t.Disposition.State == api.Settled }

// checkpointed is whether the thread keeps checkpoints of its files.
func (t Thread) checkpointed() bool { return t.Parent.IsZero() && t.Branch != "" }

// stopped is whether the thread's last run was stopped: by the user, or by
// the engine when it couldn't deliver an answer. A stop reached a run that
// completed as it landed too. A silent run since, such as an idle
// compaction, leaves it stopped: the user asked it nothing.
func (t Thread) stopped() bool {
	for i := len(t.Runs) - 1; i >= 0; i-- {
		if r := t.Runs[i]; !r.Silent {
			return r.State == RunFinished && (r.Signalled || r.Outcome == api.OutcomeInterrupted || r.Outcome == api.OutcomeAbandoned)
		}
	}
	return false
}

func (t Thread) slot() RunState {
	if r := t.Live(); r != nil {
		return r.State
	}
	return RunIdle
}

func (t Thread) runAt(id api.ID) int { return index(t.Runs, func(r Run) bool { return r.ID == id }) }

func (t Thread) silentRun(id api.ID) bool {
	i := t.runAt(id)
	return i >= 0 && t.Runs[i].Silent
}

func (t Thread) messageAt(id api.ID) int {
	return index(t.Queue, func(m Message) bool { return m.ID == id })
}

func (t Thread) requestAt(id api.ID) int {
	return index(t.Requests, func(r Request) bool { return r.ID == id })
}

func (t Thread) Message(id api.ID) (Message, bool) {
	if i := t.messageAt(id); i >= 0 {
		return t.Queue[i], true
	}
	return Message{}, false
}

func (t Thread) Request(id api.ID) (Request, bool) {
	if i := t.requestAt(id); i >= 0 {
		return t.Requests[i], true
	}
	return Request{}, false
}

func index[T any](s []T, match func(T) bool) int {
	for i, v := range s {
		if match(v) {
			return i
		}
	}
	return -1
}

// next is the ready message that starts when the thread is idle.
func (t Thread) next() *Message {
	var best *Message
	for i := range t.Queue {
		if m := &t.Queue[i]; m.State == QueueReady && (best == nil || m.Pos < best.Pos) {
			best = m
		}
	}
	return best
}

// line is the messages of the user's that wait their turn, in the order
// they take it.
func (t Thread) line() []Message {
	var line []Message
	for _, m := range t.Queue {
		if m.waiting() {
			line = append(line, m)
		}
	}
	slices.SortStableFunc(line, func(a, b Message) int { return cmp.Compare(a.Pos, b.Pos) })
	return line
}

// holding is whether a message of the user's is held: by a stop, or sent
// as a Defer.
func (t Thread) holding() bool {
	return index(t.Queue, func(m Message) bool { return m.State == QueueHeld && m.Mode == api.Defer }) >= 0
}

// resumable is whether a resume would start something: the thread is
// idle and holds a message of the user's, or wakes a stop held.
func (t Thread) resumable() bool {
	if t.Live() != nil || t.settled() || t.Deleted {
		return false
	}
	return t.holding() || t.stopped() && index(t.Queue, Message.waking) >= 0
}

func (t Thread) openRequests(run api.ID) bool {
	for _, r := range t.Requests {
		if r.Run == run && r.State == api.RequestPending {
			return true
		}
	}
	return false
}

var wireRun = map[RunState]api.RunState{
	RunIdle:      api.RunIdle,
	RunStarting:  api.RunStarting,
	RunRunning:   api.RunRunning,
	RunWaiting:   api.RunWaiting,
	RunStopping:  api.RunStopping,
	RunUncertain: api.RunUncertain,
}

// View is the projection clients read.
func View(t Thread) api.Thread {
	v := api.Thread{
		ID:          t.ID,
		Workspace:   t.Workspace,
		Project:     t.Project,
		Branch:      t.Branch,
		Title:       t.Title,
		Revision:    t.Revision,
		OwnerEpoch:  t.OwnerEpoch,
		Model:       t.Model,
		Access:      t.Access,
		Run:         wireRun[t.slot()],
		Disposition: t.Disposition,
		UpdatedAt:   t.UpdatedAt,
		Deleted:     t.Deleted,
		Parent:      t.Parent,
		Tokens:      t.Tokens,
		Reported:    t.Reported,
		Confirmed:   t.Confirmed,
		Resumable:   t.resumable(),
	}
	if t.Context != (api.Context{}) {
		c := t.Context
		v.Context = &c
	}
	if r := t.Live(); r != nil {
		v.Turn = &api.Turn{Run: r.ID, Model: r.Model, Access: r.Access, Silent: r.Silent}
	}
	if t.LastReport != nil {
		r := *t.LastReport
		v.LastReport = &r
	}
	if r, ok := t.lastTurn(); ok {
		v.LastTurn = &api.TurnResult{Run: r.ID, Model: r.Model, Access: r.Access, Outcome: r.Outcome, Ended: r.Ended, Limit: r.Limit, ResumeCancelled: r.ResumeCancelled}
	}
	for _, r := range t.Requests {
		if r.State == api.RequestPending {
			v.Requests = append(v.Requests, api.Request{
				ID: r.ID, Run: r.Run, Generation: r.Generation, Kind: r.Kind, Body: r.Body, State: r.State,
			})
		}
	}
	for _, m := range t.Queue {
		if m.State == QueueReady || m.State == QueueHeld {
			v.Queued++
		}
	}
	return v
}
