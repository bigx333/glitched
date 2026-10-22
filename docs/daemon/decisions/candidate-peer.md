# Glitched remote daemon: PEER design

Status: one of the two candidates that `../architecture.md` synthesized on 2026-10-20. Frozen. The engine design survives in `../architecture.md`. The per-machine pairing and relay were replaced by the hub and the tailnet (`../hub.md`, `../network.md`).

## Data shapes first

Go sketches; imports omitted. Domain variants and fields remain private, exposed through validating constructors/read accessors. Wire DTOs are decoded into these types, never used directly as domain state.

```go
type ID [16]byte
type Ref struct { Environment, Object ID }
type Cursor struct { Log ID; Sequence uint64 }
type Version struct { Major, Minor uint16 }
type Environment struct {
    ID, Instance ID
    PublicKey [32]byte
    Protocol Version
    Providers []ProviderInfo
}
type ProviderInfo struct {
    Name, CLIVersion, AdapterVersion string
    Models []Model
    Capabilities []string
    Enforcement string
}
type Model struct { ID string; Efforts []string; Fast bool }
type Command struct {
    ID ID; Target Ref; ExpectedRevision uint64
    Expires time.Time; Body intent
}
type QueueItem struct {
    id, thread, submittedBy ID; revision uint64
    prompt Prompt; state queueState
}
type queueState interface { queueState() }
type intent interface { intent() } // sealed variants
type start struct { prompt Prompt }
type enqueue struct { prompt Prompt; position int; notBefore time.Time }
type steer struct { run ID; generation uint64; prompt Prompt }
type interrupt struct { run ID; generation uint64 }
type respond struct { request ID; revision uint64; answer answer }
type Prompt struct {
    Text string; Attachments []BlobRef
    Provider, Model, Effort string; Fast bool; Access access
}
type BlobRef struct { Hash [32]byte; Bytes int64; MediaType string }
type access uint8 // constructor accepts Ask, AutoEdits, Full only
type Receipt struct { Command ID; Committed Cursor }
type Event struct {
    Cursor Cursor; Entity Ref; Revision uint64
    Type string; Schema uint16; Payload json.RawMessage
}
type Snapshot struct { Through Cursor; Data json.RawMessage }
type Thread struct {
    id, workspace ID; revision uint64
    execution execution; attention attention; readThrough Cursor
}
type execution interface { execution() }
type idle struct{}
type running struct { run ID; generation uint64 }
type waiting struct { run ID; generation uint64; requests nonemptyIDs }
type nonemptyIDs struct { head ID; tail []ID }
type stopping struct { run ID; generation uint64 }
type uncertain struct { run ID; reason string }
type attention interface { attention() }
type inbox struct{}
type snoozed struct { until time.Time }
type settled struct{}
type Request struct {
    id, run ID; generation, revision uint64
    providerRequest string; expires time.Time
    body requestBody; state requestState
}
// requestBody = approval{operation,scope} | question{schema,choices};
// answer = allowOnce | deny | questionAnswer{validatedJSON}.
type Grant struct {
    Client ID; PublicKey [32]byte; Role string
    WorkspaceIDs []ID; Ceiling access; Shell bool; RevokedAt time.Time
}
type Terminal struct {
    ID, Workspace, Instance ID; Generation uint64
    AfterByte uint64; InputLease ID
}
type Workspace struct { ID ID; Root, Branch string; BaseOID string }
type Checkpoint struct { ID, Run, Workspace ID; Ref, ManifestHash string }
type Delivery struct { ID ID; Event Cursor; Destination ID; Attempts uint32 }
```

Queue, request and effect states use sealed variants with state-specific fields, validated on database load. Authenticated principal is server-stamped on every command/effect, never trusted from wire input. SQLite CHECK/FK constraints and a partial unique index forbid two live runs per thread.

| Machine | Legal transitions |
|---|---|
| Run | Idle → Starting → Running ↔ Waiting; Starting/Running/Waiting → Stopping/Finished; Stopping → Finished; crash → Reconciling → restored or Uncertain; Uncertain → Finished by explicit resolution |
| Queue | Ready/Scheduled → Claimed(lease,generation) → Dispatched; Ready/Scheduled → Cancelled; Claimed → RetryAt or Uncertain |
| Request | Pending → AnswerCommitted → Delivered; Pending/AnswerCommitted → Invalidated/Expired |
| Effect | Pending → Leased → Done; expired lease → reconciliation or replay-safe retry |

Starting, finished and reconciliation variants are omitted from the sketch. Finished records a run outcome and returns the thread to Idle. Invalid transitions return conflicts; constructor-only domain values prevent mixed states such as settled-and-snoozed. Admission, execution outcome and attention are independent.

## Package layout

`cmd/glitched` selects GUI, `headless`, `daemon install|start|stop|status`, `pair`, `update`. Only `internal/api` exposes the client contract; `internal/client` implements routes, credentials and projection caching. `internal/engine` contains pure decisions/state transitions; `store` owns SQLite WAL, blobs, migrations and transactional outbox; `runtime` owns workers/reconciliation; `provider/{claude,codex,gemini,grok}` owns adapters; `workspace`, `pty`, `auth`, `transport`, `service`, `delivery` hide mechanisms. Existing MyGo code becomes `internal/gui`; it imports api/client, never store/providers.

Copy zeron's shared engine and t3code's orchestration separation; avoid bb's authoritative hub and t3code's approximately 250-method public surface.

## Key interfaces/signatures

```go
type Endpoint interface {
    Info(context.Context) (Environment, error)
    Read(context.Context, Query) (Snapshot, error)
    Submit(context.Context, Command) (Receipt, error)
    Open(context.Context, Subscription) (Stream, error)
}
type Stream interface {
    Next(context.Context) (Frame, error)
    Control(context.Context, Control) error // ack, PTY input/resize
    Close() error
}
type Adapter interface {
    Describe(context.Context) (ProviderInfo, error)
    Open(context.Context, SessionSpec) (Agent, error)
    Reconcile(context.Context, RuntimeIdentity) (Recovery, error)
}
type Agent interface {
    Do(context.Context, AgentAction) error
    Next(context.Context) (ProviderEvent, error)
    Close(context.Context) error
}
func Decide(State, Command) ([]DomainEvent, []Effect, error) {
    panic("not implemented")
}
func Bootstrap(context.Context, Options) (Endpoint, io.Closer, error) {
    panic("not implemented")
}
```

Query, Subscription, Frame, Control, AgentAction and Recovery are sealed unions. Actions include start/resume, steer, interrupt, respond, snapshot, compact and optional fork; unsupported actions return typed errors. Frames cover snapshot, event, transcript delta, PTY bytes, synchronized and gap. Separate stream subscriptions share this contract; PTY traffic cannot starve command receipts.

## Flows and decisions

**Ownership/service/update.** Every environment owns its database, event log, credentials, filesystem and processes. Stable environment identity/key survives upgrades; instance identity changes each boot; log identity changes after restore. Acquire an OS advisory lock before opening the database or binding. GUI probes authenticated local IPC; if unavailable it attempts the lock, embeds only after winning, otherwise waits for owner readiness. Embedded calls use the same codec/authorization/dispatcher through memory transport and expose authenticated local IPC. Closing an embedded GUI ends its engine; offer service installation for persistence. Installation drains/stops embedded ownership before launchd/systemd starts the owner; never transfers live database ownership.

One binary has versioned runtime directories and a tiny supervised launcher. User-level services use a captured, filtered login-shell PATH; provider credentials stay on that machine. A second owner exits distinctly without restart loops. Update downloads a signed, hash-verified runtime, defers while runs/PTYs remain live, snapshots the database, trials bind+migration, then commits the launcher pointer after prepared/ready handshake. The launcher retains the inherited ownership-lock FD throughout trial/rollback; only one child opens data. Failed trial stops completely before database restore; no downgrade against a migrated database. Copy zeron's embedding/service ergonomics and t3code's locking/update handshake; avoid hourly forced restarts and terminal loss during updates.

**Wire/resync.** Versioned JSON envelopes over authenticated WebSocket; HTTP handles bounded blobs/snapshots. Major mismatch rejects before mutation; minor additions require capability negotiation. Required unknown semantics force upgrade; optional unknown events advance cursors. Commands carry IDs, revisions and expiry; errors distinguish conflict, unsupported, expired, denied and unavailable. Read-only/operate/admin roles replace numerous scopes.

Subscribe atomically captures fence S and registers delivery after S; send snapshot at S, then buffered tail, then Synchronized. Reconnect supplies log identity/cursor; replay retained events or force snapshot on gap/restore. Shell summary subscription covers every thread; detail streams cover selected threads. Persist normalized transcript deltas before broadcasting, batch at ≤50ms, flush immediately at requests/boundaries. Bound subscribers to 8MiB; overflow closes with a resync cursor. Copy t3code's fenced subscriptions and zeron's delta lesson; avoid bb's refetch-only bus and full-transcript ticks.

**Durability/queue.** One transaction commits command ID+payload hash, events, projections, queue edits and effects; receipt means intent committed. Duplicate ID returns its receipt; changed payload conflicts. Client keeps offline drafts/eligible enqueue intents locally until receipt, with expiry; stale interrupt/approval/keystroke actions never replay offline. Queue supports edit/reorder/cancel by revision, scheduled readiness and explicit promotion to steer. Claim leases and generation fencing serialize dispatch.

Workers persist runtime identity and normalized output. Exactly-once admission does not imply exactly-once CLI effects: crashes between execution and recording completion require provider reconciliation. Reattach only when identity proves the same run; otherwise mark Uncertain and require explicit restart. Replay only effects declared safe, never blind start/steer/approval retries. Copy t3code's atomic outbox and bb's claim model; avoid bb's in-memory event loss and zeron's blanket exactly-once implication.

**Providers/access/questions.** Adapter workers isolate CLI grammars via versioned stdio; probe installed CLI/version and advertise tested capabilities rather than assuming equal support across Claude Code, Codex, Gemini and Grok. Pin supported installations; no package installation mid-turn. Missing structured approvals, steering or restore disables that feature. Provider switch starts a new provider session with explicit transcript context. Copy bb's versioned bridge and zeron's pinned installs; avoid pretending CLIs share capabilities or native continuity.

Effective policy is the intersection of requested access, client grant, workspace policy and adapter enforcement. Ask prompts for external writes/network; AutoEdits allows workspace edits but prompts for execution/network/external writes; Full allows host-user privileges within the grant. Disable unsupported policy combinations; change misleading prototype descriptions to these concrete guarantees. Full's hold gesture submits a policy request; it is not enforcement. Policy changes affect subsequent runs; tightening active policy interrupts/reconfigures before continuing.

Deduplicate requests by provider session/request ID plus run generation. Persist approval operation/scope or typed question schema before showing Inbox. Response CAS accepts one answer across clients, checks generation/expiry/access, then delivers through the effect journal. Unknown delivery reconciles or invalidates, never silently approves. Disconnection leaves requests pending; unrecoverable restart invalidates them. Expiry/invalidation interrupts blocked execution. Copy t3code's runtime requests and bb's registry; avoid zeron's unattended default and relying on a GUI-only safety toggle.

**Terminals/workspaces.** Daemon owns bounded PTYs per workspace, surviving detach, not daemon restart. Stream byte offsets with acknowledgments and 4MiB replay; gaps explicitly reset the renderer. One expiring input/resize lease, many viewers; pause/resume bounds backpressure. Input is ephemeral and never retried. Create/input/resize require operate, workspace scope and explicit Shell grant, false by default; this independently authorizes arbitrary shell execution beyond agent access policy. Viewers cannot inject input. MyGo needs a VT renderer/transport seam replacing `terminal.New` PTY ownership; missing directories error instead of falling back home. Copy bb's replay/flow control and zeron's detach behavior; avoid GUI-close hangups.

Default each thread to a daemon-local worktree; explicit shared-directory mode serializes daemon writers. Store baseline/turn checkpoints under hidden Git refs, including declared untracked files with size/secret exclusions. Diffs reference immutable base/head plus content hashes; external edits invalidate stale apply. Rollback creates a restored worktree for review, never blindly resets a dirty directory. Filesystem operations use an intent journal and reconciliation because SQLite cannot transact Git. Copy t3code's checkpoint refs and worktree operations; avoid treating checkpoints as backups or provider rollback as filesystem rollback.

**Reach/pairing/multiple machines.** Prefer LAN, Tailscale, SSH forwarding, then outbound broker, with preflight identity proof before credentials and no loopback fallback. Loopback/Unix socket is default; opt-in LAN uses pinned TLS. SSH validates host keys and tunnels to authenticated IPC. Both daemon and client dial the relay; multiplex opaque, end-to-end authenticated/encrypted streams to the pinned environment key. Broker cannot authorize commands or store transcripts; disconnect never stops agents. Copy t3code's verified route selection and zeron's outbound relay; avoid zeron's account-wide trust, URL tokens and hosted-only reach.

Local admin generates a five-minute single-use pairing QR containing environment fingerprint and bootstrap secret; bind the approved client's public key and grant. Short-lived proof-bound sessions ride authorization headers; secrets use secure storage/inherited FDs. Revocation closes connections, releases terminal leases and rejects queued commands at dispatch. Local IPC verifies OS peer identity plus an application credential. No anonymous loopback API. Each environment pairs/revokes independently; GUI maintains N clients/cursors and cached offline views keyed by Ref. No global ordering or transparent failover: moving work requires explicit export/import and destination authorization. Peer costs are per-machine maintenance, offline owners and an eventually refreshed aggregate inbox. Copy bb's instance distinction; avoid its central watch authority.

**Notifications/MCP/webhooks.** Transactional delivery outbox sends deduplicated local notifications or opt-in redacted push through APNs broker; push only hints to refetch and cannot approve. Include environment/event identity, never transcript/tool arguments. MCP stdio or authenticated HTTP translates a small allow-list into the same Endpoint; automation gets its own restricted grant. Incoming HMAC webhooks enforce replay windows and deduplicate delivery IDs into commands; outbound hooks use signed, filtered events with retries/dead letters. Copy t3code's scoped integrations and zeron's push; avoid full-trust plugins, anonymous endpoints and assuming notification delivery means command execution.

**MyGo consumption.** Replace mock `send` mutation/goroutines with pending receipt markers; event reducers update UI through `win.Update`. Stop shows Stopping until confirmation. Environment-qualified caches replace project-only shell/list/diff keys; drafts/model/access belong to each composer. Catalogs drive model/effort/fast controls. Persist read cursors, absolute snooze deadlines and disposition on the owner; derive Working before disposition, with pending requests surfaced in Inbox. Quick-ask chooses its destination explicitly. Remote previews use authorized port forwarding; host paths open through SSH editor authority. Copy t3code's summary/detail split; avoid treating GUI memory as authority.

## MVP, in verifiable order

1. Embedded engine, SQLite, fake adapter, receipt/event protocol; kill between transaction/effect and verify one admission plus replay-equivalent projections.
2. Claude Code and Codex adapters, enforced access, request races, durable enqueue; interrupt/restart fault tests must yield restored or Uncertain, never duplicate starts.
3. Service attachment, daemon PTYs, worktree/diff/checkpoint restore; close GUI and reattach, verify output replay and dirty-file conflict protection.
4. LAN/Tailscale/SSH pairing and N environments; revoke one client, break a route and restore a database, verify identity/cursor fencing.
5. Signed launcher updates, Gemini/Grok capability-gated adapters; trial migration failure must restore the prior working runtime/data.
6. Relay, opt-in push, MCP/webhooks; demonstrate direct-only operation, broker outage survival, deduplication and least-privilege rejection.

## Top five risks; exclusions

1. CLI grammar/enforcement drift: pin, probe, contract-test and fail closed.
2. Execution uncertainty: expose it; reconcile identity instead of replaying side effects.
3. SQLite/Git/process boundaries: journal intents, hash preconditions, fault-inject recovery.
4. Remote shell/approval authority: scoped grants, generation fencing, revocation on every dispatch.
5. MyGo's renderer/PTY coupling: prove transport-backed VT rendering before remote-terminal commitments.

Do not build replicated peer databases, CRDT transcripts, automatic run migration, account-wide trust, a plugin sandbox, credential forwarding, remote browser farms, autonomous commit/push, or a general scheduler. Add these only after demonstrated demand; the peer design already pays for independent backups, upgrades and disconnected owners.
