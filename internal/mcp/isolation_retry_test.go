package mcp_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

func TestARetriedWorktreeDelegateMakesOneCheckout(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "start"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "worktrees")
	h, err := runtime.New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(true))}, runtime.WithWorktrees(root))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(); st.Close() })
	o := workingIn(t, h, dir)
	args := `{"task":"Review it","isolation":"worktree","client_request_id":"retry-one"}`
	got := serve(t, h, o, delegate(1, args, ""), delegate(2, args, `{"claudecode/toolUseId":"different"}`))
	first, second := got[1], got[2]
	kids := children(t, h, o.Thread)
	if len(got) != 2 || first.Error != nil || second.Error != nil || first.Result.IsError || second.Result.IsError || len(kids) != 1 {
		t.Fatalf("got replies %+v for %d children", got, len(kids))
	}
	if first.Result.StructuredContent.TaskID != second.Result.StructuredContent.TaskID || first.Result.StructuredContent.TaskID != kids[0].ID.String() {
		t.Fatalf("wrong ids: %v, %v, %v", first.Result.StructuredContent, second.Result.StructuredContent, kids[0].ID)
	}
	wt, err := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(wt), "worktree ") != 2 {
		t.Fatalf("wanted primary plus one child checkout: %s", wt)
	}
}
