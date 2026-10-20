# Hub on Cloudflare, and where Turso fits

This picks an implementation for the hub described in `control-plane.md`. Three options were compared. One is a Go hub on an always-on box, one runs the hub on Durable Objects (zeron's edge), and one uses Turso for replication.

## Decision

**Run the hub on Durable Objects in the user's own Cloudflare account. Keep our own log-shipping protocol. Don't adopt Turso.**

The hub must be always on once ephemeral VMs exist. Durable Objects give always-on without owning a machine. A Durable Object is also a single-threaded, strongly consistent object with its own SQLite, which is exactly the serialization point a thread's ownership fence needs. Cloudflare Sandboxes (GA in April 2026) then give the ephemeral VM provider from the same account.

| | Go hub on a box | **Durable Objects** | Turso sync |
|---|---|---|---|
| Always on | only if the box is | yes | the database yes, logic no |
| Live streaming to clients | WebSocket server we run | hibernating WebSockets per object | no, `Pull` is a poll |
| Ownership fence | a mutex or SQL CAS in one process | one object per thread, single-threaded | "last push wins" on conflict |
| Command mailbox for a sleeping host | yes | yes, plus wake via alarms | no |
| Ephemeral VMs | call some provider API | Sandboxes in the same account | no |
| Language | Go, same as everything | TypeScript (Go/Rust in Workers go through wasm shims) | Go driver `tursogo` |
| Lock-in | none | Cloudflare (workerd runs locally for dev) | Turso Cloud or its sync server |
| Cost for one user | the box | likely inside the Workers paid plan (guess) | free tier likely enough (guess) |

## Object layout

```
Worker            auth (client grant, host enrollment), routes to objects
UserDO     x1     accounts (sealed), machines, client grants, thread index, capacity, placement, VM lifecycle
ThreadDO   xN     location {host, epoch}, replica log, command mailbox + receipts, client fan-out
MachineDO  xM     the host's one outbound WebSocket, presence, command and PTY relay, wake
R2                attachments, workspace snapshots for parked threads, archived logs
```

- **ThreadDO owns the thread's location.** A handoff commits by compare-and-swap of `{host, epoch}` inside one object, so two owners are impossible by construction. `UserDO` only indexes it.
- **Appends are fenced.** The thread object accepts log appends only from the current `{host, epoch}`, keyed by sequence, so a retried upload is idempotent and a stale host is refused.
- **The mailbox is not the order.** A client command lands in the thread object first, gets a receipt, and is forwarded to the owner host. The host's log decides the order. When the laptop is offline, its embedded engine admits commands locally. On reconnect it uploads them, and the thread object dedups by command ID.
- **Sleeping hosts.** A command for an offline host waits in the mailbox. For a VM, `UserDO` resumes or recreates it through the Sandbox API, which is bb's wake-on-work.
- **Parked threads** (VM torn down) keep their workspace snapshot in R2, not as hidden refs on the project's origin. That keeps the user's remote clean.
- **Hibernation hygiene** from zeron. Use no idle timers, flush only while dirty or with alarms, and use auto-response ping/pong so idle rooms stop billing. In-memory state is rebuilt from SQLite on wake.

Limits checked against Cloudflare's docs: 10 GB of SQLite per object, 2 MB per key and value, 100 KB per SQL statement, and 32 MiB per received WebSocket message. Memory is 128 MB per isolate, shared by co-located objects (zeron's research). A transcript per object fits comfortably. Large tool outputs go to R2.

## Why not Turso

`tursogo` offers a local database that reads and writes offline and syncs with `Push` and `Pull`. Conflicts resolve as "last push wins" (Turso docs). That is a good tool, but it answers a question we no longer have.

- Single writer per thread means there is nothing to merge. What we need is fencing, which "last push wins" does not give.
- Token streaming and approvals need a live socket. `Pull` returns whether anything changed, so we would still run a second channel for live events.
- Partial sync would need one database per thread. That works, but the thread object already is that, with live fan-out built in.
- It adds a third system (hosts, hub, Turso) instead of replacing one.

It would be the right tool for the Go-hub-on-a-box option. There, a self-hosted Turso sync server could replace our upload protocol. It is worth revisiting if we ever leave Cloudflare.

## What this costs

- **Two languages.** The engine and GUI are Go, and the hub is TypeScript. The wire protocol is the contract. It is defined once as JSON schemas and tested from both sides with recorded frames. zeron made the same call for its Rust app and kept its objects in TypeScript (`zeron/docs/research/durable-objects-language.md`).
- **The hub sees plaintext.** It must hold credentials to hand them out, so end-to-end encryption against the hub buys little. The trust boundary is the user's own Cloudflare account, not a third-party service.
- **Solo mode stays hubless.** Without a Cloudflare deployment, the GUI embeds a single engine as in `architecture.md`. The host-side upload code is idle, and no Go copy of the hub is built.

## Principles applied

- **Laziness protocol.** One hub implementation, no Turso, and no Go hub to maintain alongside it.
- **Model the domain.** One object per thread, matching the one-owner-per-thread rule.
- **Make operations idempotent.** Appends keyed by `{host, epoch, seq}`, commands keyed by ID, VM calls keyed by machine key.
- **Experience first.** No box to buy or keep running, and VMs come from the same account.

## Build order

1. Worker + `ThreadDO` replica with fenced appends. A host uploads, and a client reads by cursor. Check: replay a whole upload twice and get the same log.
2. Command mailbox and `MachineDO` relay. Check: a command sent while the host is offline runs once when it reconnects.
3. `UserDO` with sealed accounts and per-run credential issuance.
4. Location CAS and handoff through the thread object.
5. Sandbox VM provider, with parking to R2.

Sources:
- https://developers.cloudflare.com/durable-objects/platform/limits/
- https://developers.cloudflare.com/durable-objects/platform/pricing/
- https://www.infoq.com/news/2026/04/cloudflare-sandboxes-ga/
- https://docs.turso.tech/sync/usage
- https://docs.turso.tech/sdk/go/reference
