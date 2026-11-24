package engine

import (
	"encoding/json"
	"fmt"
	"time"

	"glitched/internal/api"
)

// Effect is sealed: the kinds below are what the runtime performs after a
// commit. They sit in the store's outbox, committed with the events.
type Effect interface{ effectKind() string }

type StartTurn struct {
	Run        api.ID
	Generation uint64
	Text       string
	Workspace  string
	Model      api.ModelRef
	Access     api.Access
	ResumeOnly bool `json:"resumeOnly,omitempty"`
}

type SteerTurn struct {
	Run, Message api.ID
	Text         string
}

type InterruptTurn struct{ Run api.ID }

// CloseAgent closes the agent of a run that may still be running: a deleted
// thread's, or one boot could not reconcile. It sits in the outbox until
// done, so a crash or a shutdown before the close leaves it for the next
// host, and the thread's later effects wait for it.
type CloseAgent struct {
	Run        api.ID
	Generation uint64
}

type DeliverAnswer struct {
	Run, Request api.ID
	Dedup        string
	Answer       json.RawMessage
	// Retried: an engine before this one took it and stopped before it
	// finished, so the agent may have the answer. Set only by recovery.
	Retried bool `json:",omitempty"`
}

// ReportToParent posts a child's report in its parent's log: Title is the
// child's, Kind how its run went; Error is a failed run's, Ask the body of
// the request a blocked one waits on, and Question that request when it
// is a question, which the parent's agent may answer. Branch is the
// child's, where it works in a worktree.
//
// A remote child's report carries what its machine said: Reply is what a
// done run said last, which its log here doesn't hold, and On the label
// of the machine, where the user answers what it asks.
type ReportToParent struct {
	Parent, Child, Run api.ID
	Kind               api.ReportKind
	Title, Error       string
	Ask                json.RawMessage
	Question           api.ID  `json:",omitzero"`
	Branch             string  `json:",omitempty"`
	Reply              *string `json:",omitempty"`
	On                 string  `json:",omitempty"`
}

// RemoteCreate creates a placeholder's child on its machine, Engine
// through Link. Command is the machine's whole command, frozen as the
// placeholder was made, so every try sends the same bytes and gets the
// receipt the first one took.
type RemoteCreate struct {
	Engine, Link api.ID
	Command      api.Command
}

// RemoveWorktree removes the worktree a deleted thread ran in, on Branch
// of Project's repository, with what is in it, when it is still the one
// Glitched made for the thread. It sits in the outbox until done, so a
// crash after the delete's commit leaves it for the next host; a thread
// that ran in another's worktree finds it isn't its own and leaves it.
type RemoveWorktree struct {
	Workspace, Project, Branch string
}

// CaptureCheckpoint saves the files of the thread's worktree in the engine's
// checkpoint store: as Run left them, or, in the base phase, before Run,
// the thread's first, starts. It sits before the thread's next start, so
// that turn starts on the files saved.
type CaptureCheckpoint struct {
	Run   api.ID
	Phase api.CheckpointPhase
}

// DropCheckpoints deletes a deleted thread's checkpoints.
type DropCheckpoints struct{}

// RevertThread puts the files of the thread's worktree back as To holds
// them, after a recovery checkpoint of them as they are, which a replay
// finds and never takes again; unless KeepFiles. Conversation rewinds the
// thread's conversation to To's run first, and a rewind that fails
// leaves the files as they are. Dropped is the runs after To the
// conversation holds as the revert is decided, oldest first.
type RevertThread struct {
	Revert       api.ID
	To           api.CheckpointRef
	Conversation bool
	Dropped      []api.ID
	KeepFiles    bool
}

// MakeWorktree makes a link's thread's worktree, of the checkout
// Project, from Base, a commit, on a new branch named by Title. Git
// writes the worktree locked, the thread named in the lock's reason, so a
// replay finds one it made and finishes it.
type MakeWorktree struct {
	Project, Base, Title string
}

func (MakeWorktree) effectKind() string      { return "worktree.make" }
func (RemoteCreate) effectKind() string      { return "remote.create" }
func (CaptureCheckpoint) effectKind() string { return "checkpoint.capture" }
func (DropCheckpoints) effectKind() string   { return "checkpoint.drop" }
func (RevertThread) effectKind() string      { return "thread.revert" }

func (StartTurn) effectKind() string      { return "turn.start" }
func (RemoveWorktree) effectKind() string { return "worktree.remove" }
func (ReportToParent) effectKind() string { return "child.report" }
func (SteerTurn) effectKind() string      { return "turn.steer" }
func (InterruptTurn) effectKind() string  { return "turn.interrupt" }
func (CloseAgent) effectKind() string     { return "agent.close" }
func (DeliverAnswer) effectKind() string  { return "request.deliver" }

func EffectKind(e Effect) string { return e.effectKind() }

// Due reports whether e still has something to do on t. An effect that is
// not due completes without reaching the agent, recording skip when it is
// not nil: a steer or an answer the run can no longer take fails.
func Due(t Thread, e Effect) (due bool, skip Observation) {
	r := t.Live()
	takes := func(run api.ID) bool { return r != nil && r.ID == run && steerable[r.State] }
	switch e := e.(type) {
	case StartTurn:
		return r != nil && r.ID == e.Run && r.State == RunStarting, nil
	case SteerTurn:
		if m, _ := t.Message(e.Message); m.State != QueueClaimed {
			return false, nil
		}
		if !takes(e.Run) {
			return false, SteerFailed{Message: e.Message, Error: "the run can no longer be steered"}
		}
		return true, nil
	case InterruptTurn:
		return r != nil && r.ID == e.Run && r.State == RunStopping, nil
	case CloseAgent, RemoveWorktree, DropCheckpoints:
		return true, nil
	case CaptureCheckpoint, RevertThread:
		return !t.Deleted, nil
	case MakeWorktree:
		// On a thread settled first, the host makes no worktree, and
		// finishes one a host before it began.
		return t.Origin != nil && t.Origin.Making && !t.Deleted, nil
	case RemoteCreate:
		// A placeholder settled before any try has nothing there; one
		// tried may, so its create goes on until the machine answers.
		r := t.Remote
		return r != nil && r.State == RemoteCreating && !t.Deleted && (r.Attempted || !t.settled()), nil
	case ReportToParent:
		// The parent decides, as it observes the report, whether it can
		// still take it.
		return true, nil
	case DeliverAnswer:
		if q, _ := t.Request(e.Request); q.State != api.RequestAnswered {
			return false, nil
		}
		if !takes(e.Run) {
			return false, DeliveryFailed{Request: e.Request, Error: "the run can no longer take an answer"}
		}
		return true, nil
	}
	return false, nil
}

// replaySafe kinds run again when a crash left them leased. The rest may
// already have reached the agent, so boot reconciles them instead. Adapters
// treat a repeated interrupt or a repeated answer as a no-op.
// A report commits with its completion, so one left leased never landed.
// Closing an agent that is gone does nothing, nor does removing a
// worktree that is. A capture whose ref is there reports what that ref
// holds and writes nothing, and a drop of refs that are gone deletes
// nothing but what a gc it left pending prunes. A revert finds the
// recovery checkpoint it took, and puts the same files back; its rewind
// makes the conversation again, and leaves unused any it made before. A
// make finds the worktree its lock names it in, or one it finished. A
// remote create sends its frozen command again, which the machine
// answers with the receipt it stored.
var replaySafe = map[string]bool{"turn.interrupt": true, "request.deliver": true, "child.report": true, "agent.close": true, "worktree.remove": true, "checkpoint.capture": true, "checkpoint.drop": true, "thread.revert": true, "worktree.make": true, "remote.create": true}

func ReplaySafe(e Effect) bool { return replaySafe[e.effectKind()] }

var effects = map[string]func([]byte) (Effect, error){
	"turn.start":      decodeAs[StartTurn],
	"turn.steer":      decodeAs[SteerTurn],
	"turn.interrupt":  decodeAs[InterruptTurn],
	"agent.close":     decodeAs[CloseAgent],
	"request.deliver": decodeAs[DeliverAnswer],
	"child.report":    decodeAs[ReportToParent],
	"worktree.remove": decodeAs[RemoveWorktree],
	"worktree.make":   decodeAs[MakeWorktree],
	"remote.create":   decodeAs[RemoteCreate],

	"checkpoint.capture": decodeAs[CaptureCheckpoint],
	"checkpoint.drop":    decodeAs[DropCheckpoints],
	"thread.revert":      decodeAs[RevertThread],
}

func decodeAs[E Effect](b []byte) (Effect, error) {
	var e E
	err := json.Unmarshal(b, &e)
	return e, err
}

func DecodeEffect(kind string, payload []byte) (Effect, error) {
	decode, ok := effects[kind]
	if !ok {
		return nil, fmt.Errorf("engine: unknown effect %q", kind)
	}
	return decode(payload)
}

// Observation is sealed: what the runtime saw an agent or adapter do.
type Observation interface{ observed() }

type Started struct{ Run api.ID }

// Bound is the session the run's agent opened or resumed, before its
// first prompt; Rewound how it took up a rewound conversation, on the
// first run after the rewind.
type Bound struct {
	Run     api.ID
	Session string
	Rewound api.RewindKind
}

type Ended struct {
	Run     api.ID
	Outcome api.Outcome
	Error   string
	Tokens  int64
	Limit   *api.UsageLimit // the usage limit that failed the turn
	Head    string
}

type Delta struct {
	Run  api.ID
	Text string
}

// Context is how full the run's context is, as provider.Context has it.
type Context struct {
	Run                   api.ID
	Used, Window          int64
	Model                 string
	Compacted             bool
	CacheExpires          time.Time
	PreTokens, PostTokens int64
}

// Granted is the access the run's agent runs at, as provider.Granted
// has it.
type Granted struct {
	Run    api.ID
	Access api.Access
	Mode   string
	Why    string
}

// Ran is the model and effort the run's agent runs, as provider.Ran has
// them.
type Ran struct {
	Run    api.ID
	Model  string
	Effort string
	Why    string
}

type Work struct{ Item api.WorkItem }

// Subagent is a native subagent's event, as provider.Nested has it: an
// api.SubagentDelta, SubagentWork or SubagentEnded of Run's.
type Subagent struct {
	Run   api.ID
	Event api.EventBody
}

// Plan is the run's agent's plan, as provider.Plan has it.
type Plan struct{ Plan api.PlanUpdated }

// Captured is what a CaptureCheckpoint saved, or why it saved nothing.
type Captured struct{ Checkpoint api.CheckpointCaptured }

// Reverted is what a RevertThread did.
type Reverted struct{ Revert api.ThreadReverted }

// Surfaced is a request the agent raised. Request is freshly minted; Dedup is
// the provider's id, so a request surfaced again after a restart is ignored.
type Surfaced struct {
	Run, Request api.ID
	Dedup, Kind  string
	Body         json.RawMessage
}

// Withdrawn is a request the agent stopped waiting on, by the provider's id.
type Withdrawn struct {
	Run    api.ID
	Dedup  string
	Reason string
}

type Delivered struct{ Request api.ID }

// Unheard is an answer the agent stopped waiting for before it arrived,
// as when the user's own hook decided first. The run goes on without it.
type Unheard struct {
	Request api.ID
	Reason  string
}

type DeliveryFailed struct {
	Request api.ID
	Error   string
	// Unconfirmed: an engine before this one stopped as it gave the
	// answer, so the agent may have it.
	Unconfirmed bool
}

type Steered struct{ Message api.ID }

type SteerFailed struct {
	Message api.ID
	Error   string
}

// InterruptSent: the agent took the run's interrupt.
type InterruptSent struct{ Run api.ID }

// InterruptLost: the interrupt failed or timed out, so nobody knows whether
// the agent stopped.
type InterruptLost struct{ Run api.ID }

// SteerLost: the steer was cut off (a crash, a timeout or a stop), so
// nobody knows if it arrived.
type SteerLost struct{ Message api.ID }

// Reconciled is boot's verdict on a run the last process left live. Failed
// is a reconcile that errored, so the run's agent may still be running.
type Reconciled struct {
	Run      api.ID
	Restored bool
	Failed   bool
	// Continue has a run of the user's lost as it worked continued unasked.
	Continue bool
	// Asked is that someone asked the run to stop, as the user's Stop or
	// an agent's task_cancel, rather than the engine replacing it.
	Asked bool
}

// Rewound: the database came back from a backup and lost history. The live
// run may have ended there, and a ready message may have run after it.
type Rewound struct{}

// Reported is a child's report, observed on its parent: what the runtime
// made of a ReportToParent. Reply is what the child's run said last, or
// why it failed, for the parent's agent, and Branch the child's. Question
// is the question request a blocked child waits on, and Ask its body.
type Reported struct {
	Report   api.ChildReported
	Reply    string
	Branch   string
	Question api.ID
	Ask      json.RawMessage
	// On is the label of the machine a remote child runs on, where the
	// user answers its question.
	On string
}

// IdleCompact asks the engine to start a silent /compact run on an idle
// thread whose latest finished run is AfterRun, if it is still within its
// idle compaction window.
type IdleCompact struct {
	Run      api.ID
	AfterRun api.ID
}

func (Started) observed()         {}
func (Bound) observed()           {}
func (Ended) observed()           {}
func (Delta) observed()           {}
func (Subagent) observed()        {}
func (Context) observed()         {}
func (Work) observed()            {}
func (Plan) observed()            {}
func (Captured) observed()        {}
func (Reverted) observed()        {}
func (Granted) observed()         {}
func (Ran) observed()             {}
func (Surfaced) observed()        {}
func (Withdrawn) observed()       {}
func (Delivered) observed()       {}
func (Unheard) observed()         {}
func (DeliveryFailed) observed()  {}
func (Steered) observed()         {}
func (SteerFailed) observed()     {}
func (SteerLost) observed()       {}
func (InterruptSent) observed()   {}
func (InterruptLost) observed()   {}
func (Reconciled) observed()      {}
func (Rewound) observed()         {}
func (Reported) observed()        {}
func (IdleCompact) observed()     {}
func (WorktreeReady) observed()   {}
func (WorktreeFailed) observed()  {}
func (RemoteAttempted) observed() {}
func (RemoteBound) observed()     {}
func (RemoteSeen) observed()      {}
func (RemoteLost) observed()      {}

// RemoteAttempted is a placeholder's create about to reach its machine for
// the first time. It is refused once the placeholder is settled or
// deleted, so no try follows that.
type RemoteAttempted struct{}

// RemoteBound is the receipt the machine gave a placeholder's create:
// Thread is its child there.
type RemoteBound struct{ Thread api.ID }

// RemoteSeen is what the placeholder's follower read of its child, up to
// Cursor in its machine's log: its last run there, Run, whether Silent,
// in Slot, and the reports the events read hold, each by its key, of the
// machine labelled On.
type RemoteSeen struct {
	Cursor  api.Cursor
	Run     api.ID
	Silent  bool
	Slot    api.RunState
	Pending []api.ID
	Reports []RemoteReport
	On      string
}

// RemoteReport is one report of a remote child's: of its run Run, by Key,
// the run of a turn's end or the request of one it waits on. Reply is
// what a done run said last, Ask the body of the request a blocked one
// waits on, and Question that request when it is a question.
type RemoteReport struct {
	Key, Run api.ID
	Kind     api.ReportKind
	Error    string
	Reply    string
	Ask      json.RawMessage
	Question api.ID
}

// RemoteLost is a placeholder this engine no longer follows, and why, on
// the machine labelled On.
type RemoteLost struct{ Reason, On string }

// WorktreeReady is a link's thread's worktree made: Workspace is the
// folder in it the thread runs in, and Branch its new branch.
type WorktreeReady struct{ Workspace, Branch string }

// WorktreeFailed is a link's thread whose worktree wasn't made, and won't
// be: what waited its turn is cancelled.
type WorktreeFailed struct{ Error string }
