# The hub runs on a host

Decided 2026-10-22. This replaces the Durable Object hub in `hub-on-cloudflare.md` and the Turso option in `turso-hub.md`. What the hub does is unchanged from `control-plane.md`. What changed is where it runs.

## Decision

**The hub is a role inside the `glitched` binary, run by one of the user's hosts and reached over the tailnet (`network.md`).**

- The user picks which host runs it, and the first host set up runs it by default. On that host, the hub and the host engine share one process, so a solo user still installs nothing extra.
- The hub stores its state in SQLite on that host. That covers machines, grants, thread index and locations, capacity, accounts, and the log replica. There's no Cloudflare, no Turso and no second language: it's Go everywhere.
- Other hosts and clients reach it at a tailnet address handed out at enrollment, such as its MagicDNS name and a fixed port.

Why not Durable Objects any more:

- Tailscale now does the meeting point, the relay and the terminal transport, which removed most of what the objects were for.
- The other reason was refreshing tokens while every machine sleeps. That isn't needed (see below).
- A hub on a host costs nothing to run, so there's no hosted bill to keep near zero.

## Refresh while the hub runs, catch up when it returns

- The hub refreshes while it is running: when a host asks for an access token and the cached one is expired or close to it, and on its own schedule for the rest.
- The hub can be offline. When it's the built-in host in the app and the app is closed or the Mac sleeps, nothing refreshes. When it comes back, it refreshes whatever is expired or near expiry before handing out tokens.
- Refresh tokens last much longer than those offline gaps, so the hub doesn't have to be always on. That is an assumption to check per provider.
- The hub is still the only refresher, refreshes one at a time per account, and saves the result before replying (`account-pool.md`). It saves that a refresh is in flight before sending it, so a host that slept mid-request knows to check on wake. The CLIs take the same risk with their own logins.

## When the hub host is down

Work continues where it can.

| Keeps working | Waits for the hub |
|---|---|
| Threads running on every other host | placing new threads with Any |
| Clients reading and steering threads directly on their owner host over the tailnet | handoffs, which need the location record |
| Runs using an access token a host already holds | refresh, and picking an account for a new run |
| Each host's own log, which uploads to the replica when the hub returns | enrolling machines and pairing clients |

## Failover (later, not required)

It isn't built now. Two rules keep it cheap to add later.

- **Hosts and clients must work without the hub,** as in the table above. Nothing on the hot path goes through it.
- **The hub writes its state as an append-only log with one writer.** A standby host can follow that log over the tailnet by cursor. Promoting the standby bumps a hub epoch, and every host rejects an older epoch. This is the same fence that thread ownership uses (`placement-and-handoff.md`).

The one real hazard in failover is token refresh. Two hubs that both believe they're current could each spend the same refresh token, and providers can revoke the whole token family for that. So promotion has to be explicit or fenced, never two hubs at once.

Moving the hub deliberately to another host is the same mechanism without the failure. bb's experimental server move is the reference.

## Build order

1. `glitched hub` role over loopback. The host appends its log to the replica, fenced by `{host, epoch, seq}`. A client reads by cursor and gets live events as a stream on the node's WebSocket, `/ws` (built 2026-10-22 as NDJSON over HTTP; moved to the WebSocket the same day behind `internal/transport`). Check: kill the host mid-upload, replay the upload, and find no gap or duplicate. An append with a stale epoch is refused.
2. Serve the same thing over the tailnet (installed Tailscale or `tsnet`). Check: a second machine enrolls and uploads.
3. Accounts with hub refresh and the host proxy (`account-pool.md`). Check: a run on a host with no local login works, and an account whose access token expired while the hub was offline is refreshed when it starts.
4. Placement and handoff through the location record.
