# Glitched remote daemon: HUB design

One `glitched` binary, three roles: **server** (owns SQLite, ordering, auth, the only thing clients talk to), **host** (one per machine; dials OUT to the server; runs provider CLIs, PTYs and git), **gui** (MyGo app; a client of the server).

## 1. Data shapes

```go
package model

type (
    MachineID string; HostInstanceID string; ProjectID string; ThreadID string
    TurnID string; CommandID string /* client-minted ULID */; Seq uint64
    RequestID string; TermID string; DeviceID string
)

type Machine struct {
    ID       MachineID
    Name     string
    Ceiling  Access          // max access any thread on it may run at (bb)
    State    MachineState    // table below
    Instance HostInstanceID  // current process; change = host restarted
    LeaseTil time.Time
    Caps     HostCaps        // provider versions, git, os; pushed at open
}

type Project struct { ID ProjectID; Machine MachineID; Path, Name string }

type Thread struct {
    ID         ThreadID
    Project    ProjectID
    Title      string
    Model      ModelRef
    Access     Access        // <= Machine.Ceiling, enforced on write
    Workspace  Workspace     // Local or Worktree{Branch, Path}
    Run        RunState      // derived from turns; never written directly
    Inbox      InboxState    // user-owned: the sidebar's state
    ProviderSession string   // opaque resume handle from the adapter
    LastSeq    Seq
}

type ModelRef struct { Provider Provider; Model string; Effort Effort; Fast bool }
type Provider uint8 // Claude, Codex, Gemini, Grok
type Access uint8   // AccessAsk, AccessAuto, AccessFull (access.go's three)

// Inbox is a sum type: settled and snoozed can't both hold.
type InboxState struct { Kind InboxKind; Until time.Time /* Snoozed only */ ; Unread bool }
type InboxKind uint8 // Open, Snoozed, Settled. "Working" is NOT stored: it's Run != Idle (inbox.go categoryOf).

type Turn struct {
    ID      TurnID
    Thread  ThreadID
    Cmd     CommandID     // the dispatch that created it
    State   TurnState
    Restarts uint8        // continuation after host crash, max 3 (t3code)
    Checkpoint string     // hidden ref refs/glitched/<thread>/<turn>
}

// Durable queue row (bb's queued_thread_messages, CHECKed in SQL).
type QueuedMessage struct {
    Cmd      CommandID
    Thread   ThreadID
    Pos      int           // reorderable
    Mode     DispatchMode
    Body     Message
    State    QueueState
    NotBefore time.Time    // Scheduled
    Claim    *Claim        // non-nil iff State==Claimed
    Attempts uint8
}
type Claim struct { Instance HostInstanceID; Token string; At time.Time }
type DispatchMode uint8 // StartNow, AfterActive, SteerActive, RestartActive, Defer  (t3code's five)

type Message struct { Text string; Attachments []BlobRef; Model *ModelRef /* override */ }

// The one event log. Server assigns Seq per thread; global Cursor for the shell stream.
type Event struct {
    Thread ThreadID; Seq Seq; Cursor uint64; At time.Time
    Kind   EventKind; Body json.RawMessage   // unknown kinds skipped, cursor still advances
}

type PendingRequest struct {      // approval or question (bb dedup key)
    ID        RequestID
    Thread    ThreadID; Turn TurnID
    DedupKey  string               // provider|providerThread|providerRequestID
    Kind      RequestKind          // ToolApproval, FileEdit, Network, Question
    Prompt    json.RawMessage
    State     RequestState         // Pending, Answered, Expired, Cancelled
    Answer    *Answer
}

type Device struct {             // a paired client (GUI on another Mac, phone)
    ID DeviceID; Name string; Scope Scope; CreatedAt, LastSeen time.Time
    RevokedAt *time.Time; PushToken string
}
type Scope uint8 // Read, Operate, Admin (three, not t3code's 17)
```

### State machines (tables, checked in one place)

```go
type TurnState uint8 // Queued, Starting, Running, AwaitingInput, Interrupting, Done, Failed, Aborted
var turnNext = map[TurnState][]TurnState{
    Queued:        {Starting, Aborted},
    Starting:      {Running, Failed, Aborted},
    Running:       {AwaitingInput, Interrupting, Done, Failed, Aborted},
    AwaitingInput: {Running, Interrupting, Aborted},
    Interrupting:  {Done, Aborted},
}
type QueueState uint8 // Queued, Waiting, Scheduled, Claimed, FailedRetrying, FailedExhausted, Consumed, Cancelled
type MachineState uint8 // Enrolling, Online, Blip(grace 30s), Offline, Suspended, Removed
func Transition[S comparable](table map[S][]S, from, to S) error { panic("not implemented") }
```
`RunState` is a function of the latest turn (Idle/Working/NeedsYou), so the GUI's `statusApproval` can't disagree with the pending-request table.

## 2. Package layout

```
cmd/glitched/        main: gui (default) | serve | host | service install|uninstall|status | pair | update
internal/model/      types + transition tables (above). No IO.
internal/store/      SQLite (WAL), migrations, the single Commit() transaction
internal/orch/       pure Decide(cmd, state) -> []Event, []Effect
internal/server/     HTTP+WS for clients, /host for hosts, auth, fan-out, effect worker
internal/hostd/      host daemon: session, spool, provider supervisor, PTYs, git
internal/provider/   Adapter interface + claude/ codex/ gemini/ grok/
internal/wire/       protocol types, versions, codecs (shared by all three roles)
internal/client/     the Go client the GUI (and `glitched` CLI) uses
internal/reach/      route selection, pairing, relay client
```
Public surface: `wire` + `client`.

## 3. Key interfaces

### Server core
```go
// orch: pure, unit-testable, replayable (t3code Orchestrator).
func Decide(s *store.View, c wire.Command) ([]model.Event, []Effect, error) { panic("not implemented") }

// store: events + projections + command receipt + outbox in ONE tx.
func (s *Store) Commit(c wire.Command, evs []model.Event, fx []Effect) (wire.Ack, error)
// Repeat CommandID returns the stored Ack (idempotency; zeron/t3code receipts).

type Effect struct { ID uint64; Kind EffectKind; Target model.MachineID; Body []byte; Bound bool }
// Bound=true: tied to a host instance; retired (not replayed) when that instance dies.
```

### Client wire (GUI, phone, CLI)
```go
package wire
const Version = 1 // major; mismatch -> 426. Minor features via Caps.

type Command struct { ID model.CommandID; Kind CommandKind; Thread model.ThreadID; Body json.RawMessage }
type Ack struct { Cmd model.CommandID; Seq model.Seq } // "intent committed", not "done"

// ~20 commands, not 250:
// thread.create/update/fork/archive, message.dispatch{mode}, queue.reorder/edit/cancel/promote,
// turn.interrupt, request.respond, inbox.snooze/settle/open, checkpoint.restore,
// terminal.open/input/resize/close, machine.suspend/resume/remove, device.revoke
```

```go
package client
type Conn interface {
    Do(ctx context.Context, c wire.Command) (wire.Ack, error)     // retries with same ID
    Shell(ctx context.Context, after uint64) (<-chan ShellDelta, error)   // thread list
    Thread(ctx context.Context, id model.ThreadID, after model.Seq) (<-chan ThreadDelta, error)
    Terminal(ctx context.Context, id model.TermID, after uint64) (io.ReadCloser, error)
    Blob(ctx context.Context, ref model.BlobRef) (io.ReadCloser, error) // diffs, attachments
    Info() wire.ServerInfo // version, caps, environment id
    Close() error
}
func Dial(ctx context.Context, r reach.Route, cred Credential) (Conn, error) { panic("not implemented") }
func InProcess(s *server.Server) Conn { panic("not implemented") } // memory transport, same protocol
```
Deltas, never full snapshots (zeron's 1.13MB/tick lesson): `ThreadDelta{Reset *Snapshot | Events []Event; Synchronized bool}`.

### Host wire (host -> server, outbound only)
```go
type SessionOpen struct {
    Proto int; Machine model.MachineID; Instance model.HostInstanceID
    Active []model.TurnID; SpoolHigh map[model.ThreadID]uint64; Caps model.HostCaps
}
type SessionGrant struct { Session string; Heartbeat, Lease time.Duration; Watch []model.ThreadID; EnvRev uint64 }

// Server -> host over the control WS:
type HostRPC struct { ID uint64; Effect uint64; Kind EffectKind; Body []byte; Deadline time.Time }
// Host -> server: batched events with host-local spool offsets, acked by offset.
type HostBatch struct { Session string; From, To uint64; Items []HostEvent }
```

### Provider adapter (host side)
```go
type Adapter interface {
    Caps() Caps // Resume, Fork, Steer, Rollback, ApprovalEnforcedBy{Provider|Glitched}, Models []string
    Start(ctx context.Context, s SessionSpec) (Session, error)     // new or resume(ProviderSession)
}
type Session interface {
    Turn(ctx context.Context, m model.Message) error
    Steer(ctx context.Context, m model.Message) error   // ErrUnsupported -> server degrades to AfterActive
    Interrupt(ctx context.Context) error
    Respond(ctx context.Context, id string, a model.Answer) error
    Events() <-chan ProviderEvent // TextDelta, Tool{Start,End}, FileChanged, Request, TurnEnd, Usage, Handle(resume)
    Close() error
}
type SessionSpec struct { Dir string; Model model.ModelRef; Access model.Access; Resume string; Env []string; MCP []MCPServer }
```
Adapters: Claude Code (`claude -p --output-format stream-json --input-format stream-json`, permission-prompt tool routed to Glitched), Codex (`codex app-server` JSON-RPC), Gemini CLI (`--output-format stream-json`; ApprovalEnforcedBy=Provider, Steer unsupported), Grok CLI (headless JSON; same degraded caps). Children get a login-shell PATH snapshot and pinned binary (zeron); caps negotiated per adapter (bb).

## 4. Flows

**Process model / single instance / service.** `serve` and `host` each take an exclusive flock on `~/.glitched/{server,host}.lock` before binding or opening data (zeron). Lock-held exits with code 78 so launchd doesn't spin (t3code). `glitched service install [--server] [--host]` writes a launchd plist / systemd user unit pointing at `~/.glitched/app/current` symlink, KeepAlive on failure only. Secrets reach children via inherited fd (t3code), never argv.

**Update.** Server: download, sha256 verify, start trial child on the existing lock-holder's handoff socket; trial migrates a DB snapshot copy, reports `prepared`, parent swaps symlink and execs; failure rolls back (t3code two-phase, simplified: one parent, no separate launcher). Hosts: on `SessionOpen` proto mismatch the server answers `upgrade{url, sha256}`; host updates only when quiescent (no running turn, no attached PTY; zeron), never downgrades (bb).

**Message dispatch (the core).**
1. GUI mints `CommandID`, sends `message.dispatch{mode}`. 2. `Decide` checks access ≤ ceiling, writes `QueuedMessage(Queued)`, emits `message.queued`; `Commit` returns `Ack{Seq}`. 3. Dispatcher (woken by named reasons: `queued`, `turn_ended`, `machine_online`, `timer`) claims the row for the machine's live instance → `Claimed` + outbox effect `turn.start`. 4. Effect worker sends `HostRPC`. 5. Host appends every `ProviderEvent` to its **durable spool** (`~/.glitched/host/spool/<thread>.jsonl`, fsync on turn boundaries/requests) then ships batches (flush per 50ms text tick, immediately on requests/turn end; bb). 6. Server assigns `(thread, Seq)`, commits, fans out. 7. Server acks spool offsets; host truncates.

**Host crash / blip.** Reopen with same Instance = blip: server diffs `Active` and `SpoolHigh`, host resends from last acked offset. New Instance = restart: server marks old instance's Bound effects retired, open turns `Aborted`, pending requests `Expired`, then auto-continues each aborted turn via `Restarts<3` (t3code), resuming the provider session handle. 30s grace before UI says offline (bb).

**Approvals/questions.** Adapter surfaces `Request`; host dedups by key and ships it; server writes `PendingRequest`, turn → `AwaitingInput`, thread → NeedsYou (inbox). `request.respond` from any device is a command; first answer wins, later ones get `ErrAlreadyAnswered`. Answer goes down as an Effect. Access mapping: Ask = every write/net request prompts; Auto = adapter's own safe-list, prompts on flagged; Full = provider's bypass flag, and the GUI's hold-to-grant gesture issues `thread.update{access: Full}` which the server refuses above `Machine.Ceiling`.

**Reconnect/resync (clients).** Subscribe live first, then read tail; if gap ≤128 events / 1MiB, replay `after`; else `Reset` snapshot; then `Synchronized`. Per-subscriber budget 1000 items / 8MiB; overflow kills the stream and client resubscribes (t3code). Shell stream (thread rows) is separate from per-thread streams. Subscriptions also set the host watch set: providers' verbose tool output and file watching only stream for watched threads (bb).

**Terminals.** PTYs live on the host (that's where the shell is). Host keeps 4MiB scrollback with byte seq; server relays without storing, clients attach `after` seq; flow control pause at 1MiB unacked, resume at 512KiB (bb). Exited PTYs kept 30 min. The GUI's libghostty terminal must be fed bytes instead of owning a PTY (risk 4).

**Worktrees / diffs / checkpoints.** Host-side git: `thread.create{workspace: Worktree}` → effect `git.worktree.add` at `~/.glitched/worktrees/<project>/<thread>`, branch `glitched/<slug>`. At each turn start the host writes `refs/glitched/<thread>/<turn>` (tree snapshot incl. untracked, via temporary index; t3code). Diff = host computes `git diff <checkpoint>..worktree`, uploads as a content-addressed blob; server stores the ref, GUI fetches via `Blob` (diffs.go already renders file lists). `checkpoint.restore` resets the worktree and calls adapter Rollback if supported.

**Remote reach + pairing.** Server listens on loopback only. Client route order (t3code): LAN (mDNS, opt-in bind), Tailscale (`tailscale serve` to loopback), SSH (`ssh -L`), relay. Client verifies the route answers with the expected environment id before sending credentials, never silently falls back. Relay (later) = outbound WS from server to a tiny Cloudflare Worker that multiplexes by connId and never parses RPC (zeron), with an allow-list of methods; deliberately not bb's hosted gate as the only path. Hosts always dial the server over the same routes (usually Tailscale).
Pairing: `glitched pair` prints a QR/URL with a 5-min single-use code → `/pair` exchange → Device + 30-day bearer token; WS upgrade uses a 60s ticket so tokens never appear in URLs (t3code). Host enrollment: `glitched host enroll <url> <code>` with a 15-min single-use key bound to a pre-created MachineID; host stores an Ed25519 key (0600) and signs session opens (bb). Revocation: `device.revoke` / `machine.remove` deletes creds and closes live sockets. The local GUI uses a token in the user's 0600 data dir; nothing is unauthenticated, including loopback (avoids bb and zeron).

**Multi-machine.** Projects belong to one machine; the sidebar groups by project, a small machine badge shows reachability. Suspended machines drain then refuse; wake policy per effect kind (`work` may nudge, `never`).

**Notifications/push.** Server emits `notify` effects on TurnEnd, NeedsYou, Failed. Local GUI posts macOS notifications from the shell stream (notify.go). Phone push via the relay with redacted payloads (title + thread id only). Snooze expiry is a server timer.

**MCP/webhooks.** `/mcp` (HTTP, bearer, Operate scope) exposes create thread / dispatch / read thread / respond to outside agents. Outgoing webhooks HMAC-signed for `turn.completed`, `request.created`.

**GUI: embedded vs attached.** GUI starts, probes `~/.glitched/server.sock` (750ms, zeron). Answers → attach as client. No answer → offer "install service" (default) or run embedded: take the server lock, start server + host in-process, use `client.InProcess` (same protocol, memory transport), and serve the socket for others while the window lives. The `app` struct's `projects []*project` becomes a projection fed by `Shell`/`Thread` deltas through `win.Update`; `send()` becomes `Do(message.dispatch)`; `t.stop` becomes `turn.interrupt`; `snooze()`/settle become commands. Mock data becomes a fake `Conn` for tests.

## 5. Where the hub shape hurts

- **Two hops.** PTY keystrokes and deltas go host→server→client, also for approvals; co-located roles share a process to make it in-memory.
- **Server down = every machine idle.** Laptop lid closed: a Linux host can't take work, the phone sees nothing. Putting the server on an always-on box makes the laptop remote to its own work. The peer shape avoids this.
- **Hosts need a durable spool** (bb loses events on crash): a second log with its own offsets.

## 6. MVP cut (each step verifiable)

1. `model` + `orch.Decide` + `store.Commit`, with table-driven tests of every transition and replay of an event log to identical projections.
2. `serve` with lock, loopback WS, token auth, `client.Conn`; GUI attaches and renders threads from the server (mock adapter). Verify: kill -9 server, GUI reconnects, `Synchronized` appears, no duplicate rows.
3. `host` dial-out with spool + Claude Code adapter, AfterActive/StartNow only. Verify: real turn streams into the existing chat UI; kill -9 host mid-turn → turn Aborted, continuation resumes.
4. Approvals for Claude (Ask/Auto/Full + ceiling). Verify: tool prompt appears in the inbox, answered from a second GUI, first wins.
5. Remote PTY in the terminal drawer. Verify: detach/reattach keeps scrollback.
6. Worktrees + checkpoints + diff blobs feeding diffs.go.
7. Codex adapter; steer/restart modes; queue reorder/edit.
8. `service install`, pairing QR, Tailscale route, revoke. Verify: phone/second Mac pairs, revoke cuts its socket.
9. Second machine enrollment; Gemini/Grok adapters; updates.
10. Relay, push, MCP, webhooks.

## 7. Top 5 risks

1. **Provider CLI drift.** Four unstable JSON grammars; pin versions, golden transcripts per adapter, gate on Caps.
2. **Server-as-single-point** (above); users will put it on the laptop and be surprised.
3. **Exactly-once across three hops** (receipts + spool offsets + claim tokens). Fault-inject drop/duplicate on every message kind.
4. **libghostty remote feed.** MyGo's terminal plugin owns its PTY today; needs a bytes-in/input-out mode upstream.
5. **Access semantics vs provider reality.** "Ask for approval" can't be enforced by Glitched on CLIs that self-enforce; the UI must never promise more than `ApprovalEnforcedBy` allows.

## 8. Will NOT build

- t3code's 250 RPCs and 17 scopes: ~20 commands, 3 scopes.
- Live thread migration; CRDT docs (zeron): the server orders everything.
- Server-side browser tabs, WebRTC previews, port tunnels.
- A hosted account system; the relay is an optional dumb pipe.
- Unauthenticated loopback or tokens in URLs.
