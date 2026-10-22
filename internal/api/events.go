package api

import (
	"encoding/json"
	"fmt"
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
	Title     string   `json:"title"`
	Model     ModelRef `json:"model"`
	Access    Access   `json:"access"`
}

// MessageQueued records a dispatched message. The ready message with the
// lowest Pos starts when the thread is idle; a Held one waits to be promoted.
type MessageQueued struct {
	Message ID           `json:"message"`
	Text    string       `json:"text"`
	Mode    DispatchMode `json:"mode"`
	Pos     int64        `json:"pos"`
	Held    bool         `json:"held,omitempty"`
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

// MessageUncertain: the engine died while handing the message to the agent.
type MessageUncertain struct {
	Message ID `json:"message"`
}

// TurnAdmitted starts a run for the message with the same ID, so a message is
// admitted at most once.
type TurnAdmitted struct {
	Run        ID     `json:"run"`
	Generation uint64 `json:"generation"`
}

// TurnStarted: the agent accepted the run's message.
type TurnStarted struct {
	Run ID `json:"run"`
}

// TurnStopping: an interrupt was sent. Open requests of the run are void.
type TurnStopping struct {
	Run ID `json:"run"`
}

// TurnEnded finishes the run. Messages it claimed but never delivered are
// cancelled and its open requests are void.
type TurnEnded struct {
	Run     ID      `json:"run"`
	Outcome Outcome `json:"outcome"`
	Error   string  `json:"error,omitempty"`
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
// Item updates it.
type WorkItem struct {
	Run    ID         `json:"run"`
	Item   string     `json:"item"`
	Kind   string     `json:"kind"`
	Title  string     `json:"title"`
	Status WorkStatus `json:"status"`
}

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

type DispositionChanged struct {
	Disposition Disposition `json:"disposition"`
}

func (ThreadCreated) eventType() string       { return "thread.created" }
func (MessageQueued) eventType() string       { return "message.queued" }
func (MessageClaimed) eventType() string      { return "message.claimed" }
func (MessageDispatched) eventType() string   { return "message.dispatched" }
func (MessageCancelled) eventType() string    { return "message.cancelled" }
func (MessageUncertain) eventType() string    { return "message.uncertain" }
func (TurnAdmitted) eventType() string        { return "turn.admitted" }
func (TurnStarted) eventType() string         { return "turn.started" }
func (TurnStopping) eventType() string        { return "turn.stopping" }
func (TurnEnded) eventType() string           { return "turn.ended" }
func (TurnRestored) eventType() string        { return "turn.restored" }
func (TurnUncertain) eventType() string       { return "turn.uncertain" }
func (AssistantDelta) eventType() string      { return "assistant.delta" }
func (WorkItem) eventType() string            { return "work.item" }
func (RequestOpened) eventType() string       { return "request.opened" }
func (RequestAnswer) eventType() string       { return "request.answered" }
func (RequestDelivery) eventType() string     { return "request.delivered" }
func (RequestInvalidation) eventType() string { return "request.invalidated" }
func (DispositionChanged) eventType() string  { return "disposition.changed" }

var eventBodies = map[string]func([]byte) (EventBody, error){}

func init() {
	register[ThreadCreated]()
	register[MessageQueued]()
	register[MessageClaimed]()
	register[MessageDispatched]()
	register[MessageCancelled]()
	register[MessageUncertain]()
	register[TurnAdmitted]()
	register[TurnStarted]()
	register[TurnStopping]()
	register[TurnEnded]()
	register[TurnRestored]()
	register[TurnUncertain]()
	register[AssistantDelta]()
	register[WorkItem]()
	register[RequestOpened]()
	register[RequestAnswer]()
	register[RequestDelivery]()
	register[RequestInvalidation]()
	register[DispositionChanged]()
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
