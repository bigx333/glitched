---
name: verify-glitched
description: Launch an isolated build of Glitched (the native macOS MyGo desktop app for agent threads) from this checkout, drive its real window through the macOS accessibility tree, and capture screenshots plus engine event logs as proof. Use when you need to show a Glitched UI or engine change working in the real app, not only in ui.Tester tests.
---

# Verify Glitched

Glitched is a native macOS window (MyGo `ui`, no web view). Its sidebar holds the inbox of threads and a chat with a composer. An engine embedded in the app runs the threads. A `glitched` CLI (`cmd/glitched`) reaches the same engine. Every helper here goes through `scripts/glv`. Run it from the repo root:

```sh
G=.agents/skills/verify-glitched/scripts/glv
```

The feature map in [`features/`](features/README.md) lists what to drive. Read its README before you drive anything.

## Isolation rules

- One run = one build in `/tmp/glv-<run>/` (binaries, `state/` as `GLITCHED_DIR`, pid, log). The run name defaults to a hash of the checkout path. `GLV_RUN=name` starts a second run next to it.
- Glitched normally uses `~/.glitched`. The user's own app, a `go tool mygo dev` build in another worktree ("Glitched Dev"), or another agent's run may be up. Never drive or kill them. glv targets only the pid it started, and `doctor` lists the others.
- A second Glitched window on screen has broken readiness before: launch timed out at 30s while a stray instance was up. If `doctor` shows others you started, stop them first with `GLV_RUN=<theirs> $G stop` or `kill <pid>`. Ask the user about any others.
- The engine runs the **fake provider** (`internal/provider/fake`, the development default). Every turn plays the same script, whatever model the picker shows: "Looking at the code.", a read of `main.go`, "May I edit it?" with an approval, an edit, "Done.". It touches no files and calls no network.

## Launch

```sh
$G launch     # go build the app, the CLI and the native helper, start the app on an empty state dir
```

Launch counts the app ready only when all of these hold: the log shows `serving socket=/tmp/glv-<run>/state/engine.sock`, `glitched threads` answers, the window is on screen, and the accessibility tree shows the empty chat's `What should we build?`. It prints `ready: run …, pid …, state …, evidence …`. It takes about 5s (Go build is cached). Every launch wipes `state/` first, so there is no migration or seed step: the engine creates `engine.db`/`hub.db` on first start. Launch refuses if this run is already up.

## Doctor

```sh
$G doctor     # read-only; exit 1 if any line says FAIL
```

It checks that the pid is alive and runs this run's binary, that the engine answers on this run's socket, that the window is on screen, and that accessibility reaches it (this needs the Accessibility permission for the terminal that runs glv). It also checks that no other Glitched process is up, and that the build matches the checkout (HEAD plus a hash of uncommitted `.go`/`.kage` changes). If the build line fails, you edited code since launch: `$G stop && $G launch`. Run doctor first whenever something looks off.

## Drive

Drive the app through accessibility first. Those commands run while the window sits in the background, so they never steal the user's focus. Elements match by the start of their accessible name or description. That's the button's text or its `Label(...)` in the Go code, e.g. `Send`, `Allow once`, `Toggle diff  ⌘D`.

| Command | What it does |
|---|---|
| `$G tree [filter]` | dump the window: `role \| name \| description \| value` per element |
| `$G find <text>` | first match: role, name, global frame `x,y,w,h` |
| `$G press <text> [role]` | AXPress it, e.g. `$G press Send AXButton` |
| `$G type <text>` | set the composer (the window's `AXTextArea`) |
| `$G set <role> <text>` | focus the first element of role and set its value, e.g. the palette's `AXTextField` |
| `$G value <role>` | read the value of the first element of role |
| `$G has <text>` / `$G wait <text> [secs]` | assert / wait until some element's name, description or value contains text |
| `$G menu <text>` | choose an app menu bar item, e.g. `$G menu "Settings"`, `$G menu "Mocha"` |
| `$G cli <cmd> [args]` | the `glitched` CLI on this run's engine: `threads`, `replica -thread ID`, `run text…` |
| `$G shot <name>` | screenshot the window into the evidence dir (waits `GLV_SETTLE`=1.5s first) |
| `$G log` / `$G env` | the app's stderr / this run's paths and pid |

Foreground input is a last resort. Some UI is reachable only by pointer or keyboard: a thread row's context menu, hover-only actions, keyboard shortcuts. These commands need the window in front:

| Command | What it does |
|---|---|
| `$G rightclick <text>` / `$G click <text>` / `$G hover <text>` | real pointer event at the element's center |
| `$G key <keys>` | keystroke: `cmd+k`, `cmd+shift+l`, `escape`, `return` |

Each one first brings the window forward (`$G front`) and confirms that it really is in front. The pointer commands also check that the top window at the point belongs to this run's pid, and refuse otherwise. macOS doesn't let a process started in the background keep focus while the user is in another app: the window comes forward for about 0.5s, then focus returns to the user's app. While the user is working, these commands usually refuse with `pid N covers x,y, not <ours>; not clicking`. Don't retry them in a loop. Use the accessibility route if one exists (most actions are also in the palette or the menu bar). Otherwise ask the user to click the Glitched window and leave it in front, then retry.

Example (the journey proven when this skill was written):

```sh
$G type "verify: approval journey"
$G press Send AXButton
$G wait "Approval needed" 20 && $G shot approval
$G press "Allow once" AXButton
$G wait "Done." 20 && $G shot done
$G cli threads                     # <id>  idle  verify: approval journey
$G cli replica -thread <id> > .verify/<run>/replica-<id>.txt
```

## Evidence

All proof goes to `.verify/<run>/` in the checkout (gitignored; `$G env` prints the path; `GLV_EVIDENCE` overrides it). It survives `stop`, and `stop` adds the app log there as `<stamp>-app.log`.

- **Action and result:** take a screenshot before the action and one after it settles, plus `$G tree <filter>` text of the element that changed. One final screenshot is not proof.
- **Side effects:** check what the engine recorded, not only what the window shows. `$G cli threads` shows thread state (`waiting` while an approval is open, `idle` after the turn). `$G cli replica -thread <id>` shows the event log (`request.opened` → `request.answered` → `turn.ended`). Save them as `.txt` next to the screenshots.
- **Real user path:** press the controls the user would. Don't seed state through the engine API or call app methods when a control exists for that action. Use `$G cli` to seed state only for flows that start from existing threads, and say so in the report.
- **Timing:** the accessibility tree updates a frame before paint, and rows fold and fade in. `wait` then `shot` is right, because `shot` settles first. Raise `GLV_SETTLE` if a picture shows a half-drawn transition.
- **Not real:** turns come from the fake provider, so "Claude wants to edit main.go" is scripted. Don't cite it as proof that a real agent integration works.

## Cleanup

```sh
$G stop       # TERM, then KILL after 5s, only if the pid still runs this run's binary; copies the log; deletes /tmp/glv-<run>
ls .verify/<run>/   # evidence is still here
```

Run `stop` after every failed attempt too. A launch that timed out still leaves a process. Never `pkill`/`killall` Glitched: other instances belong to the user or other sessions. Delete `.verify/<run>/` yourself only once the evidence is no longer needed.

## Helpers

- `scripts/glv`: everything above. Running it with no args prints the usage line.
- `scripts/ax.js`: the JXA accessibility driver glv calls: `osascript -l JavaScript scripts/ax.js <pid> tree|find|press|set|value|has|menu|front|key …`.
- `scripts/native.swift`: compiled by `launch` to `/tmp/glv-<run>/bin/native`. `native winid <pid>` finds the window for `screencapture -l`, and `native click|move <pid> <x> <y> [right]` sends the guarded pointer events.

Requirements: macOS, Go, `swiftc` (Xcode command line tools), plus the Accessibility and Screen Recording permissions for the terminal app that runs glv.

## Not covered here

- The app's own tests (`CGO_ENABLED=0 go test ./...`, `ui.Tester` harness in `harness_test.go`) and `go tool mygo vet .`: see the `mygo-maintenance` skill. They complement this skill and don't replace a real-window proof.
- Remote servers (Settings › Servers, `glitched pair`) need a second engine on the tailnet. They aren't seeded here.
