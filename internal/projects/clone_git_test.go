package projects

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fakes is a folder of the scripts given, by name, which a clone finds in
// place of git and gh. Each records its args, one a line, in <name>.args
// there.
func fakes(t *testing.T, scripts map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are shell scripts")
	}
	dir := t.TempDir()
	for name, body := range scripts {
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + filepath.Join(dir, name+".args") + "'\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// argsOf are the args the fake name in dir ran with, a line each, nil if
// it didn't run.
func argsOf(t *testing.T, dir, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name+".args"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func source(t *testing.T, s string) Source {
	t.Helper()
	src, ok := ParseSource(s)
	if !ok {
		t.Fatalf("ParseSource(%q) refused it", s)
	}
	return src
}

// owner/name clones with gh while gh is signed in to github.com, else
// with git, from GitHub.
func TestCloneOwnerName(t *testing.T) {
	gitClone := "/bin/mkdir -p \"$5\""
	for _, tc := range []struct {
		name    string
		scripts map[string]string
		ran     string
		args    func(dest string) []string
	}{
		{"gh signed in", map[string]string{"gh": "[ \"$1\" = auth ] && exit 0\n/bin/mkdir -p \"$4\"", "git": gitClone}, "gh",
			func(dest string) []string {
				return []string{"auth", "status", "--hostname", "github.com", "repo", "clone", "o/lighthouse", dest, "--", "--progress"}
			}},
		{"gh signed out", map[string]string{"gh": "exit 1", "git": gitClone}, "git",
			func(dest string) []string {
				return []string{"clone", "--progress", "--", "https://github.com/o/lighthouse.git", dest}
			}},
		{"no gh", map[string]string{"git": gitClone}, "git",
			func(dest string) []string {
				return []string{"clone", "--progress", "--", "https://github.com/o/lighthouse.git", dest}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := fakes(t, tc.scripts)
			dest := filepath.Join(t.TempDir(), "lighthouse")
			if err := Clone(context.Background(), source(t, "o/lighthouse"), dest, bin, func(string) {}); err != nil {
				t.Fatal(err)
			}
			if got, want := argsOf(t, bin, tc.ran), tc.args(dest); !slices.Equal(got, want) {
				t.Fatalf("%s ran with %q, want %q", tc.ran, got, want)
			}
			if tc.ran == "gh" && argsOf(t, bin, "git") != nil {
				t.Fatalf("git ran too, with %q", argsOf(t, bin, "git"))
			}
		})
	}
}

// Refused access says how to sign in, and no credentials from the URL.
func TestCloneRefusedSaysHowToSignIn(t *testing.T) {
	bin := fakes(t, map[string]string{"git": strings.Join([]string{
		"echo \"Cloning into '$5'...\" >&2",
		"echo 'remote: Repository not found.' >&2",
		"echo \"fatal: repository 'https://ghp_secret@github.com/o/n.git/' not found\" >&2",
		"exit 128",
	}, "\n")})
	err := Clone(context.Background(), source(t, "https://ghp_secret@github.com/o/n.git"), filepath.Join(t.TempDir(), "n"), bin, func(string) {})
	if err == nil {
		t.Fatal("the refused clone didn't fail")
	}
	f := err.Error()
	if !strings.Contains(f, "remote: Repository not found.") || !strings.HasSuffix(f, CloneHint) {
		t.Fatalf("the clone says %q", f)
	}
	if strings.Contains(f, "ghp_secret") || !strings.Contains(f, "https://github.com/o/n.git/") {
		t.Fatalf("the failure keeps the credentials, or loses the URL: %q", f)
	}
}

// A folder that's there and isn't empty is refused, and git doesn't run.
func TestCloneRefusesAFolderNotEmpty(t *testing.T) {
	bin := fakes(t, map[string]string{"git": "exit 0"})
	dest := filepath.Join(t.TempDir(), "lighthouse")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := Clone(context.Background(), source(t, "https://example.com/lighthouse.git"), dest, bin, func(string) {})
	if err == nil || err.Error() != dest+" already exists and isn't empty" {
		t.Fatalf("the clone into a full folder: %v", err)
	}
	if args := argsOf(t, bin, "git"); args != nil {
		t.Fatalf("git ran with %q", args)
	}
	if _, err := os.Stat(filepath.Join(dest, "notes.txt")); err != nil {
		t.Fatalf("the folder's file went: %v", err)
	}
}

// A clone that fails takes away the folder it made, and leaves an empty
// one that was there before it, which is the user's, as it found it.
func TestCloneFailureTakesOnlyTheFolderItMade(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	missing := source(t, "file://"+filepath.ToSlash(filepath.Join(t.TempDir(), "missing.git")))
	parent := t.TempDir()
	made := filepath.Join(parent, "made")
	if err := Clone(context.Background(), missing, made, filepath.Dir(git), func(string) {}); err == nil || strings.Contains(err.Error(), CloneHint) {
		t.Fatalf("the clone of a missing repository: %v", err)
	}
	if _, err := os.Stat(made); !os.IsNotExist(err) {
		t.Fatalf("the failed clone left its folder: %v", err)
	}
	found := filepath.Join(parent, "found")
	if err := os.Mkdir(found, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Clone(context.Background(), missing, found, filepath.Dir(git), func(string) {}); err == nil {
		t.Fatal("the clone of a missing repository didn't fail")
	}
	if _, err := os.Stat(found); err != nil {
		t.Fatalf("the failed clone took away the folder it found: %v", err)
	}
}
