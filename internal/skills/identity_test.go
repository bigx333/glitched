package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Build reads a skill only from the folder Find checked: one put in its
// place since, even with the same files, gives no overlay.
func TestBuildReadsOnlyTheFolderFindChecked(t *testing.T) {
	state, _ := folders(t)
	home := filepath.Join(state, "skills")
	dir := skill(t, home, "review", "Review a diff.")
	set := Find(state, "", "")
	if len(set.Skills) != 1 {
		t.Fatalf("skills %v, skipped %+v", names(set.Skills), set.Skipped)
	}
	if err := os.Rename(dir, filepath.Join(t.TempDir(), "review")); err != nil {
		t.Fatal(err)
	}
	skill(t, home, "review", "Review a diff.")
	if root, err := Build(state, set); err == nil || !strings.Contains(err.Error(), "moved or replaced") {
		t.Fatalf("built %q, %v: want the replaced folder refused", root, err)
	}
}

// A file with a second name elsewhere, a hard link, could be anyone's: its
// skill is skipped, and says why.
func TestFindSkipsASkillWithAHardLink(t *testing.T) {
	state, _ := folders(t)
	write(t, filepath.Join(state, "secret"), "a token\n")
	dir := skill(t, filepath.Join(state, "skills"), "leak", "Holds a hard link.")
	if err := os.Link(filepath.Join(state, "secret"), filepath.Join(dir, "token")); err != nil {
		t.Fatal(err)
	}
	set := Find(state, "", "")
	if len(set.Skills) != 0 || len(set.Skipped) != 1 || set.Skipped[0].Name != "leak" || !strings.Contains(set.Skipped[0].Why, "hard link") {
		t.Fatalf("skills %v, skipped %+v", names(set.Skills), set.Skipped)
	}
}

// A user's skill that leads into Glitched's own folder by another spelling
// of it, as a filesystem that ignores case takes, is still refused; where
// case counts, that spelling leads nowhere, and it is skipped too.
func TestFindKnowsGlitchedsFolderHoweverItIsSpelt(t *testing.T) {
	state, _ := folders(t)
	skill(t, filepath.Join(state, "secrets"), "evil", "In the state folder.")
	link(t, filepath.Join(filepath.Dir(state), "STATE", "secrets", "evil"), filepath.Join(state, "skills", "evil"))
	set := Find(state, "", "")
	if len(set.Skills) != 0 || len(set.Skipped) != 1 || set.Skipped[0].Name != "evil" {
		t.Fatalf("skills %v, skipped %+v", names(set.Skills), set.Skipped)
	}
}
