package mcp

import (
	"context"
	"encoding/json"
	"errors"

	"glitched/internal/api"
)

const dismissWake = "dismiss_wake"

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
- Secrets. When a worker says it needs a secret, check that it really does. Call request_credentials yourself once, even when several workers need it, and pass back the env script or file paths, never the values.
- Approvals. When a worker blocks on a permission approval, vet it first. Cancel or re-brief a bogus or off-scope command with task_cancel or task_send without telling the user. Escalate only legitimate approvals.
- Coordinate shared facts across workers yourself, and never let two workers write the same files.

3. Verify before you report work ready
- A worker's completion summary is a claim. Inspect its branch, diff, commits and checks against the brief, or delegate that verification, before you report it ready.
- Steer incomplete, off-scope or unverified work with task_send. Never pass unverified output to the user.

4. Escalation threshold and reporting
- Wakes from worker reports and scheduled sweeps are quiet by default. Call dismiss_wake before you end the turn unless the user must act.
- Skip dismiss_wake only for (a) a decision, secret or legitimate approval only the user can give, (b) an unresolved blocker or failure, or (c) a verified deliverable ready for review or merge.
- Never raise a wake for intermediate worker progress, routine steering, a healthy sweep, or WORKERS.md and NOTES.md bookkeeping.
- Lead every update to the user with the verdict and the action the user must take. Keep it short and leave out bookkeeping.

5. Git and safety rails
- Never merge, push, delete branches or run destructive git commands (reset --hard, checkout --, clean) unless the user explicitly asks.
- Preserve uncommitted changes already in the workspace.
- Never fabricate test or check results. Never expose or commit secrets.`

func instructionsFor(role api.ThreadRole) string {
	if role == api.RoleManager {
		return instructions + managerInstructions
	}
	return instructions
}

var dismissWakeTool = map[string]any{
	"name":        dismissWake,
	"description": "Dismiss the current Manager wake so it completes silently without marking the thread unread or notifying the user. Use when a worker report or scheduled sweep requires no user action.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reason": map[string]any{"type": "string", "description": "Optional brief note on why no user attention is needed."},
		},
		"additionalProperties": false,
	},
}

func catalogFor(tools *Tools, ctx context.Context) []map[string]any {
	list := append(append([]map[string]any{delegateTool(tools.Machines)}, taskCatalog...), credentialsTool)
	if tools != nil && tools.Origin.Role == api.RoleManager {
		list = append(list, dismissWakeTool)
	}
	return append(list, tools.pluginCatalog(ctx)...)
}

func (t *Tools) dismiss(ctx context.Context, raw json.RawMessage) (any, error) {
	if t.Origin.Role != api.RoleManager {
		return nil, errors.New("dismiss_wake is only available to Manager threads")
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
		return nil, errors.New("this thread has no live turn to dismiss")
	}
	run := snap.Threads[0].Turn.Run
	_, err = t.Engine.Submit(ctx, api.Command{
		ID:     api.NewID(),
		Target: t.Origin.Thread,
		Body:   api.WakeDismiss{Run: run, Reason: args.Reason},
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"dismissed": true,
		"run":       run.String(),
		"reason":    args.Reason,
	}, nil
}
