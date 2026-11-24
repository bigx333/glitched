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

// Command is a client intent. The same ID always returns the same receipt; a
// changed body under a known ID is a conflict.
type Command struct {
	ID       ID
	Target   ID     // the thread
	Revision uint64 // 0 skips the optimistic check
	Expires  time.Time
	Body     Intent
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
}

// TurnInterrupt stops the live run. With Drop, it also cancels the
// messages waiting their turn, held ones too, so no turn starts once the
// run ends: what a delete that waits on the stop sends.
type TurnInterrupt struct {
	Drop bool `json:"drop,omitzero"`
}

// QueueResume releases the messages a Stop held, and with none, has the
// wakes that wait start a turn: the user's go-ahead after a stop. Any
// message the user sends, but a Defer, does it too.
type QueueResume struct{}

type RequestRespond struct {
	Request    ID              `json:"request"`
	Generation uint64          `json:"generation"`
	Answer     json.RawMessage `json:"answer"`
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
}

func IntentType(i Intent) string { return i.intentType() }

type commandWire struct {
	ID       ID              `json:"id"`
	Target   ID              `json:"target"`
	Revision uint64          `json:"revision,omitempty"`
	Expires  time.Time       `json:"expires,omitzero"`
	Type     string          `json:"type"`
	Body     json.RawMessage `json:"body"`
}

func (c Command) MarshalJSON() ([]byte, error) {
	if c.Body == nil {
		return nil, errors.New("api: command without body")
	}
	body, err := json.Marshal(c.Body)
	if err != nil {
		return nil, err
	}
	return json.Marshal(commandWire{c.ID, c.Target, c.Revision, c.Expires, c.Body.intentType(), body})
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
	}
	c.ID, c.Target, c.Revision, c.Expires = w.ID, w.Target, w.Revision, w.Expires
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
// the resume as an unknown command.
const Protocol = 8

type Environment struct {
	ID       ID      `json:"id"` // the host
	Name     string  `json:"name"`
	Protocol int     `json:"protocol"`
	Log      ID      `json:"log"`
	Tailnet  Tailnet `json:"tailnet,omitzero"`
	// Checkpoints is the engine's checkpoint store; zero when it keeps
	// none.
	Checkpoints CheckpointStore `json:"checkpoints,omitzero"`
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
