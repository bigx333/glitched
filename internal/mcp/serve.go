package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"sync"

	"glitched/internal/api"
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
	notify := func(method string) {
		mu.Lock()
		defer mu.Unlock()
		enc.Encode(struct {
			JSONRPC string `json:"jsonrpc"`
			Method  string `json:"method"`
		}{JSONRPC: "2.0", Method: method})
	}
	var calls sync.WaitGroup
	defer calls.Wait()
	watchCtx, watchCancel := context.WithCancel(ctx)
	var watchWG sync.WaitGroup
	defer func() {
		watchCancel()
		watchWG.Wait()
	}()
	var watchOnce sync.Once
	startWatch := func(initialChild bool) {
		if tools == nil || tools.Engine == nil || tools.Origin.Thread.IsZero() {
			return
		}
		watchOnce.Do(func() {
			watchWG.Go(func() {
				watchOwnership(watchCtx, tools, initialChild, notify)
			})
		})
	}
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
			calls.Go(func() {
				rep, watch, child := handle(ctx, tools, m)
				send(rep)
				if watch {
					startWatch(child)
				}
			})
		}
	}
	return sc.Err()
}

// watchOwnership watches a child thread for live takeover or handback and
// emits notifications/tools/list_changed when UserOwned flips so MCP clients
// refresh their cached tool catalog (e.g. exposing request_credentials).
func watchOwnership(ctx context.Context, tools *Tools, lastChild bool, notify func(string)) {
	snap, err := tools.Engine.Read(ctx, api.Query{Thread: tools.Origin.Thread})
	if err != nil || len(snap.Threads) != 1 {
		return
	}
	th := snap.Threads[0]
	if th.Parent.IsZero() && th.Origin == nil {
		return
	}
	if cur := !th.UserOwned(); cur != lastChild {
		lastChild = cur
		notify("notifications/tools/list_changed")
	}
	stream, err := tools.Engine.Open(ctx, api.Subscription{Thread: tools.Origin.Thread, After: snap.Cursor, Views: true})
	if err != nil {
		return
	}
	defer stream.Close()
	for {
		item, err := stream.Next(ctx)
		if err != nil {
			return
		}
		if item.Thread == nil {
			continue
		}
		if cur := !item.Thread.UserOwned(); cur != lastChild {
			lastChild = cur
			notify("notifications/tools/list_changed")
		}
	}
}

func handle(ctx context.Context, tools *Tools, m message) (reply, bool, bool) {
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
		child := tools.isChild(ctx)
		r.Result = map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": tools != nil && tools.Engine != nil}},
			"serverInfo":      map[string]any{"name": "glitched", "version": "1"},
			"instructions":    instructionsFor(tools.Origin.Role, child),
		}
		return r, true, child
	case "ping":
		r.Result = struct{}{}
	case "tools/list":
		child := tools.isChild(ctx)
		r.Result = map[string]any{"tools": catalogFor(tools, ctx)}
		return r, true, child
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
			return r, false, false
		}
		// A tool's name holds no __, a plugin's id may: split on the last.
		if i := strings.LastIndex(p.Name, "__"); i > 0 && tools.Plugins != nil {
			plugin, tool := p.Name[:i], p.Name[i+2:]
			out, err := tools.pluginTool(ctx, plugin, tool, p.Arguments)
			if err != nil {
				r.Result = map[string]any{"content": []any{text(err.Error())}, "isError": true}
				return r, false, false
			}
			r.Result = map[string]any{"content": []any{text(string(out))}, "isError": false}
			return r, false, false
		}
		if p.Name == requestCredentials {
			v, err := tools.credentials(ctx, p.Arguments, p.Meta.ToolUseID)
			if err != nil {
				r.Result = map[string]any{"content": []any{text(err.Error())}, "isError": true}
				return r, false, false
			}
			b, _ := json.MarshalIndent(v, "", "  ")
			r.Result = map[string]any{"content": []any{text(string(b))}, "structuredContent": v, "isError": false}
			return r, false, false
		}
		if slices.Contains([]string{listTasks, taskStatus, taskCancel, taskSend, taskAnswer}, p.Name) {
			v, err := tools.tasks(ctx, p.Name, p.Arguments, p.Meta.ToolUseID)
			if err != nil {
				r.Result = map[string]any{"content": []any{text(err.Error())}, "isError": true}
				return r, false, false
			}
			b, _ := json.MarshalIndent(v, "", "  ")
			r.Result = map[string]any{"content": []any{text(string(b))}, "structuredContent": v, "isError": false}
			return r, false, false
		}
		if p.Name == notifyUser {
			v, err := tools.notify(ctx, p.Arguments)
			if err != nil {
				r.Result = map[string]any{"content": []any{text(err.Error())}, "isError": true}
				return r, false, false
			}
			b, _ := json.MarshalIndent(v, "", "  ")
			r.Result = map[string]any{"content": []any{text(string(b))}, "structuredContent": v, "isError": false}
			return r, false, false
		}
		if p.Name != delegateTask {
			r.Error = &rpcError{invalidParams, "unknown tool: " + p.Name}
			return r, false, false
		}
		v, err := tools.delegate(ctx, p.Arguments, p.Meta.ToolUseID)
		if err != nil {
			r.Result = map[string]any{"content": []any{text(err.Error())}, "isError": true}
			return r, false, false
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		r.Result = map[string]any{"content": []any{text(string(b))}, "structuredContent": v, "isError": false}
	default:
		r.Error = &rpcError{methodNotFound, "unknown method: " + m.Method}
	}
	return r, false, false
}

func text(s string) map[string]string { return map[string]string{"type": "text", "text": s} }
