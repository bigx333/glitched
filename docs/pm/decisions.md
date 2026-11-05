# Decisions

Newest first. Each entry has the date, who decided, and the decision.

## 2026-11-05. #64: work sent to the user's own tmux is a known limit (Nelson, Q10)

Sol's round 1 High on #64 was that a tool can send work to a tmux server the user already runs, and that work outlives Close. No cgroup can contain it, because the server is the user's own process. Nelson accepted it as a documented limit, so it does not block slice 5. Fix round 1 on #64 does three things:
- falls back to the marker scan when `systemd-run` fails, instead of aborting the turn (Sol's Medium);
- corrects the "cannot leave its cgroup unasked" comment, because tmux moves its panes into scopes of their own (Opus's Low);
- documents the tmux limit.

## 2026-11-05. #62 merged; a root-package flake is traced to #61 (PM)

- **#62 (AskUserQuestion PR B) was rebase-merged at 4da7072.** In round 1, Opus and Sol both found a lock-order deadlock between a misfit deny and `Do(Respond)`, and the PM reproduced it. Fix round 1 sends the deny after `pmu` is released and writes down the lock order. The re-check found no High. Opus's Low is #63.
- **A flaky root test.** CI on #62 failed once in `TestAnAnswerNeedsACardThatStoodStill/⌘⏎`, with "⌘⏎ answered the next card 1 presses after it landed". #62 does not touch the root package, so the PM reran the failed job, and it passed. The PM treats the flake as a timing-dependent face of #61, a held chord carrying over to the next card. The approval-gate worker must make the test deterministic and prove it under CPU load.

## 2026-11-05. #60 merged; #61 gates slice 5; the approval-gate issues go in one PR (PM)

- **#60 (AskUserQuestion PR A) was rebase-merged at 6f49cad.** Its rebase check found one High, which fix round 1 fixed: the question card's keys answered an approval waiting at the same time. The re-check found no High. Sol had no findings, and Opus had one Medium and one Low.
- **Opus's Medium is #61, a slice 5 gate.** One held ⌘⏎ with key repeat answers a card, then allows the next approval. This is already on main with two approvals waiting. It is filed as a gate because a held key can allow a command the user did not press for.
- **#59, #61 and #57 go in one PR.** All three live in the same approval key and read code, so separate PRs would conflict. The brief asks for one model of covers that reads all geometry from the same frame, not more patches.
- **PR B (the Claude AskUserQuestion mapping) runs in parallel.** It touches only `internal/provider/claude` and must not change the UI key logic.

## 2026-11-05. #51 landed with two Highs tracked in #59 (Nelson, Q9)

Round 4 (2ad59ed) fixed what it claimed, and CI was green. Opus and Sol each found a new High, and the PM reproduced both:
- Sol: the composer's position lags one frame behind the card's.
- Opus: the open agents panel hides card lines that still count as read.

Both are covers over the card that the read gate does not know about. Nelson chose to land #51, because it is strictly safer than main and only the fake provider reaches these cards before slice 5. #51 was rebase-merged at 24b7090. #59 tracks both Highs and the Lows. **#59 gates adapter slice 5.** Its fix reads the card geometry and every cover from the same frame. #47 is closed.

## 2026-11-05. #51 gets a fourth fix round (Nelson, Q8)

Opus's round 3 review found one High, and the PM reproduced it at 7dc7eeb. A composer taller than `composerRoom` hides card lines, but those lines still count as read. Nelson chose one more fix round. In that round, the read gate measures from the composer's real top, Opus's repro becomes a regression test, and Sol's two mediums are folded in. Opus and Sol then re-check only the round 4 diff. #51 stays the security gate, so "land with an issue" does not apply to it.

## 2026-11-04

- **PM (Claude), Nelson away.** On #49 (slice 4), Sol's high on escaping the env marker is downgraded to medium for this PR. The cases are `env -u`/`env -i`, a different marker, and `PR_SET_DUMPABLE=0`. It is filed as #52, which **gates slice 5**: per-run cgroup containment on Linux, with the marker kept as a fallback. Reason: the marker, `Setpgid` and `Pdeathsig` cover ordinary tools. Escaping them needs a tool that scrubs its environment or makes itself undumpable, and real containment is a platform design. Fix round 1 takes Opus F1 (no work after Stop), Sol's restart evidence and pidfd findings, and Opus F3. Nelson can override.
- **Gates before slice 5 turns Claude on in the GUI:** #47 (PR #51), #52 (cgroup containment) and #44 (Windows stop and deadlines).
- **PM (Claude).** On #51 (#47), I accepted the worker's change from "double every backslash". `plain.Text` doubles backslashes only right before an escape or a literal `u{`. This keeps shell commands readable: doubling every backslash would show `rm foo\ bar` as `rm foo\\ bar`, a different command. The display stays one-to-one, which `FuzzText` checks with a decoder. One-to-one display was #47's actual requirement.
- **PM (Claude).** Merged #43 (#14, Protocol 5, schema 6). CI is green on GARM, and the final Opus and Sol reviews found no high or medium. The Duplicate re-tell low is filed as #48, since main behaves the same.
- **PM (Claude).** Merged #45 under Nelson's rule (a high still open after the final fix round means land as is, with an issue). Its two remaining highs are a tall command hiding its first line and assistant text concealing the CLI prompt. Both use only ASCII, both were present before round 3, and both are in #47, which blocks slice 5. Until slice 5, Claude prompts are reachable only through `glitched serve`.
- **Nelson.** Agent text on the approval card and in the CLI `[y/N]` prompt uses an **ASCII allowlist**. Printable ASCII and newlines show as they are, every other character shows as `\u{…}`, and a "contains non-ASCII text" flag appears. #45 gets one final fix round. If a high is still open after it, #45 lands as is with an issue. The reason: round 3 found two ways past the denylist escaper. One is implicit right-to-left text (a Hebrew letter flips the line). The other is invisible characters outside Cc and Cf (`./check` + U+FE0F draws pixel-identical to `./check`).
- **PM (Claude).** AskUserQuestion PR B renders option labels and Other text, so it must use the same allowlist function.
- **Nelson.** #43 gets one more fix round, the third and final one. It is Opus's client-only fix: when a same-log snapshot shows a parent whose `LastReport` moved, tell that parent's children with the `notified` threshold. After that comes a short Opus+Sol review of only that diff. If a high finding is still open after this round, #43 lands as is, with an issue for the gap.
- **PM (Claude).** Merged #42 (adapter slice 2): CI green on GARM, round 2 found no high finding, and the real-app run passed 5 of 5. The `GLITCHED_TEST_CLAUDE` crash (medium) and the stale write deadline go into slice 3. Windows stop and deadlines are #44.
- **PM (Claude). Security note for Nelson.** The #42 real-app run printed this host's T3 harness `ANTHROPIC_AUTH_TOKEN` into its local session transcript. It is not in any evidence file or on GitHub. Suggest rotating that token.
- **Nelson (Q6, Protocol 5).** All six choices follow the recommendations in `/tmp/glitched-plans/askuserquestion.html`:
  1. AskUserQuestion answers go by position: `Answers[i]` answers `Questions[i]`.
  2. Dismiss sends a deny with "The user dismissed the questions without answering."
  3. Both questions and approvals send a notification.
  4. AskUserQuestion lands as two PRs. PR A carries the api, engine, GUI card and fake, and starts from main now. PR B carries the adapter mapping, after slice 3.
  5. **Schema 5→6 approved.** `ALTER TABLE threads ADD COLUMN report TEXT`, backfilled from each thread's last `child.reported` event. The PR adds an upgrade test under the app's SQLite driver.
  6. The #14 fix gets its own PR and lands first, carrying the Protocol 5 bump. PR A adds to the same version. No release goes out between the two.
- **Nelson.** Issue #14 gets an api fix. The thread's shell view (`api.Thread`) carries the parent's latest report (child, kind, log cursor), committed with the `child.reported` event. It ships in the same Protocol 5 bump as AskUserQuestion, so peers are broken once, not twice. PR #33 is closed. Its three client-side designs each failed review on the same premise, joining two unordered streams (Attack the Premise). Its tests carry over as acceptance tests.
- **PM (Claude).** The AskUserQuestion addendum plan now covers the #14 field too. It goes to Nelson before anything is built.
- **PM (Claude).** Verified on #42 (slice 2): Claude Code runs Bash tool shells in their own session, so SIGKILL to claude and a process-group kill leave them running after a Delete. SIGTERM stops them. Close must send SIGTERM first. This is recorded in the shared adapter context for slice 4.
- **Nelson.** Every `claude` process glitched launches gets `CLAUDE_CODE_ENTRYPOINT=glitched`, overriding any inherited value. That covers adapter runs, the transcript recorder, and anything else that spawns `claude`. Upstream Zeron does the same (zeronsh/zeron 0f3d11e, `crates/harness/src/process/owned.rs`). Reason: Anthropic meters the `sdk-cli` and `sdk-ts` entrypoints about 1.8 to 2.9 times harder on the 5-hour window (anthropics/claude-code#97074, pingdotgg/t3code#24649). Use our own name, and never spoof another client (`cli`, `claude-vscode`). A test asserts the env on the spawned command. This goes into adapter slice 2, which spawns the process.
- **PM (Claude). Incident in adapter slice 2.** The worker registered Claude in `daemon.Defaults`, as its brief said. That made the GUI's embedded engine use the real CLI. Five root GUI tests then ran real Claude Opus 5.5 turns in Nelson's home directory. The worker estimated 100k to 170k tokens per test, from the window. Only read-only commands ran, because Claude Code held the rest for approval. No process was left behind. Nothing was pushed.
- **PM (Claude).** Slice 2 registers Claude only for `glitched serve`. The GUI's embedded engine keeps the fake until slice 5 ("turn it on"), which is how the plan sequences it. A guard keeps any test without the `claudelive` tag from starting the real CLI.
- **PM (Claude).** Merged on 2026-11-04: #27 (#23), #26 (#17), #28 (#20). Each had CI green on GARM and no high review finding.
- **Nelson (Q5, adapter).**
  1. Transport: `claude -p` with stream-json both ways and the SDK control protocol. Add a contract test that pins the undocumented `--permission-prompt-tool stdio` behaviour against the CLI version we ship with.
  2. "Approve for me" runs `acceptEdits`, with new picker text.
  3. Load the user's full Claude config, as terminal `claude` does.
  4. Support AskUserQuestion now. Approved: `api.Protocol` 5, a question card and a new answer shape.
  5. After a crash, kill the leftover process and answer Lost (the run becomes Uncertain). A Lost run stays resumable through its Claude session id (`--resume`). Consider a process group plus `Pdeathsig`.
  - Ask first for any schema change beyond what this needs. Start the adapter now, and keep the bug PRs going in parallel. Same merge rule.
- **PM (Claude).** The adapter lands as the plan's five slices, one PR each, in order, each from a fresh origin/main. AskUserQuestion may become its own PR between slices 3 and 4. Shared worker context is in `/tmp/glitched-pm/adapter-context.md`.
- **PM (Claude).** On #29, Sol's high (a page's capturing `pagehide` listener, registered before install, stops ref invalidation) is downgraded to medium. Reason: it needs a page acting before the first snapshot, and the revived ref still names the same element in the same restored document. The proper fix (document-start injection or isolated worlds) is #35. Opus's F1 (a synthetic `pagehide` clears refs, a regression) is fixed in a round 1 fix.
- **PM (Claude).** Merged-on-green queue: #26, #27 and #28 have reviews with no high finding. Mediums and lows went to issues #31, #32 and #34.
- **Nelson.** Start bug issues #14, #17 to #20 and #23 now, in parallel, while the adapter plan waits on his Q5 answers. Same merge rule. Do not start the adapter yet.
- **PM (Claude).** One worker and one PR per issue. Shared rules are in `/tmp/glitched-pm/bug-worker-rules.md`. A PR that stops being rebaseable is redone on a `-v2` branch, never by force-push or by merging main into it.
- **PM (Claude).** Merged #22 by rebase-merge at 2cc7903 under Nelson's merge rule: CI green on Ferrum GARM (run 41777447443), round 3 Opus+Sol had no high finding, and Nelson approved Protocol 4 and schema 5. Sol's round 3 medium (#23) does not block, because main already refuses a steer to a stopping run. The downgrade finding (#24) is dismissed for this PR, because main refuses schema 5 and no user runs the PR's earlier heads.
- **PM (Claude).** A new message to a Settled thread, from any client, moves it back to the inbox and runs, in one engine commit. The GUI composer already behaves this way. Work queued before the settle stays cancelled. This follows Nelson's rule that the user may message any thread.
- **Nelson.** The engine deletes a live thread: Delete ends the run as `abandoned` and closes its agent in the same commit. This reverses main's rule "the engine deletes no live thread". The thread menu still hides Delete while the thread's own agent works.
- **Nelson.** Without a GPU, the model picker's glow draws no blur halo, for CI speed (root package about 1000 s down to about 210 s). GPU windows are unchanged. The halo comes back later with a MyGo v0.7 upgrade (its `Cache` API) in its own PR.
- **PM (Claude).** Fix round 1 rules on #22, all in the engine, with no api or schema change: a halt on a stopping run sends no second interrupt (fixes #15); Settle cancels the queue and stops the run in one commit, and a Settled thread starts nothing until moved back to the inbox; Delete ends a live run in its own commit (fixes #16).
- **PM (Claude).** Opus's finding (Stop, then Settle or Delete, leaves the run uncertain and the tree in place) blocks landing #2. Opus rated it medium, but for a lone thread it is a regression of main, which the merge rule treats as high. The same engine cause produces #15.
- **Nelson.** "Never open a child" means never create one. The user may view a child (report card click, sidebar row) and may send it messages like any thread. Sol's finding 3 on #2 is dismissed for this reason.
- **PM (Claude).** Sol's findings 1 (Delete leaves a child running when the GUI's cached state is stale) and 2 (a follow-up queued between Halt and Settle starts a turn on a Settled child) are high and block landing #2. Both reproduce at 9ab4757 (PM reran them). The fixes go on `land/pr-2`.
- **Nelson.** #2 lands as a linear copy in a new PR (`land/pr-2`) whose tree equals 9ab4757. That PR is rebase-merged and #2 is closed with a link. Cahil's branch is not touched. Fixes from the review go on `land/pr-2`.
- **Nelson.** #2's `api.Protocol` bump (3 to 4) and schema migration (4 to 5) are approved.
- **Nelson.** After #2, the Claude Code adapter comes first, built in core, before any plugin work.
- **Nelson.** The agent spawn tool lands in core, not as a plugin.
- **PM (Claude).** The adapter gets an HTML plan first (big feature). The plan starts now, in parallel with landing #2, because it writes no code.
- **PM (Claude).** No other work lands on main until #2 lands. Browser fixes #17 to #20 touch `main.go` and `preview.go`, so landing them first could break the copy's rebase.
- **PM (Claude).** These PM files live on branch `docs/pm` in a draft PR. They do not merge to main unless Nelson asks. A PR per status update would be noise on main.
- **PM (Claude).** The #2 review panel is Opus 5.5 (high effort) and GPT-6.1 Sol (high effort), as Nelson named. Grok is not on the panel.
- **Nelson.** A PR merges by rebase-merge only, after vet, test and race are green on Ferrum GARM and the Opus+Sol review has no high-severity finding. This includes Cahil's #2. Never force-push, never push to main, and push to someone else's branch only as fast-forward commits.
- **Nelson.** Big features (new API, schema change, many files, real design choices) get an HTML plan that Nelson answers before anything is built.
- **Nelson.** Ask Nelson about any `api.Protocol` bump or schema migration, and about anything that touches Cahil's PRs beyond agreed fixes.

## 2026-11-03

- **Nelson.** The user never creates or opens a child thread. Only agents create children.
- **Nelson.** Settle stops every running child. Settled means no further work on that thread, the same as archived.
- **Nelson.** The agent spawn tool goes in a follow-up PR, not #2.

## 2026-10-29

- **Nelson.** CI runs on Ferrum GARM runners only.
