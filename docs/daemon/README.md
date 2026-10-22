# Glitched daemon design

Glitched is a native agent GUI written in Go. These documents design the daemon that runs agents, and that the GUI, a second machine or a phone attaches to. One user, private.

## Read in this order

1. `architecture.md`. The host engine: data shapes, state machines, packages, flows, the MVP order and the risks.
2. `hub.md`. The hub role: accounts, machines, grants, locations, the log replica, and where it runs.
3. `network.md`. Every route is the user's tailnet.
4. `account-pool.md`. Accounts, the host proxy, refresh, and picking an account.
5. `placement-and-handoff.md`. Opening a thread on any host, placing with `Any`, and moving a thread.
6. `client-cache.md`. The GUI's local replica.
7. `push.md`. The one hosted piece: a Cloudflare Worker that forwards redacted hints to APNs.
8. `ephemeral-vms.md`. Deferred. Findings, and the lifecycle that still holds.
9. `research.md`. Traced facts from t3code, bb and zeron that the designs cite.

The live documents carry no superseded banners. Rejected options and earlier revisions are frozen under `decisions/`.

## Decisions

| Date | Decision | Where |
|---|---|---|
| 2026-10-20 | Hosts own execution. The peer shape won over bb's hub. | `architecture.md`, the candidates in `decisions/candidate-hub.md` and `decisions/candidate-peer.md` |
| 2026-10-20 | A hub for control and reads is added for central accounts and VMs. Hosts still write their logs first. | `hub.md`, the record in `decisions/control-plane.md` |
| 2026-10-20 | Rejected: the hub on Durable Objects. | `decisions/hub-on-cloudflare.md`, cost in `decisions/cost.md` |
| 2026-10-21 | Rejected: Turso Cloud as the store, and Turso sync plus a quorum for refresh. | `decisions/turso-hub.md`, `account-pool.md` |
| 2026-10-21 | Rejected: a user-picked token node with a ciphertext-only hub. | `decisions/token-node.md` |
| 2026-10-21 | Tokens reach CLIs through a loopback proxy on each host. Refresh only at the hub. | `account-pool.md` |
| 2026-10-22 | The tailnet is the only route. No Glitched relay. | `network.md` |
| 2026-10-22 | Ephemeral VMs deferred. | `ephemeral-vms.md` |
| 2026-10-22 | The hub is a role in the binary. Solo mode embeds it. More than one machine requires a dedicated always-on host. | `hub.md` |
| 2026-10-22 | Commands never go through the hub. | `hub.md`, `client-cache.md` |
| 2026-10-22 | The log holds items. Text deltas are a stream and are never log rows. One cursor works against the host and the hub. | `architecture.md` |
| 2026-10-22 | The hub pushes the grant table to hosts by revision. No grant certificates. | `hub.md` |
| 2026-10-22 | The hub's location CAS is the single commit point of a handoff. The source ships only the workspace. | `placement-and-handoff.md` |
| 2026-10-22 | Accounts are added only through Glitched's own login. The hub never imports a CLI's live login. | `account-pool.md` |
| 2026-10-22 | Subscription accounts pool too, auto-continue on by default, the risk stated in the setup screen. | `account-pool.md` |
| 2026-10-22 | Push goes from the hub through a stateless Cloudflare Worker hosted for everyone. Payload is the thread id and kind. | `push.md` |

## Open spikes

- Claude OAuth access token lifetime and whether refresh rotates the refresh token. The Codex ChatGPT access token measured 10 days on 2026-10-22.
- Claude Code behind a base-URL proxy with a per-process dummy `CLAUDE_CODE_OAUTH_TOKEN`.
- Tailscale local API detection on the macOS standalone build.
- libghostty fed from a byte stream.
- Provider session files carried across hosts, which embed absolute paths.
