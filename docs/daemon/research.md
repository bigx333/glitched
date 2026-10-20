# Grounding: remote daemons in t3code, bb, zeron (traced by read-only explorers)

## Target
Glitched: a native agent GUI written in Go with MyGo (module `glitched`, /Users/ncamargo/Projects/glitched). Today it is a UI-only prototype: threads, inbox sidebar (Inbox/Working/Snoozed/Settled, snooze/settle), model picker (Claude/Codex/Gemini/Grok families, effort, fast mode), access levels (ask approval / auto edits / full access), libghostty terminal, diff viewer, web preview, quick-ask panel, notifications, turn markers. No backend yet. We are designing the daemon that will run agents and that remote clients (another Mac, a phone, the GUI on the same box) attach to. Single developer user; private.

## t3code (pingdotgg/t3code, TS/Effect, one `t3` binary)
- Single instance: SQLite `BEGIN EXCLUSIVE` on server-owner.sqlite, OS releases on SIGKILL; distinct exit code 78 so supervisors don't restart.
- Service launcher parent under launchd/systemd swaps versioned runtimes. Two-phase update: trial child binds+migrates, reports `prepared`, launcher `commits`; DB snapshot before trial; rollback. Clients match update id with ready event.
- Secrets passed via inherited fd, not argv/env.
- Transport: WebSocket Effect-RPC `/ws`, ~250 typed methods; mutations req/resp; `subscribe*` streams. HTTP for snapshots/history/auth/webhooks. Protocol version checked (426), plus ~50 capability flags.
- Orchestration v2: pure Orchestrator command→events; EventSink commits events+projections+command receipt+outbox effects in ONE transaction; client-supplied commandId receipts = idempotency; EffectWorker runs side effects after commit; effects classified process-bound (retired on crash) vs replay-safe.
- message.dispatch modes: start_immediately, queue_after_active, steer_active, restart_active, defer_start. Ack returns {sequence} = intent committed.
- Provider adapter interface: ensureThread, resumeThread, startTurn, steerTurn, interruptTurn, respondToRuntimeRequest, rollbackThread, forkThread, readThreadSnapshot, compactThread, events stream.
- Approvals/questions are runtime-request events answered by runtime-request.respond.
- Reconnect: subscribe live first, then read tail; resume afterSequence if gap ≤128 events/1MiB else snapshot; `synchronized` marker; unknown events skipped but cursor advances; per-sub budget 1000 items/8MiB, slow consumer stream fails and resubscribes. Shell stream (thread list, cheap) vs per-thread detail stream.
- Restart continuation of interrupted turns (max 3 chained). Checkpoints as hidden git refs per turn.
- Auth: policy by mode (desktop-managed-local, loopback-browser, remote-reachable). Pairing: one-time token (5 min) or desktop bootstrap → /oauth/token exchange → 30-day session (cookie/bearer/DPoP). 5-min WS ticket so tokens never in URLs. Per-RPC scopes (17 of them; they note operate scopes can mint the rest — too complex, prefer coarser). Revocation closes live sockets. Sessions listable/revocable. Pairing URL + QR.
- Remote reach: client tries routes in order (LAN, Tailscale, VPN, SSH, T3 Connect), verifies route answers as same environment id before sending creds, never falls back to loopback, upgrades to better route after preflight. Tailscale Serve support. T3 Connect = relay brokers a Cloudflare tunnel + mints DPoP-bound one-time credential; relay never proxies traffic nor sees session token. Desktop-managed SSH: installs server on remote, pairs remotely, ssh -L forward.
- Extras: push notifications + Live Activities via redacted activity publish; relay holds webhooks while offline; scheduled tasks (interval/fixed time/webhook with HMAC); queue reorder/edit/cancel/promote-to-steer; fork, provider switch, rollback, snooze/settle; server-owned PTYs (5000 lines/8MiB); server-owned headless browser tabs streamed to clients; /mcp for outside agents via OAuth; client activity leases (what clients watch + battery) tune background polling; capabilities descriptor.

## bb (getbb.app, TS, Electron)
- Hub: ONE server (Hono, SQLite bb.db, 127.0.0.1:38886) owns all state; every machine runs a host daemon that DIALS OUT to server. Nothing connects into a daemon.
- Launcher supervises server + daemon; restart exit code semantics; moved responder (410 server_moved) for server migration; suspension marker.
- Daemon: proper-lockfile daemon.lock; persistent host-id + long-lived host key (auth.json 0600); fresh instanceId per process. Enrollment: single-use 15-min enroll key bound to pre-created hostId → `curl -H X-BB-Enrollment ... /install.sh | sh` installs app + service and enrolls.
- Session open: POST /internal/session/open (protocolVersion int 237 strict, hostId, instanceId, activeThreads, undeliveredEventThreadIds) → sessionId, heartbeat 5s, lease 30s, server-authoritative watch set, env vars. WS /internal/ws for control (host-rpc.request/cancel, watch-set.replace, terminal.*, machine-environment.replace revisioned, shutdown). Bulk via HTTP POST /internal/session/events etc.
- Same instanceId on reconnect = blip (reconcile); different = daemon restarted → interrupt active turns/pending interactions. 30s grace before UI shows disconnected.
- UI ↔ server: /ws is invalidation bus only (`changed` entity/id/changes) and clients refetch over HTTP; lenient schemas. Subscriptions drive daemon watch sets (only watch what someone looks at).
- Queue: queued_thread_messages state machine (queued, waiting, scheduled, claimed, failed_retrying, failed_exhausted) with CHECK constraints, claim tokens; dispatch woken by named wake reasons. Wake policy table: each RPC type is `work` (may wake suspended machine) or `never`.
- Events: daemon batches per text-delta tick, flushes immediately on turn boundaries/approvals; server assigns (thread,sequence); bad batch bisected; per-thread ordering; BUT daemon queue is in-memory only (lost on crash).
- Approvals: InteractiveRequestRegistry dedup by (thread,provider,providerThread,providerRequestId) → pending_interactions row → UI resolves → server RPC back to daemon.
- Provider bridge worker: child process JSON-RPC stdio, initialize negotiates grammar version + capabilities (sessionRestore, fork, steerMode, approvalEnforcedBy). Idle sessions reaped 30 min.
- PTYs: 4MB scrollback, seq/ack replay sinceSeq, flow control pause 1MB/resume 512KB, exited kept 30 min, CLI attach/send/wait-for-regex.
- Machines: multiple per server, permission ceiling per machine, suspend/resume with drain, wake-on-work, remove revokes creds, env vars encrypted pushed by revision, git creds forwarded. Port share via tunnel <label>--<port>.getbb.app. Daemon auto-update on protocol mismatch (sha256, never downgrade). Open-in-editor via SSH authority. Plugins full trust (forked workers, no sandbox).
- AVOID: /api/v1 unauthenticated (loopback + Host/Origin only), remote access outsourced to hosted gate.

## zeron (Rust, native GUI)
- One binary: GUI default; `headless`; `daemon install|start|stop|status` (launchd/systemd, KeepAlive on failure, points at ~/.zeron/app/current symlink); `status`, `sync`, `mcp`, `update`.
- InstanceLock: exclusive lock on engine.lock with pid; lock decides data ownership, taken before binding.
- GUI bootstrap: probe 127.0.0.1:27654 (750ms) + EngineInfo; if daemon answers, attach as viewer; else take lock and EMBED engine in-process and serve it on the port for others. SAME protocol in-process (memory transport) and over WS (newline JSON frames). DeferredEngineRpc answers info/auth immediately, holds data calls until ready.
- Scope (Local/Synced/Development) fixed at startup; login/logout need restart.
- Commands are durable data: chat CRDT doc ledger entries (Run, Steer, Interrupt, RespondInput) with client-minted ids, marked processed before execution; evaluate_command (processed/expired/superseded). Exactly-once across retries and offline sends. Mutations never replayed after uncertain RPC.
- run_journal: {seq,event} JSONL per chat for replay + crash recovery; recover_stale stamps crashed runs aborted and auto-resumes (≤3, ≤12h).
- Transcript: full frames measured 1.13MB per 120ms tick → forced delta protocol (reset frame then changed entries). Design deltas from start.
- Remote: engine only listens on loopback; outbound-only HostRelay WS to Cloudflare Worker DeviceRoom; clients multiplexed by connId; relay never parses RPC. Liveness: ping 10s, 25s silence lease, client echo 20s, rejoin backoff ≤5s jitter; retries cut short on system wake / network change / token change. LinkCache: presence check fail-fast, 3 dial attempts, cooldown 5→60s. Nudge queued in DO for cold host. forwardable() method list with per-method deadlines + targetDeviceId routing.
- PTYs engine-owned, 1MB replay afterSeq, max 32, survive detach.
- Approvals unattended by default; prompt mode routes can_use_tool → InputRequested → RespondInput.
- Self-update hourly, sha256, remote ApplyUpdate, restart only when quiescent (no active run, no terminal).
- Push via edge (APNs). MCP stdio + HTTP bearer; HMAC webhooks (chat.created, turn.completed, input.requested). Previews over WebRTC.
- Login-shell PATH snapshot for agents run from services; pinned adapter installs so npm never runs mid-chat; capability list for version skew.
- AVOID: any device on same account fully trusted, no pairing, no per-device revocation, local IPC unauthenticated, token in query string, no relay method allow-list, no path without hosted edge.

## t3code provider credentials (traced 2026-10-21)

- t3code does not own provider logins. Each provider instance points at the CLI's own config: `CLAUDE_CONFIG_DIR` for Claude, `CODEX_HOME` for Codex. The CLI stores and refreshes its login there (Claude on macOS uses the Keychain). Multiple accounts are multiple instances with separate directories (`docs/user/providers-claude.md`, `providers-codex.md`).
- Codex accounts can share sessions through a "shadow home". Instances share one `CODEX_HOME` for sessions and config, and each has its own `auth.json`, so a thread can switch accounts (`apps/server/src/provider/Drivers/CodexHomeLayout.ts`). Claude has no equivalent, and threads switch only between instances with the same config directory.
- "Connect with ChatGPT" drives Codex app-server's own `account/login/start`, so Codex persists and refreshes the tokens.
- What t3code stores itself: API keys and env vars marked Sensitive in provider instance settings, plus source-control tokens. They go in `<stateDir>/secrets/<name>.bin`, one file each, mode 0600 in a 0700 dir, written atomically, and are **not encrypted** (`apps/server/src/auth/ServerSecretStore.ts`). They are redacted in settings sent to clients. The desktop app uses Electron safeStorage for its saved environment connections.
- Pooling in t3code is read-only. **Usage → Limits** sums subscription windows across every account it sees, across environments and an optional CLIProxyAPI hub, and deduplicates the same account. It doesn't route runs between accounts. Routing is the external proxy's job, configured as a provider endpoint (`docs/user/usage.md`).
- Codex app-server has a `chatgptAuthTokens` auth mode. The host app supplies tokens held only in memory and answers `account/chatgptAuthTokens/refresh` requests. That is exactly a hub-issued credential, but the schema marks it "[UNSTABLE] FOR OPENAI INTERNAL USE ONLY - DO NOT USE". t3code generates the types but doesn't use it.

## zeron agent accounts and pool (traced 2026-10-21)

Source is `zeron/crates/engine/src/agent_accounts.rs` and `agent_accounts/pool.rs`.

- **Slots per device, never synced.** Each saved login is a slot at `{data_dir}/agent-accounts/{harness}/{slotId}.json`, owner-only. It holds the CLI's own credential blob (Claude `.credentials.json` or the Keychain item, Codex `auth.json`). Accounts are device-scoped, and the CRDT sync never carries them (`ARCHITECTURE.md`).
- **Swap.** `activate` overwrites the CLI's credential store with a slot and backs up the live login first. For Claude it keeps machine-shared keys (`mcpOAuth`, `pluginSecrets`) from the live blob so MCP servers don't re-auth.
- **Add an account.** Zeron drives the CLI's own OAuth (Claude PKCE to a loopback callback, `codex login` in a throwaway `CODEX_HOME`) without touching the live login. A login for another device forwards the loopback port over the P2P link.
- **The pool is a loopback HTTP proxy inside the engine.** For a pooled chat the CLI is started with a base URL of `http://127.0.0.1:<port>/claude` or `/codex/v1` and a random token. The proxy picks an account per request and injects that account's real token. The CLI never holds a pooled credential.
- **Routing.** `Sequential` pins a session to an account for 30 minutes, for prompt-cache stickiness. `WeeklyReset` prefers the account whose weekly window resets soonest, so quota that would expire anyway is used first. Accounts with errors or a `held_until` from rate limits are skipped. Each chat can bypass the pool.
- **Refresh, the important part.**
  - Zeron refreshes pooled slots itself against the provider token endpoints. It does this single-flight per slot, and once the refresh token is sent it can't be cancelled.
  - The refreshed slot is persisted with retries, because "a single-use refresh token must not disappear on a transient" failure.
  - It backs off on failure.
  - For the account that is the CLI's current live login, it never refreshes. "The CLI owns this refresh-token family. Re-read it, never rotate it."
- **Usage probes** call the same endpoints the CLIs use (`/api/oauth/usage` for Claude, `wham/usage` for Codex), stale-while-revalidate, with `Retry-After` honoured.
