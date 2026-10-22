// Package engine is the pure core of a host: Decide turns a command and the
// thread's state into events and effects, and Apply folds an event into the
// state. Projections are Apply over the log; nothing else writes them.
package engine

import (
	"encoding/json"
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
	Title       string
	Model       api.ModelRef
	Access      api.Access
	Revision    uint64
	OwnerEpoch  uint64
	Generation  uint64 // of the latest run
	Disposition api.Disposition
	UpdatedAt   time.Time
	Runs        []Run
	Queue       []Message
	Requests    []Request
}

type Run struct {
	ID         api.ID // the admitted message's ID
	Generation uint64
	State      RunState
	Signalled  bool        // an interrupt reached the agent
	Outcome    api.Outcome // set when Finished
	Error      string
	Ended      time.Time // set when Finished
}

type Message struct {
	ID    api.ID
	Text  string
	Mode  api.DispatchMode
	Pos   int64
	State QueueState
	Run   api.ID // set once claimed
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

func (t Thread) slot() RunState {
	if r := t.Live(); r != nil {
		return r.State
	}
	return RunIdle
}

func (t Thread) runAt(id api.ID) int { return index(t.Runs, func(r Run) bool { return r.ID == id }) }

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
		Title:       t.Title,
		Revision:    t.Revision,
		OwnerEpoch:  t.OwnerEpoch,
		Model:       t.Model,
		Access:      t.Access,
		Run:         wireRun[t.slot()],
		Disposition: t.Disposition,
		UpdatedAt:   t.UpdatedAt,
	}
	for i := len(t.Runs) - 1; i >= 0; i-- {
		if r := t.Runs[i]; r.State == RunFinished {
			v.LastTurn = &api.TurnResult{Outcome: r.Outcome, Ended: r.Ended}
			break
		}
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
