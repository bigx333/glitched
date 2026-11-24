# Native subagents

Written 2026-11-24 for GLIT-64 gap 5. This is what the branch builds. The GUI that shows it comes in a follow-up PR.

## Decision

**A native subagent is part of its run's transcript, nested under the step that started it. It is not a thread.** Claude Code's `Task`/`Agent` tool and Codex's spawned agents run inside the run's own agent process. They have no run, session or process of their own, so nothing in the engine can stop, steer, answer or settle one apart from its run. A child thread (see child-threads.md) is a different thing. This follows Zeron, whose drivers tag such traffic as `AgentEvent::Subagent { parent_tool_use_id, event }` and route it to the spawn chip's own document.

## Events

- `provider.Nested{Parent, Event}` is how an adapter reports one. `Parent` is the `Item` of the run's step that started the subagent. `Event` is a `TextDelta`, a `Work`, or a `TurnEnded` for how it ended and the tokens it used.
- The runtime (`Host.nested` in `internal/runtime/pump.go`) redacts it as it redacts the run's own events. It commits it as `engine.Subagent`, which the engine logs only while the run is live:
  - `subagent.delta` (`api.SubagentDelta{Run, Parent, Text}`)
  - `subagent.work` (`api.SubagentWork{Parent, WorkItem}`)
  - `subagent.ended` (`api.SubagentEnded{Run, Parent, Outcome, Tokens}`)
- These are new event types, not new fields on `assistant.delta` and `work.item`. So none of them reaches anything that reads the run's own events:
  - the run's reply (`store.RunReply`), which feeds a child's report and its parent's wake;
  - the turn's tokens and the context reading;
  - the turn's open steps.

  `apply` changes only `UpdatedAt`, as it does for a work item.
- **Tokens.** A subagent's tokens are counted on its `subagent.ended`, never in its run's `turn.ended`. That matches what the providers report: Claude Code's `result` usage leaves the subagent out and gives its total in `task_notification` (`usage.total_tokens`), and Codex counts a spawned agent's thread apart from the parent's.
- **No protocol bump, no schema change.** `Event.Decode` returns nothing for a type it doesn't know, so an older GUI or engine skips these events, which is what it showed before. Events are JSON payloads in the `events` table (schema 13).

## Providers

- **Claude Code** (`internal/provider/claude/turn.go`). A `Task` or `Agent` tool use opens a spawn. A message whose `parent_tool_use_id` names one becomes nested events: its text blocks, its tool uses as steps, and its tool results as their ends. A `system task_notification` for the spawn gives its end and `usage.total_tokens`. A message under any other parent is dropped, as before, and so is a subagent's request start (`message_start`): it never measures the run's context. When the run's turn ends, the subagent's open steps fail, as the run's own do.
- **Codex** (`internal/provider/codex`). A `subAgentActivity` item with `kind: started` (Codex 0.146) or a `collabAgentToolCall` `spawnAgent` names the spawned thread. A `subAgentActivity` now shows as a step, "Agent <path>". Notifications on that thread become nested events, as `Agent.nested` maps them: agent messages, shown items, and its `turn/completed` with the thread's `tokenUsage` total. Notifications of a thread the run never spawned are dropped, as before.
- **agy** (ACP): no native subagent traffic. Our wire captures have none, and Zeron's ACP subagent support covers grok, Devin and opencode, not agy.

## Not done

- **Between turns.** A background Claude subagent that writes after its run's turn ended stops the parked process, as any line written while parked does. A Codex child thread's notifications after the run ended are dropped.
- **A subagent's requests.** Claude Code asks for a subagent's tool approvals through the run's own `control_request`, so they already show as the run's approvals. Codex sends a spawned thread's requests on that thread, and glitched refuses them ("Glitched runs no such thread here"), as before.
- The GUI panel that the `Task` step opens.
