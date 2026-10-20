# Control plane, accounts and caching

> The hub's implementation (Durable Objects in the user's Cloudflare account, no Turso) is chosen in `hub-on-cloudflare.md`.

This revises the decision in `architecture.md`. Three new requirements change it.

1. Agent accounts are managed in one place and every host receives credentials from there (bb's model).
2. Ephemeral VMs become hosts later. They are created and destroyed on demand, and their disks don't survive.
3. Clients cache chats locally so the GUI stays fast without a live round trip to each host.

## Decision

**A hub for control and reads. Hosts still own execution.**

- The hub (`glitched hub`) holds accounts, machines, client grants, thread locations and a read replica of every thread log. It provisions and tears down ephemeral VMs.
- Every host runs the engine as designed. It is the only writer of the threads it owns, and it commits to its own SQLite log first. It dials out to the hub (bb) and streams its log upward from a durable offset.
- Clients pair once with the hub. They read through the hub into a local cache. Commands go through the hub to the owner host. Terminals may connect directly when a route exists.
- On a single laptop the hub and the host run in one process, the same way the GUI already embeds the engine. A solo user installs nothing extra.

Why this beats the pure peer shape now:

- Ephemeral VMs need something always on to create them, hand them credentials and outlive them. A peer mesh has no such place.
- An ephemeral VM's log would die with it. The hub replica makes the host's log durable elsewhere before teardown.
- Central accounts and one pairing replace per-machine pairing, which was the main accepted cost of the peer shape.

Why hosts still write their own log, not the hub (the bb mistake to avoid):

- A laptop keeps running its own threads with no network. The log catches up when the hub is reachable again.
- The host's SQLite log is its spool. bb loses events because its daemon queue lives in memory. Ours is committed before it is sent and is dropped only after the hub acknowledges the offset.

| | Pure peer (previous) | **Hub control + host writers** | Hub owns everything (bb) |
|---|---|---|---|
| Accounts | per machine | central, pushed per run | central |
| Ephemeral VMs | no place to provision from | hub provisions, replica outlives VM | hub provisions |
| Laptop offline | works | works, syncs later | local threads stop |
| Durable logs | one per host | host log + hub replica | hub log + host spool (bb skipped it) |
| Pairing | per machine | once, at the hub | once |
| Handoff | ship log and workspace | log already at hub, ship workspace only | reassign |
| Hops for approvals | 1 | 2 (or 1 direct) | 2 |

## Accounts and credentials

```go
type Account struct {
	ID       ID
	Provider Provider     // claude, codex, gemini, grok, github
	Kind     CredKind     // longLivedToken | apiKey | refreshable
	Label    string       // "work Max", "personal API"
	Secret   sealed       // encrypted at rest on the hub
	Limits   LimitStatus  // rate-limited until, from adapter reports
}
```

- **Delivery per run, not per machine.** When a host starts a run, it asks the hub for the credential of the account bound to that run. The hub checks that the machine is enrolled and the run exists. The host hands the credential to the CLI through an inherited file descriptor or the process env, never through argv or a file in the repo. Claude Code reads `CLAUDE_CODE_OAUTH_TOKEN`, and the bb bundle also references a `CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR` variable (read, not tested). bb passes only `CLAUDE_CODE_OAUTH_TOKEN` and the executable path, with the token minted by `claude setup-token` (read in bb's provider docs).
- **Ephemeral VMs never write credentials to disk.** Persistent hosts may cache them in the OS keychain for offline work.
- **The hub must be able to read credentials** so it can refresh them. Sealing them to host keys was rejected for that reason (see `cost.md`).
- **Refreshable credentials are refreshed only by the hub.** If several hosts share one refresh token and each refreshes it, rotation can invalidate the others (inferred from how rotating refresh tokens work, not observed with Codex). The hub refreshes and hands out access tokens. Long-lived tokens and API keys are preferred where the provider offers them.
- **Revocation.** Removing a machine stops issuance at once. A token already handed to a running CLI can't be recalled. That is why delivery is per run and long-lived tokens are scoped to dedicated accounts where possible.
- **Accounts become a placement input.** Rate limits are per account. With accounts central, placement can pick a different account as well as a different host when one account is limited. That is the load balancing that actually relieves token limits.
- Git credentials and project env secrets follow the same path. bb pushes machine env vars encrypted and revisioned, and forwards git credentials (read).

## Ephemeral VMs

The lifecycle follows bb's machine providers.

- A `MachineProvider` interface has `Create(key)`, `Remove(id)`, `RemoveByKey(key)`, and optionally `Suspend` and `Resume`. Every call is idempotent by key, and checkpointed resources make cleanup retryable.
- Bootstrap installs `glitched` with a one-time enroll key, and the host dials the hub. "Daemon connected" is not "agent ready". Checkout setup and the credential check must pass first (bb makes the same distinction).
- **Teardown.** An ephemeral machine is removed when no live or queued thread needs it. Before removal the hub checks two things. Every owned thread's log has been acknowledged to the tail, and the workspace snapshot has been pushed as a hidden ref to the project remote, using the snapshot format from `placement-and-handoff.md`. A thread then becomes "parked at the hub" and resumes on the next host that takes it.
- Placement treats "create a VM" as a candidate host with a startup cost. It is chosen only when every live host is full.

## Caching on the client

The client keeps a SQLite cache that is a partial replica of the hub.

- **Shell data** (projects, threads, inbox state, locations, capacity) syncs fully and continuously. It is small.
- **Transcripts** sync per query. An open thread, the visible inbox rows and the most recent N threads are kept warm. Each cached thread stores its cursor, and a reopen asks only for events after it. The existing resync rule still applies (gap too large, then snapshot).
- **Reads are local.** The UI renders from the cache and never waits for a host. A host that is offline still has a readable history.
- **Writes stay commands.** The client writes an optimistic pending row keyed by command ID and replaces it when the receipt and events arrive. There is no client-side merge.
- **Eviction** is by last-opened time with a byte budget. Evicted transcripts refetch from the hub on demand.

## Zero

[Zero](https://zero.rocicorp.dev) is the closest existing product to this cache. Its model fits well. Postgres replicates into a SQLite replica inside `zero-cache`. Clients sync only the rows their named queries need and read locally. Writes go through server-side custom mutators, which may be written in any language that speaks the push protocol.

It doesn't fit Glitched as the engine, for three reasons.

- The client library is TypeScript (web and React Native). Glitched's GUI is Go, so we would reimplement the client sync protocol, which isn't a stable public contract before 1.0 (inferred from the 0.x releases).
- It requires Postgres with logical replication behind `zero-cache`. That works on an always-on hub but not inside the laptop's single binary.
- Our source of truth is an append-only event log with cursors, which already gives deltas and resume. Zero would add a second sync layer on top of it.

**Take the model, not the product.** Use named queries scoped by what the screen shows, a local SQLite replica, server-authoritative mutations and optimistic pending rows. Build it on our own log. If a web or phone client in TypeScript appears later, Zero could serve that client from the hub without touching hosts. That is worth revisiting then.

## Changes to other docs

- `architecture.md`: the decision is now hub control plus host writers. Pairing and grants move to the hub. Hosts trust the hub through enrollment.
- `placement-and-handoff.md`: the hub records `Location` authoritatively. A handoff no longer ships the log, which is already replicated. It waits for the source's ack offset to reach the tail, then ships only the workspace snapshot and provider session files. The commit point moves to the hub's location record, which the source requests.

## Principles applied

- **Separate before serializing shared state.** "Who writes a thread" stays with the host. "Who knows everything" moves to the hub. Keeping them apart avoids bb's lost events and the peer shape's missing control point.
- **Experience first.** Solo use still needs no service, because hub and host share one process. Pairing happens once.
- **Make operations idempotent.** Machine provisioning by key, credential issuance per run, and log upload by offset.
- **Laziness protocol.** No Zero dependency, no client merge logic, no credential sync to disk on VMs.

## Build order

1. Hub and host in one process, with the host log streaming to the hub replica by offset. Check: kill the host mid-stream and see no gap or duplicate in the replica.
2. Client cache with per-query transcript sync. Check: reopen a thread with the host offline and it renders from cache.
3. Central accounts with per-run credential delivery for Claude Code. Check: a host with no local login runs a turn, and removing the machine stops the next run.
4. A separate always-on hub with laptop and second Mac enrolled.
5. Ephemeral VM provider with teardown gated on the replica ack and the pushed workspace ref.
