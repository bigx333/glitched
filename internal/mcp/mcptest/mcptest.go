// Package mcptest plays an agent's side of glitched mcp in tests: it starts
// the server an agent is given and calls its tools as the agent would.
package mcptest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// RequestCredentials starts the glitched MCP server command with env, the
// whole environment, and calls request_credentials for GLX_PROBE, as an
// agent would; it returns the text of the tool's result. When ctx ends
// first the server is stopped, as by a client that stops waiting.
func RequestCredentials(ctx context.Context, command string, args, env []string) (json.RawMessage, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = env
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { in.Close(); cmd.Wait() }()
	r := bufio.NewReader(out)
	call := func(id int, method string, params any) (json.RawMessage, error) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		in.Write(append(b, '\n'))
		line, err := r.ReadBytes('\n')
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("%s: timed out", method)
			}
			return nil, err
		}
		var reply struct {
			Result json.RawMessage
			Error  json.RawMessage
		}
		json.Unmarshal(line, &reply)
		if reply.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, reply.Error)
		}
		return reply.Result, nil
	}
	if _, err := call(0, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fake", "version": "1"}}); err != nil {
		return nil, err
	}
	res, err := call(1, "tools/call", map[string]any{"name": "request_credentials", "arguments": map[string]any{
		"reason": "probe", "fields": []any{map[string]any{"name": "GLX_PROBE", "label": "Probe key"}}, "client_request_id": "fake-1",
	}})
	if err != nil {
		return nil, err
	}
	var result struct {
		Content []struct{ Text string }
		IsError bool
	}
	json.Unmarshal(res, &result)
	if len(result.Content) == 0 || result.IsError {
		return nil, fmt.Errorf("request_credentials: %s", res)
	}
	return json.RawMessage(result.Content[0].Text), nil
}
