package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"glitched/internal/api"
	"glitched/internal/secrets"
	"glitched/internal/transport"
)

// The engine lists folders to a paired client on the tailnet, to one on
// its unix socket and to the GUI that embeds it, and to none of them the
// folder that holds its secrets.
func TestEveryWayInListsFoldersButNeverTheSecrets(t *testing.T) {
	dir := stateDir(t)
	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	d := start(t, Config{Dir: dir, Listen: []string{"127.0.0.1:0"}})
	vault := secrets.Under(dir)
	for _, p := range []string{filepath.Join(vault, "thread"), filepath.Join(dir, "kept")} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	unix := transport.NewEndpointClient(Socket(dir), api.ID{})
	t.Cleanup(func() { unix.Close() })
	embedded, ok := d.Endpoint().(transport.Projects)
	if !ok {
		t.Fatalf("the embedded engine, a %T, serves no Projects", d.Endpoint())
	}
	ctx := context.Background()
	for name, p := range map[string]transport.Projects{"the tailnet": remote(t, d, token), "the unix socket": unix, "the embedded engine": embedded} {
		list, err := p.Folders(ctx, api.FolderQuery{Path: dir})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		var names []string
		for _, f := range list.Folders {
			names = append(names, f.Name)
		}
		if !slices.Contains(names, "kept") || slices.Contains(names, "secrets") {
			t.Errorf("%s lists %q in the state folder, want kept and not secrets", name, names)
		}
		if list, err := p.Folders(ctx, api.FolderQuery{Path: filepath.Join(vault, "thread")}); err == nil {
			t.Errorf("%s lists the secrets: %+v", name, list)
		}
		dest := filepath.Join(vault, "lighthouse")
		if _, err := p.Clone(ctx, api.CloneRequest{Source: "o/lighthouse", Dest: dest}, func(string) {}); err == nil {
			t.Errorf("%s cloned into the secrets", name)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Errorf("%s made %s: %v", name, dest, err)
		}
	}
}
