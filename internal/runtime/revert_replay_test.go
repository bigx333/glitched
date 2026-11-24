package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// A revert stopped after it wrote some of the files finishes on the
// next host: the files it wrote are its own, not ones in the way, and it
// keeps the one recovery it took.
func TestAReplayTakesTheFilesItWroteAsItsOwn(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	r := twoTurns(t, path, dir, repo(t))
	p := filepath.Join(r.ws, "new.txt")
	last := filepath.Join(r.ws, "z.txt")
	write(t, last, "last\n")
	submit(t, r.n.h, r.thread, dispatch("record both files", api.StartNow))
	target := untilCheckpoints(t, r.n.h, r.thread, 4)[3]
	for _, name := range []string{p, last} {
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	entered := make(chan struct{})
	moves := 0
	reset := runtime.SetCheckpointHook(func(ctx context.Context, step string) {
		if step == "applying" {
			moves++
			if moves == 2 {
				close(entered)
				<-ctx.Done()
			}
		}
	})
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(target), Files: true})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		reset()
		t.Fatalf("first apply did not reach the second file: %+v", revertsOf(t, r.n.h, r.thread))
	}
	r.n.kill()
	reset()
	if got := read(t, p); got != "new\n" || exists(last) {
		t.Fatalf("crash fixture: new.txt is %q; z.txt exists=%t", got, exists(last))
	}
	n := serveCheckpoints(t, path, dir, fake.New(fake.Turn(false)))
	got := untilReverted(t, n.h, r.thread, 1)
	recoveries := gitIn(t, theStore(t, dir), "for-each-ref", "--format=%(refname)", "refs/glitched/"+r.thread.String()+"/recovery/")
	if refs := strings.Split(recoveries, "\n"); recoveries == "" || len(refs) != 1 {
		t.Errorf("recovery refs are %q, want exactly one", recoveries)
	}
	if got.Status != api.RevertRestored || read(t, last) != "last\n" {
		t.Fatalf("after restart got %s: %s; z.txt exists=%t", got.Status, got.Error, exists(last))
	}
}

// A rename that changes only a name's case reverts on a disk that
// ignores case, where the two names are one file.
func TestARevertUndoesACaseOnlyRename(t *testing.T) {
	r := twoTurns(t, dbPath(t), tempDir(t), repo(t))
	if _, err := os.Stat(filepath.Join(r.ws, "readme")); err != nil {
		t.Skip("worktree filesystem is case-sensitive")
	}
	gitIn(t, r.ws, "mv", "-f", "README", "readme")
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.run), Files: true})
	got := untilReverted(t, r.n.h, r.thread, 1)
	if got.Status != api.RevertRestored {
		t.Fatalf("case-only rename got %s: %s", got.Status, got.Error)
	}
}

// A folder where a file was goes back to the file, and Undo brings the
// folder back.
func TestARevertSwapsAFolderAndAFileBothWays(t *testing.T) {
	r := twoTurns(t, dbPath(t), tempDir(t), repo(t))
	p := filepath.Join(r.ws, "README")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(p, "nested"), "inside\n")
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.run), Files: true})
	got := untilReverted(t, r.n.h, r.thread, 1)
	if got.Status != api.RevertRestored || read(t, p) != "two\n" {
		t.Fatalf("folder to file: %+v; README is %q", got, read(t, p))
	}
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: api.CheckpointRef{ID: got.Revert, Phase: api.CheckpointRecovery}, Files: true})
	got = untilReverted(t, r.n.h, r.thread, 2)
	if got.Status != api.RevertRestored || read(t, filepath.Join(p, "nested")) != "inside\n" {
		t.Fatalf("file to folder undo: %+v; README/nested is %q", got, read(t, filepath.Join(p, "nested")))
	}
}

// A revert puts back an executable file over a symlink, and Undo the
// symlink.
func TestARevertKeepsSymlinksAndTheExecutableBit(t *testing.T) {
	r := twoTurns(t, dbPath(t), tempDir(t), repo(t))
	p := filepath.Join(r.ws, "new.txt")
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	submit(t, r.n.h, r.thread, dispatch("record the executable", api.StartNow))
	cp := untilCheckpoints(t, r.n.h, r.thread, 4)[3]
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README", p); err != nil {
		t.Fatal(err)
	}
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(cp), Files: true})
	got := untilReverted(t, r.n.h, r.thread, 1)
	if fi, err := os.Lstat(p); got.Status != api.RevertRestored || err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("regular executable restore: %+v, file info %v, err %v", got, fi, err)
	}
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: api.CheckpointRef{ID: got.Revert, Phase: api.CheckpointRecovery}, Files: true})
	got = untilReverted(t, r.n.h, r.thread, 2)
	if target, err := os.Readlink(p); got.Status != api.RevertRestored || err != nil || target != "README" {
		t.Fatalf("symlink undo: %+v, link target %q, err %v", got, target, err)
	}
}

// A revert left pending by a thread deleted since holds back no start in
// its folder once the next host skips it.
func TestADeletedThreadsPendingRevertHoldsNoStart(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	r := twoTurns(t, path, dir, repo(t))
	r.n.kill()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, b := range []api.Intent{
		api.ThreadRevert{To: runCheckpoint(r.first), Files: true},
		api.ThreadDelete{},
	} {
		if _, err := st.Submit(ctx, api.Command{ID: api.NewID(), Target: r.thread, Body: b}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	n := serveCheckpoints(t, path, dir, fake.New(fake.Turn(false)))
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		ids, err := n.st.Reverting(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deleted thread's pending revert did not drain")
		}
	}
	next := api.NewID()
	submit(t, n.h, next, api.ThreadCreate{Workspace: r.ws, Title: "after deletion", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Text: "start"})
	for deadline := time.Now().Add(time.Second); ; time.Sleep(2 * time.Millisecond) {
		th := view(t, n.h, next)
		if th.LastTurn != nil && idle(th) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("revert is drained but new thread is %v, waiting=%t", th.Run, runtime.Waiting(n.h, next))
		}
	}
}

// A start in a folder around the one being reverted waits, and so does
// one in it after the reverting thread's delete, while the revert runs.
func TestAStartAroundTheFolderOrAfterADeleteWaitsOnARevert(t *testing.T) {
	for _, scenario := range []string{"ancestor workspace", "reverting thread deleted"} {
		t.Run(scenario, func(t *testing.T) {
			r := twoTurns(t, dbPath(t), tempDir(t), repo(t))
			entered, release := make(chan struct{}), make(chan struct{})
			reset := runtime.SetCheckpointHook(func(ctx context.Context, step string) {
				if step == "recovered" {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
			})
			defer reset()
			defer close(release)
			submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("revert did not reach recovery hook")
			}
			ws := filepath.Dir(r.ws)
			if scenario == "reverting thread deleted" {
				submit(t, r.n.h, r.thread, api.ThreadDelete{})
				ws = r.ws
			}
			next := api.NewID()
			submit(t, r.n.h, next, api.ThreadCreate{Workspace: ws, Title: "while reverting", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Text: "start"})
			for deadline := time.Now().Add(time.Second); ; time.Sleep(2 * time.Millisecond) {
				th := view(t, r.n.h, next)
				if th.LastTurn != nil && idle(th) {
					t.Fatal("another turn finished while revert was still paused before applying")
				}
				if runtime.Waiting(r.n.h, next) {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("did not wait on the revert: %v", th.Run)
				}
			}
		})
	}
}

// A child whose start waits on its parent's revert writes nothing, so the
// revert replayed on the next host goes ahead.
func TestAParkedChildDoesntFailItsParentsReplay(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	serve := func() node {
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		h, err := runtime.New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(true)), "quick": fake.New(fake.Turn(false))},
			runtime.WithWorktrees(filepath.Join(dir, "worktrees")), runtime.WithCheckpoints(filepath.Join(dir, "checkpoints")))
		if err != nil {
			st.Close()
			t.Fatal(err)
		}
		n := node{st, h}
		t.Cleanup(n.kill)
		return n
	}
	n := serve()
	parent, kid := parentAndChild(t, n)
	p := filepath.Join(parent.Workspace, "README")
	write(t, p, "local changes\n")
	entered := make(chan struct{})
	reset := runtime.SetCheckpointHook(func(ctx context.Context, step string) {
		if step == "applying" {
			close(entered)
			<-ctx.Done()
		}
	})
	submit(t, n.h, parent.ID, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointBase, ID: parent.Turn.Run}, Files: true})
	<-entered
	submit(t, n.h, kid.ID, dispatch("start while revert is pending", api.StartNow))
	until(t, n.h, kid.ID, "the child's start waiting", func(api.Thread) bool { return runtime.Waiting(n.h, kid.ID) })
	n.kill()
	reset()
	n = serve()
	got := untilReverted(t, n.h, parent.ID, 1)
	if got.Status != api.RevertRestored || read(t, p) != "hi\n" {
		t.Fatalf("replay with a waiting child: %s: %s; README is %q", got.Status, got.Error, read(t, p))
	}
}

// A replay clears the index lock a git killed mid-restore left.
func TestAReplayClearsAStaleRestoreIndexLock(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	r := twoTurns(t, path, dir, repo(t))
	entered := make(chan struct{})
	reset := runtime.SetCheckpointHook(func(ctx context.Context, step string) {
		if step == "recovered" {
			close(entered)
			<-ctx.Done()
		}
	})
	rec := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		reset()
		t.Fatal("did not capture recovery")
	}
	r.n.kill()
	reset()
	write(t, filepath.Join(theStore(t, dir), "restore-"+rec.Command.String()+".index.lock"), "")
	n := serveCheckpoints(t, path, dir, fake.New(fake.Turn(false)))
	got := untilReverted(t, n.h, r.thread, 1)
	if got.Status != api.RevertRestored {
		t.Fatalf("replay with abandoned restore index lock: %s: %s", got.Status, got.Error)
	}
}

// The first of two reverts ending leaves the folder marked for the
// second.
func TestTheFolderStaysMarkedUntilEveryRevertEnds(t *testing.T) {
	r := twoTurns(t, dbPath(t), tempDir(t), repo(t))
	first, second, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	num := 0
	reset := runtime.SetCheckpointHook(func(ctx context.Context, step string) {
		if step != "recovered" {
			return
		}
		num++
		if num == 1 {
			close(first)
			select {
			case <-release:
			case <-ctx.Done():
			}
		} else if num == 2 {
			close(second)
			<-ctx.Done()
		}
	})
	defer func() {
		r.n.kill()
		reset()
	}()
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("revert did not reach recovery hook")
		}
	}
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
	wait(first)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.run), Files: true})
	close(release)
	wait(second)
	next := api.NewID()
	submit(t, r.n.h, next, api.ThreadCreate{Workspace: r.ws, Title: "while reverting", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Text: "start"})
	for deadline := time.Now().Add(time.Second); ; time.Sleep(2 * time.Millisecond) {
		th := view(t, r.n.h, next)
		if th.LastTurn != nil && idle(th) {
			t.Fatal("another turn finished while the second revert was still paused")
		}
		if runtime.Waiting(r.n.h, next) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not wait on the second revert: %v", th.Run)
		}
	}
}
