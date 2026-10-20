# Account pool

The hub is centralized (`control-plane.md`, `hub-on-cloudflare.md`). One reason is the account pool. A user registers several accounts per provider, and the hub picks which one each run uses. Picking needs one place that sees every account's limits and every live run, which is the user's `UserDO`.

## Model

```go
type Pool struct {
	Provider Provider
	Accounts []ID // ordered by user preference
}

type AccountState struct {
	Account      ID
	LimitedUntil time.Time  // from adapter rate-limit reports
	Windows      []Window   // e.g. 5-hour and weekly usage, when the provider reports them
	Live         int        // runs currently holding a lease
	MaxLive      int        // user-set concurrency cap
}

type Lease struct { // one per run
	Run, Account ID
	Issued       time.Time
	AccessToken  sealed // minted by the token node, held by the host proxy
}
```

## Delivery: a local proxy, refresh only at the hub

This follows zeron (`research.md`). Runs don't receive credentials. Each host runs a loopback proxy, and the CLI is started with its base URL (`http://127.0.0.1:<port>/claude`, `/codex/v1`) and a random per-process token.

- The proxy injects the chosen account's **access token** per request. Model traffic goes from the host straight to the provider, never through Cloudflare.
- **Refresh tokens never leave the token node** (next section). The token node refreshes single-flight per account and persists the result before replying. Once a refresh is sent it can't be cancelled. Host proxies hold only access tokens. On a 401 or near expiry they ask the token node for a fresh one.
- ~~Refresh tokens never leave the hub.~~ Superseded 2026-10-21 by the token node.
- This removes the one-machine-at-a-time lease for Codex ChatGPT logins and avoids the internal-only `chatgptAuthTokens` mode. Every host can use every account at once because none of them rotates anything.
- A login that is also the CLI's own live login on some machine is never refreshed by the hub, the same rule zeron uses. Accounts added through Glitched are pool-only.
- If a provider rejects proxied traffic, the fallback is `claude setup-token` for Claude and API keys elsewhere.

## Token node (the user's exit node for credentials)

The user picks which of their machines holds the plaintext credentials, the way they'd pick a Tailscale exit node. That machine is the **token node**. It refreshes tokens and mints access tokens for every other host's proxy. The hub stores only ciphertext, so the hosted tier never holds a usable credential, and refresh still works.

```go
type Vault struct {
	DataKeyWraps map[ID][]byte // the vault key wrapped to each custodian device's public key
	Blobs        map[ID][]byte // per account, encrypted with the vault key
}

type TokenRole struct { // one per user, at the hub
	Node       ID     // current token node
	Epoch      uint64 // bumped on every change
	Custodians []ID   // devices that can decrypt the vault, in failover order
	Quorum     int    // approvals needed to change Node without the current node's consent
	Mode       Mode   // tokensOnly | tokensAndTraffic
}

type RefreshIntent struct { // written before every refresh, cleared after persisting
	Account ID
	Epoch   uint64
	Started time.Time
}
```

**Roles.**
- **Custodians** are devices the user enrolls to hold a wrapped copy of the vault key, for example the Mac mini, the laptop and the phone. Only custodians can ever become the token node.
- **The token node** is one custodian chosen by the user. It's the only device that decrypts the vault and the only one that talks to the provider's token endpoints.
- **The hub** stores the ciphertext, `TokenRole` and intents. It is the compare-and-swap point that fences the role, but it can't read a token. The fencing is the same whether the hub is the Durable Object hub or Turso remote transactions.

**Modes.**
- `tokensOnly` is the default. The token node mints access tokens, and model traffic goes from each host straight to the provider.
- `tokensAndTraffic` routes every pooled request through the token node's proxy. Every account then talks to the provider from one IP. It costs latency and needs an always-on node with bandwidth.

**Refreshing safely.**
1. Before refreshing, the token node writes `RefreshIntent{account, epoch}` at the hub by CAS. The write fails if the epoch is stale, so a demoted node can't start a refresh.
2. It calls the provider, re-encrypts the new pair, uploads the blob and clears the intent in one CAS.
3. A demoted node that is mid-refresh can't be stopped, since the provider doesn't check our epoch. Its open intent stays visible instead.

**Changing the token node.**
- **Planned.** The user picks a new custodian. The current node finishes in-flight refreshes, then a CAS moves `Node` and bumps `Epoch`. The new node already has a wrapped vault key, so nothing is re-encrypted.
- **Current node gone.** It takes `Quorum` approvals from other custodians, for example 2 of 3 devices tapping "approve" (a user-controlled quorum, not consensus among machines). The hub won't move the role while a `RefreshIntent` from the old epoch is open and younger than the intent timeout.
  - After the timeout, the new node's first refresh of that account may hit a refresh token the old node already used.
  - In that case only that one account needs a new login. The UI shows it rather than retrying, because a retry can revoke the whole token family.
- **Removing a custodian** re-wraps the vault key for the remaining custodians, then rotates it.

**While the token node is offline.** Access tokens already issued keep working until they expire. After that, pooled runs on affected accounts wait in the queue with a "token node offline" reason, until the node returns or the role moves. Accounts using long-lived tokens or API keys keep working, because the token node can hand those out ahead of time to hosts that the user allows to cache them.

## Picking an account

The hub picks once per run, when the host proxy asks which account serves it.

Routing modes come from zeron. `sticky` pins a thread to an account for 30 minutes. `resetFirst` prefers the account whose weekly window resets soonest, so quota that would expire anyway is used first.

1. **Sticky first.** Reuse the account the thread last used, if it is not limited and below `MaxLive`. Provider prompt caching is per account, so switching mid-thread loses the cache and costs time and tokens (inferred).
2. Otherwise take the first account in pool order that is not limited and has the most headroom in its reported windows.
3. If every account is limited, the run waits in the queue with `scheduled{until: earliest LimitedUntil}`, and the inbox shows why.

Placement (`placement-and-handoff.md`) and the pool are separate choices. Placement picks a host for CPU and checkout. The pool picks an account for token budget. A thread can stay on one host and change accounts.

## Hitting a limit mid-turn

- The adapter reports the limit. The hub sets `LimitedUntil`, and the lease ends.
- The turn ends with a `limited` outcome, which is a clean end and not `Uncertain`.
- If the user enabled auto-continue for the pool, the queue dispatches a continue on the next account. Otherwise the thread shows "limited, continue on <account>?".
- The provider session is local to the host, so resuming it under another account's token should keep context (guess, needs the same spike as handoff session carry-over). If it fails, the portable handoff is the fallback.

## Rules

- **Pools are per user.** The hosted tier never pools across users. Sharing one person's subscription with another is a terms violation, not a feature.
- Pooling several accounts of one user may still conflict with a provider's consumer terms. Check each provider's terms before shipping auto-continue across subscription accounts. API-key accounts are the safe case.
- Every lease is recorded (run, host, account, time), and the user sees which account ran each turn.

## Build order

1. Accounts and leases in `UserDO` with one account per provider. Check: a run on a host without a local login works.
2. Rate-limit reports from the Claude Code and Codex adapters into `LimitedUntil`. Check: a recorded limit transcript marks the account.
3. Multiple accounts with sticky picking. Check: a limited account is skipped for new runs while threads on other accounts keep their account.
4. Auto-continue on limit, behind a per-pool setting.

## Rejected: Turso sync plus a host quorum for refresh

Considered on 2026-10-21. Hosts would share account state through Turso Cloud sync and elect a refresher by quorum.

- **Sync can't be the lock.** Turso sync resolves conflicts as "last push wins" (Turso docs). Two hosts that each see a free lease locally can both take it. A safe lease has to be a transaction against Turso Cloud's primary over the network. That makes Turso Cloud the central coordinator, which is a hub run by someone else.
- **LWW can bring back a consumed token.** A host refreshes and pushes the new pair. A host that was offline then pushes its older row, and the consumed refresh token wins. The next refresh fails, and zeron notes a second use of a single-use refresh token can revoke the whole token family. Any row that carries tokens must never be written from an offline replica.
- **A quorum of laptops rarely has a majority.** With two machines, one asleep means no quorum. Leader election over machines that sleep and roam is the hard part, and it buys nothing over a single always-on coordinator.
- **No lease can fence the provider.** The danger is the refresh request itself. If the holder's laptop sleeps after sending it, the lease expires and another host refreshes with the old token. The provider doesn't check our epoch. Safety needs a refresher that doesn't stall mid-request and persists before anyone retries. A Durable Object handler fits that. A laptop doesn't.
- **Blast radius.** Syncing refresh tokens puts every one on every host's disk. The hub design keeps them in one place.

Turso Cloud with an online CAS lease row is workable, if the hub is ever dropped. Because it still needs every rule above, it ends up as a hub made of a database plus refresh logic on hosts that sleep. The Durable Object already in the design does the same job without a second vendor.
