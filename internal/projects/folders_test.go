package projects

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
)

// tree makes each path under root: a folder, or a file where it ends in
// "=", and returns root.
func tree(t *testing.T, root string, paths ...string) string {
	t.Helper()
	for _, p := range paths {
		at := filepath.Join(root, strings.TrimSuffix(p, "="))
		if strings.HasSuffix(p, "=") {
			if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(at, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(at, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func link(t *testing.T, target, at string) {
	t.Helper()
	if err := os.Symlink(target, at); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
}

func names(l api.FolderList) []string {
	var out []string
	for _, f := range l.Folders {
		out = append(out, f.Name)
	}
	return out
}

func listed(t *testing.T, path string, hide ...string) api.FolderList {
	t.Helper()
	l, err := List(context.Background(), path, hide)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// Only folders are listed, a symlink to one among them, dot folders too,
// sorted by name whatever its case, each saying whether it holds a .git.
func TestListListsFolders(t *testing.T) {
	dir := tree(t, t.TempDir(), "beta/.git", "Alpha", ".config", "gamma/.git=", "notes.txt=", "elsewhere/delta")
	link(t, filepath.Join(dir, "elsewhere", "delta"), filepath.Join(dir, "delta"))
	link(t, filepath.Join(dir, "notes.txt"), filepath.Join(dir, "file-link"))
	link(t, filepath.Join(dir, "missing"), filepath.Join(dir, "dangling"))
	l := listed(t, dir)
	want := []api.Folder{{Name: ".config"}, {Name: "Alpha"}, {Name: "beta", Repo: true}, {Name: "delta"}, {Name: "elsewhere"}, {Name: "gamma", Repo: true}}
	if !slices.Equal(l.Folders, want) {
		t.Fatalf("listed %+v, want %+v", l.Folders, want)
	}
	if l.Path != dir || l.Parent != filepath.Dir(dir) || l.Truncated {
		t.Fatalf("path %q parent %q truncated %v, want %q in %q, whole", l.Path, l.Parent, l.Truncated, dir, filepath.Dir(dir))
	}
}

func TestListStopsAtAThousandFolders(t *testing.T) {
	dir := t.TempDir()
	for i := range maxFolders + 1 {
		tree(t, dir, fmt.Sprintf("f%04d", i))
	}
	l := listed(t, dir)
	if len(l.Folders) != maxFolders || !l.Truncated || l.Folders[0].Name != "f0000" || l.Folders[maxFolders-1].Name != "f0999" {
		t.Fatalf("listed %d folders, truncated %v, from %q; want the first 1000 of 1001, truncated", len(l.Folders), l.Truncated, l.Folders[0].Name)
	}
	os.Remove(filepath.Join(dir, "f1000"))
	if l := listed(t, dir); len(l.Folders) != maxFolders || l.Truncated {
		t.Fatalf("1000 folders listed %d, truncated %v; want all of them", len(l.Folders), l.Truncated)
	}
}

func TestListRefusesARelativePath(t *testing.T) {
	for _, p := range []string{"Projects", "./Projects", "../x", "~nobody/x"} {
		if l, err := List(context.Background(), p, nil); err == nil {
			t.Errorf("List(%q) = %+v, want it refused", p, l)
		}
	}
}

func TestListExpandsHome(t *testing.T) {
	home := tree(t, t.TempDir(), "Projects/glitched")
	t.Setenv("HOME", home)
	for p, want := range map[string]string{"": home, "~": home, "~/": home, "~/Projects": filepath.Join(home, "Projects")} {
		l := listed(t, p)
		if l.Path != want || l.Home != home {
			t.Errorf("List(%q) listed %q with home %q, want %q with %q", p, l.Path, l.Home, want, home)
		}
	}
	if got := names(listed(t, "~/Projects")); !slices.Equal(got, []string{"glitched"}) {
		t.Errorf("~/Projects listed %q", got)
	}
}

func TestListOfTheRootHasNoParent(t *testing.T) {
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = filepath.VolumeName(os.TempDir()) + root
	}
	if l := listed(t, root); l.Path != root || l.Parent != "" {
		t.Fatalf("the root listed %q with parent %q, want %q and none", l.Path, l.Parent, root)
	}
}

// A folder the engine's user can't read is an error, not an empty list.
func TestListOfAnUnreadableFolderFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("root reads every folder")
	}
	dir := tree(t, t.TempDir(), "locked/inside")
	locked := filepath.Join(dir, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if l, err := List(context.Background(), locked, nil); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("an unreadable folder listed %+v, %v; want a permission error", l, err)
	}
}

// state is an engine's state folder, with its secrets and a folder
// beside them, and the hide list naming the secrets.
func state(t *testing.T) (string, []string) {
	dir := tree(t, t.TempDir(), "state/secrets/thread", "state/worktrees", "other")
	st := filepath.Join(dir, "state")
	return st, []string{filepath.Join(st, "secrets")}
}

// The secrets folder is never listed or entered, however it's named.
func TestListNeverEntersTheSecrets(t *testing.T) {
	st, hide := state(t)
	secrets := hide[0]
	other := filepath.Join(filepath.Dir(st), "other")
	link(t, secrets, filepath.Join(other, "to-secrets"))
	link(t, filepath.Join(secrets, "thread"), filepath.Join(other, "to-thread"))
	link(t, st, filepath.Join(other, "to-state"))
	for _, p := range []string{
		secrets,
		filepath.Join(secrets, "thread"),
		filepath.Join(st, "worktrees", "..", "secrets"),
		filepath.Join(other, "to-secrets"),
		filepath.Join(other, "to-thread"),
		filepath.Join(other, "to-state", "secrets"),
	} {
		if l, err := List(context.Background(), p, hide); !errors.Is(err, errHidden) {
			t.Errorf("List(%q) = %+v, %v; want it refused as the secrets", p, l, err)
		}
	}
	// A disk that ignores case finds the folder by another case of its
	// name; one that doesn't finds no such folder.
	upper := filepath.Join(st, "SECRETS")
	_, err := List(context.Background(), upper, hide)
	if _, there := os.Stat(upper); there == nil && !errors.Is(err, errHidden) || err == nil {
		t.Errorf("List(%q): %v; want it refused", upper, err)
	}
}

// Nor is it shown in a listing, by itself or by a symlink to it or into
// it, though the folders beside it are.
func TestListLeavesOutTheSecrets(t *testing.T) {
	st, hide := state(t)
	other := filepath.Join(filepath.Dir(st), "other")
	link(t, hide[0], filepath.Join(other, "to-secrets"))
	link(t, filepath.Join(hide[0], "thread"), filepath.Join(other, "to-thread"))
	tree(t, other, "kept")
	if got := names(listed(t, st, hide...)); !slices.Equal(got, []string{"worktrees"}) {
		t.Errorf("the state folder listed %q, want only worktrees", got)
	}
	if got := names(listed(t, other, hide...)); !slices.Equal(got, []string{"kept"}) {
		t.Errorf("a folder with symlinks to the secrets listed %q, want only kept", got)
	}
}

// The hidden folder is found by where it is, whatever path names it.
func TestListHidesASecretsFolderNamedThroughASymlink(t *testing.T) {
	st, hide := state(t)
	via := filepath.Join(t.TempDir(), "via")
	link(t, st, via)
	if _, err := List(context.Background(), hide[0], []string{filepath.Join(via, "secrets")}); !errors.Is(err, errHidden) {
		t.Errorf("the secrets, hidden through a symlink: %v; want them refused", err)
	}
}

// On macOS a folder has a second path through the data volume's
// firmlink, which no symlink resolves; it still names the secrets.
func TestListHidesTheSecretsByAnotherPathToThem(t *testing.T) {
	_, hide := state(t)
	real, err := filepath.EvalSymlinks(hide[0])
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join("/System/Volumes/Data", real)
	if info, err := os.Stat(other); err != nil || !info.IsDir() {
		t.Skip("no second path to the folder here")
	}
	if _, err := List(context.Background(), other, hide); !errors.Is(err, errHidden) {
		t.Errorf("List(%q): %v; want it refused as the secrets", other, err)
	}
	if got := names(listed(t, filepath.Join("/System/Volumes/Data", filepath.Dir(real)), hide...)); slices.Contains(got, "secrets") {
		t.Errorf("the state folder by its second path listed %q", got)
	}
}

// A clone never goes into the secrets folder, however it's named, nor
// makes a folder there; one not yet made counts too.
func TestCloneIntoTheSecretsIsRefused(t *testing.T) {
	st, hide := state(t)
	other := filepath.Join(filepath.Dir(st), "other")
	link(t, hide[0], filepath.Join(other, "to-secrets"))
	link(t, st, filepath.Join(other, "to-state"))
	unmade := filepath.Join(t.TempDir(), "secrets")
	for _, tc := range []struct{ dest, made string }{
		{filepath.Join(hide[0], "lighthouse"), filepath.Join(hide[0], "lighthouse")},
		{filepath.Join(st, "worktrees", "..", "secrets", "lighthouse"), filepath.Join(hide[0], "lighthouse")},
		{filepath.Join(other, "to-secrets", "lighthouse"), filepath.Join(hide[0], "lighthouse")},
		{filepath.Join(other, "to-state", "secrets", "new", "lighthouse"), filepath.Join(hide[0], "new")},
		{filepath.Join(unmade, "lighthouse"), unmade},
		{filepath.Join(filepath.Dir(unmade), "SECRETS", "lighthouse"), filepath.Join(filepath.Dir(unmade), "SECRETS")},
	} {
		_, err := CloneInto(context.Background(), api.CloneRequest{Source: "https://example.com/lighthouse.git", Dest: tc.dest}, append(hide, unmade), "", func(string) {})
		if !errors.Is(err, errHidden) {
			t.Errorf("a clone into %q: %v; want it refused as the secrets", tc.dest, err)
		}
		if _, err := os.Stat(tc.made); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a clone into %q made %q", tc.dest, tc.made)
		}
	}
}

// A clone into a folder under ~ goes there, says git's lines, and gives
// the folder as an absolute path.
func TestCloneIntoExpandsHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake git is a shell script")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"Cloning into '$5'...\" >&2\necho 'Receiving objects: 100% (3/3)' >&2\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	cloned, err := CloneInto(context.Background(), api.CloneRequest{Source: "https://example.com/lighthouse.git", Dest: "~/Projects/lighthouse"}, nil, bin, func(l string) { lines = append(lines, l) })
	want := filepath.Join(home, "Projects", "lighthouse")
	if err != nil || cloned.Path != want {
		t.Fatalf("cloned into %q, %v; want %q", cloned.Path, err, want)
	}
	if !slices.Equal(lines, []string{"Cloning into '" + want + "'...", "Receiving objects: 100% (3/3)"}) {
		t.Fatalf("the clone said %q", lines)
	}
	for _, dest := range []string{"Projects/lighthouse", "~nobody/lighthouse"} {
		if _, err := CloneInto(context.Background(), api.CloneRequest{Source: "o/lighthouse", Dest: dest}, nil, bin, func(string) {}); err == nil {
			t.Errorf("a clone into %q went ahead, want it refused", dest)
		}
	}
}

// A clone through a symlink never makes anything in the state folder: not
// into a link that points there from a folder it may go in, not under a
// folder that links there, and not beside a link in it that points out.
func TestCloneThroughASymlinkMakesNothingInTheState(t *testing.T) {
	st, _ := state(t)
	hide := []string{st}
	other := filepath.Join(filepath.Dir(st), "other")
	out := tree(t, t.TempDir(), "empty")
	link(t, filepath.Join(st, "worktrees"), filepath.Join(other, "lighthouse"))
	link(t, st, filepath.Join(other, "to-state"))
	link(t, filepath.Join(out, "empty"), filepath.Join(st, "out"))
	bin := fakes(t, map[string]string{"git": ": > \"$5/README\""})
	before := walk(t, st)
	for _, dest := range []string{
		filepath.Join(other, "lighthouse"),
		filepath.Join(other, "to-state", "lighthouse"),
		filepath.Join(st, "out"),
	} {
		if _, err := CloneInto(context.Background(), api.CloneRequest{Source: "https://example.com/lighthouse.git", Dest: dest}, hide, bin, func(string) {}); err == nil {
			t.Errorf("a clone into %q went ahead", dest)
		}
		if after := walk(t, st); !slices.Equal(after, before) {
			t.Fatalf("a clone into %q made %q in the state folder", dest, slices.DeleteFunc(after, func(p string) bool { return slices.Contains(before, p) }))
		}
	}
	if args := argsOf(t, bin, "git"); args != nil {
		t.Errorf("git ran with %q", args)
	}
	if entries, _ := os.ReadDir(filepath.Join(out, "empty")); len(entries) > 0 {
		t.Errorf("the folder out of the state holds %v", entries)
	}
}

// walk is every path under dir, without following symlinks.
func walk(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		paths = append(paths, p)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return paths
}
