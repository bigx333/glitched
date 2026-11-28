package mcp_test

import (
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/gittest"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// A delegate with isolation worktree starts its child in a worktree of
// its own, on a new branch from the caller's folder, in the caller's
// project; shared, the default, keeps it in the caller's folder.
func TestADelegateWithWorktreeIsolationStartsTheChildInAWorktree(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "start"}} {
		cmd := gittest.Command(append([]string{"-C", dir}, args...)...)
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
	got := serve(t, h, o,
		delegate(1, `{"task":"Review it","isolation":"worktree"}`, ""),
		delegate(2, `{"task":"Read it","isolation":"shared"}`, ""),
		delegate(3, `{"task":"x","isolation":"copy"}`, ""),
	)
	for _, id := range []int{1, 2} {
		if r := got[id]; r.Error != nil || r.Result.IsError {
			t.Fatalf("call %d: %+v", id, r)
		}
	}
	if r := got[3]; !r.Result.IsError || !strings.Contains(r.Result.Content[0].Text, `isolation is shared or worktree, not "copy"`) {
		t.Errorf("call 3: %+v, want a refusal of the unknown isolation", r)
	}
	for _, k := range children(t, h, o.Thread) {
		switch k.Title {
		case "Review it":
			if k.Project != dir || k.Branch == "" || !strings.HasPrefix(k.Workspace, root+string(filepath.Separator)) {
				t.Errorf("the worktree child is in %q on %q of %q, want a worktree under %q of %q", k.Workspace, k.Branch, k.Project, root, dir)
			}
		case "Read it":
			if k.Workspace != dir || k.Branch != "" {
				t.Errorf("the shared child is in %q on %q, want the caller's folder", k.Workspace, k.Branch)
			}
		}
	}
}

// An engine that makes no worktrees says so, and starts no child.
func TestADelegateWithWorktreeIsolationOnAnEngineWithoutWorktreesIsRefused(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	if r := serve(t, h, o, delegate(1, `{"task":"x","isolation":"worktree"}`, ""))[1]; !r.Result.IsError || !strings.Contains(r.Result.Content[0].Text, "this engine makes none") {
		t.Errorf("%+v, want a refusal that says the engine makes no worktrees", r)
	}
	if n := len(children(t, h, o.Thread)); n != 0 {
		t.Errorf("%d children, want none", n)
	}
}
