package claude

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
)

func questionJSON(t *testing.T, questions any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"questions": questions})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testOptions(labels ...string) []any {
	out := make([]any, 0, len(labels))
	for _, l := range labels {
		out = append(out, map[string]any{"label": l, "description": ""})
	}
	return out
}

func testChoice(text string, opts []any) map[string]any {
	return map[string]any{"question": text, "header": "H", "multiSelect": false, "options": opts}
}

func TestQuestionValidInputs(t *testing.T) {
	t.Run("one single-select question with default kind", func(t *testing.T) {
		in := questionJSON(t, []any{
			map[string]any{
				"question":    "Which color?",
				"header":      "Color",
				"multiSelect": false,
				"options": []any{
					map[string]any{"label": "Red", "description": "Warm"},
					map[string]any{"label": "Blue", "description": "Cool"},
				},
			},
		})
		got, p, err := question("req-1", in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantQ := api.Questions{
			Questions: []api.Question{{
				Text:   "Which color?",
				Header: "Color",
				Multi:  false,
				Options: []api.Option{
					{Label: "Red", About: "Warm"},
					{Label: "Blue", About: "Cool"},
				},
			}},
		}
		wantBody, _ := json.Marshal(wantQ)
		wantReq := provider.RequestOpened{ID: "req-1", Kind: api.KindQuestion, Body: wantBody}
		if !reflect.DeepEqual(got, wantReq) {
			t.Fatalf("got request %+v, want %+v", got, wantReq)
		}
		if p == nil || p.kind != api.KindQuestion || !reflect.DeepEqual(p.questions, wantQ) {
			t.Fatalf("got prompt %+v, want questions %+v", p, wantQ)
		}
	})

	t.Run("explicit choice kind with multi-select and four options", func(t *testing.T) {
		in := questionJSON(t, []any{
			map[string]any{
				"question":    "Which sizes?",
				"header":      "Sizes",
				"kind":        "choice",
				"multiSelect": true,
				"options":     testOptions("XS", "S", "M", "L"),
			},
		})
		got, p, err := question("req-2", in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ID != "req-2" || got.Kind != api.KindQuestion || !p.questions.Questions[0].Multi || len(p.questions.Questions[0].Options) != 4 {
			t.Fatalf("unexpected parsed multi question: req=%+v prompt=%+v", got, p)
		}
	})

	t.Run("four questions at the upper bound", func(t *testing.T) {
		ab := testOptions("A", "B")
		in := questionJSON(t, []any{testChoice("1?", ab), testChoice("2?", ab), testChoice("3?", ab), testChoice("4?", ab)})
		_, p, err := question("req-4", in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(p.questions.Questions) != 4 {
			t.Fatalf("got %d questions, want 4", len(p.questions.Questions))
		}
	})

	t.Run("a comma in a single-select label is allowed", func(t *testing.T) {
		in := questionJSON(t, []any{testChoice("1?", testOptions("A,B", "C"))})
		if _, _, err := question("req-comma", in); err != nil {
			t.Fatalf("unexpected error for single-select comma label: %v", err)
		}
	})
}

func TestQuestionRefusals(t *testing.T) {
	ab := testOptions("A", "B")
	cases := []struct {
		name      string
		raw       json.RawMessage
		questions []any
		wantErr   string
	}{
		{name: "malformed json", raw: json.RawMessage(`{`), wantErr: "unexpected end of JSON input"},
		{name: "no questions", questions: []any{}, wantErr: "0 questions, want 1 to 4"},
		{name: "five questions", questions: []any{testChoice("1?", ab), testChoice("2?", ab), testChoice("3?", ab), testChoice("4?", ab), testChoice("5?", ab)}, wantErr: "5 questions, want 1 to 4"},
		{name: "one option", questions: []any{testChoice("1?", testOptions("A"))}, wantErr: "question 1 has 1 options, want 2 to 4"},
		{name: "five options", questions: []any{testChoice("1?", testOptions("A", "B", "C", "D", "E"))}, wantErr: "question 1 has 5 options, want 2 to 4"},
		{name: "no text", questions: []any{testChoice("", ab)}, wantErr: "question 1 has no text"},
		{name: "no label", questions: []any{testChoice("1?", []any{map[string]any{"description": "x"}, map[string]any{"label": "B"}})}, wantErr: "question 1 has a blank or repeated option"},
		{name: "a repeated label", questions: []any{testChoice("1?", testOptions("A", "A"))}, wantErr: "question 1 has a blank or repeated option"},
		{name: "a repeated question", questions: []any{testChoice("1?", ab), testChoice("1?", ab)}, wantErr: "question 2 repeats an earlier question"},
		{name: "a text question", questions: []any{map[string]any{"question": "1?", "header": "H", "kind": "text"}}, wantErr: "question 1 asks for text, not a choice"},
		{
			name: "a comma in a multi-select label",
			questions: []any{
				testChoice("1?", ab),
				map[string]any{"question": "2?", "header": "H", "multiSelect": true, "options": testOptions("A,B", "C")},
			},
			wantErr: `question 2 takes more than one answer, and Claude Code joins those with commas, so its label "A,B" can't have one`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.raw
			if raw == nil {
				raw = questionJSON(t, tc.questions)
			}
			_, _, err := question("req-bad", raw)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestMisfitDecision(t *testing.T) {
	got := misfit("req-9", errString("0 questions, want 1 to 4"))
	want := decided("req-9", map[string]any{
		"behavior": "deny",
		"message":  "glitched cannot show these questions (0 questions, want 1 to 4). Ask 1 to 4 questions, each with 2 to 4 options of distinct labels, or ask in plain text.",
	})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("misfit = %#v, want %#v", got, want)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
