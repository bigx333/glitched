package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

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
	// ToolUseID and Status are, on a system task_notification, the step
	// whose task ended and how.
	ToolUseID string `json:"tool_use_id"`
	Status    string `json:"status"`
	// ModelUsage is, on a result, each model the session called, by id.
	ModelUsage map[string]modelUsage `json:"modelUsage"`
	// UserMessageUUIDs is, on a result, the user messages it finished.
	UserMessageUUIDs []string `json:"user_message_uuids"`
	Capabilities     []string `json:"capabilities"` // system init
	// PluginErrors is, on a system init, why plugins didn't load, as
	// Glitched's skills may not.
	PluginErrors    json.RawMessage `json:"plugin_errors"`
	CompactMetadata *struct {
		PreTokens  int64 `json:"pre_tokens"`
		PostTokens int64 `json:"post_tokens"`
	} `json:"compact_metadata"` // system compact_boundary
	RateLimit *struct {
		Status string `json:"status"`
	} `json:"rate_limit_info"` // rate_limit_event
	Response  *response `json:"response"`   // control_response
	RequestID string    `json:"request_id"` // control_request, control_cancel_request
	Request   *struct {
		Subtype string `json:"subtype"`
		canUseTool
	} `json:"request"` // control_request
	// Model and PermissionMode are, on a system init, what the session
	// runs; a system status gives a PermissionMode it switched to.
	Model          string          `json:"model"`
	PermissionMode string          `json:"permissionMode"`
	FallbackModel  string          `json:"fallback_model"` // system model_fallback
	Content        json.RawMessage `json:"content"`        // system model_fallback: why, as a string
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

type cacheCreation struct {
	Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
}

type usage struct {
	Input         int64          `json:"input_tokens"`
	CacheCreation int64          `json:"cache_creation_input_tokens"`
	CacheRead     int64          `json:"cache_read_input_tokens"`
	Output        int64          `json:"output_tokens"`
	Cache         *cacheCreation `json:"cache_creation"`
	// Total is, on a task_notification, what the task used in all.
	Total int64 `json:"total_tokens"`
}

// cacheTTL is how long the prompt cache this request wrote stays warm,
// by the lifetime most of what it wrote has, or 0 when it wrote none, as
// a pure cache read does.
func (u usage) cacheTTL() time.Duration {
	switch {
	case u.Cache == nil:
		return 0
	case u.Cache.Ephemeral1h > 0 && u.Cache.Ephemeral1h >= u.Cache.Ephemeral5m:
		return time.Hour
	case u.Cache.Ephemeral5m > 0:
		return 5 * time.Minute
	}
	return 0
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
// or its assistant message says, once. The prompt cache's lifetime begins
// with the request, so CacheExpires is anchored to the first reading. A
// request that only read the cache refreshed it for the lifetime it had.
func (t *turn) sent(r *request) []provider.Event {
	if r == nil {
		return nil
	}
	used := r.Usage.prompt()
	if used == 0 {
		return nil
	}
	ttl := r.Usage.cacheTTL()
	if t.cache != nil {
		switch {
		case ttl > 0:
			t.cache.ttl = ttl
		case r.Usage.CacheRead > 0:
			ttl = t.cache.ttl
		default:
			t.cache.ttl = 0
		}
	}
	if used == t.used && r.Model == t.model && (ttl == 0 || !t.cacheExpires.IsZero()) {
		return nil
	}
	t.used, t.model, t.cacheExpires = used, r.Model, time.Time{}
	if ttl > 0 {
		t.cacheExpires = t.clock().UTC().Round(0).Add(ttl)
	}
	return []provider.Event{provider.Context{Used: used, Model: t.model, CacheExpires: t.cache.compactable(used, t.cacheExpires)}}
}

// measured is the turn's context with the window a result gives it.
func (t *turn) measured(m message) []provider.Event {
	if w := window(m.ModelUsage, t.model); t.used > 0 && w > 0 {
		return []provider.Event{provider.Context{Used: t.used, Window: w, Model: t.model, CacheExpires: t.cache.compactable(t.used, t.cacheExpires)}}
	}
	return nil
}

func (t *turn) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
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

// spawnTools are Claude Code's tools that start a native subagent, whose
// messages carry the tool use as their parent.
var spawnTools = []string{"Task", "Agent"}

// taskEnds are the statuses of a task_notification that end its task, as
// what each makes of it; any other is news of a task still at work.
var taskEnds = map[string]api.Outcome{
	"completed": api.OutcomeCompleted,
	"failed":    api.OutcomeFailed, "error": api.OutcomeFailed,
	"stopped": api.OutcomeInterrupted, "killed": api.OutcomeInterrupted, "cancelled": api.OutcomeInterrupted, "interrupted": api.OutcomeInterrupted,
}

// spawn is a native subagent of the turn's: its steps still open, whether
// it said anything yet, and the text it streamed since its last message,
// which that message's text blocks repeat.
type spawn struct {
	steps    []provider.Work
	texted   bool
	streamed strings.Builder
}

// turn turns one run's messages into provider events. A native subagent's
// messages carry the tool use that started it, and are its, nested under
// that step, never the run's own.
type turn struct {
	workspace    string
	now          func() time.Time
	steps        []provider.Work          // tool uses still open, in the order they opened
	plans        map[string]provider.Plan // TodoWrite uses awaiting their results, by tool use
	texted       bool                     // a text block already streamed
	signedOut    string                   // what Claude Code said when the API refused its login
	tokens       int64                    // what the results before the last one used
	used         int64                    // the context the turn's last request sent; 0 before one, or since a compaction
	model        string                   // the model that request went to
	cacheExpires time.Time                // when that request's prompt cache expires; zero when unknown
	cache        *cacheState              // the process's, which outlives the turn; nil in tests that need none
	interrupted  bool                     // the user stopped the turn
	spawns       map[string]*spawn        // native subagents, by the tool use that started each
}

func newTurn(workspace string) *turn {
	return &turn{workspace: workspace}
}

func (t *turn) read(m message) []provider.Event {
	if m.ParentToolUseID != nil {
		return t.nested(*m.ParentToolUseID, m)
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
		// AskUserQuestion shows as its question card, and TodoWrite as
		// the plan, not as steps.
		var evs []provider.Event
		if m.Message != nil {
			evs = t.sent(&m.Message.request)
		}
		for _, b := range m.content() {
			if b.Type == "tool_use" && b.Name == planTool {
				if p, ok := plan(b); ok {
					if t.plans == nil {
						t.plans = map[string]provider.Plan{}
					}
					t.plans[b.ID] = p
				}
				continue
			}
			if b.Type == "tool_use" && b.Name != askTool {
				w := t.open(b)
				t.steps = append(t.steps, w)
				evs = append(evs, w)
				if slices.Contains(spawnTools, b.Name) {
					if t.spawns == nil {
						t.spawns = map[string]*spawn{}
					}
					t.spawns[b.ID] = &spawn{}
				}
			}
		}
		return evs
	case "user":
		var evs []provider.Event
		for _, b := range m.content() {
			// A plan is the thread's once its TodoWrite succeeds: one the
			// user denied, or the tool refused, never was.
			if p, ok := t.plans[b.ToolUseID]; ok && b.Type == "tool_result" {
				delete(t.plans, b.ToolUseID)
				if !b.IsError {
					evs = append(evs, p)
				}
				continue
			}
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
	case "rate_limit_event":
		if t.cache != nil && t.cache.limited != nil && m.RateLimit != nil {
			t.cache.limited.Store(m.RateLimit.Status != "allowed")
		}
	case "result":
		evs := append(t.unfinished(), t.measured(m)...)
		return append(evs, t.end(m))
	case "system":
		if outcome, ok := taskEnds[m.Status]; ok && m.Subtype == "task_notification" && t.spawns[m.ToolUseID] != nil {
			return append(t.close(m.ToolUseID, t.spawns[m.ToolUseID]), provider.Nested{Parent: m.ToolUseID, Event: provider.TurnEnded{Outcome: outcome, Tokens: m.Usage.Total}})
		}
		if m.Subtype == "compact_boundary" {
			t.used = 0
			t.cacheExpires = time.Time{}
			ev := provider.Context{Compacted: true}
			if m.CompactMetadata != nil {
				ev.PreTokens, ev.PostTokens = m.CompactMetadata.PreTokens, m.CompactMetadata.PostTokens
			}
			return []provider.Event{ev}
		}
	}
	return nil
}

// unfinished fails the steps still open, its native subagents' too, for a
// turn that ends without their results.
func (t *turn) unfinished() []provider.Event {
	var evs []provider.Event
	for _, parent := range slices.Sorted(maps.Keys(t.spawns)) {
		evs = append(evs, t.close(parent, t.spawns[parent])...)
	}
	for _, w := range t.steps {
		w.Status = api.WorkFailed
		evs = append(evs, w)
	}
	t.steps = nil
	return evs
}

// close fails the steps s still has open, under parent.
func (t *turn) close(parent string, s *spawn) []provider.Event {
	var evs []provider.Event
	for _, w := range s.steps {
		w.Status = api.WorkFailed
		evs = append(evs, provider.Nested{Parent: parent, Event: w})
	}
	s.steps = nil
	return evs
}

// nested is a message of the native subagent parent started, as its own
// events: its text, and its steps as they open and finish. A message under
// any other parent, or none of the turn's, is nobody's.
func (t *turn) nested(parent string, m message) []provider.Event {
	s := t.spawns[parent]
	if s == nil {
		return nil
	}
	var evs []provider.Event
	add := func(e provider.Event) { evs = append(evs, provider.Nested{Parent: parent, Event: e}) }
	if e := m.Event; m.Type == "stream_event" && e != nil && e.Type == "content_block_delta" && e.Delta != nil && e.Delta.Type == "text_delta" && e.Delta.Text != "" {
		if s.texted && s.streamed.Len() == 0 {
			add(provider.TextDelta{Text: "\n\n"})
		}
		s.texted = true
		s.streamed.WriteString(e.Delta.Text)
		add(provider.TextDelta{Text: e.Delta.Text})
		return evs
	}
	if e := m.Event; m.Type == "stream_event" && e != nil && e.Type == "content_block_start" && e.ContentBlock != nil && e.ContentBlock.Type == "text" && s.streamed.Len() > 0 {
		add(provider.TextDelta{Text: "\n\n"})
		return evs
	}
	streamed := s.streamed.String()
	if m.Type == "assistant" {
		s.streamed.Reset()
	}
	for _, b := range m.content() {
		switch {
		case m.Type == "assistant" && b.Type == "text" && b.Text != "" && !strings.Contains(streamed, b.Text):
			if s.texted {
				add(provider.TextDelta{Text: "\n\n"})
			}
			s.texted = true
			add(provider.TextDelta{Text: b.Text})
		case m.Type == "assistant" && b.Type == "tool_use":
			w := t.open(b)
			s.steps = append(s.steps, w)
			add(w)
		case m.Type == "user" && b.Type == "tool_result":
			i := slices.IndexFunc(s.steps, func(w provider.Work) bool { return w.Item == b.ToolUseID })
			if i < 0 {
				continue
			}
			w := s.steps[i]
			s.steps = slices.Delete(s.steps, i, i+1)
			w.Status, w.Output = api.WorkCompleted, b.text()
			if b.IsError {
				w.Status = api.WorkFailed
			}
			add(w)
		}
	}
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

// planTool is Claude Code's tool that writes its todo list, the whole
// list each time.
const planTool = "TodoWrite"

// plan is the todo list a TodoWrite use writes, if its input holds one.
func plan(b block) (provider.Plan, bool) {
	var input struct {
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(b.Input, &input); err != nil || input.Todos == nil {
		return provider.Plan{}, false
	}
	p := provider.Plan{Steps: make([]api.PlanStep, len(input.Todos))}
	for i, td := range input.Todos {
		p.Steps[i] = api.PlanStep{Text: td.Content, Status: provider.PlanStatus(td.Status)}
	}
	return p, true
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
