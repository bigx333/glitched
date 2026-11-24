# Design 3. File checkpoints, restore, and conversation rewind

Goal. A person who lets an agent run in Glitched can undo what one turn did to the files, and later rewind the conversation to that turn, without losing anyone else's work. Glitched keeps what it is ahead on, which is the table-checked decide/apply/effect engine, a log whose fold equals the stored projection, command-id conflict checks, and one live run per thread.

Pins. T3 Code at 9f11faf7a6 (`t3:`, with `OV2/` for `apps/server/src/orchestration-v2/`). Zeron at 69901bfb9 (`zeron:`). Glitched at a646798 (`gl:`). Each claim is labelled src (read in code), doc (help text, schema or comment, not run) or inferred.

## 1. What T3 Code and Zeron do

### T3 Code

Storage. Checkpoints are refs in the checkout's own repository under `refs/t3/orchestration-v2/checkpoints/<hash of scope>/ordinal/<run ordinal>` (src, OV2/CheckpointService.ts:35, :283-295). The capture stages into a temporary index under the git common dir (src, t3:apps/server/src/vcs/GitVcsDriver.ts:1233). It seeds that index from a copy of the user's index to reuse its stat cache (src, GitVcsDriver.ts:1314). New objects go to a private object directory that reads the shared one as an alternate (src, GitVcsDriver.ts:1241, :1264-1268). It runs `git add -A` (`-u` when the workspace itself is ignored) (src, GitVcsDriver.ts:1546), then `write-tree`, then `commit-tree` with HEAD as first parent (src, GitVcsDriver.ts:1768-1781). It publishes the private objects into the shared store and runs `update-ref` (src, GitVcsDriver.ts:1794, :1884). Submodules keep their own ref (src, GitVcsDriver.ts:1898).

Timing. A run settles only after its checkpoint. The capture runs while the run is "waiting", and a stopped run is captured too (src, OV2/CheckpointCaptureService.ts:79-97). A baseline before the provider turn has a 5 s budget, and a workspace too slow for it starts without one (src, OV2/CheckpointService.ts:37-39, :412-414). After two capture timeouts in a folder the baseline is skipped (src, OV2/CheckpointService.ts:495-499). A capture ends `ready`, `missing` or `error` (src, OV2/CheckpointService.ts:664-706).

Restore. `git restore --source <commit> --worktree --staged`, so it rewrites the user's index (src, GitVcsDriver.ts:2068). Then `git clean -fd` up to four passes, because removing a new `.gitignore` exposes files it hid (src, GitVcsDriver.ts:2156-2165).

Safety. File restore needs an isolated worktree. A thread with no worktree path is shared. So is any scope whose folder differs from the worktree, and any other live thread, session or project root whose path contains or sits inside the restored folders. An idle side chat with no tool items and a completed app-owned child are exempt (src, OV2/CheckpointRestoreSafety.ts:19-20, :44-57, :88-134, :178-212). Rollback refuses a shared workspace when files are asked for (src, OV2/CheckpointRollbackService.ts:337). It holds a workspace reservation while it restores (src, OV2/CheckpointRollbackService.ts:554).

Rollback order. The provider conversation first, then files, then stale refs (src, OV2/CheckpointRollbackService.ts:426-436). Rolled-back runs stay in history marked `rolled_back` (src, OV2/CheckpointRollbackService.ts:518, :530). Pruning happens only at rollback and in storage cleanup, which deletes the refs with `update-ref --no-deref --stdin` (src, t3:apps/server/src/storageCleanup.ts:2030-2053).

Codex. T3 counts terminal turns after the target and pages `thread/turns/list` to find the boundary. It then calls `thread/revert` with `beforeTurnId` (src, t3:apps/server/src/provider/CodexThreadRevert.ts:6-40). It refuses legacy-history threads (src, OV2/Adapters/CodexAdapterV2.ts:9249-9257).

Claude. T3 refuses while a turn is active and closes the live query (src, OV2/Adapters/ClaudeAdapterV2.ts:15267-15276). It records the target turn's assistant message cursor as the conversation head (src, ClaudeAdapterV2.ts:15280, :15306-15320). The next turn opens the SDK query with `resumeSessionAt` (src, ClaudeAdapterV2.ts:13420, :13596). Only a compaction turn may be skipped to find a cursor (src, ClaudeAdapterV2.ts:3126-3137).

ACP. T3 starts a replacement session with no conversation head (src, t3:packages/provider-acp/src/server/adapter.ts:11068-11105).

### Zeron

Zeron has two stores. The turn diff snapshot borrows the checkout's objects as an alternate. Its own comment says pruning the checkout while a turn is open can make that diff unreadable (src, zeron:crates/engine/src/diff_sync/turn_snapshot.rs:15-25). The durable checkpoint store does not borrow. "Neither capture nor restore writes the checkout's index, HEAD, refs, config, or object database" (src, zeron:crates/engine/src/doc_host/history_files.rs:1-3). A grep for `GIT_INDEX_FILE`, `GIT_OBJECT_DIRECTORY` and alternates found only these two files and a font file (src).

Durable capture. A bare repository per checkout identity under the data dir (src, zeron:crates/engine/src/doc_host/history.rs:174-183). Each command sets `GIT_DIR` to it and `GIT_WORK_TREE` to the checkout (src, history_files.rs:40). Attributes turn off every filter (src, history_files.rs:112). The file list comes from the checkout's own git, `ls-files --cached --others --exclude-standard`, so every ignore source applies (src, history_files.rs:121-124). Submodules, special files and symlink ancestors refuse the capture (src, history_files.rs:130-165). Over 256 MiB or 50,000 files refuses it too (src, history_files.rs:169). Then `read-tree --empty`, `update-index --add --remove --stdin`, `write-tree`, and `update-ref refs/zeron/<id>` pointing at the tree (src, history_files.rs:191-212). Each git call has a 120 s timeout (src, history_files.rs:57-61). A failed automatic capture keeps the conversation checkpoint without files (src, history.rs:183-189).

Restore. Zeron captures a recovery checkpoint first, titled "Before restoring checkpoint" (src, history.rs:197, :488-499). It writes a durable receipt before each step (src, history.rs:446-546). The restore diffs the current and target manifests. A preflight refuses when a file outside the checkpoint blocks a path (src, history_files.rs:248-290). It materializes the target with `checkout-index` into a staging folder, removes the known files and copies the target in (src, history_files.rs:292-372). A failed apply reapplies the recovery tree (src, history_files.rs:376-398). An interrupted restore recovers from its receipt (src, history.rs:534-545).

Safety. Zeron allows restore in any checkout. It refuses while the chat or any other chat in the same checkout is working, has subagents, delegated tasks, pending messages or a queue (src, history.rs:257-363). It holds a history mutation guard during rollback (src, history.rs:435).

Conversation. Only Codex rewinds natively (src, history.rs:150-170). Zeron forks with `thread/fork`, `lastTurnId` inclusive, in a short-lived app-server that declines every server request. It verifies with `thread/turns/list` that the new thread ends at that turn (src, zeron:crates/harness/src/codex/history.rs:41-75). Everything else is "portable". The session is cleared and the next prompt carries the visible history (src, history.rs:231, zeron:crates/engine/src/sessions.rs:2080). Zeron does not use Claude's `--resume-session-at`. A grep of its Rust for that flag, `resumeSessionAt`, `thread/rollback` and `thread/revert` found nothing (src). The UI labels each checkpoint "Native resume" or "Portable" (src, zeron:crates/ui/src/shell/session_lanes/history.rs:894-897).

### What the providers can do today

Codex. The installed codex-cli 0.171.0 has no `thread/rollback`. Its generated schema lists `thread/revert` and `thread/fork` among client requests (doc, `codex app-server generate-json-schema` run with a scratch CODEX_HOME). `thread/revert` "replace[s] a paginated thread's durable history with the prefix before one turn" and "does not revert local file changes". `thread/fork` takes `lastTurnId`, "inclusive", and that turn "cannot be in progress" (doc, same schema).

Claude. The installed claude 2.1.331 is a native binary with no cli.js. `--help` lists `--fork-session` only. The binary's strings hold hidden flags (doc). `--resume-session-at <message id>` and `--resume-drops-turn <message id>` are both "ignored outside print mode" and require `--resume`. The second declares "the prompt uuid of the turn the truncating resume intends to discard", and the resume "is refused if the discarded range contains anything not attributable to that turn". `--rewind-files <user-message-id>` restores files "and exit[s]", is standalone, and needs Claude's own file checkpointing. No prompt was run.

ACP and agy. acp-go-sdk v0.20.0 has `session/load`, `session/resume` and an unstable `session/fork`. The fork request has no turn boundary (doc, acp-go-sdk types_gen.go:14060-14112). There is no truncate method (src, constants_gen.go:23-31).

### Which fits Glitched

Glitched follows Zeron for storage and restore mechanics, and T3 for where restore is allowed. A native app with paired engines keeps each folder on the engine that owns it. A remote engine is reached over the tailnet and has its own store (src, gl:internal/client/saved.go:17-18). So checkpoints belong in that engine's state folder, not in the person's repository. A private store adds no refs to their `git log --all` and no objects to their gc. It also never touches their index, which T3's `git restore --staged` does. Zeron's no-alternates rule is the gc-safe one. T3's isolation rule is the one Glitched can enforce, because its worktrees carry ownership markers (src, gl:internal/runtime/worktree.go:197-221).

## 2. The Glitched design

### What exists

The engine is pure. Decide returns events and effects, and Apply folds events (src, gl:internal/engine/state.go:1-3). Transitions are checked only in the run, queue and request tables (src, gl:internal/engine/table.go:9-39). A thread's effects run one at a time in commit order, and threads never wait on each other (src, gl:internal/runtime/host.go:338-361). Observe runs settle after each observation, so an effect appended while observing a run's end sits before the next run's StartTurn (src, gl:internal/engine/decide.go:124-128, :378-392). An effect performs and then commits its observation with its completion (src, host.go:378-386). At boot a leased effect that is not replay-safe stops the host (src, host.go:298-299). A commit hook can mark a thread busy before the next lease, as the agent-closing mark does (src, host.go:851-860; gl:internal/store/effects.go:23-26). The projection lives in columns, `threads.session` among them (src, gl:internal/store/store.go:153-180). Unknown event types decode to nil and readers skip them (src, gl:internal/api/events.go:324-336). A Claude thread's session id is its thread id as a UUID, run with `-p` stream-json (src, gl:internal/provider/claude/claude.go:153, :282, :359-366). Codex resumes `spec.Session` with `thread/resume` (src, gl:internal/provider/codex/proc.go:350-370). The Codex agent already keeps the turn id (src, gl:internal/provider/codex/agent.go:197). A child refuses its own worktree today (src, worktree.go:107-109).

### Capture

Who. Every non-silent run of a top-level thread that runs in its own Glitched worktree (Branch set). Children share the parent's folder, so the parent's next capture includes their edits (inferred).

When. Decide appends `CaptureCheckpoint{Run, Phase: run}` wherever such a run ends. That is Observe of Ended (decide.go:618-634) and the user's end of an uncertain run (decide.go:434-436). A delete captures nothing. The effect is appended before settle admits the next message, so the next turn starts on the captured tree (inferred from decide.go:124-128). The thread's first run also gets `CaptureCheckpoint{Phase: base}`, appended in admit before StartTurn (decide.go:350-352). The recovery capture every restore takes first covers edits the person makes between turns.

Where. `<engine state dir>/checkpoints/<sha256 of the git common dir, 16 hex>.git`, one bare repository per repository, shared by its worktrees so blobs dedupe. Zeron keys by checkout identity instead. Refs are `refs/glitched/<thread>/base`, `refs/glitched/<thread>/run/<run>`, `refs/glitched/<thread>/recovery/<revert command>` and `refs/glitched/<thread>/applying/<revert command>`. Each thread writes only its own refs.

How, in a new `internal/runtime/checkpoint.go`:

1. If the ref exists, report it and stop. This makes the effect replay-safe.
2. `rev-parse` gives the top level, the common dir and HEAD. A folder with no git reports `skipped: not a repository`.
3. `git init --bare` once, with Zeron's `info/attributes` line.
4. List with the checkout's git, `ls-files --cached --others --exclude-standard -z`. Tracked files are kept even when ignored. Ignored untracked files are never captured. Submodules, special files and symlink ancestors give `skipped` with the reason. Over 256 MiB or 50,000 files gives `skipped: too large`.
5. With `GIT_DIR` set to the store, `GIT_WORK_TREE` to the top level and `GIT_INDEX_FILE` to `capture-<run>.index` in the store, run `read-tree --empty`, `update-index --add --remove -z --stdin` and `write-tree`.
6. `commit-tree` with no parent. The message names the thread, the run and HEAD. The author is a fixed Glitched identity set in the process environment, as T3 does (GitVcsDriver.ts:1262-1274).
7. One `update-ref --stdin` transaction creates the ref and deletes this thread's run refs beyond the newest 50 and its recovery refs beyond the newest 10.
8. `diff-tree -r --name-only` against the previous run ref gives the changed-file count.
9. Every 20th capture runs `git gc --auto --quiet` in the store, with Git's default prune expiry of two weeks.

Nothing writes the checkout's index, HEAD, refs or objects. Zeron has a test for exactly that property (src, turn_snapshot.rs:746).

Budgets. The base phase gets 5 s, as in T3, because the turn waits on it. The run phase gets 120 s per git call, as in Zeron, because the next queued turn waits behind it. The size budget is what keeps a healthy repository fast. Two timeouts in a row in one repository skip later captures there until the engine restarts, as T3 does for baselines.

Deleting a thread. Decide's delete appends `DropCheckpoints{Thread}`. The effect deletes `refs/glitched/<thread>/*` in one transaction, and gc prunes the objects later. It is replay-safe, because deleting absent refs does nothing. CloseAgent shows that a deleted thread's effects still run (src, gl:internal/engine/effect.go:85-86).

Gc safety. Refs keep every checkpoint reachable in the private store. The store never borrows the checkout's objects, so the person's own gc cannot break a checkpoint. A two-week prune expiry is far longer than the gap between `write-tree` and `update-ref` (inferred).

### Revert, files only first

One intent, `api.ThreadRevert{To api.Checkpoint, Files, Conversation bool}`. `api.Checkpoint` is `{Kind: Base|Run|Recovery, ID api.ID}`. "Undo this turn" is a revert to the end of the run before it. "Undo the restore" is a revert to its recovery checkpoint. T3 has one rollback with a `restoreFiles` flag (src, OV2/CheckpointRollbackService.ts:105, :162), and Glitched does the same.

Decide refuses in these cases:
- a child thread ("a child's files are its parent's");
- a live run in any state ("stop the thread first");
- Files on a thread without a worktree of its own (Branch empty);
- a target run that is unknown, not finished, or silent;
- Conversation with a Recovery target.

Otherwise it emits `thread.reverting` and one `RevertThread` effect.

The host checks three things before it submits, as it does for worktree create and delete (src, worktree.go:92-195, :229-293). The worktree must still be the thread's by `ownedBy`. No thread under it may have a live run. No other non-deleted thread may have a workspace inside the folder or around it.

A commit hook marks the folder as restoring when `thread.reverting` commits. A StartTurn whose workspace is inside a restoring folder hands its lease back, as a closing host does (host.go:378-386). Boot sets the mark again from any pending or leased `RevertThread` before work starts. This covers a child or another thread that becomes live between the commit and the effect. The thread's own later StartTurn already waits, because it sits behind the revert in the lane.

As built in PR 2:

- The intent is `thread.revert {to: {phase: base|run|recovery, id}, files: true}`, protocol 12. `files: false` is refused until the conversation rewind lands. A base checkpoint is named by the thread's first run.
- `thread.reverting` and `thread.reverted` move the thread's revision, so the runtime's commit hook sees the thread whose folder it marks. They change nothing else in the thread's view.
- The restoring mark counts the reverts of a worktree that are not done. Each revert's effect ends its count, whether it ran or was skipped for a deleted thread. A StartTurn waits when its workspace is in the folder or around it. A start the mark holds back writes nothing. So when the effect checks again, that start doesn't count as a thread at work, in the tree or outside it.
- A replay treats a path that already holds the target's blob, of the same kind and executable bit, as one it wrote itself. A delete counts as done only once it succeeds.

What a person sees differs from the plan above:

- The timeline row reads "Restored 12 files" with Undo, without a turn number. A failed revert's row offers Undo too.
- The confirmation doesn't list the paths that change.
- A turn with no work log gets no chip.
- After a revert, a turn's count compares with the previous run, not with the files the revert put back.
- Pruning keeps 50 run checkpoints and 10 recovery ones. When an Undo or a queued revert needs one that was pruned, it fails with "no checkpoint of the files was saved there".
- Empty folders aren't saved, so Undo doesn't bring one back.
- Settings › This machine shows how much the store holds of its 2 GiB cap.

The effect, all steps replay-safe:

1. Capture `recovery/<command>` unless it exists. A replay never retakes it, because a half-applied tree must not become the recovery.
2. Check `ownedBy` and the branch again.
3. Diff the recovery and target manifests. Preflight as Zeron does, and refuse when an ignored or unknown file blocks a target path. Once the preflight passes and before any file moves, write `applying/<command>`: a commit whose parent is the recovery checkpoint and whose one file, `plan`, lists in git's raw diff format each path the revert is to write, with its mode and blob before and after. A path already as the target has it, such as an ignored file with the target's bytes, is left out. A replay keeps the plan the first attempt wrote. The end of a revert deletes the thread's other `applying/` refs.
4. Materialize the target with `checkout-index --prefix` into `restore-<command>/` in the store. Delete and copy only the paths whose blob or mode differ. Zeron rewrites every known file; touching only the differing ones keeps editors and file watchers quiet.
5. On a failed apply, undo the plan's paths, as Zeron reapplies the recovery tree.

The rule for putting files back, which the forward apply, a failed apply's undo and a refused replay's undo all follow: recovery never removes or replaces a path unless that path holds exactly what this revert wrote there, or, going forward, what it found there before it wrote. It never removes anything recursively. Where it can't keep the rule, it leaves the path as it is and fails the revert, naming the path ("<path> changed since the revert began, so it was left as it is"). It never guesses. One function, `put`, makes every change: it checks the path with Lstat and a blob hash just before it acts, removes only a file or a symlink, removes a folder only with `os.Remove` once the folder is empty, and refuses to write through a file or a symlink where a folder above the path was. An undo is the same apply with each plan entry's sides swapped. An undo learns what the revert wrote from a journal, never from what a path happens to hold. After each change, `put` appends the step to `<store>/glitched-applying/<command>` and fsyncs it, and fsyncs the folder when the journal is new: a file or symlink written, with its mode, blob, size and modification time, a file deleted, or a folder made or removed. It records a folder's removal before it removes the folder, because an undo counts a folder with no entry as one the revert never touched. An undo replaces or removes a file only when the journal's last step for that path wrote it and the path still has that mode, blob, size and time. It puts a file back where it is absent only when the journal says the revert deleted it. It writes into a folder only when the revert made that folder or never touched it. Anything else it refuses and names. That covers a path that holds the target with no journal entry, a folder the user made where the revert left a file, and a folder that isn't empty where the undo would put a file, which it names with everything the folder holds. A crash between a file's change and its record leaves that path with no entry, and a crash between a folder's record and its removal leaves a folder the journal says is gone, so recovery refuses either. The journal is deleted with its `applying/` ref. A record that a crash cut short is cut off the journal, synced, before the next record is appended. A replay whose plan is gone but whose journal holds steps leaves the files as they are and names them. A replay whose plan doesn't follow the revert's own `recovery/<command>` commit puts nothing back and names both commits. Neither says that nothing changed. The journal records what a path held, not which file held it. A replacement identical in bytes, mode, size and mtime counts as the revert's own. So does an empty folder put where the revert made one. Recording each object's device and inode would close this, but on Windows that needs file-index code this PR can't test. `TestRecoveryKeepsEveryByteTheUserWrote` proves the rule. It cuts the revert at each of its hooks. It also cuts recovery at each of its own steps, in the order recovery takes them: deletes first, then writes. At each cut, the user does one of ten things to each path the plan touches, to each folder, the intermediate ones included, and to a file in each folder. The user creates a file, edits it, replaces a file with a folder or a folder with a file, replaces the path with a symlink, writes the target's or the recovery's exact bytes, changes only the mode, makes an empty folder, or truncates the file. A cell passes when recovery succeeds, the acted path and everything under it are as the user left them, every other path, empty folders included, is as the recovery checkpoint has it, and so is every plan path that recovery reached after the user acted. A cell also passes when recovery fails naming the acted path, or fails naming only what the same cut names when the user does nothing. Doing nothing may fail only where a crash cut a change off from its record.
6. Report `Reverted{Revert, Files: restored|unchanged|failed, Changed, Recovery, Error}`. It commits as `thread.reverted` with the effect's completion.

HEAD, the index and the branch never move. When the agent committed during the reverted turns, the commits stay and `git status` shows the restored files as changes. The UI says so.

### Revert, conversation

Every run records its provider cursor as `head` on `turn.ended`. For Claude it is the last assistant message uuid. For Codex it is the turn id the agent already holds. The field is optional JSON, and no column projects it. The runtime reads it with `json_extract`, as `RunReply` reads replies (src, gl:internal/store/read.go:13-44). Glitched's Claude message struct does not parse `uuid` yet (src, gl:internal/provider/claude/turn.go:17-34).

The same `RevertThread` effect does the conversation first and the files second, as T3 and Zeron both do. A failed conversation revert leaves the files alone.

| Provider | Native rewind | Glitched does |
| --- | --- | --- |
| Codex | `thread/fork` with `lastTurnId` (Zeron), or the destructive `thread/revert` (T3) | Fork at the target run's turn id and verify with `thread/turns/list`, as Zeron does. The new thread id becomes the session. The old one stays intact, so a later revert can return to it. A Base target clears the session. |
| Claude | hidden `--resume-session-at` and `--fork-session` | Mint `NEW = uuid(revert command)` as the session, and record `from` and `at` in `thread.reverted`. At the next launch, if `--resume NEW` finds no session, launch `--resume OLD --resume-session-at <at> --fork-session --session-id NEW`. The adapter uses `spec.Session` when it is set, instead of the thread uuid. If the flags are absent, it falls back to portable. |
| ACP, agy | none with a boundary | Portable |

Portable means a fresh session. The next run's first message gets a handoff block built from the log up to the target. It holds each kept run's user text and its reply, newest first, capped at 48 KiB, in a tag like Zeron's task tags (gl:internal/engine/decide.go:800-810). The runtime adds the block at launch when the event that set the session says portable and the session is not yet bound. Glitched refuses a rewind only when the log has nothing to hand over, such as a target before the first reply.

Apply of `thread.reverted` with a session sets `Session` and clears `Context`, as SessionBound does (src, gl:internal/engine/apply.go:54-60). It refuses the event when a run is live past Starting. The lane order means that never happens, so the check guards against a bug and is not a state Glitched expects.

Runs only grow and never change index (src, state.go:37-39). A reverted run stays in the projection. The UI dims the runs between the target and a `thread.reverted` it reads from the log. Child reports and idle compaction read only the projection, so they still see those runs (inferred). That is acceptable for top-level threads, the only ones that can revert.

As built in PR 3:

- The intent is `thread.revert {to, files, conversation}`, protocol 15. A version 14 engine decodes intents without refusing unknown fields, so it would drop `conversation` and put back only the files. A rewind keeps the conversation through the end of To's run. It needs a later turn of the user's to drop. It refuses a base target ("Nothing comes before the thread's first turn") and a recovery target. With `files: false` it needs no worktree, so a thread in its project's folder can rewind its conversation too.
- `thread.reverting` carries `conversation` and `keepFiles`, and `thread.reverted` carries `conversation` (native, portable or failed), `session`, `from`, `at` and `dropped`. A revert that leaves the files reports `status: kept`. A revert does all it was asked or nothing: when the files fail after the conversation was rewound, `conversation` is `failed` too and the session stays. A revert that leaves the files takes no restoring mark and no recovery checkpoint, and its end clears no other revert's mark. A replay whose rewind now fails puts files back only when the earlier attempt left its `applying/<command>` mark, so a crash before the first file moved touches nothing, an ignored draft included. With the mark, it undoes the plan's paths, by the rule above for putting files back. The engine applies a rewind's `thread.reverted` under a run admitted behind it that no agent began, starting or already stopping.
- The conversation's runs. Decide lists the runs after To that the conversation still holds in the effect, and the runtime copies them to `thread.reverted`'s `dropped`. Apply marks them `Dropped`, in the new `runs.dropped` column. That is schema 15, and the upgrade is one way: a build before it refuses the database ("this database is from a newer Glitched"). A rewind refuses a dropped target ("A rewind took this turn out of the conversation"), and counts only kept runs for "The conversation already ends at this turn". Decide checks both at admission, and the effect checks them again against the thread as it holds the lane, so a second rewind admitted before the first committed is refused as well. The effect also drops from its list any run that an earlier rewind already dropped. The handoff holds only kept turns, and the adapter is told only of the runs a rewind drops now. A run's head counts only while the conversation still holds it: a `session.bound` that starts a conversation of the run's own, portable or a new session, leaves the heads before it behind, so a later rewind to such a turn goes portable.
- `turn.ended` carries `head`, which the adapter writes and only the adapter reads. For Codex it is the turn id. For Claude it is `{"at": <last top-level assistant uuid>, "prompt": <uuid of the run's first message>}`. The effect reads the heads, and any `context.reported` with `compacted`, with `json_extract`. It passes the target's head, the dropped runs' heads and whether a dropped run compacted to `provider.Rewinder`.
- Codex forks: a short-lived app-server, `thread/fork {threadId, lastTurnId: head, cwd, excludeTurns: true}`, then `thread/turns/list` to check that the fork ends at that turn, as Zeron does. `thread/revert` would rewrite the thread in place, so a crash before the commit would leave it cut with no record. A fork leaves the old thread as it was, and at worst one unused fork. A fork Codex refuses, or one past the turn, fails the revert. A run with no head goes portable.
- Claude uses flags Claude Code doesn't document: `--resume-session-at`, `--resume-drops-turn` and `--fork-session`. The adapter reads the installed program's bytes for them, cached by path, file identity, size and modification time, so a program moved over the old one is read again. A launch passes a flag only when that read found it. A rewind always names a new session, `uuid(revert)`, because Claude's default session is the thread's uuid. It is native when the program holds the flags, the target has a head, and no dropped run compacted. The fork is made at the thread's next launch:
  1. Try `--resume NEW`, in case an earlier launch already made the fork. Only a fork is ever NEW.
  2. Then `--resume FROM --resume-session-at <at> [--resume-drops-turn <prompt>] --fork-session --session-id NEW`. The drops flag goes only when one run is dropped and the program holds the flag.
  3. Any failure of that launch other than the run's own cancel goes portable on a session of its own, `--session-id <new uuid>`, which the agent reports as `Session()`. So a launch cut off before its handoff never leaves a NEW that a later `--resume NEW` would take for the fork. `--rewind-files` isn't used: Glitched's checkpoints put the files back.
- ACP and the fake are portable. Portable means a new session. The run's message starts with `<rewound_conversation>`, which holds the kept turns newest first: each run's dispatched messages and its reply as `RunReply` reads it. The whole block, its tags and the omission note included, is capped at 48 KiB of valid UTF-8. It always holds the target turn, cut to fit, and leaves out the oldest turns where the rest don't fit, and says how many.
- The rewind is pending while no `session.bound` with `rewound` follows the last `thread.reverted` that rewound, native or portable. A Codex fork the rewind made is pending too, as Codex may have lost it since: a run that finds it lost starts a new thread and gets the kept turns. The host reads that from the log at each launch and passes `SessionSpec.Rewound{From, At}`. A pending rewind refuses an idle compaction. Each adapter stops the thread's parked process instead of reusing it. The first run whose message goes out emits `session.bound {rewound: native|portable}`, which ends the rewind. The message has gone out when the adapter's `Start` returns nil: Codex returns once `turn/start` is written, and ACP once `session/prompt` is written. The host commits the bind on its drain context, so a Stop right after the send can't cancel it. A run whose start fails leaves it to the next run. An agent that isn't `provider.Rewinding` with `Native()` gets the kept turns.
- The UI adds "Rewind conversation to here" and "Rewind conversation and files to here" to the chip's menu. The second carries the files' reason where the files can't go back. Each reason the menu refuses for leads it on a line of its own, and no item has a tooltip. The chip's tooltip says what the chip can still do, then each reason for the rest. A turn whose checkpoint never came still has a chip where its conversation can rewind. A menu is built as it opens, so an open one keeps its reasons until it is opened again. The timeline row reads "Rewound the conversation" or "Rewound the conversation and restored N files", with a Native or Portable badge, which the next run's `session.bound` corrects. Undo puts back only the files.
- Limits: the turns after the target aren't dimmed. A turn with no work row has no chip, so it can't be a rewind target. Undo doesn't restore the conversation, though the old Codex thread and Claude session stay there. The handoff holds each turn's messages and last reply, not its tool calls. A Claude CLI whose flags exist but behave otherwise is caught only by its refusal.

### What the UI shows per turn

Each top-level run's footer in the timeline gets a chip. It reads "3 files", "No file changes", "Not saved: repository over 256 MiB" or "Not saved: submodules". Its menu holds "Restore files to here", "Undo this turn's files" and, in step 3, "Rewind conversation to here" with an "and files" checkbox. A confirmation lists the paths that differ between the target and the newest checkpoint, and says commits stay. After a revert the timeline shows "Restored 12 files to turn 4. Undo" and a "Native" or "Portable" badge. These are controls in an existing surface, so they are built in, with headless before and after shots. A per-turn diff viewer would be a plugin and is left out.

## 3. Events, intents and effects

| Kind | Name | Payload | Projection |
| --- | --- | --- | --- |
| Intent | `thread.revert` | `to {kind, id}`, `files`, `conversation` | none |
| Event | `checkpoint.captured` | `run, phase, status (ready, skipped, failed), reason, files, head, ms` | none, transcript only, like `work.item` |
| Event | `thread.reverting` | `revert, to, files, conversation` | the thread's revision only |
| Event | `thread.reverted` | `revert, to, files, changed, recovery, conversation (native, portable, failed, none), session, from, at, error` | the thread's revision; with the conversation rewind, `session` and the context columns, which exist |
| Field | `turn.ended.head` | provider cursor | none |
| Effect | `checkpoint.capture` | `run, phase` | replay-safe |
| Effect | `thread.revert` | `revert, to, files, conversation` | replay-safe |
| Effect | `checkpoint.drop` | `thread` | replay-safe |
| Observation | `Captured`, `Reverted` | as the events | |

The tables. No new event moves a run, a message or a request, so runEdges, queueEdges and requestEdges stay as they are (src, table.go:18-39). The new guarantees come from Decide's idle check, the lane order and the restoring-folder mark. The one new Apply check is the live-run guard on `thread.reverted`. `Due` is "the thread exists" for drop, and "exists and is not deleted" for the other two.

Commands. A retried `thread.revert` with the same id returns the stored receipt. The same id with other content is a conflict (src, gl:internal/store/commit.go:198-210). The recovery ref is keyed by that id, so a retry finds it.

## 4. Store schema

None. Capture status lives in transcript events and deterministic refs. "Restoring" is a host mark derived from pending effects. A conversation revert changes only `threads.session` and the context columns, which exist. Older builds skip the new event types (events.go:324-336). A schema number would be needed only for a projected "reverted" flag on runs, if code beyond the UI must skip reverted runs. A checkpoints table for indexed status is not needed, because a thread has at most 50 capture events and `events_by_thread` finds them (src, store.go:136-144).

## 5. PR sequence

1. Capture. The private store, the capture and drop effects, `checkpoint.captured`, budgets and retention, with no UI. Done means these tests pass on real git. A capture leaves the checkout's `.git` byte-identical. It honors `.gitignore`, `info/exclude` and `core.excludesFile`. It skips over the size budget and on submodules. A replayed capture writes nothing new. Delete drops the refs. A crash with the effect leased converges at boot (`crashDuring`, gl:internal/runtime/recovery_test.go:22; `assertReplay`, gl:internal/runtime/runtime_test.go:143). Engine tests show the capture effect sits before the next StartTurn in one commit. A timing run on a 50,000-file fixture is explained before the budget is fixed.
2. Restore files. `thread.revert` with Files only (Conversation rejected), the host checks, the restoring mark, recovery and apply, and the chip, menu, confirmation and undo. Done means a round trip restores and undoes. A blocked path refuses with nothing written. A failed apply reapplies recovery. Crashes after recovery, mid-apply and before the commit converge with one recovery ref. A child's or another thread's StartTurn in the folder waits until the revert ends. A live descendant, an outside thread in the folder and a foreign ownership marker each refuse. The fold equals the projection. Headless before and after shots of the timeline.
3. Provider cursors. `turn.ended.head` for Claude and Codex, recorded only. Done means the fake CLIs' uuids and turn ids land in the log, and the fold is unchanged.
4. Conversation revert for Codex, and portable for ACP. Done means the fake Codex records `thread/fork` with `lastTurnId`. A boundary mismatch reports `failed` and leaves the files alone. The next run resumes the forked id. The ACP handoff block reaches the next prompt once, within the cap. A crash between the fork and the commit leaves at most an orphan fork and one `thread.reverted`.
5. Claude native rewind. A probe for the hidden flags, the fork-at-next-launch path, and the adapter honoring `spec.Session`. Done means the fake claude records the exact argv. A later launch resumes NEW. A missing flag falls back to portable. A compaction inside the reverted range goes portable.

Each PR is against main and needs no schema number. Changes in decide.go and host.go stay new cases.

## 6. What could go wrong

- The hidden Claude flags change or vanish. Their combination with `--session-id` is unverified, because no prompt was run. PR 5 needs a fake that mirrors the help text, and one sanctioned real check by Nelson before it ships.
- A Claude rewind across a `/compact` boundary may fail or drop more than one turn. T3 special-cases compaction (ClaudeAdapterV2.ts:3126). Glitched goes portable there.
- An older Codex may accept `lastTurnId` and ignore it. Zeron's verification catches that.
- The first capture of a large repository copies its blobs into the store, and disk grows with retention. The size budget and gc bound it.
- Untracked files that are not ignored, such as a stray `.env`, are copied into the engine's state folder. They stay on the same machine under the same user. Deleting the thread drops them after gc.
- A capture that lands while the person edits in their editor records a mix. The recovery capture before every restore keeps the newest state either way.
- The restoring mark lives in memory. A bug that leaves it set blocks starts in that folder until restart, so a test must prove the mark clears on every revert outcome.
- The sibling worktree isolation for `delegate_task` gives children their own worktrees. They still refuse here as children. Allowing it later needs the same host checks on the child's tree.
- Reverted runs still count in the projection, so an idle compaction may follow a rewind on the new session (inferred from gl:internal/engine/compact.go). That is harmless but visible.

## 7. What I would leave out

- Restore in a shared folder (Zeron's rule). It can be a later choice once the recovery checkpoint has proven itself.
- A per-turn diff viewer, T3's authored-path and branch-switch bookkeeping, multi-repository task folders and submodule checkpoints.
- Codex `thread/revert`, because it destroys history the fork keeps.
- Claude's `--rewind-files`. Glitched owns file checkpoints, and that flag needs Claude's own file checkpointing.
- A checkpoints table and a projected rolled-back state on runs.
- Rewinding child threads, and any checkpoint that crosses engines.
- `--resume-drops-turn`. It guards only a one-turn rewind and needs a deterministic prompt uuid per run. It is worth adding after PR 5.

## Open product decisions

1. Where file restore is allowed. Option A is T3's rule. Restore runs only in the thread's own Glitched worktree, with its tree idle and no outside thread in the folder. Option B is Zeron's rule. Restore runs in any checkout where nothing else is active, after a recovery checkpoint. Recommendation A. Glitched can enforce it with its ownership markers, and B needs a host-wide lock Glitched does not have.
2. Claude rewind. Option A is native, through the hidden `--resume-session-at` and `--fork-session`, with portable as the fallback. T3 does the SDK equivalent. Option B is portable only, as Zeron does. Recommendation A. It keeps tool results and reasoning, and the probe and fallback cover the flags going away.
3. Which threads capture. Option A is only threads in their own worktree, where restore is possible. Option B is every top-level thread in a git folder, as T3 does, which shows per-turn changes everywhere. Recommendation A for v1. B costs a capture per turn in the person's main checkout, where nothing can be restored.
