package claude_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/runtime"
	"glitched/internal/skills/skillstest"
)

const native = "native: Claude's own."

// skillDirs is a state folder, a workspace and a home, with their links
// resolved, as macOS's temporary folder is under one. HOME is the home
// from now on, with a skill of Claude Code's own in it.
func skillDirs(t *testing.T) (state, ws, home string) {
	t.Helper()
	tmp := must(filepath.EvalSymlinks(t.TempDir()))
	state, ws, home = filepath.Join(tmp, "state"), filepath.Join(tmp, "work"), filepath.Join(tmp, "home")
	for _, d := range []string{state, ws, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	skillstest.Write(t, filepath.Join(home, ".claude", "skills"), "native", "Claude's own.")
	return state, ws, home
}

// seeing has claudes started from now on say the skills they see, and
// returns what each saw so far.
func seeing(t *testing.T) func() [][]string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "seen")
	t.Setenv(envSkills, file)
	return func() [][]string { return seenIn(t, file) }
}

func seenIn(t *testing.T, file string) [][]string {
	t.Helper()
	var out [][]string
	data, _ := os.ReadFile(file)
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var seen []string
		if l != "" {
			if err := json.Unmarshal([]byte(l), &seen); err != nil {
				t.Fatal(err)
			}
			out = append(out, seen)
		}
	}
	return out
}

// started is the argv of each claude started, in order.
func startedWith(t *testing.T, argv string) [][]string {
	t.Helper()
	var out [][]string
	for _, l := range strings.Split(strings.TrimSpace(string(must(os.ReadFile(argv)))), "\n") {
		var args []string
		json.Unmarshal([]byte(l), &args)
		out = append(out, args)
	}
	return out
}

// pluginDirs is the --plugin-dir each claude started with, "" for none.
func pluginDirs(t *testing.T, argv string) []string {
	t.Helper()
	var out []string
	for _, args := range startedWith(t, argv) {
		dir := ""
		if i := slices.Index(args, "--plugin-dir"); i >= 0 {
			dir = args[i+1]
		}
		out = append(out, dir)
	}
	return out
}

func askThreadIn(t *testing.T, h *runtime.Host, ws string) api.ID {
	t.Helper()
	thread := api.NewID()
	submit(t, h, thread, api.ThreadCreate{Workspace: ws, Title: "hi", Access: api.AccessAsk, Model: api.ModelRef{Provider: "claude", Model: "haiku"}})
	return thread
}

// byHand is what a claude the user starts in ws sees, with args, which
// are a run's without Glitched's skills.
func byHand(t *testing.T, ws string, args []string) []string {
	t.Helper()
	if i := slices.Index(args, "--plugin-dir"); i >= 0 {
		args = slices.Delete(slices.Clone(args), i, i+2)
	}
	file := filepath.Join(t.TempDir(), "seen")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, must(os.Executable()), args...)
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), envSkills+"="+file, envArgv+"=", envPids+"=")
	cmd.Stdin = strings.NewReader("")
	cmd.Run()
	seen := seenIn(t, file)
	if len(seen) != 1 {
		t.Fatalf("the claude started by hand saw %v", seen)
	}
	return seen[0]
}

func turnEnds(t *testing.T, h *runtime.Host, thread api.ID, text string) {
	t.Helper()
	if got := turn(t, h, thread, text); got.ended.Outcome != api.OutcomeCompleted {
		t.Fatalf("turn: %+v", got)
	}
}

// Claude Code gets the user's skills and the project's, which win by
// name, as a plugin of the run's alone: a claude the user starts in the
// same folder with the same home sees only its own, and the run writes
// nothing in either. A skill that fails a check isn't given; one the user
// links to from elsewhere is.
func TestClaudeGetsGlitchedsSkillsOnlyFromGlitched(t *testing.T) {
	state, ws, home := skillDirs(t)
	mine, theirs := filepath.Join(state, "skills"), filepath.Join(ws, ".glitched", "skills")
	skillstest.Write(t, mine, "mine", "Mine.")
	skillstest.Write(t, mine, "review", "My review.")
	skillstest.Write(t, theirs, "review", "The project's review.")
	skillstest.Write(t, theirs, "Upper", "Not a skill's name.")
	if err := os.MkdirAll(filepath.Join(state, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(state, "secrets", "SKILL.md"), []byte("---\nname: leak\ndescription: TOKEN=hunter2\n---\n"), 0o600)
	os.Symlink(filepath.Join(state, "secrets"), filepath.Join(theirs, "leak"))
	os.Symlink(filepath.Join(state, "secrets"), filepath.Join(mine, "leak"))
	elsewhere := skillstest.Write(t, filepath.Join(filepath.Dir(state), "library"), "linked", "Kept elsewhere.")
	os.Symlink(elsewhere, filepath.Join(mine, "linked"))
	before := skillstest.Fingerprint(t, ws, home)
	argv := replay(t, "hi", "")
	seen := seeing(t)
	h := host(t, nil, runtime.WithSkills(state))
	turnEnds(t, h, askThreadIn(t, h, ws), firstAsk)
	dirs := pluginDirs(t, argv)
	if len(dirs) != 1 || filepath.Dir(dirs[0]) != filepath.Join(state, "run-skills") {
		t.Fatalf("claude started with --plugin-dir %q, want one overlay in the state folder", dirs)
	}
	want := []string{"glitched:linked: Kept elsewhere.", "glitched:mine: Mine.", "glitched:review: The project's review.", native}
	if got := seen(); len(got) != 1 || !slices.Equal(got[0], want) {
		t.Errorf("the run's claude saw %q, want %q", got, want)
	}
	if after := skillstest.Fingerprint(t, ws, home); after != before {
		t.Errorf("the run changed the workspace or the home:\n%s\nwas\n%s", after, before)
	}
	if got := byHand(t, ws, startedWith(t, argv)[0]); !slices.Equal(got, []string{native}) {
		t.Errorf("a claude started by hand saw %q, want only its own", got)
	}
	filepath.WalkDir(dirs[0], func(p string, d os.DirEntry, err error) error {
		if data, _ := os.ReadFile(p); err == nil && !d.IsDir() && strings.Contains(string(data), "hunter2") {
			t.Errorf("the overlay's %s holds the state folder's secret", p)
		}
		return nil
	})
}

// What a claude starts with is fixed for its life: a turn after the
// thread's skills changed gets a claude of its own, which resumes the
// session; a turn after they didn't keeps the claude it has.
func TestChangedSkillsStartAClaudeThatResumesTheSession(t *testing.T) {
	state, ws, _ := skillDirs(t)
	skillstest.Write(t, filepath.Join(state, "skills"), "mine", "Mine.")
	argv, started := keep(t, []string{"hi"}, []string{"again-resume", "again-resume"})
	seen := seeing(t)
	h := host(t, nil, runtime.WithSkills(state))
	thread := askThreadIn(t, h, ws)
	turnEnds(t, h, thread, firstAsk)
	skillstest.Write(t, filepath.Join(ws, ".glitched", "skills"), "added", "Added between turns.")
	turnEnds(t, h, thread, againAsk)
	turnEnds(t, h, thread, againAsk)
	if l := launches(t, argv); !resumed(l) {
		t.Fatalf("claude ran with sessions %v, want the session created, then resumed once", l)
	}
	dirs := pluginDirs(t, argv)
	want := [][]string{{"glitched:mine: Mine.", native}, {"glitched:added: Added between turns.", "glitched:mine: Mine.", native}}
	if got := seen(); dirs[0] == dirs[1] || !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("claudes with --plugin-dir %q saw %q, want %q", dirs, got, want)
	}
	if n := len(started("claude")); n != 2 {
		t.Errorf("%d claudes started, want one for each set of skills", n)
	}
	waitGone(t, started("claude")[0], "the claude of the first skills")
	if _, err := os.Stat(dirs[0]); !os.IsNotExist(err) {
		t.Errorf("the first skills' overlay is still there: %v", err)
	}
}

// Each engine gives its threads the skills of its own state folder and
// of their workspaces on its machine, as a paired machine's does.
func TestEachEngineGivesItsOwnSkills(t *testing.T) {
	stateA, wsA, _ := skillDirs(t)
	stateB, wsB, _ := skillDirs(t)
	skillstest.Write(t, filepath.Join(stateA, "skills"), "here", "This machine's.")
	skillstest.Write(t, filepath.Join(wsA, ".glitched", "skills"), "this-project", "This project's.")
	skillstest.Write(t, filepath.Join(stateB, "skills"), "there", "That machine's.")
	skillstest.Write(t, filepath.Join(wsB, ".glitched", "skills"), "that-project", "That project's.")
	argv := replay(t, "hi", "")
	seen := seeing(t)
	a := hostAt(t, filepath.Join(stateA, "engine.db"), nil, runtime.WithSkills(stateA))
	b := hostAt(t, filepath.Join(stateB, "engine.db"), nil, runtime.WithSkills(stateB))
	turnEnds(t, a, askThreadIn(t, a, wsA), firstAsk)
	turnEnds(t, b, askThreadIn(t, b, wsB), firstAsk)
	want := [][]string{
		{"glitched:here: This machine's.", "glitched:this-project: This project's.", native},
		{"glitched:that-project: That project's.", "glitched:there: That machine's.", native},
	}
	if got := seen(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("the engines' claudes saw %q, want %q", got, want)
	}
	if dirs := pluginDirs(t, argv); len(dirs) != 2 || !strings.HasPrefix(dirs[0], stateA) || !strings.HasPrefix(dirs[1], stateB) {
		t.Errorf("claudes started with --plugin-dir %q", dirs)
	}
}

// A thread with no project, as a scratch thread, gets only the user's
// skills; one with no skills at all starts claude as before.
func TestAScratchThreadGetsOnlyTheUsersSkills(t *testing.T) {
	state, _, _ := skillDirs(t)
	scratch := filepath.Join(state, "scratch", "s1")
	skillstest.Write(t, filepath.Join(scratch, ".glitched", "skills"), "planted", "Not a project's.")
	argv := replay(t, "hi", "")
	seen := seeing(t)
	h := host(t, nil, runtime.WithSkills(state))
	turnEnds(t, h, askThreadIn(t, h, scratch), firstAsk)
	skillstest.Write(t, filepath.Join(state, "skills"), "mine", "Mine.")
	turnEnds(t, h, askThreadIn(t, h, scratch), firstAsk)
	want := [][]string{{native}, {"glitched:mine: Mine.", native}}
	if got := seen(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("scratch threads' claudes saw %q, want %q", got, want)
	}
	if dirs := pluginDirs(t, argv); dirs[0] != "" || dirs[1] == "" {
		t.Errorf("claudes started with --plugin-dir %q, want none and then one", dirs)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A thread in a worktree gets the skills its worktree has, as git checked
// them out; a worktree with none, as of skills git doesn't track, gets
// those in the project's folder.
func TestAWorktreeThreadGetsTheProjectsSkills(t *testing.T) {
	state, _, _ := skillDirs(t)
	tracked, untracked := filepath.Join(filepath.Dir(state), "tracked"), filepath.Join(filepath.Dir(state), "untracked")
	for _, repo := range []string{tracked, untracked} {
		os.MkdirAll(repo, 0o755)
		git(t, repo, "init", "-q", "-b", "main")
		os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0o644)
	}
	skillstest.Write(t, filepath.Join(tracked, ".glitched", "skills"), "committed", "In git.")
	for _, repo := range []string{tracked, untracked} {
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-q", "-m", "start")
	}
	skillstest.Write(t, filepath.Join(tracked, ".glitched", "skills"), "loose", "Not in git.")
	skillstest.Write(t, filepath.Join(untracked, ".glitched", "skills"), "loose", "Not in git.")
	replay(t, "hi", "")
	seen := seeing(t)
	h := host(t, nil, runtime.WithSkills(state), runtime.WithWorktrees(filepath.Join(state, "worktrees")))
	for _, repo := range []string{tracked, untracked} {
		thread := api.NewID()
		submit(t, h, thread, api.ThreadCreate{Workspace: repo, Title: "hi", Access: api.AccessAsk, Model: api.ModelRef{Provider: "claude", Model: "haiku"}, Worktree: &api.NewWorktree{}})
		turnEnds(t, h, thread, firstAsk)
	}
	want := [][]string{{"glitched:committed: In git.", native}, {"glitched:loose: Not in git.", native}}
	if got := seen(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("worktree threads' claudes saw %q, want %q", got, want)
	}
}

// Plugins claude says it couldn't load, as Glitched's skills, are logged,
// and the turn runs as it would without them.
func TestPluginErrorsAreLoggedNotFatal(t *testing.T) {
	state, ws, _ := skillDirs(t)
	skillstest.Write(t, filepath.Join(state, "skills"), "mine", "Mine.")
	var logs bytes.Buffer
	was := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(was) })
	replay(t, "plugin-errors", "")
	h := host(t, nil, runtime.WithSkills(state))
	turnEnds(t, h, askThreadIn(t, h, ws), firstAsk)
	if !strings.Contains(logs.String(), "plugins didn't load") || !strings.Contains(logs.String(), "skills/review: unreadable") {
		t.Errorf("logged %s", logs.String())
	}
}
