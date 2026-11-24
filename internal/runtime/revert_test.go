package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

func revertsOf(t *testing.T, ep api.Endpoint, thread api.ID) []api.ThreadReverted {
	t.Helper()
	var out []api.ThreadReverted
	for _, e := range events(t, ep, thread) {
		if b, _ := e.Decode(); b != nil {
			if r, ok := b.(api.ThreadReverted); ok {
				out = append(out, r)
			}
		}
	}
	return out
}

func untilReverted(t *testing.T, ep api.Endpoint, thread api.ID, n int) api.ThreadReverted {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		if rs := revertsOf(t, ep, thread); len(rs) >= n {
			return rs[n-1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for revert %d: %+v", n, revertsOf(t, ep, thread))
		}
	}
}

// reverting is a thread of the user's in a worktree of its own after two
// turns: the first left README as it was made, the second changed it and
// added new.txt, and since then README was changed again.
type reverting struct {
	n          node
	dir        string
	thread     api.ID
	ws         string
	first, run api.CheckpointCaptured
}

func twoTurns(t *testing.T, path, dir string, project string) reverting {
	t.Helper()
	n := serveCheckpoints(t, path, dir, fake.New(fake.Turn(false)))
	thread := api.NewID()
	submit(t, n.h, thread, inWorktree(project, ""))
	until(t, n.h, thread, "the first turn's end", idle)
	untilCheckpoints(t, n.h, thread, 2)
	ws := view(t, n.h, thread).Workspace
	write(t, filepath.Join(ws, "README"), "two\n")
	write(t, filepath.Join(ws, "new.txt"), "new\n")
	submit(t, n.h, thread, dispatch("next", api.StartNow))
	cps := untilCheckpoints(t, n.h, thread, 3)
	until(t, n.h, thread, "the second turn's end", idle)
	write(t, filepath.Join(ws, "README"), "three\n")
	return reverting{n: n, dir: dir, thread: thread, ws: ws, first: cps[1], run: cps[2]}
}

func runCheckpoint(c api.CheckpointCaptured) api.CheckpointRef {
	return api.CheckpointRef{Phase: api.CheckpointRun, ID: c.Run}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// A revert puts the files back as the checkpoint holds them, after a
// recovery checkpoint of them as they were, which a revert to undoes;
// HEAD, the index and the branch stay, and the checkout's git with them.
func TestARevertRestoresTheFilesAndItsRecoveryUndoesIt(t *testing.T) {
	project := repo(t)
	r := twoTurns(t, dbPath(t), tempDir(t), project)
	git := files(t, filepath.Join(project, ".git"))
	head := gitIn(t, r.ws, "rev-parse", "HEAD")

	rec := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
	done := untilReverted(t, r.n.h, r.thread, 1)
	if done.Status != api.RevertRestored || done.Changed != 2 || done.Recovery == "" || done.Revert != rec.Command {
		t.Fatalf("revert %+v, want README and new.txt restored, with a recovery", done)
	}
	if got := read(t, filepath.Join(r.ws, "README")); got != "hi\n" {
		t.Fatalf("README %q after the revert, want the first turn's", got)
	}
	if exists(filepath.Join(r.ws, "new.txt")) {
		t.Fatal("new.txt is there after a revert to before it was")
	}
	store := theStore(t, r.dir)
	recovery := "refs/glitched/" + r.thread.String() + "/recovery/" + rec.Command.String()
	if got := gitIn(t, store, "rev-parse", recovery); got != done.Recovery {
		t.Fatalf("the recovery ref is at %s, want %s", got, done.Recovery)
	}
	if got := gitIn(t, store, "show", done.Recovery+":README"); got != "three" {
		t.Fatalf("the recovery holds README %q, want it as it was before the revert", got)
	}

	submit(t, r.n.h, r.thread, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointRecovery, ID: rec.Command}, Files: true})
	undo := untilReverted(t, r.n.h, r.thread, 2)
	if undo.Status != api.RevertRestored || undo.Changed != 2 {
		t.Fatalf("undo %+v, want the two files back", undo)
	}
	if got, added := read(t, filepath.Join(r.ws, "README")), read(t, filepath.Join(r.ws, "new.txt")); got != "three\n" || added != "new\n" {
		t.Fatalf("after the undo README %q and new.txt %q, want them as they were before the revert", got, added)
	}
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointRecovery, ID: rec.Command}, Files: true})
	if again := untilReverted(t, r.n.h, r.thread, 3); again.Status != api.RevertUnchanged {
		t.Fatalf("a revert to the files as they are: %+v, want unchanged", again)
	}
	if got := gitIn(t, r.ws, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved from %s to %s", head, got)
	}
	if after := files(t, filepath.Join(project, ".git")); !reflect.DeepEqual(after, git) {
		t.Fatal("the checkout's git changed in a revert")
	}
	assertReplay(t, r.n.st)
}

// A file the recovery checkpoint didn't save, as an ignored one, in the
// way of one the revert writes, refuses the revert with nothing changed.
func TestARevertBlockedByAnUnsavedFileChangesNothing(t *testing.T) {
	n := serveCheckpoints(t, dbPath(t), tempDir(t), fake.New(fake.Turn(false)))
	thread := api.NewID()
	project := repo(t)
	submit(t, n.h, thread, inWorktree(project, ""))
	until(t, n.h, thread, "the first turn's end", idle)
	ws := view(t, n.h, thread).Workspace
	write(t, filepath.Join(ws, "out.txt"), "from the turn\n")
	submit(t, n.h, thread, dispatch("next", api.StartNow))
	first := untilCheckpoints(t, n.h, thread, 3)[2]
	until(t, n.h, thread, "the second turn's end", idle)
	os.Remove(filepath.Join(ws, "out.txt"))
	write(t, filepath.Join(ws, ".gitignore"), "out.txt\n")
	submit(t, n.h, thread, dispatch("again", api.StartNow))
	untilCheckpoints(t, n.h, thread, 4)
	until(t, n.h, thread, "the third turn's end", idle)
	write(t, filepath.Join(ws, "out.txt"), "precious\n")
	before := files(t, ws)

	submit(t, n.h, thread, api.ThreadRevert{To: runCheckpoint(first), Files: true})
	done := untilReverted(t, n.h, thread, 1)
	if done.Status != api.RevertFailed || !strings.Contains(done.Error, "out.txt is in the way") || done.Changed != 0 {
		t.Fatalf("revert %+v, want it refused for out.txt", done)
	}
	if after := files(t, ws); !reflect.DeepEqual(after, before) {
		t.Fatalf("a refused revert changed the worktree:\n%v\nwant\n%v", after, before)
	}
	assertReplay(t, n.st)
}

// A revert that fails part way puts back what it had changed, as the
// recovery checkpoint holds it, and says so.
func TestARevertThatFailsPutsTheFilesBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a folder it can't")
	}
	n := serveCheckpoints(t, dbPath(t), tempDir(t), fake.New(fake.Turn(false)))
	thread := api.NewID()
	project := repo(t)
	write(t, filepath.Join(project, "sub", "a"), "one\n")
	gitIn(t, project, "add", ".")
	gitIn(t, project, "commit", "-q", "-m", "sub")
	submit(t, n.h, thread, inWorktree(project, ""))
	first := untilCheckpoints(t, n.h, thread, 2)[1]
	until(t, n.h, thread, "the first turn's end", idle)
	ws := view(t, n.h, thread).Workspace
	write(t, filepath.Join(ws, "README"), "two\n")
	write(t, filepath.Join(ws, "sub", "a"), "two\n")
	submit(t, n.h, thread, dispatch("next", api.StartNow))
	untilCheckpoints(t, n.h, thread, 3)
	until(t, n.h, thread, "the second turn's end", idle)
	if err := os.Chmod(filepath.Join(ws, "sub"), 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(ws, "sub"), 0o755)

	submit(t, n.h, thread, api.ThreadRevert{To: runCheckpoint(first), Files: true})
	done := untilReverted(t, n.h, thread, 1)
	if done.Status != api.RevertFailed || !strings.Contains(done.Error, "sub/a couldn't be put back") || !strings.Contains(done.Error, "the files were put back as they were") {
		t.Fatalf("revert %+v, want it failed at sub/a, with the files put back", done)
	}
	if got, sub := read(t, filepath.Join(ws, "README")), read(t, filepath.Join(ws, "sub", "a")); got != "two\n" || sub != "two\n" {
		t.Fatalf("after a failed revert README %q and sub/a %q, want both as they were before it", got, sub)
	}
	assertReplay(t, n.st)
}

// A host that dies part way through a revert leaves it to the next, which
// finds the recovery checkpoint the first took, never takes one of a tree
// half put back, and ends the revert once.
func TestACrashMidRevertConverges(t *testing.T) {
	for _, step := range []string{"recovered", "applying", "applied"} {
		t.Run("at "+step, func(t *testing.T) {
			path, dir := dbPath(t), tempDir(t)
			r := twoTurns(t, path, dir, repo(t))
			var mu sync.Mutex
			steps := map[string]int{}
			block := step
			entered := make(chan struct{}, 1)
			defer runtime.SetCheckpointHook(func(ctx context.Context, s string) {
				mu.Lock()
				steps[s]++
				hold := s == block
				if hold {
					block = ""
				}
				mu.Unlock()
				if hold {
					entered <- struct{}{}
					<-ctx.Done()
				}
			})()
			rec := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
			<-entered
			r.n.kill()
			mu.Lock()
			clear(steps)
			mu.Unlock()

			n := serveCheckpoints(t, path, dir, fake.New(fake.Turn(false)))
			done := untilReverted(t, n.h, r.thread, 1)
			store := theStore(t, dir)
			if done.Status != api.RevertRestored || done.Changed != 2 {
				t.Fatalf("revert %+v after the crash, want it restored", done)
			}
			if got := gitIn(t, store, "for-each-ref", "--format=%(refname)", "refs/glitched/"+r.thread.String()+"/recovery/"); got != "refs/glitched/"+r.thread.String()+"/recovery/"+rec.Command.String() {
				t.Fatalf("recovery refs %q, want the one revert's", got)
			}
			if got := gitIn(t, store, "show", done.Recovery+":README"); got != "three" || !strings.Contains(gitIn(t, store, "ls-tree", "--name-only", done.Recovery), "new.txt") {
				t.Fatalf("the recovery holds README %q, want the files as they were before the revert", got)
			}
			mu.Lock()
			captured := steps["captured"]
			mu.Unlock()
			if captured != 0 {
				t.Fatalf("the next host captured %d times; want it to find the recovery", captured)
			}
			if got := read(t, filepath.Join(r.ws, "README")); got != "hi\n" || exists(filepath.Join(r.ws, "new.txt")) {
				t.Fatalf("README %q after the replay, want the first turn's and no new.txt", got)
			}
			if rs := revertsOf(t, n.h, r.thread); len(rs) != 1 {
				t.Fatalf("reverts %+v, want one", rs)
			}
			assertReplay(t, n.st)
		})
	}
}

// serveFolder is a host of worktrees and checkpoints under dir whose
// "fake" turns wait on an approval and whose "quick" ones don't.
func serveFolder(t *testing.T, dir string) node {
	t.Helper()
	st, err := store.Open(dbPath(t))
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

// A parent in its worktree whose child runs in the same folder, both idle
// once they return: the parent's turn was stopped, and saved.
func parentAndChild(t *testing.T, n node) (parent, kid api.Thread) {
	t.Helper()
	parent = asking(t, n.h, inWorktree(repo(t), ""))
	shared := api.ThreadCreate{Workspace: parent.Workspace, Title: "In the folder", Model: api.ModelRef{Provider: "quick", Model: "fake"}, Parent: parent.ID, Run: parent.Turn.Run, Text: "x"}
	id := api.NewID()
	submit(t, n.h, id, shared)
	kid = until(t, n.h, id, "the child's turn to end", func(th api.Thread) bool { return th.LastTurn != nil && idle(th) })
	stopped(t, n.h, parent)
	untilCheckpoints(t, n.h, parent.ID, 2)
	return parent, kid
}

// A start in a worktree whose files a revert puts back waits for it to
// end, a child's in its parent's folder too.
func TestAStartInAFolderBeingRevertedWaits(t *testing.T) {
	n := serveFolder(t, tempDir(t))
	parent, kid := parentAndChild(t, n)
	var mu sync.Mutex
	var order []string
	held, release := make(chan struct{}), make(chan struct{})
	defer runtime.SetCheckpointHook(func(ctx context.Context, s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
		if s == "recovered" {
			close(held)
			<-release
		}
	})()
	submit(t, n.h, parent.ID, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointBase, ID: parent.Turn.Run}, Files: true})
	<-held
	submit(t, n.h, kid.ID, dispatch("again", api.StartNow))
	until(t, n.h, kid.ID, "the child's start to be refused a lease", func(th api.Thread) bool { return runtime.Waiting(n.h, kid.ID) })
	if th := view(t, n.h, kid.ID); th.Run != api.RunStarting {
		t.Fatalf("the child is %v while its parent's revert works, want it starting", th.Run)
	}
	close(release)
	untilReverted(t, n.h, parent.ID, 1)
	until(t, n.h, kid.ID, "the child's turn after the revert", func(th api.Thread) bool { return idle(th) && th.LastTurn != nil && th.LastTurn.Run != kid.LastTurn.Run })
	assertReplay(t, n.st)
}

// A revert is refused before it commits while a thread under the thread
// works, while another thread runs in its worktree, and when the
// worktree is marked another thread's.
func TestARevertOfAFolderOthersMayWriteIsRefused(t *testing.T) {
	t.Run("a child at work", func(t *testing.T) {
		n := serveFolder(t, tempDir(t))
		parent := asking(t, n.h, inWorktree(repo(t), ""))
		kid := child(t, n.h, api.ThreadCreate{Workspace: parent.Workspace, Title: "Busy", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Parent: parent.ID, Run: parent.Turn.Run, Text: "x"})
		stopped(t, n.h, parent)
		_, err := attempt(n.h, parent.ID, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointBase, ID: parent.Turn.Run}, Files: true})
		refusal(t, err, "Stop every thread under this one")
		stopped(t, n.h, kid)
	})
	t.Run("another thread in the folder", func(t *testing.T) {
		n := serveFolder(t, tempDir(t))
		parent := asking(t, n.h, inWorktree(repo(t), ""))
		stopped(t, n.h, parent)
		write(t, filepath.Join(parent.Workspace, "sub", "notes"), "x\n")
		submit(t, n.h, api.NewID(), api.ThreadCreate{Workspace: filepath.Join(parent.Workspace, "sub"), Title: "Elsewhere", Model: api.ModelRef{Provider: "quick", Model: "fake"}})
		_, err := attempt(n.h, parent.ID, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointBase, ID: parent.Turn.Run}, Files: true})
		refusal(t, err, "the thread \"Elsewhere\" runs in")
	})
	t.Run("a worktree marked another's", func(t *testing.T) {
		n := serveFolder(t, tempDir(t))
		parent := asking(t, n.h, inWorktree(repo(t), ""))
		stopped(t, n.h, parent)
		own := gitIn(t, parent.Workspace, "rev-parse", "--absolute-git-dir")
		write(t, filepath.Join(own, "glitched-thread"), api.NewID().String())
		_, err := attempt(n.h, parent.ID, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointBase, ID: parent.Turn.Run}, Files: true})
		refusal(t, err, "is no longer the thread's worktree")
	})
}

// A thread keeps the recovery checkpoints of its newest 10 reverts, and
// its run checkpoints with them.
func TestAThreadKeepsItsNewestRecoveries(t *testing.T) {
	r := twoTurns(t, dbPath(t), tempDir(t), repo(t))
	var last api.Receipt
	for i := range 12 {
		last = submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
		untilReverted(t, r.n.h, r.thread, i+1)
	}
	var recoveries, others []string
	for _, ref := range refsOf(t, theStore(t, r.dir), r.thread) {
		if strings.Contains(ref, "/recovery/") {
			recoveries = append(recoveries, ref)
		} else {
			others = append(others, ref)
		}
	}
	if len(recoveries) != 10 || !strings.HasSuffix(recoveries[len(recoveries)-1], last.Command.String()) {
		t.Fatalf("recovery refs %q, want the newest 10, the last revert's among them", recoveries)
	}
	if len(others) != 3 {
		t.Fatalf("other refs %q, want the base and both runs'", others)
	}
	assertReplay(t, r.n.st)
}
