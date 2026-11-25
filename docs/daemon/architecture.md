# Glitched daemon architecture

This is the synthesis of two candidate designs, `candidate-hub.md` (one server, thin hosts that dial out, after bb) and `candidate-peer.md` (a self-contained daemon per machine, after t3code and zeron). The traced facts behind both are in `research.md`.

## Decision

> **Revised in `control-plane.md`.** Central accounts and ephemeral VMs add a hub that holds accounts, machines, grants, thread locations and a read replica of every log. Hosts still own execution and write their own logs first. The hub runs as a role on one host (`hub.md`), and every route is the tailnet (`network.md`). The rest of this document describes the host engine, which is unchanged.

**Peer shape.** Every machine runs one `glitched` engine that owns its own SQLite, event log, agents, PTYs and worktrees. The GUI embeds that engine when no service runs, and attaches to N engines at once. A phone or a second Mac reaches an engine directly (LAN, Tailscale, SSH) or through an optional outbound relay.

Why peer over hub:

- Glitched is laptop-first and single-user. In the hub shape, closing the laptop lid stops work on every machine, or the server moves to an always-on box and the laptop becomes remote to its own work.
- The hub needs a second durable log on every host (a spool with its own offsets) because the host is not the owner. bb skips that spool and loses events when a daemon crashes. In the peer shape the process that runs the agent is the process that commits the event, so there is one log and one ordering.
- PTY keystrokes and approvals take one hop, not two.

What peer costs, accepted on purpose:

- No global ordering. The inbox across machines is a client-side merge of N shell streams.
- Each machine is paired, updated and backed up on its own.
- A thread has one owner host at a time. Moving it is an explicit two-phase handoff, and only new threads are placed automatically. See `placement-and-handoff.md`.

## Grafted from the hub candidate

- A small command set (about 20 commands) and three roles (Read, Operate, Admin) instead of t3code's 250 RPCs and 17 scopes.
- `Working` is derived from run state and never stored. Inbox disposition (Open, Snoozed, Settled) is a separate user-owned field, so settled-and-snoozed can't be represented.
- Concrete adapter transports. Claude Code uses `stream-json` in and out with its permission-prompt tool routed to Glitched. Codex uses `codex app-server` JSON-RPC. Gemini and Grok use their headless JSON modes with reduced capabilities.
- The mapping from today's prototype to the engine: `send()` becomes `Submit(message.dispatch)`, `t.stop` becomes `turn.interrupt`, snooze and settle become commands, and the mock data becomes a fake `Endpoint` for tests.
- A wake policy table per effect kind (bb), so read-only calls never wake a suspended machine.

## Grafted from the peer candidate

- Sealed unions for run, queue, request and effect state, with SQLite CHECK constraints and a partial unique index that forbids two live runs per thread.
- An `Uncertain` run state. After a crash the engine asks the adapter to reconcile the run. It resumes only when the adapter proves it is the same run, otherwise it marks the run Uncertain and the user restarts it. This replaces blind auto-continue.
- The effective access policy is the intersection of the requested level, the client's grant, the workspace policy and what the adapter can enforce. The UI never promises more than the adapter enforces.
- Shell access is a separate grant, off by default. An agent's access level never implies the right to type into a terminal.
- The relay carries end-to-end encrypted streams pinned to the engine's key. It cannot authorize commands or read transcripts.
- Offline drafts and queued sends stay on the client until a receipt arrives. Interrupts, approvals and keystrokes are never replayed after a reconnect.

## Data shapes

```go
type ID [16]byte                         // ULID; commands are client-minted
type Ref struct{ Env, Object ID }        // client cache key; for threads Env is a routing hint, the ID is global
type Cursor struct{ Log ID; Seq uint64 } // Log changes after a restore, which fences stale cursors

type Command struct {
	ID       ID
	Target   Ref
	Revision uint64    // optimistic concurrency on the target
	Expires  time.Time
	Body     intent    // sealed: dispatch, queueEdit, interrupt, respond, inbox, terminal*, grant*
}
type Receipt struct{ Command ID; Committed Cursor; Revision uint64 } // intent committed, not "done"; Revision is the target's after it

type DispatchMode uint8 // StartNow, AfterActive, SteerActive, RestartActive, Defer (t3code's five)

type Thread struct {
	id, workspace ID
	revision      uint64
	ownerEpoch    uint64   // bumped by each committed handoff
	residency     residency // sealed: owned | handingOff{transfer, to} | moved{to, epoch}
	model         ModelRef // provider, model, effort, fast
	access        Access   // Ask, AutoEdits, Full
	run           run      // sealed: idle | starting | running | waiting{requests} | stopping | uncertain
	disposition   disposition // sealed: open | snoozed{until} | settled
	readThrough   Cursor
	parent        ID       // zero for a thread of the user's; depth is walked, never stored (cap 4)
	tokens        int64    // this thread's own; a parent's total is rolled up at read time
	reported      time.Time // a child's last report, which wakes the thread as a turn's end does
	lastReport    *Report   // that report's child, kind and cursor, committed with it
}

type Request struct { // approval or question
	id, run       ID
	generation    uint64 // fences answers to a restarted run
	dedup         string // provider|providerThread|providerRequestID (bb)
	body          requestBody // approval{operation, scope} | question{schema, choices}
	state         requestState // pending | answered | delivered | invalidated | expired
}
```

A question request is kind `question`, with an `api.Questions` body: 1 to 4 questions, each with 2 to 10 options (Claude Code asks at most 4, agy more), and a `multi` flag for a multi-select one (`internal/api/question.go`). Its answer is `api.Answers`, matched to the questions by position: `answers[i]` answers `questions[i]`, with the labels picked and the user's own words (`other`). No answers is a dismissal. The engine checks the answer fits the questions before the request's race (`fits` in `internal/engine/decide.go`), so a misfit, such as an approval's `{"allow":true}`, is refused and never wins. An approval takes any JSON, as before.

```go
type Event struct {
	Cursor  Cursor
	Entity  Ref
	Type    string
	Schema  uint16
	Payload json.RawMessage // unknown optional types are skipped, the cursor still advances
}

type Grant struct { // one paired client
	Client    ID
	PublicKey [32]byte
	Role      Role // Read, Operate, Admin
	Ceiling   Access
	Shell     bool
	RevokedAt time.Time
}
```

State tables live in `internal/engine` next to the types and are the only place a transition is checked.

| Machine | Transitions |
|---|---|
| Run | idle → starting → running ⇄ waiting. starting, running or waiting → stopping or finished. stopping → finished. crash → reconciling → restored or uncertain. uncertain → finished by explicit user action. |
| Queue | ready or scheduled → claimed(lease, generation) → dispatched. ready or scheduled → cancelled. claimed → retryAt or uncertain. |
| Request | pending → answered → delivered. pending → invalidated or expired. An answer commits together with the effect that delivers it, so only that delivery settles an answered request. |
| Effect | pending → leased → done. An expired lease reconciles, or retries only if the effect kind is replay-safe. |

## Packages

```
cmd/glitched         gui (default) | headless | service install|uninstall|status | pair | update
internal/api         wire types and the Endpoint contract; the only public surface
internal/engine      pure Decide(state, command) -> events, effects; state tables
internal/store       SQLite WAL, migrations, blobs, Commit() = events + projections + receipt + outbox in one tx
internal/runtime     effect worker, dispatcher, reconciliation on boot
internal/provider    Adapter + claude/ codex/ gemini/ grok/
internal/workspace   worktrees, diffs
internal/pty         engine-owned PTYs, replay, input lease
internal/auth        pairing, grants, sessions, revocation
internal/transport   memory, unix socket, tailnet (installed Tailscale or tsnet); see network.md
internal/hub         the hub role one host runs; see hub.md
internal/client      routes, credentials, cursors, offline cache; what the GUI uses
internal/gui         today's MyGo code; imports api and client only
```

```go
type Endpoint interface {
	Info(ctx context.Context) (Environment, error) // identity, protocol version, capabilities, provider catalog
	Read(ctx context.Context, q Query) (Snapshot, error)
	Submit(ctx context.Context, c Command) (Receipt, error) // same ID returns the same receipt; a changed payload conflicts
	Open(ctx context.Context, s Subscription) (Stream, error) // shell | thread | terminal
}

type Adapter interface {
	Describe(ctx context.Context) (ProviderInfo, error) // CLI version, models, efforts, fast, capabilities, enforcement
	Open(ctx context.Context, spec SessionSpec) (Agent, error)
	Reconcile(ctx context.Context, id RuntimeIdentity) (Recovery, error)
}

type Agent interface {
	Do(ctx context.Context, a AgentAction) error // start, resume, steer, interrupt, respond, compact, fork; unsupported returns a typed error
	Next(ctx context.Context) (ProviderEvent, error)
	Close(ctx context.Context) error
}
```

## Flows

**Startup and ownership.** The engine takes an OS lock (`flock` on `~/.glitched/engine.lock`) before opening the database or binding a socket. A second owner exits with a distinct code so launchd doesn't restart it in a loop. The GUI probes the local socket for 750 ms. If an engine answers, the GUI attaches. If not, the GUI takes the lock, embeds the engine in-process and serves the same socket while its window lives. The embedded engine uses the same codec and dispatcher over a memory transport (zeron). Closing the GUI ends an embedded engine, so the GUI offers to install the service.

**Service and update.** `glitched service install` writes a launchd agent or systemd user unit that points at `~/.glitched/app/current`, with KeepAlive on failure only and a captured login-shell PATH (zeron). An update downloads and verifies a signed runtime and waits until no run or PTY is live. It snapshots the database, starts a trial child that migrates and reports `prepared`, then swaps the symlink. The launcher holds the lock fd throughout, and a failed trial restores the snapshot (t3code, simplified).

**Dispatch.**
1. The client mints a command ID and submits `message.dispatch{mode}`.
2. `Decide` checks the access ceiling and writes the queue row. `Commit` stores events, projections, the receipt and a `turn.start` effect in one transaction.
3. The dispatcher, woken by named reasons (queued, turn ended, timer), claims the row with a lease and generation.
4. The effect worker drives the adapter. Each provider event is normalized, committed and then broadcast. Text deltas batch at 50 ms and flush at once on requests and turn boundaries.

**Resync.** A subscription captures a fence, sends the snapshot at the fence, then the buffered tail, then `synchronized` (t3code). A reconnect supplies its cursor. The engine replays from the log if the gap is at most 128 events and 1 MiB, and otherwise sends a fresh snapshot. A changed log id forces a snapshot. Each subscriber is capped at 8 MiB, and overflow closes the stream with a resync cursor. Transcripts travel as deltas from day one (zeron measured 1.13 MB per tick with full frames).

**Approvals.** The adapter surfaces a request, which is deduplicated, persisted, and shown in the inbox as NeedsYou. The first `respond` with a matching generation wins, and later ones get `already answered`. Expiry or invalidation interrupts the blocked run. A request that can't be delivered is invalidated, never silently approved. The Full-access hold gesture submits a policy change, and the engine refuses it above the client's ceiling.

**Child threads.** The full design, with files, failure cases and open questions, is in `child-threads.md`. A thread can start child threads on its own engine (only an agent starts one, by a `create_chat`-like tool planned for a follow-up PR; the user never does). The tree is a `parent` on each thread, as bb's `parent_thread_id`; depth is found by walking up, capped at 4, and the walk is bounded so a corrupt cycle can't hang it. A parent deleted leaves its children standing as threads of their own.

- *State.* Run state and NeedsYou stay apart: a run blocked on a request is `waiting`, still a run, and the thread shows NeedsYou beside it. bb folds both into one status, so a blocked child reads as idle.
- *Push-back.* When a child's run completes, fails or blocks on a request, `Observe` puts a `child.report` effect in the outbox in the same transaction as the run's event. The runtime reads what came of it, a line or two of the run's last reply, the failure, or the request, and commits it in the parent's log as `child.reported` with the effect's completion; that wakes the parent as a turn's end does: unread, and back in the inbox from snoozed. A settled parent stays settled, as its tree was halted with it. bb keeps its notices in memory and loses them on a crash; here they are outbox rows, replayed on boot. A run the user stopped reports nothing.
- *Guardrails.* The engine refuses a child wider than its parent, so access only narrows down the tree. The client's grant ceiling would apply on top; today `engine.ceiling()` is Full, so it narrows nothing.
- *Cascades.* Delete of a thread takes its whole subtree, after a confirm that states the count and how many are still working; the engine ends a deleted thread's run in the delete's commit, with an outbox effect that closes its agent, so a crash before the close leaves it to the next host. Settling a thread settles its whole subtree; the engine stops a settled thread's run, cancels what waits on it, and starts nothing on it until a message, which brings the thread back to the inbox. Both take the tree in the command's own commit, a child started a moment before included, so the GUI sends one command and a crash leaves all of it or none. The Agents panel's Stop all stops what works under a thread, one command per thread.
- *Usage.* Each thread stores the tokens its turns used; a parent's total is its own plus its descendants', summed when read, so a moved or deleted child never leaves a stale total.

Protocol 4 adds the parent, the tokens and the reports; a protocol 3 peer would show children as threads of the user's with nothing reported, so it is refused. Protocol 5 adds the parent's last report to its view, so a client tells of each report from the shell stream alone. Protocol 6 has settle and delete take the tree in the engine, so a protocol 5 peer, which sends or expects a command per thread, is refused.

**Terminals.** The engine owns PTYs per workspace. They survive the GUI detaching but not an engine restart. Output carries byte offsets with 4 MiB of replay and pause/resume flow control at 1 MiB and 512 KiB (bb). One client holds an expiring input lease while others watch. The libghostty view needs a bytes-in, input-out mode instead of owning its PTY.

**Worktrees and forks.** Threads can use a worktree under `~/.glitched/worktrees/<project>/<thread>`. Forking from a finished turn creates a separate thread and does not change the source conversation or roll back its files. A same-provider fork can branch the provider's session where supported; otherwise, including Continue on another provider, the new session gets a handoff containing the conversation through the selected turn.

**Forks.** A `thread.create` with a `fork` (protocol 16) makes a thread of the user's that goes on from a finished turn of another, on the same engine, its last one when it names none. The store resolves the origin in the create's commit, from the source as it is then (`thread.created`'s `fork`: its title, provider, conversation's session and the runs that conversation holds), so a rewind or a delete of the source since changes nothing of it. The fork's first run takes the source's conversation up as a rewind's next run does. On the source's provider, the adapter forks it at the turn: Codex with `thread/fork` through the turn's id, checked with `thread/turns/list`, and Claude at the launch with `--resume` of the source's session, `--resume-session-at` and `--fork-session`, where the installed program holds them. Elsewhere, on another provider (Continue on) or where the adapter can't, the run starts a new conversation and its message hands the agent the turns up to there, newest first, in at most 48 KiB, again with the next message if the first never reached the agent; a slash command (a slash and a name, not a path) goes as it is, and the turns go with the message after it, in the conversation the command ran in. A handoff of a fork, the fork's own or a portable rewind's of it, walks the fork's line back through each source to the turn it forked at, as T3 walks a fork's ancestors. The run's `session.bound` says which, as a rewind's does. The source's session and log are never touched. The GUI forks from Fork from here under a turn's last reply, and from a thread row's Fork and, for a thread on this machine, Continue on, and the fork's timeline starts with the thread it goes on from.

**Reach and pairing.** The engine listens on a Unix socket locally and on the tailnet for everything else (`network.md`, which replaces the LAN, SSH and relay routes). A client checks that the route answers with the expected environment id before sending credentials, and never falls back to loopback (t3code). `glitched pair` shows a 5-minute single-use QR holding the engine's fingerprint and a bootstrap secret. Redeeming it binds the client's public key to a grant. Sessions are short-lived, and a WebSocket ticket keeps tokens out of URLs. Revoking a grant closes its sockets, releases its terminal leases and rejects its queued commands at dispatch. Local IPC checks the OS peer identity plus an app credential. Nothing is anonymous, loopback included (unlike bb and zeron).

**Multi-machine in the GUI.** The GUI keeps one client per environment, each with its own cursor and offline cache keyed by `Ref`. Projects carry a machine badge. The inbox merges the shell streams. Quick-ask and the composer pick a host, or Any, which places the thread by published capacity. Threads move between hosts with `thread.move` (`placement-and-handoff.md`).

**Notifications and integrations.** A delivery outbox sends local notifications and, opt-in, redacted push through the relay (thread id and kind only). Push is a hint to refetch and can never approve. `/mcp` (stdio or bearer HTTP) maps a small allow-list onto the same `Endpoint` with its own restricted grant. Webhooks are HMAC-signed with a replay window and delivery-id dedup.

## MVP, each step ending in a check

1. `engine` + `store` + fake adapter, embedded in the GUI. Check: kill the process between commit and effect, and verify one admission and that replaying the log yields identical projections.
2. Claude Code adapter, access levels and approvals. Check: a real turn streams into the existing chat, a tool prompt appears in the inbox, and two clients race an answer with exactly one winning.
3. Codex adapter, queue modes and edits. Check: a crash during a turn ends restored or Uncertain, never as a duplicate start.
4. Service install and attach, engine PTYs feeding libghostty, worktrees and diffs. Check: close the GUI, reopen it, and the scrollback and diff are intact.
5. Pairing, Tailscale and SSH routes, N environments, revocation. Check: a second Mac pairs, revoking it cuts its socket, and a restored database fences its old cursor.
6. Signed updates, Gemini and Grok behind capability gates. Check: a failed trial migration leaves the old runtime and data working.
7. Push, MCP and webhooks. Check: everything still works with push unavailable.

## Top risks

1. **Provider CLI drift.** Four unstable JSON grammars. Pin versions, keep golden transcripts per adapter, and gate features on `Describe`.
2. **Execution uncertainty after a crash.** Surface it as Uncertain instead of replaying start, steer or approve.
3. **SQLite, git and process boundaries.** Use intent journals and hash preconditions, and fault-inject every boundary.
4. **Remote shell and approval authority.** Scoped grants, generation fencing, and a revocation check on every dispatch.
5. **MyGo terminal coupling.** Prove libghostty rendering from a byte stream before promising remote terminals.

## Not building

Replicated databases or CRDT transcripts, automatic moves of running threads, account-wide trust, a hosted gate as the only remote path, full-trust plugins, credential forwarding between machines, server-side browser tabs, WebRTC previews, port tunnels, and autonomous commit and push.
