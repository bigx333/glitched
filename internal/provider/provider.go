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

// ErrWithdrawn is what Do returns for a Respond to a request the agent
// stopped waiting on before the answer reached it, as when the user's own
// hook decided first. The answer went nowhere, and the run goes on.
var ErrWithdrawn = errors.New("The agent stopped waiting before the answer reached it.")

// ErrSignedOut is what Describe and Open return, wrapped with what to do,
// when the provider needs its user to sign in: unlike an empty list of
// models, it says nothing of what the account runs.
var ErrSignedOut = errors.New("signed out")

// ErrNotInstalled is what Describe and Open return, wrapped with how to
// install it, when the provider's program isn't there.
var ErrNotInstalled = errors.New("not installed")

// Needs is an error its user must act on before the provider runs: Kind
// is ErrSignedOut or ErrNotInstalled, Help what to do, in the provider's
// words, which is all the error says.
type Needs struct {
	Kind error
	Help string
}

func (n Needs) Error() string { return n.Help }
func (n Needs) Unwrap() error { return n.Kind }

// Forgetter is an Adapter that keeps what Describe said: Forget has the
// next Describe ask the provider again, as after the user signed in.
type Forgetter interface{ Forget() }

// ErrUnavailable is what Ask returns when the provider's CLI isn't on this
// machine.
var ErrUnavailable = errors.New("provider: not installed")

// Asker is an Adapter that answers one prompt with text: no session, no
// tools, no thread.
type Asker interface {
	Ask(ctx context.Context, spec AskSpec) (AskResult, error)
}

// AskSpec is one prompt. Model "" is the adapter's own cheap model. Dir is
// the folder the provider runs in, whose project files it may read.
type AskSpec struct {
	Model  string `json:"model,omitempty"`
	System string `json:"system,omitempty"`
	Prompt string `json:"prompt"`
	Dir    string `json:"dir,omitempty"`
}

// AskResult is the answer, and the model that wrote it.
type AskResult struct {
	Text  string `json:"text"`
	Model string `json:"model"`
}

// Sessioned is an Agent whose provider chose its own id for the thread's
// conversation. The host reads it after Open and stores it before Start,
// and gives it back in SessionSpec.Session to the thread's later runs.
type Sessioned interface{ Session() string }

// Info is what a provider is and offers. Models are those its sessions can
// run, as the provider reports them for the user's own account, in its
// order: none for a provider that runs nothing yet.
type Info struct {
	Name    string  `json:"name"`
	Version string  `json:"version"`
	Models  []Model `json:"models"`
	// Path is the program Glitched runs for the provider.
	Path string `json:"path,omitempty"`
}

// Model is one a provider's sessions can run. ID is what a session's
// api.ModelRef names it by; Name and Description are the provider's own
// words for it. Efforts are the levels it runs at, in order, least first:
// none where the provider doesn't say.
type Model struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Efforts     []Effort `json:"efforts,omitempty"`
}

// Effort is a level of effort a model runs at: ID is what a session's
// api.ModelRef names it by, Label the provider's word for it, if any.
type Effort struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// Command is one of the provider's slash commands, as /Name in a prompt:
// its own, the user's, or a skill.
type Command struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argumentHint,omitempty"`
}

// Commander is an Adapter that can list the slash commands a session in
// workspace would have, without a run.
type Commander interface {
	Commands(ctx context.Context, workspace string) ([]Command, error)
}

type SessionSpec struct {
	Thread, Run api.ID
	Generation  uint64
	Workspace   string
	Model       api.ModelRef
	Access      api.Access
	// MCP are the MCP servers the run's agent gets, glitched's own first,
	// which each adapter hands its agent its own way; none runs the agent
	// without them.
	MCP []MCPServer
	// Session is the thread's conversation as a Sessioned agent of the
	// provider's last bound it; "" when none has.
	Session string
}

// MCPServer is a stdio MCP server an agent starts for itself: Command with
// Args and Env. Instructions tell the agent what the server is for. A
// Trusted server's tools run without asking, as glitched mcp's do, whose
// engine decides each call itself; any other's ask as the agent's own
// tools do.
type MCPServer struct {
	Name         string
	Command      string
	Args         []string
	Env          map[string]string
	Instructions string
	Trusted      bool
}

// RuntimeIdentity is what the engine persisted about a run. The adapter tags
// the provider process with it so Reconcile can find it after a restart.
// Provider is the thread's, which picks the adapter.
type RuntimeIdentity struct {
	Thread, Run api.ID
	Generation  uint64
	Provider    string
}

// Adapter opens and finds agents. The ctx given to Open or Reconcile bounds
// that call only. The agent it returns lives until its Close, so an adapter
// must not tie the session to that ctx.
type Adapter interface {
	// Describe may start the provider, as to ask it for its models, but
	// never calls a model.
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

// Work is a step of the agent's. Path is the file an edit or read
// touched; Exit and Output are how a finished command went, its exit code
// and what it printed, where the provider says.
type Work struct {
	Item, Kind, Title string
	Status            api.WorkStatus
	Path              string
	Exit              *int
	Output            string
}

type RequestOpened struct {
	ID   string // the provider's id, unique within the run
	Kind string // approval | question
	Body json.RawMessage
}

// RequestWithdrawn is a request the provider stopped waiting on before
// anyone answered it, as when one of the user's own hooks decided it.
type RequestWithdrawn struct {
	ID     string // RequestOpened.ID
	Reason string
}

type TurnEnded struct {
	Outcome api.Outcome
	Error   string
	Tokens  int64 // what the turn used, where the provider says
}

func (TextDelta) event()        {}
func (Work) event()             {}
func (RequestOpened) event()    {}
func (RequestWithdrawn) event() {}
func (TurnEnded) event()        {}
