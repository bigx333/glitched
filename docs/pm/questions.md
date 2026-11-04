# Questions for Nelson

Newest first. Answer under each question, or in the PM thread.

No open questions.

## Q5 (2026-11-04, answered; see decisions.md). Claude Code adapter plan: five decisions

The plan is `/tmp/glitched-plans/claude-adapter.packed.html` (local file, not committed). Each decision sits on the claim it changes. The PM's recommendation is first in each.

1. **Channel for tool prompts and interrupt.** `claude -p` with stream-json both ways and the SDK control protocol (probed, one pipe, but the `stdio` prompt-tool value is not in the CLI docs). Or an MCP prompt tool the engine serves. Or the TypeScript SDK in a Node sidecar.
2. **"Approve for me" mode.** `acceptEdits`, with new picker text. Or `auto` (a classifier decides; matches today's text).
3. **User's Claude configuration.** Load all of it, as `claude` in a terminal does, so the user's rules and hooks apply. Or `--safe-mode`.
4. **AskUserQuestion.** Turn the tool off for now (no Protocol bump). Or support it now (`api.Protocol` 5, a question card, a new answer shape).
5. **Reconcile after a crash.** Stop the leftover process and answer Lost (the run becomes Uncertain). Or a per-run shim so Reconcile can reattach. Or Lost without stopping the process.

With 1, 2, 3 and 5 as recommended, there is no Protocol bump and no schema change.

Answered questions stay below for the record.

## Q4 (2026-11-04, answered: spawn tool in core). Is the spawn tool the first plugin, or does it land in core first?

**Recommendation: neither comes first. Make the Claude Code adapter the first plugin, and the spawn tool the second.**

- The spawn tool is a tool an agent calls. Only the fake provider exists, so no real agent can call it yet. As the first plugin it would test the plugin API against a caller that does not exist.
- A provider seam already exists: `provider.Adapter` and `provider.Agent` in `internal/provider/provider.go`. The fake provider implements it. The Claude Code adapter (MVP step 2 in `docs/daemon/architecture.md`) is a second, real implementation. Two implementations of one interface is the best test of a plugin boundary.
- The spawn tool then becomes the second plugin, of a new kind (an agent tool). It tests a second seam once a real agent can call it.
- Building the spawn tool in core and moving it out later is work done twice. The plugin plan comes first, so nothing needs to move.

Other options: spawn tool as the first plugin (tests the plugin API sooner, but only against the fake provider), or spawn tool in core now and plugins later (fastest to a feature, but it moves twice).

## Q3 (2026-11-04, answered: Claude Code adapter first, in core, before plugin work). Order of work after #2 lands

**Proposal.**

1. **Child-thread bugs #15 and #16, then #14.** In both, a tree delete races another command (an earlier Stop in #15, another client's follow-up in #16) and the tree stays alive. Settle now uses the same stop path as Delete, so it likely has the same hole (inferred, not yet run). #14 is a small notification bug. One worker each, small PRs.
2. **Browser bugs #17 to #20.** Four small, independent fixes. They run in parallel with item 3.
3. **Plugin and modules HTML plan.** Module boundaries, a public plugin package with its own version (separate from `api.Protocol`), Caddy-style registration at compile time, and a `glitched build` command that rebuilds the app with chosen plugins. You answer the plan before any code.
4. **Claude Code adapter as the first plugin** (see Q4).
5. **Agent spawn tool as the second plugin.**

Bugs go first because the plugin work moves the code they live in. Fixing them first keeps each fix small and gives the refactor tests to hold it.

## Q2 (2026-11-04, answered: approved). Do you approve #2's protocol bump and schema migration?

#2 bumps `api.Protocol` from 3 to 4 and the store schema from 4 to 5. The migration is forward only. It adds three columns to `threads`: `parent`, `tokens` and `reported`. Every paired peer must update together, because a version 3 peer is refused. The reviewers are checking the migration against an existing version 4 database.

## Q1 (2026-11-04, answered: linear copy PR). #2 cannot be rebase-merged as it stands. How should it land?

GitHub reports `rebaseable: false` for #2. The cause is merge commit 9ab4757, which merged main's #12 into the branch to resolve conflicts. A rebase-merge replays the 44 commits without that merge, and they conflict again.

**Recommendation: a linear copy in a new PR.** A worker replays #2's commits onto main on a new branch (`land/pr-2`), keeps Cahil as author of every commit, and resolves the #12 conflicts in the commit where they first occur. The check is `git diff 9ab4757 land/pr-2` printing nothing, so the review of 9ab4757 still covers it. The new PR is rebase-merged, and #2 is closed with a link to it. Cahil's branch is not touched.

Other options:

- **Squash-merge #2.** One commit, Cahil as author. Simple, but it breaks the rebase-merge-only rule and loses the commit history.
- **Ask Cahil to rebase his own branch.** Keeps his PR, but it waits on Cahil, and only he may force-push his branch.
