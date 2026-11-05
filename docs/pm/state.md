# Project state

Updated 2026-11-05 by the PM thread (Claude Opus 5.5).

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
| 4 | Land the copy, close #2 with a link | **Done 2026-11-04 05:38Z.** #22 rebase-merged at 2cc7903; main is 604b182, linear. #2 closed with a link. #15 and #16 closed by the merge. Main CI green at 604b182 (run 41780484873: vet, test, race). | The copy is merged on main by rebase-merge, main's CI is green, #2 is closed with a link. |
| 5 | HTML plan for the Claude Code adapter (in core) | Done. Nelson answered Q5 on 2026-11-04. | Done. |
| 6 | Claude Code adapter, 5 slices | Slices 1 (#36), 2 (#42), 3 (#45) and 4 (#49) are merged. #51 (#47) landed at 24b7090 on 2026-11-05 with two Highs tracked in #59 (Q9).  AskUserQuestion PR A landed as #60 at 6f49cad (2026-11-05; #50 was closed for the `-v2` rule). PR B (the Claude mapping) landed as #62 at 4da7072. Its round 1 High, a deadlock between a misfit deny and an answer, was fixed in fix round 1. The Low about a comma in typed Other text is #63. #65 merged at ac0b747 (2026-11-05). It closed #59, #61 and #57 and fixed the client flake. Opus's non-regression Medium is #66, which does not gate slice 5. #64 (#52, cgroup containment) merged at 82b6ca1 after two fix rounds. On GARM, which has no user systemd, the scope tests skip and the fallback test passes. Nelson chose to ship slice 5 everywhere: Windows is not gated, and its gap is #44/#68, with Cahil tagged. **Slice 5 merged as #69 at 01c5115 (2026-11-05). The Claude Code adapter is done: Claude runs in the GUI on every platform.** Windows' Stop/Delete gap is #44/#68. The flaky client, transport and runtime tests are tracked in #70. The agent spawn tool plan is answered (Q11, plus a held report after a manual Stop). The plan is `/tmp/glitched-plans/spawn-tool.html`. PR 1 (the engine spawn path behind the fake, `feat/spawn-engine`) is in progress. PRs 2 to 4 follow: `glitched mcp` and its injection, the report wake with Protocol 6, and the status tools. A worker is fixing the approval-gate issues #59, #61 and #57 in one PR (`fix/approval-gate-covers`). Slice 5 is gated on #59, #61, #52 and #44. | Each slice's done-when is in `/tmp/glitched-pm/adapter-context.md`. |
| 7 | Agent spawn tool, in core | Waiting on task 6 | To be set by its plan. |
| 8 | Child-thread bugs #14, #23 | **Done.** #23 is in #27. #14 is in #43, with Protocol 5 and schema 6, merged 2026-11-04. | Done. |
| 9 | Browser bugs #17 to #20 | **Done.** #26 (#17), #29 (#18), #40 (#19, the rebase of #30), #28 (#20), all merged 2026-11-04. | Done. |
| 12 | Doc nit on Mark settled for a waiting thread | Filed as #25 | The doc matches the menu. |
| 14 | Follow-ups filed 2026-11-04: #31 (layout fsync on UI thread), #32 (settled-thread message edges), #34 (transport test flaky under load), #35 (script at document start) | Open | Triage after the adapter. |
| 15 | Windows: stopping claude, and pipe deadlines (#44) | Open | Before slice 5 ships Claude in the GUI on Windows. |
| 16 | Follow-ups filed after #43 and #45: #46 (Write/Edit card content; lost answer race), #47 (approval surface security, blocks slice 5, being fixed), #48 (Duplicate re-tell, low) | Open | #47 before slice 5. |
| 13 | Refuse an unknown effect kind before leasing it | Filed as #24 (design note) | A build boots cleanly on a store with an unknown kind. |
| 11 | MyGo v0.5.5 to v0.7 upgrade, restore the CPU glow halo with `Cache` | After task 4 | TestShot renders match the pre-#22 glow, CI time stays near 210 s. |
| 10 | Modules and plugin HTML plan | Waiting on tasks 6 and 7 | Nelson has answered the plan. |

## Landed

- 2026-11-05: #49 (adapter slice 4: stop, steer, crash and resume). It took 3 fix rounds, and the final Opus and Sol reviews found no high or medium.
- 2026-11-04: #43 (#14 fixed with `api.Thread.LastReport`, Protocol 5, schema 6). It took 3 fix rounds, and the final review found no high or medium.
- 2026-11-04: #45 (adapter slice 3: tool prompts answered from the inbox, ASCII-only approval text). It landed under Nelson's rule with #47 open.
- 2026-11-04: #42 (adapter slice 2: a Claude Code turn streams through `glitched serve`).
- 2026-11-04: #27 (#23), #26 (#17), #28 (#20), #29 (#18), #36 (adapter slice 1), #40 (#19).
- 2026-11-04: #22 (linear copy of Cahil's #2) at 2cc7903. api.Protocol 4, schema 5. Reviews: https://github.com/bigx333/glitched/pull/2#issuecomment-6620473978, https://github.com/bigx333/glitched/pull/22#issuecomment-6622283563, https://github.com/bigx333/glitched/pull/22#issuecomment-6623521211
