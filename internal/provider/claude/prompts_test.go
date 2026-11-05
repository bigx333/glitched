package claude_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider/claude/transcript"
	"glitched/internal/runtime"
)

// shippedCLI is the Claude Code release whose undocumented
// --permission-prompt-tool stdio protocol the adapter speaks.
const shippedCLI = "2.1.317"

// clients is how many clients race to answer each prompt.
const clients = 8

// A promptScenario is a turn on which Claude Code asks about tool calls.
// The claudelive contract test runs each on the real CLI and records its
// transcript; the replay tests play that transcript back, and both check
// the same things.
type promptScenario struct {
	name, prompt string
	// settings is the workspace's .claude/settings.local.json. A -p run
	// takes no allow rule from .claude/settings.json in a folder the
	// user never trusted, but takes them from this file outside git.
	settings string
	// access is the thread's.
	access api.Access
	// answer is what the clients answer the request with, or nil to leave
	// it to Claude Code's own rules and hooks.
	answer func(r requestLog) json.RawMessage
	// replied is, for a question the clients answer, the decision Claude
	// Code must get, less the questions an allow gives back as they came.
	replied string
	check   func(t *testing.T, got promptLog)
	// pin checks what the transcript at tape shows beyond what every
	// scenario's shows.
	pin   func(t *testing.T, tape string)
	files map[string]bool // which files a live run leaves in the workspace
}

type ask struct{ Operation, Path, Command, Reason string }

var (
	// The first write is allowed and the second denied. The recording
	// follows each answer with a contrary one, which Claude Code ignores.
	approve = promptScenario{
		name:   "prompt-approve",
		prompt: "Use the Write tool to create yes.txt containing y. Then use the Write tool to create no.txt containing n. If a tool call is denied, do not try it again. Then reply done.",
		answer: func(r requestLog) json.RawMessage { return allowed(r.ask.Path == "yes.txt") },
		files:  map[string]bool{"yes.txt": true, "no.txt": false},
		check: func(t *testing.T, got promptLog) {
			if len(got.requests) < 2 {
				t.Fatalf("requests %+v, want one for each write", got.requests)
			}
			for _, r := range got.requests {
				if r.opened.Kind != "approval" || r.ask.Operation != "edit" || r.state != api.RequestDelivered {
					t.Errorf("request %+v, want an approval to edit, delivered", r)
				}
			}
			yes, no := got.step("Write yes.txt"), got.step("Write no.txt")
			if yes.Status != api.WorkCompleted {
				t.Errorf("the allowed write ended %+v, want completed", yes)
			}
			if no.Status != api.WorkFailed || !strings.Contains(no.Output, "denied this tool call") {
				t.Errorf("the denied write ended %+v, want failed with the reason glitched gave", no)
			}
			if got.ended.Outcome != api.OutcomeCompleted {
				t.Errorf("turn ended %+v, want completed", got.ended)
			}
		},
	}

	// The user's own allow rule lets the first write run with no prompt;
	// their PermissionRequest hook allows the second while its prompt is
	// open.
	settled = promptScenario{
		name:     "prompt-settled",
		prompt:   "Use the Write tool to create ruled.txt containing r. Then use the Write tool to create hooked.txt containing h. Then reply done.",
		settings: `{"permissions":{"allow":["Edit(./ruled.txt)"]},"hooks":{"PermissionRequest":[{"matcher":"Write","hooks":[{"type":"command","command":"sleep 1; echo '{\"hookSpecificOutput\":{\"hookEventName\":\"PermissionRequest\",\"decision\":{\"behavior\":\"allow\"}}}'"}]}]}}`,
		answer:   func(requestLog) json.RawMessage { return nil },
		files:    map[string]bool{"ruled.txt": true, "hooked.txt": true},
		check: func(t *testing.T, got promptLog) {
			if len(got.requests) != 1 || got.requests[0].ask.Path != "hooked.txt" {
				t.Fatalf("requests %+v, want one, for the write the rule does not cover", got.requests)
			}
			if r := got.requests[0]; r.state != api.RequestInvalidated || !strings.Contains(r.reason, "no longer asks") {
				t.Errorf("the hook's request is %+v, want it void once the hook decided", r)
			}
			if w := got.step("Write ruled.txt"); w.Status != api.WorkCompleted {
				t.Errorf("the ruled write ended %+v, want completed", w)
			}
			if w := got.step("Write hooked.txt"); w.Status != api.WorkCompleted {
				t.Errorf("the hooked write ended %+v, want completed", w)
			}
			if got.ended.Outcome != api.OutcomeCompleted {
				t.Errorf("turn ended %+v, want completed", got.ended)
			}
		},
	}
)

type promptLog struct {
	workspace string
	text      string
	work      []api.WorkItem
	requests  []requestLog
	ended     api.TurnEnded
}

type requestLog struct {
	opened api.RequestOpened
	ask    ask
	answer json.RawMessage
	state  api.RequestState
	reason string
}

// void invalidates the open requests, as the engine does once the run
// stops.
func (l promptLog) void() {
	for i := range l.requests {
		if l.requests[i].state == api.RequestPending {
			l.requests[i].state = api.RequestInvalidated
		}
	}
}

// step is the last state of the step titled title.
func (l promptLog) step(title string) api.WorkItem {
	var w api.WorkItem
	for _, x := range l.work {
		if x.Title == title {
			w = x
		}
	}
	return w
}

// workspace is a scratch directory with the scenario's settings in it.
func (s promptScenario) workspace(t *testing.T, dir string) string {
	t.Helper()
	if s.settings != "" {
		os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
		if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), []byte(s.settings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// play runs the scenario's turn on h in ws, has clients race to answer
// each prompt the scenario answers, and returns what the thread's log
// holds of it.
func (s promptScenario) play(t *testing.T, h *runtime.Host, ws string) promptLog {
	t.Helper()
	thread := api.NewID()
	submit(t, h, thread, api.ThreadCreate{Workspace: ws, Title: s.name, Access: s.access,
		Model: api.ModelRef{Provider: "claude", Model: "haiku"}})
	return playOn(t, h, thread, s.prompt, func(r requestLog) bool {
		if answer := s.answer(r); answer != nil {
			race(t, h, thread, r.opened, func(int) json.RawMessage { return answer })
		}
		return false
	})
}

// playOn sends text and waits for the turn to end. Each request it opens
// goes to opened once, which stops the turn by returning true.
func playOn(t *testing.T, h *runtime.Host, thread api.ID, text string, opened func(requestLog) bool) promptLog {
	t.Helper()
	// A message starts a run under its own id.
	run := api.NewID()
	if _, err := h.Submit(ctx, api.Command{ID: run, Target: thread, Body: api.MessageDispatch{Text: text, Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	seen := map[api.ID]bool{}
	for deadline := time.Now().Add(turnWait); ; time.Sleep(5 * time.Millisecond) {
		l, over := readLog(t, h, thread, run)
		for _, r := range l.requests {
			if r.state == api.RequestPending && !seen[r.opened.Request] {
				seen[r.opened.Request] = true
				if opened(r) {
					submit(t, h, thread, api.TurnInterrupt{})
				}
			}
		}
		if over {
			return l
		}
		if time.Now().After(deadline) {
			t.Fatalf("the turn did not end: %+v", l)
		}
	}
}

// readLog is what the thread's log holds of run, and whether the run is
// over: ended, or uncertain.
func readLog(t *testing.T, h *runtime.Host, thread, run api.ID) (l promptLog, over bool) {
	t.Helper()
	snap, err := h.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range snap.Threads {
		if th.ID == thread {
			l.workspace = th.Workspace
		}
	}
	at := map[api.ID]int{}
	for _, e := range snap.Events {
		switch b := must(e.Decode()).(type) {
		case api.AssistantDelta:
			if b.Run == run {
				l.text += b.Text
			}
		case api.WorkItem:
			if b.Run == run {
				l.work = append(l.work, b)
			}
		case api.RequestOpened:
			if b.Run == run {
				r := requestLog{opened: b, state: api.RequestPending}
				json.Unmarshal(b.Body, &r.ask)
				at[b.Request] = len(l.requests)
				l.requests = append(l.requests, r)
			}
		case api.RequestAnswer:
			if i, ok := at[b.Request]; ok {
				l.requests[i].answer, l.requests[i].state = b.Answer, api.RequestAnswered
			}
		case api.RequestDelivery:
			if i, ok := at[b.Request]; ok {
				l.requests[i].state = api.RequestDelivered
			}
		case api.RequestInvalidation:
			if i, ok := at[b.Request]; ok {
				l.requests[i].state, l.requests[i].reason = api.RequestInvalidated, b.Reason
			}
		case api.TurnStopping:
			if b.Run == run {
				l.void()
			}
		case api.TurnUncertain:
			if b.Run == run {
				l.void()
				over = true
			}
		case api.TurnEnded:
			if b.Run == run {
				l.ended, over = b, true
				l.void()
			}
		}
	}
	return l, over
}

// allowed is an approval's answer.
func allowed(allow bool) json.RawMessage {
	b, _ := json.Marshal(map[string]bool{"allow": allow})
	return b
}

// race has clients answer r at once, client i with answer(i), and fails
// unless exactly one wins.
func race(t *testing.T, h *runtime.Host, thread api.ID, r api.RequestOpened, answer func(i int) json.RawMessage) {
	t.Helper()
	errs := make([]error, clients)
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := range clients {
		wg.Go(func() {
			a := answer(i)
			<-gate
			_, errs[i] = h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread,
				Body: api.RequestRespond{Request: r.Request, Generation: r.Generation, Answer: a}})
		})
	}
	close(gate)
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, api.ErrAlreadyAnswered):
			t.Errorf("a losing client got %v, want already answered", err)
		}
	}
	if won != 1 {
		t.Errorf("%d of %d clients won the answer to %s, want 1", won, clients, r.Dedup)
	}
}

// stdin has claude's stdin go to a file, and returns the replies to its
// prompts that it got, by request id, once the run is over.
func stdin(t *testing.T) func() map[string][]reply {
	t.Helper()
	file := filepath.Join(t.TempDir(), "stdin")
	t.Setenv(transcript.EnvStdin, file)
	return func() map[string][]reply {
		data, _ := os.ReadFile(file)
		got := map[string][]reply{}
		for _, l := range bytes.Split(data, []byte("\n")) {
			var m struct {
				Type     string
				Response struct {
					Subtype   string
					RequestID string `json:"request_id"`
					Response  reply
				}
			}
			if json.Unmarshal(l, &m) == nil && m.Type == "control_response" {
				r := m.Response.Response
				r.subtype = m.Response.Subtype
				got[m.Response.RequestID] = append(got[m.Response.RequestID], r)
			}
		}
		return got
	}
}

type reply struct {
	subtype      string // the control_response's
	Behavior     string
	UpdatedInput json.RawMessage
	Message      string
}

// checkReplies fails unless Claude Code got one reply to each request the
// thread answered, in the shape it takes, saying what the winner said;
// and none to a request nobody answered. An allow gives back the input
// of the request in the transcript at tape, which for a question gains
// the answers s replied.
func checkReplies(t *testing.T, s promptScenario, l promptLog, got map[string][]reply, tape string) {
	t.Helper()
	asked := inputs(tape, l.workspace)
	for _, r := range l.requests {
		replies := got[r.opened.Dedup]
		if r.answer == nil {
			if len(replies) != 0 {
				t.Errorf("Claude Code got %+v for %s, which nobody answered", replies, r.opened.Dedup)
			}
			continue
		}
		if len(replies) != 1 {
			t.Errorf("Claude Code got %d replies to %s, want 1", len(replies), r.opened.Dedup)
			continue
		}
		if r.opened.Kind == api.KindQuestion {
			checkAnswered(t, s, replies[0], asked[r.opened.Dedup])
			continue
		}
		var a struct{ Allow bool }
		json.Unmarshal(r.answer, &a)
		switch p := replies[0]; {
		case p.subtype != "success":
			t.Errorf("a reply went to Claude Code as a %q control_response, want success", p.subtype)
		case a.Allow && (p.Behavior != "allow" || !sameJSON(p.UpdatedInput, asked[r.opened.Dedup])):
			t.Errorf("an allow went to Claude Code as %+v, want behavior allow with the tool's input %s", p, asked[r.opened.Dedup])
		case !a.Allow && (p.Behavior != "deny" || p.Message == ""):
			t.Errorf("a deny went to Claude Code as %+v, want behavior deny with a reason", p)
		}
	}
}

// inputs are the tool inputs of the can_use_tool requests in the
// transcript at tape, by request id, in workspace.
func inputs(tape, workspace string) map[string]json.RawMessage {
	in := map[string]json.RawMessage{}
	for _, l := range strings.Split(string(must(os.ReadFile(tape))), "\n") {
		var e struct {
			Recv *struct {
				Type      string
				RequestID string `json:"request_id"`
				Request   *struct {
					Subtype string
					Input   json.RawMessage
				}
			}
		}
		json.Unmarshal([]byte(strings.ReplaceAll(l, "$WORKSPACE", workspace)), &e)
		if m := e.Recv; m != nil && m.Type == "control_request" && m.Request != nil && m.Request.Subtype == "can_use_tool" {
			in[m.RequestID] = m.Request.Input
		}
	}
	return in
}

// sameJSON is whether a and b hold the same JSON value.
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

// checkTape pins what the transcript shows of the protocol: the CLI
// release, the shape of each can_use_tool, and, in a recording that
// followed each answer with a contrary one, that the contrary one went.
func checkTape(t *testing.T, path string, s promptScenario) {
	t.Helper()
	var version string
	asked := map[string]string{}
	var cancelled, dupes []string
	for _, l := range strings.Split(string(must(os.ReadFile(path))), "\n") {
		var e struct {
			Recv, Dupe *struct {
				Type, Subtype     string
				ClaudeCodeVersion string `json:"claude_code_version"`
				RequestID         string `json:"request_id"`
				Request           *struct {
					Subtype   string
					ToolName  string          `json:"tool_name"`
					Input     json.RawMessage `json:"input"`
					ToolUseID string          `json:"tool_use_id"`
					// RequiresUserInteraction is set on every AskUserQuestion.
					RequiresUserInteraction bool `json:"requires_user_interaction"`
				}
				Response *struct {
					RequestID string `json:"request_id"`
				}
			}
		}
		json.Unmarshal([]byte(l), &e)
		switch m := e.Recv; {
		case e.Dupe != nil && e.Dupe.Response != nil:
			dupes = append(dupes, e.Dupe.Response.RequestID)
		case m == nil:
		case m.Type == "system" && m.Subtype == "init":
			version = m.ClaudeCodeVersion
		case m.Type == "control_request" && m.Request != nil && m.Request.Subtype == "can_use_tool":
			r := m.Request
			if m.RequestID == "" || r.ToolName == "" || r.ToolUseID == "" || !json.Valid(r.Input) || r.Input[0] != '{' {
				t.Errorf("can_use_tool %s is %+v, want a request id, tool name, tool use id and input object", m.RequestID, r)
			}
			if r.ToolName == "AskUserQuestion" && (!r.RequiresUserInteraction || !askShaped(r.Input)) {
				t.Errorf("AskUserQuestion %s is %+v, want it to require the user, and questions with a question, header, multiSelect and labelled options", m.RequestID, r)
			}
			asked[m.RequestID] = r.ToolName
		case m.Type == "control_cancel_request":
			cancelled = append(cancelled, m.RequestID)
		}
	}
	if version != shippedCLI {
		t.Errorf("the transcript is of Claude Code %q, want %s", version, shippedCLI)
	}
	if len(asked) == 0 {
		t.Error("Claude Code sent no can_use_tool")
	}
	switch s.name {
	case approve.name:
		if len(dupes) != len(asked) {
			t.Errorf("the recording sent %d contrary answers to %d prompts, want one each", len(dupes), len(asked))
		}
	case settled.name:
		if len(asked) != 1 {
			t.Errorf("Claude Code asked %v, want only the write the user's rule does not allow", asked)
		}
		if len(cancelled) != 1 || asked[cancelled[0]] != "Write" {
			t.Errorf("Claude Code cancelled %v of %v, want the write the hook decided", cancelled, asked)
		}
	}
	if s.pin != nil {
		s.pin(t, path)
	}
}

// replayed plays a scenario's recorded transcript.
func replayed(t *testing.T, s promptScenario) {
	replay(t, s.name, "")
	replies := stdin(t)
	h := host(t, nil)
	got := s.play(t, h, s.workspace(t, t.TempDir()))
	tape := filepath.Join("testdata", s.name+".jsonl")
	s.check(t, got)
	checkReplies(t, s, got, replies(), tape)
	checkTape(t, tape, s)
}

func TestEightClientsRaceAndClaudeGetsOneReply(t *testing.T) { replayed(t, approve) }

func TestTheUsersRulesAndHooksDecideFirst(t *testing.T) { replayed(t, settled) }

// Half the clients allow and half deny: Claude Code gets the winner's
// answer, once.
func TestClaudeGetsTheWinningAnswer(t *testing.T) {
	replay(t, approve.name, "")
	replies := stdin(t)
	h := host(t, nil)
	thread := newThread(t, h, api.AccessAsk)
	got := playOn(t, h, thread, approve.prompt, func(r requestLog) bool {
		race(t, h, thread, r.opened, func(i int) json.RawMessage { return allowed(i%2 == 0) })
		return false
	})
	checkReplies(t, approve, got, replies(), filepath.Join("testdata", approve.name+".jsonl"))
}

func TestStoppingATurnVoidsItsPrompt(t *testing.T) {
	replay(t, approve.name, "")
	replies := stdin(t)
	h := host(t, nil)
	thread := newThread(t, h, api.AccessAsk)
	got := playOn(t, h, thread, approve.prompt, func(requestLog) bool { return true })
	if len(got.requests) != 1 || got.requests[0].state != api.RequestInvalidated {
		t.Errorf("requests %+v, want the one open when the turn stopped void", got.requests)
	}
	if got.ended.Outcome != api.OutcomeInterrupted {
		t.Errorf("the turn ended %+v, want interrupted", got.ended)
	}
	if r := replies(); len(r) != 0 {
		t.Errorf("Claude Code got %+v after the turn stopped, want nothing", r)
	}
}
