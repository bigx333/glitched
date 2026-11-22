// Package fake is a deterministic provider: every turn plays a fixed script.
// It is the test double and the development default. Agents live in the
// Adapter, not the Host, so a Host restarted on the same Adapter can
// reconcile them the way a CLI process outlives an engine crash.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

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
		s = append(s, provider.TextDelta{Text: " May I edit it?"}, provider.RequestOpened{ID: "approve-1", Kind: api.KindApproval, Body: json.RawMessage(`{"operation":"edit","path":"main.go"}`)})
	}
	return append(s,
		provider.Work{Item: "edit-1", Kind: "edit", Title: "Edit main.go", Status: api.WorkStarted},
		provider.Work{Item: "edit-1", Kind: "edit", Title: "Edit main.go", Status: api.WorkCompleted},
		provider.TextDelta{Text: " Done."},
	)
}

// Asking is a script with one question request of two questions, the
// second multi-select. The agent says the answer it got, which a test
// reads in the transcript.
func Asking() []provider.Event {
	body, _ := json.Marshal(api.Questions{Questions: []api.Question{
		{Text: "Which store should the cache use?", Header: "Store", Options: []api.Option{
			{Label: "SQLite", About: "One file next to the log"}, {Label: "Memory", About: "Lost on restart"}}},
		{Text: "Which suites gate the merge?", Header: "Tests", Multi: true, Options: []api.Option{
			{Label: "Unit"}, {Label: "Race"}, {Label: "GUI", About: "4 to 8 minutes"}}},
	}})
	return []provider.Event{
		provider.TextDelta{Text: "Two things first."},
		provider.RequestOpened{ID: "ask-1", Kind: api.KindQuestion, Body: body},
		provider.TextDelta{Text: " Thanks."},
	}
}

type Adapter struct {
	script []provider.Event
	// HoldStart makes Start block until its context ends, like a CLI that
	// hangs while the engine dies.
	HoldStart bool
	// Tokens is what each turn that completes says it used.
	Tokens int64
	// Models are what Describe reports it runs: none, as a provider
	// that's only a placeholder.
	Models []provider.Model

	mu     sync.Mutex
	agents map[api.ID]*Agent
	starts int
	engine api.Endpoint
	spawns map[string][]Child
	made   map[api.ID]error // each kid's create, by its thread, once answered
}

// A Child is an agent a turn starts on the engine, as a call to glitched
// mcp's delegate_task does: a create with the parent's live run and Task as
// the first message. The turn sends the call Tries times, at least once, as
// a client resends one whose reply it lost; each try is the same command.
type Child struct {
	Title, Task string
	Access      api.Access // ignored unless Wider; else the parent's
	Wider       bool
	Tries       int
}

// Spawns has every turn whose message is text start kids on ep, in order,
// and end, as an agent that hands its work out does: it plays no script
// and uses no tokens. Each kid shows as a step, failed with the engine's
// refusal when it has one.
func (a *Adapter) Spawns(ep api.Endpoint, text string, kids ...Child) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.spawns == nil {
		a.spawns = map[string][]Child{}
	}
	a.engine, a.spawns[text] = ep, kids
}

// Spawned is how the create of the kid that starts child went, once the
// engine answered it.
func (a *Adapter) Spawned(child api.ID) (answered bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	err, answered = a.made[child]
	return answered, err
}

// ChildOf is the thread the nth Child of run's turn starts, from 0.
func ChildOf(run api.ID, n int) api.ID { return derive(run, "child", n) }

func derive(run api.ID, what string, n int) api.ID {
	sum := sha256.Sum256([]byte(run.String() + "/" + what + "/" + strconv.Itoa(n)))
	return api.ID(sum[:16])
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
	return provider.Info{Name: "fake", Version: "1", Models: a.Models}, nil
}

var _ provider.Commander = (*Adapter)(nil)

// Commands are a CLI's two own commands and a plugin's skill, the same in
// every workspace.
func (a *Adapter) Commands(context.Context, string) ([]provider.Command, error) {
	return []provider.Command{
		{Name: "compact", Description: "Summarize the conversation to save context", ArgumentHint: "<instructions>"},
		{Name: "review", Description: "Review the changes so far"},
		{Name: "fake:tidy", Description: "(fake) Tidy the workspace"},
	}, nil
}

func (a *Adapter) Open(_ context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	g := &Agent{a: a, run: spec.Run, spec: spec, wake: make(chan struct{}, 1), answered: map[string]bool{}}
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
	spec provider.SessionSpec
	wake chan struct{}

	mu        sync.Mutex
	phase     phase
	script    []provider.Event // still to play
	out       []provider.Event // goes ahead of the script
	asking    *provider.RequestOpened
	answered  map[string]bool
	resurface bool
	tokens    int64   // what the turn says it used
	kids      []Child // still to start
	spawned   int     // kids started or refused
	spawning  bool    // a kid's create is in flight
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
		g.phase, g.script, g.tokens = running, slices.Clone(g.a.script), g.a.Tokens
		g.a.mu.Lock()
		if kids, ok := g.a.spawns[act.Text]; ok {
			g.script, g.tokens, g.kids = nil, 0, slices.Clone(kids)
		}
		g.a.mu.Unlock()
	case provider.Steer:
		if g.phase != running {
			return errors.New("fake: nothing to steer")
		}
		g.out = append(g.out, provider.TextDelta{Text: "steered: " + act.Text})
	case provider.Interrupt:
		if g.phase != running {
			return nil
		}
		g.phase, g.script, g.asking, g.kids = stopping, nil, nil, nil
		g.out = append(g.out, provider.TurnEnded{Outcome: api.OutcomeInterrupted})
	case provider.Respond:
		if g.answered[act.Request] {
			return nil
		}
		if g.asking == nil || g.asking.ID != act.Request {
			return fmt.Errorf("fake: no open request %q", act.Request)
		}
		if g.asking.Kind == api.KindQuestion {
			g.out = append(g.out, provider.TextDelta{Text: " got: " + string(act.Answer)})
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
	case g.phase != running || g.asking != nil || g.spawning:
		return nil, nil
	case len(g.kids) > 0:
		return g.spawn(), nil
	case len(g.script) == 0:
		g.phase = ended
		return provider.TurnEnded{Outcome: api.OutcomeCompleted, Tokens: g.tokens}, nil
	}
	e := g.script[0]
	g.script = g.script[1:]
	if r, ok := e.(provider.RequestOpened); ok {
		g.asking = &r
	}
	return e, nil
}

// spawn sends the next kid's create and returns its step. The create runs
// apart from the agent, as an MCP server's call does, and the step ends
// once the engine answers.
func (g *Agent) spawn() provider.Event {
	k, n := g.kids[0], g.spawned
	g.kids, g.spawned, g.spawning = g.kids[1:], n+1, true
	access := g.spec.Access
	if k.Wider {
		access = k.Access
	}
	c := api.Command{ID: derive(g.run, "call", n), Target: ChildOf(g.run, n), Body: api.ThreadCreate{
		Workspace: g.spec.Workspace, Title: k.Title, Model: g.spec.Model, Access: access,
		Parent: g.spec.Thread, Run: g.run, Text: k.Task,
	}}
	g.a.mu.Lock()
	ep := g.a.engine
	g.a.mu.Unlock()
	item := "spawn-" + strconv.Itoa(n)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var err error
		for range max(k.Tries, 1) {
			_, err = ep.Submit(ctx, c)
		}
		g.a.mu.Lock()
		if g.a.made == nil {
			g.a.made = map[api.ID]error{}
		}
		g.a.made[c.Target] = err
		g.a.mu.Unlock()
		w := provider.Work{Item: item, Kind: "tool", Title: "Started agent " + k.Title, Status: api.WorkCompleted}
		if err != nil {
			w.Status, w.Output = api.WorkFailed, err.Error()
		}
		g.mu.Lock()
		if g.phase == running {
			g.out = append(g.out, w)
		}
		g.spawning = false
		g.mu.Unlock()
		g.signal()
	}()
	return provider.Work{Item: item, Kind: "tool", Title: "Started agent " + k.Title, Status: api.WorkStarted}
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
