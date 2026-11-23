package skills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// skill writes a skill folder name in root whose SKILL.md says it is
// named as it is, with files beside it.
func skill(t *testing.T, root, name, description string, files ...string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	write(t, filepath.Join(dir, "SKILL.md"), "---\nname: "+name+"\ndescription: "+description+"\n---\n\n# "+name+"\n")
	for i := 0; i+1 < len(files); i += 2 {
		write(t, filepath.Join(dir, files[i]), files[i+1])
	}
	return dir
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, at string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, at); err != nil {
		t.Fatal(err)
	}
}

// folders is a state folder and a workspace beside it, with their links
// resolved, as macOS's TempDir is under a link.
func folders(t *testing.T) (state, ws string) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, ws = filepath.Join(tmp, "state"), filepath.Join(tmp, "work")
	for _, d := range []string{state, ws} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return state, ws
}

func names(skills []Skill) []string {
	var out []string
	for _, sk := range skills {
		out = append(out, string(sk.Source)+":"+sk.Name)
	}
	return out
}

func why(set Set, name string) string {
	for _, s := range set.Skipped {
		if s.Name == name {
			return s.Why
		}
	}
	return ""
}

func TestFrontmatter(t *testing.T) {
	long := strings.Repeat("é", 1024)
	for _, c := range []struct {
		name, md        string
		wantName, wantD string
		err             string
	}{
		{"plain", "---\nname: a\ndescription: Does a thing. # not a comment? it is\n---\n", "a", "Does a thing.", ""},
		{"single quotes", "---\nname: 'a'\ndescription: 'It''s: here'\n---\n", "a", "It's: here", ""},
		{"double quotes", "---\nname: \"a\"\ndescription: \"Tab\\there \\\"quoted\\\"\"\n---\n", "a", "Tab\there \"quoted\"", ""},
		{"literal block", "---\nname: a\ndescription: |\n  Line one.\n  Line two.\n---\n", "a", "Line one.\nLine two.", ""},
		{"folded block", "---\nname: a\ndescription: >-\n  Line one\n  continues.\n---\n", "a", "Line one continues.", ""},
		{"other keys pass", "---\nname: a\nallowed-tools: [Read, Bash]\nmetadata:\n  x: 1\ndescription: d\n---\n", "a", "d", ""},
		{"crlf and bom", "\ufeff---\r\nname: a\r\ndescription: d\r\n---\r\nbody", "a", "d", ""},
		{"a number is text", "---\nname: 123\ndescription: 2026-11-24\n---\n", "123", "2026-11-24", ""},
		{"longest description", "---\nname: a\ndescription: " + long + "\n---\n", "a", long, ""},
		{"no frontmatter", "# a\n", "", "", "doesn't start with frontmatter"},
		{"unclosed", "---\nname: a\ndescription: d\n", "", "", "no closing --- line"},
		{"no name", "---\ndescription: d\n---\n", "", "", "has no name"},
		{"empty name", "---\nname:\ndescription: d\n---\n", "", "", "has no name"},
		{"no description", "---\nname: a\n---\n", "", "", "has no description"},
		{"description too long", "---\nname: a\ndescription: " + long + "x\n---\n", "", "", "longer than 1024"},
		{"name a list", "---\nname: [a]\ndescription: d\n---\n", "", "", "name isn't text"},
		{"name an alias", "---\nx: &n a\nname: *n\ndescription: d\n---\n", "", "", "name isn't text"},
		{"not a mapping", "---\n- a\n---\n", "", "", "isn't a list of keys"},
		{"not yaml", "---\nname: [a\n---\n", "", "", "isn't YAML"},
		{"repeated key", "---\nname: a\nname: b\ndescription: d\n---\n", "", "", "gives name twice"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, err := frontmatter([]byte(c.md))
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("got %+v, %v; want an error saying %q", f, err, c.err)
				}
				return
			}
			if err != nil || f.Name != c.wantName || f.Description != c.wantD {
				t.Fatalf("got %+v, %v; want %q, %q", f, err, c.wantName, c.wantD)
			}
		})
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"a", "review", "a-b-c", "x1", "123", strings.Repeat("a", 64)} {
		if !validName(n) {
			t.Errorf("%q refused", n)
		}
	}
	for _, n := range []string{"", "../x", "Upper", "a--b", "-a", "a-", "a_b", "a.b", "a b", strings.Repeat("a", 65), "é"} {
		if validName(n) {
			t.Errorf("%q taken", n)
		}
	}
}

func TestFindHomeOnly(t *testing.T) {
	state, ws := folders(t)
	skill(t, filepath.Join(state, "skills"), "review", "Reviews a diff.")
	set := Find(state, ws, "")
	if got := names(set.Skills); !slices.Equal(got, []string{"home:review"}) || len(set.Skipped) != 0 {
		t.Fatalf("skills %v, skipped %+v", got, set.Skipped)
	}
	sk := set.Skills[0]
	if sk.Description != "Reviews a diff." || sk.Dir != filepath.Join(state, "skills", "review") || len(sk.Hash) != 64 {
		t.Errorf("skill %+v", sk)
	}
}

func TestFindProjectOnly(t *testing.T) {
	state, ws := folders(t)
	skill(t, filepath.Join(ws, ".glitched", "skills"), "deploy", "Deploys.")
	set := Find(state, ws, "")
	if got := names(set.Skills); !slices.Equal(got, []string{"project:deploy"}) {
		t.Fatalf("skills %v, skipped %+v", got, set.Skipped)
	}
	if want := filepath.Join(filepath.Base(ws), ".glitched", "skills", "deploy"); set.Skills[0].Shown != want {
		t.Errorf("shown %q, want %q", set.Skills[0].Shown, want)
	}
}

// A project's skill replaces the user's of the same name, which is kept
// as shadowed; the rest of both are the set, by name.
func TestFindProjectWinsAClash(t *testing.T) {
	state, ws := folders(t)
	skill(t, filepath.Join(state, "skills"), "review", "Mine.")
	skill(t, filepath.Join(state, "skills"), "zeta", "Mine too.")
	skill(t, filepath.Join(ws, ".glitched", "skills"), "review", "The project's.")
	skill(t, filepath.Join(ws, ".glitched", "skills"), "alpha", "The project's too.")
	set := Find(state, ws, "")
	if got := names(set.Skills); !slices.Equal(got, []string{"project:alpha", "project:review", "home:zeta"}) {
		t.Fatalf("skills %v", got)
	}
	if got := names(set.Shadowed); !slices.Equal(got, []string{"home:review"}) || set.Skills[1].Description != "The project's." {
		t.Errorf("shadowed %v, review %+v", got, set.Skills[1])
	}
}

// A worktree's tracked skills are its own; a worktree with none, as git
// doesn't check out skills it doesn't track, has its project's.
func TestFindInAWorktree(t *testing.T) {
	state, project := folders(t)
	skill(t, filepath.Join(project, ".glitched", "skills"), "untracked", "Only in the project folder.")
	tree := filepath.Join(state, "worktrees", "work-1")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := names(Find(state, tree, project).Skills); !slices.Equal(got, []string{"project:untracked"}) {
		t.Errorf("a worktree with no skills folder got %v, want the project's", got)
	}
	skill(t, filepath.Join(tree, ".glitched", "skills"), "tracked", "Checked out.")
	set := Find(state, tree, project)
	if got := names(set.Skills); !slices.Equal(got, []string{"project:tracked"}) {
		t.Errorf("a worktree with its own got %v", got)
	}
	if set.Skills[0].Dir != filepath.Join(tree, ".glitched", "skills", "tracked") {
		t.Errorf("read from %s", set.Skills[0].Dir)
	}
}

// A thread with no project, as a scratch thread in the state folder,
// gets only the user's skills, and nothing else of the state folder is
// read as a project's.
func TestFindScratchGetsOnlyHome(t *testing.T) {
	state, _ := folders(t)
	skill(t, filepath.Join(state, "skills"), "mine", "Mine.")
	scratch := filepath.Join(state, "scratch", "s1")
	skill(t, filepath.Join(scratch, ".glitched", "skills"), "planted", "Not a project's.")
	skill(t, filepath.Join(state, ".glitched", "skills"), "planted", "Not a project's.")
	for _, ws := range []string{scratch, state, ""} {
		set := Find(state, ws, "")
		if got := names(set.Skills); !slices.Equal(got, []string{"home:mine"}) || len(set.Skipped) != 0 {
			t.Errorf("workspace %q: skills %v, skipped %+v", ws, got, set.Skipped)
		}
	}
}

// A workspace whose .glitched/skills is the user's own, as home's when the
// state folder is ~/.glitched, counts it once, as the user's.
func TestFindAProjectRootThatIsHomeCountsOnce(t *testing.T) {
	state, _ := folders(t)
	ws := filepath.Dir(state)
	if err := os.Rename(state, filepath.Join(ws, ".glitched")); err != nil {
		t.Fatal(err)
	}
	state = filepath.Join(ws, ".glitched")
	skill(t, filepath.Join(state, "skills"), "mine", "Mine.")
	set := Find(state, ws, "")
	if got := names(set.Skills); !slices.Equal(got, []string{"home:mine"}) || len(set.Shadowed) != 0 || len(set.Skipped) != 0 {
		t.Errorf("skills %v, shadowed %v, skipped %+v", got, names(set.Shadowed), set.Skipped)
	}
}

func TestFindRefusesHostileSkills(t *testing.T) {
	state, ws := folders(t)
	home, mine := filepath.Join(state, "skills"), filepath.Join(ws, ".glitched", "skills")
	write(t, filepath.Join(state, "secrets", "t1", "env"), "TOKEN=hunter2")
	skill(t, home, "Upper", "Bad name.")
	skill(t, home, "a--b", "Bad name.")
	skill(t, home, strings.Repeat("a", 65), "Bad name.")
	write(t, filepath.Join(home, "x", "SKILL.md"), "---\nname: ../x\ndescription: Escapes.\n---\n")
	link(t, filepath.Join(state, "secrets"), filepath.Join(home, "leak"))
	link(t, filepath.Join(state, "secrets"), filepath.Join(mine, "leak"))
	outside := skill(t, t.TempDir(), "outside", "Out of the workspace.")
	link(t, outside, filepath.Join(mine, "outside"))
	skill(t, home, "inner-link", "Holds a link.")
	link(t, filepath.Join(state, "secrets", "t1", "env"), filepath.Join(home, "inner-link", "env"))
	skill(t, mine, "nomd", "x")
	os.Remove(filepath.Join(mine, "nomd", "SKILL.md"))
	write(t, filepath.Join(mine, "README.md"), "not a skill")
	set := Find(state, ws, "")
	if len(set.Skills) != 0 {
		t.Errorf("took %v", names(set.Skills))
	}
	for name, want := range map[string]string{
		"Upper":                 "isn't a skill name",
		"a--b":                  "isn't a skill name",
		strings.Repeat("a", 65): "isn't a skill name",
		"x":                     `names it "../x"`,
		"leak":                  "links",
		"outside":               "links out of its project",
		"inner-link":            "holds a link, env",
		"nomd":                  "no SKILL.md",
	} {
		if got := why(set, name); !strings.Contains(got, want) {
			t.Errorf("%s skipped for %q, want %q", name, got, want)
		}
	}
	var leaks []string
	for _, s := range set.Skipped {
		if s.Name == "leak" {
			leaks = append(leaks, string(s.Source)+": "+s.Why)
		}
	}
	slices.Sort(leaks)
	if want := []string{"home: It links into Glitched's own folder.", "project: It links out of its project."}; !slices.Equal(leaks, want) {
		t.Errorf("links into the state folder skipped as %q, want %q", leaks, want)
	}
	if why(set, "README.md") != "" || len(set.Skipped) != 9 {
		t.Errorf("skipped %+v", set.Skipped)
	}
}

// The user's skill folder may be a link to anywhere but the rest of the
// state folder; a project's, to anywhere in its workspace.
func TestFindFollowsAllowedLinks(t *testing.T) {
	state, ws := folders(t)
	shared := skill(t, filepath.Dir(ws), "shared", "Kept elsewhere.")
	link(t, shared, filepath.Join(state, "skills", "shared"))
	skill(t, filepath.Join(state, "skills", ".library"), "kept", "In the skills folder.")
	link(t, filepath.Join(state, "skills", ".library", "kept"), filepath.Join(state, "skills", "kept"))
	skill(t, filepath.Join(ws, "tools"), "local", "Elsewhere in the workspace.")
	link(t, filepath.Join(ws, "tools", "local"), filepath.Join(ws, ".glitched", "skills", "local"))
	set := Find(state, ws, "")
	if got := names(set.Skills); !slices.Equal(got, []string{"home:kept", "project:local", "home:shared"}) || len(set.Skipped) != 0 {
		t.Fatalf("skills %v, skipped %+v", got, set.Skipped)
	}
	if set.Skills[2].Dir != shared || set.Skills[2].Shown != filepath.Join(state, "skills", "shared") {
		t.Errorf("shared %+v", set.Skills[2])
	}
}

// A skills folder that leads out of its project, or into the state
// folder, is read for nothing.
func TestFindRefusesARootThatLeadsAway(t *testing.T) {
	state, ws := folders(t)
	away := t.TempDir()
	skill(t, away, "away", "Out of the workspace.")
	link(t, away, filepath.Join(ws, ".glitched", "skills"))
	skill(t, filepath.Join(state, "secrets"), "secret", "In the state folder.")
	link(t, filepath.Join(state, "secrets"), filepath.Join(state, "skills"))
	set := Find(state, ws, "")
	if len(set.Skills) != 0 || len(set.Skipped) != 2 {
		t.Fatalf("skills %v, skipped %+v", names(set.Skills), set.Skipped)
	}
	for _, s := range set.Skipped {
		if s.Name != "" || !strings.Contains(s.Why, "so Glitched reads none of it") {
			t.Errorf("skipped %+v", s)
		}
	}
}

func TestFindLimits(t *testing.T) {
	state, _ := folders(t)
	home := filepath.Join(state, "skills")
	var many []string
	for i := range maxFiles {
		many = append(many, filepath.Join("files", strings.Repeat("f", 1+i%7)+string(rune('a'+i%26))+strings.Repeat("0", i/182), "x"), "")
	}
	skill(t, home, "files", "Too many.", many...)
	deep := strings.Repeat("d/", maxDepth+1) + "x"
	skill(t, home, "deep", "Too deep.", deep, "")
	skill(t, home, "deepest", "Deep enough.", strings.Repeat("d/", maxDepth)+"x", "")
	skill(t, home, "big", "Too big.", "blob", strings.Repeat("x", maxBytes))
	set := Find(state, "", "")
	if got := names(set.Skills); !slices.Equal(got, []string{"home:deepest"}) {
		t.Errorf("skills %v", got)
	}
	for name, want := range map[string]string{"files": "more than 1000 files", "deep": "more than 24 deep", "big": "more than 8 MiB"} {
		if got := why(set, name); !strings.Contains(got, want) {
			t.Errorf("%s skipped for %q, want %q", name, got, want)
		}
	}
}

func TestFindReadsAtMostAThousandSkills(t *testing.T) {
	state, _ := folders(t)
	for i := range maxSkills + 1 {
		if err := os.MkdirAll(filepath.Join(state, "skills", strings.Repeat("s", 1+i/100)+string(rune('a'+i%26))+string(rune('a'+i/26%26))), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	set := Find(state, "", "")
	if len(set.Skipped) != maxSkills+1 || set.Skipped[0].Name != "" || !strings.Contains(set.Skipped[0].Why, "reads only the first 1000") {
		t.Errorf("%d skipped, first %+v", len(set.Skipped), set.Skipped[0])
	}
}

// A skill's hash is of its files, not of where they are, and the hash of
// a set of its skills'.
func TestHashes(t *testing.T) {
	state, ws := folders(t)
	skill(t, filepath.Join(state, "skills"), "same", "Same.", "scripts/run.sh", "echo hi")
	skill(t, filepath.Join(ws, ".glitched", "skills"), "same", "Same.", "scripts/run.sh", "echo hi")
	home, mine := Find(state, "", "").Skills[0], Find(state, ws, "").Skills[0]
	if home.Hash != mine.Hash || home.Dir == mine.Dir {
		t.Fatalf("the same files in two places hash %s and %s", home.Hash, mine.Hash)
	}
	before := Find(state, "", "")
	os.Chmod(filepath.Join(state, "skills", "same", "scripts", "run.sh"), 0o755)
	moded := Find(state, "", "")
	write(t, filepath.Join(state, "skills", "same", "scripts", "run.sh"), "echo bye")
	edited := Find(state, "", "")
	if before.Hash() == moded.Hash() || moded.Hash() == edited.Hash() || before.Skills[0].Hash == moded.Skills[0].Hash {
		t.Errorf("hashes %s, %s, %s: a mode and an edit each change it", before.Hash(), moded.Hash(), edited.Hash())
	}
	if (Set{}).Hash() == before.Hash() || edited.Hash() != Find(state, "", "").Hash() {
		t.Error("a set's hash isn't its skills'")
	}
}
