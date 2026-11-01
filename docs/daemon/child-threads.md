# Child threads

Written 2026-10-25 for review, with PR #2; brought up to main on 2026-10-29, after archiving went (Settled is where a thread done with goes) and protocol 3 was taken by that change. This is what the branch builds, not a plan, except where a line says planned.

## Decision

**A child thread is an ordinary thread on the same engine with a `Parent`. The engine checks the line at create and posts each child's report into its parent's log through the outbox. The GUI does the cascades and the usage roll-up.**

- The tree is one field on the thread. Depth is never stored; it's walked, bounded.
- The engine refuses a child deeper than 4 below a thread of the user's, or wider than its parent's access.
- A child's run that completes, fails, or blocks on a request reports to its parent as a `child.reported` event. The report is an outbox effect, committed with the child's event, so a crash replays it rather than losing it.
- Stop and delete of a tree are one command per thread, sent by the GUI. There's no engine-side cascade. Settling a thread is the user's word on that thread alone: its children stay as they are, nested under it wherever it shows, but for one whose agent works, which also shows in Working (see Settle below).
- Each thread stores the tokens its own turns used. The GUI adds up a tree's.
- The protocol goes from 3 to 4, and the store schema from 4 to 5.

## The parent

- `api.ThreadCreate.Parent` (`internal/api/api.go`) names the parent; zero is a thread of the user's. It's carried into `api.ThreadCreated.Parent` (`internal/api/events.go`), folded into `engine.Thread.Parent` by `apply` (`internal/engine/apply.go`), and shown as `api.Thread.Parent` by `engine.View`.
- The store keeps it in `threads.parent`, a nullable BLOB (`internal/store/store.go`). Upgrade 4 adds it with `tokens` and `reported`, after main's upgrade 3 settles the archived threads; `schemaVersion` is 5. An older build refuses a version 5 database ("newer than this build").
- The parent never changes after create. There's no intent to move a thread.
- The client sends it with `Manager.CreateUnder` (`internal/client/commands.go`); `Create` is `CreateUnder` with no parent.

## Checks at create

`store.Submit` (`internal/store/commit.go`) runs them for a `ThreadCreate` with a parent, on a thread that doesn't exist yet, in the create's own transaction:

- `lineOf` loads the parent, then its parent, up to `api.MaxDepth+1` threads, so a corrupt cycle can't hang it. A deleted ancestor above the parent ends the line, as the GUI's does: its children stand on their own.
- `engine.CheckChild` (`internal/engine/decide.go`) refuses, as `*api.RejectedError`:
  - "the parent thread is gone": no parent on this engine, or a deleted one. A parent on another engine is "gone" too: children are same-engine only.
  - "a child thread can be at most 4 below a thread of yours": the line is longer than `MaxDepth`.
  - "a child thread's access can't be wider than its parent's": `Access` above the parent's. The parent's is no wider than its own parent's, so the check at one level holds for the line.
- A replayed create with the same command ID returns its stored receipt before any of this runs, as every command does.
- Access is fixed at create on main (there's no access-change intent), so narrowing after create isn't a case yet. The GUI clamps a child's draft to its parent's level and says so under the composer (`accessUnder`, `accessCapNote` in `children.go`).

## Reports

When:

| The child's run | Report | Source in `Observe` |
|---|---|---|
| ends Completed | `ReportDone` | `Ended` |
| ends Failed, a failed start included | `ReportFailed`, with the error | `Ended` |
| opens a request (approval or question) | `ReportBlocked`, with the request's body | `Surfaced` |
| ends Interrupted or Abandoned, or ends any way while stopping | none: the user stopped it | |
| becomes Uncertain | none | |

A thread with no parent never reports.

How:

1. `decision.report` adds an `engine.ReportToParent` effect (`internal/engine/effect.go`, kind `child.report`): parent, child, run, kind, the child's title, and the error or the request body. The store writes it to `effects` in the same transaction as the child's `turn.ended` or `request.opened`. Its row is the child's, so it runs in the child's lane, in order with the child's other effects.
2. The runtime's `perform` hands it to `Host.report` (`internal/runtime/report.go`). That reads what came of the run: for a block, a line from the request body ("Wants to run …", "Wants to edit main.go"); for a failure, the error; otherwise the last paragraph of the run's assistant deltas, which `store.RunText` reads with one query on the child's log (`internal/store/read.go`). It's one line, cut to 280 characters.
3. It commits `engine.Reported` on the **parent** with the effect's completion, in one transaction (`store.Observe` with `done`). `Observe` emits `api.ChildReported{Child, Run, Kind, Title, Outcome}` (`child.reported`) into the parent's log. `apply` sets `Thread.Reported` and bumps the revision.
4. In the same commit, a parent that was Snoozed or Settled goes back to Open.

Replay and failure:

- `child.report` is in `replaySafe`. A report left leased by a crash is released at boot and performed again. Because the parent's event and the effect's completion commit together, a crash leaves both or neither: the report lands once.
- Reading the child's log retries until the host closes, then the lease is released for the next host.
- A parent deleted since: `Observe` refuses the observation (`ErrRefused`), which still completes the effect. A parent that isn't on this engine at all: `ErrNotFound`, and `report` completes the effect itself; the create's check makes that unreachable today, so no test covers it. Either way the child's lane moves on; `TestAnOrphansReportGoesNowhere` covers the deleted parent.
- There's no batching. Each report is its own event. The GUI shows a run of reports one after another as one card, a child's later report replacing its earlier one in that card (`addReport` in `children.go`). A card is keyed by its first report's child and run and by its order among that run's cards (`entriesOf` in `data.go`): a run that asks twice can start two cards, the parent's text between them, and each keeps its own pointer, focus and scroll state. The prototype batched for 2 s in memory; that's dropped.

Reading it:

- `client.Transcript` folds `child.reported` into a `*ReportEntry` and records `Transcript.Reported` (`internal/client/transcript.go`).
- A report is never updated. The card shows a blocked report as Needs you only while the child's run still has a pending request and the report is the run's last blocked one (a run that asks twice reports twice); once it's answered or the run stopped, the same card says Was waiting, with what it waited on. No event is added for it.
- `Manager.Unread` treats a report after the read mark as news, as a turn's end is; `MarkRead` and `MarkShown` read up to it (`internal/client/saved.go`).
- A blocked report is told of by a desktop notification on the parent (`notifyReported` in `notify.go`), once per report, unless the user is watching the parent or the child: "Write the tests: Needs you". A request ends no turn, so nothing else would tell of it; a done or failed child's turn end already notifies on the child, so its report adds none. The parent's transcript says which child, while it's open and caught up; else it's the child with a request pending that changed last.

## Cascades

All in the GUI (`children.go`), one command per thread through the existing intents, `turn.interrupt` taking a `Drop` for Delete. The engine knows nothing of trees here.

- **Stop**: the composer's Stop and ⌘↩ stop only the thread's own agent, as on main. The Agents panel's Stop all sends `turn.interrupt` to every live thread under it that shows (`stopAll`) and says how many. A stopped child reports nothing (see the table).
- **Settle**: main's Settle, on one thread. Its children don't move: they show under it in Settled, and a report from one brings it back to the inbox. A child whose agent works (`categoryOf` is Working) while the thread at the top of its line is Snoozed or Settled also shows in Working on its own, with "under <its parent's title>" beneath its title (`lifted` in `inbox.go`). Once it stops, or that thread is back in the inbox, it shows only nested again. A working thread under a lifted one shows in that one's rows, not lifted a second time.
- **Delete**: the alert counts the threads under it, its children's children too ("and the 2 threads under it"), and the working ones. `deleteTree` walks the tree in post-order, each thread after its own children, so no child stands on its own on the way. A thread that isn't live and has no child still waiting is deleted at once; a live one is halted (`Manager.Halt`: `turn.interrupt` with `Drop`, which also cancels the messages waiting their turn, held ones too, "the thread is being deleted") and kept in `afterStop`, and a thread over one is kept waiting on its own children still kept, not on anything else in the tree. So an idle branch beside a working one goes at once. A plain interrupt would leave a queued follow-up to start its turn as the stop landed, and the engine refuses to delete a live thread, so the thread and every one over it would stay; `TestDeleteTreeWithAQueuedFollowUp` and the engine's `TestAHaltStartsNothingAfter` cover it.
- `settleStopped` runs after each change the manager posts. It deletes a kept thread once its run has ended, waiting for the threads under it to go first. A thread whose stop hasn't landed within a minute (`stopGrace`) is given up on, so it isn't deleted later, unseen, after the user went back to it, and so is every thread over it up the line, which would otherwise be left standing on its own. A cascade's threads share one timestamp.
- **Partial failure**: each command is independent; they're not one transaction. A refused or unsent command shows on its own thread as main's failed commands do, with Retry and Dismiss. The rest go through.
- `afterStop` is in memory. If the GUI quits before a stop lands, that thread is stopped but not deleted.
- A deleted parent leaves its children as threads of their own: `parentOf` finds no parent, and they show at the top level.

## Usage

- `provider.TurnEnded.Tokens` → `engine.Ended.Tokens` → `api.TurnEnded.Tokens`. `apply` adds it to `Thread.Tokens`, stored in `threads.tokens`, shown as `api.Thread.Tokens`.
- `app.usage` (`children.go`) adds up a thread's and every thread's under it at read time. The header shows "1.2k · 3.6k with children".
- No real provider reports tokens yet. The fake reports `Adapter.Tokens`, and `daemon.Defaults` sets it to 1800 so the dev engine shows something.

## Protocol 4

- `turn.interrupt` gains `drop` (omitted when false). An engine of this protocol cancels what waits its turn on it; Stop and Stop all never send it.
- `api.Protocol` is 4. The transport already refuses a peer of another protocol in both directions, so a protocol 3 GUI shows a protocol 4 engine as Incompatible, and the reverse. A protocol 3 hub refuses the uploads.
- Why refuse rather than stay at 3: a protocol 3 client would list children as threads of the user's and drop the reports (unknown event types are skipped), which looks like lost work.

## Not done

- An agent starting a child (bb's `create_chat` tool): planned. No provider has a tool for it yet; only the user's "New child thread" creates one.
- Children on another engine than their parent, and moving a thread to another parent.
- Engine-side cascades, or cascades that survive the GUI quitting.
- Narrowing a tree after create, as access can't change after create on main.
- Indexing parents in the hub replica.
- Real token counts from Claude or Codex.

## Open questions for Nelson

1. Cascades as engine commands (one intent, the engine fanning out, atomic per engine), or keep them in the GUI as now?
2. Should the outcome line be built in the engine, as now, or should the event carry less and the GUI read the child's transcript?
3. Is "a stopped run reports nothing" right once agents start children themselves, when an agent rather than the user stops one? Runs the engine stops itself (an answer it couldn't deliver) report nothing today either.
4. Should a child be held to its parent's workspace? Nothing checks it; the GUI only ever creates one there.
5. What should `Tokens` count across providers: input plus output, cached or not?
6. Settling a parent leaves its children as they are. Should Settle offer to settle the tree, as Delete takes it?
7. A working child under a Snoozed or Settled thread now also shows in Working, with the way to its parent, and nests again once it stops or the thread is back in the inbox; it still shows under its parent too. Should it show only in Working meanwhile, its parent's count leaving it out?
