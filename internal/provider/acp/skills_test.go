package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/skills"
	"glitched/internal/skills/skillstest"
)

const native = "native: Antigravity's own."

// skillDirs is a state folder, a workspace and a home, with their links
// resolved. HOME is the home from now on, and the workspace has a skill
// of Antigravity's own.
func skillDirs(t *testing.T) (state, ws, home string) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, ws, home = filepath.Join(tmp, "state"), filepath.Join(tmp, "work"), filepath.Join(tmp, "home")
	for _, d := range []string{state, ws, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	skillstest.Write(t, filepath.Join(ws, ".agents", "skills"), "native", "Antigravity's own.")
	return state, ws, home
}

// staged is the skills a run in ws gets, as the engine stages them.
func staged(t *testing.T, state, ws string) provider.Skills {
	t.Helper()
	set := skills.Find(state, ws, "")
	root, err := skills.Build(state, set)
	if err != nil {
		t.Fatal(err)
	}
	out := provider.Skills{Root: root}
	for _, sk := range set.Skills {
		out.List = append(out.List, provider.Skill{Name: sk.Name, Description: sk.Description, Path: skills.SkillFile(root, sk.Name)})
	}
	return out
}

// prompts is the text of each prompt the agent got, and what it found of
// skills with it, in order.
func prompts(t *testing.T, log string) (texts []string, seen [][]string) {
	t.Helper()
	for _, e := range wire(t, log) {
		switch e.Method {
		case "session/prompt":
			var p struct{ Prompt []struct{ Text string } }
			json.Unmarshal(e.Params, &p)
			texts = append(texts, p.Prompt[0].Text)
		case "skills":
			var s []string
			json.Unmarshal(e.Params, &s)
			seen = append(seen, s)
		}
	}
	return texts, seen
}

func runIn(t *testing.T, a *Adapter, s provider.SessionSpec, text string) provider.Agent {
	t.Helper()
	g := open(t, a, s)
	do(t, g, provider.Start{Text: text})
	if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeCompleted {
		t.Fatalf("ended %+v", e)
	}
	closeRun(t, g)
	return g
}

// Antigravity gets the user's skills and the project's, which win by
// name, listed in its session's first prompt after the MCP servers'
// instructions, by SKILL.md files in the overlay it can read; later
// prompts don't repeat them. An agent started without Glitched's skills,
// in the same folder with the same home, finds only its own, and the run
// writes nothing in either.
func TestAntigravityGetsGlitchedsSkillsInItsFirstPrompt(t *testing.T) {
	state, ws, home := skillDirs(t)
	skillstest.Write(t, filepath.Join(state, "skills"), "mine", "Mine.")
	skillstest.Write(t, filepath.Join(state, "skills"), "review", "My review.")
	skillstest.Write(t, filepath.Join(ws, ".glitched", "skills"), "review", "The project's review.")
	skillstest.Write(t, filepath.Join(ws, ".glitched", "skills"), strings.Repeat("x", 65), "Too long a name.")
	os.MkdirAll(filepath.Join(state, "secrets"), 0o700)
	os.Symlink(filepath.Join(state, "secrets"), filepath.Join(state, "skills", "leak"))
	before := skillstest.Fingerprint(t, ws, home)
	a, log := fake(t, "hi")
	s := spec(t)
	s.Workspace, s.Skills = ws, staged(t, state, ws)
	runIn(t, a, s, "Say hi.")
	s.Run = api.NewID()
	runIn(t, a, s, "Again.")
	plain := spec(t)
	plain.Workspace = ws
	runIn(t, a, plain, "Plain.")
	texts, seen := prompts(t, log)
	list := "<glitched-skills>\nGlitched gives you these skills. When a task matches one, read its SKILL.md and follow it.\n" +
		"- mine: Mine. (SKILL.md: " + filepath.Join(s.Skills.Root, "skills", "mine", "SKILL.md") + ")\n" +
		"- review: The project's review. (SKILL.md: " + filepath.Join(s.Skills.Root, "skills", "review", "SKILL.md") + ")\n" +
		"</glitched-skills>"
	if want := "<instructions>\nUse delegate_task.\n</instructions>\n\n" + list + "\n\nSay hi."; texts[0] != want {
		t.Errorf("the first prompt is\n%s\nwant\n%s", texts[0], want)
	}
	if texts[1] != "Again." || strings.Contains(texts[2], "glitched-skills") {
		t.Errorf("later prompts %q", texts[1:])
	}
	want := [][]string{{"briefed:mine: Mine.", "briefed:review: The project's review.", native}, {native}, {native}}
	if !slices.EqualFunc(seen, want, slices.Equal) {
		t.Errorf("the agent found %q, want %q", seen, want)
	}
	if after := skillstest.Fingerprint(t, ws, home); after != before {
		t.Errorf("the run changed the workspace or the home:\n%s\nwas\n%s", after, before)
	}
}

// An agent is started with its skills: the thread's run after they
// changed gets a new one, which takes up the thread's session and lists
// the skills it has now; a run after they didn't keeps the agent it has.
func TestChangedSkillsStartAnAgentThatListsThem(t *testing.T) {
	state, ws, _ := skillDirs(t)
	skillstest.Write(t, filepath.Join(state, "skills"), "mine", "Mine.")
	a, log := fake(t, "memory")
	s := spec(t)
	s.Workspace, s.Skills = ws, staged(t, state, ws)
	first := runIn(t, a, s, "Remember the word kumquat.")
	s.Run, s.Generation, s.Session = api.NewID(), 2, first.(provider.Sessioned).Session()
	runIn(t, a, s, "Still there?")
	skillstest.Write(t, filepath.Join(ws, ".glitched", "skills"), "added", "Added between turns.")
	s.Run, s.Generation, s.Skills = api.NewID(), 3, staged(t, state, ws)
	runIn(t, a, s, "What word did I ask you to remember?")
	_, seen := prompts(t, log)
	want := [][]string{{"briefed:mine: Mine.", native}, {native}, {"briefed:added: Added between turns.", "briefed:mine: Mine.", native}}
	if !slices.EqualFunc(seen, want, slices.Equal) {
		t.Errorf("the agent found %q, want %q", seen, want)
	}
	var pids []int
	for _, e := range find(t, wire(t, log), "session/prompt") {
		pids = append(pids, e.PID)
	}
	if len(pids) != 3 || pids[1] != pids[0] || pids[2] == pids[0] {
		t.Errorf("prompts went to agents %v, want the second to the first's and the third to a new one", pids)
	}
	if n := len(find(t, wire(t, log), "session/new")); n != 1 {
		t.Errorf("%d sessions made, want the new agent to take up the thread's", n)
	}
}
