package claude_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

// asked is what a one-shot claude was started with.
type asked struct {
	Argv  []string `json:"argv"`
	Dir   string   `json:"dir"`
	Stdin string   `json:"stdin"`
}

// fakeOneShot plays claude -p --output-format json: it writes what it was
// started with to path and prints envAnswer.
func fakeOneShot(path string) int {
	in, _ := io.ReadAll(os.Stdin)
	wd, _ := os.Getwd()
	b, _ := json.Marshal(asked{Argv: os.Args[1:], Dir: wd, Stdin: string(in)})
	os.WriteFile(path, b, 0o644)
	os.Stdout.WriteString(os.Getenv(envAnswer))
	return 0
}

func oneShotAsk(t *testing.T, answer string, spec provider.AskSpec) (provider.AskResult, asked, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "asked.json")
	t.Setenv(envAsked, path)
	t.Setenv(envAnswer, answer)
	got, err := (&claude.Adapter{Path: must(os.Executable())}).Ask(context.Background(), spec)
	var a asked
	if b, rerr := os.ReadFile(path); rerr == nil {
		json.Unmarshal(b, &a)
	}
	return got, a, err
}

func TestAnAskRunsOneClaudeWithNoToolsAndThePromptOnStdin(t *testing.T) {
	dir := t.TempDir()
	got, a, err := oneShotAsk(t, `{"type":"result","is_error":false,"result":"A title","modelUsage":{"claude-haiku-5-5":{"outputTokens":3}}}`,
		provider.AskSpec{Prompt: "Write a title", System: "Answer in one line.", Dir: dir})
	if err != nil || got != (provider.AskResult{Text: "A title", Model: "claude-haiku-5-5"}) {
		t.Fatalf("Ask = %+v, %v; want the result and the model that ran", got, err)
	}
	want := []string{"-p", "--no-session-persistence", "--tools", "", "--strict-mcp-config", "--output-format", "json", "--model", "haiku", "--system-prompt", "Answer in one line."}
	if !slices.Equal(a.Argv, want) {
		t.Errorf("claude ran with %q, want %q", a.Argv, want)
	}
	if a.Stdin != "Write a title" {
		t.Errorf("stdin was %q", a.Stdin)
	}
	if real, _ := filepath.EvalSymlinks(dir); a.Dir != dir && a.Dir != real {
		t.Errorf("claude ran in %s, want %s", a.Dir, dir)
	}
}

// A model named runs, and is the one answered when claude's usage names
// more than one.
func TestAnAskRunsTheModelItNames(t *testing.T) {
	got, a, err := oneShotAsk(t, `{"result":"ok","modelUsage":{"claude-sonnet-5-5":{},"claude-haiku-5-5":{}}}`, provider.AskSpec{Prompt: "p", Model: "claude-sonnet-5-5"})
	if err != nil || got.Model != "claude-sonnet-5-5" {
		t.Fatalf("Ask = %+v, %v", got, err)
	}
	if i := slices.Index(a.Argv, "--model"); i < 0 || a.Argv[i+1] != "claude-sonnet-5-5" || slices.Contains(a.Argv, "--system-prompt") {
		t.Errorf("claude ran with %q", a.Argv)
	}
}

func TestAnAskClaudeRefusesSaysWhy(t *testing.T) {
	_, _, err := oneShotAsk(t, `{"result":"Not logged in · Please run /login","is_error":true}`, provider.AskSpec{Prompt: "p"})
	if err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("Ask = %v, want claude's own words", err)
	}
	if _, _, err := oneShotAsk(t, "not json", provider.AskSpec{Prompt: "p"}); err == nil {
		t.Fatal("an answer that isn't JSON was taken")
	}
}

func TestAnAskWithNoClaudeIsUnavailable(t *testing.T) {
	ad := &claude.Adapter{Path: filepath.Join(t.TempDir(), "claude")}
	if _, err := ad.Ask(context.Background(), provider.AskSpec{Prompt: "p"}); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatalf("Ask = %v, want ErrUnavailable", err)
	}
}
