package runtime_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// A rewind of a thread that is no fork hands every turn that fits the
// cap, as it did before forks.
func TestARewindHandsEveryTurnThatFits(t *testing.T) {
	a := newRewinder(provider.Rewind{}, nil, false)
	n := start(t, dbPath(t), a)
	th := api.NewID()
	older := strings.Repeat("A", 24400)
	newer := strings.Repeat("B", 24400)
	submit(t, n.h, th, api.ThreadCreate{Workspace: t.TempDir(), Model: api.ModelRef{Provider: "fake"}, Text: older})
	finished := func(turns int) {
		t.Helper()
		until(t, n.h, th, fmt.Sprint(turns, " turns ended"), func(v api.Thread) bool { return idle(v) && len(runsOf(t, n.h, th)) == turns })
	}
	finished(1)
	kept := submit(t, n.h, th, dispatch(newer, api.StartNow)).Command
	finished(2)
	submit(t, n.h, th, dispatch("discard this turn", api.StartNow))
	finished(3)
	submit(t, n.h, th, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointRun, ID: kept}, Conversation: true})
	untilReverted(t, n.h, th, 1)
	const next = "after rewrite"
	submit(t, n.h, th, dispatch(next, api.StartNow))
	finished(4)

	_, _, sent := a.seen()
	prompt := strings.TrimSuffix(sent[len(sent)-1], next)
	head := "<rewound_conversation>\nThe user rewound this conversation to an earlier turn, and it goes on in a new session. These are its turns up to there, newest first.\n"
	const shape = "<turn>\n<user>\n%s\n</user>\n<assistant>\nDone.\n</assistant>\n</turn>\n"
	const tail = "</rewound_conversation>\n\n"
	both := head + fmt.Sprintf(shape, newer) + fmt.Sprintf(shape, older) + tail
	if len(both) > 48<<10 {
		t.Fatalf("bad fixture, both turns use %d bytes", len(both))
	}
	if !strings.Contains(prompt, older) || !strings.Contains(prompt, newer) {
		t.Fatalf("non-fork rewind handed %d bytes and %d turn(s), although both complete turns fit in %d bytes of the 49152-byte cap", len(prompt), strings.Count(prompt, "<turn>"), len(both))
	}
}

// A command sent after a native rewind of a fork that has yet to take its
// source up goes as it is; the turns go with the next message.
func TestACommandAfterARewindOfAPendingForkGoesAsItIs(t *testing.T) {
	f := newForking(t, provider.Rewind{}, false)
	fork := f.fork(t, 1, "other", "/status")
	f.again(t, fork, "/help", 2)
	f.other.mu.Lock()
	f.other.rewind = provider.Rewind{Session: "native-rewind", Native: true, At: "checkpoint"}
	f.other.native = true
	f.other.mu.Unlock()
	to := runsOf(t, f.n.h, fork)[0].Run
	submit(t, f.n.h, fork, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointRun, ID: to}, Conversation: true})
	if r := untilReverted(t, f.n.h, fork, 1); r.Conversation != api.RewindNative {
		t.Fatalf("fixture rewound %+v; want native", r)
	}
	const command = "/compact"
	f.again(t, fork, command, 3)
	_, _, sent := f.other.seen()
	if got := sent[len(sent)-1]; got != command {
		remaining, _ := f.n.st.Forked(ctx, fork)
		t.Fatalf("the slash command after a native rewind was sent as %q; origin still pending=%t", got, remaining != nil)
	}
	f.again(t, fork, "go on", 4)
	_, _, sent = f.other.seen()
	if got := sent[len(sent)-1]; !strings.Contains(got, "<user>\none\n</user>") || !strings.HasSuffix(got, "go on") {
		t.Fatalf("the message after the command was sent as %q; want the source's turns with it", got)
	}
	if pending, _ := f.n.st.Forked(ctx, fork); pending != nil {
		t.Fatal("the fork still waits to take its source up")
	}
}

// A fork into a new worktree from its source's worktree belongs to the
// source's project, so it reverts its files after that worktree is gone.
func TestAForkInANewWorktreeOutlivesItsSourcesWorktree(t *testing.T) {
	project := repo(t)
	ad := newRewinder(provider.Rewind{}, nil, false)
	path := dbPath(t)
	r := twoTurnsWith(t, path, tempDir(t), project, ad)
	fork := api.NewID()
	src := view(t, r.n.h, r.thread)
	submit(t, r.n.h, fork, api.ThreadCreate{
		Workspace: src.Workspace, Title: "separate worktree fork", Text: "start", Model: api.ModelRef{Provider: "fake"},
		Worktree: &api.NewWorktree{Base: src.Branch}, Fork: &api.Fork{Thread: src.ID, Run: r.run.Run},
	})
	until(t, r.n.h, fork, "the fork's first run", func(v api.Thread) bool { return idle(v) && v.LastTurn != nil })
	untilCheckpoints(t, r.n.h, fork, 2)
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
	to := api.CheckpointRef{Phase: api.CheckpointRun, ID: dst.LastTurn.Run}
	_, err := r.n.h.Submit(ctx, api.Command{ID: api.NewID(), Target: dst.ID, Body: api.ThreadRevert{To: to, Files: true}})
	if err != nil {
		t.Fatalf("fork's independent worktree cannot revert after source cleanup; project points at source=%t, original repository=%t: %s", dst.Project == src.Workspace, dst.Project == project, strings.ReplaceAll(err.Error(), src.Workspace, "<source-worktree>"))
	}
}

// Turns its source takes after a fork is made change nothing of what the
// fork is handed.
func TestASourcesLaterTurnsLeaveAForksHandoff(t *testing.T) {
	sourceAd, targetAd := newRewinder(provider.Rewind{}, nil, false), newRewinder(provider.Rewind{}, nil, false)
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
	ad := newRewinder(provider.Rewind{}, nil, false)
	path := dbPath(t)
	r := twoTurnsWith(t, path, tempDir(t), project, ad)
	fork := api.NewID()
	src := view(t, r.n.h, r.thread)
	submit(t, r.n.h, fork, api.ThreadCreate{
		Workspace: src.Workspace + suffix, Title: "separate worktree fork", Text: "start", Model: api.ModelRef{Provider: "fake"},
		Worktree: &api.NewWorktree{Base: src.Branch}, Fork: &api.Fork{Thread: src.ID, Run: r.run.Run},
	})
	until(t, r.n.h, fork, "the fork's first run", func(v api.Thread) bool { return idle(v) && v.LastTurn != nil })
	untilCheckpoints(t, r.n.h, fork, 2)
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
	write(t, filepath.Join(dst.Workspace, "README"), "a new edit\n")
	to := api.CheckpointRef{Phase: api.CheckpointRun, ID: dst.LastTurn.Run}
	_, err := r.n.h.Submit(ctx, api.Command{ID: api.NewID(), Target: dst.ID, Body: api.ThreadRevert{To: to, Files: true}})
	if err != nil {
		t.Fatalf("fork's independent worktree cannot revert after source cleanup; project points at source=%t, original repository=%t: %s", filepath.Clean(dst.Project) == src.Workspace, dst.Project == project, strings.ReplaceAll(err.Error(), src.Workspace, "<source-worktree>"))
	}

	done := untilReverted(t, r.n.h, fork, 1)
	if done.Status != api.RevertRestored || read(t, filepath.Join(dst.Workspace, "README")) != "hi\n" {
		t.Fatalf("fork failed to restore its own README after the source was removed: %+v", done)
	}
}
