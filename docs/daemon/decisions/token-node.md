# Token node (rejected)

Status: proposed and reverted on 2026-10-21 (commits `e0eb127` and `1f2e05d`), then made moot on 2026-10-22 when the hub moved onto the user's own host (`../hub.md`).

## The idea

The user picks one of their devices as the **token node**, the way they'd pick a Tailscale exit node. Only that device decrypts the credential vault and talks to the providers' token endpoints. The hosted hub stores ciphertext, a `TokenRole{node, epoch, custodians, quorum}` record and refresh intents. It is the compare-and-swap point that fences the role, and it can't read a token. Custodian devices hold a wrapped copy of the vault key so the role can move. Changing the node while the current one is gone takes a user-controlled quorum of custodians, and a refresh intent from the old epoch blocks the move until it times out.

## Why it existed

The hub was going to be hosted on Durable Objects (`hub-on-cloudflare.md`). A hosted service holding plaintext refresh tokens was the liability that `cost.md` calls "what can't be free". The token node kept plaintext on the user's hardware and still had a single refresher.

## Why it's moot

With the hub as a role on the user's always-on host, the hub is the token node. Plaintext never leaves the user's machines, there is one refresher, and the hub epoch is the fence. The custodian and quorum machinery would only matter for hub failover, which `../hub.md` leaves explicit and manual.

## Worth keeping

The refresh intent written before a refresh is sent and cleared after the result persists. `../hub.md` keeps it as `refreshInFlight`.
