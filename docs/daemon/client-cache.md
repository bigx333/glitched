# Client cache

The GUI keeps a SQLite cache that is a partial replica of the hub, so reads never wait for a host.

## Rules

- **Shell data** (projects, threads, inbox state, locations, capacity) syncs fully and continuously from the hub. It is small.
- **Transcripts** sync per query. An open thread, the visible inbox rows and the most recent N threads are kept warm. Each cached thread stores its cursor, and a reopen asks only for events after it. The resync rule from `architecture.md` still applies: a gap too large returns a snapshot.
- **One cursor, two sources.** The hub replica keeps the host's `{host, epoch, seq}`, so the client reads a thread from the hub or from its owner host with the same cursor (`hub.md`). It prefers the local engine for its own threads and the hub for threads on other hosts. When the hub is down it reads from the owner.
- **Reads are local.** The UI renders from the cache and never waits for a host. A host that is offline still has a readable history.
- **Writes stay commands.** The client sends every command to the owner host, never to the hub. It writes an optimistic pending row keyed by command ID and replaces it when the receipt and events arrive. There is no client-side merge. A command the owner can't take stays pending and is retried when the hub's shell stream shows the owner online or the thread moved (`placement-and-handoff.md`).
- **Live text** comes over the transcript stream, not the log (`architecture.md`). The cache holds the in-progress message buffer from the snapshot and replaces it when the item commits.
- **Eviction** is by last-opened time with a byte budget. Evicted transcripts refetch from the hub on demand.

## Zero

[Zero](https://zero.rocicorp.dev) is the closest existing product to this cache. Its model fits well. Postgres replicates into a SQLite replica inside `zero-cache`. Clients sync only the rows their named queries need and read locally. Writes go through server-side custom mutators, which may be written in any language that speaks the push protocol.

It doesn't fit Glitched as the engine, for three reasons.

- The client library is TypeScript (web and React Native). Glitched's GUI is Go, so we would reimplement the client sync protocol, which isn't a stable public contract before 1.0 (inferred from the 0.x releases).
- It requires Postgres with logical replication behind `zero-cache`. That works on an always-on hub but not inside the laptop's single binary.
- Our source of truth is an append-only event log with cursors, which already gives deltas and resume. Zero would add a second sync layer on top of it.

**Take the model, not the product.** Use named queries scoped by what the screen shows, a local SQLite replica, server-authoritative mutations and optimistic pending rows. Build it on our own log. If a web or phone client in TypeScript appears later, Zero could serve that client from the hub without touching hosts. That is worth revisiting then.

## Build order

1. Shell data synced from the hub with live updates. Check: a thread created on a second host shows up in the sidebar without a restart.
2. Per-query transcript sync with cursors. Check: reopen a thread with its host offline and it renders from the cache.
3. Pending rows for commands. Check: send with the owner offline, see the pending marker, bring the owner back, and see the receipt replace it once.
