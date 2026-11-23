package skills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuildCopiesTheSetAsAClaudePlugin(t *testing.T) {
	state, ws := folders(t)
	skill(t, filepath.Join(state, "skills"), "review", "Mine.")
	skill(t, filepath.Join(state, "skills"), "notes", "Mine too.", "ref/guide.md", "read me")
	skill(t, filepath.Join(ws, ".glitched", "skills"), "review", "The project's.", "run.sh", "echo hi")
	os.Chmod(filepath.Join(ws, ".glitched", "skills", "review", "run.sh"), 0o755)
	write(t, filepath.Join(state, "secrets", "t1", "env"), "TOKEN=hunter2")
	link(t, filepath.Join(state, "secrets"), filepath.Join(state, "skills", "leak"))
	set := Find(state, ws, "")
	root, err := Build(state, set)
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(state, "run-skills", set.Hash()) {
		t.Fatalf("built %s", root)
	}
	got := map[string]string{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		fi, _ := d.Info()
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(p)
		got[filepath.ToSlash(rel)] = string(data)
		if want := map[bool]os.FileMode{false: 0o444, true: 0o555}[rel == filepath.Join("skills", "review", "run.sh")]; fi.Mode().Perm() != want {
			t.Errorf("%s is %v, want %v", rel, fi.Mode(), want)
		}
		return nil
	})
	var paths []string
	for p := range got {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	want := []string{".claude-plugin/plugin.json", "skills/notes/SKILL.md", "skills/notes/ref/guide.md", "skills/review/SKILL.md", "skills/review/run.sh"}
	if !slices.Equal(paths, want) {
		t.Fatalf("overlay holds %v, want %v", paths, want)
	}
	if !strings.Contains(got["skills/review/SKILL.md"], "The project's.") || got["skills/notes/ref/guide.md"] != "read me" {
		t.Errorf("copied %q", got)
	}
	if got[".claude-plugin/plugin.json"] != `{"name":"glitched","version":"1.0.0","description":"Skills Glitched gives its agents"}`+"\n" {
		t.Errorf("manifest %s", got[".claude-plugin/plugin.json"])
	}
	if SkillFile(root, "notes") != filepath.Join(root, "skills", "notes", "SKILL.md") {
		t.Errorf("SkillFile %s", SkillFile(root, "notes"))
	}
	for p, data := range got {
		if strings.Contains(data, "hunter2") {
			t.Errorf("%s holds the state folder's secret", p)
		}
	}
}

// An overlay is its set's: a second build of the same set is the first,
// left as it was; no skills build nothing.
func TestBuildReusesTheSetsOverlay(t *testing.T) {
	state, _ := folders(t)
	if root, err := Build(state, Find(state, "", "")); root != "" || err != nil {
		t.Fatalf("no skills built %q, %v", root, err)
	}
	if _, err := os.Lstat(Runs(state)); !os.IsNotExist(err) {
		t.Fatalf("no skills made %s: %v", Runs(state), err)
	}
	skill(t, filepath.Join(state, "skills"), "review", "Mine.")
	first, err := Build(state, Find(state, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	was, _ := os.Stat(filepath.Join(first, "skills", "review", "SKILL.md"))
	again, err := Build(state, Find(state, "", ""))
	is, _ := os.Stat(filepath.Join(again, "skills", "review", "SKILL.md"))
	if err != nil || again != first || !os.SameFile(was, is) {
		t.Errorf("built %s then %s (%v)", first, again, err)
	}
}

// A skill that changes between its read and its copy fails the build, and
// leaves nothing behind.
func TestBuildRefusesASkillChangedSinceItsRead(t *testing.T) {
	state, _ := folders(t)
	skill(t, filepath.Join(state, "skills"), "review", "Mine.")
	set := Find(state, "", "")
	write(t, filepath.Join(state, "skills", "review", "new.md"), "added")
	if root, err := Build(state, set); err == nil {
		t.Fatalf("built %s", root)
	}
	if entries, _ := os.ReadDir(Runs(state)); len(entries) != 0 {
		t.Errorf("left %v", entries)
	}
}

func TestSweepAndClear(t *testing.T) {
	state, _ := folders(t)
	skill(t, filepath.Join(state, "skills"), "one", "One.")
	a, _ := Build(state, Find(state, "", ""))
	skill(t, filepath.Join(state, "skills"), "two", "Two.")
	b, _ := Build(state, Find(state, "", ""))
	if a == "" || b == "" || a == b {
		t.Fatalf("built %q and %q", a, b)
	}
	if err := Sweep(state, []string{b}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Errorf("swept, %s is still there: %v", a, err)
	}
	if _, err := os.Stat(b); err != nil {
		t.Errorf("swept what was kept: %v", err)
	}
	if err := Clear(state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Runs(state)); !os.IsNotExist(err) {
		t.Errorf("cleared, %s is still there: %v", Runs(state), err)
	}
	if err := Sweep(state, nil); err != nil {
		t.Errorf("sweeping nothing: %v", err)
	}
}
