package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAPathUnderTheWorkspacesResolvedPathIsShownInside(t *testing.T) {
	real := t.TempDir()
	ws := filepath.Join(t.TempDir(), "ws")
	if err := os.Symlink(real, ws); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(ws, "a", "b.txt"), filepath.Join(resolved, "a", "b.txt")} {
		if shown := Rel(ws, path); shown != filepath.Join("a", "b.txt") {
			t.Errorf("Rel(%q, %q) = %q, want a/b.txt", ws, path, shown)
		}
	}
	if outside := filepath.Join(filepath.Dir(resolved), "other.txt"); Rel(ws, outside) != outside {
		t.Errorf("Rel(%q, %q) = %q, want it as given", ws, outside, Rel(ws, outside))
	}
}

func TestAPathThroughASymlinkIsShownAsGiven(t *testing.T) {
	ws, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "sub"), filepath.Join(ws, "via")); err != nil {
		t.Fatal(err)
	}
	for dir, contents := range map[string]string{ws: "workspace", outside: "outside"} {
		if err := os.WriteFile(filepath.Join(dir, "note"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := ws + "/via/../note"
	f, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if shown := Rel(ws, target); shown != target {
		t.Fatalf("the OS opens %q for %q, but Rel shows it as %q", f, target, shown)
	}
}
