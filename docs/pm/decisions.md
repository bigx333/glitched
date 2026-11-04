# Decisions

Newest first. Each entry has the date, who decided, and the decision.

## 2026-11-04

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
