// Package fake is a deterministic provider: every turn plays a fixed script.
// It is the test double and the development default. Agents live in the
// Adapter, not the Host, so a Host restarted on the same Adapter can
// reconcile them the way a CLI process outlives an engine crash.
package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// Turn is the default script: text, a read, an approval when ask is set,
// an edit and more text.
func Turn(ask bool) []provider.Event {
	s := []provider.Event{
		provider.TextDelta{Text: "Looking "},
		provider.TextDelta{Text: "at the code."},
		provider.Work{Item: "read-1", Kind: "read", Title: "Read main.go", Status: api.WorkStarted},
		provider.Work{Item: "read-1", Kind: "read", Title: "Read main.go", Status: api.WorkCompleted},
	}
	if ask {
		s = append(s, provider.TextDelta{Text: " May I edit it?"}, provider.RequestOpened{ID: "approve-1", Kind: "approval", Body: json.RawMessage(`{"operation":"edit","path":"main.go"}`)})
	}
	return append(s,
		provider.Work{Item: "edit-1", Kind: "edit", Title: "Edit main.go", Status: api.WorkStarted},
		provider.Work{Item: "edit-1", Kind: "edit", Title: "Edit main.go", Status: api.WorkCompleted},
		provider.TextDelta{Text: " Done."},
	)
}

type Adapter struct {
	script []provider.Event
	// HoldStart makes Start block until its context ends, like a CLI that
	// hangs while the engine dies.
	HoldStart bool

	mu     sync.Mutex
	agents map[api.ID]*Agent
	starts int
}

func New(script []provider.Event) *Adapter {
	return &Adapter{script: script, agents: map[api.ID]*Agent{}}
}

// Starts counts every Start the adapter received, including held ones.
func (a *Adapter) Starts() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.starts
}

func (a *Adapter) Describe(context.Context) (provider.Info, error) {
	return provider.Info{Name: "fake", Version: "1", Models: []string{"fake"}}, nil
}

func (a *Adapter) Open(_ context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	g := &Agent{a: a, run: spec.Run, wake: make(chan struct{}, 1), answered: map[string]bool{}}
	a.mu.Lock()
	a.agents[spec.Run] = g
	a.mu.Unlock()
	return g, nil
}

func (a *Adapter) Reconcile(_ context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	a.mu.Lock()
	g := a.agents[id.Run]
	a.mu.Unlock()
	if g == nil || !g.restore() {
		return provider.Lost{Reason: "no running agent for this run"}, nil
	}
	return provider.Restored{Agent: g}, nil
}

func (a *Adapter) countStart() {
	a.mu.Lock()
	a.starts++
	a.mu.Unlock()
}

type phase uint8

const (
	opened phase = iota
	running
	stopping
	ended
	closed
)

var errClosed = errors.New("fake: agent closed")

type Agent struct {
	a    *Adapter
	run  api.ID
	wake chan struct{}

	mu        sync.Mutex
	phase     phase
	script    []provider.Event // still to play
	out       []provider.Event // goes ahead of the script
	asking    *provider.RequestOpened
	answered  map[string]bool
	resurface bool
}

func (g *Agent) Do(ctx context.Context, act provider.Action) error {
	if _, ok := act.(provider.Start); ok && g.a.HoldStart {
		g.a.countStart()
		<-ctx.Done()
		return ctx.Err()
	}
	g.mu.Lock()
	defer g.signal()
	defer g.mu.Unlock()
	switch act := act.(type) {
	case provider.Start:
		if g.phase != opened {
			return errors.New("fake: already started")
		}
		g.a.countStart()
		g.phase, g.script = running, slices.Clone(g.a.script)
	case provider.Steer:
		if g.phase != running {
			return errors.New("fake: nothing to steer")
		}
		g.out = append(g.out, provider.TextDelta{Text: "steered: " + act.Text})
	case provider.Interrupt:
		if g.phase != running {
			return nil
		}
		g.phase, g.script, g.asking = stopping, nil, nil
		g.out = append(g.out, provider.TurnEnded{Outcome: api.OutcomeInterrupted})
	case provider.Respond:
		if g.answered[act.Request] {
			return nil
		}
		if g.asking == nil || g.asking.ID != act.Request {
			return fmt.Errorf("fake: no open request %q", act.Request)
		}
		g.answered[act.Request], g.asking = true, nil
	}
	return nil
}

func (g *Agent) Next(ctx context.Context) (provider.Event, error) {
	for {
		// A reader that is going away must not consume an event.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		g.mu.Lock()
		e, err := g.step()
		g.mu.Unlock()
		if e != nil || err != nil {
			return e, err
		}
		select {
		case <-g.wake:
		case <-ctx.Done():
		}
	}
}

// step returns the next event, or nil when the agent has to wait.
func (g *Agent) step() (provider.Event, error) {
	switch {
	case g.phase == closed:
		return nil, errClosed
	case len(g.out) > 0:
		e := g.out[0]
		g.out = g.out[1:]
		if _, ok := e.(provider.TurnEnded); ok {
			g.phase = ended
		}
		return e, nil
	case g.asking != nil && g.resurface:
		g.resurface = false
		return *g.asking, nil
	case g.phase != running || g.asking != nil:
		return nil, nil
	case len(g.script) == 0:
		g.phase = ended
		return provider.TurnEnded{Outcome: api.OutcomeCompleted}, nil
	}
	e := g.script[0]
	g.script = g.script[1:]
	if r, ok := e.(provider.RequestOpened); ok {
		g.asking = &r
	}
	return e, nil
}

func (g *Agent) restore() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	alive := g.phase == running || g.phase == stopping
	g.resurface = alive
	return alive
}

func (g *Agent) Close(context.Context) error {
	g.mu.Lock()
	g.phase = closed
	g.mu.Unlock()
	g.signal()
	g.a.mu.Lock()
	if g.a.agents[g.run] == g {
		delete(g.a.agents, g.run)
	}
	g.a.mu.Unlock()
	return nil
}

func (g *Agent) signal() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}
