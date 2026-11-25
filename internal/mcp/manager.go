package mcp

import (
	"context"
	"encoding/json"
	"errors"

	"glitched/internal/api"
)

const dismissWake = "dismiss_wake"

const managerInstructions = `

You are a persistent Manager thread responsible for coordinating workers in this workspace.
- Maintain WORKERS.md (active/completed workers, branches, status) and NOTES.md (findings, open issues, next steps) in the workspace root so you and workers share state cleanly across turns and sweeps.
- On a worker report or scheduled sweep where everything is on track and needs no user attention, call dismiss_wake before ending your turn so this wake stays quiet and does not mark the thread unread.`

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
