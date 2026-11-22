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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

const (
	// Scenario names, in the fake's environment, what it does.
	Scenario = "GLITCHED_FAKE_ACP"
	// Log names the file the fake appends what it received to.
	Log = "GLITCHED_FAKE_ACP_LOG"
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
	f := &fake{scenario: scenario}
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
	sessions int
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
	line, _ := json.Marshal(Entry{Method: method, Params: raw, PID: os.Getpid()})
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
	case "memory", "memory-reject-model", "memory-resume-fails", "memory-load-metadata":
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
func (f *fake) remembers() bool { return strings.HasPrefix(f.scenario, "memory") }

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
	f.model, f.level = Models[0].ID, "high"
	return f.options(), said, nil
}

// Models are the fake's: Pro runs at two efforts, Flash at none.
var Models = []struct{ ID, Name string }{{"fake-pro", "Fake Pro"}, {"fake-flash", "Fake Flash"}}

func (f *fake) options() []acp.SessionConfigOption {
	model := acp.SessionConfigOptionCategoryModel
	level := acp.SessionConfigOptionCategoryThoughtLevel
	var models acp.SessionConfigSelectOptionsUngrouped
	for _, m := range Models {
		models = append(models, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(m.ID), Name: m.Name})
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
	f.model, f.level = Models[0].ID, "high"
	options := f.options()
	f.mu.Unlock()
	if f.remembers() {
		m := memory()
		m[id] = []string{}
		remember(m)
	}
	if f.is("updates-before-new-response") {
		f.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: id, Update: acp.UpdateAgentMessageText("history the run must not show")})
		f.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: id, Update: acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{AvailableCommands: []acp.AvailableCommand{}}}})
	}
	return acp.NewSessionResponse{SessionId: id, ConfigOptions: options}, nil
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
		f.model = string(v.Value)
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
	case "permission", "permission-no-allow-once", "permission-glitched", "gui":
		if f.is("gui") {
			f.send(ctx, sid, acp.UpdateAgentMessageText("I'll read the notes, then add a line to them.\n\n"))
			f.send(ctx, sid, acp.StartToolCall("read-1", "Read notes.txt", acp.WithStartKind(acp.ToolKindRead), acp.WithStartStatus(acp.ToolCallStatusCompleted),
				acp.WithStartLocations([]acp.ToolCallLocation{{Path: cwdOf(p) + "/notes.txt"}})))
		}
		options := allOptions
		if f.is("permission-no-allow-once") {
			options = allOptions[1:]
		}
		call := acp.ToolCallUpdate{ToolCallId: "edit-1", Title: acp.PatchSet("Edit notes.txt"), Kind: acp.PatchSet(acp.ToolKindEdit),
			Locations: acp.PatchSet([]acp.ToolCallLocation{{Path: cwdOf(p) + "/notes.txt"}}), RawInput: acp.PatchSet(json.RawMessage(`{"path":"notes.txt","content":"hi"}`))}
		if f.is("permission-glitched") {
			call = acp.ToolCallUpdate{ToolCallId: "mcp-1", Title: acp.PatchSet("delegate_task"), Kind: acp.PatchSet(acp.ToolKindOther),
				RawInput: acp.PatchSet(json.RawMessage(`{"serverName":"glitched","toolName":"delegate_task","args":{}}`))}
		}
		f.send(ctx, sid, acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{ToolCallId: call.ToolCallId, Title: "Edit notes.txt", Kind: acp.ToolKindEdit, Status: acp.ToolCallStatusPending}})
		answer := f.ask(ctx, sid, call, options)
		status := map[bool]acp.ToolCallStatus{true: acp.ToolCallStatusCompleted, false: acp.ToolCallStatusFailed}[answer == "allow-once"]
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
		gate()
		f.send(ctx, sid, acp.UpdateAgentMessageText("Here is the plan."))
	case "thinking-silent":
		gate()
		f.send(ctx, sid, acp.UpdateAgentMessageText("Here is the plan."))
	case "thinking-tools":
		f.send(ctx, sid, acp.UpdateAgentThoughtText("First the notes."))
		f.send(ctx, sid, acp.StartToolCall("read-1", "Read notes.txt", acp.WithStartKind(acp.ToolKindRead), acp.WithStartStatus(acp.ToolCallStatusCompleted)))
		f.send(ctx, sid, acp.UpdateAgentThoughtText("Then the code."))
		gate()
		f.send(ctx, sid, acp.StartToolCall("read-2", "Read main.go", acp.WithStartKind(acp.ToolKindRead), acp.WithStartStatus(acp.ToolCallStatusCompleted)))
		f.send(ctx, sid, acp.UpdateAgentMessageText("Read both."))
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

// gate waits for the file Gate names to exist.
func gate() {
	for {
		if _, err := os.Stat(os.Getenv(Gate)); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
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
	case "memory", "memory-reject-model", "memory-load-metadata":
	case "memory-resume-fails":
		return acp.ResumeSessionResponse{}, acp.NewInternalError(map[string]any{"message": "Failed to read session " + string(p.SessionId) + "; retry, or load it with session/load"})
	default:
		return acp.ResumeSessionResponse{}, acp.NewMethodNotFound("session/resume")
	}
	options, _, err := f.takeUp(p.SessionId, false)
	return acp.ResumeSessionResponse{ConfigOptions: options}, err
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
	return acp.LoadSessionResponse{ConfigOptions: options}, err
}

func (f *fake) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound("session/set_mode")
}
