# Ephemeral VMs (deferred)

Put off on 2026-10-22. These are findings so far, not a design. The VM lifecycle in `control-plane.md` still holds: `Create`, `Remove`, optional `Suspend` and `Resume`, all idempotent by machine key.

## Providers

Candidates are E2B, Daytona, Modal, Cloudflare Sandbox, Vercel Sandbox, Fly Sprites and Fly Machines. Pricing is out of scope for now. E2B is Apache-2.0 and can be self-hosted, but needs KVM. Daytona's open-source repo is archived.

## How a VM reaches the tailnet

tsnet runs in userspace, so a VM only needs outbound HTTPS. There are four options:

| Option | Tailscale limits | What the VM can reach | What it needs |
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
