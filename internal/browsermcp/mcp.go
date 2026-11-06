// Package browsermcp gives agents the app's browser as tools: an MCP
// server on stdio, which Claude Code starts for each run (glitched
// browser-mcp), and the socket it reaches the app through, where the
// browser pane is (docs/browser.md, Agents).
//
// The server knows nothing of tabs or pages: each tool call goes to the
// app as it comes, which acts on its tabs and answers in text. An app not
// open answers nothing, and the tool says so, so the tools are there for
// every run, and work as soon as the app is.
package browsermcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sync"
)

// Tool is a tool's definition, as tools/list gives it.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations Annotations     `json:"annotations"`
}

// Annotations hint how a tool acts, for the agent's permission prompts.
type Annotations struct {
	ReadOnly    bool `json:"readOnlyHint"`
	Destructive bool `json:"destructiveHint"`
	OpenWorld   bool `json:"openWorldHint"`
}

const tabArg = `"tab": {"type": "integer", "description": "The tab's id, as browser_tabs lists it; the tab shown when left out."}`

// Tools are the browser's tools.
var Tools = []Tool{
	{
		Name: "browser_tabs",
		Description: "List, open, show or close the tabs of the browser in the Glitched app, the pane beside the user's chat. " +
			"Each tab has an id the other tools take. Opening one at a url loads it and returns its snapshot.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {
			"action": {"type": "string", "enum": ["list", "new", "select", "close"]},
			"url": {"type": "string", "description": "For new: the address to open."},
			"tab": {"type": "integer", "description": "For select and close: the tab's id."}
		}, "required": ["action"]}`),
		Annotations: Annotations{Destructive: true, OpenWorld: true}, // close closes the user's tab
	},
	{
		Name: "browser_navigate",
		Description: "Load an address in a tab of the Glitched browser, or go back, forward or reload, and wait for the page to load. " +
			"Shows the browser pane and the tab, so the user sees it. Returns the page's snapshot.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {
			"url": {"type": "string", "description": "The address, http or https; one without a scheme loads over https, or http on this machine and the local network."},
			"action": {"type": "string", "enum": ["back", "forward", "reload"], "description": "Instead of url."},
			` + tabArg + `
		}}`),
		Annotations: Annotations{OpenWorld: true},
	},
	{
		Name: "browser_snapshot",
		Description: "Read what a tab of the Glitched browser shows, as lines of roles and names, as an accessibility tree reads. " +
			"Each element to act on has a ref, as [ref=s12e3], which browser_click and browser_type take while this snapshot is the tab's latest.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {
			"full": {"type": "boolean", "description": "Also list the page's structure (landmarks, lists, tables) around the elements to act on."},
			` + tabArg + `
		}}`),
		// Not read-only: a tab never shown loads its page, and a snapshot
		// replaces the tab's refs.
		Annotations: Annotations{OpenWorld: true},
	},
	{
		Name: "browser_click",
		Description: "Click an element of a page in the Glitched browser, by its ref from the tab's latest snapshot. " +
			"Waits for any page it loads, and returns the new snapshot.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {
			"ref": {"type": "string", "description": "The element's ref, as s12e3."}
		}, "required": ["ref"]}`),
		Annotations: Annotations{Destructive: true, OpenWorld: true},
	},
	{
		Name: "browser_type",
		Description: "Replace the text of a text box or editor of a page in the Glitched browser, by its ref from the tab's latest snapshot, " +
			"and press Enter after it with submit. Returns the new snapshot.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {
			"ref": {"type": "string", "description": "The element's ref, as s12e3."},
			"text": {"type": "string"},
			"submit": {"type": "boolean", "description": "Press Enter after typing, which submits a form."}
		}, "required": ["ref", "text"]}`),
		Annotations: Annotations{Destructive: true, OpenWorld: true},
	},
}

// Call runs a tool: it answers the agent in text, or says why the tool
// failed.
type Call func(ctx context.Context, tool string, args json.RawMessage) (string, error)

// Versions are the protocol versions the server speaks, the latest first.
var Versions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	Content []content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// maxMessage is the longest message the server reads.
const maxMessage = 4 << 20

// Serve answers the MCP client on in and out, one JSON-RPC message a
// line, until in ends or ctx does. Tool calls run as they come, each on
// its own, as a client may send the next before the last is answered.
func Serve(ctx context.Context, in io.Reader, out io.Writer, call Call) error {
	ctx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	var wg sync.WaitGroup
	// As in ends, the calls still running end too: the agent is gone.
	defer wg.Wait()
	defer cancel()
	// running are the calls not yet answered, by id, which the client
	// may cancel.
	var calls sync.Mutex
	running := map[string]context.CancelFunc{}
	enc := json.NewEncoder(out)
	send := func(r response) {
		r.JSONRPC = "2.0"
		mu.Lock()
		defer mu.Unlock()
		enc.Encode(r)
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), maxMessage)
	for sc.Scan() {
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			send(response{ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			// A notification: none needs an answer, and only a call
			// cancelled, as the user stops the turn, needs anything done.
			if req.Method == "notifications/cancelled" {
				var p struct {
					RequestID json.RawMessage `json:"requestId"`
				}
				json.Unmarshal(req.Params, &p)
				calls.Lock()
				if stop := running[string(p.RequestID)]; stop != nil {
					delete(running, string(p.RequestID))
					stop()
				}
				calls.Unlock()
			}
			continue
		}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(req.Params, &p)
			v := Versions[0]
			if slices.Contains(Versions, p.ProtocolVersion) {
				v = p.ProtocolVersion
			}
			send(response{ID: req.ID, Result: map[string]any{
				"protocolVersion": v,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "glitched-browser", "version": "1"},
				"instructions": "These tools drive the browser in the Glitched app, beside the user's chat, which the user sees. " +
					"Take a snapshot to find an element's ref, then click or type by ref.",
			}})
		case "ping":
			send(response{ID: req.ID, Result: map[string]any{}})
		case "tools/list":
			send(response{ID: req.ID, Result: map[string]any{"tools": Tools}})
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				send(response{ID: req.ID, Error: &rpcError{-32602, "invalid params"}})
				continue
			}
			if !slices.ContainsFunc(Tools, func(t Tool) bool { return t.Name == p.Name }) {
				send(response{ID: req.ID, Error: &rpcError{-32602, "unknown tool " + p.Name}})
				continue
			}
			if len(p.Arguments) == 0 {
				p.Arguments = json.RawMessage("{}")
			}
			callCtx, stop := context.WithCancel(ctx)
			calls.Lock()
			running[string(req.ID)] = stop
			calls.Unlock()
			wg.Add(1)
			go func(id json.RawMessage) {
				defer wg.Done()
				text, err := call(callCtx, p.Name, p.Arguments)
				calls.Lock()
				_, open := running[string(id)]
				delete(running, string(id))
				calls.Unlock()
				stop()
				// A call the client cancelled gets no answer (MCP's
				// cancellation).
				if !open {
					return
				}
				r := callResult{Content: []content{{"text", text}}}
				if err != nil {
					r = callResult{Content: []content{{"text", err.Error()}}, IsError: true}
				}
				send(response{ID: id, Result: r})
			}(req.ID)
		default:
			send(response{ID: req.ID, Error: &rpcError{-32601, "method not found: " + req.Method}})
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
