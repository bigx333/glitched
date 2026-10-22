// Package provider is the boundary to agent CLIs. An Adapter opens one Agent
// per run; the runtime drives it with actions and reads normalized events.
package provider

import (
	"context"
	"encoding/json"
	"errors"

	"glitched/internal/api"
)

// ErrUnsupported is what Do returns for an action the provider can't take.
var ErrUnsupported = errors.New("provider: unsupported action")

type Info struct {
	Name    string
	Version string
	Models  []string
}

type SessionSpec struct {
	Thread, Run api.ID
	Generation  uint64
	Workspace   string
	Model       api.ModelRef
	Access      api.Access
}

// RuntimeIdentity is what the engine persisted about a run. The adapter tags
// the provider process with it so Reconcile can find it after a restart.
type RuntimeIdentity struct {
	Thread, Run api.ID
	Generation  uint64
}

// Adapter opens and finds agents. The ctx given to Open or Reconcile bounds
// that call only. The agent it returns lives until its Close, so an adapter
// must not tie the session to that ctx.
type Adapter interface {
	Describe(ctx context.Context) (Info, error)
	Open(ctx context.Context, spec SessionSpec) (Agent, error)
	// Reconcile answers Restored only when it can prove the agent is the
	// same run; anything else is Lost and the run becomes uncertain.
	Reconcile(ctx context.Context, id RuntimeIdentity) (Recovery, error)
}

// Agent is one run's provider session. Do and Next may be called from
// different goroutines. A repeated Interrupt, or a repeated Respond to the
// same request, is a no-op, because the runtime retries them after a crash.
// A restored agent surfaces its unanswered requests again. The runtime
// retries a failed Close until it returns nil, so Close on an agent that is
// already gone returns nil.
type Agent interface {
	Do(ctx context.Context, a Action) error
	Next(ctx context.Context) (Event, error)
	Close(ctx context.Context) error
}

// Recovery is sealed: Restored or Lost.
type Recovery interface{ recovery() }

type Restored struct{ Agent Agent }

type Lost struct{ Reason string }

func (Restored) recovery() {}
func (Lost) recovery()     {}

// Action is sealed.
type Action interface{ action() }

type Start struct{ Text string }

type Steer struct{ Text string }

type Interrupt struct{}

type Respond struct {
	Request string // RequestOpened.ID
	Answer  json.RawMessage
}

func (Start) action()     {}
func (Steer) action()     {}
func (Interrupt) action() {}
func (Respond) action()   {}

// Event is sealed. A turn ends with exactly one TurnEnded.
type Event interface{ event() }

type TextDelta struct{ Text string }

type Work struct {
	Item, Kind, Title string
	Status            api.WorkStatus
}

type RequestOpened struct {
	ID   string // the provider's id, unique within the run
	Kind string // approval | question
	Body json.RawMessage
}

type TurnEnded struct {
	Outcome api.Outcome
	Error   string
}

func (TextDelta) event()     {}
func (Work) event()          {}
func (RequestOpened) event() {}
func (TurnEnded) event()     {}
