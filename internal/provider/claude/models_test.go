package claude

import (
	"context"
	"slices"
	"testing"

	"glitched/internal/provider"
)

// Claude Code's initialize names the account's models as /model lists
// them: each goes by its full id, in Claude Code's own words less the
// price, and the default, one of the others, goes.
func TestInitializeReportsTheAccountsModels(t *testing.T) {
	out, _ := printed(t, `{"type":"control_response","response":{"subtype":"success","request_id":"`+initRequest+`","response":{"commands":[],"claude_code_version":"2.1.330","models":[`+
		`{"value":"default","resolvedModel":"claude-opus-5-5","displayName":"Default (recommended)","description":"Use the default model (currently Opus 5.5) · $4/$20 per Mtok"},`+
		`{"value":"opus","resolvedModel":"claude-opus-5-5","displayName":"Opus","description":"Opus 5.5 · Best for everyday, complex tasks · $4/$20 per Mtok"},`+
		`{"value":"fable","resolvedModel":"claude-fable-5-2","displayName":"Fable","description":"Fable 5.2 · Most capable for your hardest and longest-running tasks"},`+
		`{"value":"haiku","resolvedModel":"claude-haiku-5-5","displayName":"Haiku","description":"Haiku 5.5 · Fastest for quick answers · $0.10/$0.50 per Mtok ($0.50/$2.50 for prompts over 100k)"},`+
		`{"value":"claude-next","displayName":"Next","description":"Coming soon"}]}}}
`)
	r, err := readInitialized(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.Model{
		{ID: "claude-opus-5-5", Name: "Opus 5.5", Description: "Best for everyday, complex tasks"},
		{ID: "claude-fable-5-2", Name: "Fable 5.2", Description: "Most capable for your hardest and longest-running tasks"},
		{ID: "claude-haiku-5-5", Name: "Haiku 5.5", Description: "Fastest for quick answers"},
		{ID: "claude-next", Name: "Next", Description: "Coming soon"},
	}
	if got := r.models(); !slices.Equal(got, want) || r.Version != "2.1.330" {
		t.Errorf("models = %+v, version %q; want %+v", got, r.Version, want)
	}
}
