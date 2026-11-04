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
| 1 | Review #2 at 9ab4757 (round 1, whole change) | Running. Opus and Sol reviewers started 2026-11-04. | Both reports are in, verified, and posted as PR comments. |
| 2 | Fix verified high-severity findings on #2 | Waiting on task 1 | A fresh review of the fix diff has no high-severity finding. At most three fix rounds. |
| 3 | Make #2 landable by rebase-merge | Blocked on Nelson (questions.md Q1) | GitHub reports `rebaseable: true` for the PR that carries #2, and its tree equals the reviewed head. |
| 4 | Land #2 | Blocked on Nelson (Q1, Q2) and tasks 1 to 3 | `gh pr view` shows it merged on main, and main's CI is green. |
| 5 | Post-#2 work | Waiting on Nelson (Q3, Q4) | Nelson answered the order in questions.md. |

## PR #2 facts (checked 2026-11-04)

- Head 9ab4757, base main 85689e1, 45 commits, 68 files, +5165 / -196. Draft.
- CI run 41754148365 was pending at the time of writing.
- GitHub API: `mergeable: true`, `rebaseable: false`. The merge commit 9ab4757 (main's #12 merged into the branch) stops a rebase-merge.
- Bumps `api.Protocol` from 3 to 4 and the store schema from 4 to 5 (adds `parent`, `tokens`, `reported` to `threads`).
- The 2026-11-04 work thread pushed 15699e3 (perf) and 9ab4757 (merge). No independent reviewer had seen those two commits.
