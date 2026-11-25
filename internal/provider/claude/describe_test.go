package claude_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

// describing has the fake claude answer initialize with response, then
// note what else it's sent, and returns the file it notes it in.
func describing(t *testing.T, response string) string {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_MODEL", "")
	tape := filepath.Join(t.TempDir(), "describe.jsonl")
	os.WriteFile(tape, []byte(`{"sent":{"request":{"subtype":"initialize"},"request_id":"glitched-init","type":"control_request"}}
{"recv":{"type":"control_response","response":{"subtype":"success","request_id":"glitched-init","response":`+response+`}}}
{"eof":true}
`), 0o644)
	t.Setenv(envFake, tape)
	after := filepath.Join(t.TempDir(), "after")
	t.Setenv(envAfter, after)
	return after
}

// Describe says the models the account runs, as Claude Code's initialize
// names them, the default by its words where none else is it, and calls
// none: no prompt goes to claude.
func TestDescribeSaysTheAccountsModels(t *testing.T) {
	after := describing(t, `{"commands":[],"claude_code_version":"2.1.330","models":[`+
		`{"value":"default","resolvedModel":"claude-opus-5-5","displayName":"Default (recommended)","description":"Use the default model (currently Opus 5.5) · $4/$20 per Mtok"},`+
		`{"value":"fable","resolvedModel":"claude-fable-5-2","displayName":"Fable","description":"Fable 5.2 · Most capable for your hardest tasks · $8/$40 per Mtok","supportedEffortLevels":["low","high","max"]},`+
		`{"value":"sonnet","resolvedModel":"claude-sonnet-5-5","displayName":"Sonnet","description":"Sonnet 5.5 · Smart and fast"}]}`)
	ad := &claude.Adapter{Path: must(os.Executable())}
	info, err := ad.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.Model{
		{ID: "claude-fable-5-2", Name: "Fable 5.2", Description: "Most capable for your hardest tasks", Efforts: []provider.Effort{{ID: "low"}, {ID: "high"}, {ID: "max"}}, Speeds: []api.Speed{api.SpeedFast}, SpeedsKnown: true},
		{ID: "claude-sonnet-5-5", Name: "Sonnet 5.5", Description: "Smart and fast", Speeds: []api.Speed{api.SpeedFast}, SpeedsKnown: true},
		{ID: "claude-opus-5-5", Name: "Opus 5.5", Default: true, Speeds: []api.Speed{api.SpeedFast}, SpeedsKnown: true},
	}
	if !reflect.DeepEqual(info.Models, want) || info.Name != "claude" || info.Version != "2.1.330" {
		t.Errorf("Describe = %+v; want claude 2.1.330 running %+v", info, want)
	}
	if sent, _ := os.ReadFile(after); len(sent) > 0 {
		t.Errorf("Describe sent claude %q past initialize", sent)
	}
}

// An initialize that doesn't name the models is no answer: Describe says
// so, rather than that the account runs none.
func TestDescribeOfAnAnswerWithoutModelsFails(t *testing.T) {
	describing(t, `{"commands":[],"claude_code_version":"2.1.330"}`)
	ad := &claude.Adapter{Path: must(os.Executable())}
	if info, err := ad.Describe(ctx); err == nil {
		t.Errorf("Describe = %+v, want an error", info)
	}
	describing(t, `{"commands":[],"claude_code_version":"2.1.330","models":[]}`)
	if info, err := ad.Describe(ctx); err != nil || info.Models == nil || len(info.Models) != 0 {
		t.Errorf("Describe of an account with no models = %+v, %v; want none and no error", info, err)
	}
}

// A Claude Code whose launch setting names its folder runs the model that
// folder's settings name, though the engine's own CLAUDE_CONFIG_DIR names
// another: Describe's Default and DefaultModel are that one.
func TestTheDefaultIsTheLaunchFoldersSettings(t *testing.T) {
	describing(t, `{"commands":[],"claude_code_version":"2.1.330","models":[`+
		`{"value":"default","resolvedModel":"claude-fable-5-2","displayName":"Default (recommended)"},`+
		`{"value":"fable","resolvedModel":"claude-fable-5-2","displayName":"Fable","description":"Fable 5.2 · Most capable"},`+
		`{"value":"sonnet","resolvedModel":"claude-sonnet-5-5","displayName":"Sonnet","description":"Sonnet 5.5 · Smart and fast"},`+
		`{"value":"haiku","resolvedModel":"claude-haiku-5-5","displayName":"Haiku","description":"Haiku 5.5 · Fastest"}]}`)
	engines := os.Getenv("CLAUDE_CONFIG_DIR")
	os.WriteFile(filepath.Join(engines, "settings.json"), []byte(`{"model":"haiku"}`), 0o600)
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"model":"sonnet"}`), 0o600)
	ad := &claude.Adapter{}
	ad.Place(provider.Launch{Path: must(os.Executable()), Home: home})
	info, err := ad.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var defaults []string
	for _, m := range info.Models {
		if m.Default {
			defaults = append(defaults, m.ID)
		}
	}
	if !reflect.DeepEqual(defaults, []string{"claude-sonnet-5-5"}) {
		t.Errorf("with the launch folder's settings naming sonnet, the Default is %q", defaults)
	}
	if got, err := ad.DefaultModel(ctx, t.TempDir()); err != nil || got != "claude-sonnet-5-5" {
		t.Errorf("DefaultModel = %q, %v; want claude-sonnet-5-5", got, err)
	}
}

// A workspace's settings may move what an alias names, as
// ANTHROPIC_DEFAULT_OPUS_MODEL does: DefaultModel resolves the workspace's
// model by what a claude there says, not by what Describe's claude said.
func TestAWorkspacesAliasIsTheOneAClaudeThereResolves(t *testing.T) {
	describing(t, `{"commands":[],"claude_code_version":"2.1.330","models":[`+
		`{"value":"default","resolvedModel":"claude-opus-5-5","displayName":"Default (recommended)"},`+
		`{"value":"opus","resolvedModel":"claude-opus-5-5","displayName":"Opus","description":"Opus 5.5 · Best"}]}`)
	ad := &claude.Adapter{Path: must(os.Executable())}
	if _, err := ad.Describe(ctx); err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, ".claude"), 0o755)
	os.WriteFile(filepath.Join(ws, ".claude", "settings.json"), []byte(`{"model":"opus","env":{"ANTHROPIC_DEFAULT_OPUS_MODEL":"claude-opus-4-8"}}`), 0o600)
	describing(t, `{"commands":[],"claude_code_version":"2.1.330","models":[`+
		`{"value":"default","resolvedModel":"claude-opus-5-5","displayName":"Default (recommended)"},`+
		`{"value":"opus","resolvedModel":"claude-opus-4-8","displayName":"Opus","description":"Opus 4.8 · Best"}]}`)
	if got, err := ad.DefaultModel(ctx, ws); err != nil || got != "claude-opus-4-8" {
		t.Errorf("DefaultModel = %q, %v; want the claude-opus-4-8 a claude in the workspace resolves opus to", got, err)
	}
}
