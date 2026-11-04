# Decisions

Newest first. Each entry has the date, who decided, and the decision.

## 2026-11-04

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
