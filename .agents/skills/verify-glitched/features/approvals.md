# Approvals

When the agent asks before acting, the chat shows an `Approval needed` card (`Claude wants to edit main.go`) and the thread waits in the inbox. The user allows or denies it. The card then records the answer (`Allowed` / `Denied`) and the turn continues. The composer's `Access` picker sets how freely the agent acts.

## Sub-features

- `approval-card` shows `Approval needed`, `waiting now`, the operation and path, with `Allow once ⌘⏎` and `Deny ⇧⌘D`.
- `approval-allow` allows: the card becomes `Allowed`, `Edit main.go` runs, and the turn ends at `Done.`.
- `approval-deny` denies: the card becomes `Denied`, and the engine logs the answer.
- `approval-inbox` shows the waiting thread in the inbox with a count on `Inbox` while it needs the user.
- `access-level` picks `Ask for approval`, `Approve for me` or `Full access` in the composer's `Access` popover.

## How to get to it (user POV)

- Send any message. The scripted turn always asks to edit `main.go`.
- Press the card's buttons, or `⌘⏎` / `⇧⌘D` while the card is showing.
- The `Access` button in the composer (shows `Approve for me` by default).

## Driving it with glv

Preconditions:

- Baseline state, then a sent message: `$G type "verify: approvals"`, `$G press Send AXButton`.

- **Card shows.** Run `$G wait "Approval needed" 20` and `$G shot approvals-card`. `$G tree "Allow once"` lists `AXButton | Allow once ⌘⏎`, and `$G cli threads` shows `waiting`.
- **Allow.** Run `$G press "Allow once" AXButton`, then `$G wait "Done." 20` and `$G shot approvals-allowed`. `$G tree Allowed` shows `Allowed Claude wants to edit main.go`.
- **Engine record.** Run `$G cli threads` (`idle`) and `$G cli replica -thread <id> > .verify/<run>/approvals-replica.txt`. The log has `request.opened`, `request.answered`, `request.delivered`, then `turn.ended`. Proven on 2026-10-26.
- **Deny.** Start another thread (`$G press "New thread" AXButton`, `$G type "verify: deny"`, `$G press Send AXButton`, `$G wait "Approval needed" 20`). Then run `$G press Deny AXButton`, `$G wait Denied 20` and `$G shot approvals-denied`. `$G tree Denied` shows `Denied Claude wants to edit main.go`, and the replica shows `request.answered`. Proven on 2026-10-26.
- **Access level.** Run `$G press Access AXButton`. The popover lists `Ask for approval …`, `Approve for me …`, `Full access`. Run `$G press "Ask for approval" AXButton`, then `$G press Access AXButton` again to close it, and `$G shot approvals-access`.

## Gotchas

- After `Deny`, the fake script still plays `Edit main.go` and `Done.`: the fake provider ignores the answer. The proof of a denial is the `Denied` card plus `request.answered` in the replica, not a missing edit.
- The fake provider asks for approval whatever the Access level, so access levels can't be shown changing the agent's behavior here. Verify only the picker's state.
- `Full access` may require holding (`Hold to grant full access…`). An AXPress might not satisfy the hold, so check the result rather than assuming it.
- The `Approve for me` text inside the card is static text, not the picker. The picker is the `Access` button.
- The keyboard shortcuts need the window in front. Prefer the card's buttons.
