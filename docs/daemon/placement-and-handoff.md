# Placement and handoff

This extends `architecture.md`. With the hub from `control-plane.md`, the hub records `Location`, and a handoff waits for the log to be acknowledged at the hub and then ships only the workspace and provider session files. It covers three features. A thread can be opened on any host, a new thread can be placed on the least busy host, and a thread can be handed off from one host to another.

## Decision

**No CRDT.** Each thread has exactly one owner host at a time. A per-thread `epoch` fences the owner, and a handoff ships the thread to the new owner in two phases. The source host stays the authority until it commits the move.

A CRDT solves concurrent writers that must merge without coordination. A Glitched thread has no concurrent writers. The agent's output is written only by the host that runs it. User intents (send, steer, snooze, rename) are commands that the owner serializes. The hard part of a handoff is deciding who the writer is, which is a fencing problem a CRDT does not solve. Zeron confirms this. It syncs transcripts and its registry as Loro docs, yet its executor is still gated on chat ownership, and a chat never changes host (read in `zeron/ARCHITECTURE.md` §2 and the command plane).

| | CRDT docs (zeron) | Replicated log on every host | **One owner + epoch, ship on move** |
|---|---|---|---|
| Who writes | anyone, merged later | owner, followers mirror | owner only |
| Handoff | still needs an ownership protocol on top | epoch bump, near instant | freeze, ship log and workspace, commit |
| Steady-state cost | doc per transcript, tombstone growth, delta frames needed (zeron measured 1.13 MB per tick before deltas) | every host stores and streams every thread | zero until a move |
| Workspace | not covered | not covered | moved in the same protocol |
| Go support | Loro has no first-party Go binding (guess). Automerge has cgo bindings. | plain SQLite | plain SQLite |
| Fits peer shape | needs a sync service or relay room | needs host-to-host mesh always on | needs host-to-host only during a move |

The replicated log is the strongest alternative. It is worth revisiting only if instant failover of a running thread becomes a requirement. It is not one today.

## Identity and location

A thread's ID is global and never changes. Its host is a separate, versioned fact.

```go
type Location struct {
	Thread ID
	Host   ID     // environment ID of the current owner
	Epoch  uint64 // bumped by every committed handoff
}
```

- `Ref{Env, Object}` stays as the client cache key, but for threads `Env` is a routing hint, not identity.
- Each host's thread row carries `owner_epoch` and a sealed `residency`: `owned | handingOff{transfer, to} | moved{to, epoch}`.
- A command sent to a host that no longer owns the thread is answered with `Moved{to, epoch}`. The client re-sends the same command ID to the new host. The receipts for that thread travel with it, so the retry is still exactly once.
- The client keeps a `Location` table, learned from shell streams and `Moved` replies. A stale entry costs one redirect.

## Placement

Every host publishes a `Capacity` record on its shell stream (the cheap stream the sidebar already reads).

```go
type Capacity struct {
	Slots, Active, Queued int      // Slots is user-configured concurrent runs
	Load1                 float64
	OnBattery, Sleeping   bool
	Providers             []ProviderStatus // installed, authenticated, rate-limited until
	Projects              []Checkout       // repository identity -> local path
}
```

The composer gets a host picker: a specific host, or **Any**. For Any, the client runs one pure function.

1. **Filter.** The host is online, has a checkout of the project's repository (identified by its root commit, after zeron), and has the chosen provider installed, authenticated and not rate-limited.
2. **Score.** Free slots first. Then prefer hosts on AC and not sleeping. Then the host that last ran this project, because its dependencies and build caches are warm. Then the lower `Load1`.
3. **Submit** `thread.create` to the winner. A host never rejects for load. If its slots are full, the run waits in its queue.

Two clients placing at the same moment can briefly overfill one host. That costs a queued run, not a failure, which is acceptable for a single user.

Placement happens when the thread is created. A thread that is already running is never moved automatically. Rebalancing is limited to threads that were placed with Any and have not started their first run. They have no worktree yet, so moving one is cheap. Such a thread may re-place itself if its host stays full for longer than a set time. This is phase two.

Rate limits are usually per account, not per machine. If every host uses the same Claude or Codex subscription, balancing spreads CPU, disk and build load, but not token budget (inferred). The filter still skips a host whose provider reports a limit, so a host on a different account can take the work.

## Handoff

A handoff is user-initiated (`Move to…` on a thread) and reuses the two-phase shape of the update flow. The command is `thread.move{to, when}`. `when` is `afterTurn` (default) or `now`, which interrupts the active run first.

```
client   asks target for a one-time import ticket (scoped to this thread and transfer ID)
client → source  thread.move{to, ticket, transferID}
source   waits for idle (or interrupts), then commits residency = handingOff
         new commands are still admitted to the thread queue and travel with it
source   builds the bundle
source → target  stream bundle (direct route, or relayed through the client if hosts can't reach each other)
target   imports into a staging area, reports prepared(transferID)
source   COMMITS residency = moved{target, epoch+1}  <- the single commit point
source   outbox effect: activate(transferID) to target, retried until acknowledged
target   activates, owner_epoch = epoch+1, starts its queue
```

**The bundle contains** the thread row, its event log, queue, open requests (invalidated, since the run that asked has ended), command receipts, checkpoint refs, and the workspace snapshot.

**Workspace snapshot.** The source writes a commit of the worktree, untracked files included, built through a temporary index so the user's real index is untouched. It points at `refs/glitched/handoff/<thread>/<epoch>` and ships with `git bundle` against the commits the target already has. The target fetches it, creates the worktree on the thread's branch, checks out the snapshot tree, and resets the index to `HEAD` so the changes show as uncommitted again. Then it runs the project's setup script.

**Provider context.** Native continuation is tried first. Claude Code keeps sessions as JSONL files under a directory named after the working directory, and Codex keeps rollout files. Copying them and resuming on the target may work, but the files embed absolute paths (guess, needs a spike). If native resume fails, the target starts a fresh provider session with a portable handoff: recent intact messages within a budget, with older text retrievable on demand (t3code's portable handoff).

**Failure cases.**

| Failure | Outcome |
|---|---|
| Target dies before `prepared` | Source aborts after a timeout, residency returns to `owned`, the target discards staging. |
| Target dies after `prepared` | Source may still abort. The target never activates without `activate`, so there are never two owners. |
| Source dies after its commit | On restart its outbox re-sends `activate`. The target acknowledges idempotently by transfer ID. |
| Source dies before its commit | It restarts in `handingOff` and either resumes the transfer or aborts it. Nothing is live elsewhere. |
| Source unreachable for good | The thread stays owned by the source. A forced takeover from an older export is a separate admin action that bumps the epoch and marks the source copy stale when it returns. |

**What does not move.** Terminals and dev servers end on the source, and the thread records that they did. Ignored files (`.env`, `node_modules`) are not copied. The setup script rebuilds dependencies. A per-project `carry` list can name ignored files to include, off by default because it moves secrets. Preview URLs change with the host.

**After the move** the source keeps a read-only tombstone with the transcript up to the handoff, so links and the inbox still resolve. It redirects and can be garbage-collected later.

## Host-to-host trust

Hosts don't trust each other by default. Each transfer is authorized by the client, which already holds Operate grants on both hosts. The target mints a ticket that accepts one bundle for one thread and transfer ID. The source presents it. No standing machine-to-machine grant exists, and the relay can carry the bundle end-to-end encrypted to the target's key.

## Principles applied

- **Model the domain.** Identity and location are split. That one change makes routing, redirects and the epoch fence fall out naturally.
- **Separate before serializing shared state.** Splitting "who writes" from "how data syncs" showed that a CRDT answers the second question, while handoff needs the first.
- **Make operations idempotent.** Transfer ID, the activate outbox effect, and the receipts that travel with the thread make every retry safe.
- **Experience first.** One `Move to…` action. Clients re-route on their own, and placement is a default (Any) rather than a setting to manage.
- **Laziness protocol.** No CRDT, no replicated log, no automatic moves of running threads, no standing host-to-host trust.

## Build order

1. Global thread IDs, `Location`, `owner_epoch`, and the `Moved` redirect. Check: a command sent to the old host lands once on the new one.
2. The host picker and `Capacity` stream with Any placement. Check: with one host's slots full, the next Any thread lands on the other host.
3. Handoff with the workspace snapshot and portable context. Check: kill either host at each step of the protocol and see exactly one owner afterwards, with uncommitted changes intact.
4. Native Claude and Codex session carry-over, behind a spike.
5. Re-placement of unstarted Any threads.
