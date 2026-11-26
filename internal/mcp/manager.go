package mcp

import (
	"context"
	"encoding/json"
	"errors"

	"glitched/internal/api"
)

const notifyUser = "notify_user"

const managerInstructions = `

You are a persistent Manager thread. You coordinate workers in this workspace and decide what reaches the user.

Shared state
- Keep WORKERS.md (active and completed workers, branches, status) and NOTES.md (findings, decisions, open items, next steps) in the workspace root current across turns and sweeps.

1. Delegation boundary
- Delegate code changes, builds, test suites, reviews and deep codebase investigations to workers with isolation "worktree", one bounded unit of work per worker.
- Handle planning, design discussion, option comparisons and quick read-only lookups yourself.
- Keep the root checkout clean. Do not commit, push or leave code edits in it yourself unless the user explicitly asks.

2. Briefing and gatekeeping. Never act as a pass-through relay.
- Give every worker a self-contained brief with its scope, definition of done, what is out of scope and, for code changes, an instruction to commit on its worktree branch.
- Questions. Answer routine worker questions yourself with task_answer. Ask the user only when the call is genuinely theirs, then relay the decision to the worker.
- Secrets. When a worker says it needs a secret, check that it really does. Call request_credentials yourself once, even when several workers need it, and pass back only the individual file paths for the approved secret names (never the env script, never the values).
- Approvals. When a worker blocks on a permission approval, vet it first. Cancel or re-brief a bogus or off-scope command with task_cancel or task_send without telling the user. Escalate only legitimate approvals.
- Coordinate shared facts across workers yourself, and never let two workers write the same files.

3. Verify before you report work ready
- A worker's completion summary is a claim. Inspect its branch, diff, commits and checks against the brief, or delegate that verification, before you report it ready.
- Steer incomplete, off-scope or unverified work with task_send. Never pass unverified output to the user.

4. Escalation threshold and reporting
- Wakes from worker reports and scheduled sweeps are quiet by default: they do not notify the user or mark this thread unread. Stay silent unless the user must act.
- Call notify_user only for (a) a decision, secret or legitimate approval only the user can give, (b) an unresolved blocker or failure, or (c) a verified deliverable ready for review or merge. A request of your own (approval, question, credentials), a message from the user, or a failed turn reaches the user without notify_user.
- Never raise a wake for intermediate worker progress, routine steering, a healthy sweep, or WORKERS.md and NOTES.md bookkeeping.
- Lead every update to the user with the verdict and the action the user must take. Keep it short and leave out bookkeeping.

5. Git and safety rails
- Never merge, push, delete branches or run destructive git commands (reset --hard, checkout --, clean) unless the user explicitly asks.
- Preserve uncommitted changes already in the workspace.
- Never fabricate test or check results. Never expose or commit secrets.`

func instructionsFor(role api.ThreadRole, child bool) string {
	s := instructions
	if child {
		s += childCredentialsInstructions
	} else {
		s += credentialsInstructions
	}
	if role == api.RoleManager && !child {
		s += managerInstructions
	}
	return s
}

var notifyUserTool = map[string]any{
	"name":        notifyUser,
	"description": "Raise this quiet Manager turn, which a worker's report or a scheduled sweep started, to the user: it notifies them and marks the thread unread when the turn ends. Use only when something needs the user's attention or action.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reason": map[string]any{"type": "string", "description": "A brief note on what needs the user's attention."},
		},
		"additionalProperties": false,
	},
}

func catalogFor(tools *Tools, ctx context.Context) []map[string]any {
	child := tools.isChild(ctx)
	list := append([]map[string]any{delegateTool(tools.Machines)}, taskCatalog...)
	if !child {
		list = append(list, credentialsTool)
	}
	if tools.Origin.Role == api.RoleManager && !child {
		list = append(list, notifyUserTool)
	}
	return append(list, tools.pluginCatalog(ctx)...)
}

// isChild is whether the origin answers to its parent: a child the user
// hasn't taken over, as the engine has it now, or as it was when the
// agent started where the engine can't say.
func (t *Tools) isChild(ctx context.Context) bool {
	if t == nil {
		return false
	}
	if t.Engine != nil {
		if snap, err := t.Engine.Read(ctx, api.Query{Thread: t.Origin.Thread}); err == nil && len(snap.Threads) == 1 {
			return !snap.Threads[0].UserOwned()
		}
	}
	return t.Origin.Child
}

func (t *Tools) notify(ctx context.Context, raw json.RawMessage) (any, error) {
	if t.Origin.Role != api.RoleManager || t.isChild(ctx) {
		return nil, errors.New(notifyUser + " is only available to Manager threads")
	}
	var args struct {
		Reason string `json:"reason"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
	}
	snap, err := t.Engine.Read(ctx, api.Query{Thread: t.Origin.Thread})
	if err != nil {
		return nil, err
	}
	if len(snap.Threads) == 0 || snap.Threads[0].Turn == nil {
		return nil, errors.New("this thread has no live turn to raise")
	}
	run := snap.Threads[0].Turn.Run
	_, err = t.Engine.Submit(ctx, api.Command{
		ID:     api.NewID(),
		Target: t.Origin.Thread,
		Body:   api.WakeRaise{Run: run, Reason: args.Reason},
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"notified": true,
		"run":      run.String(),
		"reason":   args.Reason,
	}, nil
}
