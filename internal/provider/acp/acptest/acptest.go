// Package acptest is a fake ACP agent for tests. A test binary plays it,
// as Main says, when an adapter starts the test binary as its agent; the
// scenario the environment names says what the fake does, and the fake
// writes what it received to a log the test reads.
package acptest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"glitched/internal/mcp/mcptest"
)

const (
	// Scenario names, in the fake's environment, what it does.
	Scenario = "GLITCHED_FAKE_ACP"
	// Log names the file the fake appends what it received to.
	Log = "GLITCHED_FAKE_ACP_LOG"
	// Offered, where set, is the modes a session lists, by comma, in
	// place of Modes; "none" lists none.
	Offered      = "GLITCHED_FAKE_ACP_MODES"
	DefaultModel = "GLITCHED_FAKE_ACP_DEFAULT_MODEL"
	// Replay names, for the scenario "replay", a wire capture the fake
	// answers from as the agent that made it did: lines ">> " the client
	// sent and "<< " the agent did, a JSON-RPC message each.
	Replay = "GLITCHED_FAKE_ACP_REPLAY"
	// Gate names, for the "thinking" scenarios, a file the fake waits to
	// exist before it says the turn's next thing.
	Gate = "GLITCHED_FAKE_ACP_GATE"
)

// Entry is a line of the fake's log: a method it was called with and its
// params, or something it saw, as the answer to a permission request.
type Entry struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	PID    int             `json:"pid"`
	// Argv, on initialize, is what the fake was started with.
	Argv []string `json:"argv,omitempty"`
}

// Main plays the fake agent, until its stdin closes, when the environment
// names a scenario, and reports whether it did. A test binary calls it
// first thing in TestMain and returns when it is true.
func Main() bool {
	scenario := os.Getenv(Scenario)
	if scenario == "" {
		return false
	}
	if scenario == "sleeper" {
		time.Sleep(time.Hour)
		return true
	}
	f := &fake{scenario: scenario, mode: "default"}
	if f.is("hang-on-initialize") {
		f.record("started", nil)
	}
	f.conn = acp.NewAgentSideConnection(f, os.Stdout, os.Stdin)
	<-f.conn.Done()
	f.record("stdin closed", nil)
	return true
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

type fake struct {
	conn     *acp.AgentSideConnection
	scenario string
	prompts  int // the prompts so far, for a replay
	mu       sync.Mutex
	model    string
	level    string
	mode     string
	sessions int
	forms    bool // the client takes elicitation forms
	turns    int
	servers  []acp.McpServer // the last session's
}

var _ acp.Agent = (*fake)(nil)

func (f *fake) is(s string) bool { return f.scenario == s }

func (f *fake) record(method string, params any) {
	path := os.Getenv(Log)
	if path == "" {
		return
	}
	raw, _ := json.Marshal(params)
	if params == nil {
		raw = nil
	}
	e := Entry{Method: method, Params: raw, PID: os.Getpid()}
	if method == "initialize" {
		e.Argv = os.Args[1:]
	}
	line, _ := json.Marshal(e)
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer fh.Close()
	fh.Write(append(line, '\n'))
}

// message is a line of a wire capture.
type message struct {
	out    bool // the client sent it
	ID     json.RawMessage
	Method string
	Params json.RawMessage
	Result json.RawMessage
}

func capture() []message {
	b, err := os.ReadFile(os.Getenv(Replay))
	if err != nil {
		panic(err)
	}
	var out []message
	for line := range bytes.SplitSeq(bytes.TrimSpace(b), []byte("\n")) {
		m := message{out: bytes.HasPrefix(line, []byte(">> "))}
		if err := json.Unmarshal(line[3:], &m); err != nil {
			panic(err)
		}
		out = append(out, m)
	}
	return out
}

// recorded is the agent's answer, in the capture, to the nth request the
// client sent with method, from the first; and what the agent sent
// between the two.
func recorded[T any](method string, n int) (T, []message) {
	var result T
	wire := capture()
	for i, m := range wire {
		if !m.out || m.Method != method {
			continue
		}
		if n--; n >= 0 {
			continue
		}
		for j, r := range wire[i+1:] {
			if !r.out && r.Method == "" && bytes.Equal(r.ID, m.ID) {
				if err := json.Unmarshal(r.Result, &result); err != nil {
					panic(err)
				}
				return result, wire[i+1 : i+1+j]
			}
		}
	}
	panic(fmt.Sprintf("acptest: no answer to %s #%d in the capture", method, n))
}

// replay sends what the agent sent during a recorded prompt, in its
// order, and answers it as the agent did.
func (f *fake) replay(ctx context.Context, sid acp.SessionId) (acp.PromptResponse, error) {
	r, between := recorded[acp.PromptResponse]("session/prompt", f.prompts)
	f.prompts++
	for _, m := range between {
		switch {
		case m.out:
		case m.Method == "session/update":
			var n acp.SessionNotification
			if err := json.Unmarshal(m.Params, &n); err != nil {
				return r, err
			}
			f.send(ctx, sid, n.Update)
		case m.Method == "session/request_permission":
			var p acp.RequestPermissionRequest
			if err := json.Unmarshal(m.Params, &p); err != nil {
				return r, err
			}
			f.ask(ctx, sid, p.ToolCall, p.Options)
		}
	}
	return r, nil
}

func (f *fake) Initialize(ctx context.Context, p acp.InitializeRequest) (acp.InitializeResponse, error) {
	f.record("initialize", p)
	f.forms = p.ClientCapabilities.Elicitation != nil && p.ClientCapabilities.Elicitation.Form != nil
	if f.is("replay") {
		r, _ := recorded[acp.InitializeResponse]("initialize", 0)
		return r, nil
	}
	if f.is("hang-on-initialize") {
		<-ctx.Done()
		select {}
	}
	caps := acp.AgentCapabilities{SessionCapabilities: acp.SessionCapabilities{Close: &acp.SessionCloseCapabilities{}}}
	switch f.scenario {
	case "memory", "memory-reject-model", "memory-resume-fails", "memory-load-metadata", "compact", "compact-no-cmd", "compact-agy", "compact-replies", "compact-unlisted", "compact-failed":
		caps.LoadSession = true
		caps.SessionCapabilities.Resume = &acp.SessionResumeCapabilities{}
	case "memory-load-only":
		caps.LoadSession = true
	}
	return acp.InitializeResponse{
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentInfo:         &acp.Implementation{Name: "Fake ACP Agent", Version: "9.9.9"},
		AgentCapabilities: caps,
		AuthMethods:       []acp.AuthMethod{},
	}, nil
}

// The memory scenarios keep each session's prompts in a file beside the
// log, so another process of the fake can take the session up again.
func (f *fake) remembers() bool {
	return strings.HasPrefix(f.scenario, "memory") || strings.HasPrefix(f.scenario, "compact")
}

func memory() map[acp.SessionId][]string {
	m := map[acp.SessionId][]string{}
	if b, err := os.ReadFile(os.Getenv(Log) + ".sessions"); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func remember(m map[acp.SessionId][]string) {
	b, _ := json.Marshal(m)
	os.WriteFile(os.Getenv(Log)+".sessions", b, 0o600)
}

// takeUp answers a resume or load of id: the session's options when the
// fake has it. As agy 1.5.14 does, it fails a resume of a session it
// doesn't have with -32603, and loads one as an empty session that it
// keeps nowhere: a later resume of it fails again.
func (f *fake) takeUp(id acp.SessionId, load bool) ([]acp.SessionConfigOption, []string, error) {
	m := memory()
	said, ok := m[id]
	if !ok && !load {
		return nil, nil, acp.NewInternalError(map[string]any{"message": "Failed to read session " + string(id) + "; retry, or load it with session/load"})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.model, f.level = defaultModel(), "high"
	return f.options(), said, nil
}

// Models are the fake's: Pro runs at two efforts, Flash at none.
var Models = []struct{ ID, Name string }{{"fake-pro", "Fake Pro"}, {"fake-flash", "Fake Flash"}}

func defaultModel() string {
	if m := os.Getenv(DefaultModel); m != "" {
		return m
	}
	return Models[0].ID
}

func (f *fake) options() []acp.SessionConfigOption {
	model := acp.SessionConfigOptionCategoryModel
	level := acp.SessionConfigOptionCategoryThoughtLevel
	var models acp.SessionConfigSelectOptionsUngrouped
	for _, m := range Models {
		models = append(models, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(m.ID), Name: m.Name})
	}
	if m := os.Getenv(DefaultModel); m != "" {
		models = append(models, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(m), Name: m})
	}
	out := []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id: "model", Name: "Model", Category: &model, Type: "select",
		CurrentValue: acp.SessionConfigValueId(f.model), Options: acp.SessionConfigSelectOptions{Ungrouped: &models},
	}}}
	if f.model == "fake-pro" {
		levels := acp.SessionConfigSelectOptionsUngrouped{{Value: "low", Name: "Low"}, {Value: "high", Name: "High"}}
		out = append(out, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
			Id: "thought_level", Name: "Thinking Level", Category: &level, Type: "select",
			CurrentValue: acp.SessionConfigValueId(f.level), Options: acp.SessionConfigSelectOptions{Ungrouped: &levels},
		}})
	}
	return out
}

func (f *fake) NewSession(ctx context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	f.record("session/new", p)
	switch {
	case f.is("signed-out"):
		return acp.NewSessionResponse{}, acp.NewAuthRequired(nil)
	case f.is("stale-login-line"):
		fmt.Fprintln(os.Stdout, "Your login expired. Please sign in again: https://accounts.example/auth?code=SECRET")
		return acp.NewSessionResponse{}, acp.NewInternalError(map[string]any{"error": "no credentials"})
	}
	if f.is("replay") {
		r, _ := recorded[acp.NewSessionResponse]("session/new", 0)
		return r, nil
	}
	f.mu.Lock()
	f.sessions++
	id := acp.SessionId("session-" + strconv.Itoa(f.sessions))
	if f.remembers() {
		id = acp.SessionId(fmt.Sprintf("session-%d-%d", os.Getpid(), f.sessions))
	}
	f.model, f.level = defaultModel(), "high"
	f.servers = p.McpServers
	options := f.options()
	f.mu.Unlock()
	if f.is("silent") {
		return acp.NewSessionResponse{SessionId: id}, nil
	}
	if f.remembers() {
		m := memory()
		m[id] = []string{}
		remember(m)
	}
	if f.is("updates-before-new-response") {
		f.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: id, Update: acp.UpdateAgentMessageText("history the run must not show")})
		f.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: id, Update: acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{AvailableCommands: []acp.AvailableCommand{}}}})
	}
	f.sendCommands(ctx, id)
	return acp.NewSessionResponse{SessionId: id, ConfigOptions: options, Modes: f.modes()}, nil
}

func (f *fake) sendCommands(ctx context.Context, id acp.SessionId) {
	switch f.scenario {
	case "compact", "compact-failed":
		f.send(ctx, id, acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
			AvailableCommands: []acp.AvailableCommand{{Name: "compact", Description: "Compact conversation"}},
		}})
	case "compact-no-cmd":
		f.send(ctx, id, acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
			AvailableCommands: []acp.AvailableCommand{{Name: "help", Description: "Show help"}},
		}})
	case "compact-agy", "compact-replies":
		// agy 1.5.15 lists its commands only after it answered the
		// session's resume or creation.
		go func() {
			if gate := os.Getenv(Gate); gate != "" {
				for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
					if _, err := os.Stat(gate); err == nil {
						break
					}
				}
			} else {
				time.Sleep(200 * time.Millisecond)
			}
			f.send(context.Background(), id, acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
				AvailableCommands: []acp.AvailableCommand{{Name: "goal"}, {Name: "compact", Description: "Compact the conversation now"}},
			}})
		}()
	}
}

// Modes are agy's, as `session/new` lists them.
var Modes = []string{"default", "accept-edits", "auto", "plan"}

func (f *fake) modes() *acp.SessionModeState {
	f.mu.Lock()
	defer f.mu.Unlock()
	offered := Modes
	switch v := os.Getenv(Offered); v {
	case "":
	case "none":
		return nil
	default:
		offered = strings.Split(v, ",")
	}
	s := &acp.SessionModeState{CurrentModeId: acp.SessionModeId(f.mode), AvailableModes: []acp.SessionMode{}}
	for _, m := range offered {
		s.AvailableModes = append(s.AvailableModes, acp.SessionMode{Id: acp.SessionModeId(m), Name: m})
	}
	return s
}

func (f *fake) SetSessionConfigOption(ctx context.Context, p acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	f.record("session/set_config_option", p)
	v := p.ValueId
	if v == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if v.ConfigId == "model" {
		if f.is("reject-model") || f.is("memory-reject-model") {
			return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams("Model not available on your plan")
		}
		if !f.is("substitute-model") {
			f.model = string(v.Value)
		}
	}
	if v.ConfigId == "thought_level" {
		f.level = string(v.Value)
	}
	// As agy does, the fake tells of the change before it answers.
	options := f.options()
	f.send(ctx, v.SessionId, acp.SessionUpdate{ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{ConfigOptions: options}})
	return acp.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
}

func (f *fake) send(ctx context.Context, sid acp.SessionId, u acp.SessionUpdate) {
	f.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: u})
}

func (f *fake) ask(ctx context.Context, sid acp.SessionId, call acp.ToolCallUpdate, options []acp.PermissionOption) string {
	r, err := f.conn.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: sid, ToolCall: call, Options: options})
	answer := "error"
	switch {
	case err != nil:
	case r.Outcome.Cancelled != nil:
		answer = "cancelled"
	case r.Outcome.Selected != nil:
		answer = string(r.Outcome.Selected.OptionId)
	}
	f.record("permission answered", answer)
	return answer
}

// allOptions are what agy offers for a tool call: once, always, or no.
var allOptions = []acp.PermissionOption{
	{OptionId: "allow-once", Name: "Allow", Kind: acp.PermissionOptionKindAllowOnce},
	{OptionId: "allow-always", Name: "Always allow", Kind: acp.PermissionOptionKindAllowAlways},
	{OptionId: "reject-once", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
}

func (f *fake) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	f.record("session/prompt", p)
	sid := p.SessionId
	end := acp.PromptResponse{StopReason: acp.StopReasonEndTurn}
	switch {
	case f.is("compact-failed") && len(p.Prompt) == 1 && p.Prompt[0].Text != nil && p.Prompt[0].Text.Text == "/compact":
		f.send(ctx, sid, acp.SessionUpdate{CompactionUpdate: &acp.SessionCompactionUpdate{CompactionId: "cmp-1", Status: acp.CompactionStatusFailed}})
		return end, nil
	case f.is("compact") && len(p.Prompt) == 1 && p.Prompt[0].Text != nil && p.Prompt[0].Text.Text == "/compact":
		f.send(ctx, sid, acp.SessionUpdate{CompactionUpdate: &acp.SessionCompactionUpdate{CompactionId: "cmp-1", Status: acp.CompactionStatusInProgress}})
		f.send(ctx, sid, acp.SessionUpdate{CompactionSummaryChunk: &acp.SessionUpdateCompactionSummaryChunk{CompactionId: "cmp-1", Content: acp.TextBlock("Summary")}})
		f.send(ctx, sid, acp.SessionUpdate{CompactionUpdate: &acp.SessionCompactionUpdate{CompactionId: "cmp-1", Status: acp.CompactionStatusCompleted}})
		return end, nil
	case f.is("compact-agy") && len(p.Prompt) == 1 && p.Prompt[0].Text != nil && p.Prompt[0].Text.Text == "/compact":
		// agy 1.5.15 compacts with no compaction update and no text: only
		// a usage update, of the request that wrote the summary.
		f.send(ctx, sid, acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{Used: 250000, Size: 1048576}})
		return end, nil
	case f.is("compact-replies") && len(p.Prompt) == 1 && p.Prompt[0].Text != nil && p.Prompt[0].Text.Text == "/compact":
		f.send(ctx, sid, acp.UpdateAgentMessageText("I can't compact this."))
		return end, nil
	case f.remembers():
		var said []string
		for _, b := range p.Prompt {
			if b.Text != nil {
				said = append(said, b.Text.Text)
			}
		}
		m := memory()
		earlier := m[sid]
		m[sid] = append(earlier, strings.Join(said, " "))
		remember(m)
		f.send(ctx, sid, acp.UpdateAgentMessageText(fmt.Sprintf("Earlier: %q", earlier)))
		return end, nil
	}
	switch f.scenario {
	case "replay":
		return f.replay(ctx, sid)
	case "tools":
		f.send(ctx, sid, acp.StartToolCall("read-1", "Read main.go", acp.WithStartKind(acp.ToolKindRead), acp.WithStartStatus(acp.ToolCallStatusPending),
			acp.WithStartLocations([]acp.ToolCallLocation{{Path: cwdOf(p) + "/main.go"}})))
		f.send(ctx, sid, acp.UpdateToolCall("read-1", acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
			acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock("package main"))})))
		f.send(ctx, sid, acp.StartToolCall("think-1", "Thinking", acp.WithStartKind(acp.ToolKindThink)))
		f.send(ctx, sid, acp.StartToolCall("run-1", "ls", acp.WithStartKind(acp.ToolKindExecute), acp.WithStartStatus(acp.ToolCallStatusInProgress),
			acp.WithStartRawInput(map[string]any{"command": "ls"})))
		f.send(ctx, sid, acp.UpdateToolCall("run-1", acp.WithUpdateStatus(acp.ToolCallStatusFailed), acp.WithUpdateRawOutput("ls: no such file")))
		f.send(ctx, sid, acp.UpdateAgentMessageText("Read it."))
	case "plan":
		// session/update's plan: the whole plan, each time.
		entry := func(text string, status acp.PlanEntryStatus) acp.PlanEntry {
			return acp.PlanEntry{Content: text, Priority: acp.PlanEntryPriorityHigh, Status: status}
		}
		f.send(ctx, sid, acp.SessionUpdate{Plan: &acp.SessionUpdatePlan{Entries: []acp.PlanEntry{
			entry("Read the code", acp.PlanEntryStatusInProgress), entry("Fix the bug", acp.PlanEntryStatusPending)}}})
		f.send(ctx, sid, acp.SessionUpdate{Plan: &acp.SessionUpdatePlan{Entries: []acp.PlanEntry{
			entry("Read the code", acp.PlanEntryStatusCompleted), entry("Fix the bug", acp.PlanEntryStatusInProgress)}}})
		f.send(ctx, sid, acp.UpdateAgentMessageText("Planned."))
	case "permission", "permission-no-allow-once", "permission-once", "permission-glitched", "gui", "gui-once":
		if f.is("gui") || f.is("gui-once") {
			f.send(ctx, sid, acp.UpdateAgentMessageText("I'll read the notes, then add a line to them.\n\n"))
			f.send(ctx, sid, acp.StartToolCall("read-1", "Read notes.txt", acp.WithStartKind(acp.ToolKindRead), acp.WithStartStatus(acp.ToolCallStatusCompleted),
				acp.WithStartLocations([]acp.ToolCallLocation{{Path: cwdOf(p) + "/notes.txt"}})))
		}
		options := allOptions
		switch f.scenario {
		case "permission-no-allow-once":
			options = allOptions[1:]
		case "permission-once", "gui-once":
			options = []acp.PermissionOption{allOptions[0], allOptions[2]}
		}
		call := acp.ToolCallUpdate{ToolCallId: "edit-1", Title: acp.PatchSet("Edit notes.txt"), Kind: acp.PatchSet(acp.ToolKindEdit),
			Locations: acp.PatchSet([]acp.ToolCallLocation{{Path: cwdOf(p) + "/notes.txt"}}), RawInput: acp.PatchSet(json.RawMessage(`{"path":"notes.txt","content":"hi"}`))}
		if f.is("permission-glitched") {
			call = acp.ToolCallUpdate{ToolCallId: "mcp-1", Title: acp.PatchSet("delegate_task"), Kind: acp.PatchSet(acp.ToolKindOther),
				RawInput: acp.PatchSet(json.RawMessage(`{"serverName":"glitched","toolName":"delegate_task","args":{}}`))}
		}
		f.send(ctx, sid, acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{ToolCallId: call.ToolCallId, Title: "Edit notes.txt", Kind: acp.ToolKindEdit, Status: acp.ToolCallStatusPending}})
		answer := f.ask(ctx, sid, call, options)
		status := map[bool]acp.ToolCallStatus{true: acp.ToolCallStatusCompleted, false: acp.ToolCallStatusFailed}[answer == "allow-once" || answer == "allow-always"]
		f.send(ctx, sid, acp.UpdateToolCall(call.ToolCallId, acp.WithUpdateStatus(status)))
		f.send(ctx, sid, acp.UpdateAgentMessageText("Answer: "+answer))
	case "permission-overlap":
		call := acp.ToolCallUpdate{ToolCallId: "run-1", Title: acp.PatchSet("rm -rf build"), Kind: acp.PatchSet(acp.ToolKindExecute),
			RawInput: acp.PatchSet(json.RawMessage(`{"command":["rm","-rf","build"]}`))}
		var wg sync.WaitGroup
		answers := make([]string, 2)
		for i := range answers {
			wg.Go(func() { answers[i] = f.ask(ctx, sid, call, allOptions) })
		}
		wg.Wait()
		f.send(ctx, sid, acp.UpdateAgentMessageText("Answers: "+answers[0]+", "+answers[1]))
	case "cancel", "slow-prompt", "cancel-with-pending-permission":
		f.send(ctx, sid, acp.UpdateAgentMessageText("Working"))
		if f.is("cancel-with-pending-permission") {
			call := acp.ToolCallUpdate{ToolCallId: "run-1", Title: acp.PatchSet("make"), Kind: acp.PatchSet(acp.ToolKindExecute)}
			// The client answers it cancelled, whatever the agent's own
			// context does once the cancel comes.
			f.ask(context.WithoutCancel(ctx), sid, call, allOptions)
		}
		if f.is("slow-prompt") {
			time.Sleep(time.Hour)
		}
		<-ctx.Done()
		f.record("cancelled", nil)
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	case "thinking":
		f.send(ctx, sid, acp.UpdateAgentThoughtText("The user wants a plan."))
		if !gate(ctx) {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		f.send(ctx, sid, acp.UpdateAgentMessageText("Here is the plan."))
	case "thinking-silent":
		if !gate(ctx) {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		f.send(ctx, sid, acp.UpdateAgentMessageText("Here is the plan."))
	case "thinking-tools":
		f.send(ctx, sid, acp.UpdateAgentThoughtText("First the notes."))
		f.send(ctx, sid, acp.StartToolCall("read-1", "Read notes.txt", acp.WithStartKind(acp.ToolKindRead), acp.WithStartStatus(acp.ToolCallStatusCompleted)))
		f.send(ctx, sid, acp.UpdateAgentThoughtText("Then the code."))
		if !gate(ctx) {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		f.send(ctx, sid, acp.StartToolCall("read-2", "Read main.go", acp.WithStartKind(acp.ToolKindRead), acp.WithStartStatus(acp.ToolCallStatusCompleted)))
		f.send(ctx, sid, acp.UpdateAgentMessageText("Read both."))
	case "question", "question-multi", "question-two":
		return f.question(ctx, sid)
	case "crash-mid-turn":
		f.send(ctx, sid, acp.UpdateAgentMessageText("Starting"))
		fmt.Fprintln(os.Stderr, "fake agent: out of memory")
		os.Exit(3)
	case "broken-pipe":
		f.send(ctx, sid, acp.UpdateAgentMessageText("Starting"))
		os.Stdout.Close()
		time.Sleep(time.Hour)
	case "child-process-survives":
		self, _ := os.Executable()
		cmd := exec.Command(self)
		cmd.Env = append(os.Environ(), Scenario+"=sleeper")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err == nil {
			f.record("child", cmd.Process.Pid)
		}
		f.send(ctx, sid, acp.UpdateAgentMessageText("Started a child."))
	case "switch-mid-turn":
		// As though the user moved agy's session on in agy itself.
		f.mu.Lock()
		f.mode, f.model = "plan", Models[1].ID
		options := f.options()
		f.mu.Unlock()
		f.send(ctx, sid, acp.UpdateAgentMessageText("Before."))
		f.send(ctx, sid, acp.SessionUpdate{CurrentModeUpdate: &acp.SessionCurrentModeUpdate{CurrentModeId: "plan"}})
		f.send(ctx, sid, acp.SessionUpdate{ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{ConfigOptions: options}})
		f.send(ctx, sid, acp.UpdateAgentMessageText("After."))
	case "usage":
		// What the session holds, which is no turn's use, then the turn's.
		f.mu.Lock()
		f.turns++
		n := int64(f.turns)
		f.mu.Unlock()
		f.send(ctx, sid, acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{Used: 999999, Size: 1048576}})
		f.send(ctx, sid, acp.UpdateAgentMessageText("Counted."))
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn, Usage: &acp.Usage{InputTokens: 70 * n, OutputTokens: 30 * n, TotalTokens: 100 * n}}, nil
	case "credentials":
		// The agent asks for GLX_PROBE through glitched mcp, then echoes
		// the value it read, split across two messages a flush apart.
		result, err := f.askCredentials()
		f.record("credentials", result)
		if err != nil {
			f.send(ctx, sid, acp.UpdateAgentMessageText("The ask failed: "+err.Error()))
			break
		}
		var r struct {
			Status string
			Files  map[string]string
		}
		json.Unmarshal(result, &r)
		v, _ := os.ReadFile(r.Files["GLX_PROBE"])
		f.send(ctx, sid, acp.UpdateAgentMessageText(r.Status+": the key is "+string(v[:len(v)/2])))
		time.Sleep(150 * time.Millisecond)
		f.send(ctx, sid, acp.UpdateAgentMessageText(string(v[len(v)/2:])+"."))
	case "speaks-while-parked":
		f.send(ctx, sid, acp.UpdateAgentMessageText("Done."))
		go func() {
			time.Sleep(200 * time.Millisecond)
			f.send(context.Background(), sid, acp.UpdateAgentMessageText("I kept going on my own."))
		}()
	default:
		f.send(ctx, sid, acp.UpdateAgentThoughtText("The user said hi."))
		f.send(ctx, sid, acp.UpdateAgentMessageText("Hi "))
		f.send(ctx, sid, acp.UpdateAgentMessageText("there."))
	}
	return end, nil
}

// gate waits for the file Gate names to exist, and says false if the
// turn was cancelled first.
func gate(ctx context.Context) bool {
	for {
		if _, err := os.Stat(os.Getenv(Gate)); err == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// agyQuestion is the form agy 1.5.14 asks its ask_question tool's one
// question with, agyMulti a multi-select one, and agyTwo two questions,
// one of each, as it sent them live.
const (
	agyQuestion = `{"mode":"form","message":"The agent has a question.","sessionId":"SID","toolCallId":"call-ask-1","requestedSchema":{"type":"object","properties":{"q1":{"type":"string","title":"What is your favourite colour?","oneOf":[{"const":"1","title":"(Recommended) Blue"},{"const":"2","title":"Green"},{"const":"3","title":"Red"},{"const":"4","title":"Yellow"},{"const":"5","title":"Purple"}]},"q1_other":{"type":"string","title":"Other","description":"Optional free-form answer to: What is your favourite colour?"}}}}`
	agyTwo      = `{"mode":"form","message":"The agent has a question.","sessionId":"SID","toolCallId":"call-ask-1","requestedSchema":{"type":"object","properties":{"q1":{"type":"string","title":"What is your favourite colour?","oneOf":[{"const":"1","title":"(Recommended) Blue"},{"const":"2","title":"Red"},{"const":"3","title":"Green"},{"const":"4","title":"Yellow"}]},"q1_other":{"type":"string","title":"Other","description":"Optional free-form answer to: What is your favourite colour?"},"q2":{"type":"array","title":"Which fruits do you like?","items":{"anyOf":[{"const":"1","title":"(Recommended) Apple"},{"const":"2","title":"Banana"},{"const":"3","title":"Orange"},{"const":"4","title":"Strawberry"}]}},"q2_other":{"type":"string","title":"Other","description":"Optional free-form answer to: Which fruits do you like?"}}}}`
	agyMulti    = `{"mode":"form","message":"The agent has a question.","sessionId":"SID","toolCallId":"call-ask-1","requestedSchema":{"type":"object","properties":{"q1":{"type":"array","title":"Which suites gate the merge?","items":{"anyOf":[{"const":"1","title":"Unit"},{"const":"2","title":"Race"},{"const":"3","title":"GUI"}]}},"q1_other":{"type":"string","title":"Other"}}}}`
)

// question asks as agy does: its ask_question tool's step, and the form
// when the client takes forms, else the question printed in the reply.
// It says the answer it got.
func (f *fake) question(ctx context.Context, sid acp.SessionId) (acp.PromptResponse, error) {
	const call = "call-ask-1"
	f.send(ctx, sid, acp.StartToolCall(call, "Run ask_question", acp.WithStartKind(acp.ToolKindOther), acp.WithStartStatus(acp.ToolCallStatusInProgress)))
	if !f.forms {
		f.send(ctx, sid, acp.UpdateToolCall(call, acp.WithUpdateStatus(acp.ToolCallStatusPending)))
		f.send(ctx, sid, acp.UpdateAgentMessageText("\nWhat is your favourite colour?\n- [1] (Recommended) Blue\n- [2] Green\n"))
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
	shape := agyQuestion
	switch {
	case f.is("question-multi"):
		shape = agyMulti
	case f.is("question-two"):
		shape = agyTwo
	}
	var req acp.CreateElicitationRequest
	if err := json.Unmarshal([]byte(strings.Replace(shape, "SID", string(sid), 1)), &req); err != nil {
		panic(err)
	}
	// What the client answers, whatever the agent's own context does once
	// a cancel comes.
	r, err := f.conn.CreateElicitation(context.WithoutCancel(ctx), req)
	if err != nil {
		f.record("elicitation error", err.Error())
		return acp.PromptResponse{}, err
	}
	f.record("elicitation/create answer", r)
	b, _ := json.Marshal(r)
	f.send(ctx, sid, acp.UpdateToolCall(call, acp.WithUpdateStatus(acp.ToolCallStatusCompleted)))
	f.send(ctx, sid, acp.UpdateAgentMessageText("Answer: "+string(b)))
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

// cwdOf is where the fake's session runs, from its first prompt's log.
func cwdOf(acp.PromptRequest) string {
	wd, _ := os.Getwd()
	return wd
}

func (f *fake) Cancel(ctx context.Context, p acp.CancelNotification) error {
	f.record("session/cancel", p)
	return nil
}

func (f *fake) CloseSession(ctx context.Context, p acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	f.record("session/close", p)
	return acp.CloseSessionResponse{}, nil
}

func (f *fake) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, acp.NewMethodNotFound("authenticate")
}

func (f *fake) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound("logout")
}

func (f *fake) DeleteSession(context.Context, acp.DeleteSessionRequest) (acp.DeleteSessionResponse, error) {
	return acp.DeleteSessionResponse{}, acp.NewMethodNotFound("session/delete")
}

func (f *fake) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound("session/list")
}

func (f *fake) ResumeSession(ctx context.Context, p acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	f.record("session/resume", p)
	switch f.scenario {
	case "memory", "memory-reject-model", "memory-load-metadata", "compact", "compact-no-cmd", "compact-agy", "compact-replies", "compact-unlisted", "compact-failed":
	case "memory-resume-fails":
		return acp.ResumeSessionResponse{}, acp.NewInternalError(map[string]any{"message": "Failed to read session " + string(p.SessionId) + "; retry, or load it with session/load"})
	default:
		return acp.ResumeSessionResponse{}, acp.NewMethodNotFound("session/resume")
	}
	options, _, err := f.takeUp(p.SessionId, false)
	if err == nil {
		f.sendCommands(ctx, p.SessionId)
	}
	return acp.ResumeSessionResponse{ConfigOptions: options, Modes: f.modes()}, err
}

// LoadSession replays the session's conversation, as ACP has an agent
// do, before it answers.
func (f *fake) LoadSession(ctx context.Context, p acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	f.record("session/load", p)
	if !f.remembers() || f.is("memory-no-resume") {
		return acp.LoadSessionResponse{}, acp.NewMethodNotFound("session/load")
	}
	options, said, err := f.takeUp(p.SessionId, true)
	if f.is("memory-load-metadata") {
		f.send(ctx, p.SessionId, acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{AvailableCommands: []acp.AvailableCommand{}}})
	}
	for _, s := range said {
		f.send(ctx, p.SessionId, acp.UpdateUserMessageText(s))
		f.send(ctx, p.SessionId, acp.UpdateAgentMessageText("replayed: "+s))
	}
	return acp.LoadSessionResponse{ConfigOptions: options, Modes: f.modes()}, err
}

// SetSessionMode takes one of Modes, as agy does, and tells of it before
// it answers; "mode-refused" takes none.
func (f *fake) SetSessionMode(ctx context.Context, p acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	f.record("session/set_mode", p)
	if f.is("mode-refused") || !slices.Contains(Modes, string(p.ModeId)) {
		return acp.SetSessionModeResponse{}, acp.NewInvalidParams("Unknown mode: " + string(p.ModeId))
	}
	f.mu.Lock()
	f.mode = string(p.ModeId)
	f.mu.Unlock()
	f.send(ctx, p.SessionId, acp.SessionUpdate{CurrentModeUpdate: &acp.SessionCurrentModeUpdate{CurrentModeId: p.ModeId}})
	return acp.SetSessionModeResponse{}, nil
}

// askCredentials calls request_credentials for GLX_PROBE on the session's
// glitched MCP server, as the real agent would, and returns the text of
// its result.
func (f *fake) askCredentials() (json.RawMessage, error) {
	f.mu.Lock()
	servers := f.servers
	f.mu.Unlock()
	var srv *acp.McpServerStdio
	for _, s := range servers {
		if s.Stdio != nil && s.Stdio.Name == "glitched" {
			srv = s.Stdio
		}
	}
	if srv == nil {
		return nil, fmt.Errorf("no glitched MCP server in %d", len(servers))
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, Scenario+"=") {
			env = append(env, kv)
		}
	}
	for _, e := range srv.Env {
		env = append(env, e.Name+"="+e.Value)
	}
	return mcptest.RequestCredentials(context.Background(), srv.Command, srv.Args, env)
}
