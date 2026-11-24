package runtime_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// serveCheckpoints starts a host on the database at path that keeps its
// worktrees and checkpoints under dir.
func serveCheckpoints(t *testing.T, path, dir string, ad provider.Adapter) node {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad},
		runtime.WithWorktrees(filepath.Join(dir, "worktrees")), runtime.WithCheckpoints(filepath.Join(dir, "checkpoints")))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	n := node{st, h}
	t.Cleanup(n.kill)
	return n
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func checkpointsOf(t *testing.T, ep api.Endpoint, thread api.ID) []api.CheckpointCaptured {
	t.Helper()
	var out []api.CheckpointCaptured
	for _, e := range events(t, ep, thread) {
		if b, _ := e.Decode(); b != nil {
			if c, ok := b.(api.CheckpointCaptured); ok {
				out = append(out, c)
			}
		}
	}
	return out
}

func untilCheckpoints(t *testing.T, ep api.Endpoint, thread api.ID, n int) []api.CheckpointCaptured {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if cps := checkpointsOf(t, ep, thread); len(cps) >= n {
			return cps
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d checkpoints: %+v", n, checkpointsOf(t, ep, thread))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// theStore is the one checkpoint store under dir.
func theStore(t *testing.T, dir string) string {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(dir, "checkpoints"))
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".git") {
		t.Fatalf("checkpoint stores %v, want one", entries)
	}
	return filepath.Join(dir, "checkpoints", entries[0].Name())
}

func refsOf(t *testing.T, store string, thread api.ID) []string {
	t.Helper()
	out := gitIn(t, store, "for-each-ref", "--format=%(refname)", "refs/glitched/"+thread.String()+"/")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// files hashes every file under dir by its path, with its mode.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		var b []byte
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			b = []byte(target)
			if err != nil {
				return err
			}
		} else if b, err = os.ReadFile(p); err != nil {
			return err
		}
		out[p] = fmt.Sprintf("%s %x", fi.Mode(), sha256.Sum256(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A turn's checkpoint holds the worktree's files as the turn left them, as
// the checkout's git lists them with every ignore source of the user's,
// before the next turn starts, and nothing of the checkout's git changes.
func TestATurnsCheckpointHoldsTheWorktreeAsItLeftIt(t *testing.T) {
	dir := tempDir(t)
	write(t, filepath.Join(dir, "excludes"), "global.txt\n")
	write(t, filepath.Join(dir, "gitconfig"), "[core]\n\texcludesFile = "+filepath.Join(dir, "excludes")+"\n")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	project := repo(t)
	write(t, filepath.Join(project, ".gitignore"), "ignored.txt\nbuild/\nforced.txt\n")
	write(t, filepath.Join(project, "forced.txt"), "v1\n")
	write(t, filepath.Join(project, "dir", "a"), "a\n")
	gitIn(t, project, "add", ".gitignore", "dir")
	gitIn(t, project, "add", "-f", "forced.txt")
	gitIn(t, project, "commit", "-q", "-m", "ignore")
	write(t, filepath.Join(project, ".git", "info", "exclude"), "excluded.txt\n")
	monitor, marker := filepath.Join(dir, "fsmonitor"), filepath.Join(dir, "fsmonitor-ran")
	write(t, monitor, "#!/bin/sh\ntouch '"+marker+"'\n")
	os.Chmod(monitor, 0o755)
	gitIn(t, project, "config", "core.fsmonitor", monitor)

	n := serveCheckpoints(t, dbPath(t), dir, fake.New(fake.Turn(true)))
	thread := api.NewID()
	submit(t, n.h, thread, inWorktree(project, ""))
	first := until(t, n.h, thread, "approval", waiting)
	base := untilCheckpoints(t, n.h, thread, 1)[0]
	ws := first.Workspace
	os.Remove(marker)
	for name, text := range map[string]string{"README": "changed\n", "forced.txt": "v2\n", "new.txt": "new\n", "ignored.txt": "x", "excluded.txt": "x", "global.txt": "x", "build/out": "x"} {
		write(t, filepath.Join(ws, name), text)
	}
	if err := os.Symlink("README", filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	git := files(t, filepath.Join(project, ".git"))
	submit(t, n.h, thread, dispatch("next", api.AfterActive))
	respond(t, n.h, thread, first.Requests[0])
	second := until(t, n.h, thread, "the next turn's approval", func(th api.Thread) bool {
		return waiting(th) && th.Requests[0].ID != first.Requests[0].ID
	})
	if err := os.Remove(filepath.Join(ws, "new.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(ws, "README"), "chaNged\n")
	os.RemoveAll(filepath.Join(ws, "dir"))
	write(t, filepath.Join(ws, "dir"), "now a file\n")
	respond(t, n.h, thread, second.Requests[0])
	until(t, n.h, thread, "turn end", idle)
	cps := untilCheckpoints(t, n.h, thread, 3)

	if base.Phase != api.CheckpointBase || base.Status != api.CheckpointReady || base.Files != 4 || base.Run != first.Turn.Run {
		t.Fatalf("base %+v, want ready with README, .gitignore, dir/a and forced.txt, for the first run", base)
	}
	turn, next := cps[1], cps[2]
	if turn.Phase != api.CheckpointRun || turn.Status != api.CheckpointReady || turn.Run != first.Turn.Run || turn.Since != base.Commit || turn.Changed != 4 {
		t.Fatalf("the first turn's checkpoint %+v, want ready, since the base, with README, forced.txt, new.txt and link changed", turn)
	}
	if next.Status != api.CheckpointReady || next.Since != turn.Commit || next.Changed != 4 {
		t.Fatalf("the next turn's checkpoint %+v, want ready, since the first's, with new.txt gone, README of the same size changed, and dir/a a file dir", next)
	}
	store := theStore(t, dir)
	if got, want := gitIn(t, store, "ls-tree", "-r", "--name-only", turn.Commit), ".gitignore\nREADME\ndir/a\nforced.txt\nlink\nnew.txt"; got != want {
		t.Fatalf("the first turn's checkpoint holds\n%s\nwant\n%s", got, want)
	}
	if got, want := gitIn(t, store, "ls-tree", "-r", "--name-only", next.Commit), ".gitignore\nREADME\ndir\nforced.txt\nlink"; got != want {
		t.Fatalf("the next turn's checkpoint holds\n%s\nwant\n%s", got, want)
	}
	if got, again := gitIn(t, store, "show", turn.Commit+":README"), gitIn(t, store, "show", next.Commit+":README"); got != "changed" || again != "chaNged" {
		t.Fatalf("README in the checkpoints: %q then %q", got, again)
	}
	if got := gitIn(t, store, "ls-tree", turn.Commit, "link"); !strings.HasPrefix(got, "120000 ") {
		t.Fatalf("link in the checkpoint: %q, want a symlink", got)
	}
	want := []string{"refs/glitched/" + thread.String() + "/base", "refs/glitched/" + thread.String() + "/run/" + turn.Run.String(), "refs/glitched/" + thread.String() + "/run/" + next.Run.String()}
	if got := refsOf(t, store, thread); !reflect.DeepEqual(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("refs %v, want %v", got, want)
	}
	if after := files(t, filepath.Join(project, ".git")); !reflect.DeepEqual(after, git) {
		t.Fatal("the checkout's git changed in a capture")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a capture ran the checkout's FSMonitor")
	}
	for _, c := range cps {
		if c.Reason != "" {
			t.Fatalf("checkpoint %+v, want no reason: the store was seeded from HEAD", c)
		}
	}
	gitIn(t, store, "cat-file", "-e", gitIn(t, project, "rev-parse", "HEAD:.gitignore"))

	var capturedAt, startedAt []uint64
	for _, e := range events(t, n.h, thread) {
		switch e.Type {
		case "checkpoint.captured":
			capturedAt = append(capturedAt, e.Cursor.Seq)
		case "turn.started":
			startedAt = append(startedAt, e.Cursor.Seq)
		}
	}
	if len(startedAt) != 2 || capturedAt[0] > startedAt[0] || capturedAt[1] > startedAt[1] {
		t.Fatalf("checkpoints at %v, turns started at %v: want the base before the first turn, and the first turn's before the next", capturedAt, startedAt)
	}
	assertReplay(t, n.st)
}

// A capture that a budget or the worktree's shape refuses is skipped,
// says why in its event, and writes no ref.
func TestACheckpointOverABudgetIsSkipped(t *testing.T) {
	for name, tc := range map[string]struct {
		budgets func() func()
		mutate  func(t *testing.T, project, ws string)
		phase   api.CheckpointPhase
		reason  string
	}{
		"a base slower than its budget": {
			budgets: func() func() { return runtime.SetCheckpointBudgets(time.Nanosecond, 50_000, 256<<20, 2<<30, 50) },
			phase:   api.CheckpointBase, reason: "it took longer than 1ns",
		},
		"more files than the budget": {
			budgets: func() func() { return runtime.SetCheckpointBudgets(5*time.Second, 2, 256<<20, 2<<30, 50) },
			mutate: func(t *testing.T, _, ws string) {
				write(t, filepath.Join(ws, "a"), "a")
				write(t, filepath.Join(ws, "b"), "b")
			},
			phase: api.CheckpointRun, reason: "it holds more than 2 files",
		},
		"more bytes than the budget": {
			budgets: func() func() { return runtime.SetCheckpointBudgets(5*time.Second, 50_000, 1<<20, 2<<30, 50) },
			mutate: func(t *testing.T, _, ws string) {
				write(t, filepath.Join(ws, "big"), strings.Repeat("x", 1<<20))
			},
			phase: api.CheckpointRun, reason: "it holds more than 1 MiB",
		},
		"a store over its limit": {
			budgets: func() func() { return runtime.SetCheckpointBudgets(5*time.Second, 50_000, 256<<20, 1, 50) },
			phase:   api.CheckpointRun, reason: "Checkpoints are full (1 B); delete old threads to free space.",
		},
		"a submodule": {
			mutate: func(t *testing.T, _, ws string) {
				sub := repo(t)
				gitIn(t, ws, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
			},
			phase: api.CheckpointRun, reason: "it has a submodule at sub",
		},
		"a symlinked folder": {
			mutate: func(t *testing.T, _, ws string) {
				write(t, filepath.Join(ws, "dir", "a"), "a")
				gitIn(t, ws, "add", "dir")
				os.RemoveAll(filepath.Join(ws, "dir"))
				other := tempDir(t)
				write(t, filepath.Join(other, "a"), "a")
				if err := os.Symlink(other, filepath.Join(ws, "dir")); err != nil {
					t.Fatal(err)
				}
			},
			phase: api.CheckpointRun, reason: "dir is a symlink",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.budgets != nil {
				defer tc.budgets()()
			}
			dir := tempDir(t)
			project := repo(t)
			n := serveCheckpoints(t, dbPath(t), dir, fake.New(fake.Turn(true)))
			thread := api.NewID()
			submit(t, n.h, thread, inWorktree(project, ""))
			th := until(t, n.h, thread, "approval", waiting)
			untilCheckpoints(t, n.h, thread, 1)
			if tc.mutate != nil {
				tc.mutate(t, project, th.Workspace)
			}
			respond(t, n.h, thread, th.Requests[0])
			until(t, n.h, thread, "turn end", idle)
			var got *api.CheckpointCaptured
			for _, c := range untilCheckpoints(t, n.h, thread, 2) {
				if c.Phase == tc.phase {
					got = &c
				}
			}
			if got == nil || got.Status != api.CheckpointSkipped || !strings.Contains(got.Reason, tc.reason) || got.Commit != "" {
				t.Fatalf("the %s checkpoint: %+v, want skipped as %q", tc.phase, got, tc.reason)
			}
			stores, _ := filepath.Glob(filepath.Join(dir, "checkpoints", "*.git"))
			for _, store := range stores {
				for _, ref := range refsOf(t, store, thread) {
					if strings.HasSuffix(ref, "/"+string(tc.phase)) || (tc.phase == api.CheckpointRun && strings.Contains(ref, "/run/")) {
						t.Fatalf("a skipped capture wrote %s", ref)
					}
				}
			}
			assertReplay(t, n.st)
		})
	}

	t.Run("a workspace in no repository", func(t *testing.T) {
		n := serveCheckpoints(t, dbPath(t), tempDir(t), fake.New(fake.Turn(false)))
		thread := api.NewID()
		submit(t, n.h, thread, api.ThreadCreate{Workspace: tempDir(t), Project: tempDir(t), Branch: "glitched/t", Title: "t",
			Model: api.ModelRef{Provider: "fake", Model: "fake"}, Text: "go"})
		until(t, n.h, thread, "turn end", idle)
		for _, c := range untilCheckpoints(t, n.h, thread, 2) {
			if c.Status != api.CheckpointSkipped || !strings.Contains(c.Reason, "isn't in a git repository") {
				t.Fatalf("checkpoint %+v, want skipped: no repository", c)
			}
		}
		assertReplay(t, n.st)
	})
}

// An empty store is seeded with a copy of HEAD's blobs, read from the
// checkout without a write to its git; a seed that runs out of time
// leaves the capture to hash every file, and the checkpoint says so.
// Either way the store holds every object of its checkpoints itself.
func TestAnEmptyStoreIsSeededFromHEAD(t *testing.T) {
	for name, tc := range map[string]struct {
		budget time.Duration
		reason string
		packs  bool
	}{
		"seeded":           {budget: time.Minute, packs: true},
		"seed out of time": {budget: time.Nanosecond, reason: "seeding the store from HEAD took longer than 1ns, so every file was hashed"},
	} {
		t.Run(name, func(t *testing.T) {
			defer runtime.SetSeedBudget(tc.budget)()
			dir, project := tempDir(t), repo(t)
			gitIn(t, project, "repack", "-adq")
			ws := filepath.Join(tempDir(t), "ws")
			gitIn(t, project, "worktree", "add", "-q", "-b", "glitched/t", ws)
			git := files(t, filepath.Join(project, ".git"))
			n := serveCheckpoints(t, dbPath(t), dir, fake.New(fake.Turn(false)))
			thread := api.NewID()
			submit(t, n.h, thread, api.ThreadCreate{Workspace: ws, Project: project, Branch: "glitched/t", Title: "t",
				Model: api.ModelRef{Provider: "fake", Model: "fake"}, Text: "go"})
			until(t, n.h, thread, "turn end", idle)
			base := untilCheckpoints(t, n.h, thread, 2)[0]
			if base.Status != api.CheckpointReady || base.Reason != tc.reason {
				t.Fatalf("base %+v, want ready with reason %q", base, tc.reason)
			}
			if !reflect.DeepEqual(files(t, filepath.Join(project, ".git")), git) {
				t.Fatal("seeding or capturing changed the checkout's git")
			}
			store := theStore(t, dir)
			packs, _ := filepath.Glob(filepath.Join(store, "objects", "pack", "*.pack"))
			if _, err := os.Stat(filepath.Join(store, "objects", "info", "alternates")); err == nil || (len(packs) > 0) != tc.packs {
				t.Fatalf("store packs %v, want packed %v and no alternates", packs, tc.packs)
			}
			gitIn(t, store, "fsck", "--connectivity-only", "--no-dangling")
			assertReplay(t, n.st)
		})
	}
}

// A thread keeps its newest run checkpoints and its base; a capture
// deletes the oldest past them.
func TestAThreadKeepsItsNewestCheckpoints(t *testing.T) {
	defer runtime.SetCheckpointBudgets(5*time.Second, 50_000, 256<<20, 2<<30, 2)()
	dir := tempDir(t)
	n := serveCheckpoints(t, dbPath(t), dir, fake.New(fake.Turn(false)))
	thread := api.NewID()
	submit(t, n.h, thread, inWorktree(repo(t), ""))
	until(t, n.h, thread, "turn end", idle)
	for i := range 2 {
		untilCheckpoints(t, n.h, thread, 2+i)
		submit(t, n.h, thread, dispatch("more", api.StartNow))
		until(t, n.h, thread, "turn end", idle)
	}
	cps := untilCheckpoints(t, n.h, thread, 4)
	prefix := "refs/glitched/" + thread.String() + "/"
	want := []string{prefix + "base", prefix + "run/" + cps[2].Run.String(), prefix + "run/" + cps[3].Run.String()}
	if got := refsOf(t, theStore(t, dir), thread); !reflect.DeepEqual(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("refs %v, want the base and the two newest runs' %v", got, want)
	}
}

// Deleting a thread deletes its checkpoints and no other thread's in the
// same store, and the engine reports what the store holds.
func TestDeletingAThreadDropsItsCheckpoints(t *testing.T) {
	dir := filepath.Join(tempDir(t), "state[1]")
	project := repo(t)
	n := serveCheckpoints(t, dbPath(t), dir, fake.New(fake.Turn(false)))
	gone, kept := api.NewID(), api.NewID()
	for _, thread := range []api.ID{gone, kept} {
		submit(t, n.h, thread, inWorktree(project, ""))
		until(t, n.h, thread, "turn end", idle)
		untilCheckpoints(t, n.h, thread, 2)
	}
	store := theStore(t, dir)
	env, err := n.h.Info(ctx)
	if err != nil || env.Checkpoints.Bytes <= 0 || env.Checkpoints.Limit != 2<<30 {
		t.Fatalf("info %+v, %v; want the store's size and its limit", env.Checkpoints, err)
	}
	submit(t, n.h, gone, api.ThreadDelete{})
	deadline := time.Now().Add(10 * time.Second)
	for len(refsOf(t, store, gone)) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the deleted thread's checkpoints stay: %v", refsOf(t, store, gone))
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := refsOf(t, store, kept); len(got) != 2 {
		t.Fatalf("the other thread's checkpoints: %v, want its base and its run", got)
	}
	assertReplay(t, n.st)
}

// Deleting a tree with its worktrees removes them and drops the root's
// checkpoints, in the root's own lane after its worktree goes; a worktree
// child keeps no checkpoints, and its removal, in its own lane, shares no
// lock or folder with the drop.
func TestDeletingATreeWithItsWorktreesDropsOnlyTheRootsCheckpoints(t *testing.T) {
	dir, path := tempDir(t), dbPath(t)
	n := serveCheckpoints(t, path, dir, fake.New(fake.Turn(true)))
	project := repo(t)
	parent := asking(t, n.h, inWorktree(project, ""))
	kid := child(t, n.h, isolated(parent))
	stopped(t, n.h, kid, parent)
	untilCheckpoints(t, n.h, parent.ID, 2)
	if cps := checkpointsOf(t, n.h, kid.ID); len(cps) != 0 {
		t.Fatalf("the worktree child's checkpoints: %+v, want none", cps)
	}
	store := theStore(t, dir)
	if got := gitIn(t, store, "for-each-ref", "--format=%(refname)"); strings.Contains(got, kid.ID.String()) {
		t.Fatalf("the store holds refs of the child: %s", got)
	}
	submit(t, n.h, parent.ID, api.ThreadDelete{RemoveWorktree: true})
	db := sideDB(t, path)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		var pending int
		if err := db.QueryRow("SELECT COUNT(*) FROM effects WHERE kind IN ('agent.close', 'worktree.remove', 'checkpoint.drop') AND state <> 'done'").Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of the tree's delete effects still pending", pending)
		}
	}
	if refs := refsOf(t, store, parent.ID); len(refs) > 0 || exists(parent.Workspace) || exists(kid.Workspace) {
		t.Fatalf("after the tree's delete: refs %v, the parent's worktree there %v, the child's %v", refs, exists(parent.Workspace), exists(kid.Workspace))
	}
	if got := strings.Count(gitIn(t, project, "worktree", "list", "--porcelain"), "worktree "); got != 1 {
		t.Fatalf("git lists %d worktrees after the tree's delete, want the project's own", got)
	}
	gitIn(t, store, "fsck", "--full", "--no-dangling")
	assertReplay(t, n.st)
}

// A host that dies with a checkpoint effect leased leaves it to the next,
// which converges: a capture whose ref was written reports it once and
// writes nothing, and a drop runs again.
func TestACrashMidCheckpointConverges(t *testing.T) {
	type hooked struct {
		mu    sync.Mutex
		steps map[string]int
		block string // the step to hold until the host dies, at its nth call
		nth   int
	}
	hook := func(h *hooked, entered chan<- struct{}) func(context.Context, string) {
		return func(ctx context.Context, step string) {
			h.mu.Lock()
			h.steps[step]++
			hold := step == h.block && h.steps[step] == h.nth
			h.mu.Unlock()
			if hold {
				entered <- struct{}{}
				<-ctx.Done()
			}
		}
	}
	count := func(h *hooked, step string) int {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.steps[step]
	}

	t.Run("a capture whose ref was written", func(t *testing.T) {
		path, dir := dbPath(t), tempDir(t)
		entered := make(chan struct{}, 1)
		h := &hooked{steps: map[string]int{}, block: "captured", nth: 2}
		defer runtime.SetCheckpointHook(hook(h, entered))()
		n := serveCheckpoints(t, path, dir, fake.New(fake.Turn(false)))
		thread := api.NewID()
		submit(t, n.h, thread, inWorktree(repo(t), ""))
		<-entered
		ws := view(t, n.h, thread).Workspace
		n.kill()
		if err := os.RemoveAll(ws); err != nil {
			t.Fatal(err)
		}
		store := theStore(t, dir)
		left := files(t, store)
		ref := refsOf(t, store, thread)

		h.mu.Lock()
		h.steps, h.block = map[string]int{}, ""
		h.mu.Unlock()
		n = serveCheckpoints(t, path, dir, fake.New(fake.Turn(false)))
		cps := untilCheckpoints(t, n.h, thread, 2)
		if len(cps) != 2 || len(ref) != 2 {
			t.Fatalf("checkpoints %+v with refs %v, want the base's and the run's once each", cps, ref)
		}
		run := cps[1]
		var body api.CheckpointCaptured
		json.Unmarshal([]byte(gitIn(t, store, "show", "-s", "--format=%b", run.Commit)), &body)
		body.Commit = run.Commit
		if commit := gitIn(t, store, "rev-parse", "refs/glitched/"+thread.String()+"/run/"+run.Run.String()); commit != run.Commit || !reflect.DeepEqual(body, run) {
			t.Fatalf("replayed %+v, want what the ref %s holds: %+v", run, commit, body)
		}
		if count(h, "captured") != 0 || !reflect.DeepEqual(files(t, store), left) {
			t.Fatalf("the replay captured %d times, or changed the store; want it to write nothing", count(h, "captured"))
		}
		assertReplay(t, n.st)
	})

	t.Run("a capture whose budget ran out once its ref was written", func(t *testing.T) {
		defer runtime.SetCheckpointBudgets(3*time.Second, 50_000, 256<<20, 2<<30, 50)()
		var once sync.Once
		defer runtime.SetCheckpointHook(func(ctx context.Context, step string) {
			once.Do(func() { <-ctx.Done() })
		})()
		dir := tempDir(t)
		n := serveCheckpoints(t, dbPath(t), dir, fake.New(fake.Turn(false)))
		thread := api.NewID()
		submit(t, n.h, thread, inWorktree(repo(t), ""))
		until(t, n.h, thread, "turn end", idle)
		base := untilCheckpoints(t, n.h, thread, 1)[0]
		if base.Status != api.CheckpointReady {
			t.Fatalf("base %+v, want it ready", base)
		}
		if commit := gitIn(t, theStore(t, dir), "rev-parse", "refs/glitched/"+thread.String()+"/base"); base.Commit != commit {
			t.Fatalf("base %+v with the ref at %s, want it ready, as a replay would report it", base, commit)
		}
		assertReplay(t, n.st)
	})

	t.Run("a drop after a killed git left its locks", func(t *testing.T) {
		path, dir := dbPath(t), tempDir(t)
		n := serveCheckpoints(t, path, dir, fake.New(fake.Turn(false)))
		thread := api.NewID()
		submit(t, n.h, thread, inWorktree(repo(t), ""))
		until(t, n.h, thread, "turn end", idle)
		untilCheckpoints(t, n.h, thread, 2)
		n.kill()
		store := theStore(t, dir)
		write(t, filepath.Join(store, "packed-refs.lock"), "")
		write(t, filepath.Join(store, "refs", "glitched", thread.String(), "base.lock"), "")
		n = serveCheckpoints(t, path, dir, fake.New(fake.Turn(false)))
		submit(t, n.h, thread, api.ThreadDelete{})
		deadline := time.Now().Add(10 * time.Second)
		for len(refsOf(t, store, thread)) > 0 {
			if time.Now().After(deadline) {
				t.Fatalf("the deleted thread's checkpoints stay: %v", refsOf(t, store, thread))
			}
			time.Sleep(2 * time.Millisecond)
		}
		assertReplay(t, n.st)
	})

	t.Run("a drop", func(t *testing.T) {
		path, dir := dbPath(t), tempDir(t)
		entered := make(chan struct{}, 1)
		h := &hooked{steps: map[string]int{}, block: "dropped", nth: 1}
		defer runtime.SetCheckpointHook(hook(h, entered))()
		n := serveCheckpoints(t, path, dir, fake.New(fake.Turn(false)))
		thread := api.NewID()
		submit(t, n.h, thread, inWorktree(repo(t), ""))
		until(t, n.h, thread, "turn end", idle)
		untilCheckpoints(t, n.h, thread, 2)
		submit(t, n.h, thread, api.ThreadDelete{})
		<-entered
		n.kill()

		h.mu.Lock()
		h.steps, h.block = map[string]int{}, ""
		h.mu.Unlock()
		n = serveCheckpoints(t, path, dir, fake.New(fake.Turn(false)))
		deadline := time.Now().Add(10 * time.Second)
		for {
			leased, err := n.st.Leased(ctx)
			if err == nil && len(leased) == 0 && count(h, "dropped") == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("after the restart the drop ran %d times with %v leased, want once and none", count(h, "dropped"), leased)
			}
			time.Sleep(2 * time.Millisecond)
		}
		if got := refsOf(t, theStore(t, dir), thread); len(got) != 0 {
			t.Fatalf("the deleted thread's checkpoints stay: %v", got)
		}
		assertReplay(t, n.st)
	})
}
