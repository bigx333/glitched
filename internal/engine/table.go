package engine

import (
	"fmt"

	"glitched/internal/api"
)

// The tables below are the only place a transition is checked. Apply refuses
// any edge they don't list, and Decide learns a command is illegal by trying
// the events and seeing Apply refuse them.

type edges[S comparable] map[S][]S

// RunIdle → RunStarting is admission. A finished run leaves the slot idle.
// There is no persisted reconciling state: boot reconciles every live run,
// and the answer is turn.restored (no change) or turn.uncertain.
var runEdges = edges[RunState]{
	RunIdle:      {RunStarting},
	RunStarting:  {RunRunning, RunStopping, RunUncertain, RunFinished},
	RunRunning:   {RunWaiting, RunStopping, RunUncertain, RunFinished},
	RunWaiting:   {RunRunning, RunStopping, RunUncertain, RunFinished},
	RunStopping:  {RunUncertain, RunFinished},
	RunUncertain: {RunFinished},
}

var queueEdges = edges[QueueState]{
	QueueReady:   {QueueClaimed, QueueCancelled},
	QueueHeld:    {QueueClaimed, QueueCancelled},
	QueueClaimed: {QueueDispatched, QueueCancelled, QueueUncertain},
}

// An answer commits with the effect that delivers it, so only that effect
// settles an answered request: delivered when the agent took it, invalidated
// when it could not.
var requestEdges = edges[api.RequestState]{
	api.RequestPending:  {api.RequestAnswered, api.RequestInvalidated, api.RequestExpired},
	api.RequestAnswered: {api.RequestDelivered, api.RequestInvalidated},
}

// steerable runs can take a message mid-turn; the agent exists or is about to.
var steerable = map[RunState]bool{RunStarting: true, RunRunning: true, RunWaiting: true}

type TransitionError struct {
	Machine  string
	From, To any
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("engine: %s cannot go from %v to %v", e.Machine, e.From, e.To)
}

func (t edges[S]) check(machine string, from, to S) error {
	for _, s := range t[from] {
		if s == to {
			return nil
		}
	}
	return &TransitionError{machine, from, to}
}
