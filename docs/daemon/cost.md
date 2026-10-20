# Hosted hub cost

Goal: offer the hub (`hub-on-cloudflare.md`) to users for free. This estimates the marginal Cloudflare cost per user per month. The model is `cost-model.py`, and the prices come from Cloudflare's pricing pages (read 2026-10-20). The usage figures are assumptions, not measurements.

## Assumptions

| Tier | Agent thread-hours per day | Machines | Remote terminal hours per day |
|---|---|---|---|
| Light | 1 | 1 | 0 |
| Typical | 6 | 2 | 0.5 |
| Heavy | 40 (4 threads, 10 h) | 3 | 2 |

The model also assumes the following.

- 10 ms of handler time per incoming WebSocket message. Each object is billed at 128 MB.
- The host sends capacity every 30 s, and heartbeats use auto-response so they cost nothing.
- Terminal relay runs at 20 chunks/s.
- Transcripts take about 300 KB per thread-hour, with tool output capped at 16 KB in the hub. Full output stays on the host.
- Transcripts stay hot in object SQLite for 14 days, then move to R2 compressed 6x.

## Result (per user per month, after the account's included allowances are used up)

| Design | Light | Typical | Heavy |
|---|---|---|---|
| Naive: live deltas always, 3 rows per item, 200 items/thread-hour | $0.03 | $0.15 | $0.93 |
| **With the cost rules below** | **$0.01** | **$0.05** | **$0.25** |
| Worst case: a row for every 1 s delta batch | | | $13.26 |

With the rules applied, $100 a month covers about 2,000 typical users or 400 heavy ones. The account also has a fixed $5 minimum. Its included allowances (1M requests, 400k GB-s, 50M rows written, 5 GB) absorb the first few hundred light users.

**Rows written is the cost driver.** It is $1 per million after 50M, while requests and duration together stay below a quarter of the total. That leads to these rules.

## Cost rules for the hub

1. **Write rows at item boundaries, not per delta.** A completed message, tool call or approval becomes one row. Streaming deltas stay in memory and are broadcast without being written. The hub is a replica, so losing unflushed deltas on eviction only means the host resends from the hub's ack offset.
2. **One row per item.** Use the sequence number as the primary key with `WITHOUT ROWID` and no secondary indexes on the log table, since every index entry counts as a row write.
3. **Send live deltas only while someone remote watches** (bb's watch sets). Otherwise the host uploads only items.
4. **Never block hibernation.** Use no `setTimeout` or `setInterval`, no outgoing WebSockets and no long fetches from an object. Use alarms instead, and handle heartbeats with `setWebSocketAutoResponse`. One object stuck awake costs $4.05 a month, which is more than 80 typical users.
5. **Keep large data out of objects.** Cap tool output in the replica, put attachments in R2, and move settled threads to R2 compressed. R2 egress is free.
6. **Presence and capacity stay in memory.** They live in the machine object and are fanned out, never written as rows.

## Per-user quotas for a free tier

These bound abuse, since the hub is also a relay.

- Hot storage of about 1 GB and an R2 archive of about 5 GB per user.
- A cap on incoming messages per minute per object, and a monthly terminal-relay byte budget.
- An alert on any object awake for more than an hour.

## What can't be free

**Ephemeral VMs.** Cloudflare Sandbox and Containers pricing is $0.00002 per vCPU-second (active CPU only), $0.0000025 per GiB-second of memory and $0.00000007 per GB-second of disk (memory and disk are billed for provisioned time).

- A `standard-1` instance (1/2 vCPU, 4 GiB, 8 GB) at 10% CPU costs about $0.042 per hour.
- 4 h/day comes to about $5 a month.
- Two VMs at 10 h/day come to about $25 a month.

Memory is most of that, and agents mostly wait on the API. VMs need to be paid by the user, either on their own Cloudflare account or as a paid tier.

**Credentials.** A hosted multi-tenant hub holding other people's Claude and Codex tokens is a liability that costs more than any of the numbers above. The free hosted tier should seal each credential to the enrolled hosts' public keys, so the hub stores only ciphertext it can't use. A user's own deployment can keep the simpler model. A new ephemeral VM gets its sealed copy re-wrapped by an already-enrolled device or by the user's own deployment.
