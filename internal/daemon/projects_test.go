package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/secrets"
	"glitched/internal/transport"
)

// The engine lists folders to a paired client on the tailnet, to one on
// its unix socket and to the GUI that embeds it, and to none of them its
// state folder, which holds its secrets: the folder it's in lists without
// it, and it isn't entered or cloned into by any name for it. Its scratch
// folder, which holds the folders of threads with no project, is listed,
// but for a link out of it into the rest of the state, and isn't cloned
// into.
func TestEveryWayInListsFoldersButNeverTheStateFolder(t *testing.T) {
	home := stateDir(t)
	dir := filepath.Join(home, "state")
	scratch := filepath.Join(dir, "scratch")
	for _, p := range []string{filepath.Join(secrets.Under(dir), "thread"), filepath.Join(dir, "worktrees"), filepath.Join(home, "kept"), filepath.Join(scratch, "x", "inner")} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(dir, filepath.Join(home, "to-state")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secrets.Under(dir), filepath.Join(scratch, "x", "to-secrets")); err != nil {
		t.Fatal(err)
	}
	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	d := start(t, Config{Dir: dir, Listen: []string{"127.0.0.1:0"}})
	unix := transport.NewEndpointClient(Socket(dir), api.ID{})
	t.Cleanup(func() { unix.Close() })
	embedded, ok := d.Endpoint().(transport.Projects)
	if !ok {
		t.Fatalf("the embedded engine, a %T, serves no Projects", d.Endpoint())
	}
	refused := []string{
		dir,
		filepath.Join(dir, "worktrees"),
		filepath.Join(secrets.Under(dir), "thread"),
		home + "/kept/../state",
		filepath.Join(home, "to-state"),
		filepath.Join(home, "to-state", "worktrees"),
		filepath.Join(scratch, "x", "to-secrets"),
	}
	if upper := strings.ToUpper(dir); upper != dir {
		if _, err := os.Stat(upper); err == nil {
			refused = append(refused, upper)
		}
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil && runtime.GOOS == "darwin" {
		if _, err := os.Stat(filepath.Join("/System/Volumes/Data", real)); err == nil {
			refused = append(refused, filepath.Join("/System/Volumes/Data", real))
		}
	}
	ctx := context.Background()
	for name, p := range map[string]transport.Projects{"the tailnet": remote(t, d, token), "the unix socket": unix, "the embedded engine": embedded} {
		list, err := p.Folders(ctx, api.FolderQuery{Path: home})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		var names []string
		for _, f := range list.Folders {
			names = append(names, f.Name)
		}
		if !slices.Equal(names, []string{"kept"}) {
			t.Errorf("%s lists %q in the folder the state folder is in, want kept alone", name, names)
		}
		for _, path := range refused {
			if list, err := p.Folders(ctx, api.FolderQuery{Path: path}); err == nil {
				t.Errorf("%s lists %s: %+v", name, path, list)
			}
		}
		for path, want := range map[string][]string{scratch: {"x"}, filepath.Join(scratch, "x"): {"inner"}, filepath.Join(home, "to-state", "scratch", "x"): {"inner"}} {
			list, err := p.Folders(ctx, api.FolderQuery{Path: path})
			var got []string
			for _, f := range list.Folders {
				got = append(got, f.Name)
			}
			if err != nil || !slices.Equal(got, want) {
				t.Errorf("%s lists %q in %s, %v; want %q", name, got, path, err, want)
			}
		}
		for _, dest := range []string{filepath.Join(scratch, "y"), filepath.Join(dir, "lighthouse"), filepath.Join(home, "to-state", "lighthouse"), filepath.Join(secrets.Under(dir), "lighthouse")} {
			if _, err := p.Clone(ctx, api.CloneRequest{Source: "o/lighthouse", Dest: dest}, func(string) {}); err == nil {
				t.Errorf("%s cloned into %s", name, dest)
			}
			if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Errorf("%s made %s: %v", name, dest, err)
			}
		}
	}
}
