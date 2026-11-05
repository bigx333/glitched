package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"glitched/internal/api"
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
	kind      string          // api.KindApproval or api.KindQuestion
	input     json.RawMessage // the tool's input, which an allow sends back
	questions api.Questions   // a question's, by position, as the card shows them
	state     promptState
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
	return provider.RequestOpened{ID: id, Kind: api.KindApproval, Body: body}, &prompt{kind: api.KindApproval, input: r.Input}
}

// argsText is a tool's input as text, a line per field in Claude Code's
// order: its key, then its value as JSON, so that a string shows apart
// from the number, bool, null or array it spells. A string of more than
// one line shows as a block under "|", each of its lines indented. A key
// shows bare when it holds only letters, digits, "_", "." and "-", and as
// JSON otherwise, so no key passes for a key and a value. A key or a
// string with a lone surrogate, which Go decodes as U+FFFD, shows as JSON,
// with the surrogate as the escape that spells it. Input that is no object
// shows as it came.
func argsText(input json.RawMessage) string {
	d := json.NewDecoder(bytes.NewReader(input))
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		return strings.TrimSpace(string(input))
	}
	var lines []string
	for d.More() {
		from := d.InputOffset()
		k, err := d.Token()
		rawKey := bytes.TrimLeft(input[from:d.InputOffset()], ", \t\r\n")
		var v json.RawMessage
		if err != nil || d.Decode(&v) != nil {
			return strings.TrimSpace(string(input))
		}
		key := k.(string)
		if q, lone := loneQuoted(rawKey); lone {
			key = q
		} else if !bareKey.MatchString(key) {
			key = jsonText(key)
		}
		var s string
		q, lone := "", false
		if v[0] == '"' {
			q, lone = loneQuoted(v)
		}
		switch {
		case lone:
			s = " " + q
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

// loneQuoted is the JSON string raw as jsonText shows it, but with each
// lone surrogate in it as the escape \uXXXX that spells it, and whether
// it has any. A tool reads a lone surrogate apart from U+FFFD, which
// shows as \u{FFFD}, and from the text \uXXXX, whose backslash shows
// doubled.
func loneQuoted(raw []byte) (string, bool) {
	if len(raw) < 2 || raw[0] != '"' || !bytes.Contains(raw, []byte(`\u`)) {
		return "", false
	}
	var b, run strings.Builder
	lone := false
	flush := func() {
		q := jsonText(run.String())
		b.WriteString(q[1 : len(q)-1])
		run.Reset()
	}
	s := string(raw[1 : len(raw)-1])
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			r, n := utf8.DecodeRuneInString(s[i:])
			run.WriteRune(r)
			i += n
			continue
		}
		if s[i+1] != 'u' {
			var r string
			json.Unmarshal([]byte(`"`+s[i:i+2]+`"`), &r)
			run.WriteString(r)
			i += 2
			continue
		}
		u := unit(s[i+2 : i+6])
		i += 6
		switch {
		case !utf16.IsSurrogate(u):
			run.WriteRune(u)
		case u < 0xDC00 && strings.HasPrefix(s[i:], `\u`) && unit(s[i+2:i+6]) >= 0xDC00 && unit(s[i+2:i+6]) < 0xE000:
			run.WriteRune(utf16.DecodeRune(u, unit(s[i+2:i+6])))
			i += 6
		default:
			flush()
			fmt.Fprintf(&b, `\u%04X`, u)
			lone = true
		}
	}
	if !lone {
		return "", false
	}
	flush()
	return `"` + b.String() + `"`, true
}

// unit is the UTF-16 unit four hex digits spell.
func unit(hex string) rune {
	n, _ := strconv.ParseUint(hex, 16, 16)
	return rune(n)
}

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
	if p.kind == api.KindQuestion {
		decision, err := p.answered(answer)
		if err != nil {
			return nil, err
		}
		return decided(id, decision), nil
	}
	var a struct{ Allow bool }
	json.Unmarshal(answer, &a)
	if a.Allow {
		return decided(id, map[string]any{"behavior": "allow", "updatedInput": p.input}), nil
	}
	return decided(id, map[string]any{"behavior": "deny", "message": deniedReason}), nil
}

// decided is the control_response that gives Claude Code a decision on
// its can_use_tool request id.
func decided(id string, decision map[string]any) map[string]any {
	return map[string]any{"type": "control_response",
		"response": map[string]any{"subtype": "success", "request_id": id, "response": decision}}
}

// refusal answers a control request this adapter does not serve, so
// Claude Code does not wait on it.
func refusal(id, subtype string) map[string]any {
	return map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "error", "request_id": id, "error": "glitched does not serve " + subtype}}
}
