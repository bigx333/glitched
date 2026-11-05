# Child threads

Written 2026-10-25 for review, with PR #2; brought up to main on 2026-10-29, after archiving went (Settled is where a thread done with goes) and protocol 3 was taken by that change; changed on 2026-11-03 so that only an agent starts a child and Settle takes the tree; changed on 2026-11-05 so that the engine settles and deletes a tree in one commit (open question 1). This is what the branch builds, not a plan, except where a line says planned.

## Decision

**A child thread is an ordinary thread on the same engine with a `Parent`. The engine checks the line at create, posts each child's report into its parent's log through the outbox, and settles or deletes a thread's tree with it in one commit. The GUI does Stop all and the usage roll-up.**

- The tree is one field on the thread. Depth is never stored; it's walked, bounded.
- The engine refuses a child deeper than 4 below a thread of the user's, or wider than its parent's access, and a child past the configured limits (see Limits).
- A child's run that completes, fails, or blocks on a request reports to its parent as a `child.reported` event. The report is an outbox effect, committed with the child's event, so a crash replays it rather than losing it.
- Only an agent starts a child; the user can't (decided 2026-11-03). A create names the parent's live run in `ThreadCreate.Run`, and the engine refuses one that doesn't. No provider has a tool for it yet: the fake provider spawns for tests (`fake.Adapter.Spawns`), and `glitched mcp` is the next PR of the spawn tool.
- Settle and delete of a tree are one command, on the thread the user acts on. The engine applies it to that thread and every thread under it in the same commit, a child started a moment before included: settling stops each run and cancels what waits, and deleting ends each run and closes its agent. A settled tree has nothing running, and a deleted one leaves nothing behind. Stop all stays one `turn.interrupt` per thread, sent by the GUI.
- Each thread stores the tokens its own turns used. The GUI adds up a tree's.
- The protocol goes from 3 to 4, and the store schema from 4 to 5. Protocol 5 and schema 6 add the parent's last report (see Reading it). Protocol 6 has settle and delete take the tree, with no schema change (see Protocol 6).

## The parent

- `api.ThreadCreate.Parent` (`internal/api/api.go`) names the parent; zero is a thread of the user's. It's carried into `api.ThreadCreated.Parent` (`internal/api/events.go`), folded into `engine.Thread.Parent` by `apply` (`internal/engine/apply.go`), and shown as `api.Thread.Parent` by `engine.View`.
- The store keeps it in `threads.parent`, a nullable BLOB (`internal/store/store.go`). Upgrade 4 adds it with `tokens` and `reported`, after main's upgrade 3 settles the archived threads. Upgrade 5 adds `report` (see Reading it); `schemaVersion` is 6. An older build refuses a newer database ("newer than this build").
- The parent never changes after create. There's no intent to move a thread.
- No client sends one: `Manager.Create` (`internal/client/commands.go`) makes a thread of the user's. An agent's run asks for a child with `Parent`, `Run` (the parent's live run) and `Text` (the child's first message).
- A create with `Text` queues it and admits the child's first turn in the create's own commit, under the command's ID, with the `turn.start` effect in the outbox. A crash leaves both or neither, and a retry of the same command returns the stored receipt, so one call makes one child.

## Checks at create

`store.Submit` (`internal/store/commit.go`) runs them for a `ThreadCreate` with a parent, on a thread that doesn't exist yet, in the create's own transaction:

- `lineOf` loads the parent, then its parent, up to `api.MaxDepth+1` threads, so a corrupt cycle can't hang it. A deleted ancestor above the parent ends the line, as the GUI's does: its children stand on their own. Since protocol 6 a delete takes the tree, so only a thread deleted before it leaves children like that.
- `engine.CheckChild` (`internal/engine/decide.go`) refuses, as `*api.RejectedError`:
  - "the parent thread is gone": no parent on this engine, or a deleted one. A parent on another engine is "gone" too: children are same-engine only.
  - "only the parent's live run can start a child thread, and not once it is stopping": `Run` is not the parent's live run, or that run is stopping or uncertain.
  - "a child thread can be at most N below a thread of yours": the line is longer than the limits' depth, which is at most `MaxDepth`.
  - "a child thread's access can't be wider than its parent's": `Access` above the parent's. The parent's is no wider than its own parent's, so the check at one level holds for the line.
  - "N child threads of this thread are at work, the most at once; start another once one of them stops", and "this thread has N child threads, the most it may; start another once one is deleted": the parent is at a limit. `kidsOf` counts its children that aren't deleted, and those of them whose agent works: a live run that isn't uncertain. An uncertain child, as every live one is after an engine restart under Claude, has no agent at work and waits on the user, so it takes no place at once.
- A replayed create with the same command ID returns its stored receipt before any of this runs, as every command does.

## Limits

`engine.Limits` caps what agents start: 5 children of one thread at work at once, 20 in all not counting deleted ones, and depth 4, the defaults for a field of zero. Depth is at most `api.MaxDepth`. They live in `daemon.Config.Children`, saved in `engine.json` with no store schema change. `glitched serve -max-working -max-children -max-depth` sets them for the next start, and the Agents card on Settings, This machine sets them too. The engine embedded in the GUI applies a change at once through `Daemon.SetLimits`; an engine another process runs applies it when it next starts.
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
2. The runtime's `perform` hands it to `Host.report` (`internal/runtime/report.go`). That reads what came of the run: for a block, a line from the request body ("Wants to run …", "Wants to edit main.go", or "Asks: " and the first question); for a failure, the error; otherwise the last paragraph of the run's assistant deltas, which `store.RunText` reads with one query on the child's log (`internal/store/read.go`). It's one line, cut to 280 characters.
3. It commits `engine.Reported` on the **parent** with the effect's completion, in one transaction (`store.Observe` with `done`). `Observe` emits `api.ChildReported{Child, Run, Kind, Title, Outcome}` (`child.reported`) into the parent's log. `apply` sets `Thread.Reported` and `Thread.LastReport` (the child, the kind, and the event's seq, which `Store.write` sets on `engine.Event.Seq` before it applies the event), and bumps the revision.
4. In the same commit, a parent that was Snoozed goes back to Open. A Settled parent stays Settled: settling halted its tree, so a report into it comes from a run that ended before its stop landed. The report is still logged and counts as unread, but it moves no thread and starts no turn. A Settled thread comes back only by the user's hand (Back to inbox, or a message sent to it).

Replay and failure:

- `child.report` is in `replaySafe`. A report left leased by a crash is released at boot and performed again. Because the parent's event and the effect's completion commit together, a crash leaves both or neither: the report lands once.
- Reading the child's log retries until the host closes, then the lease is released for the next host.
- A parent deleted since, as when a report is still in the child's lane when the tree's delete commits: `Observe` refuses the observation (`ErrRefused`), which still completes the effect. A parent that isn't on this engine at all: `ErrNotFound`, and `report` completes the effect itself; the create's check makes that unreachable today. Either way the child's lane moves on. `TestDelete` in `internal/engine` covers the refusal on a deleted thread; since the delete takes the child too (protocol 6), no runtime test drives a report into a deleted parent.
- There's no batching. Each report is its own event. The GUI shows a run of reports one after another as one card, a child's later report replacing its earlier one in that card (`addReport` in `children.go`). A card is keyed by its first report's child and run and by its order among that run's cards (`entriesOf` in `data.go`): a run that asks twice can start two cards, the parent's text between them, and each keeps its own pointer, focus and scroll state. The prototype batched for 2 s in memory; that's dropped.

Reading it:

- `client.Transcript` folds `child.reported` into a `*ReportEntry` and records `Transcript.Reported` (`internal/client/transcript.go`).
- A report is never updated. The card shows a blocked report as Needs you only while the child's run still has a pending request and the report is the run's last blocked one (a run that asks twice reports twice); once it's answered or the run stopped, the same card says Was waiting, with what it waited on. No event is added for it.
- `Manager.Unread` treats a report after the read mark as news, as a turn's end is; `MarkRead` and `MarkShown` read up to it (`internal/client/saved.go`).
- The parent's view carries its last report, `api.Thread.LastReport`: the child, the kind, and the event's cursor. It's committed with the event, so the shell stream sends it in commit order with every other view; the child's own view always comes first. The store keeps it in `threads.report`, JSON `{child, kind, seq}`, and stamps the cursor's log when it builds a view. Upgrade 5 fills it in from each thread's last `child.reported`, so a stored row still equals its log's fold.
- A child that waits on the user is told of by a desktop notification on the parent (`notifyAsked` in `notify.go`), unless the user is watching the parent or the child: "Write the tests: Needs you". A request ends no turn, so nothing else would tell of it; a done or failed child's turn end already notifies on the child, so its report adds none. The client tells of a child once for each of its pending requests the user has not heard of (`Manager.tell` in `internal/client/client.go`). Each child's `Thread.told` records, per pending request, whether it was told of; a request leaves it when it leaves the view, and it goes with the thread. Two things tell: the child's Blocked report, seen as its parent's `LastReport` on the shell stream, and a reconnect to the same log, which tells of each child that asks something untold, whatever the last report says. A child told of tells nothing more until it asks a new request; a Done or Failed report never tells. A shell snapshot of another log than the views held, the first connect or a restored database, tells of none: its requests count as heard, as their reports came before. A Blocked report seen after still tells of them, so a child that asked just before the app started is told of. A reconnect tells of them only for a parent whose last report moved while away, as that report may be the child's own with a sibling's after it (issue #14, fixed 2026-11-04).

## Cascades

Settle and Delete are engine-side (decided 2026-11-05, open question 1). The GUI sends one `inbox.set` or `thread.delete`, to the thread the user acts on. `store.Submit` (`internal/store/commit.go`) decides a command that `engine.TakesTree` names on every thread under its target too, in the same transaction: `treeOf` walks the threads that aren't deleted, as far down as `api.MaxDepth`, each before the thread above it, and each is decided with the same body through `engine.Decide`, so it means on each what it means on one thread. Each thread's effects go in its own lane. The commit's views go out together on the shell stream, so a client sees the whole tree change at once.

- **A child no GUI has seen**: an agent can start a child just before the user settles or deletes its tree. The child's create and the settle or delete commit one after the other under the store's lock, so the settle or delete finds the child in the table and takes it. A create after finds a settled or deleted parent and is refused (`CheckChild`). Nothing depends on what a GUI last saw, and nothing is left for a GUI to send after: a failed command that is retried takes the tree as it is then, and one dismissed leaves nothing behind (fixed 2026-11-05, PR 74 round 2, replacing the client's marks of round 1).
- **All or none**: the tree is one transaction, so a crash or a failed write part way leaves none of it, and a retry under the same ID takes all of it (`TestATreeCommitIsAllOrNothing`).

- **Stop**: the composer's Stop and ⌘↩ stop only the thread's own agent, as on main. The Agents panel's Stop all sends `turn.interrupt` to every live thread under it that shows (`stopAll`) and says how many. A stopped child reports nothing (see the table).
- **Settle** (open question 6, decided 2026-11-03): settling a thread settles it and every thread under it, its children's children too, in the settle's commit (`settleTree` sends the one `inbox.set`). The engine makes Settled mean done with (fixed 2026-11-04): in the settle's own commit it cancels the queued and held messages ("the thread was settled or deleted") and stops the live run, and it admits no turn on a settled thread. A message sent to one after is new work: in the message's own commit the engine moves the thread back to the inbox and runs it, whichever client sent it, in any mode (fixed 2026-11-04, round 2). So a follow-up another client queued just before the settle never starts, one sent after does, and a stop already under way is left to land. The engine also refuses a child under a settled parent. A thread whose agent works offers no Settle, as on main; one waiting on an approval, or left uncertain, does, and settling it stops its own run too. Back to inbox brings back that thread alone, not its tree.
- **Snooze** is unchanged: it's the user's word on that thread alone, and a report from a child brings a snoozed parent back to the inbox. A child whose agent works (`categoryOf` is Working) while the thread at the top of its line is Snoozed also shows in Working on its own, with "under <its parent's title>" beneath its title (`lifted` in `inbox.go`). Once it stops, or that thread is back in the inbox, it shows only nested again. A working thread under a lifted one shows in that one's rows, not lifted a second time. Nothing is lifted out of Settled: a working child under a settled thread can only be one another GUI left, or one the user sent a message to, and it shows nested there.
- **Delete**: the alert counts the threads under it, its children's children too ("and the 2 threads under it"), and the working ones. `deleteTree` sends one `thread.delete`, and the engine deletes every thread under it in the same commit. While it's on its way, the GUI shows none of the tree (`deleting` in `data.go` looks up the line), and a failed delete shows it all again. The engine ends each deleted thread's live run in the delete's commit, as `turn.ended` with `abandoned`, with an `agent.close` effect (`engine.CloseAgent`) in the outbox (fixed 2026-11-04). The host's pump closes the agent it holds; the effect then finds the run's agent through `Reconcile` and closes what it finds, so a crash or a shutdown before the pump's close leaves the close to the next host. It is replay-safe: closing an agent that is gone does nothing. So the delete doesn't depend on what this GUI last saw of a thread's run: a child another client just set to work goes too, a follow-up waiting its turn on a thread when its delete lands never starts, and one sent after finds no thread. Nothing waits on a stop, and there's no `afterStop`. The thread menu still offers no Delete on a thread whose own agent works, as on main.
- **Failure**: a settle or delete that fails to reach the engine shows on the thread the user acted on, as main's failed commands do, with Retry and Dismiss. Its tree is untouched until a retry lands.
- A thread whose parent was deleted before protocol 6 stands on its own: `parentOf` finds no parent, it shows at the top level, and `treeOf` stops at a deleted thread, as `lineOf` does.

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

## Protocol 6

- A settle or a delete takes the thread's tree in its commit. `api.Protocol` is 6: a protocol 5 GUI sends a command per thread, which an engine of 6 has already taken, and a protocol 5 engine would leave a child of a settled or deleted thread at work behind a protocol 6 GUI's one command. So each refuses the other. No store schema change.
- Spawn PR 3 adds `MessageQueued.Report` within protocol 6. No release goes out in between.

## Not done

- An agent starting a child (bb's `create_chat` tool): the follow-up PR, likely the first plugin. Until then no path in the app creates a child. The user's "New child thread", the Agents panel's +, and the child's draft were removed on 2026-11-03: a child is always an agent's.
- Children on another engine than their parent, and moving a thread to another parent.
- An engine-side Stop all.
- Narrowing a tree after create, as access can't change after create on main.
- Indexing parents in the hub replica.
- Real token counts from Claude or Codex.

## Open questions for Nelson

1. Answered 2026-11-05: the engine does Settle and Delete of a tree, in the command's own commit, on the existing intents (see Cascades and Protocol 6). Stop all stays in the GUI.
2. Should the outcome line be built in the engine, as now, or should the event carry less and the GUI read the child's transcript?
3. Is "a stopped run reports nothing" right once agents start children themselves, when an agent rather than the user stops one? Runs the engine stops itself (an answer it couldn't deliver) report nothing today either.
4. Should a child be held to its parent's workspace? Nothing checks it; the spawn tool will decide where one starts.
5. What should `Tokens` count across providers: input plus output, cached or not?
6. Decided 2026-11-03: Settle cascades. Settling a thread stops and settles its whole tree, and a late report doesn't unsettle it (see Cascades and Reports).
7. A working child under a Snoozed thread now also shows in Working, with the way to its parent, and nests again once it stops or the thread is back in the inbox; it still shows under its parent too. Should it show only in Working meanwhile, its parent's count leaving it out?
