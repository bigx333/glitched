package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"sync"
)

// protocols are the MCP revisions the server speaks, newest first. A
// client asking for another gets the newest and decides for itself.
var protocols = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const (
	parseError     = -32700
	invalidRequest = -32600
	methodNotFound = -32601
	invalidParams  = -32602
)

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type reply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve answers the requests on in, a JSON-RPC message a line, on out
// until in ends. Requests run at once, each on its own, so a client's ping
// is answered while a tool call waits on the engine.
func Serve(ctx context.Context, in io.Reader, out io.Writer, tools *Tools) error {
	var mu sync.Mutex
	enc := json.NewEncoder(out)
	send := func(r reply) {
		r.JSONRPC = "2.0"
		if r.ID == nil {
			r.ID = json.RawMessage("null")
		}
		mu.Lock()
		defer mu.Unlock()
		enc.Encode(r)
	}
	var calls sync.WaitGroup
	defer calls.Wait()
	sc := bufio.NewScanner(in)
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			send(reply{Error: &rpcError{parseError, "parse error: " + err.Error()}})
			continue
		}
		switch {
		case m.Method == "" && m.ID == nil:
			send(reply{Error: &rpcError{invalidRequest, "missing method"}})
		case m.Method == "" || m.ID == nil:
			// A notification, or a reply to a request this server never sends.
		default:
			calls.Go(func() { send(handle(ctx, tools, m)) })
		}
	}
	return sc.Err()
}

func handle(ctx context.Context, tools *Tools, m message) reply {
	r := reply{ID: m.ID}
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(m.Params, &p)
		version := protocols[0]
		if slices.Contains(protocols, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		r.Result = map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "glitched", "version": "1"},
			"instructions":    instructions,
		}
	case "ping":
		r.Result = struct{}{}
	case "tools/list":
		r.Result = map[string]any{"tools": append(slices.Clone(catalog), tools.pluginCatalog(ctx)...)}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      struct {
				ToolUseID string `json:"claudecode/toolUseId"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			r.Error = &rpcError{invalidParams, err.Error()}
			return r
		}
		if plugin, tool, ok := strings.Cut(p.Name, "__"); ok && tools.Plugins != nil {
			out, err := tools.pluginTool(ctx, plugin, tool, p.Arguments)
			if err != nil {
				r.Result = map[string]any{"content": []any{text(err.Error())}, "isError": true}
				return r
			}
			r.Result = map[string]any{"content": []any{text(string(out))}, "isError": false}
			return r
		}
		if p.Name != delegateTask {
			r.Error = &rpcError{invalidParams, "unknown tool: " + p.Name}
			return r
		}
		v, err := tools.delegate(ctx, p.Arguments, p.Meta.ToolUseID)
		if err != nil {
			r.Result = map[string]any{"content": []any{text(err.Error())}, "isError": true}
			return r
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		r.Result = map[string]any{"content": []any{text(string(b))}, "structuredContent": v, "isError": false}
	default:
		r.Error = &rpcError{methodNotFound, "unknown method: " + m.Method}
	}
	return r
}

func text(s string) map[string]string { return map[string]string{"type": "text", "text": s} }
