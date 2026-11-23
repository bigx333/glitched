// Package codextest is a fake Codex app-server for tests. A test binary
// plays it, as Main says, when an adapter starts the test binary as its
// codex; the scenario the environment names says what the fake does, and
// the fake writes what it received to a log the test reads.
package codextest

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"glitched/internal/mcp/mcptest"
	"glitched/internal/skills/skillstest"
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
	spent   int           // the thread's tokens so far
	stop    chan struct{} // takes an interrupt of the active turn
	steers  chan string
	parked  map[string]any // a usage reading sent while no turn ran
	// settings are what the fake says the thread runs at, as
	// thread/settings/updated says them.
	settings map[string]any

	replay *replay
	// extraRoots are the skill roots skills/extraRoots/set gave.
	extraRoots []string
	// glitched is the glitched MCP server thread/start configured.
	glitched glitchedServer
}

// glitchedServer is an mcp_servers entry, as Codex's config takes it.
type glitchedServer struct {
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	ToolTimeoutSec float64           `json:"tool_timeout_sec"`
}

// Second is how long one of Codex's seconds is in the fake, so that its
// 300 s default for a tool call can be met in a test: a duration, 1s unset.
const Second = "GLITCHED_FAKE_CODEX_SECOND"

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
	case "config/read":
		config := map[string]any{"model": nil, "model_reasoning_effort": nil}
		switch f.scenario {
		case "no-config-read":
			f.refuse(m.ID, -32601, "unsupported method config/read")
			return
		case "config-read-fails":
			f.refuse(m.ID, -32603, "config is being written")
			return
		case "configured":
			config = map[string]any{"model": "gpt-6-luna", "model_reasoning_effort": "low"}
		case "configured-unlisted":
			config = map[string]any{"model": "gpt-7-preview", "model_reasoning_effort": "xhigh"}
		}
		// Codex reads the config.toml of its CODEX_HOME, then a project's
		// .codex/config.toml where it's asked of a cwd: a model line there
		// is the config's.
		var p struct{ Cwd string }
		json.Unmarshal(m.Params, &p)
		files := []string{filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")}
		if p.Cwd != "" {
			files = append(files, filepath.Join(p.Cwd, ".codex", "config.toml"))
		}
		for _, file := range files {
			b, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			for line := range strings.Lines(string(b)) {
				if v, ok := strings.CutPrefix(strings.TrimSpace(line), "model = "); ok {
					config["model"] = strings.Trim(v, `"`)
				}
			}
		}
		f.reply(m.ID, map[string]any{"config": config})
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
				{"id": "gpt-6-luna", "model": "gpt-6-luna", "displayName": "GPT-6 Luna", "description": "Fast and cheap", "hidden": false, "isDefault": false, "defaultReasoningEffort": "medium", "supportedReasoningEfforts": effort("low", "medium")},
				{"id": "codex-auto-review", "model": "codex-auto-review", "displayName": "Auto review", "hidden": true, "isDefault": false},
			}, "nextCursor": "2"})
			return
		}
		f.reply(m.ID, map[string]any{"data": []map[string]any{
			{"id": "gpt-6.2-sol", "model": "gpt-6.2-sol", "displayName": "GPT-6.2 Sol", "description": "The default", "hidden": false, "isDefault": true, "defaultReasoningEffort": "medium", "supportedReasoningEfforts": effort("low", "medium", "high", "xhigh")},
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
		f.spent = 500
		f.mu.Unlock()
		f.reply(m.ID, f.reviewed(m.Params, map[string]any{"thread": map[string]any{"id": p.ThreadID}, "model": "gpt-6.2-sol"}))
	case "thread/start":
		var p struct {
			Config struct {
				Servers map[string]glitchedServer `json:"mcp_servers"`
			} `json:"config"`
		}
		json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.glitched = p.Config.Servers["glitched"]
		f.thread = fmt.Sprintf("019a0000-0000-7000-8000-%012d", f.pid)
		thread := f.thread
		os.WriteFile(f.rollout(thread), nil, 0o600)
		f.mu.Unlock()
		f.reply(m.ID, f.reviewed(m.Params, map[string]any{"thread": map[string]any{"id": thread}, "model": "gpt-6.2-sol"}))
		f.notify("thread/started", map[string]any{"thread": map[string]any{"id": thread}})
		f.notify("mcpServer/startupStatus/updated", map[string]any{"name": "glitched", "status": "starting"})
	case "skills/extraRoots/set":
		switch f.scenario {
		case "no-skills-call":
			f.refuse(m.ID, -32601, "unsupported method skills/extraRoots/set")
			return
		case "skills-refused":
			f.refuse(m.ID, -32602, "extraRoots must be absolute")
			return
		}
		var p struct{ ExtraRoots []string }
		json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.extraRoots = p.ExtraRoots
		f.mu.Unlock()
		f.reply(m.ID, map[string]any{})
	case "turn/start":
		var p struct {
			Input []struct{ Text string } `json:"input"`
		}
		json.Unmarshal(m.Params, &p)
		f.record("skills", f.skills())
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
		f.settle(m.Params)
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

// skills are the skills a turn's Codex lists, as Codex finds them: its
// own, the user's and the workspace's, by name, and those of the extra
// roots it was given as extra:<name>.
func (f *fake) skills() []string {
	home, _ := os.UserHomeDir()
	codexHome := cmp.Or(os.Getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
	seen := skillstest.Seen("", filepath.Join(home, ".agents", "skills"), filepath.Join(codexHome, "skills"), filepath.Join(f.cwd, ".agents", "skills"))
	f.mu.Lock()
	seen = append(seen, skillstest.Seen("extra:", f.extraRoots...)...)
	f.mu.Unlock()
	slices.Sort(seen)
	return seen
}

// reviewed is a thread's start or resume answer, with what the thread
// runs at: the reviewer of approvals asked, but the user's in place of
// auto-review for an account without it and guardian_subagent for
// "guardian", the approval policy and sandbox
// asked, the model asked, but gpt-6.2-sol for "substitute" and for none,
// and the effort of Codex's config. A Codex too old says none of it.
func (f *fake) reviewed(params json.RawMessage, answer map[string]any) map[string]any {
	var p struct{ ApprovalsReviewer, ApprovalPolicy, Sandbox, Model string }
	json.Unmarshal(params, &p)
	if f.scenario == "old-codex" {
		delete(answer, "model")
		return answer
	}
	reviewer := p.ApprovalsReviewer
	switch {
	case f.scenario == "guardian":
		reviewer = "guardian_subagent"
	case f.scenario == "no-auto" && reviewer == "auto_review":
		reviewer = "user"
	}
	model := p.Model
	if strings.HasPrefix(f.scenario, "substitute") || model == "" {
		model = "gpt-6.2-sol"
	}
	sandbox := map[string]string{"read-only": "readOnly", "workspace-write": "workspaceWrite", "danger-full-access": "dangerFullAccess"}[p.Sandbox]
	f.mu.Lock()
	f.settings = map[string]any{"approvalPolicy": p.ApprovalPolicy, "approvalsReviewer": reviewer, "sandboxPolicy": map[string]any{"type": sandbox}, "model": model, "effort": "xhigh"}
	f.mu.Unlock()
	answer["approvalsReviewer"], answer["approvalPolicy"], answer["sandbox"] = reviewer, p.ApprovalPolicy, map[string]any{"type": sandbox}
	answer["model"], answer["reasoningEffort"] = model, "xhigh"
	return answer
}

// settle takes what a turn/start asks the thread to run at, as Codex
// does, and says so with thread/settings/updated before it answers, when
// that changed anything. A Codex too old, or "silent", says nothing.
// "effort-null" says the effort is unset. "stale-settings" says, on its
// second turn, only an update of its first turn's and one of the
// approval policy alone.
func (f *fake) settle(params json.RawMessage) {
	if f.scenario == "old-codex" || strings.HasSuffix(f.scenario, "silent") {
		return
	}
	f.mu.Lock()
	thread, n := f.thread, f.turns
	f.mu.Unlock()
	if f.scenario == "stale-settings" && n == 2 {
		f.notify("thread/settings/updated", map[string]any{"threadId": thread, "turnId": "turn-1", "threadSettings": map[string]any{"model": "old-model", "effort": "low"}})
		f.notify("thread/settings/updated", map[string]any{"threadId": thread, "threadSettings": map[string]any{"approvalPolicy": "on-request"}})
		return
	}
	var p struct {
		ApprovalPolicy, ApprovalsReviewer, Model, Effort string
		SandboxPolicy                                    struct{ Type string }
	}
	json.Unmarshal(params, &p)
	f.mu.Lock()
	was := fmt.Sprint(f.settings)
	s := maps.Clone(f.settings)
	if p.ApprovalPolicy != "" {
		s["approvalPolicy"] = p.ApprovalPolicy
	}
	if p.ApprovalsReviewer != "" {
		s["approvalsReviewer"] = p.ApprovalsReviewer
	}
	if p.SandboxPolicy.Type != "" {
		s["sandboxPolicy"] = map[string]any{"type": p.SandboxPolicy.Type}
	}
	if p.Model != "" && f.scenario != "substitute" {
		s["model"] = p.Model
	}
	if p.Effort != "" {
		s["effort"] = p.Effort
	}
	if f.scenario == "effort-null" {
		s["effort"] = nil
	}
	f.settings = s
	f.mu.Unlock()
	if fmt.Sprint(s) != was {
		f.notify("thread/settings/updated", map[string]any{"threadId": thread, "threadSettings": s})
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

// usage is the thread's having spent spent tokens, its last reading the
// request of 100 that brought it to at.
func usage(spent, at int) map[string]any {
	last := map[string]any{"totalTokens": 100, "inputTokens": 80, "cachedInputTokens": at / 100, "outputTokens": 20, "reasoningOutputTokens": 0}
	total := map[string]any{"totalTokens": spent, "inputTokens": spent * 4 / 5, "cachedInputTokens": 0, "outputTokens": spent / 5, "reasoningOutputTokens": 0}
	return map[string]any{"total": total, "last": last, "modelContextWindow": 258400}
}

// reading is a usage update of total tokens spent, last of them by the
// last request.
func reading(total, last int) map[string]any {
	count := func(n int) map[string]any {
		return map[string]any{"totalTokens": n, "inputTokens": n * 2 / 3, "cachedInputTokens": 0, "outputTokens": n / 3, "reasoningOutputTokens": 0}
	}
	return map[string]any{"total": count(total), "last": count(last), "modelContextWindow": 258400}
}

func (f *fake) complete(status string, turnErr any) {
	thread, turn := f.ids()
	f.mu.Lock()
	f.active = false
	if f.scenario == "parked-usage" && f.parked != nil {
		f.notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": f.parked["tokenUsage"]})
	}
	if f.scenario == "usage-again" {
		f.spent += 40
		first := map[string]any{"totalTokens": f.spent, "inputTokens": 30, "cachedInputTokens": 0, "outputTokens": 10, "reasoningOutputTokens": 0}
		again := map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": map[string]any{"total": first, "last": map[string]any{"totalTokens": 40, "inputTokens": 30, "cachedInputTokens": 0, "outputTokens": 10, "reasoningOutputTokens": 0}, "modelContextWindow": 258400}}
		f.notify("thread/tokenUsage/updated", again)
		f.notify("thread/tokenUsage/updated", again)
	}
	f.spent += 100
	spent := f.spent
	f.mu.Unlock()
	f.notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": usage(spent, spent)})
	f.notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": status, "error": turnErr, "items": []any{}}})
	f.notify("thread/status/changed", map[string]any{"threadId": thread, "status": map[string]any{"type": "idle"}})
	if f.scenario == "parked-usage" {
		f.mu.Lock()
		f.spent += 40
		f.parked = map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": map[string]any{"total": map[string]any{"totalTokens": f.spent, "inputTokens": 30, "cachedInputTokens": 0, "outputTokens": 10, "reasoningOutputTokens": 0}, "last": map[string]any{"totalTokens": 40, "inputTokens": 30, "cachedInputTokens": 0, "outputTokens": 10, "reasoningOutputTokens": 0}, "modelContextWindow": 258400}}
		parked := f.parked
		f.mu.Unlock()
		go func() {
			time.Sleep(50 * time.Millisecond)
			f.notify("thread/tokenUsage/updated", parked)
		}()
	}
	if f.scenario == "usage-again" {
		late := map[string]any{"totalTokens": 7, "inputTokens": 0, "cachedInputTokens": 0, "outputTokens": 7, "reasoningOutputTokens": 0}
		f.notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": map[string]any{"total": map[string]any{"totalTokens": 1107}, "last": late, "modelContextWindow": 258400}})
	}
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
	case "credentials":
		// The agent asks for GLX_PROBE through glitched mcp, giving up
		// at its tool timeout as Codex does (300 s unless configured), then
		// echoes the value it read, split across two deltas a flush apart.
		f.mu.Lock()
		srv := f.glitched
		f.mu.Unlock()
		f.record("tool_timeout_sec", srv.ToolTimeoutSec)
		second, err := time.ParseDuration(os.Getenv(Second))
		if err != nil {
			second = time.Second
		}
		timeout := cmp.Or(srv.ToolTimeoutSec, 300)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout*float64(second)))
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, Scenario+"=") {
				env = append(env, kv)
			}
		}
		for k, v := range srv.Env {
			env = append(env, k+"="+v)
		}
		result, err := mcptest.RequestCredentials(ctx, srv.Command, srv.Args, env)
		cancel()
		if err != nil {
			f.message("msg-"+turn, "The ask failed: "+err.Error())
			break
		}
		var r struct {
			Status string
			Files  map[string]string
		}
		json.Unmarshal(result, &r)
		v, _ := os.ReadFile(r.Files["GLX_PROBE"])
		id := "msg-" + turn
		f.item("item/started", map[string]any{"type": "agentMessage", "id": id, "text": ""})
		f.notify("item/agentMessage/delta", map[string]any{"threadId": thread, "turnId": turn, "itemId": id, "delta": r.Status + ": the key is " + string(v[:len(v)/2])})
		time.Sleep(150 * time.Millisecond)
		f.notify("item/agentMessage/delta", map[string]any{"threadId": thread, "turnId": turn, "itemId": id, "delta": string(v[len(v)/2:]) + "."})
		f.item("item/completed", map[string]any{"type": "agentMessage", "id": id, "text": r.Status + ": the key is " + string(v) + ".", "phase": "final_answer"})
	case "two-messages":
		f.message("msg-a", "first")
		f.message("msg-b", "second")
	case "whole":
		f.item("item/completed", map[string]any{"type": "agentMessage", "id": "msg-w", "text": "all at once"})
	case "plan":
		// update_plan, as Codex 0.171 reports it: the whole plan each
		// time, and the plan's own item, which is no step.
		step := func(text, status string) map[string]any { return map[string]any{"step": text, "status": status} }
		f.notify("turn/plan/updated", map[string]any{"threadId": thread, "turnId": turn, "explanation": nil,
			"plan": []any{step("Read the code", "inProgress"), step("Fix the bug", "pending")}})
		f.item("item/started", map[string]any{"type": "plan", "id": "plan-1", "text": ""})
		f.item("item/completed", map[string]any{"type": "plan", "id": "plan-1", "text": "- Read the code\n- Fix the bug"})
		f.notify("turn/plan/updated", map[string]any{"threadId": thread, "turnId": turn, "explanation": "read it",
			"plan": []any{step("Read the code", "completed"), step("Fix the bug", "inProgress")}})
		f.message("msg-"+turn, "planned")
	case "overflow":
		f.mu.Lock()
		f.spent = 258400
		f.mu.Unlock()
		full := map[string]any{"totalTokens": 258400, "inputTokens": 0, "cachedInputTokens": 0, "outputTokens": 0, "reasoningOutputTokens": 0}
		delta := map[string]any{"totalTokens": 13400, "inputTokens": 0, "cachedInputTokens": 0, "outputTokens": 0, "reasoningOutputTokens": 0}
		f.notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": map[string]any{"total": full, "last": delta, "modelContextWindow": 258400}})
	case "cut-short":
		f.mu.Lock()
		f.spent = 90
		f.mu.Unlock()
		f.notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": reading(60, 60)})
		f.notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": reading(90, 60)})
		f.message("msg-"+turn, "cut short")
	case "old-repeat", "old-repeat-reset":
		if n == 2 {
			f.mu.Lock()
			f.spent = 170
			f.mu.Unlock()
			now := reading(170, 70)
			if f.scenario == "old-repeat-reset" {
				f.notify("thread/compacted", map[string]any{"threadId": thread, "turnId": turn})
				f.mu.Lock()
				f.spent = 20
				f.mu.Unlock()
				now = reading(20, 20)
			}
			f.notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": now})
			f.notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": "turn-1", "tokenUsage": usage(100, 100)})
		}
		f.message("msg-"+turn, "old repeat")
	case "resent":
		if n == 2 {
			f.mu.Lock()
			spent := f.spent
			f.spent += 100
			f.mu.Unlock()
			f.notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": usage(spent, spent)})
			f.notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": usage(spent+100, spent)})
		}
		f.message("msg-"+turn, "resent")
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
	case "switch":
		// Codex moves the thread to another model mid-turn.
		f.message("msg-a", "on the first model")
		f.mu.Lock()
		s := maps.Clone(f.settings)
		s["model"], s["effort"] = "gpt-6.2-sol", "high"
		f.settings = s
		f.mu.Unlock()
		f.notify("thread/settings/updated", map[string]any{"threadId": thread, "threadSettings": s})
		f.message("msg-b", "on the second")
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
