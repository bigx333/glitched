package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// message is one line of Claude Code's stream-json output, with the fields
// a turn reads. Its Type picks which of them are set.
type message struct {
	Type            string       `json:"type"`
	Subtype         string       `json:"subtype"`
	ParentToolUseID *string      `json:"parent_tool_use_id"`
	Event           *streamEvent `json:"event"` // stream_event
	Message         *struct {
		Content blocks `json:"content"`
		request
	} `json:"message"` // assistant, user
	Error   string   `json:"error"` // assistant: why a synthetic reply stands in for the model's
	IsError bool     `json:"is_error"`
	Result  string   `json:"result"`
	Errors  []string `json:"errors"`
	Usage   usage    `json:"usage"`
	// ModelUsage is, on a result, each model the session called, by id.
	ModelUsage map[string]modelUsage `json:"modelUsage"`
	// UserMessageUUIDs is, on a result, the user messages it finished.
	UserMessageUUIDs []string  `json:"user_message_uuids"`
	Capabilities     []string  `json:"capabilities"` // system init
	Response         *response `json:"response"`     // control_response
	RequestID        string    `json:"request_id"`   // control_request, control_cancel_request
	Request          *struct {
		Subtype string `json:"subtype"`
		canUseTool
	} `json:"request"` // control_request
}

type streamEvent struct {
	Type         string   `json:"type"`
	Message      *request `json:"message"` // message_start
	ContentBlock *block   `json:"content_block"`
	Delta        *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"` // a string, or text blocks
}

// blocks is a message's content: a list of blocks, or a plain string that
// a turn has no use for.
type blocks []block

func (b *blocks) UnmarshalJSON(data []byte) error {
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		return json.Unmarshal(data, (*[]block)(b))
	}
	return nil
}

type usage struct {
	Input         int64 `json:"input_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	Output        int64 `json:"output_tokens"`
}

func (u usage) total() int64 { return u.Input + u.CacheCreation + u.CacheRead + u.Output }

// prompt is what the request sent the model: its context as the request
// found it, which the request's output joins only from the next one.
func (u usage) prompt() int64 { return u.Input + u.CacheCreation + u.CacheRead }

// request is a model request's, as its message says: the model it went
// to and what it used.
type request struct {
	Model string `json:"model"`
	Usage usage  `json:"usage"`
}

type modelUsage struct {
	Canonical     string `json:"canonicalModel"`
	ContextWindow int64  `json:"contextWindow"`
}

// window is the context window of model, the model the turn's last
// request went to, as the result's modelUsage gives it: by its id, else
// by its canonical name where every entry of that name gives the same
// window; 0 if none says, and none for a request that didn't say its
// model. Another model's window is not this one's.
func window(models map[string]modelUsage, model string) int64 {
	if model == "" {
		return 0
	}
	if u, ok := models[model]; ok {
		return u.ContextWindow
	}
	w := int64(-1)
	for _, u := range models {
		switch {
		case u.Canonical != model:
		case w != -1 && w != u.ContextWindow:
			return 0
		default:
			w = u.ContextWindow
		}
	}
	return max(w, 0)
}

// sent is the context a request of the turn's sent, as its message_start
// or its assistant message says, once.
func (t *turn) sent(r *request) []provider.Event {
	if r == nil {
		return nil
	}
	used := r.Usage.prompt()
	if used == 0 {
		return nil
	}
	same := used == t.used && r.Model == t.model
	t.used, t.model = used, r.Model
	if same {
		return nil
	}
	return []provider.Event{provider.Context{Used: used, Model: t.model}}
}

// measured is the turn's context with the window a result gives it.
func (t *turn) measured(m message) []provider.Event {
	if w := window(m.ModelUsage, t.model); t.used > 0 && w > 0 {
		return []provider.Event{provider.Context{Used: t.used, Window: w, Model: t.model}}
	}
	return nil
}

type response struct {
	Subtype   string `json:"subtype"`
	RequestID string `json:"request_id"`
	Error     string `json:"error"`
	Response  struct {
		Capabilities []string `json:"capabilities"`
	} `json:"response"`
}

// tool is how a Claude Code tool shows as a step: its kind, and the input
// field whose value follows the tool's name in the title.
type tool struct{ kind, arg string }

var tools = map[string]tool{
	"Bash":         {"command", "command"},
	"Read":         {"read", "file_path"},
	"Write":        {"edit", "file_path"},
	"Edit":         {"edit", "file_path"},
	"MultiEdit":    {"edit", "file_path"},
	"NotebookEdit": {"edit", "notebook_path"},
	"Grep":         {"search", "pattern"},
	"Glob":         {"search", "pattern"},
	"WebSearch":    {"search", "query"},
	"WebFetch":     {"read", "url"},
	"Task":         {"tool", "description"},
	"Agent":        {"tool", "description"},
}

// turn turns one run's messages into provider events. Subagents' messages
// carry a parent tool use and stay out of the thread: their Task step
// stands for them.
type turn struct {
	workspace   string
	steps       []provider.Work // tool uses still open, in the order they opened
	texted      bool            // a text block already streamed
	signedOut   string          // what Claude Code said when the API refused its login
	tokens      int64           // what the results before the last one used
	used        int64           // the context the turn's last request sent; 0 before one, or since a compaction
	model       string          // the model that request went to
	interrupted bool            // the user stopped the turn
}

func newTurn(workspace string) *turn {
	return &turn{workspace: workspace}
}

func (t *turn) read(m message) []provider.Event {
	if m.ParentToolUseID != nil {
		return nil
	}
	switch m.Type {
	case "stream_event":
		return t.stream(m.Event)
	case "assistant":
		// Text blocks repeat what the deltas streamed; tool uses open steps.
		if m.Error == "authentication_failed" {
			for _, b := range m.content() {
				t.signedOut += b.text()
			}
		}
		// AskUserQuestion shows as its question card, not as a step.
		var evs []provider.Event
		if m.Message != nil {
			evs = t.sent(&m.Message.request)
		}
		for _, b := range m.content() {
			if b.Type == "tool_use" && b.Name != askTool {
				w := t.open(b)
				t.steps = append(t.steps, w)
				evs = append(evs, w)
			}
		}
		return evs
	case "user":
		var evs []provider.Event
		for _, b := range m.content() {
			i := slices.IndexFunc(t.steps, func(w provider.Work) bool { return w.Item == b.ToolUseID })
			if b.Type != "tool_result" || i < 0 {
				continue
			}
			w := t.steps[i]
			t.steps = slices.Delete(t.steps, i, i+1)
			w.Status, w.Output = api.WorkCompleted, b.text()
			if b.IsError {
				w.Status = api.WorkFailed
			}
			evs = append(evs, w)
		}
		return evs
	case "result":
		evs := append(t.unfinished(), t.measured(m)...)
		return append(evs, t.end(m))
	case "system":
		if m.Subtype == "compact_boundary" {
			t.used = 0
			return []provider.Event{provider.Context{Compacted: true}}
		}
	}
	return nil
}

// unfinished fails the steps still open, for a turn that ends without
// their results.
func (t *turn) unfinished() []provider.Event {
	evs := make([]provider.Event, len(t.steps))
	for i, w := range t.steps {
		w.Status = api.WorkFailed
		evs[i] = w
	}
	t.steps = nil
	return evs
}

func (t *turn) stream(e *streamEvent) []provider.Event {
	switch {
	case e == nil:
	case e.Type == "message_start":
		return t.sent(e.Message)
	case e.Type == "content_block_start" && e.ContentBlock != nil && e.ContentBlock.Type == "text" && t.texted:
		return []provider.Event{provider.TextDelta{Text: "\n\n"}}
	case e.Type == "content_block_delta" && e.Delta != nil && e.Delta.Type == "text_delta" && e.Delta.Text != "":
		t.texted = true
		return []provider.Event{provider.TextDelta{Text: e.Delta.Text}}
	}
	return nil
}

func (t *turn) open(b block) provider.Work {
	tl, ok := tools[b.Name]
	if !ok {
		tl = tool{kind: "tool"}
	}
	var input map[string]any
	json.Unmarshal(b.Input, &input)
	arg, _ := input[tl.arg].(string)
	w := provider.Work{Item: b.ID, Kind: tl.kind, Title: b.Name, Status: api.WorkStarted}
	if tl.kind == "edit" || tl.kind == "read" && tl.arg == "file_path" {
		arg = t.rel(arg)
		w.Path = arg
	}
	if arg, _, _ = strings.Cut(arg, "\n"); arg != "" {
		w.Title += " " + arg
	}
	return w
}

// rel is path as the workspace sees it, when it is inside.
func (t *turn) rel(path string) string {
	return provider.Rel(t.workspace, path)
}

// tally counts a result that did not end the turn, as when a message sent
// mid-turn came too late for it and Claude Code took it after.
func (t *turn) tally(m message) []provider.Event {
	t.tokens += m.Usage.total()
	return t.measured(m)
}

func (t *turn) end(m message) provider.TurnEnded {
	e := provider.TurnEnded{Outcome: api.OutcomeCompleted, Tokens: t.tokens + m.Usage.total()}
	if t.interrupted {
		e.Outcome = api.OutcomeInterrupted
		return e
	}
	if !m.IsError {
		return e
	}
	e.Outcome = api.OutcomeFailed
	switch {
	case t.signedOut != "":
		e.Error = notSignedIn(t.signedOut)
	case len(m.Errors) > 0:
		e.Error = strings.Join(m.Errors, "\n")
	case m.Result != "":
		e.Error = m.Result
	default:
		e.Error = fmt.Sprintf("Claude Code ended the turn with %s.", m.Subtype)
	}
	return e
}

func notSignedIn(said string) string {
	return "Claude Code is not signed in.\nRun claude in a terminal and use /login.\n(claude said: " + said + ")"
}

func (m message) content() blocks {
	if m.Message == nil {
		return nil
	}
	return m.Message.Content
}

// text is a block's own text, or the text of the blocks it holds.
func (b block) text() string {
	if b.Type == "text" {
		return b.Text
	}
	var s string
	if json.Unmarshal(b.Content, &s) == nil {
		return s
	}
	var inner []block
	json.Unmarshal(b.Content, &inner)
	var out strings.Builder
	for _, c := range inner {
		out.WriteString(c.text())
	}
	return out.String()
}
