package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// Cursor names a position in one host's log. Log changes when a database is
// restored, which fences cursors taken against the old one.
type Cursor struct {
	Log ID     `json:"log"`
	Seq uint64 `json:"seq"`
}

// Event is one committed fact. Seq is dense per log. Payload decoders skip
// unknown optional types; the cursor still advances.
type Event struct {
	Cursor  Cursor          `json:"cursor"`
	Thread  ID              `json:"thread"`
	Type    string          `json:"type"`
	Schema  uint16          `json:"schema"`
	At      time.Time       `json:"at"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type Access uint8

const (
	AccessAsk Access = iota
	AccessAutoEdits
	AccessFull
)

type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort,omitempty"`
	Fast     bool   `json:"fast,omitempty"`
}

type DispatchMode uint8

const (
	StartNow DispatchMode = iota
	AfterActive
	SteerActive
	RestartActive
	Defer
)

// LimitContinueText is the message that continues a thread whose last
// turn a usage limit ended, sent by Continue or once the limit resets.
const LimitContinueText = "Continue where you left off."

// ContinueText is the message that continues a run left uncertain, sent
// RestartActive: the agent picks up where it left off, and checks first
// that what its last turn started did not already take effect.
const ContinueText = "Continue where you left off. Your last turn was cut short, so a command or other action it started may have finished or may still be running. Before you repeat one that changes state, confirm it did not take effect and is not still running; if you cannot tell, say so instead of repeating it."

// Command is a client intent. The same ID always returns the same receipt; a
// changed body under a known ID is a conflict.
type Command struct {
	ID       ID
	Target   ID     // the thread
	Revision uint64 // 0 skips the optimistic check
	Expires  time.Time
	Body     Intent
	// Caller is the agent's run that sends the command to a thread under
	// its own, as glitched mcp's task tools do; nil for the user's. The
	// commit checks it: see Caller.
	Caller *Caller
}

// Caller is an agent's run acting on a thread under its own. Only a
// message after or into the live run, or a stop that drops nothing, may
// name one, and the engine takes it only from Run,
// the live run of Thread, which is above the target, and only while the
// user hasn't taken the target over: the target's last message or stop
// was an agent's, not the user's.
type Caller struct {
	Thread ID `json:"thread"`
	Run    ID `json:"run"`
}

// Intent is sealed: only the types in intents below implement it.
type Intent interface{ intentType() string }

// ThreadCreate creates a thread; with a Parent, a child of that thread on
// the same engine, at most MaxDepth below a thread of the user's, and at
// its parent's access or less. Only an agent starts a child: Run is the
// parent's live run that asks. Text, when given, is the thread's first
// message, which starts its first turn in the create's commit.
//
// With a Worktree, the host first makes a git worktree of Workspace's
// repository and creates the thread in it: Workspace becomes the folder
// in the worktree, Project the Workspace asked for, and Branch the
// worktree's new branch. Project and Branch given without a Worktree
// are a child's, which runs in its parent's worktree.
//
// With a Fork, the thread is a fork of another thread's conversation,
// which the other thread keeps as it is.
type ThreadCreate struct {
	Workspace string       `json:"workspace"`
	Title     string       `json:"title"`
	Model     ModelRef     `json:"model"`
	Access    Access       `json:"access"`
	Parent    ID           `json:"parent,omitzero"`
	Run       ID           `json:"run,omitzero"`
	Text      string       `json:"text,omitempty"`
	Worktree  *NewWorktree `json:"worktree,omitempty"`
	Project   string       `json:"project,omitempty"`
	Branch    string       `json:"branch,omitempty"`
	Fork      *Fork        `json:"fork,omitempty"`
	// Forked is Fork as the store resolves it in the create's commit,
	// which no client sends.
	Forked *Forked `json:"-"`
	// Remote names a machine this engine holds a link to, by its label,
	// for a child that runs there: the thread here is its placeholder,
	// and never runs an agent.
	Remote string `json:"remote,omitempty"`
	// Origin and Repository make a link's create: a thread here that
	// another machine's agent starts through the link, as a child of its
	// own, in a worktree this engine makes of the checkout of Repository.
	// Only a link sends them, and with them nothing but Title, Model,
	// Access and Text: this engine picks the folder, the worktree and its
	// branch.
	Origin     *Origin     `json:"origin,omitempty"`
	Repository *Repository `json:"repository,omitempty"`
}

// Fork asks for a thread that goes on from Run, a finished turn of
// Thread's, its last one when zero, a thread of the user's on the same
// engine: its first run
// forks Thread's conversation there, natively where the fork's provider
// is Thread's and can, else in a new conversation its first message
// hands the turns up to Run. A fork on another provider is how a thread
// goes on there.
type Fork struct {
	Thread ID `json:"thread"`
	Run    ID `json:"run"`
}

// NewWorktree asks for a thread in a worktree of its own, on a new branch
// from Base, a branch or commit of the repository; "" is its HEAD.
// Guarded has git make it with no hooks and no fsmonitor, as a plugin's
// programs run git: for a worktree a plugin asked for.
type NewWorktree struct {
	Base    string `json:"base,omitempty"`
	Guarded bool   `json:"guarded,omitempty"`
}

// MaxDepth is how far below a thread of the user's a child may be: a
// thread that deep starts no more.
const MaxDepth = 4

type MessageDispatch struct {
	Text string       `json:"text"`
	Mode DispatchMode `json:"mode"`
	// Replaces, with RestartActive, is the uncertain run the message is to
	// replace: the engine refuses it once that run is no longer the live
	// one, as when another message replaced it first, rather than stop a
	// newer run.
	Replaces ID `json:"replaces,omitzero"`
	// Auto is the engine's own continue of a run a restart cut off, or of
	// a turn a usage limit ended once it resets, which it refuses on a
	// child or on a thread settled or snoozed.
	Auto bool `json:"auto,omitempty"`
}

// TurnInterrupt stops the live run. With Drop, it also cancels the
// messages waiting their turn, held ones too, so no turn starts once the
// run ends: what a delete that waits on the stop sends.
type TurnInterrupt struct {
	Drop bool `json:"drop,omitzero"`
	// Tree stops every thread under the thread too, in the same commit.
	Tree bool `json:"tree,omitzero"`
}

// QueueResume releases the messages a Stop held, and with none, has the
// wakes that wait start a turn: the user's go-ahead after a stop. Any
// message the user sends, but a Defer, does it too.
type QueueResume struct{}

// QueueEdit gives a message of the user's that waits its turn new words.
// Like the queue's other intents, it is refused once the message is no
// longer waiting: claimed, sent, cancelled, or a wake the engine queued.
type QueueEdit struct {
	Message ID     `json:"message"`
	Text    string `json:"text"`
}

// QueueCancel takes a message of the user's out of the queue.
type QueueCancel struct {
	Message ID `json:"message"`
}

// QueueMove puts a message of the user's that waits its turn just before
// Before, another, or last with Before zero.
type QueueMove struct {
	Message ID `json:"message"`
	Before  ID `json:"before,omitzero"`
}

// QueueSteer sends a message of the user's that waits its turn now: into
// the live run, as a SteerActive does, or, with none, as the thread's next
// turn. Like a send, it releases what a stop held.
type QueueSteer struct {
	Message ID `json:"message"`
}

type RequestRespond struct {
	Request    ID              `json:"request"`
	Generation uint64          `json:"generation"`
	Answer     json.RawMessage `json:"answer"`
}

// ResumeCancel stops the engine continuing, once its usage limit resets,
// Run, the thread's last turn, which a limit ended: the engine refuses it
// once another turn is the last.
type ResumeCancel struct {
	Run ID `json:"run"`
}

type InboxSet struct {
	Disposition Disposition `json:"disposition"`
}

// ThreadDelete deletes the thread. Its events stay in the log, but no
// command or read reaches it again. With RemoveWorktree, the worktree of
// a thread in one goes too, with whatever is in it, as the user chose; a
// branch with commits that aren't merged stays.
type ThreadDelete struct {
	RemoveWorktree bool `json:"removeWorktree,omitzero"`
}

// ThreadRename retitles the thread. The engine trims the title and refuses
// an empty one.
type ThreadRename struct {
	Title string `json:"title"`
}

// ThreadRevert takes the thread back to a checkpoint of its: its files,
// its conversation, or both. Files puts the files of the thread's worktree
// back as To holds them, after a checkpoint of them as they are, which a
// revert to it undoes; only a thread in a worktree of its own keeps them.
// Conversation rewinds the thread's conversation to the end of To's run,
// so that its next turn follows that run's, and the runs after it are no
// longer its agent's to remember; to a recovery checkpoint, it undoes the
// rewind that took it, files and all, while that is the thread's latest
// revert and no turn came after it. Only a thread of the user's reverts,
// with no run of its tree live.
type ThreadRevert struct {
	To           CheckpointRef `json:"to"`
	Files        bool          `json:"files"`
	Conversation bool          `json:"conversation,omitempty"`
}

// ThreadConfigure changes the model and the access the thread's next turn
// runs at: a turn already running keeps its own. It gives only what its
// sender chose: the model's name, its effort, fast mode, the access. What
// it leaves out stays as the engine has it when it decides, so a configure
// sent again, or chosen on a view the thread has moved past, undoes
// nothing another client changed since. It names no provider: that stays
// the thread's, as its session is that provider's. A child's access can't
// be wider than its parent's, and a narrower access narrows the threads
// under it to it too.
type ThreadConfigure struct {
	Model  *string `json:"model,omitempty"`
	Effort *string `json:"effort,omitempty"`
	Fast   *bool   `json:"fast,omitempty"`
	Access *Access `json:"access,omitempty"`
}

// Onto is the model and access c makes of m and a: what it gives in
// place of theirs, and the rest as they are.
func (c ThreadConfigure) Onto(m ModelRef, a Access) (ModelRef, Access) {
	if c.Model != nil {
		m.Model = *c.Model
	}
	if c.Effort != nil {
		m.Effort = *c.Effort
	}
	if c.Fast != nil {
		m.Fast = *c.Fast
	}
	if c.Access != nil {
		a = *c.Access
	}
	return m, a
}

func (ThreadCreate) intentType() string    { return "thread.create" }
func (MessageDispatch) intentType() string { return "message.dispatch" }
func (TurnInterrupt) intentType() string   { return "turn.interrupt" }
func (RequestRespond) intentType() string  { return "request.respond" }
func (InboxSet) intentType() string        { return "inbox.set" }
func (ThreadRename) intentType() string    { return "thread.rename" }
func (ThreadDelete) intentType() string    { return "thread.delete" }
func (ThreadConfigure) intentType() string { return "thread.configure" }
func (QueueResume) intentType() string     { return "queue.resume" }
func (QueueEdit) intentType() string       { return "queue.edit" }
func (QueueCancel) intentType() string     { return "queue.cancel" }
func (QueueMove) intentType() string       { return "queue.move" }
func (QueueSteer) intentType() string      { return "queue.steer" }
func (ThreadRevert) intentType() string    { return "thread.revert" }
func (ResumeCancel) intentType() string    { return "resume.cancel" }

// StartsTurn is whether i may start a turn of its thread's agent: a
// message, the go-ahead for what a stop held, a queued message sent now,
// or a create with a first message.
func StartsTurn(i Intent) bool {
	switch b := i.(type) {
	case MessageDispatch, QueueResume, QueueSteer:
		return true
	case ThreadCreate:
		return b.Text != ""
	}
	return false
}

var intents = map[string]func() Intent{
	"thread.create":    func() Intent { return new(ThreadCreate) },
	"message.dispatch": func() Intent { return new(MessageDispatch) },
	"turn.interrupt":   func() Intent { return new(TurnInterrupt) },
	"request.respond":  func() Intent { return new(RequestRespond) },
	"inbox.set":        func() Intent { return new(InboxSet) },
	"thread.rename":    func() Intent { return new(ThreadRename) },
	"thread.delete":    func() Intent { return new(ThreadDelete) },
	"thread.configure": func() Intent { return new(ThreadConfigure) },
	"queue.resume":     func() Intent { return new(QueueResume) },
	"queue.edit":       func() Intent { return new(QueueEdit) },
	"queue.cancel":     func() Intent { return new(QueueCancel) },
	"queue.move":       func() Intent { return new(QueueMove) },
	"queue.steer":      func() Intent { return new(QueueSteer) },
	"thread.revert":    func() Intent { return new(ThreadRevert) },
	"resume.cancel":    func() Intent { return new(ResumeCancel) },
}

func IntentType(i Intent) string { return i.intentType() }

type commandWire struct {
	ID       ID              `json:"id"`
	Target   ID              `json:"target"`
	Revision uint64          `json:"revision,omitempty"`
	Expires  time.Time       `json:"expires,omitzero"`
	Type     string          `json:"type"`
	Body     json.RawMessage `json:"body"`
	Caller   *Caller         `json:"caller,omitempty"`
}

func (c Command) MarshalJSON() ([]byte, error) {
	if c.Body == nil {
		return nil, errors.New("api: command without body")
	}
	body, err := json.Marshal(c.Body)
	if err != nil {
		return nil, err
	}
	return json.Marshal(commandWire{c.ID, c.Target, c.Revision, c.Expires, c.Body.intentType(), body, c.Caller})
}

func (c *Command) UnmarshalJSON(b []byte) error {
	var w commandWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	mk, ok := intents[w.Type]
	if !ok {
		return fmt.Errorf("api: unknown command type %q", w.Type)
	}
	body := mk()
	if err := json.Unmarshal(w.Body, body); err != nil {
		return fmt.Errorf("api: %s: %w", w.Type, err)
	}
	// Store the value, not the pointer, so callers switch on value types.
	switch v := body.(type) {
	case *ThreadCreate:
		c.Body = *v
	case *MessageDispatch:
		c.Body = *v
	case *TurnInterrupt:
		c.Body = *v
	case *RequestRespond:
		c.Body = *v
	case *InboxSet:
		c.Body = *v
	case *ThreadRename:
		c.Body = *v
	case *ThreadDelete:
		c.Body = *v
	case *ThreadConfigure:
		c.Body = *v
	case *QueueResume:
		c.Body = *v
	case *QueueEdit:
		c.Body = *v
	case *QueueCancel:
		c.Body = *v
	case *QueueMove:
		c.Body = *v
	case *QueueSteer:
		c.Body = *v
	case *ThreadRevert:
		c.Body = *v
	case *ResumeCancel:
		c.Body = *v
	}
	c.ID, c.Target, c.Revision, c.Expires, c.Caller = w.ID, w.Target, w.Revision, w.Expires, w.Caller
	return nil
}

// Receipt says the intent was committed, not that it is done.
type Receipt struct {
	Command   ID     `json:"command"`
	Committed Cursor `json:"committed"`
	// Revision is the target thread's after the commit: its view at that
	// revision or a later one has what the command did. Zero is a receipt
	// stored before receipts kept it.
	Revision uint64 `json:"revision,omitempty"`
}

type DispositionState uint8

const (
	Open DispositionState = iota
	Snoozed
	Settled
)

// Disposition is the user's inbox state, separate from run state, so
// settled-and-snoozed can't be represented.
// Until is set only when Snoozed.
type Disposition struct {
	State DispositionState `json:"state"`
	Until time.Time        `json:"until,omitzero"`
}

type RunState uint8

const (
	RunIdle RunState = iota
	RunStarting
	RunRunning
	RunWaiting
	RunStopping
	RunUncertain
)

type RequestState uint8

const (
	RequestPending RequestState = iota
	RequestAnswered
	RequestDelivered
	RequestInvalidated
	RequestExpired
)

type Request struct {
	ID         ID              `json:"id"`
	Run        ID              `json:"run"`
	Generation uint64          `json:"generation"`
	Kind       string          `json:"kind"` // approval | question
	Body       json.RawMessage `json:"body"`
	State      RequestState    `json:"state"`
}

// TurnResult is how a turn ended, and the model and access it was
// admitted at.
type TurnResult struct {
	Run     ID        `json:"run,omitzero"`
	Model   ModelRef  `json:"model,omitzero"`
	Access  Access    `json:"access,omitempty"`
	Outcome Outcome   `json:"outcome"`
	Ended   time.Time `json:"ended"`
	// Limit is the usage limit that ended the turn, nil when none did, and
	// ResumeCancelled that the user stopped the engine continuing it once
	// the limit resets.
	Limit           *UsageLimit `json:"limit,omitempty"`
	ResumeCancelled bool        `json:"resumeCancelled,omitempty"`
}

// UsageLimit is a provider's usage limit that ended a turn: Resets is when
// the provider said it resets, zero when it didn't say.
type UsageLimit struct {
	Resets time.Time `json:"resets,omitzero"`
}

// Turn is a thread's live run as its view carries it: the model and
// access the run was admitted at, which a configure since leaves as they
// were. Silent is true for background maintenance runs such as idle
// compaction.
type Turn struct {
	Run    ID       `json:"run"`
	Model  ModelRef `json:"model"`
	Access Access   `json:"access"`
	Silent bool     `json:"silent,omitempty"`
}

// Thread is the projection a client reads. Working is derived from Run and
// never stored.
type Thread struct {
	ID        ID     `json:"id"`
	Workspace string `json:"workspace"`
	// Project is the folder a thread in a worktree was started from, and
	// Branch the worktree's; both "" for a thread that runs in its
	// project's folder.
	Project     string      `json:"project,omitempty"`
	Branch      string      `json:"branch,omitempty"`
	Title       string      `json:"title"`
	Revision    uint64      `json:"revision"`
	OwnerEpoch  uint64      `json:"ownerEpoch"`
	Model       ModelRef    `json:"model"`
	Access      Access      `json:"access"`
	Run         RunState    `json:"run"`
	Requests    []Request   `json:"requests,omitempty"` // pending only
	Queued      int         `json:"queued"`
	Disposition Disposition `json:"disposition"`
	LastTurn    *TurnResult `json:"lastTurn,omitempty"` // the most recent finished turn; nil before one ends
	UpdatedAt   time.Time   `json:"updatedAt"`
	// Parent is the thread that started this one, zero for a thread of the
	// user's. A parent deleted leaves its children standing on their own.
	Parent ID `json:"parent,omitzero"`
	// Tokens is what the thread's own turns used, as their agents told;
	// a parent's total with its children's is summed when read.
	Tokens int64 `json:"tokens,omitempty"`
	// Context is how full the thread's context is, as its agent last said;
	// nil before any agent has.
	Context *Context `json:"context,omitempty"`
	// Reported is when a child last reported back to the thread, which
	// wakes it as a finished turn does.
	Reported time.Time `json:"reported,omitzero"`
	// LastReport is the last child.reported in the thread's log, committed
	// with that event, so the shell stream orders it with every view.
	LastReport *Report `json:"lastReport,omitempty"`
	// Turn is the live run, nil while there is none.
	Turn *Turn `json:"turn,omitempty"`
	// Confirmed is what the thread's provider last confirmed its agent
	// runs at, which Model and Access may be ahead of.
	Confirmed Confirmed `json:"confirmed,omitzero"`
	// Resumable is whether QueueResume would start something: the thread is
	// idle and holds what a stop held, a Defer, or the wakes a stop held.
	Resumable bool `json:"resumable,omitempty"`
	// Deleted is the tombstone: the last view a deleted thread has, which
	// the shell stream sends once so that clients drop it. No read returns
	// one.
	Deleted bool `json:"deleted,omitempty"`
}

// Context is how full a thread's context is: Used tokens of Model's
// Window, 0 or "" where no agent has said. Stale says the agent compacted
// it since Used was read, so what it holds now is unknown. CacheExpires
// is when the provider's prompt cache for the last request expires, when
// the provider reported its TTL.
type Context struct {
	Used         int64     `json:"used,omitempty"`
	Window       int64     `json:"window,omitempty"`
	Model        string    `json:"model,omitempty"`
	Stale        bool      `json:"stale,omitempty"`
	CacheExpires time.Time `json:"cacheExpires,omitzero"`
}

// Report is a child's report as its parent's view carries it.
type Report struct {
	Child  ID         `json:"child"`
	Kind   ReportKind `json:"kind"`
	Cursor Cursor     `json:"cursor"` // the child.reported event in the log
}

// Working is whether the user sees the thread at work: a silent run, such
// as idle compaction, shows nothing.
func (t Thread) Working() bool { return t.Run != RunIdle && t.Run != RunUncertain && !t.Silent() }

// Silent is whether the live run is a silent one.
func (t Thread) Silent() bool { return t.Turn != nil && t.Turn.Silent }

// Stoppable is whether the thread has a run the user can stop: a live
// one that isn't silent.
func (t Thread) Stoppable() bool { return t.Live() && !t.Silent() }

// Live is whether the thread has a run, an uncertain one too. Only an
// interrupt, a settle or a delete ends an uncertain run.
func (t Thread) Live() bool { return t.Run != RunIdle }

// Protocol is the version of what crosses between engines, clients and
// hubs. A peer of another protocol is refused, not half understood:
// version 2 added the thread.deleted tombstone, which a version 1 peer
// would show as an open thread, and an archived disposition, which version
// 3 dropped for Settled and a version 2 peer may still send; version 4
// added child threads and their reports, which a version 3 peer would show as
// threads of the user's with nothing reported; version 5 added a parent's
// last report to its view, which a version 4 peer would drop, and with it
// the notification of a child that waits on the user, and question
// requests, which a version 4 peer would show as raw JSON and answer with
// an Allow the engine refuses; version 6 settles and deletes a thread's
// tree with it, so a version 5 GUI would send a command per thread the
// engine already took, and a version 5 engine would leave a child of a
// settled or deleted thread at work; version 7 changes a thread's model
// and access, which a version 6 engine refuses as an unknown command;
// version 8 has a stop hold the queue, which a version 7 GUI would show as
// queued with no way to resume it, and a version 7 engine would refuse
// the resume as an unknown command; version 9 has a message name the
// uncertain run it replaces, which a version 8 engine would drop and stop
// whatever run is live; version 10 lets an agent message and stop the
// threads under its own, a command whose caller a version 9 engine would
// drop and take as the user's; version 11 edits, cancels, moves and
// steers a queued message, which a version 10 engine refuses as unknown
// commands, and a version 10 GUI would show at its old words and place;
// version 12 reverts a thread's files, which a version 11 engine refuses
// as an unknown command; version 13 syncs the GUI's machine's skills to
// the engines it pairs with, which a version 12 engine would refuse as
// unknown calls and a version 12 GUI would show as the engine's own;
// version 14 says a usage limit ended a turn, and lets the user cancel the
// engine's continue once it resets, which a version 13 engine refuses as an
// unknown command; version 15 rewinds a thread's conversation, which a
// version 14 engine would drop from a revert and put only the files back,
// and a version 14 GUI would show as a revert of files it left; version
// 16 forks a thread, which a version 15 engine would create as a thread
// with none of the conversation it was to go on from; version 17 places a
// child on another machine, which a version 16 GUI would show as a child
// here with an empty transcript; version 18 takes a child from another
// machine's link, whose create, worktree events and child.reply a version
// 17 engine refuses or drops; version 19 undoes a rewind, conversation and
// all, which a version 18 engine refuses as a rewind to a recovery
// checkpoint, and a version 18 GUI would show with the turns it brings back
// still forgotten.
const Protocol = 19

type Environment struct {
	ID       ID      `json:"id"` // the host
	Name     string  `json:"name"`
	Protocol int     `json:"protocol"`
	Log      ID      `json:"log"`
	Tailnet  Tailnet `json:"tailnet,omitzero"`
	// Checkpoints is the engine's checkpoint store; zero when it keeps
	// none.
	Checkpoints CheckpointStore `json:"checkpoints,omitzero"`
	// ProviderLog is that the engine keeps its provider log now, whatever
	// its engine.json says it should from its next start.
	ProviderLog bool `json:"providerLog,omitempty"`
	// Link is the id of the link whose token asked, which the other
	// machine keeps to know the link it added; zero for any other caller.
	Link ID `json:"link,omitzero"`
}

// CheckpointStore is how much disk the checkpoints of threads' files take,
// as git counts the store's objects, and the most they may: a capture
// while Bytes is over Limit is skipped.
type CheckpointStore struct {
	Bytes int64 `json:"bytes"`
	Limit int64 `json:"limit"`
}

type TailnetMode string

const (
	TailnetNone      TailnetMode = ""          // no Tailscale here, and no node of the engine's own
	TailnetInstalled TailnetMode = "installed" // the Tailscale app or tailscaled on this machine
	TailnetEmbedded  TailnetMode = "embedded"  // the engine's own tsnet node
)

// Tailnet is how the engine's machine is on the tailnet.
type Tailnet struct {
	Mode TailnetMode `json:"mode,omitempty"`
	// State is Tailscale's backend state: Running, NeedsLogin,
	// NeedsMachineAuth, Stopped, Starting or NoState.
	State   string     `json:"state,omitempty"`
	AuthURL string     `json:"auth_url,omitempty"` // where to sign in while it needs login
	Name    string     `json:"name,omitempty"`     // the MagicDNS name, without its final dot
	IP      netip.Addr `json:"ip,omitzero"`        // IPv4 if it has one
	Tailnet string     `json:"tailnet,omitempty"`
	User    string     `json:"user,omitempty"`    // the login it's signed in as
	Control string     `json:"control,omitempty"` // the embedded node's control server; "" for Tailscale's
	// Serving is where clients with a token reach the engine on the
	// tailnet, once it listens there.
	Serving string `json:"serving,omitempty"`
}

func (t Tailnet) Running() bool { return t.State == "Running" }

// Query reads one thread when Thread is set, otherwise the shell (every
// thread, no events). Events are those after After.
type Query struct {
	Thread ID     `json:"thread,omitzero"`
	After  Cursor `json:"after,omitzero"`
}

type Snapshot struct {
	Cursor  Cursor   `json:"cursor"` // the fence the snapshot was taken at
	Threads []Thread `json:"threads"`
	Events  []Event  `json:"events,omitempty"`
}

// Subscription is a Query that stays open. One on a thread streams its
// events; the shell streams thread views instead, after a snapshot.
type Subscription Query

type StreamKind uint8

const (
	StreamEvent StreamKind = iota
	// StreamSnapshot replaces everything the client holds for the
	// subscription; sent first, or when the gap is too large to replay.
	StreamSnapshot
	// StreamSynchronized follows the snapshot and buffered tail.
	StreamSynchronized
	// StreamThread replaces one thread of a shell snapshot with its view
	// after a commit that moved it. A commit sends at most one per thread.
	StreamThread
)

type StreamItem struct {
	Kind     StreamKind `json:"kind"`
	Event    *Event     `json:"event,omitempty"`
	Snapshot *Snapshot  `json:"snapshot,omitempty"`
	Thread   *Thread    `json:"thread,omitempty"`
}

type Stream interface {
	Next(ctx context.Context) (StreamItem, error)
	Close() error
}

type Endpoint interface {
	Info(ctx context.Context) (Environment, error)
	Read(ctx context.Context, q Query) (Snapshot, error)
	Submit(ctx context.Context, c Command) (Receipt, error)
	Open(ctx context.Context, s Subscription) (Stream, error)
}

// Upload carries a contiguous run of one host log to the hub replica.
// Events[0].Cursor.Seq may be at or below the acked offset; the hub skips
// what it already has, so a replayed upload is harmless.
type Upload struct {
	Protocol int     `json:"protocol"`
	Host     ID      `json:"host"`
	Epoch    uint64  `json:"epoch"`
	Log      ID      `json:"log"`
	Events   []Event `json:"events"`
}

type UploadAck struct {
	Acked    uint64 `json:"acked"`    // highest contiguous seq the hub holds for this log
	Protocol int    `json:"protocol"` // the hub's
}
