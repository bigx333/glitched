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
	"time"

	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

// asked is what a one-shot claude was started with.
type asked struct {
	Argv    []string `json:"argv"`
	Dir     string   `json:"dir"`
	Entries []string `json:"entries"`
	Stdin   string   `json:"stdin"`
}

// fakeOneShot plays claude -p --output-format json: it writes what it was
// started with to path and prints envAnswer.
func fakeOneShot(path string) int {
	in, _ := io.ReadAll(os.Stdin)
	wd, _ := os.Getwd()
	var entries []string
	if es, err := os.ReadDir(wd); err == nil {
		for _, e := range es {
			entries = append(entries, e.Name())
		}
	}
	b, _ := json.Marshal(asked{Argv: os.Args[1:], Dir: wd, Entries: entries, Stdin: string(in)})
	os.WriteFile(path, b, 0o644)
	os.Stdout.WriteString(os.Getenv(envAnswer))
	if os.Getenv(envHang) != "" {
		time.Sleep(time.Minute)
	}
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
	got, a, err := oneShotAsk(t, `{"type":"result","is_error":false,"result":"A title","modelUsage":{"claude-haiku-5-5":{"outputTokens":3}}}`,
		provider.AskSpec{Prompt: "Write a title", System: "Answer in one line."})
	if err != nil || got != (provider.AskResult{Text: "A title", Model: "claude-haiku-5-5"}) {
		t.Fatalf("Ask = %+v, %v; want the result and the model that ran", got, err)
	}
	want := []string{"-p", "--no-session-persistence", "--tools", "", "--strict-mcp-config", "--setting-sources", "user", "--settings", `{"disableAllHooks":true}`, "--output-format", "json", "--model", "haiku", "--system-prompt", "Answer in one line."}
	if !slices.Equal(a.Argv, want) {
		t.Errorf("claude ran with %q, want %q", a.Argv, want)
	}
	if a.Stdin != "Write a title" {
		t.Errorf("stdin was %q", a.Stdin)
	}
}

// An ask reads no project: claude runs in an empty folder made for it and
// gone after, with the user's settings alone, never a project's CLAUDE.md,
// settings or hooks.
func TestAnAskRunsInAnEmptyFolderWithTheUsersSettingsOnly(t *testing.T) {
	_, a, err := oneShotAsk(t, `{"result":"ok"}`, provider.AskSpec{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(a.Argv, "--setting-sources"); i < 0 || a.Argv[i+1] != "user" || slices.Contains(a.Argv, "--bare") {
		t.Errorf("claude ran with %q, want --setting-sources user and no --bare, which drops its sign-in", a.Argv)
	}
	if i := slices.Index(a.Argv, "--settings"); i < 0 || a.Argv[i+1] != `{"disableAllHooks":true}` {
		t.Errorf("claude ran with %q, want the user's hooks off", a.Argv)
	}
	tmp, _ := filepath.EvalSymlinks(os.TempDir())
	if real, _ := filepath.EvalSymlinks(filepath.Dir(a.Dir)); real != tmp || !strings.HasPrefix(filepath.Base(a.Dir), "glitched-ask-") || len(a.Entries) != 0 {
		t.Errorf("claude ran in %s holding %q, want a new empty folder in %s", a.Dir, a.Entries, tmp)
	}
	if _, err := os.Stat(a.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s is left behind: %v", a.Dir, err)
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

// An ask whose claude is stopped at its deadline fails, though it printed
// an answer first.
func TestAnAskKilledAtItsDeadlineFails(t *testing.T) {
	t.Setenv(envAsked, filepath.Join(t.TempDir(), "asked.json"))
	t.Setenv(envAnswer, `{"result":"printed before the deadline"}`)
	t.Setenv(envHang, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	got, err := (&claude.Adapter{Path: must(os.Executable())}).Ask(ctx, provider.AskSpec{Prompt: "p"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Ask = %+v, %v; want the deadline's error", got, err)
	}
}

func TestAnAskWithNoClaudeIsUnavailable(t *testing.T) {
	ad := &claude.Adapter{Path: filepath.Join(t.TempDir(), "claude")}
	if _, err := ad.Ask(context.Background(), provider.AskSpec{Prompt: "p"}); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatalf("Ask = %v, want ErrUnavailable", err)
	}
}
