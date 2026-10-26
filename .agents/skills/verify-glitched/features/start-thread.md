# Start a thread

A user types a message into the composer of an empty chat and sends it. Glitched creates a thread titled after the message in the chosen workspace and shows the agent's turn as it streams. The thread appears in the sidebar inbox.

## Sub-features

- `thread-compose` types into the composer (`Ask anything, @tag files/folders, or use / to show available commands`).
- `thread-send` sends with the `Send` button, or `⌘⏎`, creating a thread titled with the message.
- `thread-stream` shows the turn: the user bubble, `Looking at the code.`, a `Worked for …` work log, and ends at `Done.` (after an approval, see [approvals](./approvals.md)).
- `thread-sidebar` lists the new thread in the inbox with its project tile and `now`. A project chip for the workspace appears beside `All`.
- `thread-new` starts another empty chat with `New thread` (sidebar), `⌘N`, or Thread › New Thread.

## How to get to it (user POV)

- The empty chat after launch, or `New thread` in the sidebar.
- Thread › New Thread in the menu bar, `⌘N`, or the palette's `New thread` / `New thread in <project>`.
- Type in the composer, then press the round `Send` button or `⌘⏎`.

## Driving it with glv

Preconditions:

- Baseline state (no threads). `$G has "What should we build?"` succeeds.

- **Empty chat.** Run `$G shot thread-empty`. The window shows `What should we build?` and the composer.
- **Compose.** Run `$G type "verify: start thread"`, then `$G value AXTextArea`. It prints `verify: start thread`.
- **Send.** Run `$G press Send AXButton`, then `$G wait "Looking at the code." 20`. The chat header title reads `verify: start thread`, and the composer's button is now `Stop` (`$G has Stop`).
- **Engine record.** Run `$G cli threads`. One line, `<id>  waiting  verify: start thread` (waiting on the scripted approval). `$G cli replica -thread <id>` starts `thread.created`, `message.queued`, `turn.admitted`, `turn.started`.
- **Sidebar.** Run `$G tree "verify: start thread"`. It includes `AXButton | verify: start thread now`, the inbox row. Then `$G shot thread-sent`.
- **Finish.** Run `$G press "Allow once" AXButton`, then `$G wait "Done." 20` and `$G shot thread-done`. `$G cli threads` shows `idle`.
- **New thread.** Run `$G press "New thread" AXButton`. `$G has "What should we build?"` succeeds again, and the previous thread stays in the sidebar.

## Gotchas

- The model button shows `Claude Opus 5.5`, but the engine plays the fake script. The model choice doesn't change the turn.
- The default workspace is the home directory. Never press `Commit & push` or `Open in` on a thread whose workspace is a real repository: they act on it.
- `$G type` sets the first `AXTextArea`. That's the composer on the chat page; a renamed thread title or other editor may be a different element, so check `$G tree AXText`.
- The tree shows the streamed text before it is painted. Screenshots right after `wait` can show a half-faded row, which is why `shot` settles 1.5s.
- `⌘⏎` needs the window in front (`$G key cmd+return`). While the user is in another app, use the `Send` button.
