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

type DeliverAnswer struct {
	Run, Request api.ID
	Dedup        string
	Answer       json.RawMessage
}

func (StartTurn) effectKind() string     { return "turn.start" }
func (SteerTurn) effectKind() string     { return "turn.steer" }
func (InterruptTurn) effectKind() string { return "turn.interrupt" }
func (DeliverAnswer) effectKind() string { return "request.deliver" }

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
var replaySafe = map[string]bool{"turn.interrupt": true, "request.deliver": true}

func ReplaySafe(e Effect) bool { return replaySafe[e.effectKind()] }

var effects = map[string]func([]byte) (Effect, error){
	"turn.start":      decodeAs[StartTurn],
	"turn.steer":      decodeAs[SteerTurn],
	"turn.interrupt":  decodeAs[InterruptTurn],
	"request.deliver": decodeAs[DeliverAnswer],
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

type Ended struct {
	Run     api.ID
	Outcome api.Outcome
	Error   string
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

type Delivered struct{ Request api.ID }

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

// SteerLost: the engine died while steering, so nobody knows if it arrived.
type SteerLost struct{ Message api.ID }

// Reconciled is the verdict on a run the last process left live: boot's,
// or the store's on a restored database, which never restores a run.
type Reconciled struct {
	Run      api.ID
	Restored bool
}

func (Started) observed()        {}
func (Ended) observed()          {}
func (Delta) observed()          {}
func (Work) observed()           {}
func (Surfaced) observed()       {}
func (Delivered) observed()      {}
func (DeliveryFailed) observed() {}
func (Steered) observed()        {}
func (SteerFailed) observed()    {}
func (SteerLost) observed()      {}
func (InterruptSent) observed()  {}
func (Reconciled) observed()     {}
