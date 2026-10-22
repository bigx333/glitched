# Network: Tailscale first

Decided 2026-10-22. This replaces the relay, the LAN, SSH and the relay routes in `architecture.md`, and the Durable Object relay in `hub-on-cloudflare.md`.

## Decision

**Every node and client is on the user's tailnet. There is no Glitched relay.**

- **Tailscale installed:** Glitched uses it through its local API (`tailscale.com/client/local`). It reads its tailnet address and peers, listens on the tailnet address, and identifies callers with `WhoIs`. That gives no second device and no extra login.
- **Tailscale not installed:** the app runs its own node with `tsnet` and becomes a device on the tailnet. The GUI shows "Sign in to Tailscale" using the login URL tsnet reports. This node is in userspace, needs no root, and adds no network interface to the machine.
- **If Tailscale is installed later,** the embedded node logs out on the next start so the machine isn't listed twice.
- Tailscale's DERP servers carry traffic when two devices can't connect directly. DERP runs over HTTPS on 443, so it also gets through networks that block UDP. That is why no Glitched relay is needed.

```go
type Transport interface {
	Listen(ctx context.Context) (net.Listener, error)
	Dial(ctx context.Context, machine ID) (net.Conn, error)
	WhoIs(conn net.Conn) (Peer, error)
}
```

There are two implementations, one for an installed Tailscale and one for the embedded `tsnet` node. A loopback version exists for tests and development.

## Rules

- **Being on the tailnet is the route, not the authorization.** A tailnet can include devices shared by other people. A caller must also present an enrolled Glitched machine key or a client grant, and `WhoIs` is checked against it.
- **Discovery goes through the hub.** Personal devices aren't tagged, so they can't be found by tag. Each node reports its tailnet address to the hub, and the hub returns the machine list. Probing a fixed port on peers is a fallback.
- **Machines join untagged under the user's login.** Untagged devices are unlimited on the free Personal plan. Tags are for VMs only, which are deferred (`ephemeral-vms.md`).
- **Phones and other GUI-only devices are clients, not nodes.** They reach nodes through the Tailscale app. Without it, the app embeds libtailscale in userspace. That uses no VPN slot, which matters on iOS, where only one VPN can run at a time.
- **A browser client is out of scope.** A web page can't run tsnet, and that is the one case that would bring a relay back.

## Built so far (2026-10-22)

This step assumes Tailscale is installed and up on both machines. The engine listens on the machine's tailnet address and calls no Tailscale API yet.

- `glitched serve -listen <addr>` adds a TCP listener beside the unix socket, and may be repeated. The address must be loopback, in `100.64.0.0/10` or in `fd7a:115c:a1e0::/48`. The engine refuses any other at start, `0.0.0.0` included, because only WireGuard encrypts the traffic. An address in those ranges that the machine doesn't hold yet, as at boot before Tailscale is up, doesn't stop the engine. That listener retries in the background, backing off to 30 seconds, until it binds.
- A client refuses a server name that resolves outside those ranges before it sends a token. In range is not enough. On Linux, Tailscale routes only its current peers, and any other 100.x address leaves by the default route in cleartext. So the client also requires both ends of the connection to be loopback, or both Tailscale addresses. It checks the route with a UDP connect, which sends nothing, before it dials, and checks the connection's own addresses after. A server applies the same rule to each connection it accepts.
- Every TCP socket presents a pairing token as `Authorization: Bearer <token>` on the WebSocket upgrade. A missing, wrong or revoked token gets 401, and the client reports that the server refused the token. The unix socket needs no token. Its mode is 0600, in a 0700 directory.
- `glitched pair -name <label>` mints a random 32-byte token and prints it once with a connect line. `<dir>/tokens.json` keeps only the token's SHA-256, its label and when it was made, with mode 0600. `pair -list` lists the labels, and `pair -revoke <label>` removes one.
- The engine rereads `tokens.json` on every upgrade and once a second. One lock spans each read and the admission it decides, so no upgrade that begins after a revoke returns gets in with the revoked token. The revoke also closes the token's live sockets within a second, without a restart. An unreadable `tokens.json` admits no token and is logged once per distinct error.
- A server closes a connection idle for 10 seconds between requests, so a peer refused with 401 can't hold it open. The engine logs a refused peer at most once a minute.
- `run`, `threads` and `replica` reach a remote engine with `-server <addr> -token <token>`. A host that doesn't run the hub uploads to one with `serve -hub=false -hub-server <addr> -hub-token <token>`. A token without its server, a server without its token, or `-hub` with another hub named exits 2 before anything dials.
- When the hub was unreachable at start, the startup check can't see a restore from backup. If the uploader later finds that the hub holds more of the log than the database, the engine stops with that error and says to restart. `serve` exits 1, and the restart's startup check starts a new log.
- `internal/daemon` holds the startup that `serve` runs and the GUI will embed. `daemon.Start` takes the lock, opens the store, the runtime and the hub, and binds every socket whose address the machine holds.

Deferred:

- The embedded `tsnet` node for machines without Tailscale.
- Checking `WhoIs` against an enrolled machine key. Until then any tailnet peer with a token gets in. `100.64.0.0/10` is also carrier-grade NAT space, so on a machine that holds such an address without Tailscale, the token is the only gate and nothing encrypts it.
- Asking Tailscale's LocalAPI whether an address is a peer in the netmap. The range check and the source-address check are not proof of that. On a machine whose ISP gives it a carrier-grade NAT address, a connection from that address to another 100.x address passes both and leaves unencrypted.
- Scoped grants. A token reaches the whole engine, the hub included.
- Passing the token some other way than argv.

## Spike (2026-10-21, `/tmp/tsspike`, not in the repo)

Two tsnet nodes in one Go process ran against a local Headscale v0.29.5, using `tailscale.com` v1.104.2.

| | Result |
|---|---|
| Finding the peer | Node B found `glitched-a` from `LocalClient().Status()` by tag. No meeting-point service was involved. |
| Caller identity | `WhoIs` on the accepted connection returned `glitched-b tags=[tag:glitched]`. |
| Path | direct over the LAN, with home DERP `jnb` as the fallback |
| First reply | 52–235 ms, including the WireGuard handshake |
| Cold start | about 5 s for both nodes, including registration |
| Warm start, persistent node | 1.9–3.8 s, with a stable IP |
| Ephemeral node | Headscale deleted it on logout. It gets a new IP every run. |
| Binary | 30 MB for the spike. The size added to Glitched wasn't measured. |

## Tailscale plan limits (pricing page and FAQ, read 2026-10-21)

- Personal is free and covers 6 users and unlimited user devices.
- Tagged resources are limited to 50, and each one beyond that costs $1 a month.
- Ephemeral devices draw on a pool of 1,000 minutes a month. One present for more than 4 hours counts as a tagged resource instead.
- Personal is "only suitable for non-commercial use". Users bring their own tailnet, so Glitched doesn't resell Tailscale.

## Risks

- **The local API on macOS.** The App Store, standalone and open-source builds of Tailscale expose the local API differently, and some only allow the same user. This Mac runs the standalone build (`io.tailscale.ipn.macsys`). A spike must confirm detection here before we depend on it.
- **Tailscale's login is required.** A user who won't sign in to Tailscale can't use more than one machine. That is accepted.
