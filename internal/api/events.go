package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventSchema is the payload version every event below is written with.
const EventSchema = 1

// EventBody is sealed: only the payload types in events below implement it.
type EventBody interface{ eventType() string }

type Outcome uint8

const (
	OutcomeCompleted Outcome = iota
	OutcomeInterrupted
	OutcomeFailed
	// OutcomeAbandoned ends an uncertain run the user gave up on; nobody knows
	// what the agent did.
	OutcomeAbandoned
)

type WorkStatus uint8

const (
	WorkStarted WorkStatus = iota
	WorkCompleted
	WorkFailed
)

type ThreadCreated struct {
	Workspace string   `json:"workspace"`
	Project   string   `json:"project,omitempty"`
	Branch    string   `json:"branch,omitempty"`
	Title     string   `json:"title"`
	Model     ModelRef `json:"model"`
	Access    Access   `json:"access"`
	Parent    ID       `json:"parent,omitzero"`
	Fork      *Forked  `json:"fork,omitempty"`
	// Remote is the machine a remote child runs on, frozen at its create:
	// this thread is its placeholder here.
	Remote *Remote `json:"remote,omitempty"`
	// Origin is where a link's thread came from, its link stamped. Such a
	// thread has no folder until its worktree is made (WorktreeReady).
	Origin *Origin `json:"origin,omitempty"`
}

// WorktreeReady is a link's thread's worktree made: Workspace is the
// folder in it, and Branch its new branch.
type WorktreeReady struct {
	Workspace string `json:"workspace"`
	Branch    string `json:"branch"`
}

// WorktreeFailed is a link's thread whose worktree wasn't made, and never
// will be: it runs nothing.
type WorktreeFailed struct {
	Error string `json:"error"`
}

// Forked is where a fork goes on from, as its create's commit found it:
// Run of Thread, whose title was Title, on Provider, in the conversation
// Session, "" where the provider names it after the thread. Runs are the
// runs that conversation held then, oldest first, Run among them.
type Forked struct {
	Thread   ID     `json:"thread"`
	Run      ID     `json:"run"`
	Title    string `json:"title"`
	Provider string `json:"provider"`
	Session  string `json:"session,omitempty"`
	Runs     []ID   `json:"runs"`
}

// Remote is another machine's engine a child was placed on, and the link
// it was placed through, by id: the label the person gave the machine can
// later name another (daemon.Machine).
type Remote struct {
	Engine ID `json:"engine"`
	Link   ID `json:"link"`
}

// RemoteAttempted is a placeholder's first try to create its child on
// its machine: from here on, the child may be there.
type RemoteAttempted struct{}

// RemoteBound is the child's create the machine took: Thread is the
// child's there.
type RemoteBound struct {
	Thread ID `json:"thread"`
}

// RemoteSeen is what this engine read of the child on its machine, up to
// Cursor, a position in that machine's log: the child's last run there,
// whether it is silent, its slot, and the reports this engine delivered
// of it, each by its key: the run for a turn's end, the request for one
// it waits on.
type RemoteSeen struct {
	Cursor Cursor   `json:"cursor"`
	Run    ID       `json:"run,omitzero"`
	Silent bool     `json:"silent,omitempty"`
	State  RunState `json:"state"`
	Keys   []ID     `json:"keys,omitempty"`
}

// RemoteLost is a placeholder whose child this engine no longer reaches
// or follows, and why.
type RemoteLost struct {
	Reason string `json:"reason"`
}

// MessageQueued records a dispatched message. The ready message with the
// lowest Pos starts when the thread is idle; a Held one waits to be promoted.
// A message with a Report is no client's: the engine queued it to give the
// thread's agent the report of that child, the one just before it in the
// log, with the thread's next turn.
type MessageQueued struct {
	Message ID           `json:"message"`
	Text    string       `json:"text"`
	Mode    DispatchMode `json:"mode"`
	Pos     int64        `json:"pos"`
	Held    bool         `json:"held,omitempty"`
	Report  ID           `json:"report,omitzero"`
	Silent  bool         `json:"silent,omitempty"`
	// By is the thread whose agent sent the message, as a child's first
	// message or a task tool's; zero for the user's.
	By ID `json:"by,omitzero"`
	// Replaces is the uncertain run the message replaced; Auto, that the
	// engine sent it to continue a run a restart cut off.
	Replaces ID   `json:"replaces,omitzero"`
	Auto     bool `json:"auto,omitempty"`
}

// MessageClaimed binds a message to a live run it will be steered into.
// With Steer, the message waited its turn and is steered in now: it takes
// the steer's mode, so the run's start doesn't count it as a wake that
// went in with its first prompt.
type MessageClaimed struct {
	Message ID   `json:"message"`
	Run     ID   `json:"run"`
	Steer   bool `json:"steer,omitempty"`
}

type MessageDispatched struct {
	Message ID `json:"message"`
}

type MessageCancelled struct {
	Message ID     `json:"message"`
	Reason  string `json:"reason"`
}

// MessageHeld holds a ready message of the user's as a Defer, until they
// send another or resume the queue: what their stop does to what waits.
type MessageHeld struct {
	Message ID `json:"message"`
}

// MessageReleased readies a held message of the user's again, after the
// messages ahead of it.
type MessageReleased struct {
	Message ID `json:"message"`
}

// MessageEdited gives a message that waits its turn the user's new words.
type MessageEdited struct {
	Message ID     `json:"message"`
	Text    string `json:"text"`
}

// MessageMoved gives a message that waits its turn a new Pos, as the user
// moved it or one moved into its place.
type MessageMoved struct {
	Message ID    `json:"message"`
	Pos     int64 `json:"pos"`
}

// MessageUncertain: handing the message to the agent was cut off, so nobody
// knows if it arrived.
type MessageUncertain struct {
	Message ID `json:"message"`
}

// TurnAdmitted starts a run for the message with the same ID, so a message is
// admitted at most once. Silent marks a background maintenance run, such as
// idle compaction.
type TurnAdmitted struct {
	Run        ID     `json:"run"`
	Generation uint64 `json:"generation"`
	Silent     bool   `json:"silent,omitempty"`
}

// TurnStarted: the agent accepted the run's message.
type TurnStarted struct {
	Run ID `json:"run"`
}

// TurnStopping: an interrupt was sent. Open requests of the run are void.
type TurnStopping struct {
	Run ID `json:"run"`
}

// StopAsked: someone asked the live run to stop, as the user's Stop or an
// agent's task_cancel, whatever the run then did. By is the thread whose
// agent asked; zero for the user. The engine's own stops ask nothing.
type StopAsked struct {
	Run ID `json:"run"`
	By  ID `json:"by,omitzero"`
}

// TurnSignalled: the run's stop took: its interrupt reached the agent, or
// it ended while it stopped, whichever came first. A run still stopping
// after it ignores the interrupt.
type TurnSignalled struct {
	Run ID `json:"run"`
}

// TurnEnded finishes the run. Messages it claimed but never delivered are
// cancelled and its open requests are void.
type TurnEnded struct {
	Run     ID      `json:"run"`
	Outcome Outcome `json:"outcome"`
	Error   string  `json:"error,omitempty"`
	Tokens  int64   `json:"tokens,omitempty"` // what the run used, where the agent said
	// Limit is the provider's usage limit that failed the turn, nil when
	// none did.
	Limit *UsageLimit `json:"limit,omitempty"`
	// Head is where the run left the thread's conversation, in its
	// provider's terms, which only its adapter reads: a revert rewinds the
	// conversation to it. "" where the adapter doesn't say.
	Head string `json:"head,omitempty"`
}

// ResumeCancelled: the user stopped the engine continuing Run, which a
// usage limit ended, once the limit resets.
type ResumeCancelled struct {
	Run ID `json:"run"`
}

// ContextReported is how full the run's context is, as its agent said:
// Used tokens of a Window, 0 where the agent didn't say, as its request
// to Model, "" where it didn't. Compacted says the agent compacted it,
// and what it holds is unknown until the next. CacheExpires is when the
// prompt cache created by that request expires, when reported.
type ContextReported struct {
	Run          ID        `json:"run"`
	Used         int64     `json:"used,omitempty"`
	Window       int64     `json:"window,omitempty"`
	Model        string    `json:"model,omitempty"`
	Compacted    bool      `json:"compacted,omitempty"`
	CacheExpires time.Time `json:"cacheExpires,omitzero"`
	PreTokens    int64     `json:"preTokens,omitempty"`
	PostTokens   int64     `json:"postTokens,omitempty"`
}

// TurnRestored: after a restart the adapter proved the agent is the same run.
type TurnRestored struct {
	Run ID `json:"run"`
}

// TurnUncertain: nobody can prove what the agent is doing, for Reason.
// The run blocks the thread until the user ends or replaces it.
type TurnUncertain struct {
	Run    ID              `json:"run"`
	Reason UncertainReason `json:"reason,omitzero"`
	// Continue is that the engine continues the run unasked, as Continue
	// does, once it can: the user has it continue the runs a restart cut
	// off as they worked on a thread of theirs.
	Continue bool `json:"continue,omitempty"`
}

// UncertainReason is why a run became uncertain.
type UncertainReason uint8

const (
	// UncertainUnknown is a run logged uncertain before reasons were.
	UncertainUnknown UncertainReason = iota
	// UncertainStopped: the user stopped a run whose agent took the first
	// stop and never ended, or the stop never reached its agent.
	UncertainStopped
	// UncertainRestarted: the engine restarted, and its provider said no
	// agent of the run is left.
	UncertainRestarted
	// UncertainUnconfirmed: the engine restarted, and its provider couldn't
	// say whether the run's agent is left: it may still be working.
	UncertainUnconfirmed
	// UncertainRewound: the database was restored to before the run's end.
	UncertainRewound
)

type AssistantDelta struct {
	Run  ID     `json:"run"`
	Text string `json:"text"`
}

// WorkItem reports a tool call or similar step; a later event with the same
// Item updates it. Path, Exit and Output are as provider.Work has them;
// Output is at most the last OutputLimit bytes.
type WorkItem struct {
	Run    ID         `json:"run"`
	Item   string     `json:"item"`
	Kind   string     `json:"kind"`
	Title  string     `json:"title"`
	Status WorkStatus `json:"status"`
	Path   string     `json:"path,omitempty"`
	Exit   *int       `json:"exit,omitempty"`
	Output string     `json:"output,omitempty"`
}

// A native subagent is one the run's agent runs in its own process, as
// Claude Code's Task tool or Codex's spawned agents do: not a thread, so
// nothing stops, steers or answers it but its run. Its transcript is under
// Parent, the Item of the run's step that started it, and none of it is
// the run's: not its reply, its context, nor its tokens.

// SubagentDelta is text a native subagent said.
type SubagentDelta struct {
	Run    ID     `json:"run"`
	Parent string `json:"parent"`
	Text   string `json:"text"`
}

// SubagentWork is a native subagent's step, as WorkItem is the run's.
type SubagentWork struct {
	Parent string `json:"parent"`
	WorkItem
}

// SubagentEnded is how a native subagent ended, and the tokens it used,
// where the provider says: they are counted here, not in its run's.
type SubagentEnded struct {
	Run     ID      `json:"run"`
	Parent  string  `json:"parent"`
	Outcome Outcome `json:"outcome"`
	Tokens  int64   `json:"tokens,omitempty"`
}

// PlanStatus is where a step of an agent's plan stands.
type PlanStatus uint8

const (
	PlanPending PlanStatus = iota
	PlanActive
	PlanDone
)

// PlanStep is one step of an agent's plan, in the agent's words.
type PlanStep struct {
	Text   string     `json:"text"`
	Status PlanStatus `json:"status"`
}

// PlanUpdated is the plan the run's agent works to, whole, as it last
// said it: each replaces the one before. No steps clears it.
type PlanUpdated struct {
	Run   ID         `json:"run"`
	Steps []PlanStep `json:"steps,omitempty"`
}

// OutputLimit caps a work item's output in the log: its tail says how a
// command failed, and every subscriber of the thread replays it.
const OutputLimit = 8 << 10

type RequestOpened struct {
	Request    ID              `json:"request"`
	Run        ID              `json:"run"`
	Generation uint64          `json:"generation"`
	Dedup      string          `json:"dedup"` // the provider's request id, unique per run
	Kind       string          `json:"kind"`  // approval | question
	Body       json.RawMessage `json:"body"`
}

type RequestAnswer struct {
	Request ID              `json:"request"`
	Answer  json.RawMessage `json:"answer"`
}

type RequestDelivery struct {
	Request ID `json:"request"`
}

type RequestInvalidation struct {
	Request ID     `json:"request"`
	Reason  string `json:"reason"`
	// Unconfirmed is that the answer may have reached the agent: the engine
	// stopped as it gave it, and can no longer tell.
	Unconfirmed bool `json:"unconfirmed,omitempty"`
}

type ThreadRenamed struct {
	Title string `json:"title"`
}

// SessionBound is the provider's id for the thread's conversation, as the
// run's agent bound it: the thread's later runs resume it. Rewound is,
// on the first run after a revert rewound the conversation, how the
// run's agent took it up: natively, or as a new session the run's
// message hands the kept turns to.
type SessionBound struct {
	Run     ID         `json:"run"`
	Session string     `json:"session"`
	Rewound RewindKind `json:"rewound,omitempty"`
}

// ThreadConfigured is the model and access the thread's turns run at from
// its next on.
type ThreadConfigured struct {
	Model  ModelRef `json:"model"`
	Access Access   `json:"access"`
}

// AccessGranted is the access the run's agent runs at, as its provider
// confirmed it: ConfirmedAccess's Access, Mode and Why.
type AccessGranted struct {
	Run    ID     `json:"run"`
	Access Access `json:"access"`
	Mode   string `json:"mode,omitempty"`
	Why    string `json:"why,omitempty"`
}

// ModelConfirmed is the model and effort the run's agent runs, as its
// provider confirmed them: ConfirmedModel's Model, Effort and Why.
type ModelConfirmed struct {
	Run    ID     `json:"run"`
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
	Why    string `json:"why,omitempty"`
}

// ThreadDeleted is a thread's tombstone, its last event.
type ThreadDeleted struct{}

// ReportKind is how a child's run went, as it reports to its parent.
type ReportKind uint8

const (
	ReportDone ReportKind = iota
	ReportFailed
	// ReportBlocked: the child waits on the user, as on an approval.
	ReportBlocked
)

// ChildReported is a child's report in its parent's log: its run finished,
// failed or blocked on the user, and what came of it, in a line or two.
// A run the user stopped reports nothing: they asked for it.
type ChildReported struct {
	Child   ID         `json:"child"`
	Run     ID         `json:"run"`
	Kind    ReportKind `json:"kind"`
	Title   string     `json:"title"`
	Outcome string     `json:"outcome,omitempty"`
}

type DispositionChanged struct {
	Disposition Disposition `json:"disposition"`
}

// CheckpointPhase is when a checkpoint was taken: base before a thread's
// first run, run at the end of each run, and recovery before a revert
// put files back, so that the revert can be undone.
type CheckpointPhase string

const (
	CheckpointBase     CheckpointPhase = "base"
	CheckpointRun      CheckpointPhase = "run"
	CheckpointRecovery CheckpointPhase = "recovery"
)

// CheckpointRef names one of a thread's checkpoints: the base or a run's
// by the run, and a recovery one by the revert that took it.
type CheckpointRef struct {
	Phase CheckpointPhase `json:"phase"`
	ID    ID              `json:"id"`
}

type CheckpointStatus string

const (
	CheckpointReady   CheckpointStatus = "ready"
	CheckpointSkipped CheckpointStatus = "skipped" // over a budget, or a folder it can't save whole
	CheckpointFailed  CheckpointStatus = "failed"  // git failed
)

// CheckpointCaptured is a checkpoint of the thread's worktree: its files as
// Run left them, or as they were before it in the base phase, saved in the
// engine's own store under refs/glitched/<thread>/base or
// refs/glitched/<thread>/run/<run>. Commit is that ref's commit, Head the
// checkout's HEAD then, Files and Bytes what it holds, and Changed how many
// paths differ from Since, the thread's checkpoint before it, if any. Reason
// says why one is skipped or failed, or on a ready one what it had to do
// the slow way. Transcript only.
type CheckpointCaptured struct {
	Run     ID               `json:"run"`
	Phase   CheckpointPhase  `json:"phase"`
	Status  CheckpointStatus `json:"status"`
	Reason  string           `json:"reason,omitempty"`
	Commit  string           `json:"commit,omitempty"`
	Head    string           `json:"head,omitempty"`
	Files   int              `json:"files,omitempty"`
	Bytes   int64            `json:"bytes,omitempty"`
	Changed int              `json:"changed,omitempty"`
	Since   string           `json:"since,omitempty"`
	Ms      int64            `json:"ms"`
}

// ThreadReverting is a revert the engine is to do: Revert is its command,
// and To the checkpoint it goes back to. It puts the files back unless
// KeepFiles, and rewinds the conversation too when Conversation. It moves
// the thread's revision and nothing else of its view.
type ThreadReverting struct {
	Revert       ID            `json:"revert"`
	To           CheckpointRef `json:"to"`
	Conversation bool          `json:"conversation,omitempty"`
	KeepFiles    bool          `json:"keepFiles,omitempty"`
}

type RevertStatus string

const (
	RevertRestored  RevertStatus = "restored"
	RevertUnchanged RevertStatus = "unchanged" // the files were as To has them
	RevertFailed    RevertStatus = "failed"    // nothing changed, or what had was put back
	RevertKept      RevertStatus = "kept"      // the revert left the files as they were, as asked
)

// RewindKind is how a revert rewound a thread's conversation.
type RewindKind string

const (
	// RewindNative: the provider's own conversation, cut after To's run.
	RewindNative RewindKind = "native"
	// RewindPortable: a new conversation, whose first message hands the
	// agent each kept turn's message and reply.
	RewindPortable RewindKind = "portable"
	// RewindFailed: the conversation couldn't be rewound, so nothing changed.
	RewindFailed RewindKind = "failed"
)

// ThreadReverted is what a revert did: Changed is how many paths it put
// back as To has them, and Recovery the commit of the checkpoint it took
// of the files first, which a revert to it undoes. Error says why one
// failed. HEAD, the index and the branch stay as they were.
//
// Conversation is how it rewound the conversation, "" when it wasn't
// asked to. Session is then the thread's conversation from now on, ""
// for a new one its provider names, and From the one it was. At is where
// a native rewind's new conversation branches off From, in its provider's
// terms, when the provider makes it at the thread's next run. Until that
// run takes it up, natively or as a portable one where its provider can't
// or lost the one it made, the rewind is pending. Dropped is the
// runs after To the conversation held, which it no longer does. A revert
// either does all it was asked or changes nothing. One that undoes a
// rewind, To the rewind's recovery checkpoint, rewinds the conversation to
// where it was before it: Restored is the runs the rewind dropped, which
// the conversation holds again.
//
// It moves the thread's revision; one that rewound the conversation sets
// the thread's session, and clears what the thread knew of its context.
type ThreadReverted struct {
	Revert       ID            `json:"revert"`
	To           CheckpointRef `json:"to"`
	Status       RevertStatus  `json:"status"`
	Changed      int           `json:"changed,omitempty"`
	Recovery     string        `json:"recovery,omitempty"`
	Error        string        `json:"error,omitempty"`
	Conversation RewindKind    `json:"conversation,omitempty"`
	Session      string        `json:"session,omitempty"`
	From         string        `json:"from,omitempty"`
	At           string        `json:"at,omitempty"`
	Dropped      []ID          `json:"dropped,omitempty"`
	Restored     []ID          `json:"restored,omitempty"`
}

// Rewound is whether the revert rewound the thread's conversation.
func (r ThreadReverted) Rewound() bool {
	return r.Conversation == RewindNative || r.Conversation == RewindPortable
}

func (ThreadCreated) eventType() string       { return "thread.created" }
func (MessageQueued) eventType() string       { return "message.queued" }
func (MessageClaimed) eventType() string      { return "message.claimed" }
func (MessageDispatched) eventType() string   { return "message.dispatched" }
func (MessageCancelled) eventType() string    { return "message.cancelled" }
func (MessageUncertain) eventType() string    { return "message.uncertain" }
func (MessageHeld) eventType() string         { return "message.held" }
func (MessageReleased) eventType() string     { return "message.released" }
func (MessageEdited) eventType() string       { return "message.edited" }
func (MessageMoved) eventType() string        { return "message.moved" }
func (TurnAdmitted) eventType() string        { return "turn.admitted" }
func (TurnStarted) eventType() string         { return "turn.started" }
func (TurnStopping) eventType() string        { return "turn.stopping" }
func (StopAsked) eventType() string           { return "stop.asked" }
func (TurnSignalled) eventType() string       { return "turn.signalled" }
func (TurnEnded) eventType() string           { return "turn.ended" }
func (TurnRestored) eventType() string        { return "turn.restored" }
func (TurnUncertain) eventType() string       { return "turn.uncertain" }
func (AssistantDelta) eventType() string      { return "assistant.delta" }
func (WorkItem) eventType() string            { return "work.item" }
func (SubagentDelta) eventType() string       { return "subagent.delta" }
func (SubagentWork) eventType() string        { return "subagent.work" }
func (SubagentEnded) eventType() string       { return "subagent.ended" }
func (PlanUpdated) eventType() string         { return "plan.updated" }
func (RequestOpened) eventType() string       { return "request.opened" }
func (RequestAnswer) eventType() string       { return "request.answered" }
func (RequestDelivery) eventType() string     { return "request.delivered" }
func (RequestInvalidation) eventType() string { return "request.invalidated" }
func (DispositionChanged) eventType() string  { return "disposition.changed" }
func (ThreadRenamed) eventType() string       { return "thread.renamed" }
func (ThreadDeleted) eventType() string       { return "thread.deleted" }
func (ThreadConfigured) eventType() string    { return "thread.configured" }
func (ChildReported) eventType() string       { return "child.reported" }
func (SessionBound) eventType() string        { return "session.bound" }
func (ContextReported) eventType() string     { return "context.reported" }
func (AccessGranted) eventType() string       { return "access.granted" }
func (ModelConfirmed) eventType() string      { return "model.confirmed" }
func (CheckpointCaptured) eventType() string  { return "checkpoint.captured" }
func (ThreadReverting) eventType() string     { return "thread.reverting" }
func (ThreadReverted) eventType() string      { return "thread.reverted" }
func (ResumeCancelled) eventType() string     { return "resume.cancelled" }
func (WorktreeReady) eventType() string       { return "worktree.ready" }
func (WorktreeFailed) eventType() string      { return "worktree.failed" }
func (RemoteAttempted) eventType() string     { return "remote.attempted" }
func (RemoteBound) eventType() string         { return "remote.bound" }
func (RemoteSeen) eventType() string          { return "remote.seen" }
func (RemoteLost) eventType() string          { return "remote.lost" }

var eventBodies = map[string]func([]byte) (EventBody, error){}

func init() {
	register[ThreadCreated]()
	register[MessageQueued]()
	register[MessageClaimed]()
	register[MessageDispatched]()
	register[MessageCancelled]()
	register[MessageUncertain]()
	register[MessageHeld]()
	register[MessageReleased]()
	register[MessageEdited]()
	register[MessageMoved]()
	register[TurnAdmitted]()
	register[TurnStarted]()
	register[TurnStopping]()
	register[StopAsked]()
	register[TurnSignalled]()
	register[TurnEnded]()
	register[TurnRestored]()
	register[TurnUncertain]()
	register[AssistantDelta]()
	register[WorkItem]()
	register[SubagentDelta]()
	register[SubagentWork]()
	register[SubagentEnded]()
	register[PlanUpdated]()
	register[RequestOpened]()
	register[RequestAnswer]()
	register[RequestDelivery]()
	register[RequestInvalidation]()
	register[DispositionChanged]()
	register[ThreadRenamed]()
	register[ThreadDeleted]()
	register[ThreadConfigured]()
	register[ChildReported]()
	register[SessionBound]()
	register[ContextReported]()
	register[AccessGranted]()
	register[ModelConfirmed]()
	register[CheckpointCaptured]()
	register[ThreadReverting]()
	register[ThreadReverted]()
	register[ResumeCancelled]()
	register[WorktreeReady]()
	register[WorktreeFailed]()
	register[RemoteAttempted]()
	register[RemoteBound]()
	register[RemoteSeen]()
	register[RemoteLost]()
}

func register[B EventBody]() {
	var zero B
	eventBodies[zero.eventType()] = func(payload []byte) (EventBody, error) {
		var v B
		err := json.Unmarshal(payload, &v)
		return v, err
	}
}

func EventType(b EventBody) string { return b.eventType() }

// Decode returns the typed payload. An unknown type returns nil and no error:
// readers skip it and the cursor still advances.
func (e Event) Decode() (EventBody, error) {
	decode, ok := eventBodies[e.Type]
	if !ok {
		return nil, nil
	}
	b, err := decode(e.Payload)
	if err != nil {
		return nil, fmt.Errorf("api: %s at %d: %w", e.Type, e.Cursor.Seq, err)
	}
	return b, nil
}
