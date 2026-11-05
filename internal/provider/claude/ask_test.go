package claude_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/api"
)

const askPrompt = "Call the AskUserQuestion tool once, with two questions in that one call: 'Which color?' (header Color, options Red and Blue, single select) and 'Which sizes?' (header Sizes, options S, M and L, multiSelect true). Do not use any other tool. After the tool returns, reply done."

var (
	// The user types their own color and picks two sizes, at full access,
	// where Claude Code still asks.
	answered = promptScenario{
		name:   "ask-answer",
		prompt: askPrompt,
		access: api.AccessFull,
		answer: func(requestLog) json.RawMessage {
			return answersJSON(api.Answer{Other: "Teal"}, api.Answer{Picked: []string{"S", "M"}})
		},
		replied: `{"behavior":"allow","updatedInput":{"answers":{"Which color?":"Teal","Which sizes?":["S","M"]}}}`,
		check: func(t *testing.T, got promptLog) {
			checkAsked(t, got, api.RequestDelivered)
			if got.ended.Outcome != api.OutcomeCompleted {
				t.Errorf("turn ended %+v, want completed", got.ended)
			}
		},
		pin: func(t *testing.T, tape string) {
			r := askResult(t, tape)
			if r.IsError || !strings.Contains(r.Content, `"Which color?"="Teal"`) || !strings.Contains(r.Content, `"Which sizes?"="S,M"`) {
				t.Errorf("Claude Code told the model %+v, want the typed color and both sizes", r)
			}
		},
	}

	// The user dismisses the questions.
	dismissed = promptScenario{
		name:    "ask-dismiss",
		prompt:  askPrompt,
		answer:  func(requestLog) json.RawMessage { return answersJSON() },
		replied: `{"behavior":"deny","message":"The user dismissed the questions without answering."}`,
		check: func(t *testing.T, got promptLog) {
			checkAsked(t, got, api.RequestDelivered)
			if got.ended.Outcome != api.OutcomeCompleted {
				t.Errorf("turn ended %+v, want completed", got.ended)
			}
		},
		pin: func(t *testing.T, tape string) {
			if r := askResult(t, tape); !r.IsError || r.Content != "The user dismissed the questions without answering." {
				t.Errorf("Claude Code told the model %+v, want the dismissal as an error", r)
			}
		},
	}
)

func answersJSON(a ...api.Answer) json.RawMessage {
	b, _ := json.Marshal(api.Answers{Answers: append([]api.Answer{}, a...)})
	return b
}

// checkAsked fails unless the turn opened one question request, in state,
// of the two questions the prompt asks for, and showed no step for the
// tool that asked them.
func checkAsked(t *testing.T, got promptLog, state api.RequestState) {
	t.Helper()
	if len(got.requests) != 1 {
		t.Fatalf("requests %+v, want one", got.requests)
	}
	r := got.requests[0]
	q, err := api.ParseQuestions(r.opened.Body)
	switch {
	case r.opened.Kind != api.KindQuestion || err != nil:
		t.Errorf("request %+v (%v), want questions the card can show", r.opened, err)
	case len(q.Questions) != 2 || q.Questions[0].Text != "Which color?" || q.Questions[0].Multi || !q.Questions[1].Multi ||
		q.Questions[0].Header != "Color" || len(q.Questions[1].Options) != 3 || q.Questions[1].Options[0].Label != "S":
		t.Errorf("questions %+v, want Which color? of Red and Blue, then Which sizes? of S, M and L, multi", q)
	}
	if r.state != state {
		t.Errorf("the question is %v, want %v", r.state, state)
	}
	for _, w := range got.work {
		if strings.HasPrefix(w.Title, "AskUserQuestion") {
			t.Errorf("the turn shows a step %+v for the questions, want only their card", w)
		}
	}
}

// checkAnswered fails unless Claude Code got s.replied, and an allow gives
// back the tool's input as it came with the answers added.
func checkAnswered(t *testing.T, s promptScenario, got reply, input json.RawMessage) {
	t.Helper()
	var want struct {
		Behavior     string
		Message      string
		UpdatedInput map[string]json.RawMessage
	}
	json.Unmarshal([]byte(s.replied), &want)
	var updated map[string]json.RawMessage
	json.Unmarshal(got.UpdatedInput, &updated)
	var in map[string]json.RawMessage
	json.Unmarshal(input, &in)
	if updated != nil {
		in["answers"] = updated["answers"]
	}
	switch {
	case got.subtype != "success" || got.Behavior != want.Behavior || got.Message != want.Message:
		t.Errorf("Claude Code got %+v, want %s", got, s.replied)
	case want.Behavior == "allow" && !sameJSON(updated["answers"], want.UpdatedInput["answers"]):
		t.Errorf("Claude Code got the answers %s, want %s", updated["answers"], want.UpdatedInput["answers"])
	case want.Behavior == "allow" && !sameJSON(got.UpdatedInput, must(json.Marshal(in))):
		t.Errorf("Claude Code got the input %s, want its own %s with the answers", got.UpdatedInput, input)
	}
}

// askShaped is whether input is AskUserQuestion's as the adapter reads it.
func askShaped(input json.RawMessage) bool {
	var in struct {
		Questions []struct {
			Question, Header *string
			MultiSelect      *bool
			Options          []struct{ Label *string }
		}
	}
	if json.Unmarshal(input, &in) != nil || len(in.Questions) == 0 {
		return false
	}
	for _, q := range in.Questions {
		if q.Question == nil || q.Header == nil || q.MultiSelect == nil || len(q.Options) < 2 {
			return false
		}
		for _, o := range q.Options {
			if o.Label == nil {
				return false
			}
		}
	}
	return true
}

type toolResult struct {
	Content string
	IsError bool `json:"is_error"`
}

// askResult is the tool_result Claude Code gave the model for the
// AskUserQuestion in the transcript at tape.
func askResult(t *testing.T, tape string) toolResult {
	t.Helper()
	var id string
	for _, l := range strings.Split(string(must(os.ReadFile(tape))), "\n") {
		var e struct {
			Recv *struct {
				Type    string
				Message *struct {
					Content []struct {
						Type, ID, Name string
						ToolUseID      string `json:"tool_use_id"`
						toolResult
					}
				}
			}
		}
		if json.Unmarshal([]byte(l), &e) != nil || e.Recv == nil || e.Recv.Message == nil {
			continue
		}
		for _, b := range e.Recv.Message.Content {
			switch {
			case b.Type == "tool_use" && b.Name == "AskUserQuestion":
				id = b.ID
			case b.Type == "tool_result" && b.ToolUseID == id && id != "":
				return b.toolResult
			}
		}
	}
	t.Fatalf("%s has no tool_result for AskUserQuestion", tape)
	return toolResult{}
}

func TestClaudeGetsTheAnswersByQuestionText(t *testing.T) { replayed(t, answered) }

func TestClaudeHearsTheUserDismissedTheQuestions(t *testing.T) { replayed(t, dismissed) }

func TestStoppingATurnVoidsItsQuestions(t *testing.T) {
	replay(t, answered.name, "")
	replies := stdin(t)
	h := host(t, nil)
	thread := newThread(t, h, api.AccessFull)
	got := playOn(t, h, thread, answered.prompt, func(requestLog) bool { return true })
	checkAsked(t, got, api.RequestInvalidated)
	if got.ended.Outcome != api.OutcomeInterrupted {
		t.Errorf("the turn ended %+v, want interrupted", got.ended)
	}
	if r := replies(); len(r) != 0 {
		t.Errorf("Claude Code got %+v after the turn stopped, want nothing", r)
	}
}

// Claude Code cancels its questions, as when a hook of the user's
// answered them: the card goes void, and Claude Code gets no reply.
func TestACancelledQuestionGoesVoid(t *testing.T) {
	tape := rewrite(t, answered.name, func(m map[string]any) []map[string]any {
		if r := canUseTool(m); r != nil {
			return []map[string]any{m, {"recv": map[string]any{"type": "control_cancel_request", "request_id": m["recv"].(map[string]any)["request_id"]}}}
		}
		if sent, _ := m["sent"].(map[string]any); sent != nil && sent["type"] == "control_response" {
			return nil
		}
		return []map[string]any{m}
	})
	t.Setenv(envFake, tape)
	replies := stdin(t)
	h := host(t, nil)
	thread := newThread(t, h, api.AccessFull)
	got := turn(t, h, thread, answered.prompt)
	checkAsked(t, got, api.RequestInvalidated)
	if r := got.requests[0]; !strings.Contains(r.reason, "no longer asks") {
		t.Errorf("the question went void with %q, want that Claude Code no longer asks", r.reason)
	}
	if got.ended.Outcome != api.OutcomeCompleted {
		t.Errorf("turn ended %+v, want completed", got.ended)
	}
	if r := replies(); len(r) != 0 {
		t.Errorf("Claude Code got %+v for a request it cancelled, want nothing", r)
	}
}

// Questions the card can't show get a deny that says why, so the turn
// goes on, and open no card.
func TestQuestionsTheCardCantShowAreRefused(t *testing.T) {
	options := func(labels ...string) []any {
		var o []any
		for _, l := range labels {
			o = append(o, map[string]any{"label": l, "description": ""})
		}
		return o
	}
	q := func(text string, opts []any) map[string]any {
		return map[string]any{"question": text, "header": "H", "multiSelect": false, "options": opts}
	}
	ab := options("A", "B")
	for _, c := range []struct {
		name      string
		questions []any
		why       string
	}{
		{"five questions", []any{q("1?", ab), q("2?", ab), q("3?", ab), q("4?", ab), q("5?", ab)}, "5 questions, want 1 to 4"},
		{"no questions", []any{}, "0 questions, want 1 to 4"},
		{"one option", []any{q("1?", options("A"))}, "question 1 has 1 options, want 2 to 4"},
		{"five options", []any{q("1?", options("A", "B", "C", "D", "E"))}, "question 1 has 5 options, want 2 to 4"},
		{"no text", []any{q("", ab)}, "question 1 has no text"},
		{"no label", []any{q("1?", []any{map[string]any{"description": "x"}, map[string]any{"label": "B"}})}, "question 1 has a blank or repeated option"},
		{"a repeated label", []any{q("1?", options("A", "A"))}, "question 1 has a blank or repeated option"},
		{"a repeated question", []any{q("1?", ab), q("1?", ab)}, "question 2 repeats an earlier question"},
		{"a text question", []any{map[string]any{"question": "1?", "header": "H", "kind": "text"}}, "question 1 asks for text, not a choice"},
		{"a comma in a multi-select label", []any{q("1?", ab), map[string]any{"question": "2?", "header": "H", "multiSelect": true, "options": options("A,B", "C")}},
			`question 2 takes more than one answer, and Claude Code joins those with commas, so its label "A,B" can't have one`},
	} {
		t.Run(c.name, func(t *testing.T) {
			tape := rewrite(t, dismissed.name, func(m map[string]any) []map[string]any {
				if r := canUseTool(m); r != nil {
					r["input"] = map[string]any{"questions": c.questions}
				}
				return []map[string]any{m}
			})
			t.Setenv(envFake, tape)
			replies := stdin(t)
			h := host(t, nil)
			got := turn(t, h, newThread(t, h, api.AccessAsk), dismissed.prompt)
			if len(got.requests) != 0 {
				t.Errorf("requests %+v, want no card", got.requests)
			}
			if got.ended.Outcome != api.OutcomeCompleted {
				t.Errorf("turn ended %+v, want completed", got.ended)
			}
			var all []reply
			for _, r := range replies() {
				all = append(all, r...)
			}
			if len(all) != 1 || all[0].subtype != "success" || all[0].Behavior != "deny" ||
				!strings.HasPrefix(all[0].Message, "glitched cannot show these questions (") || !strings.Contains(all[0].Message, c.why) {
				t.Errorf("Claude Code got %+v, want one deny that says %q", all, c.why)
			}
		})
	}
}

// rewrite is the transcript name with each line as edit makes it, in a
// file of the test's.
func rewrite(t *testing.T, name string, edit func(map[string]any) []map[string]any) string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(must(os.ReadFile(filepath.Join("testdata", name+".jsonl"))))), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		for _, e := range edit(m) {
			out = append(out, string(must(json.Marshal(e))))
		}
	}
	path := filepath.Join(t.TempDir(), name+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// canUseTool is the request of a transcript line that receives a
// can_use_tool, or nil.
func canUseTool(m map[string]any) map[string]any {
	recv, _ := m["recv"].(map[string]any)
	if recv == nil || recv["type"] != "control_request" {
		return nil
	}
	r, _ := recv["request"].(map[string]any)
	if r == nil || r["subtype"] != "can_use_tool" {
		return nil
	}
	return r
}
