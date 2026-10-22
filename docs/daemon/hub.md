# The hub

The hub is the one place that knows every machine, account, grant and thread location, and it keeps a replica of every thread log. It runs as a role inside the `glitched` binary. Hosts own execution and write their own logs first. The hub never runs an agent and never carries a command.

## Why a hub

Three requirements changed the pure peer shape in `decisions/candidate-peer.md`.

1. Agent accounts are managed in one place, and every host gets its access tokens from there (bb's model, `account-pool.md`).
2. Clients pair once instead of once per machine.
3. Ephemeral VMs, when they come, need something that outlives them to create them, hand them tokens and keep their logs (`ephemeral-vms.md`).

Why hosts still write their own log, which is the bb mistake to avoid:

- A laptop keeps running its own threads with no network. The log uploads when the hub is reachable again.
- bb loses events because its daemon queue lives in memory. Here the host commits an event before it uploads it. The upload offset is a watermark, and the host never truncates its own log.

| | Pure peer | **Hub for control and reads, hosts write** | Hub owns everything (bb) |
|---|---|---|---|
| Accounts | per machine | central, access token per run | central |
| Pairing | per machine | once, at the hub | once |
| Laptop offline | works | works, uploads later | local threads stop |
| Durable logs | one per host | host log plus hub replica | hub log plus a host spool, which bb skipped |
| Handoff | ship log and workspace | log already at the hub, ship the workspace | reassign |
| Hops for a command | 1 | 1, commands never touch the hub | 2 |

## Where it runs

**Solo.** One machine runs the GUI, the engine and the hub role in one process. The hub's tables live in the same SQLite file as the engine's. A solo user installs nothing and never sees a hub.

**More than one machine.** Enrolling a second machine requires an always-on host for the hub. Setup asks the user to name one, and the hub moves there before the second machine enrolls. The laptop then becomes a host like any other. It dials the hub, uploads its log and asks for tokens. A host that reports it runs on battery or is allowed to sleep can't be named.

Why a dedicated host and not the laptop. The hub refreshes tokens and records locations, so a sleeping hub delays refresh, placement, handoff, enrollment and pairing. A Codex ChatGPT access token measured on 2026-10-22 lasts 10 days, so a sleeping hub would rarely hurt a Codex run. Claude's access token lifetime is unmeasured. Requiring an always-on host removes the question instead of answering it per provider. Decided 2026-10-22 (`README.md`).

**Moving the hub.** The hub's state is one SQLite file with an append-only log and one writer. A move is an explicit admin action from a paired GUI. Stop the hub role, copy the file to the new host, start it there with the hub epoch bumped, and leave a `movedTo{address, epoch}` marker on the old host. The old host answers `Moved` for a grace period, which is bb's `410 server_moved`. A host or client that hears `Moved`, or an older epoch, re-dials the new address. Grants and enrollments travel with the file, so nothing re-pairs.

## What the hub holds

```go
type Machine struct {
	ID       ID           // the enrolled machine key, which is the identity (network.md)
	Name     string
	Address  string       // tailnet address, reported at every session open
	Ceiling  Access
	AlwaysOn bool         // reported by the host; the hub role requires it
	State    machineState // sealed: enrolling | online | blip | offline | suspended | removed
	Instance ID           // changes on each host process start
}

type Location struct{ Thread, Host ID; Epoch uint64 }      // placement-and-handoff.md
type Replica struct{ Thread, Host ID; Epoch, Through uint64 } // ack watermark per thread
type GrantTable struct{ Revision uint64; Grants []Grant }   // pushed to every host
```

Accounts, pools and leases are in `account-pool.md`. `Grant` is in `architecture.md`.

- **Accounts and tokens.** The hub is the only refresher (`account-pool.md`).
- **Machines.** Enrollment uses a 15-minute single-use key bound to a pre-created machine ID. The host stores an Ed25519 key and signs every session open (bb).
- **Grants.** Clients pair with the hub. The hub pushes the full grant table to every host by revision, the way bb pushes env vars. A host checks a client connection against its copy. Revocation bumps the revision, and a host closes the client's sockets when it receives it. While a host can't reach the hub it uses its last copy, so revocation lags by the length of the outage. There are no signed grant certificates, because the table is enough and smaller.
- **Locations.** The hub's compare-and-swap on `{host, epoch}` is the only commit point for a move (`placement-and-handoff.md`).
- **Replica.** Every host uploads its log from a durable offset. The replica keeps the host's `{host, epoch, seq}`, so a cursor is valid against the host and against the hub. An upload with a stale epoch is refused.
- **Capacity and presence** stay in memory and fan out to clients. They are never stored.

## What goes through the hub

| Through the hub | Direct, client to host over the tailnet |
|---|---|
| Pairing and enrollment | Commands, always |
| Token leases and refresh | Terminals |
| Reads for the client cache and the inbox across machines (`client-cache.md`) | Reads and subscriptions when the client prefers the owner, with the same cursor |
| Placement with `Any`, and the `Location` record | Live transcript deltas, unless a remote client watches through the hub |
| The handoff commit | |
| Push hints to phones, through the hosted sender (`push.md`) | |

Commands never go through the hub. The host's log decides their order and issues the receipt, so the hub could add nothing but a hop. If the owner host is unreachable, the command stays on the client as a pending draft (`architecture.md`). A mailbox for a sleeping host returns only with ephemeral VMs (`ephemeral-vms.md`).

## Refreshing tokens

- The hub refreshes when a host asks for a lease and the access token is expired or near expiry, and on its own schedule otherwise.
- It refreshes one account at a time. It writes `refreshInFlight` before it sends the request and persists the result before it replies. A refresh can't be cancelled once sent, so a hub that restarts mid-request checks that account before it retries.
- Hosts hold only access tokens. Refresh tokens never leave the hub (`account-pool.md`).

## When the hub host is down

The hub host is always on, so this is a failure, not a daily event. Work continues where it can.

| Keeps working | Waits for the hub |
|---|---|
| Every running thread on every host | placing new threads with `Any` |
| Commands, reads and terminals straight to the owner host | handoffs |
| Runs whose access token is still valid | token leases for new runs, and refresh |
| Each host's own log, uploaded when the hub returns | enrolling machines, pairing clients, revoking grants |
| Local notifications on each GUI | push hints to phones |

## Failover, later and not required

Two rules keep it cheap to add.

- Hosts and clients work without the hub, as the table above shows.
- The hub writes its state as an append-only log with one writer. A standby host can follow it by cursor. Promoting the standby bumps the hub epoch, and every host and client rejects an older epoch. That is the same fence thread ownership uses.

The one hazard is token refresh. Two hubs that both believe they are current could each spend the same refresh token, and a provider can revoke the whole token family for that. Promotion is explicit, never automatic.

## Build order

1. The hub role in the solo process. The host appends its log to the replica, fenced by `{host, epoch, seq}`, and a client reads by cursor with live events over a WebSocket. Check: kill the host mid-upload, replay the upload, and find no gap or duplicate. An append with a stale epoch is refused.
2. The hub role on a named always-on host over the tailnet (`network.md`). Check: a second machine enrolls, uploads, and receives the grant table. Revoking a client at the hub closes its socket on the second machine.
3. Accounts with hub refresh and the host proxy (`account-pool.md`). Check: a run on a host with no local login works, and an account whose access token expired while the hub was down is refreshed when the hub starts.
4. Locations, placement and handoff (`placement-and-handoff.md`).
5. Moving the hub to another host. Check: hosts and clients re-dial after `Moved`, and an upload with the old hub epoch is refused.
