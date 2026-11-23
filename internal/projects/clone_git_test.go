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
	"time"

	"glitched/internal/api"
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
	gitClone := "/bin/mkdir -p \"$5\" && : > \"$5/README\""
	for _, tc := range []struct {
		name    string
		scripts map[string]string
		ran     string
		args    func(into string) []string
	}{
		{"gh signed in", map[string]string{"gh": "[ \"$1\" = auth ] && exit 0\n/bin/mkdir -p \"$4\" && : > \"$4/README\"", "git": gitClone}, "gh",
			func(into string) []string {
				return []string{"auth", "status", "--hostname", "github.com", "repo", "clone", "o/lighthouse", into, "--", "--progress"}
			}},
		{"gh signed out", map[string]string{"gh": "exit 1", "git": gitClone}, "git",
			func(into string) []string {
				return []string{"clone", "--progress", "--", "https://github.com/o/lighthouse.git", into}
			}},
		{"no gh", map[string]string{"git": gitClone}, "git",
			func(into string) []string {
				return []string{"clone", "--progress", "--", "https://github.com/o/lighthouse.git", into}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := fakes(t, tc.scripts)
			dest := filepath.Join(t.TempDir(), "lighthouse")
			if err := Clone(context.Background(), source(t, "o/lighthouse"), dest, bin, func(string) {}); err != nil {
				t.Fatal(err)
			}
			// It clones into a folder beside dest, which becomes dest.
			got := argsOf(t, bin, tc.ran)
			i := slices.IndexFunc(got, func(a string) bool {
				return strings.HasPrefix(a, filepath.Join(filepath.Dir(dest), ".lighthouse.glitched-clone-"))
			})
			if i < 0 {
				t.Fatalf("%s ran with %q, into no folder beside %s", tc.ran, got, dest)
			}
			if want := tc.args(got[i]); !slices.Equal(got, want) {
				t.Fatalf("%s ran with %q, want %q", tc.ran, got, want)
			}
			if _, err := os.Stat(filepath.Join(dest, "README")); err != nil {
				t.Fatalf("the clone isn't in %s: %v", dest, err)
			}
			if _, err := os.Stat(got[i]); !os.IsNotExist(err) {
				t.Fatalf("the folder it cloned into is still there: %v", err)
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

// waitFor waits for the file at p, which a fake makes once it runs.
func waitFor(t *testing.T, p string) {
	t.Helper()
	for range 500 {
		if _, err := os.Stat(p); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never came", p)
}

// held is a fake git's script that makes started in dir, waits for
// release there, then does then.
func held(dir, then string) string {
	return ": > '" + filepath.Join(dir, "started") + "'\nwhile [ ! -e '" + filepath.Join(dir, "release") + "' ]; do /bin/sleep 0.02; done\n" + then
}

// Of two clones into one folder, the second to finish fails, and the
// first's files stay as it left them.
func TestCloneSecondIntoAFolderLeavesTheFirst(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "lighthouse")
	sig := t.TempDir()
	slow := fakes(t, map[string]string{"git": held(sig, "echo second > \"$5/who\"")})
	errs := make(chan error, 1)
	go func() {
		errs <- Clone(context.Background(), source(t, "https://example.com/lighthouse.git"), dest, slow, func(string) {})
	}()
	waitFor(t, filepath.Join(sig, "started"))
	fast := fakes(t, map[string]string{"git": "echo first > \"$5/who\""})
	if err := Clone(context.Background(), source(t, "https://example.com/lighthouse.git"), dest, fast, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sig, "release"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err == nil || err.Error() != dest+" already exists and isn't empty" {
		t.Fatalf("the second clone: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "who")); err != nil || string(b) != "first\n" {
		t.Fatalf("the first clone's file is %q, %v", b, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(dest)); len(entries) != 1 {
		t.Fatalf("the clones left %v beside the folder", entries)
	}
}

// A file the user puts in the folder while a clone runs stays when the
// clone fails.
func TestCloneFailureLeavesWhatTheUserAdded(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "lighthouse")
	sig := t.TempDir()
	bin := fakes(t, map[string]string{"git": held(sig, "echo 'fatal: the remote hung up' >&2\nexit 128")})
	errs := make(chan error, 1)
	go func() {
		errs <- Clone(context.Background(), source(t, "https://example.com/lighthouse.git"), dest, bin, func(string) {})
	}()
	waitFor(t, filepath.Join(sig, "started"))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sig, "release"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err == nil || err.Error() != "fatal: the remote hung up" {
		t.Fatalf("the clone: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "notes.txt")); err != nil || string(b) != "mine" {
		t.Fatalf("the user's file is %q, %v", b, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(dest)); len(entries) != 1 {
		t.Fatalf("the clone left %v beside the folder", entries)
	}
}

// With no git a clone makes no folder, not even the one dest is in.
func TestCloneWithNoGitMakesNoFolder(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "Projects")
	err := Clone(context.Background(), source(t, "https://example.com/lighthouse.git"), filepath.Join(parent, "lighthouse"), t.TempDir(), func(string) {})
	if err == nil || err.Error() != noGit {
		t.Fatalf("the clone with no git: %v", err)
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("the clone made %s: %v", parent, err)
	}
}

// The lines a clone says have no credentials in them, and name dest, not
// the folder the clone goes into first.
func TestCloneProgressHasNoCredentials(t *testing.T) {
	bin := fakes(t, map[string]string{"git": strings.Join([]string{
		"echo \"Cloning into '$5'...\" >&2",
		"echo 'remote: fetching https://me:tok_secret@example.com/r.git' >&2",
		"printf 'Receiving objects:  10%% (1/10) https://tok_secret@example.com\\r' >&2",
	}, "\n")})
	dest := filepath.Join(t.TempDir(), "r")
	var said []string
	if err := Clone(context.Background(), source(t, "https://me:tok_secret@example.com/r.git"), dest, bin, func(line string) { said = append(said, line) }); err != nil {
		t.Fatal(err)
	}
	want := []string{"Cloning into '" + dest + "'...", "remote: fetching https://example.com/r.git", "Receiving objects:  10% (1/10) https://example.com"}
	if !slices.Equal(said, want) {
		t.Fatalf("the clone said %q, want %q", said, want)
	}
}

// A source that isn't one isn't said back.
func TestCloneIntoRefusesASourceUnsaid(t *testing.T) {
	_, err := CloneInto(context.Background(), api.CloneRequest{Source: "ftp://me:tok_secret@example.com/r.git", Dest: t.TempDir()}, nil, t.TempDir(), func(string) {})
	if err == nil || strings.Contains(err.Error(), "tok_secret") {
		t.Fatalf("the clone of a source that isn't one: %v", err)
	}
}
