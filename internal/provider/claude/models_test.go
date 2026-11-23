package claude

import (
	"context"
	"reflect"
	"testing"

	"glitched/internal/provider"
)

// Claude Code's initialize names the account's models as /model lists
// them: each goes by its full id, in Claude Code's own words less the
// price, and the default's entry goes: the one it names is the Default.
func TestInitializeReportsTheAccountsModels(t *testing.T) {
	out, _ := printed(t, `{"type":"control_response","response":{"subtype":"success","request_id":"`+initRequest+`","response":{"commands":[],"claude_code_version":"2.1.330","models":[`+
		`{"value":"default","resolvedModel":"claude-opus-5-5","displayName":"Default (recommended)","description":"Use the default model (currently Opus 5.5) · $4/$20 per Mtok"},`+
		`{"value":"opus","resolvedModel":"claude-opus-5-5","displayName":"Opus","description":"Opus 5.5 · Best for everyday, complex tasks · $4/$20 per Mtok"},`+
		`{"value":"fable","resolvedModel":"claude-fable-5-2","displayName":"Fable","description":"Fable 5.2 · Most capable for your hardest and longest-running tasks","supportedEffortLevels":["low","medium","high","xhigh","max"]},`+
		`{"value":"haiku","resolvedModel":"claude-haiku-5-5","displayName":"Haiku","description":"Haiku 5.5 · Fastest for quick answers · $0.10/$0.50 per Mtok ($0.50/$2.50 for prompts over 100k)"},`+
		`{"value":"claude-next","displayName":"Next","description":"Coming soon"}]}}}
`)
	r, err := readInitialized(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.Model{
		{ID: "claude-opus-5-5", Name: "Opus 5.5", Description: "Best for everyday, complex tasks", Default: true},
		{ID: "claude-fable-5-2", Name: "Fable 5.2", Description: "Most capable for your hardest and longest-running tasks", Efforts: []provider.Effort{{ID: "low"}, {ID: "medium"}, {ID: "high"}, {ID: "xhigh"}, {ID: "max"}}},
		{ID: "claude-haiku-5-5", Name: "Haiku 5.5", Description: "Fastest for quick answers"},
		{ID: "claude-next", Name: "Next", Description: "Coming soon"},
	}
	if got := r.models(); !reflect.DeepEqual(got, want) || r.Version != "2.1.330" {
		t.Errorf("models = %+v, version %q; want %+v", got, r.Version, want)
	}
}

// The default is the model its entry resolves to where /model lists it,
// else a long-context default's base; one it lists by no id of its own is
// no Default.
func TestInitializeMarksTheDefaultItResolvesTo(t *testing.T) {
	defaults := func(resolved string) []string {
		out, _ := printed(t, `{"type":"control_response","response":{"subtype":"success","request_id":"`+initRequest+`","response":{"commands":[],"claude_code_version":"2.1.330","models":[`+
			`{"value":"default","resolvedModel":"`+resolved+`","displayName":"Default (recommended)"},`+
			`{"value":"opus","resolvedModel":"claude-opus-5-5","displayName":"Opus"},`+
			`{"value":"sonnet[1m]","resolvedModel":"claude-sonnet-5-5[1m]","displayName":"Sonnet (1M context)"},`+
			`{"value":"haiku","resolvedModel":"claude-haiku-5-5","displayName":"Haiku"}]}}}
`)
		r, err := readInitialized(context.Background(), out)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, m := range r.models() {
			if m.Default {
				ids = append(ids, m.ID)
			}
		}
		return ids
	}
	for resolved, want := range map[string][]string{
		"claude-haiku-5-5":      {"claude-haiku-5-5"},
		"claude-sonnet-5-5[1m]": {"claude-sonnet-5-5[1m]"},
		"claude-opus-5-5[1m]":   {"claude-opus-5-5"},
		"claude-fable-5-2":      nil,
		"":                      nil,
	} {
		if got := defaults(resolved); !reflect.DeepEqual(got, want) {
			t.Errorf("with the default resolving to %q, the Default is %q, want %q", resolved, got, want)
		}
	}
}
