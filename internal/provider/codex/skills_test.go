package codex

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

const native = "native: Codex's own."

// skillDirs is a state folder, a workspace and a home, with their links
// resolved. HOME is the home from now on, and CODEX_HOME its .codex, with
// a skill of Codex's own in the user's .agents.
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
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	skillstest.Write(t, filepath.Join(home, ".agents", "skills"), "native", "Codex's own.")
	return state, ws, home
}

// staged is the skills a run in ws gets, as the engine stages them.
func staged(t *testing.T, state, ws string) provider.Skills {
	t.Helper()
	root, err := skills.Build(state, skills.Find(state, ws, ""))
	if err != nil {
		t.Fatal(err)
	}
	return provider.Skills{Root: root}
}

// listed is the skills each turn's Codex listed, in order.
func listed(t *testing.T, log string) [][]string {
	t.Helper()
	var out [][]string
	for _, raw := range sent(t, log, "skills") {
		var seen []string
		json.Unmarshal(raw, &seen)
		out = append(out, seen)
	}
	return out
}

func roots(t *testing.T, log string) []string {
	t.Helper()
	var out []string
	for _, raw := range sent(t, log, "skills/extraRoots/set") {
		var p struct{ ExtraRoots []string }
		json.Unmarshal(raw, &p)
		out = append(out, strings.Join(p.ExtraRoots, ","))
	}
	return out
}

func runIn(t *testing.T, a *Adapter, s provider.SessionSpec) []provider.Event {
	t.Helper()
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "hi"})
	events := turn(t, g)
	closeRun(t, g)
	if end := ended(events); end.Outcome != api.OutcomeCompleted {
		t.Fatalf("the turn ended %+v", end)
	}
	return events
}

// Codex gets the user's skills and the project's, which win by name, as
// an extra root of the app-server's, set before its thread starts: a
// Codex started in the same folder with the same home, but without
// Glitched's skills, lists only its own, and the run writes nothing in
// either.
func TestCodexGetsGlitchedsSkillsAsAnExtraRoot(t *testing.T) {
	state, ws, home := skillDirs(t)
	skillstest.Write(t, filepath.Join(state, "skills"), "mine", "Mine.")
	skillstest.Write(t, filepath.Join(state, "skills"), "review", "My review.")
	skillstest.Write(t, filepath.Join(ws, ".glitched", "skills"), "review", "The project's review.")
	skillstest.Write(t, filepath.Join(ws, ".glitched", "skills"), "a--b", "Not a skill's name.")
	os.MkdirAll(filepath.Join(state, "secrets"), 0o700)
	os.Symlink(filepath.Join(state, "secrets"), filepath.Join(ws, ".glitched", "skills", "leak"))
	before := skillstest.Fingerprint(t, ws, home)
	a, log := fake(t, "hi")
	s := spec(t)
	s.Workspace, s.Skills = ws, staged(t, state, ws)
	runIn(t, a, s)
	if got, want := roots(t, log), []string{filepath.Join(s.Skills.Root, "skills")}; !slices.Equal(got, want) {
		t.Errorf("extra roots %q, want %q", got, want)
	}
	var order []string
	for _, e := range wire(t, log) {
		if e.Method == "skills/extraRoots/set" || e.Method == "thread/start" {
			order = append(order, e.Method)
		}
	}
	if !slices.Equal(order, []string{"skills/extraRoots/set", "thread/start"}) {
		t.Errorf("app-server was asked %q, want the skills before the thread", order)
	}
	want := []string{"extra:mine: Mine.", "extra:review: The project's review.", native}
	if got := listed(t, log); len(got) != 1 || !slices.Equal(got[0], want) {
		t.Errorf("Codex listed %q, want %q", got, want)
	}
	if after := skillstest.Fingerprint(t, ws, home); after != before {
		t.Errorf("the run changed the workspace or the home:\n%s\nwas\n%s", after, before)
	}
	plain := spec(t)
	plain.Workspace = ws
	runIn(t, a, plain)
	if got := listed(t, log); len(got) != 2 || !slices.Equal(got[1], []string{native}) || len(roots(t, log)) != 1 {
		t.Errorf("a Codex without Glitched's skills listed %q, with extra roots %q", got, roots(t, log))
	}
}

// What an app-server starts with is fixed for its life: the thread's run
// after its skills changed gets a new one, which resumes the conversation;
// one after they didn't keeps the one it has.
func TestChangedSkillsStartAnAppServerThatResumes(t *testing.T) {
	state, ws, _ := skillDirs(t)
	skillstest.Write(t, filepath.Join(state, "skills"), "mine", "Mine.")
	a, log := fake(t, "hi")
	s := spec(t)
	s.Workspace, s.Skills = ws, staged(t, state, ws)
	first := text(runIn(t, a, s))
	g := open(t, a, s)
	s.Session = g.(provider.Sessioned).Session()
	do(t, g, provider.Start{Text: "hi"})
	same := text(turn(t, g))
	closeRun(t, g)
	skillstest.Write(t, filepath.Join(ws, ".glitched", "skills"), "added", "Added between turns.")
	s.Run, s.Skills = api.NewID(), staged(t, state, ws)
	changed := text(runIn(t, a, s))
	pid := strings.TrimPrefix(first, "hi 1 from ")
	if same != "hi 2 from "+pid || !strings.HasPrefix(changed, "hi 1 from ") || changed == "hi 1 from "+pid {
		t.Errorf("turns said %q, %q, %q: want the second on the first's app-server, the third on a new one", first, same, changed)
	}
	if n := len(sent(t, log, "thread/resume")); n != 1 {
		t.Errorf("%d resumes, want the new app-server to resume the conversation", n)
	}
	want := [][]string{{"extra:mine: Mine.", native}, {"extra:mine: Mine.", native}, {"extra:added: Added between turns.", "extra:mine: Mine.", native}}
	if got := listed(t, log); !slices.EqualFunc(got, want, slices.Equal) || len(roots(t, log)) != 2 {
		t.Errorf("Codex listed %q with extra roots %q, want %q", got, roots(t, log), want)
	}
}

// A Codex that refuses the skills, as one too old for the call, runs the
// turn without them, and the turn says so.
func TestACodexThatRefusesTheSkillsRunsWithoutThem(t *testing.T) {
	for scenario, says := range map[string]string{
		"no-skills-call": "Codex 0.171.0 can't take Glitched's skills, so it runs without them.",
		"skills-refused": "Codex refused Glitched's skills (extraRoots must be absolute), so it runs without them.",
	} {
		t.Run(scenario, func(t *testing.T) {
			state, ws, _ := skillDirs(t)
			skillstest.Write(t, filepath.Join(state, "skills"), "mine", "Mine.")
			a, log := fake(t, scenario)
			s := spec(t)
			s.Workspace, s.Skills = ws, staged(t, state, ws)
			var notices []string
			for _, w := range works(runIn(t, a, s)) {
				if strings.HasPrefix(w.Item, "glitched-notice-") {
					notices = append(notices, w.Title)
				}
			}
			if !slices.Equal(notices, []string{says}) {
				t.Errorf("notices %q, want %q", notices, says)
			}
			if got := listed(t, log); len(got) != 1 || !slices.Equal(got[0], []string{native}) {
				t.Errorf("Codex listed %q", got)
			}
		})
	}
}
