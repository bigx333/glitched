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

type ThreadCreate struct {
	Workspace string   `json:"workspace"`
	Title     string   `json:"title"`
	Model     ModelRef `json:"model"`
	Access    Access   `json:"access"`
}

type MessageDispatch struct {
	Text string       `json:"text"`
	Mode DispatchMode `json:"mode"`
}

type TurnInterrupt struct{}

type RequestRespond struct {
	Request    ID              `json:"request"`
	Generation uint64          `json:"generation"`
	Answer     json.RawMessage `json:"answer"`
}

type InboxSet struct {
	Disposition Disposition `json:"disposition"`
}

// ThreadDelete deletes the thread. Its events stay in the log, but no
// command or read reaches it again.
type ThreadDelete struct{}

// ThreadRename retitles the thread. The engine trims the title and refuses
// an empty one.
type ThreadRename struct {
	Title string `json:"title"`
}

func (ThreadCreate) intentType() string    { return "thread.create" }
func (MessageDispatch) intentType() string { return "message.dispatch" }
func (TurnInterrupt) intentType() string   { return "turn.interrupt" }
func (RequestRespond) intentType() string  { return "request.respond" }
func (InboxSet) intentType() string        { return "inbox.set" }
func (ThreadRename) intentType() string    { return "thread.rename" }
func (ThreadDelete) intentType() string    { return "thread.delete" }

var intents = map[string]func() Intent{
	"thread.create":    func() Intent { return new(ThreadCreate) },
	"message.dispatch": func() Intent { return new(MessageDispatch) },
	"turn.interrupt":   func() Intent { return new(TurnInterrupt) },
	"request.respond":  func() Intent { return new(RequestRespond) },
	"inbox.set":        func() Intent { return new(InboxSet) },
	"thread.rename":    func() Intent { return new(ThreadRename) },
	"thread.delete":    func() Intent { return new(ThreadDelete) },
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
	}
	c.ID, c.Target, c.Revision, c.Expires = w.ID, w.Target, w.Revision, w.Expires
	return nil
}

// Receipt says the intent was committed, not that it is done.
type Receipt struct {
	Command   ID     `json:"command"`
	Committed Cursor `json:"committed"`
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

// TurnResult is how a turn ended.
type TurnResult struct {
	Outcome Outcome   `json:"outcome"`
	Ended   time.Time `json:"ended"`
}

// Thread is the projection a client reads. Working is derived from Run and
// never stored.
type Thread struct {
	ID          ID          `json:"id"`
	Workspace   string      `json:"workspace"`
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
	// Deleted is the tombstone: the last view a deleted thread has, which
	// the shell stream sends once so that clients drop it. No read returns
	// one.
	Deleted bool `json:"deleted,omitempty"`
}

func (t Thread) Working() bool { return t.Run != RunIdle && t.Run != RunUncertain }

// Live is whether the thread has a run, an uncertain one too. The engine
// deletes no live thread, and only an interrupt ends an uncertain run.
func (t Thread) Live() bool { return t.Run != RunIdle }

// Protocol is the version of what crosses between engines, clients and
// hubs. A peer of another protocol is refused, not half understood:
// version 2 added the thread.deleted tombstone, which a version 1 peer
// would show as an open thread, and an archived disposition, which version
// 3 dropped for Settled and a version 2 peer may still send.
const Protocol = 3

type Environment struct {
	ID       ID      `json:"id"` // the host
	Name     string  `json:"name"`
	Protocol int     `json:"protocol"`
	Log      ID      `json:"log"`
	Tailnet  Tailnet `json:"tailnet,omitzero"`
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
