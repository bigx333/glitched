# Thread menu

Right-clicking a thread in the sidebar opens its menu: Rename, Snooze ›, Mark settled, Back to inbox, Copy branch name, Open in editor, Archive, Delete. Each one moves or changes the thread. Settled and snoozed threads move to their own sidebar sections. Archived threads leave the inbox for Settings › Archived threads. Delete asks for confirmation first.

## Sub-features

- `menu-rename` edits the title in place (a field labeled `Thread title`). Escape cancels.
- `menu-snooze` hides the thread until later, in a `Snoozed` section.
- `menu-settle` moves it to `Settled`. This is disabled while the thread is working.
- `menu-archive` archives it (toast `Archived`, disabled while live). Settings › Archived threads restores it.
- `menu-delete` deletes after a confirm dialog (`Cancel` / `Delete`).
- `menu-copy-branch` copies the branch name, or toasts `No branch to copy`.

## How to get to it (user POV)

- Right-click a thread row in the sidebar.
- Hovering a row shows its snooze and settle actions in place of the time.

## Driving it with glv

Preconditions:

- One finished thread: follow [start a thread](./start-thread.md) through **Finish**, so the thread is `idle`.
- **The Glitched window must stay in front.** The row's menu has no accessibility action (the row offers only `AXPress`, `AXScrollToVisible`), so it needs a real right-click. Ask the user to click the Glitched window and leave it in front.

- **Open the menu.** Run `$G rightclick "verify: start thread"`, then `$G tree AXMenuItem` and `$G shot menu-open`. It lists `Rename`, `Snooze`, `Mark settled`, `Back to inbox`, `Copy branch name`, `Archive`, `Delete`.
- **Archive.** Run `$G menu Archive`, then `$G wait Archived 10` and `$G shot menu-archived`. The row leaves the inbox.
- **Restore.** Run `$G press Settings AXButton` and `$G press "Archived threads" AXButton`. The thread is listed; restore it, then `$G press "Back to app" AXButton`. The row is back in the inbox.
- **Settle.** Open the menu again, then run `$G menu "Mark settled"`. A `Settled` section appears holding the thread.

## Gotchas

- **Not yet verified.** When this skill was written, every right-click was refused (`pid <user's app> covers x,y, not <ours>; not clicking`), because the user's browser held focus. The menu items' roles (`AXMenuItem`) and `$G menu` working on them are unconfirmed. Check with `$G tree` and fix this recipe on the first successful run.
- Don't loop on a refused right-click. Each try briefly pulls the window forward over the user's work.
- `Archive` and `Mark settled` are disabled while a turn runs. Finish the turn first.
- `Delete` is permanent for the list (history stays in the engine log). Use it only on threads this run created.
