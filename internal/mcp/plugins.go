package mcp

import (
	"context"
	"encoding/json"
)

// pluginTool is a plugin's tool as the engine lists it.
type pluginTool struct {
	Plugin      string          `json:"plugin"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Input       json.RawMessage `json:"input,omitempty"`
}

// pluginCatalog lists the plugins' tools, each named <plugin>__<tool>: a
// tool's own name never holds __, so the name splits one way. An engine
// that can't say lists none.
func (t *Tools) pluginCatalog(ctx context.Context) []map[string]any {
	if t.Plugins == nil {
		return nil
	}
	raw, err := t.Plugins.PluginTools(ctx)
	if err != nil {
		return nil
	}
	var tools []pluginTool
	if json.Unmarshal(raw, &tools) != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, pt := range tools {
		input := pt.Input
		if len(input) == 0 {
			input = json.RawMessage(`{"type": "object"}`)
		}
		out = append(out, map[string]any{
			"name":        pt.Plugin + "__" + pt.Name,
			"description": pt.Description + " (from the Glitched plugin " + pt.Plugin + ")",
			"inputSchema": input,
		})
	}
	return out
}

// pluginTool calls a plugin's tool for this agent's thread.
func (t *Tools) pluginTool(ctx context.Context, plugin, tool string, args json.RawMessage) (json.RawMessage, error) {
	call, _ := json.Marshal(map[string]any{"plugin": plugin, "name": tool, "args": args, "thread": t.Origin.Thread.String()})
	return t.Plugins.PluginTool(ctx, call)
}
