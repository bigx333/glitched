package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"glitched/internal/provider"
)

// canUseTool is Claude Code's request to run a tool that nothing in its
// own settings allowed: its rules, its hooks and its permission mode.
type canUseTool struct {
	ToolName       string          `json:"tool_name"`
	Input          json.RawMessage `json:"input"`
	BlockedPath    string          `json:"blocked_path"`
	DecisionReason string          `json:"decision_reason"` // may carry ANSI escapes
}

// prompt is a request Claude Code waits on. It gets one reply: the first
// answer, or nothing once Claude Code stops waiting.
type prompt struct {
	kind  string          // approval; a question is another kind
	input json.RawMessage // what an allow sends back as the tool's input
	state promptState
}

type promptState int

const (
	asking   promptState = iota
	answered             // the one reply went to Claude Code
	gone                 // Claude Code stopped waiting: a hook decided, or the turn ended
)

var errNoPrompt = errors.New("claude: Claude Code made no such request")

// errGone is why an answer to a prompt Claude Code stopped waiting on
// never went, as the card shows it.
var errGone = errors.New("Claude Code stopped waiting before the answer reached it.")

// deniedReason is what Claude Code hears when the user denies a tool call.
const deniedReason = "The user denied this tool call in glitched."

// withdrawnReason is what the card says once Claude Code no longer asks.
const withdrawnReason = "Claude Code no longer asks: a rule or hook of yours decided."

// ask is the request a can_use_tool becomes, in the ask body the card
// reads: a command to run where and why, or an operation on a path.
func (t *turn) ask(id string, r canUseTool) (provider.RequestOpened, *prompt) {
	var b struct {
		Operation string `json:"operation,omitempty"`
		Path      string `json:"path,omitempty"`
		Command   string `json:"command,omitempty"`
		Cwd       string `json:"cwd,omitempty"`
		Reason    string `json:"reason,omitempty"`
	}
	var input map[string]any
	json.Unmarshal(r.Input, &input)
	tl, ok := tools[r.ToolName]
	if !ok {
		tl = tool{kind: "tool"}
	}
	arg, _ := input[tl.arg].(string)
	switch tl.kind {
	case "command":
		b.Command, b.Cwd = arg, t.workspace
		b.Reason, _ = input["description"].(string)
	case "edit", "read":
		b.Operation, b.Path = tl.kind, t.rel(arg)
	case "search":
		b.Operation, b.Path = "search for", arg
	default:
		b.Operation = "use " + r.ToolName
	}
	if why := ansi.ReplaceAllString(r.DecisionReason, ""); why != "" {
		b.Reason = why
	}
	if b.Path == "" && b.Command == "" && r.BlockedPath != "" {
		b.Path = t.rel(r.BlockedPath)
	}
	body, _ := json.Marshal(b)
	return provider.RequestOpened{ID: id, Kind: "approval", Body: body}, &prompt{kind: "approval", input: r.Input}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// reply is the control_response that carries answer to the prompt.
func (p *prompt) reply(id string, answer json.RawMessage) (map[string]any, error) {
	var decision map[string]any
	switch p.kind {
	case "approval":
		var a struct{ Allow bool }
		json.Unmarshal(answer, &a)
		decision = map[string]any{"behavior": "deny", "message": deniedReason}
		if a.Allow {
			decision = map[string]any{"behavior": "allow", "updatedInput": p.input}
		}
	default:
		return nil, fmt.Errorf("claude: no answer for a %s", p.kind)
	}
	return map[string]any{"type": "control_response",
		"response": map[string]any{"subtype": "success", "request_id": id, "response": decision}}, nil
}

// refusal answers a control request this adapter does not serve, so
// Claude Code does not wait on it.
func refusal(id, subtype string) map[string]any {
	return map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "error", "request_id": id, "error": "glitched does not serve " + subtype}}
}
