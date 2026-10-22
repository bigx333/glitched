# Ephemeral VMs (deferred)

Put off on 2026-10-22. These are findings so far, not a design. The VM lifecycle in `control-plane.md` still holds: `Create`, `Remove`, optional `Suspend` and `Resume`, all idempotent by machine key.

## Providers

Rates are as each provider published them in 2026 (from comparisons on Fly.io and Northflank, and MarkTechPost's August 2026 tests). Check them again before choosing.

| Provider | CPU | Memory | Billed on | Minimum |
|---|---|---|---|---|
| E2B | $0.050/vCPU-hr | $0.016/GiB-hr | reserved, whole run | Hobby $0 + $100 credit; Pro $150/mo |
| Daytona | $0.050/vCPU-hr | $0.016/GiB-hr | reserved; a stopped sandbox pays for disk only | none, $200 credit |
| Modal | ~$0.071/vCPU-hr | $0.024/GiB-hr | higher of requested and used | $30/mo credit; Team $250/mo |
| Cloudflare Sandbox | $0.072/vCPU-hr, active CPU only | $0.009/GiB-hr | memory and disk while running | Workers Paid $5/mo |
| Vercel Sandbox | $0.128/vCPU-hr, active CPU only | $0.021/GB-hr | active CPU only | Pro $20 credit |
| Fly Sprites | $0.0385/CPU-hr used | $0.022/GB-hr used | usage; pauses ~30 s after activity stops | none |
| Fly Machines | from $0.0093/hr (shared-1x, 1 GB) | included | per second | none |

Agents mostly wait on the model, so billing on active CPU or pausing when idle fits better than paying for reserved capacity. E2B is Apache-2.0 and can be self-hosted, but needs KVM. Daytona's open-source repo is archived.

## How a VM reaches the tailnet

tsnet runs in userspace, so a VM only needs outbound HTTPS. There are four options:

| Option | Tailscale limits | What the VM can reach | Cost to us |
|---|---|---|---|
| Tagged ephemeral key | 1,000 min/mo; more than 4 h counts as a tagged resource (50 max) | limited by access rules on the tag | none |
| Untagged ephemeral key, user-owned | probably a free user device (inferred from the FAQ) | everything the user can reach; access rules can't target it alone | a user API token (90 days at most), since OAuth-minted keys are probably tag-only (unverified) |
| Provider channel (exec API plus a port tunnel or token-gated URL) | none | nothing; nodes talk to it | one adapter per provider, and a node hop for every client |
| A second control server on the hub (ionscale supports many tailnets; Headscale supports one) | none | only the VM tailnet; nodes join both | running a control server and a DERP relay |

With an untagged key the agent shell can't use the tailnet directly, because only the Glitched process holds it. That holds only if Glitched runs as a different OS user from the agent and keeps its node key where the agent can't read it.

## How bb does it (bb.app `environment-modal-sandbox`, read 2026-10-22)

- The plugin builds a Modal image from an editable Dockerfile. It creates the sandbox tagged with the machine key (1 CPU, 2 GiB, and Modal's 24 h maximum), and gives core an executor backed by `sandbox.exec`. It opens no ports or tunnels.
- Core installs the daemon through that executor with a one-time enroll key. The daemon then dials the bb server, either at a directly reachable `machineServerUrl` (such as a Tailscale Serve URL, which Modal can't reach) or through getbb.app's public machine gate with a per-machine credential.
- An idle pause after 15 minutes drains work, snapshots the filesystem, records the snapshot and only then terminates. Resume restores from the snapshot without rerunning setup.

Worth copying, whatever the network ends up being: the provider interface keyed by machine key, bootstrap through the provider's exec channel, setup scripts in the repo, and pausing as snapshot, record, then terminate.
