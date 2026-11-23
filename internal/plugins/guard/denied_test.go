//go:build unix

package guard

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// vaultFixture is a folder to deny holding a value, a folder beside it
// whose name starts the same, and a link to the denied one.
func vaultFixture(t *testing.T) (root, denied, held, beside, link string) {
	t.Helper()
	root = t.TempDir()
	denied = filepath.Join(root, "secrets")
	held = filepath.Join(denied, "thread", "KEY")
	beside = filepath.Join(root, "secrets-other")
	link = filepath.Join(root, "link")
	for _, err := range []error{
		os.MkdirAll(filepath.Dir(held), 0o700), os.MkdirAll(beside, 0o700),
		os.WriteFile(held, []byte("probe-value-0123"), 0o600), os.WriteFile(filepath.Join(beside, "f"), []byte("fine"), 0o600),
		os.Symlink(denied, link),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return
}

// A run is refused when it names a path in a denied folder: absolute,
// relative, dotted, the folder itself, a file a flag reads, through a link,
// by another case of its name where the disk ignores case, or as its cwd.
// A folder beside it runs, and so does echo of the path, which opens
// nothing.
func TestRunReadsNothingInADeniedFolder(t *testing.T) {
	root, denied, held, beside, link := vaultFixture(t)
	at := func(program, cwd string, args ...string) Spec {
		return Spec{Program: program, Args: args, Cwd: cwd, Allowed: []string{program}, Denied: []string{denied}}
	}
	refused := map[string]Spec{
		"absolute":   at("cat", root, held),
		"relative":   at("cat", root, "secrets/thread/KEY"),
		"dotted":     at("cat", beside, "../secrets/thread/KEY"),
		"the folder": at("ls", root, "secrets"),
		"flag":       at("grep", root, "--file="+held, "/dev/null"),
		"link":       at("cat", root, filepath.Join(link, "thread", "KEY")),
		"cwd":        at("cat", filepath.Dir(held), "KEY"),
		"not yet":    at("ls", root, filepath.Join(link, "new")),
	}
	if upper := filepath.Join(root, "SECRETS", "thread", "KEY"); fileExists(upper) {
		refused["case"] = at("cat", root, upper)
	}
	for name, s := range refused {
		res, err := runT(t, s)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: ran (%v), got %q", name, err, res.Stdout)
		}
	}
	if res := mustRun(t, at("cat", root, filepath.Join(beside, "f"))); string(res.Stdout) != "fine" {
		t.Errorf("a folder beside the denied one got %q", res.Stdout)
	}
	if res := mustRun(t, at("echo", root, "--to="+held)); !strings.Contains(string(res.Stdout), held) {
		t.Errorf("echo of the path got %q", res.Stdout)
	}
}

// On macOS the kernel holds a run out of a denied folder however it gets
// there: a shell, a tree walk, a path it builds itself.
func TestRunHoldsAShellOutOfADeniedFolder(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("only macOS confines a run")
	}
	root, denied, held, beside, _ := vaultFixture(t)
	s := Spec{Program: "sh", Cwd: root, Allowed: []string{"sh"}, Denied: []string{denied},
		Args: []string{"-c", "cat " + filepath.Join(beside, "f") + "; d=secr; cat \"$d\"ets/thread/KEY; grep -r probe ."}}
	res := mustRun(t, s)
	if strings.Contains(string(res.Stdout), "probe-value") || !strings.Contains(string(res.Stdout), "fine") {
		t.Errorf("the shell printed %q (stderr %q); want the file beside and nothing of %s", res.Stdout, res.Stderr, held)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
