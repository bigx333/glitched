# Glitched

Glitched is a native desktop app and engine daemon for running and coordinating AI coding agents across local workspaces, git worktrees, and paired Tailnet machines.

## Overview

- **Native GUI (`package main`)** — Built with [MyGo](https://github.com/egoist/mygo), featuring a live thread inbox, streaming transcripts, inline approvals/questions, unified diffs, terminal drawer, and an embedded preview/agent browser.
- **Engine Daemon (`internal/daemon`, `internal/engine`, `internal/store`, `internal/runtime`)** — Pure decision engine backed by a SQLite WAL event log, projection tables, and transactional outbox. Runs standalone (`glitched serve`) or embedded automatically when the GUI launches.
- **Multi-Provider Support (`internal/provider`)** — Runs Claude Code (`claude`), Codex (`codex`), Antigravity/ACP (`agy`), and a deterministic `fake` provider for development and tests.
- **Multi-Machine Tailnet Mesh (`internal/transport`, `internal/tailnet`, `internal/hub`)** — Pair remote engines over Tailscale (or an embedded `tsnet` node) with challenge-response HMAC auth, child-thread delegation across machines, and hub log replication.
- **Plugins & MCP (`internal/plugins`, `internal/mcp`, `internal/browsermcp`)** — Extensible plugin services/tools and built-in MCP servers for child-thread orchestration and browser automation.

## Prerequisites

- **Go 1.27+** (no CGo required for building or running)
- **macOS** (for the native desktop UI; Linux/Windows builds of the CLI/engine are supported too)
- **Optional provider CLIs** on `PATH` (or configured under *Settings › Providers*):
  - `claude` (Claude Code)
  - `codex` (OpenAI Codex CLI)
  - `agy` (Antigravity CLI)

## Quick Start

Build the desktop app and CLI:

```sh
go build .
go build ./cmd/glitched
```

Run common CLI commands:

```sh
glitched serve                  # run the host engine (and hub by default)
glitched status                 # inspect engine, hub, tailnet, and pairing tokens
glitched doctor                 # check state dir, config, DB integrity, tailscale, and CLIs
glitched pair -name laptop      # mint a pairing token for a remote GUI or CLI
glitched run "fix the tests"    # create a thread and stream a turn in the terminal
```

## Development & Testing

Glitched builds with `CGO_ENABLED=0` and uses MyGo's static vet pass:

```sh
CGO_ENABLED=0 go vet ./...
go tool mygo vet .
CGO_ENABLED=0 go test -count=1 -run '<Pattern>' .      # root UI tests that cover the change
CGO_ENABLED=0 go test -count=1 ./internal/<pkg>/...    # each package you touched
```

Don't run `go test ./...` unsharded. The root package's UI tests exceed Go's 10-minute default timeout. CI splits them across 8 runners, and you can run one shard the same way:

```sh
RUNNER_TEMP=$(mktemp -d) ./.github/root-shard run <i> 8 2   # i is 1 to 8
```

See [AGENTS.md](AGENTS.md) for the full local workflow.

Run race-detector checks (requires CGo):

```sh
CGO_ENABLED=1 go test -race -count=1 ./internal/engine ./internal/store ./internal/runtime ./internal/client ./internal/daemon ./internal/transport ./internal/provider/codex ./internal/provider/acp
```

## Architecture & Docs

- [Daemon Architecture](docs/daemon/architecture.md) — Peer engine model, command/event log, outbox, and flows
- [Networking & Pairing](docs/daemon/network.md) — Tailnet listeners, challenge-response auth, and revocation
- [Child Threads](docs/daemon/child-threads.md) — Sub-agent spawning, tree cascades, and push-back reports
- [Placement & Handoff](docs/daemon/placement-and-handoff.md) — Multi-machine thread placement
- [Browser Integration](docs/browser.md) — Preview pane, `webtarget` snapshot/refs, and `glitched-browser` MCP
- [Plugins](docs/plugins.md) & [Skills](docs/skills.md) — Plugin lifecycle and skill syncing
