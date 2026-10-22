# Push notifications

Push to a phone needs a sender that holds the APNs key. That key can't be in the user's binary, so the sender is the one piece we host for everyone. It runs on Cloudflare to keep it near free, and it holds no per-user state.

## Shape

```
hub (user's host)  → POST https://push.glitched.dev/v1/send   {deviceToken, kind, thread}
Worker             → APNs with a token-based JWT from the .p8 key
phone              → receives {kind, thread}, reads the thread from the hub over the tailnet
```

- **The hub sends, not the hosts.** The hub sees every thread and holds the device tokens that phones register with it. Hosts never learn a device token.
- **The payload is the thread id and the kind** (`turnEnded`, `needsYou`, `failed`). No title, no text, no tool arguments. The Worker and Apple see nothing else.
- **Push is a hint to refetch.** It can never approve, and the app never acts on a push without reading the thread from the hub.
- **The Worker is stateless.** It validates the shape, rate-limits per device token with Cloudflare's rate-limiting binding, signs the APNs JWT, and forwards. No accounts, no database, no Durable Objects.
- **No account system.** An APNs device token is per app and per device and isn't guessable, so a request that names one is treated as coming from that phone's own hub. The only abuse is spam against a token the attacker already has, and the rate limit bounds it. Revisit if the sender ever carries anything beyond a hint.
- **Local notifications don't use it.** A GUI on a Mac posts its own from the shell stream (`architecture.md`).

## Cost

Workers' free tier covers 100,000 requests a day. A heavy user from `decisions/cost.md` (40 thread-hours a day) sends a few hundred pushes a day, so the free tier covers a few hundred heavy users and the paid plan's $5 minimum covers far more. There is no per-user storage, so nothing grows.

## When it's down

The hub keeps a short outbox of hints and retries with backoff. A phone that opens the app reads from the hub anyway, so a push outage delays attention and loses nothing. The hub also drops a hint older than the thread's next state change, because a stale `needsYou` is noise.

## Build order

Last, with the phone client. Until then the hub's outbox has no sender and the delivery kind is disabled by capability.
