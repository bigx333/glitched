# Project state

Updated 2026-11-04 by the PM thread (Claude Opus 5.5).

## Goal

Land the open PRs first, starting with #2. Then split glitched into modules and test compile-time plugins, so people add features without forking the whole app. Rebuilding the app locally to add a plugin is acceptable.

## Merge rule

A PR merges to main by rebase-merge only when all of these hold at its current head:

- CI jobs vet, test and race are green on the Ferrum GARM runners.
- An Opus 5.5 and a GPT-6.1 Sol interrogate review report no high-severity finding.
- Nelson said yes to any `api.Protocol` bump or schema migration in it.

## Tasks

| # | Task | Status | Done when |
|---|---|---|---|
| 1 | Review #2 at 9ab4757 (round 1, whole change) | Done. Sol: 2 high verified, 1 dismissed. Opus: 1 cause (Halt on a signalled run goes uncertain), blocking as a regression of main. Posted on #2. | Both reports are in, verified, and posted as PR comments. |
| 2a | Make the root test package fit CI's 20-minute limit | Done: CI root 210 s at 20dfea6. CI failed on 9ab4757: root package timed out at 1200 s (main: 799 s). | CI `test` is green on `land/pr-2` with margin, TestShot renders unchanged. |
| 2 | Linear copy of #2 on `land/pr-2`, opened as a new PR | Done: #22 at 9ffcc71. PM checked: no tree diff to 9ab4757, no merge commits, `rebaseable: true`. | `git diff 9ab4757 origin/land/pr-2` is empty, GitHub reports `rebaseable: true`, local vet, test and race pass. |
| 3 | Fix verified high-severity findings on `land/pr-2` | Fix round 2 pushed (2cc7903): delete closes the agent through the outbox; a message brings a settled thread back; five flaky tests fixed. Round 3 review (Opus, Sol) and a real-app run in progress. Real-app run at 20dfea6 passed 6 of 6. Fix rounds used: 2 of 3. | A fresh review of the fix diff has no high-severity finding. At most three fix rounds. |
| 4 | Land the copy, close #2 with a link | Waiting on tasks 1 to 3 | The copy is merged on main by rebase-merge, main's CI is green, #2 is closed with a link. |
| 5 | HTML plan for the Claude Code adapter (in core) | Plan written, waiting on Nelson (Q5). | The plan passes the html-plan linter and Nelson has answered its decisions. |
| 6 | Claude Code adapter, in slices from the plan | Waiting on task 5 | Each slice's done-when check from the plan. |
| 7 | Agent spawn tool, in core | Waiting on task 6 | To be set by its plan. |
| 8 | Child-thread bugs #14, #15, #16 | Waiting on task 4 | Each fix has a test that fails without it, and its PR merges under the rule. |
| 9 | Browser bugs #17 to #20 | Waiting on task 4 | Same as task 8. |
| 12 | Doc nit: Mark settled is greyed in the thread menu while a thread waits on an approval (main's rule); `docs/daemon/child-threads.md` says it offers Settle | Final nit batch on #22 | The doc matches the menu. |
| 11 | MyGo v0.5.5 to v0.7 upgrade, restore the CPU glow halo with `Cache` | After task 4 | TestShot renders match the pre-#22 glow, CI time stays near 210 s. |
| 10 | Modules and plugin HTML plan | Waiting on tasks 6 and 7 | Nelson has answered the plan. |

## PR #2 facts (checked 2026-11-04)

- Head 9ab4757, base main 85689e1, 45 commits, 68 files, +5165 / -196. Draft.
- CI run 41754148365 was pending at the time of writing.
- GitHub API: `mergeable: true`, `rebaseable: false`. The merge commit 9ab4757 (main's #12 merged into the branch) stops a rebase-merge.
- Bumps `api.Protocol` from 3 to 4 and the store schema from 4 to 5 (adds `parent`, `tokens`, `reported` to `threads`).
- The 2026-11-04 work thread pushed 15699e3 (perf) and 9ab4757 (merge). No independent reviewer had seen those two commits.
