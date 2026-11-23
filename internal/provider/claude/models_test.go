package claude

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"glitched/internal/provider"
)

// Claude Code's initialize names the account's models as /model lists
// them: each goes by its full id, in Claude Code's own words less the
// price, and the default's entry goes: the one it names is the Default.
func TestInitializeReportsTheAccountsModels(t *testing.T) {
	unconfigured(t)
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
	if got := r.models(r.defaultModel(os.Getenv, "")); !reflect.DeepEqual(got, want) || r.Version != "2.1.330" {
		t.Errorf("models = %+v, version %q; want %+v", got, r.Version, want)
	}
}

// unconfigured has claude's settings and environment name no model.
func unconfigured(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("ANTHROPIC_MODEL", "")
	return dir
}

func writeSettings(t *testing.T, path, settings string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The Default is the model claude runs given no --model: ANTHROPIC_MODEL,
// then its settings' env, then their model, local over project over
// user, else /model's default; an alias as /model resolves it, a
// long-context one as its base's with [1m]. One /model doesn't list is
// listed by its id, a long-context one under its base's name.
func TestTheDefaultIsTheModelClaudeRunsGivenNone(t *testing.T) {
	init := func(def string) initialized {
		out, _ := printed(t, `{"type":"control_response","response":{"subtype":"success","request_id":"`+initRequest+`","response":{"commands":[],"claude_code_version":"2.1.330","models":[`+
			`{"value":"default","resolvedModel":"`+def+`","displayName":"Default (recommended)"},`+
			`{"value":"opus","resolvedModel":"claude-opus-5-5","displayName":"Opus","description":"Opus 5.5 · Best"},`+
			`{"value":"sonnet","resolvedModel":"claude-sonnet-5-5","displayName":"Sonnet"},`+
			`{"value":"sonnet[1m]","resolvedModel":"claude-sonnet-5-5[1m]","displayName":"Sonnet (1M context)"},`+
			`{"value":"haiku","resolvedModel":"claude-haiku-5-5","displayName":"Haiku"}]}}}
`)
		r, err := readInitialized(context.Background(), out)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, tc := range []struct {
		name, def, env, user, project, local, want, wantName string
	}{
		{name: "the catalog's", def: "claude-haiku-5-5", want: "claude-haiku-5-5"},
		{name: "a listed long-context default", def: "claude-sonnet-5-5[1m]", want: "claude-sonnet-5-5[1m]"},
		{name: "an unlisted long-context default", def: "claude-opus-5-5[1m]", want: "claude-opus-5-5[1m]", wantName: "Opus 5.5 (1M context)"},
		{name: "none", def: "", want: ""},
		{name: "the user's alias", def: "claude-opus-5-5", user: `{"model":"sonnet"}`, want: "claude-sonnet-5-5"},
		{name: "the user's long-context alias", def: "claude-haiku-5-5", user: `{"model":"opus[1m]"}`, want: "claude-opus-5-5[1m]", wantName: "Opus 5.5 (1M context)"},
		{name: "settings' env over model", def: "claude-opus-5-5", user: `{"model":"sonnet","env":{"ANTHROPIC_MODEL":"haiku"}}`, want: "claude-haiku-5-5"},
		{name: "the environment over settings", def: "claude-opus-5-5", env: "claude-fable-5-2", user: `{"env":{"ANTHROPIC_MODEL":"haiku"}}`, want: "claude-fable-5-2", wantName: "claude-fable-5-2"},
		{name: "the project's over the user's", def: "claude-opus-5-5", user: `{"model":"sonnet"}`, project: `{"model":"haiku"}`, want: "claude-haiku-5-5"},
		{name: "the local over the project's", def: "claude-opus-5-5", project: `{"model":"haiku"}`, local: `{"model":"sonnet[1m]"}`, want: "claude-sonnet-5-5[1m]"},
		{name: "a project's env over the user's", def: "claude-opus-5-5", user: `{"env":{"ANTHROPIC_MODEL":"sonnet"}}`, project: `{"env":{"ANTHROPIC_MODEL":"haiku"}}`, want: "claude-haiku-5-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := unconfigured(t)
			t.Setenv("ANTHROPIC_MODEL", tc.env)
			workspace := t.TempDir()
			if tc.user != "" {
				writeSettings(t, filepath.Join(dir, "settings.json"), tc.user)
			}
			if tc.project != "" {
				writeSettings(t, filepath.Join(workspace, ".claude", "settings.json"), tc.project)
			}
			if tc.local != "" {
				writeSettings(t, filepath.Join(workspace, ".claude", "settings.local.json"), tc.local)
			}
			r := init(tc.def)
			got := r.defaultModel(os.Getenv, workspace)
			if got != tc.want {
				t.Fatalf("the default is %q, want %q", got, tc.want)
			}
			var defaults []provider.Model
			for _, m := range r.models(got) {
				if m.Default {
					defaults = append(defaults, m)
				}
			}
			if tc.want == "" {
				if len(defaults) != 0 {
					t.Errorf("the Default is %+v, want none", defaults)
				}
				return
			}
			if len(defaults) != 1 || defaults[0].ID != tc.want || tc.wantName != "" && defaults[0].Name != tc.wantName {
				t.Errorf("the Default is %+v, want %s named %q", defaults, tc.want, tc.wantName)
			}
		})
	}
}
