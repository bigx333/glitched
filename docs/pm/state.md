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
| 1 | Review #2 at 9ab4757 (round 1, whole change) | Done. Rounds 2 and 3 reviewed the fix diffs on #22. | Both reports in, verified, posted. |
| 2a | Make the root test package fit CI's 20-minute limit | Done: CI root 210 s at 20dfea6. CI failed on 9ab4757: root package timed out at 1200 s (main: 799 s). | CI `test` is green on `land/pr-2` with margin, TestShot renders unchanged. |
| 2 | Linear copy of #2 on `land/pr-2`, opened as a new PR | Done: #22 at 9ffcc71. PM checked: no tree diff to 9ab4757, no merge commits, `rebaseable: true`. | `git diff 9ab4757 origin/land/pr-2` is empty, GitHub reports `rebaseable: true`, local vet, test and race pass. |
| 3 | Fix verified high-severity findings on `land/pr-2` | Done in 2 fix rounds. Round 3: no high (Opus 1 low, Sol 2 medium: one filed as #23, one dismissed and filed as design note #24). Real-app runs passed 6/6 at 20dfea6 and at 2cc7903. | Done. |
| 4 | Land the copy, close #2 with a link | **Done 2026-11-04 05:38Z.** #22 rebase-merged at 2cc7903; main is 604b182, linear. #2 closed with a link. #15 and #16 closed by the merge. Main CI running. | The copy is merged on main by rebase-merge, main's CI is green, #2 is closed with a link. |
| 5 | HTML plan for the Claude Code adapter (in core) | Plan written, waiting on Nelson (Q5). | The plan passes the html-plan linter and Nelson has answered its decisions. |
| 6 | Claude Code adapter, in slices from the plan | Waiting on task 5 | Each slice's done-when check from the plan. |
| 7 | Agent spawn tool, in core | Waiting on task 6 | To be set by its plan. |
| 8 | Child-thread bugs #14, #23 (#15, #16 fixed in #22) | Held behind the adapter, per Nelson's order | Each fix has a test that fails without it, and its PR merges under the rule. |
| 9 | Browser bugs #17 to #20 | Held behind the adapter, per Nelson's order | Same as task 8. |
| 12 | Doc nit on Mark settled for a waiting thread | Filed as #25 | The doc matches the menu. |
| 13 | Refuse an unknown effect kind before leasing it | Filed as #24 (design note) | A build boots cleanly on a store with an unknown kind. |
| 11 | MyGo v0.5.5 to v0.7 upgrade, restore the CPU glow halo with `Cache` | After task 4 | TestShot renders match the pre-#22 glow, CI time stays near 210 s. |
| 10 | Modules and plugin HTML plan | Waiting on tasks 6 and 7 | Nelson has answered the plan. |

## Landed

- 2026-11-04: #22 (linear copy of Cahil's #2) at 2cc7903. api.Protocol 4, schema 5. Reviews: https://github.com/bigx333/glitched/pull/2#issuecomment-6620473978, https://github.com/bigx333/glitched/pull/22#issuecomment-6622283563, https://github.com/bigx333/glitched/pull/22#issuecomment-6623521211
