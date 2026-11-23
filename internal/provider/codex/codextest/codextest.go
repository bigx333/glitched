// Package codextest is a fake Codex app-server for tests. A test binary
// plays it, as Main says, when an adapter starts the test binary as its
// codex; the scenario the environment names says what the fake does, and
// the fake writes what it received to a log the test reads.
package codextest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Scenario names, in the fake's environment, what it does.
	Scenario = "GLITCHED_FAKE_CODEX"
	// Log names the file the fake appends what it received to.
	Log = "GLITCHED_FAKE_CODEX_LOG"
	// Replay names, for the scenario "replay", a wire capture the fake
	// answers from as the app-server that made it did: lines ">> " the
	// client sent and "<< " app-server did, a JSON-RPC message each.
	Replay = "GLITCHED_FAKE_CODEX_REPLAY"
)

// Entry is a line of the fake's log: a method it was called with and its
// params, "answer" and what the client answered a request of its with,
// or "refused" and why the fake refused a call.
type Entry struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	PID    int             `json:"pid"`
}

// Read is the log at path, in order.
func Read(path string) ([]Entry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Entry
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(nil, 1<<20)
	for s.Scan() {
		var e Entry
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, s.Err()
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type fake struct {
	scenario string
	pid      int
	cwd      string

	wmu sync.Mutex
	mu  sync.Mutex
	// next is the id of the fake's next request; answers to them go to
	// waiting.
	next    int
	waiting map[string]chan message
	thread  string
	turns   int
	turn    string
	active  bool
	stop    chan struct{} // takes an interrupt of the active turn
	steers  chan string

	replay *replay
}

// Main plays the fake app-server, until its stdin closes, when the
// environment names a scenario, and reports whether it did. A test binary
// calls it first thing in TestMain and returns when it is true.
func Main() bool {
	scenario := os.Getenv(Scenario)
	if scenario == "" {
		return false
	}
	cwd, _ := os.Getwd()
	f := &fake{scenario: scenario, pid: os.Getpid(), cwd: cwd, waiting: map[string]chan message{}, steers: make(chan string, 4)}
	if scenario == "replay" {
		r, err := loadReplay(os.Getenv(Replay))
		if err != nil {
			fmt.Fprintln(os.Stderr, "codextest:", err)
			os.Exit(2)
		}
		f.replay = r
	}
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if t := bytes.TrimSpace(line); len(t) > 0 {
			var m message
			if json.Unmarshal(t, &m) == nil {
				f.take(m, t)
			}
		}
		if err != nil {
			break
		}
	}
	f.record("stdin closed", nil)
	return true
}

func (f *fake) record(method string, params any) {
	raw, _ := json.Marshal(params)
	if r, ok := params.(json.RawMessage); ok {
		raw = r
	}
	line, _ := json.Marshal(Entry{Method: method, Params: raw, PID: f.pid})
	file, err := os.OpenFile(os.Getenv(Log), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	file.Write(append(line, '\n'))
}

func (f *fake) send(m any) {
	line, _ := json.Marshal(m)
	f.wmu.Lock()
	defer f.wmu.Unlock()
	os.Stdout.Write(append(line, '\n'))
}

func (f *fake) reply(id json.RawMessage, result any) {
	f.send(map[string]any{"id": id, "result": result})
}

func (f *fake) refuse(id json.RawMessage, code int, text string) {
	f.record("refused", text)
	f.send(map[string]any{"id": id, "error": map[string]any{"code": code, "message": text}})
}

func (f *fake) notify(method string, params any) {
	f.send(map[string]any{"method": method, "params": params})
}

// ask sends a request of the fake's and returns its id, and the answer
// once it comes.
func (f *fake) ask(method string, params any) (string, <-chan message) {
	f.mu.Lock()
	id := strconv.Itoa(f.next)
	f.next++
	ch := make(chan message, 1)
	f.waiting[id] = ch
	f.mu.Unlock()
	f.send(map[string]any{"id": json.RawMessage(id), "method": method, "params": params})
	return id, ch
}

// answer is what the client answered request method with, as the log
// shows it: its result, or its error.
func (f *fake) answer(method string, params any) message {
	_, ch := f.ask(method, params)
	select {
	case m := <-ch:
		return m
	case <-time.After(20 * time.Second):
		return message{}
	}
}

func (f *fake) take(m message, raw []byte) {
	if f.replay != nil {
		if m.Method != "" {
			f.record(m.Method, m.Params)
		} else {
			f.record("answer", json.RawMessage(raw))
		}
		f.replay.take(f, m)
		return
	}
	if m.Method == "" {
		f.record("answer", json.RawMessage(raw))
		f.mu.Lock()
		ch := f.waiting[string(m.ID)]
		delete(f.waiting, string(m.ID))
		f.mu.Unlock()
		if ch != nil {
			ch <- m
		}
		return
	}
	f.record(m.Method, m.Params)
	switch m.Method {
	case "initialize":
		if f.scenario == "hang-on-initialize" {
			return
		}
		f.reply(m.ID, map[string]any{"userAgent": "glitched/0.171.0 (Fake OS; arm64) fake (glitched; 1)", "codexHome": "/fake", "platformFamily": "unix", "platformOs": "macos"})
	case "initialized":
	case "account/read":
		if f.scenario == "signed-out" {
			f.reply(m.ID, map[string]any{"account": nil, "userId": nil, "requiresOpenaiAuth": true, "workspaceRouting": nil})
			return
		}
		f.reply(m.ID, map[string]any{"account": map[string]any{"type": "chatgpt", "email": "user@example.com", "planType": "plus"}, "requiresOpenaiAuth": true})
	case "model/list":
		var p struct{ Cursor string }
		json.Unmarshal(m.Params, &p)
		effort := func(ids ...string) []map[string]any {
			var out []map[string]any
			for _, id := range ids {
				out = append(out, map[string]any{"reasoningEffort": id, "description": id})
			}
			return out
		}
		if p.Cursor == "" {
			f.reply(m.ID, map[string]any{"data": []map[string]any{
				{"id": "gpt-6-luna", "model": "gpt-6-luna", "displayName": "GPT-6 Luna", "description": "Fast and cheap", "hidden": false, "isDefault": false, "supportedReasoningEfforts": effort("low", "medium")},
				{"id": "codex-auto-review", "model": "codex-auto-review", "displayName": "Auto review", "hidden": true, "isDefault": false},
			}, "nextCursor": "2"})
			return
		}
		f.reply(m.ID, map[string]any{"data": []map[string]any{
			{"id": "gpt-6.2-sol", "model": "gpt-6.2-sol", "displayName": "GPT-6.2 Sol", "description": "The default", "hidden": false, "isDefault": true, "supportedReasoningEfforts": effort("low", "medium", "high", "xhigh")},
		}, "nextCursor": nil})
	case "thread/resume":
		var p struct{ ThreadID string }
		json.Unmarshal(m.Params, &p)
		switch f.scenario {
		case "writer":
			f.refuse(m.ID, -32600, "thread "+p.ThreadID+" already has an active writer")
			return
		case "closing":
			f.refuse(m.ID, -32600, "thread "+p.ThreadID+" is closing; try again")
			return
		case "other-id":
			f.reply(m.ID, map[string]any{"thread": map[string]any{"id": p.ThreadID + "-other"}})
			return
		}
		if _, err := os.Stat(f.rollout(p.ThreadID)); err != nil {
			f.refuse(m.ID, -32600, "no rollout found for thread id "+p.ThreadID)
			return
		}
		f.mu.Lock()
		f.thread = p.ThreadID
		f.mu.Unlock()
		f.reply(m.ID, map[string]any{"thread": map[string]any{"id": p.ThreadID}, "model": "gpt-6.2-sol"})
	case "thread/start":
		f.mu.Lock()
		f.thread = fmt.Sprintf("019a0000-0000-7000-8000-%012d", f.pid)
		thread := f.thread
		os.WriteFile(f.rollout(thread), nil, 0o600)
		f.mu.Unlock()
		f.reply(m.ID, map[string]any{"thread": map[string]any{"id": thread}, "model": "gpt-6.2-sol"})
		f.notify("thread/started", map[string]any{"thread": map[string]any{"id": thread}})
		f.notify("mcpServer/startupStatus/updated", map[string]any{"name": "glitched", "status": "starting"})
	case "turn/start":
		var p struct {
			Input []struct{ Text string } `json:"input"`
		}
		json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.turns++
		n := f.turns
		f.turn = fmt.Sprintf("turn-%d", n)
		turn := f.turn
		f.stop = make(chan struct{}, 1)
		f.active = f.scenario != "late-started"
		f.mu.Unlock()
		if f.scenario == "stale" && n == 2 {
			thread, _ := f.ids()
			f.notify("item/agentMessage/delta", map[string]any{"threadId": thread, "turnId": "turn-1", "itemId": "msg-turn-1", "delta": "old text"})
		}
		f.reply(m.ID, map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress", "items": []any{}}})
		thread, _ := f.ids()
		said, _ := os.ReadFile(f.rollout(thread))
		if len(p.Input) > 0 {
			os.WriteFile(f.rollout(thread), append(said, p.Input[0].Text+"\n"...), 0o600)
		}
		go f.play(n, turn, string(said))
	case "turn/interrupt":
		f.mu.Lock()
		active, stop := f.active, f.stop
		f.mu.Unlock()
		if !active {
			f.refuse(m.ID, -32600, "no active turn to interrupt")
			return
		}
		f.reply(m.ID, map[string]any{})
		stop <- struct{}{}
	case "turn/steer":
		var p struct {
			Input []struct{ Text string } `json:"input"`
		}
		json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		active, turn := f.active, f.turn
		f.mu.Unlock()
		if !active || len(p.Input) == 0 {
			f.refuse(m.ID, -32600, "no active turn to steer")
			return
		}
		f.reply(m.ID, map[string]any{"turnId": turn})
		f.steers <- p.Input[0].Text
	default:
		f.refuse(m.ID, -32601, "unsupported method "+m.Method)
	}
}

func (f *fake) ids() (thread, turn string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.thread, f.turn
}

func (f *fake) started() {
	thread, turn := f.ids()
	f.mu.Lock()
	f.active = true
	f.mu.Unlock()
	f.notify("thread/status/changed", map[string]any{"threadId": thread, "status": map[string]any{"type": "active", "activeFlags": []any{}}})
	f.notify("turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "inProgress", "items": []any{}}})
}

func (f *fake) item(method string, it map[string]any) {
	thread, turn := f.ids()
	f.notify(method, map[string]any{"threadId": thread, "turnId": turn, "item": it, "startedAtMs": 1})
}

// message streams an agent message of chunks.
func (f *fake) message(id string, chunks ...string) {
	thread, turn := f.ids()
	f.item("item/started", map[string]any{"type": "agentMessage", "id": id, "text": ""})
	for _, c := range chunks {
		f.notify("item/agentMessage/delta", map[string]any{"threadId": thread, "turnId": turn, "itemId": id, "delta": c})
	}
	f.item("item/completed", map[string]any{"type": "agentMessage", "id": id, "text": strings.Join(chunks, ""), "phase": "final_answer"})
}

func (f *fake) complete(status string, turnErr any) {
	thread, turn := f.ids()
	f.mu.Lock()
	f.active = false
	f.mu.Unlock()
	usage := map[string]any{"totalTokens": 100, "inputTokens": 80, "cachedInputTokens": 0, "outputTokens": 20, "reasoningOutputTokens": 0}
	f.notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": map[string]any{"total": usage, "last": usage, "modelContextWindow": 258400}})
	f.notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": status, "error": turnErr, "items": []any{}}})
	f.notify("thread/status/changed", map[string]any{"threadId": thread, "status": map[string]any{"type": "idle"}})
}

func decision(m message) string {
	var r struct{ Decision string }
	json.Unmarshal(m.Result, &r)
	return r.Decision
}

// rollout is where the fake keeps what the user said in thread, as Codex
// keeps a thread's rollout under CODEX_HOME: beside the log, so a test's
// fakes share it across restarts.
func (f *fake) rollout(thread string) string {
	return filepath.Join(filepath.Dir(os.Getenv(Log)), "rollout-"+thread)
}

// play plays turn n of the scenario; said is what the user said in the
// thread's earlier turns.
func (f *fake) play(n int, turn, said string) {
	thread, _ := f.ids()
	switch f.scenario {
	case "late-started":
		time.Sleep(300 * time.Millisecond)
		f.started()
		select {
		case <-f.stop:
			f.complete("interrupted", nil)
		case <-time.After(2 * time.Second):
			f.message("msg-"+turn, "ran to its end")
			f.complete("completed", nil)
		}
		return
	}
	f.started()
	switch f.scenario {
	case "stale":
		if n == 2 {
			d := decision(f.answer("item/commandExecution/requestApproval", map[string]any{"threadId": thread, "turnId": "turn-1", "itemId": "cmd-old", "command": "rm -rf old", "cwd": f.cwd, "startedAtMs": 1}))
			f.message("msg-"+turn, "new text, old ask "+d)
		} else {
			f.message("msg-"+turn, "first turn")
		}
	case "network":
		cmd := map[string]any{"type": "commandExecution", "id": "cmd-n", "command": "curl status.example", "cwd": f.cwd, "status": "inProgress", "commandActions": []any{}}
		f.item("item/started", cmd)
		d := decision(f.answer("item/commandExecution/requestApproval", map[string]any{"threadId": thread, "turnId": turn, "itemId": "cmd-n", "command": "curl status.example", "cwd": f.cwd, "reason": "check status", "startedAtMs": 1,
			"networkApprovalContext": map[string]any{"host": "status.example", "protocol": "https"},
			"availableDecisions":     []any{"accept", map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []any{"curl"}}}, "cancel"}}))
		cmd["status"] = map[bool]string{true: "completed", false: "declined"}[d == "accept"]
		f.item("item/completed", cmd)
		f.message("msg-"+turn, "decision "+d)
	case "memory":
		f.message("msg-"+turn, "earlier you said: "+strings.ReplaceAll(strings.TrimSpace(said), "\n", " | "))
	case "two-messages":
		f.message("msg-a", "first")
		f.message("msg-b", "second")
	case "whole":
		f.item("item/completed", map[string]any{"type": "agentMessage", "id": "msg-w", "text": "all at once"})
	case "compacted":
		f.item("item/started", map[string]any{"type": "contextCompaction", "id": "compact-1"})
		f.item("item/completed", map[string]any{"type": "contextCompaction", "id": "compact-1"})
		f.notify("thread/compacted", map[string]any{"threadId": thread, "turnId": turn})
		f.message("msg-"+turn, "compacted")
	case "command":
		cmd := map[string]any{"type": "commandExecution", "id": "cmd-1", "command": "ls -a", "cwd": f.cwd, "status": "inProgress", "commandActions": []any{}}
		f.item("item/started", cmd)
		d := decision(f.answer("item/commandExecution/requestApproval", map[string]any{"threadId": thread, "turnId": turn, "itemId": "cmd-1", "command": "ls -a", "cwd": f.cwd, "reason": "list the files", "startedAtMs": 1}))
		if d == "accept" {
			cmd["status"], cmd["exitCode"], cmd["aggregatedOutput"] = "completed", 0, "a.txt\n"
		} else {
			cmd["status"] = "declined"
		}
		f.item("item/completed", cmd)
		f.message("msg-"+turn, "decision "+d)
	case "file":
		fc := map[string]any{"type": "fileChange", "id": "fc-1", "changes": []any{map[string]any{"path": filepath.Join(f.cwd, "a.txt"), "kind": map[string]any{"type": "add"}, "diff": "+hi\n"}}, "status": "inProgress"}
		f.item("item/started", fc)
		d := decision(f.answer("item/fileChange/requestApproval", map[string]any{"threadId": thread, "turnId": turn, "itemId": "fc-1", "reason": "write a.txt", "startedAtMs": 1}))
		fc["status"] = map[bool]string{true: "completed", false: "declined"}[d == "accept"]
		f.item("item/completed", fc)
		f.message("msg-"+turn, "decision "+d)
	case "permissions":
		f.answer("item/permissions/requestApproval", map[string]any{"threadId": thread, "turnId": turn, "itemId": "p-1", "cwd": f.cwd, "reason": "needs the network", "permissions": map[string]any{"network": map[string]any{"enabled": true}}, "startedAtMs": 1, "environmentId": nil})
		f.message("msg-"+turn, "asked")
	case "mcp-approval":
		call := map[string]any{"type": "mcpToolCall", "id": "call-1", "server": "glitched-browser", "tool": "navigate", "arguments": map[string]any{"url": "https://example.com"}, "status": "inProgress"}
		f.item("item/started", call)
		f.answer("item/tool/requestUserInput", map[string]any{"threadId": thread, "turnId": turn, "itemId": "call-1", "isBlocking": true, "autoResolutionMs": nil, "questions": []any{map[string]any{
			"id": "mcp_tool_call_approval_call-1", "header": "Approve app tool call?", "question": "Allow glitched-browser to run navigate?", "isOther": false, "isSecret": false,
			"options": []any{map[string]any{"label": "Allow", "description": "Run the tool and continue."}, map[string]any{"label": "Allow for this session", "description": "Remember."}, map[string]any{"label": "Cancel", "description": "Cancel this tool call."}}}}})
		call["status"] = "completed"
		f.item("item/completed", call)
		f.message("msg-"+turn, "used the tool")
	case "question", "question-unshowable":
		options := []any{map[string]any{"label": "Red", "description": "warm"}, map[string]any{"label": "Blue", "description": "cool"}}
		if f.scenario == "question-unshowable" {
			options = options[:1]
		}
		a := f.answer("item/tool/requestUserInput", map[string]any{"threadId": thread, "turnId": turn, "itemId": "q-1", "isBlocking": true, "autoResolutionMs": nil, "questions": []any{map[string]any{
			"id": "color", "header": "Color", "question": "Which color?", "isOther": true, "isSecret": false, "options": options}}})
		f.message("msg-"+turn, "answered "+string(a.Result))
	case "elicitation", "elicitation-fields":
		schema := map[string]any{"type": "object", "properties": map[string]any{}}
		if f.scenario == "elicitation-fields" {
			schema["properties"] = map[string]any{"name": map[string]any{"type": "string"}}
		}
		f.answer("mcpServer/elicitation/request", map[string]any{"threadId": thread, "turnId": turn, "serverName": "glitched-browser", "environmentId": nil, "mode": "form", "_meta": nil, "message": "Open the page?", "requestedSchema": schema})
		f.message("msg-"+turn, "elicited")
	case "unknown-request":
		f.answer("item/weird/request", map[string]any{"threadId": thread, "turnId": turn})
		f.message("msg-"+turn, "still here")
	case "resolved":
		id, _ := f.ask("item/commandExecution/requestApproval", map[string]any{"threadId": thread, "turnId": turn, "itemId": "cmd-9", "command": "make", "cwd": f.cwd, "startedAtMs": 1})
		time.Sleep(200 * time.Millisecond)
		f.notify("serverRequest/resolved", map[string]any{"threadId": thread, "requestId": json.RawMessage(id)})
		f.message("msg-"+turn, "decided elsewhere")
	case "hang", "steer":
		f.message("msg-"+turn+"-0", "working")
		select {
		case <-f.stop:
			f.complete("interrupted", nil)
			return
		case text := <-f.steers:
			f.item("item/completed", map[string]any{"type": "userMessage", "id": "user-2", "content": []any{map[string]any{"type": "text", "text": text}}})
			f.message("msg-"+turn, "steered: "+text)
		case <-time.After(10 * time.Second):
		}
	case "crash":
		f.message("msg-"+turn, "about to")
		fmt.Fprintln(os.Stderr, "boom: the fake crashed")
		os.Exit(3)
	case "turn-failed":
		f.notify("error", map[string]any{"threadId": thread, "turnId": turn, "willRetry": false, "error": map[string]any{"message": "You've hit your usage limit.", "codexErrorInfo": "usageLimitExceeded"}})
		f.complete("failed", map[string]any{"message": "You've hit your usage limit.", "codexErrorInfo": "usageLimitExceeded", "additionalDetails": nil})
		return
	case "unauthorized":
		f.complete("failed", map[string]any{"message": "401 Unauthorized", "codexErrorInfo": "unauthorized", "additionalDetails": nil})
		return
	default:
		f.message("msg-"+turn, "hi ", strconv.Itoa(n), " from ", strconv.Itoa(f.pid))
	}
	f.complete("completed", nil)
	if f.scenario == "late-speech" {
		time.Sleep(200 * time.Millisecond)
		f.item("item/completed", map[string]any{"type": "commandExecution", "id": "bg-1", "command": "sleep 100", "cwd": f.cwd, "status": "completed", "commandActions": []any{}})
	}
}

// replay answers a client as the app-server of a wire capture did.
type replay struct {
	lines []wireLine
	at    int
}

type wireLine struct {
	sent bool // the client sent it
	m    message
	raw  json.RawMessage
}

func loadReplay(path string) (*replay, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r := &replay{}
	for line := range strings.SplitSeq(string(b), "\n") {
		dir, body, ok := strings.Cut(line, " ")
		if !ok || dir != ">>" && dir != "<<" {
			continue
		}
		var m message
		if json.Unmarshal([]byte(body), &m) != nil {
			continue
		}
		r.lines = append(r.lines, wireLine{sent: dir == ">>", m: m, raw: json.RawMessage(body)})
	}
	return r, nil
}

// take finds the client's message in the capture, by its method or as an
// answer, and sends what app-server sent after it, up to the client's
// next message, with the capture's id for it swapped for the client's.
func (r *replay) take(f *fake, m message) {
	for i := r.at; i < len(r.lines); i++ {
		l := r.lines[i]
		if !l.sent || l.m.Method != m.Method {
			continue
		}
		recorded := string(l.m.ID)
		j := i + 1
		for ; j < len(r.lines) && !r.lines[j].sent; j++ {
			out := r.lines[j]
			if out.m.Method == "" && recorded != "" && string(out.m.ID) == recorded {
				var generic map[string]json.RawMessage
				json.Unmarshal(out.raw, &generic)
				generic["id"] = m.ID
				f.send(generic)
				continue
			}
			f.send(out.raw)
		}
		r.at = j
		return
	}
	if m.Method != "" && len(m.ID) > 0 {
		f.refuse(m.ID, -32601, "the capture has no "+m.Method+" here")
	}
}
