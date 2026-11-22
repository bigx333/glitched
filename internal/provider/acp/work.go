package acp

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// step is one of the agent's tool calls, as its updates have filled it in
// so far: each update patches the fields it names.
type step struct {
	id        acpsdk.ToolCallId
	title     string
	kind      acpsdk.ToolKind
	status    acpsdk.ToolCallStatus
	locations []acpsdk.ToolCallLocation
	input     json.RawMessage
	content   []acpsdk.ToolCallContent
	output    json.RawMessage
}

func (s *step) apply(u acpsdk.ToolCallUpdate) {
	if v, ok := u.Title.Value(); ok {
		s.title = v
	}
	if v, ok := u.Kind.Value(); ok {
		s.kind = v
	}
	if v, ok := u.Status.Value(); ok {
		s.status = v
	}
	if v, ok := u.Locations.Value(); ok {
		s.locations = v
	}
	if v, ok := u.RawInput.Value(); ok {
		s.input = v
	}
	if v, ok := u.Content.Value(); ok {
		s.content = v
	}
	if v, ok := u.RawOutput.Value(); ok {
		s.output = v
	}
}

// kinds are the steps Glitched shows, by the kind of call: the agent's
// thinking and mode switches are not steps of the work.
var kinds = map[acpsdk.ToolKind]string{
	acpsdk.ToolKindExecute: "command",
	acpsdk.ToolKindEdit:    "edit",
	acpsdk.ToolKindDelete:  "edit",
	acpsdk.ToolKindMove:    "edit",
	acpsdk.ToolKindSearch:  "search",
	acpsdk.ToolKindRead:    "read",
	acpsdk.ToolKindFetch:   "read",
}

func (s *step) shown() bool {
	return s.kind != acpsdk.ToolKindThink && s.kind != acpsdk.ToolKindSwitchMode
}

func (s *step) open() bool {
	return s.shown() && s.status != acpsdk.ToolCallStatusCompleted && s.status != acpsdk.ToolCallStatusFailed
}

func (s *step) work(workspace string) provider.Work {
	kind, ok := kinds[s.kind]
	if !ok {
		kind = "tool"
	}
	w := provider.Work{Item: string(s.id), Kind: kind, Title: s.title, Status: api.WorkStarted}
	switch s.status {
	case acpsdk.ToolCallStatusCompleted:
		w.Status = api.WorkCompleted
	case acpsdk.ToolCallStatusFailed:
		w.Status = api.WorkFailed
	}
	if (kind == "edit" || kind == "read") && len(s.locations) > 0 {
		w.Path = rel(workspace, s.locations[0].Path)
	}
	if w.Status != api.WorkStarted {
		w.Output = s.said()
		var out struct {
			ExitCode *int `json:"exit_code"`
		}
		if json.Unmarshal(s.output, &out) == nil {
			w.Exit = out.ExitCode
		}
	}
	return w
}

// said is what a finished call printed: its text content, else its raw
// output.
func (s *step) said() string {
	var b strings.Builder
	for _, c := range s.content {
		if c.Content != nil && c.Content.Content.Text != nil {
			b.WriteString(c.Content.Content.Text.Text)
		}
	}
	if b.Len() > 0 {
		return b.String()
	}
	var text string
	if json.Unmarshal(s.output, &text) == nil {
		return text
	}
	return strings.TrimSpace(string(s.output))
}

// approval is the card's body for a request to run s: the command to run
// where, or the operation on every path it names, and every argument an
// allow approves.
func (s *step) approval(workspace, agent string) json.RawMessage {
	var b struct {
		Operation string `json:"operation,omitempty"`
		Path      string `json:"path,omitempty"`
		Command   string `json:"command,omitempty"`
		Cwd       string `json:"cwd,omitempty"`
		Reason    string `json:"reason,omitempty"`
		Args      string `json:"args,omitempty"`
	}
	// agy names a command CommandLine, and says why in toolSummary.
	var input struct {
		Command     json.RawMessage
		CommandLine string
		Cwd         string
		ToolSummary string
	}
	json.Unmarshal(s.input, &input)
	var paths []string
	for _, l := range s.locations {
		paths = append(paths, rel(workspace, l.Path))
	}
	b.Path = strings.Join(paths, ", ")
	b.Reason = cmp.Or(input.ToolSummary, s.title)
	switch kinds[s.kind] {
	case "command":
		b.Command, b.Cwd = cmp.Or(commandOf(input.Command), input.CommandLine), workspace
		if input.Cwd != "" {
			b.Cwd = input.Cwd
		}
		if b.Command == "" {
			b.Command = s.title
		}
		b.Path = ""
	case "edit", "read":
		b.Operation = map[acpsdk.ToolKind]string{acpsdk.ToolKindDelete: "delete", acpsdk.ToolKindMove: "move", acpsdk.ToolKindFetch: "fetch"}[s.kind]
		if b.Operation == "" {
			b.Operation = kinds[s.kind]
		}
	case "search":
		b.Operation = "search"
	default:
		b.Operation = "use " + strings.ToLower(string(s.kind))
		if s.kind == "" || s.kind == acpsdk.ToolKindOther {
			b.Operation = "use a tool"
		}
	}
	if b.Command == "" {
		b.Args = argsText(s.input)
	}
	if b.Operation != "" && b.Path == "" && b.Args == "" && b.Reason == "" {
		b.Reason = agent + " gave no detail."
	}
	body, _ := json.Marshal(b)
	return body
}

// commandOf is a command as the agent sent it: a string, or its words,
// each quoted as a shell would need it to stay one word.
func commandOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var words []string
	if json.Unmarshal(raw, &words) != nil {
		return ""
	}
	for i, w := range words {
		if w == "" || strings.ContainsFunc(w, func(r rune) bool { return !strings.ContainsRune(plainWord, r) }) {
			words[i] = "'" + strings.ReplaceAll(w, "'", "'\\''") + "'"
		}
	}
	return strings.Join(words, " ")
}

const plainWord = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:,+@%"

// argsText is a tool's input as the card shows it: a line per field, its
// key and its value as JSON.
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
		var c bytes.Buffer
		json.Compact(&c, v)
		lines = append(lines, fmt.Sprintf("%s: %s", k, c.String()))
	}
	return strings.Join(lines, "\n")
}

// rel is path inside workspace, as the card and the steps show it, or as
// it is outside. The agent may name the workspace by the path its links
// resolve to, as macOS's /var is /private/var.
func rel(workspace, path string) string {
	// Past a symlink, .. leaves the link's target, not its directory: such
	// a path is shown as the agent gave it.
	if slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..") {
		return path
	}
	resolved, _ := filepath.EvalSymlinks(workspace)
	for _, w := range []string{workspace, resolved} {
		if r, err := filepath.Rel(w, path); w != "" && err == nil && filepath.IsLocal(r) {
			return r
		}
	}
	return path
}

// kindOf names an update for the log.
func kindOf(u acpsdk.SessionUpdate) string {
	b, _ := json.Marshal(u)
	var k struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	json.Unmarshal(b, &k)
	return k.SessionUpdate
}
