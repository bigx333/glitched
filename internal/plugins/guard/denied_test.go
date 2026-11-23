//go:build unix

package guard

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A run never reads a denied folder: not as its cwd, not by a path inside
// it, absolute, relative, after a flag's "=" or through a link; a folder
// beside it whose name starts the same runs.
func TestRunReadsNothingInADeniedFolder(t *testing.T) {
	root := t.TempDir()
	denied := filepath.Join(root, "secrets")
	held := filepath.Join(denied, "thread", "KEY")
	beside := filepath.Join(root, "secrets-other")
	for _, d := range []string{filepath.Dir(held), beside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(held, []byte("probe-value-0123"), 0o600)
	os.WriteFile(filepath.Join(beside, "f"), []byte("fine"), 0o600)
	link := filepath.Join(root, "link")
	if err := os.Symlink(denied, link); err != nil {
		t.Fatal(err)
	}
	at := func(program, cwd string, args ...string) Spec {
		return Spec{Program: program, Args: args, Cwd: cwd, Allowed: []string{program}, Denied: []string{denied}}
	}
	for name, s := range map[string]Spec{
		"absolute":   at("cat", root, held),
		"relative":   at("cat", root, "secrets/thread/KEY"),
		"dotted":     at("cat", filepath.Join(root, "secrets-other"), "../secrets/thread/KEY"),
		"the folder": at("ls", root, "secrets"),
		"flag":       at("grep", root, "-r", "probe", "--include=x", "--exclude-dir="+denied),
		"link":       at("cat", root, filepath.Join(link, "thread", "KEY")),
		"cwd":        at("cat", filepath.Dir(held), "KEY"),
		"not yet":    at("ls", root, filepath.Join(link, "new")),
	} {
		res, err := runT(t, s)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: ran (%v), got %q", name, err, res.Stdout)
		}
	}
	res := mustRun(t, at("cat", root, filepath.Join(beside, "f")))
	if string(res.Stdout) != "fine" {
		t.Errorf("a folder beside the denied one got %q", res.Stdout)
	}
}
