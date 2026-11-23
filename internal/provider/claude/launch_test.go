package claude_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

// start is how one start of the fake claude was start.
type start struct {
	Exe  string   `json:"exe"`
	Argv []string `json:"argv"`
	Home string   `json:"home"`
	Set  bool     `json:"set"`
}

func noteLaunch(path string) {
	home, set := os.LookupEnv("CLAUDE_CONFIG_DIR")
	b, _ := json.Marshal(start{Exe: os.Args[0], Argv: os.Args[1:], Home: home, Set: set})
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	f.Write(append(b, '\n'))
	f.Close()
}

func startsIn(t *testing.T, path string) []start {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []start
	for sc := bufio.NewScanner(f); sc.Scan(); {
		var s start
		json.Unmarshal(sc.Bytes(), &s)
		out = append(out, s)
	}
	return out
}

// placed is the test binary as claude, at a path with a space, in a
// folder of its own with a space, with arguments in shell quoting, and
// the file its starts are noted in.
func placed(t *testing.T) (*claude.Adapter, provider.Launch, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "my tools")
	home := filepath.Join(t.TempDir(), "claude home")
	for _, d := range []string{dir, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "my claude")
	if err := os.Symlink(must(os.Executable()), bin); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "launches")
	t.Setenv(envLaunch, log)
	l := provider.Launch{Path: bin, Home: home, Args: `--add-dir "/tmp/a b"`}
	ad := &claude.Adapter{}
	ad.Place(l)
	return ad, l, log
}

// Every claude the adapter starts, for a turn, a probe, an ask or its
// version, runs the program Place names, with its folder and arguments.
func TestEveryClaudeStartsAsItsLaunchSays(t *testing.T) {
	ad, l, log := placed(t)
	home := l.Home
	user := []string{"--add-dir", "/tmp/a b"}

	replay(t, "hi", "")
	h := host(t, ad)
	if got := turn(t, h, newThread(t, h, api.AccessAsk), "say hi"); got.ended.Outcome != api.OutcomeCompleted {
		t.Fatalf("turn ended %+v", got.ended)
	}
	describing(t, `{"commands":[],"claude_code_version":"2.1.330","models":[]}`)
	if _, err := ad.Describe(ctx); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envAsked, filepath.Join(t.TempDir(), "asked.json"))
	t.Setenv(envAnswer, `{"type":"result","is_error":false,"result":"A title","modelUsage":{"claude-haiku-5-5":{}}}`)
	if _, err := ad.Ask(context.Background(), provider.AskSpec{Prompt: "Title"}); err != nil {
		t.Fatal(err)
	}
	got := startsIn(t, log)
	if len(got) < 3 {
		t.Fatalf("%d claudes started, want a turn's, a probe's and an ask's: %+v", len(got), got)
	}
	for _, s := range got {
		if !s.Set || s.Home != home {
			t.Errorf("claude %q ran with CLAUDE_CONFIG_DIR %q (set %v), want %q", s.Argv, s.Home, s.Set, home)
		}
		if !slices.Equal(s.Argv[:2], user) {
			t.Errorf("claude ran with %q, want the user's %q first", s.Argv, user)
		}
	}
	turnArgv := got[0].Argv
	if !slices.Contains(turnArgv, "stream-json") || !slices.Contains(turnArgv, "--session-id") && !slices.Contains(turnArgv, "--resume") {
		t.Errorf("the turn's claude ran with %q", turnArgv)
	}
}

func TestVersionAsksTheLaunchedClaude(t *testing.T) {
	ad, l, log := placed(t)
	home := l.Home
	prog, err := ad.Placement().Program(l)
	if err != nil {
		t.Fatal(err)
	}
	v, err := ad.Version(ctx, prog)
	if err != nil || v != "2.1.330 (Claude Code)" {
		t.Fatalf("Version = %q, %v", v, err)
	}
	if got := startsIn(t, log); len(got) != 1 || got[0].Home != home || !slices.Equal(got[0].Argv, []string{"--version"}) {
		t.Errorf("--version ran as %+v", got)
	}
	cmd := ad.Command(prog)
	if cmd[0] != prog.Path || !slices.Contains(cmd, "--permission-mode") || !slices.Contains(cmd, "--session-id") {
		t.Errorf("Command = %q", cmd)
	}
}

// Glitched's own flags can't come from the user's arguments.
func TestTheUsersArgumentsCantSetGlitchedsFlags(t *testing.T) {
	ad := &claude.Adapter{}
	for _, args := range []string{"--permission-mode bypassPermissions", "--dangerously-skip-permissions", "--mcp-config x.json", "--resume abc", "--output-format=text", "--session-id x", "-p"} {
		_, err := ad.Placement().Program(provider.Launch{Path: must(os.Executable()), Args: args})
		var bad *provider.LaunchError
		if !errors.As(err, &bad) || !strings.Contains(bad.Args, "set by Glitched itself") {
			t.Errorf("args %q: %v, want them refused", args, err)
		}
	}
}

// Describe says the path of the claude that answered it, while Place
// moves the program.
func TestDescribeNamesTheClaudeThatAnswered(t *testing.T) {
	ad, l, _ := placed(t)
	describing(t, `{"commands":[],"claude_code_version":"2.1.330","models":[]}`)
	var w sync.WaitGroup
	w.Go(func() {
		for range 200 {
			ad.Place(provider.Launch{Path: l.Path})
			ad.Place(provider.Launch{Path: "/no-such-program"})
		}
	})
	for range 200 {
		if d, err := ad.Describe(t.Context()); err == nil && d.Path != l.Path {
			t.Errorf("Describe answered with %q's models and the path %q", l.Path, d.Path)
			break
		}
	}
	w.Wait()
}

// A claude started with the folder in its arguments isn't the one a
// launch with the folder set as CLAUDE_CONFIG_DIR starts.
func TestAParkedClaudeIsKeptOnlyForTheSameEnvAndArguments(t *testing.T) {
	ad, l, log := placed(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := l.Home
	l.Home, l.Args = "", "CLAUDE_CONFIG_DIR='"+home+"'"
	ad.Place(l)
	keep(t, []string{"hi", "again-resume"}, []string{"again-resume"})
	h := host(t, ad)
	th := newThread(t, h, api.AccessAsk)
	if got := turn(t, h, th, firstAsk); got.ended.Outcome != api.OutcomeCompleted {
		t.Fatalf("first turn %+v", got.ended)
	}
	l.Home, l.Args = home, ""
	ad.Place(l)
	if got := turn(t, h, th, againAsk); got.ended.Outcome != api.OutcomeCompleted {
		t.Fatalf("second turn %+v", got.ended)
	}
	if got := startsIn(t, log); got[len(got)-1].Home != home {
		t.Errorf("the second turn ran in a claude with CLAUDE_CONFIG_DIR %q: %+v", got[len(got)-1].Home, got)
	}
}
