package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

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

// deniedReason is what Claude Code hears when the user denies a tool call.
const deniedReason = "The user denied this tool call in glitched."

// withdrawnReason is what the card says once Claude Code no longer asks.
const withdrawnReason = "Claude Code no longer asks: a rule or hook of yours decided."

// ask is the request a can_use_tool becomes, in the ask body the card
// reads: a command to run where and why, or an operation on a path. A
// tool the card has no words for shows every argument an allow approves.
func (t *turn) ask(id string, r canUseTool) (provider.RequestOpened, *prompt) {
	var b struct {
		Operation   string `json:"operation,omitempty"`
		Path        string `json:"path,omitempty"`
		Command     string `json:"command,omitempty"`
		Cwd         string `json:"cwd,omitempty"`
		Reason      string `json:"reason,omitempty"`
		Args        string `json:"args,omitempty"`
		Unsandboxed bool   `json:"unsandboxed,omitempty"`
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
		b.Unsandboxed, _ = input["dangerouslyDisableSandbox"].(bool)
	case "edit", "read":
		b.Operation, b.Path = tl.kind, t.rel(arg)
	case "search":
		b.Operation, b.Path = "search for", arg
	default:
		b.Operation, b.Args = "use "+r.ToolName, argsText(r.Input)
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

// argsText is a tool's input as text, a line per field in Claude Code's
// order: its key, then its value as JSON, so that a string shows apart
// from the number, bool, null or array it spells. A string of more than
// one line shows as a block under "|", each of its lines indented. A key
// shows bare when it holds only letters, digits, "_", "." and "-", and as
// JSON otherwise, so no key passes for a key and a value. Input that is no
// object shows as it came.
func argsText(input json.RawMessage) string {
	d := json.NewDecoder(bytes.NewReader(input))
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		return strings.TrimSpace(string(input))
	}
	var lines []string
	for d.More() {
		k, err := d.Token()
		var v json.RawMessage
		if err != nil || d.Decode(&v) != nil {
			return strings.TrimSpace(string(input))
		}
		key := k.(string)
		if !bareKey.MatchString(key) {
			key = jsonText(key)
		}
		var s string
		switch {
		case v[0] == '"' && json.Unmarshal(v, &s) == nil && strings.Contains(s, "\n"):
			s = " |\n  " + strings.ReplaceAll(s, "\n", "\n  ")
		case v[0] == '"':
			s = " " + jsonText(s)
		default:
			var c bytes.Buffer
			json.Compact(&c, v)
			s = " " + c.String()
		}
		lines = append(lines, key+":"+s)
	}
	return strings.Join(lines, "\n")
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// jsonText is s as a JSON string, its <, > and & as they are.
func jsonText(s string) string {
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
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
