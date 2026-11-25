package runtime_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

func TestAForkInANewWorktreeOutlivesItsSourcesWorktree(t *testing.T) {
	project := repo(t)
	ad := newForkAdapter(provider.ConversationFork{}, nil, false)
	path := dbPath(t)
	r := twoTurnsWith(t, path, tempDir(t), project, ad)
	fork := api.NewID()
	src := view(t, r.n.h, r.thread)
	submit(t, r.n.h, fork, api.ThreadCreate{
		Workspace: src.Workspace, Title: "separate worktree fork", Text: "start", Model: api.ModelRef{Provider: "fake"},
		Worktree: &api.NewWorktree{Base: src.Branch}, Fork: &api.Fork{Thread: src.ID, Run: r.run.Run},
	})
	until(t, r.n.h, fork, "the fork's first run", func(v api.Thread) bool { return idle(v) && v.LastTurn != nil })
	dst := view(t, r.n.h, fork)
	if dst.Workspace == src.Workspace {
		t.Fatal("fixture did not create a separate worktree")
	}
	submit(t, r.n.h, src.ID, api.ThreadDelete{RemoveWorktree: true})
	untilEffectsDone(t, path, "worktree.remove")
	if _, err := os.Stat(src.Workspace); !os.IsNotExist(err) {
		t.Fatalf("source worktree was not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst.Workspace, "README")); err != nil {
		t.Fatalf("fork should keep its own worktree: %v", err)
	}
	if dst.Project != project {
		t.Fatalf("fork's project is %q; want original repository %q", dst.Project, project)
	}
}

// Turns its source takes after a fork is made change nothing of what the
// fork is handed.
func TestASourcesLaterTurnsLeaveAForksHandoff(t *testing.T) {
	sourceAd, targetAd := newForkAdapter(provider.ConversationFork{}, nil, false), newForkAdapter(provider.ConversationFork{}, nil, false)
	n := serve(t, dbPath(t), provider.Mux{"fake": sourceAd, "other": targetAd})
	ws, src := t.TempDir(), api.NewID()
	older := strings.Repeat("A", 24303)
	newer := strings.Repeat("B", 24303)
	wait := func(thread api.ID, nturns int) {
		t.Helper()
		until(t, n.h, thread, "turn end", func(v api.Thread) bool { return idle(v) && len(runsOf(t, n.h, thread)) == nturns })
	}
	submit(t, n.h, src, api.ThreadCreate{Workspace: ws, Model: api.ModelRef{Provider: "fake"}, Text: older})
	wait(src, 1)
	to := submit(t, n.h, src, dispatch(newer, api.StartNow)).Command
	wait(src, 2)
	first, delayed := api.NewID(), api.NewID()
	for _, id := range []api.ID{first, delayed} {
		submit(t, n.h, id, api.ThreadCreate{Workspace: ws, Model: api.ModelRef{Provider: "other"}, Fork: &api.Fork{Thread: src, Run: to}})
	}
	send := func(id api.ID) string {
		submit(t, n.h, id, dispatch("go on", api.StartNow))
		wait(id, 1)
		_, _, sent := targetAd.seen()
		return sent[len(sent)-1]
	}
	initial := send(first)
	if !strings.Contains(initial, older) || !strings.Contains(initial, newer) {
		t.Fatalf("fixture's first handoff had %d turns, expected two", strings.Count(initial, "<turn>"))
	}
	for i := 3; i <= 10; i++ {
		submit(t, n.h, src, dispatch("unrelated later turn", api.StartNow))
		wait(src, i)
	}
	got := send(delayed)
	if got != initial {
		t.Fatalf("two forks created at the same boundary received different handoffs after unrelated source turns: before %d bytes and %d turns; after %d bytes and %d turns", len(initial), strings.Count(initial, "<turn>"), len(got), strings.Count(got, "<turn>"))
	}
}

// A fork from its source's worktree, however its path is spelled, belongs
// to the source's project.
func TestAForksWorktreeProjectIgnoresHowItsPathIsSpelled(t *testing.T) {
	for _, suffix := range []string{"", "/."} {
		t.Run(map[string]string{"": "exact", "/.": "same_path_spelled_differently"}[suffix], func(t *testing.T) {
			checkGLIT71ForkInNewWorktree(t, suffix)
		})
	}
}

func checkGLIT71ForkInNewWorktree(t *testing.T, suffix string) {
	t.Helper()
	project := repo(t)
	ad := newForkAdapter(provider.ConversationFork{}, nil, false)
	path := dbPath(t)
	r := twoTurnsWith(t, path, tempDir(t), project, ad)
	fork := api.NewID()
	src := view(t, r.n.h, r.thread)
	submit(t, r.n.h, fork, api.ThreadCreate{
		Workspace: src.Workspace + suffix, Title: "separate worktree fork", Text: "start", Model: api.ModelRef{Provider: "fake"},
		Worktree: &api.NewWorktree{Base: src.Branch}, Fork: &api.Fork{Thread: src.ID, Run: r.run.Run},
	})
	until(t, r.n.h, fork, "the fork's first run", func(v api.Thread) bool { return idle(v) && v.LastTurn != nil })
	dst := view(t, r.n.h, fork)
	if dst.Workspace == src.Workspace {
		t.Fatal("fixture did not create a separate worktree")
	}
	submit(t, r.n.h, src.ID, api.ThreadDelete{RemoveWorktree: true})
	untilEffectsDone(t, path, "worktree.remove")
	if _, err := os.Stat(src.Workspace); !os.IsNotExist(err) {
		t.Fatalf("source worktree was not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst.Workspace, "README")); err != nil {
		t.Fatalf("fork should keep its own worktree: %v", err)
	}
	if dst.Project != project {
		t.Fatalf("fork's project is %q; want original repository %q", dst.Project, project)
	}
}

type forkWorktreeSource struct {
	n      node
	thread api.ID
	ws     string
	run    api.TurnEnded
}

func twoTurnsWith(t *testing.T, path, dir, project string, ad provider.Adapter) forkWorktreeSource {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithWorktrees(filepath.Join(dir, "worktrees")))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	n := node{st, h}
	t.Cleanup(n.kill)
	thread := api.NewID()
	submit(t, h, thread, inWorktree(project, ""))
	until(t, h, thread, "the first turn's end", func(v api.Thread) bool { return idle(v) && len(runsOf(t, h, thread)) == 1 })
	submit(t, h, thread, dispatch("next", api.StartNow))
	until(t, h, thread, "the second turn's end", func(v api.Thread) bool { return idle(v) && len(runsOf(t, h, thread)) == 2 })
	return forkWorktreeSource{n: n, thread: thread, ws: view(t, h, thread).Workspace, run: runsOf(t, h, thread)[1]}
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
