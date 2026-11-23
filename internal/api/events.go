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
}

// MessageClaimed binds a message to a live run it will be steered into.
type MessageClaimed struct {
	Message ID `json:"message"`
	Run     ID `json:"run"`
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

// TurnSignalled: the run's interrupt reached the agent. A run still
// stopping after it ignores the interrupt.
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

// TurnUncertain: after a restart nobody can prove what the agent is doing.
// The run blocks the thread until the user ends it.
type TurnUncertain struct {
	Run ID `json:"run"`
}

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
}

type ThreadRenamed struct {
	Title string `json:"title"`
}

// SessionBound is the provider's id for the thread's conversation, as the
// run's agent bound it: the thread's later runs resume it.
type SessionBound struct {
	Run     ID     `json:"run"`
	Session string `json:"session"`
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

func (ThreadCreated) eventType() string       { return "thread.created" }
func (MessageQueued) eventType() string       { return "message.queued" }
func (MessageClaimed) eventType() string      { return "message.claimed" }
func (MessageDispatched) eventType() string   { return "message.dispatched" }
func (MessageCancelled) eventType() string    { return "message.cancelled" }
func (MessageUncertain) eventType() string    { return "message.uncertain" }
func (MessageHeld) eventType() string         { return "message.held" }
func (MessageReleased) eventType() string     { return "message.released" }
func (TurnAdmitted) eventType() string        { return "turn.admitted" }
func (TurnStarted) eventType() string         { return "turn.started" }
func (TurnStopping) eventType() string        { return "turn.stopping" }
func (TurnSignalled) eventType() string       { return "turn.signalled" }
func (TurnEnded) eventType() string           { return "turn.ended" }
func (TurnRestored) eventType() string        { return "turn.restored" }
func (TurnUncertain) eventType() string       { return "turn.uncertain" }
func (AssistantDelta) eventType() string      { return "assistant.delta" }
func (WorkItem) eventType() string            { return "work.item" }
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
	register[TurnAdmitted]()
	register[TurnStarted]()
	register[TurnStopping]()
	register[TurnSignalled]()
	register[TurnEnded]()
	register[TurnRestored]()
	register[TurnUncertain]()
	register[AssistantDelta]()
	register[WorkItem]()
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
