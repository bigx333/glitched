# Glitched verification map

This directory is the maintained source for verifying the user-facing behavior of Glitched. Read this index before driving the app, then use the matching feature file as the recipe.

## Baseline preconditions

- `G=.agents/skills/verify-glitched/scripts/glv`, run from the repo root.
- `$G launch` printed `ready`, and `$G doctor` shows no `FAIL`. Its "no other Glitched instance" line matters: a second window has broken runs.
- State is empty after launch: one project chip (the home directory, shown by its name, e.g. `ncamargo`), no threads, the inbox reads `Inbox zero. Nothing needs you.`
- The engine runs the fake provider. Every turn plays: `Looking at the code.` → `Read main.go` → `May I edit it?` + approval for `main.go` → `Edit main.go` → `Done.`
- Never drive an instance this run did not start.

## Driving conventions

- Start each recipe from the baseline unless its preconditions say otherwise. `$G stop && $G launch` resets it.
- Match elements by accessible name (button text or `Label`), with a role when a name repeats: `$G press Send AXButton`.
- Prefer accessibility commands (`press`, `type`, `set`, `menu`). Use foreground commands (`click`, `rightclick`, `hover`, `key`) only where a recipe says no accessibility route exists. They need the window in front, see SKILL.md.
- Seed state through the UI. Use `$G cli` to seed state only when a recipe says so.

## Proof and skip reporting

- Capture the action and the result: a screenshot before and after (`$G shot <feature>-<step>`), plus `$G tree <filter>` for the element that changed.
- Pair what's visible with the engine's record: `$G cli threads`, `$G cli replica -thread <id>`.
- Save artifacts under `.verify/<run>/`, named with the feature ID and step.
- An entry point you could not drive (for example a foreground command refused while the user had focus) is reported as **not verified**, with the command and its refusal. Don't count it as verified through a different entry point.

## Feature entry contract

Each feature file starts with an H1 title and one paragraph describing the user-visible behavior, then exactly four H2 sections in this order: `Sub-features`, `How to get to it (user POV)`, `Driving it with glv`, `Gotchas`.

## Features

- [Start a thread](./start-thread.md): the composer, Send, a new thread's turn, and the thread in the sidebar.
- [Approvals](./approvals.md): Approval needed card, Allow once, Deny, and the Access level picker.
- [Command palette](./command-palette.md): Search / ⌘K, filtering, and running actions.
- [Settings and appearance](./settings.md): the Settings pages, themes from Settings, the menu bar and the palette.
- [Thread menu](./thread-menu.md): a sidebar row's context menu (rename, snooze, settle, archive, delete). Pointer only.
