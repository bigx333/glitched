# Placement and handoff

A thread can be opened on any host, a new thread can be placed on the least busy host, and a thread can be moved from one host to another. The hub records where every thread lives (`hub.md`). Hosts own execution.

## Decision

**No CRDT. One owner host per thread, fenced by an epoch, and the hub's location record is the only commit point for a move.**

A CRDT solves concurrent writers that must merge without coordination. A Glitched thread has no concurrent writers. The agent's output is written only by the host that runs it. User intents (send, steer, snooze, rename) are commands that the owner serializes. The hard part of a handoff is deciding who the writer is, which is a fencing problem a CRDT does not solve. Zeron confirms this. It syncs transcripts and its registry as Loro docs, yet its executor is still gated on chat ownership, and a chat never changes host (read in `zeron/ARCHITECTURE.md` §2 and the command plane).

| | CRDT docs (zeron) | Replicated log on every host | **One owner plus epoch, the hub commits the move** |
|---|---|---|---|
| Who writes | anyone, merged later | owner, followers mirror | owner only |
| Handoff | still needs an ownership protocol on top | epoch bump, near instant | freeze, upload to the tail, ship the workspace, one CAS at the hub |
| Steady-state cost | doc per transcript, tombstone growth, delta frames needed (zeron measured 1.13 MB per tick before deltas) | every host stores and streams every thread | the upload the hub replica already does |
| Workspace | not covered | not covered | moved in the same protocol |
| Go support | Loro has no first-party Go binding (guess). Automerge has cgo bindings. | plain SQLite | plain SQLite |

The replicated log is the strongest alternative. It is worth revisiting only if instant failover of a running thread becomes a requirement. It is not one today.

## Identity and location

A thread's ID is global and never changes. Its host is a separate, versioned fact that the hub holds.

```go
type Location struct {
	Thread ID
	Host   ID     // machine ID of the current owner
	Epoch  uint64 // bumped by every committed handoff
}
```

- The hub creates a `Location` with epoch 1 when a host's first upload for a new thread arrives. Only a handoff changes it, by compare-and-swap on `{host, epoch}`.
- Each host's thread row carries `owner_epoch` and a sealed `residency`: `owned | handingOff{transfer, to} | moved{to, epoch}`.
- The replica refuses an upload whose epoch is older than the location's. That refusal is how a host learns it lost a thread while it was unreachable.
- A command sent to a host that no longer owns the thread is answered with `Moved{to, epoch}`. The client re-sends the same command ID to the new host. Every admission is an event, so the receipts travel in the log and the retry is still exactly once.
- `Ref{Env, Object}` stays as the client cache key, but for threads `Env` is a routing hint, not identity. The client keeps a `Location` table learned from the hub's shell stream and from `Moved` replies. A stale entry costs one redirect.

## Placement

Every host reports a `Capacity` record to the hub, which fans it out on the shell stream.

```go
type Capacity struct {
	Slots, Active, Queued int      // Slots is user-configured concurrent runs
	Load1                 float64
	OnBattery, Sleeping   bool
	Providers             []ProviderStatus // installed, authenticated, rate-limited until
	Projects              []Checkout       // repository identity -> local path
}
```

The composer gets a host picker: a specific host, or **Any**. For Any, the client asks the hub to `place{project, provider}`, and the hub runs one pure function over the capacity records it holds.

1. **Filter.** The host is online, has a checkout of the project's repository (identified by its root commit, after zeron), and has the chosen provider installed, authenticated and not rate-limited.
2. **Score.** Free slots first. Then prefer hosts on AC and not sleeping. Then the host that last ran this project, because its dependencies and build caches are warm. Then the lower `Load1`.
3. The client submits `thread.create` to the winner. A host never rejects for load. If its slots are full, the run waits in its queue.

Two clients placing at the same moment can briefly overfill one host, because `place` is a read and `thread.create` goes to the host. That costs a queued run, not a failure. With the hub down, Any waits and the picker says so (`hub.md`).

Placement happens when the thread is created. A thread that is already running is never moved automatically. Rebalancing is limited to threads that were placed with Any and have not started their first run. They have no worktree yet, so moving one is cheap. Such a thread may re-place itself if its host stays full for longer than a set time. This is phase two.

Rate limits are per account, not per machine. Placement picks a host for CPU, disk and checkout. The account pool picks an account for token budget (`account-pool.md`). A thread can stay on one host and change accounts.

## Handoff

A handoff is user-initiated (`Move to…` on a thread). The command is `thread.move{thread, to, when}`, sent to the hub, which checks the client's Operate grant and that both hosts are enrolled and online. `when` is `afterTurn` (default) or `now`, which interrupts the active run first.

```
client → hub     thread.move{thread, to, when, transferID}
hub → source     freeze{transferID, to}
source           waits for idle, or interrupts; commits residency = handingOff{transferID, to}
                 keeps admitting commands; every admission is an event, so they upload with the log
hub → target     ticket{transferID, thread, source}
source → target  workspace bundle over the tailnet, with the ticket
target           imports into staging, reports prepared{transferID}
hub → source     seal{transferID}
source           stops admitting, answers Moved{to} from now on, uploads to its tail, reports sealed{tail}
target           pulls the log through tail from the replica, reports ready{transferID}
hub              CAS Location {source, e} → {target, e+1}        <- the single commit point
hub → target     activate{transferID}, an outbox effect retried until acknowledged
target           activates with owner_epoch = e+1 and starts its queue
source           commits residency = moved{target, e+1} on the hub's reply, or on its next refused upload
```

Between `seal` and `activate` a client that gets `Moved` holds the command as a pending draft and retries when the hub's shell stream shows the new location (`client-cache.md`).

**What the source ships.** Only the workspace snapshot and the provider session files. The thread row, event log, queue, command receipts and checkpoint refs are projections of the log, which the target rebuilds from the hub replica. Open requests are invalidated, because the run that asked has ended.

**Workspace snapshot.** The source writes a commit of the worktree, untracked files included, built through a temporary index so the user's real index is untouched. It points at `refs/glitched/handoff/<thread>/<epoch>` and ships with `git bundle` against the commits the target already has. The target fetches it, creates the worktree on the thread's branch, checks out the snapshot tree, and resets the index to `HEAD` so the changes show as uncommitted again. Then it runs the project's setup script.

**Provider context.** Native continuation is tried first. Claude Code keeps sessions as JSONL files under a directory named after the working directory, and Codex keeps rollout files. Copying them and resuming on the target may work, but the files embed absolute paths (guess, needs a spike). If native resume fails, the target starts a fresh provider session with a portable handoff: recent intact messages within a budget, with older text retrievable on demand (t3code's portable handoff).

**Failure cases.** The hub's CAS is the only commit. Before it, nothing has moved. After it, the source is fenced by its epoch.

| Failure | Outcome |
|---|---|
| Source unreachable before it acks `freeze` | The hub aborts after a timeout. Nothing changed. |
| Target dies before `prepared` | The hub aborts after a timeout. The source returns to `owned` and admits again. The target discards staging on restart. |
| Target dies after `prepared`, before the CAS | The hub may still abort or commit. If it commits, `activate` is retried from the hub's outbox, and the target activates from staging on restart. |
| Hub dies before the CAS | The source stays in `handingOff`. When the hub returns it resumes the transfer if `prepared` is still valid, else it aborts. The source times out back to `owned` if it hears nothing. |
| Hub dies after the CAS, before `activate` | The hub's outbox re-sends `activate` on restart. The source learns it lost the thread on its next upload, which the replica refuses as stale. |
| Source dies after `freeze` or after `seal` | On restart it finds `handingOff`, asks the hub for the transfer's state, and resumes or aborts. |
| Source unreachable for good | A forced takeover is a separate admin action. The hub bumps the epoch to a new owner, which rebuilds from the replica and the last pushed workspace snapshot. The source's copy is marked stale when it returns. |

**What does not move.** Terminals and dev servers end on the source, and the thread records that they did. Ignored files (`.env`, `node_modules`) are not copied. The setup script rebuilds dependencies. A per-project `carry` list can name ignored files to include, off by default because it moves secrets. Preview URLs change with the host.

**After the move** the source keeps a read-only tombstone with the transcript up to the handoff, so links and the inbox still resolve. It redirects and can be garbage-collected later.

## Trust during a handoff

Hosts don't trust each other by default, and there is no standing host-to-host grant. The client authorizes the move with its Operate grant at the hub. The hub mints a ticket that lets the target accept one bundle for one thread and transfer ID, and the source presents it. The target verifies the ticket against the hub, so a host that is not the named source can't push a bundle.

## Principles applied

- **Model the domain.** Identity and location are split. That one change makes routing, redirects and the epoch fence fall out naturally.
- **Separate before serializing shared state.** Splitting "who writes" from "how data syncs" showed that a CRDT answers the second question, while handoff needs the first. The one shared write, the location, is a single CAS at the hub.
- **Make operations idempotent.** The transfer ID, the activate outbox effect, and the receipts in the log make every retry safe.
- **Experience first.** One `Move to…` action. Clients re-route on their own, and placement is a default (Any) rather than a setting to manage.
- **Laziness protocol.** No CRDT, no replicated log, no automatic moves of running threads, no standing host-to-host trust, and the source ships only what the replica doesn't have.

## Build order

1. Global thread IDs, `Location` at the hub, `owner_epoch`, and the `Moved` redirect. Check: a command sent to the old host lands once on the new one.
2. The host picker, the `Capacity` stream and `place` at the hub. Check: with one host's slots full, the next Any thread lands on the other host.
3. Handoff with the workspace snapshot and portable context. Check: kill the hub, the source and the target at each step of the protocol and find exactly one owner afterwards, with uncommitted changes intact.
4. Native Claude and Codex session carry-over, behind a spike.
5. Re-placement of unstarted Any threads.
