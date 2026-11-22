package engine

import (
	"encoding/json"
	"fmt"

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
}

// ReportToParent posts a child's report in its parent's log: Title is the
// child's, Kind how its run went; Error is a failed run's, Ask the body of
// the request a blocked one waits on.
type ReportToParent struct {
	Parent, Child, Run api.ID
	Kind               api.ReportKind
	Title, Error       string
	Ask                json.RawMessage
}

func (StartTurn) effectKind() string      { return "turn.start" }
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
	case CloseAgent:
		return true, nil
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
// Closing an agent that is gone does nothing.
var replaySafe = map[string]bool{"turn.interrupt": true, "request.deliver": true, "child.report": true, "agent.close": true}

func ReplaySafe(e Effect) bool { return replaySafe[e.effectKind()] }

var effects = map[string]func([]byte) (Effect, error){
	"turn.start":      decodeAs[StartTurn],
	"turn.steer":      decodeAs[SteerTurn],
	"turn.interrupt":  decodeAs[InterruptTurn],
	"agent.close":     decodeAs[CloseAgent],
	"request.deliver": decodeAs[DeliverAnswer],
	"child.report":    decodeAs[ReportToParent],
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
// first prompt.
type Bound struct {
	Run     api.ID
	Session string
}

type Ended struct {
	Run     api.ID
	Outcome api.Outcome
	Error   string
	Tokens  int64
}

type Delta struct {
	Run  api.ID
	Text string
}

type Work struct{ Item api.WorkItem }

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
}

// Rewound: the database came back from a backup and lost history. The live
// run may have ended there, and a ready message may have run after it.
type Rewound struct{}

// Reported is a child's report, observed on its parent: what the runtime
// made of a ReportToParent. Reply is what the child's run said last, or
// why it failed, for the parent's agent.
type Reported struct {
	Report api.ChildReported
	Reply  string
}

func (Started) observed()        {}
func (Bound) observed()          {}
func (Ended) observed()          {}
func (Delta) observed()          {}
func (Work) observed()           {}
func (Surfaced) observed()       {}
func (Withdrawn) observed()      {}
func (Delivered) observed()      {}
func (Unheard) observed()        {}
func (DeliveryFailed) observed() {}
func (Steered) observed()        {}
func (SteerFailed) observed()    {}
func (SteerLost) observed()      {}
func (InterruptSent) observed()  {}
func (InterruptLost) observed()  {}
func (Reconciled) observed()     {}
func (Rewound) observed()        {}
func (Reported) observed()       {}
