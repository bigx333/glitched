// Package provider is the boundary to agent CLIs. An Adapter opens one Agent
// per run; the runtime drives it with actions and reads normalized events.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

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

// AskSpec is one prompt. Model "" is the adapter's own cheap model.
// Timeout bounds the ask in the engine that runs it, as a caller's
// deadline doesn't cross the transport. An ask has one: the engine refuses
// one that isn't positive.
type AskSpec struct {
	Model   string        `json:"model,omitempty"`
	System  string        `json:"system,omitempty"`
	Prompt  string        `json:"prompt"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

// AskResult is the answer, and the model that wrote it.
type AskResult struct {
	Text  string `json:"text"`
	Model string `json:"model"`
}

// Sessioned is an Agent whose provider chose its own id for the thread's
// conversation. The host reads it after Open and stores it before Start,
// or, in a run that takes up a rewind, once Start sent the run's message,
// and gives it back in SessionSpec.Session to the thread's later runs.
type Sessioned interface{ Session() string }

// Rewinder is an Adapter that can rewind a thread's conversation in its
// provider's own terms. Rewind never calls a model. An adapter that isn't
// one, or whose Rewind returns a portable Rewind, has the thread's next
// run start a new conversation, which the host hands the kept turns.
type Rewinder interface {
	Rewind(ctx context.Context, spec RewindSpec) (Rewind, error)
}

// RewindSpec is a rewind of Thread's conversation, Session, to where its
// run Head left it, which Revert, the revert's command, asks; or a fork
// of it there, which Revert, the fork's thread, asks. Dropped are
// the Heads of the runs after it, oldest first, "" for one that said
// none. Compacted is that the provider compacted the conversation in one
// of those.
type RewindSpec struct {
	Thread, Revert api.ID
	Workspace      string
	Session, Head  string
	Dropped        []string
	Compacted      bool
}

// Rewind is how the adapter rewound the conversation: Session is the
// thread's from now on, "" for a new one its provider names. Native is
// that it holds the kept turns as the provider had them. At is, when the
// adapter makes the native conversation at the thread's next run, where
// it branches off spec.Session, which SessionSpec.Rewound gives it back.
type Rewind struct {
	Session string
	Native  bool
	At      string
}

// Rewound is the rewind the run's agent takes up, when the thread's
// conversation was rewound since its last run opened one. From and At are
// the Rewind's, set for an adapter that makes the native conversation
// now; without them the run starts a new one.
type Rewound struct{ From, At string }

// Rewinding is an Agent opened for a SessionSpec with Rewound that says
// whether it took up the native conversation; the host hands any other
// agent the kept turns in the run's message.
type Rewinding interface{ Native() bool }

// Grant is how one of Glitched's access levels reaches an agent: the
// permission mode its session runs in, "" for the agent's own, and what
// its program starts with besides. Else is the mode it runs in where the
// agent doesn't offer Mode, as an older version or another account may
// not. Each adapter keeps Grants for its agent, a Grant per level, as the
// agent names its own controls.
type Grant struct {
	Mode string
	Else string
	Args []string
}

type Grants map[api.Access]Grant

// Info is what a provider is and offers. Models are those its sessions can
// run, as the provider reports them for the user's own account, in its
// order: none for a provider that runs nothing yet.
type Info struct {
	Name    string  `json:"name"`
	Version string  `json:"version"`
	Models  []Model `json:"models"`
	// Path is the program Glitched runs for the provider.
	Path string `json:"path,omitempty"`
	// NoAuto is why the provider's own auto mode, Approve for me, was
	// last not on offer, so that its run approved edits and asked for the
	// rest instead; "" while it is.
	NoAuto string `json:"noAuto,omitempty"`
}

// Model is one a provider's sessions can run. ID is what a session's
// api.ModelRef names it by; Name and Description are the provider's own
// words for it. Efforts are the levels it runs at, in order, least first:
// none where the provider doesn't say. Default is that the provider's
// program runs it where a session names no model, and DefaultEffort the
// effort it runs it at where a session names none: "" where it doesn't
// say. One model of a provider's at most is its Default.
type Model struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Efforts       []Effort `json:"efforts,omitempty"`
	Default       bool     `json:"default,omitempty"`
	DefaultEffort string   `json:"defaultEffort,omitempty"`
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

// Defaulter is an Adapter whose program's default model depends on the
// workspace a session runs in, as its settings there may name one:
// DefaultModel is the id of the model a session in workspace runs where
// it names none, as Info's Default is where no workspace's settings do;
// "" where it can't say.
type Defaulter interface {
	DefaultModel(ctx context.Context, workspace string) (string, error)
}

// Commander is an Adapter that can list the slash commands a session in
// workspace would have, without a run.
type Commander interface {
	Commands(ctx context.Context, workspace string) ([]Command, error)
}

// ErrDeclined is an adapter's answer to a ResumeOnly run its agent would
// not make now: its own settings, or what it said of the account, say no.
var ErrDeclined = errors.New("provider: the agent declines the compaction")

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
	// Credentials is the thread's vault, the folder of the values its user
	// gave its agents, which the agent's commands may read; "" when the
	// host keeps none. An adapter whose agent can source a script before
	// each command hands it secrets.ClaudeEnv's kind there.
	Credentials string
	// ResumeOnly requires the adapter to resume the existing conversation
	// and fail if it cannot, rather than starting a fresh session. Used for
	// background maintenance such as idle compaction; an adapter whose
	// agent would not compact now returns ErrDeclined.
	ResumeOnly bool
	// CacheExpires is when the thread's prompt cache expires, as its last
	// run reported; zero when none did.
	CacheExpires time.Time
	// Skills are Glitched's skills the run's agent gets, which each
	// adapter hands its agent its own way; none adds nothing to its start.
	Skills Skills
	// Tap sees each line on the pipes of the program the run's agent runs
	// in, which may serve the thread's later runs too; nil sees none.
	Tap Tap
	// Rewound is the rewind this run takes up, nil when there is none. A
	// program the thread's earlier runs left holds the conversation before
	// it, which the run's agent never runs in.
	Rewound *Rewound
}

// Skills are Glitched's skills a run's agent gets. Root is a Claude Code
// plugin folder that holds them, each in skills/<name>, named by what they
// are, so that another set of skills is another Root; "" for none.
type Skills struct {
	Root string
	List []Skill
}

// Skill is one of Skills: Path is its SKILL.md in Root.
type Skill struct {
	Name, Description, Path string
}

// Skilled is an Adapter that hands its agents Glitched's skills: Delivery
// says how, in words for its user.
type Skilled interface{ SkillDelivery() string }

// Tap sees a line on one of a provider program's pipes as the adapter
// read it, or wrote it whole, in the protocol the adapter speaks to the
// program, on the adapter's own goroutine, so it must not block: at most
// TapCap bytes of it, and how long it was, both without its line ending.
// The line is the caller's once Tap returns. A nil Tap sees nothing.
type Tap func(proto Protocol, pipe Pipe, line []byte, size int)

// TapCap is the most of a line a Tap sees.
const TapCap = 64 << 10

// Line has t see line, less its line ending.
func (t Tap) Line(proto Protocol, pipe Pipe, line []byte) {
	if t != nil {
		line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		t(proto, pipe, line[:min(len(line), TapCap)], len(line))
	}
}

// Protocol is what an adapter speaks to its provider's program.
type Protocol string

const (
	ClaudeStreamJSON Protocol = "claude" // Claude Code's stream-json
	CodexAppServer   Protocol = "codex"  // Codex's app-server
	ACP              Protocol = "acp"    // the Agent Client Protocol
)

// Pipe is which of a provider program's pipes a line went over.
type Pipe string

const (
	Stdin  Pipe = "stdin"
	Stdout Pipe = "stdout"
	Stderr Pipe = "stderr"
)

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
	// ToolTimeout is how long the agent waits on one of its tools' calls,
	// where the agent takes one: 0 leaves its default.
	ToolTimeout time.Duration
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

// Start sends the run's message. Do returns nil only once the message is
// written to the agent, so a run that takes up a rewind is known to have
// handed it what the message holds; an error means it was never sent.
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

// Plan is the plan the agent works to, whole, as it last said it: an
// adapter sends it each time the agent changes it, and one with no steps
// when the agent clears it.
type Plan struct{ Steps []api.PlanStep }

// PlanStatus is a step's status as an agent names it: in_progress or
// inProgress is active, completed done, and anything else, as cancelled
// or a status Glitched doesn't know, pending: not done, nor at work.
func PlanStatus(s string) api.PlanStatus {
	switch s {
	case "completed":
		return api.PlanDone
	case "in_progress", "inProgress":
		return api.PlanActive
	}
	return api.PlanPending
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

// Context is how full the session's context is, as of the agent's last
// request to its model: Used tokens of a Window, 0 where the provider
// doesn't say, and the Model the request went to, "" where it doesn't.
// Compacted says the provider just compacted the session, so what it
// holds is unknown until the next report; PreTokens and PostTokens are
// its token counts before and after when the provider reports them.
// CacheExpires is when the prompt cache created by the request expires,
// zero when the provider didn't report a TTL.
type Context struct {
	Used, Window          int64
	Model                 string
	Compacted             bool
	CacheExpires          time.Time
	PreTokens, PostTokens int64
}

type TurnEnded struct {
	Outcome api.Outcome
	Error   string
	Tokens  int64 // what the turn used, where the provider says
	// Limit is, on a failed turn, the provider's usage limit that failed
	// it; nil when none did or the provider can't tell.
	Limit *api.UsageLimit
	// Head is where the turn left the conversation, in the adapter's own
	// terms, which RewindSpec gives it back: "" where it can't say.
	Head string
}

// Granted is the access the run's agent runs at, as its provider
// confirmed it: in the session's permission mode the provider reports,
// Mode, or as the program it started took the arguments it was given.
// Access is the Glitched level that mode makes. Why says, in the
// provider's words, why it runs short of the run's access, "" when it
// doesn't. An adapter sends it once it knows, before the turn's first
// work, and again if the provider changes it; it never sends a level the
// provider didn't confirm.
type Granted struct {
	Access api.Access
	Mode   string
	Why    string
}

// Ran is the model and effort the run's agent runs, as its provider
// confirmed them, by the ids an api.ModelRef names them by: the run's
// own where the provider runs those. Effort is "" where the provider
// doesn't say. Why says why they aren't the run's, as when the provider
// substituted another model, "" when they are.
type Ran struct {
	Model  string
	Effort string
	Why    string
}

// Nested is an event of a native subagent's (see api.SubagentDelta):
// Parent is the Item of the run's step that started it, and Event a
// TextDelta, a Work, or a TurnEnded, how it ended, with the tokens it used
// where the provider says.
type Nested struct {
	Parent string
	Event  Event
}

func (Nested) event()           {}
func (TextDelta) event()        {}
func (Granted) event()          {}
func (Ran) event()              {}
func (Work) event()             {}
func (Plan) event()             {}
func (RequestOpened) event()    {}
func (RequestWithdrawn) event() {}
func (TurnEnded) event()        {}
func (Context) event()          {}
