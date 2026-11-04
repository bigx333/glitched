# Child threads

Written 2026-10-25 for review, with PR #2; brought up to main on 2026-10-29, after archiving went (Settled is where a thread done with goes) and protocol 3 was taken by that change; changed on 2026-11-03 so that only an agent starts a child and Settle takes the tree. This is what the branch builds, not a plan, except where a line says planned.

## Decision

**A child thread is an ordinary thread on the same engine with a `Parent`. The engine checks the line at create and posts each child's report into its parent's log through the outbox. The GUI does the cascades and the usage roll-up.**

- The tree is one field on the thread. Depth is never stored; it's walked, bounded.
- The engine refuses a child deeper than 4 below a thread of the user's, or wider than its parent's access.
- A child's run that completes, fails, or blocks on a request reports to its parent as a `child.reported` event. The report is an outbox effect, committed with the child's event, so a crash replays it rather than losing it.
- Only an agent starts a child; the user can't (decided 2026-11-03). No provider has a tool for it yet, so today only tests create one, through the engine or `Manager.CreateUnder`. The spawn tool is a follow-up PR.
- Stop, settle and delete of a tree are one command per thread, sent by the GUI. There's no engine-side cascade. Each command is atomic on its thread: settling stops the thread's run and cancels what waits, and deleting ends its run. A settled tree has nothing running, and a deleted one leaves nothing behind.
- Each thread stores the tokens its own turns used. The GUI adds up a tree's.
- The protocol goes from 3 to 4, and the store schema from 4 to 5. Protocol 5 and schema 6 add the parent's last report (see Reading it).

## The parent

- `api.ThreadCreate.Parent` (`internal/api/api.go`) names the parent; zero is a thread of the user's. It's carried into `api.ThreadCreated.Parent` (`internal/api/events.go`), folded into `engine.Thread.Parent` by `apply` (`internal/engine/apply.go`), and shown as `api.Thread.Parent` by `engine.View`.
- The store keeps it in `threads.parent`, a nullable BLOB (`internal/store/store.go`). Upgrade 4 adds it with `tokens` and `reported`, after main's upgrade 3 settles the archived threads. Upgrade 5 adds `report` (see Reading it); `schemaVersion` is 6. An older build refuses a newer database ("newer than this build").
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
- Access is fixed at create on main (there's no access-change intent), so narrowing after create isn't a case yet.

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
3. It commits `engine.Reported` on the **parent** with the effect's completion, in one transaction (`store.Observe` with `done`). `Observe` emits `api.ChildReported{Child, Run, Kind, Title, Outcome}` (`child.reported`) into the parent's log. `apply` sets `Thread.Reported` and `Thread.LastReport` (the child, the kind, and the event's seq, which `Store.write` sets on `engine.Event.Seq` before it applies the event), and bumps the revision.
4. In the same commit, a parent that was Snoozed goes back to Open. A Settled parent stays Settled: settling halted its tree, so a report into it comes from a run that ended before its stop landed. The report is still logged and counts as unread, but it moves no thread and starts no turn. A Settled thread comes back only by the user's hand (Back to inbox, or a message sent to it).

Replay and failure:

- `child.report` is in `replaySafe`. A report left leased by a crash is released at boot and performed again. Because the parent's event and the effect's completion commit together, a crash leaves both or neither: the report lands once.
- Reading the child's log retries until the host closes, then the lease is released for the next host.
- A parent deleted since: `Observe` refuses the observation (`ErrRefused`), which still completes the effect. A parent that isn't on this engine at all: `ErrNotFound`, and `report` completes the effect itself; the create's check makes that unreachable today, so no test covers it. Either way the child's lane moves on; `TestAnOrphansReportGoesNowhere` covers the deleted parent.
- There's no batching. Each report is its own event. The GUI shows a run of reports one after another as one card, a child's later report replacing its earlier one in that card (`addReport` in `children.go`). A card is keyed by its first report's child and run and by its order among that run's cards (`entriesOf` in `data.go`): a run that asks twice can start two cards, the parent's text between them, and each keeps its own pointer, focus and scroll state. The prototype batched for 2 s in memory; that's dropped.

Reading it:

- `client.Transcript` folds `child.reported` into a `*ReportEntry` and records `Transcript.Reported` (`internal/client/transcript.go`).
- A report is never updated. The card shows a blocked report as Needs you only while the child's run still has a pending request and the report is the run's last blocked one (a run that asks twice reports twice); once it's answered or the run stopped, the same card says Was waiting, with what it waited on. No event is added for it.
- `Manager.Unread` treats a report after the read mark as news, as a turn's end is; `MarkRead` and `MarkShown` read up to it (`internal/client/saved.go`).
- The parent's view carries its last report, `api.Thread.LastReport`: the child, the kind, and the event's cursor. It's committed with the event, so the shell stream sends it in commit order with every other view; the child's own view always comes first. The store keeps it in `threads.report`, JSON `{child, kind, seq}`, and stamps the cursor's log when it builds a view. Upgrade 5 fills it in from each thread's last `child.reported`, so a stored row still equals its log's fold.
- A blocked report is told of by a desktop notification on the parent (`notifyReported` in `notify.go`), once per report, unless the user is watching the parent or the child: "Write the tests: Needs you". A request ends no turn, so nothing else would tell of it; a done or failed child's turn end already notifies on the child, so its report adds none. `Manager.view` calls `Config.Reported` when a view's `LastReport` differs from the view it held (`internal/client/client.go`). A shell snapshot of another log than the views held, the first connect or a restored database, tells of none: what it carries came before. A reconnect to the same log tells of the last report made while it was away, and only that one (issue #14, fixed 2026-11-04).

## Cascades

All in the GUI (`children.go`), one command per thread through the existing intents, `turn.interrupt` taking a `Drop` for Delete. The engine knows nothing of trees here.

- **Stop**: the composer's Stop and ⌘↩ stop only the thread's own agent, as on main. The Agents panel's Stop all sends `turn.interrupt` to every live thread under it that shows (`stopAll`) and says how many. A stopped child reports nothing (see the table).
- **Settle** (open question 6, decided 2026-11-03): settling a thread settles it and every thread under it, its children's children too (`settleTree`), one `inbox.set` each. The engine makes Settled mean done with (fixed 2026-11-04): in the settle's own commit it cancels the queued and held messages ("the thread was settled or deleted") and stops the live run, and it admits no turn on a settled thread. A message sent to one after is new work: in the message's own commit the engine moves the thread back to the inbox and runs it, whichever client sent it, in any mode (fixed 2026-11-04, round 2). So a follow-up another client queued just before the settle never starts, one sent after does, and a stop already under way is left to land. The engine also refuses a child under a settled parent. A thread whose agent works offers no Settle, as on main; one waiting on an approval, or left uncertain, does, and settling it stops its own run too. Back to inbox brings back that thread alone, not its tree.
- **Snooze** is unchanged: it's the user's word on that thread alone, and a report from a child brings a snoozed parent back to the inbox. A child whose agent works (`categoryOf` is Working) while the thread at the top of its line is Snoozed also shows in Working on its own, with "under <its parent's title>" beneath its title (`lifted` in `inbox.go`). Once it stops, or that thread is back in the inbox, it shows only nested again. A working thread under a lifted one shows in that one's rows, not lifted a second time. Nothing is lifted out of Settled: a working child under a settled thread can only be one another GUI left, or one the user sent a message to, and it shows nested there.
- **Delete**: the alert counts the threads under it, its children's children too ("and the 2 threads under it"), and the working ones. `deleteTree` sends `thread.delete` to each thread in post-order, each after its own children, so no child stands on its own on the way. The engine ends a deleted thread's live run in the delete's commit, as `turn.ended` with `abandoned`, with an `agent.close` effect (`engine.CloseAgent`) in the outbox (fixed 2026-11-04). The host's pump closes the agent it holds; the effect then finds the run's agent through `Reconcile` and closes what it finds, so a crash or a shutdown before the pump's close leaves the close to the next host. It is replay-safe: closing an agent that is gone does nothing. So the delete doesn't depend on what this GUI last saw of a thread: a child another client just started goes too, a follow-up waiting its turn on a thread when its delete lands never starts, and one sent after finds no thread. Nothing waits on a stop, and there's no `afterStop`. The thread menu still offers no Delete on a thread whose own agent works, as on main.
- **Partial failure**: each command is independent; they're not one transaction. A command that fails to reach the engine shows on its own thread as main's failed commands do, with Retry and Dismiss. The rest go through.
- A deleted parent leaves its children as threads of their own: `parentOf` finds no parent, and they show at the top level.

## Usage

- `provider.TurnEnded.Tokens` → `engine.Ended.Tokens` → `api.TurnEnded.Tokens`. `apply` adds it to `Thread.Tokens`, stored in `threads.tokens`, shown as `api.Thread.Tokens`.
- `app.usage` (`children.go`) adds up a thread's and every thread's under it at read time. The header shows "1.2k · 3.6k with children".
- No real provider reports tokens yet. The fake reports `Adapter.Tokens`, and `daemon.Defaults` sets it to 1800 so the dev engine shows something.

## Protocol 4

- `turn.interrupt` gains `drop` (omitted when false). An engine of this protocol cancels what waits its turn on it and stops the run; on a run already stopping it adds no second interrupt, so the run ends as its agent does rather than going uncertain. No GUI path sends it since settle and delete stop runs themselves (2026-11-04); it stays in the protocol. `drop` is new inside protocol 4 and an engine built from this branch before f62ba50 ignores it (protocol 4 is unreleased, so no bump).
- `api.Protocol` is 4. The transport already refuses a peer of another protocol in both directions, so a protocol 3 GUI shows a protocol 4 engine as Incompatible, and the reverse. A protocol 3 hub refuses the uploads.
- Why refuse rather than stay at 3: a protocol 3 client would list children as threads of the user's and drop the reports (unknown event types are skipped), which looks like lost work.

## Protocol 5

- `api.Thread.LastReport` is new. `api.Protocol` is 5: a protocol 4 GUI would drop the field and tell of reports from `Reported` and the children's requests, which is issue #14, so it is refused.

## Not done

- An agent starting a child (bb's `create_chat` tool): the follow-up PR, likely the first plugin. Until then no path in the app creates a child. The user's "New child thread", the Agents panel's +, and the child's draft were removed on 2026-11-03: a child is always an agent's.
- Children on another engine than their parent, and moving a thread to another parent.
- Engine-side cascades, or cascades that survive the GUI quitting.
- Narrowing a tree after create, as access can't change after create on main.
- Indexing parents in the hub replica.
- Real token counts from Claude or Codex.

## Open questions for Nelson

1. Cascades as engine commands (one intent, the engine fanning out, atomic per engine), or keep them in the GUI as now?
2. Should the outcome line be built in the engine, as now, or should the event carry less and the GUI read the child's transcript?
3. Is "a stopped run reports nothing" right once agents start children themselves, when an agent rather than the user stops one? Runs the engine stops itself (an answer it couldn't deliver) report nothing today either.
4. Should a child be held to its parent's workspace? Nothing checks it; the spawn tool will decide where one starts.
5. What should `Tokens` count across providers: input plus output, cached or not?
6. Decided 2026-11-03: Settle cascades. Settling a thread stops and settles its whole tree, and a late report doesn't unsettle it (see Cascades and Reports).
7. A working child under a Snoozed thread now also shows in Working, with the way to its parent, and nests again once it stops or the thread is back in the inbox; it still shows under its parent too. Should it show only in Working meanwhile, its parent's count leaving it out?
