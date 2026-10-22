# Turso Cloud as the central store

Status: rejected on 2026-10-22. Neither Turso nor Durable Objects is used (`../hub.md`). Kept for the comparison.

This is the alternative to the Durable Object hub in `hub-on-cloudflare.md`, written down on 2026-10-21 so the two can be compared.

## What Turso Cloud can and can't be

Turso Cloud is a database. No code runs there. These facts come from the Turso sync docs, read 2026-10-21.

- `push` sends local changes. On conflict, "last push wins".
- `pull` applies remote changes, with an optional long poll that waits for changes.
- No subscriptions or server push are described.
- Sync is per whole database. Partial sync isn't mentioned.

So Turso can be the **system of record and the client cache**. It cannot be the refresher, the VM provisioner, the auth broker, the write validator or the terminal relay.

## Shape

```
Turso Cloud
  user DB         machines, grants, thread index, locations, capacity, account metadata (no secrets)
  thread DBs      one per thread: log replica + command inbox
  vault DB        credentials + refresh leases; never synced, online transactions only
Worker (stateless, Cloudflare or anywhere)
  refresher       CAS lease row in vault, call provider, persist, release
  broker          pairing, mints per-device Turso tokens, VM create/remove
Hosts and GUI     tursogo embedded replicas of user DB + the thread DBs they watch
Live bytes        terminal and token streaming: direct (LAN/Tailscale) or a thin relay
```

## Rules that make LWW safe here

- **Every synced row has exactly one writer.** The owner host writes its thread's log rows. Clients only **insert** commands with client-minted IDs. Unique inserts never conflict, so "last push wins" never picks between two versions of the same row.
- **The host is the validator.** No server checks writes, so commands are signed by the device key and the host verifies them against the grants before applying. A forged or revoked command is dropped and recorded.
- **Locations change only through a remote transaction** against the primary, by compare-and-swap on `{host, epoch}`. Offline replicas never write them.
- **Secrets never sync.** The vault DB is reached only by the Worker over online transactions. The refresh lease is a CAS row with a TTL longer than the Worker's longest request, and the result is persisted before the lease is released. A Worker invocation doesn't sleep mid-request the way a laptop does.

## Compared with the Durable Object hub

| | Durable Object hub | Turso store + Worker |
|---|---|---|
| Languages | Go + TypeScript hub | Go everywhere except a small Worker (could be Go on any host) |
| Client cache | custom cursor sync we write | embedded replica, offline reads and writes built in |
| Live token streaming | WebSocket fan-out in the object | long-poll pull (latency inferred to be about one round trip, untested), or direct or relay |
| Terminals | relayed by the machine object | needs direct routes or a separate relay |
| Write validation | in the object | on the owner host, with signed commands |
| Partial sync for a phone | per query | per database, so threads are sharded into their own DBs |
| Revoking a device | drop its grant | grant check on hosts plus rotating that device's DB token |
| Moving parts | 1 (Cloudflare) | 3 (Turso, Worker, relay) |
| Est. cost, typical / heavy user per month | $0.05 / $0.25 | about $0.11 / $0.77 (sync bandwidth and storage dominate) |

The cost line uses Turso Developer pricing: rows written $1/M after 25M, syncs $0.35/GB after 10 GB, storage $0.75/GB after 9 GB, unlimited databases. It assumes the same workload as `cost.md` and two clients pulling each transcript. Storage keeps growing unless old threads move to object storage. The free plan caps at 100 databases, which a DB-per-thread design outgrows quickly.

## Verdict

Turso works as the central store, and it gives the client cache almost for free in Go. The price is that validation, live streaming and terminals have to live elsewhere. The architecture goes from one hub to three pieces, at roughly three times the per-user cost (still under $1).

The Durable Object hub stays the recommendation because an agent app depends on live streaming and validated commands, and Turso provides neither. Turso is the better pick if Go-only matters more than that, or if offline-first clients become the main requirement.
