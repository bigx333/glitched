package runtime_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

func worktreeOwner(t *testing.T, w worktreeHost, project string) api.Thread {
	t.Helper()
	id := api.NewID()
	create := inWorktree(project, "")
	create.Text = ""
	submit(t, w.h, id, create)
	return view(t, w.h, id)
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitGate(t *testing.T, predicate string) (gate string, wait, release func()) {
	t.Helper()
	gate = t.TempDir()
	script := "#!/bin/sh\n" + predicate + `
  touch "$GITGATE_GATE/entered"
  i=0
  while [ ! -e "$GITGATE_GATE/release" ]; do
    i=$((i+1))
    [ "$i" -lt 300 ] || exit 42
    sleep .025
  done
fi
exec /usr/bin/git "$@"
`
	if err := os.WriteFile(filepath.Join(gate, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITGATE_GATE", gate)
	t.Setenv("PATH", gate+string(os.PathListSeparator)+os.Getenv("PATH"))
	wait = func() {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !exists(filepath.Join(gate, "entered")); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("Git gate wasn't reached")
			}
		}
	}
	release = func() { writeFile(t, filepath.Join(gate, "release"), "") }
	t.Cleanup(release)
	return gate, wait, release
}

func gitGateAtRemoval(t *testing.T, project string) (wait, release func()) {
	t.Helper()
	t.Setenv("GITGATE_PROJECT", project)
	_, wait, release = gitGate(t, `if [ "$2" = "$GITGATE_PROJECT" ] && [ "$3:$4:$5" = "rev-parse:--path-format=absolute:--git-common-dir" ] && ! mkdir "$GITGATE_GATE/preflight" 2>/dev/null; then`)
	return wait, release
}

func TestATreeRemovalThatWaitsKeepsAWorktreeItsChildKept(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	project := repo(t)
	p := asking(t, w.h, api.ThreadCreate{Workspace: project, Worktree: &api.NewWorktree{}})
	kid := child(t, w.h, isolated(p))
	stopped(t, w.h, kid, p)
	_, wait, release := gitGate(t, `if [ "$3:$4:$5" = "rev-parse:--path-format=absolute:--git-common-dir" ] && mkdir "$GITGATE_GATE/once" 2>/dev/null; then`)
	done := make(chan error, 1)
	go func() {
		_, err := attempt(w.h, p.ID, api.ThreadDelete{RemoveWorktree: true})
		done <- err
	}()
	wait()
	submit(t, w.h, kid.ID, api.ThreadDelete{})
	note := filepath.Join(kid.Workspace, "kept.txt")
	writeFile(t, note, "work explicitly kept\n")
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete did not commit")
	}
	w.settle(t)
	if exists(p.Workspace) || !exists(note) || gitIn(t, project, "branch", "--list", kid.Branch) == "" {
		t.Fatalf("parent checkout present=%v; child note present=%v; child ref %q", exists(p.Workspace), exists(note), gitIn(t, project, "branch", "--list", kid.Branch))
	}
}

func TestAThreadInAnotherThreadsWorktreeRefusesItsRemoval(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	project := repo(t)
	owner := worktreeOwner(t, w, project)
	live := asking(t, w.h, api.ThreadCreate{Workspace: owner.Workspace})
	note := filepath.Join(live.Workspace, "same-path-uncommitted.txt")
	writeFile(t, note, "keep the live thread's work\n")
	_, err := attempt(w.h, owner.ID, api.ThreadDelete{RemoveWorktree: true})
	refusal(t, err, "runs in")
	if _, err := w.h.Read(ctx, api.Query{Thread: owner.ID}); err != nil {
		t.Fatalf("refused delete still tombstoned the owner: %v", err)
	}
	if !exists(note) || gitIn(t, project, "branch", "--list", owner.Branch) == "" {
		t.Fatal("refused delete still took the worktree or branch")
	}
}

func TestAWorktreeLockedAfterTheDeleteStays(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	owner := worktreeOwner(t, w, project)
	note := filepath.Join(owner.Workspace, "uncommitted.txt")
	writeFile(t, note, "keep the locked worktree\n")
	wait, release := gitGateAtRemoval(t, project)
	submit(t, w.h, owner.ID, api.ThreadDelete{RemoveWorktree: true})
	wait()
	gitIn(t, project, "worktree", "lock", "--reason", "hands off", owner.Workspace)
	release()
	w.settle(t)
	if !exists(note) {
		t.Fatalf("delete erased the locked checkout and note; Git still lists:\n%s", gitIn(t, project, "worktree", "list", "--porcelain"))
	}
}

// crashAside leaves a removal leased with the worktree moved aside, as a
// host that stopped in git's move does, mutates what is aside, and boots
// the next host, which keeps what it can't tell git finished moving as
// the thread's worktree.
func crashAside(t *testing.T, mutate func(project string, owner api.Thread, aside string)) {
	t.Helper()
	path, root := dbPath(t), filepath.Join(t.TempDir(), "worktrees")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h, err := runtime.New(ctx, st, nil, runtime.WithWorktrees(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill() })
	project := repo(t)
	owner := worktreeOwner(t, worktreeHost{h: h}, project)
	h.Kill()
	if _, err := st.Submit(ctx, api.Command{ID: api.NewID(), Target: owner.ID, Body: api.ThreadDelete{RemoveWorktree: true}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := st.Lease(ctx, nil)
	if err != nil || !ok {
		t.Fatalf("lease: %v %v", ok, err)
	}
	if _, ok := lease.Body.(engine.RemoveWorktree); !ok {
		t.Fatalf("unexpected lease: %+v", lease)
	}
	aside := owner.Workspace + ".removing"
	if err := os.Rename(owner.Workspace, aside); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(project, owner, aside)
	}
	h, err = runtime.New(ctx, st, nil, runtime.WithWorktrees(root))
	if err != nil {
		t.Fatal(err)
	}
	w := worktreeHost{h: h, db: sideDB(t, path)}
	w.settle(t)
	if !exists(aside) {
		t.Fatal("the next host erased what it couldn't tell was the thread's worktree")
	}
}

func TestARestartKeepsAWorktreeGitDidNotFinishMoving(t *testing.T) {
	crashAside(t, nil)
}

func TestARestartKeepsAPartlyRemovedWorktree(t *testing.T) {
	crashAside(t, func(_ string, _ api.Thread, aside string) {
		if err := os.Remove(filepath.Join(aside, ".git")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestARestartKeepsAWorktreeGitNoLongerRegisters(t *testing.T) {
	crashAside(t, func(project string, _ api.Thread, _ string) {
		gitIn(t, project, "worktree", "prune", "--expire", "now")
	})
}

func TestARemovalLeavesOtherWorktreesRegistered(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	owner := worktreeOwner(t, w, project)
	other := filepath.Join(t.TempDir(), "other-worktree")
	unavailable := filepath.Join(t.TempDir(), "disk-offline")
	gitIn(t, project, "worktree", "add", "-q", other, "other")
	registration := gitIn(t, other, "rev-parse", "--absolute-git-dir")
	if err := os.Rename(other, unavailable); err != nil {
		t.Fatal(err)
	}
	w.remove(t, owner.ID)
	if !exists(registration) {
		t.Fatal("removal pruned an unrelated worktree's Git metadata while its folder was unavailable")
	}
}

func TestAChildsBranchGoesWhenItsProjectIsALinkedCheckout(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	project := repo(t)
	linked := filepath.Join(t.TempDir(), "linked-project")
	gitIn(t, project, "worktree", "add", "-q", linked, "other")
	p := asking(t, w.h, api.ThreadCreate{Workspace: linked})
	kid := child(t, w.h, isolated(p))
	stopped(t, w.h, kid)
	if a, b := gitIn(t, linked, "rev-parse", "HEAD"), gitIn(t, kid.Workspace, "rev-parse", "HEAD"); a != b {
		t.Fatalf("child not at parent HEAD: %s != %s", a, b)
	}
	w.remove(t, kid.ID)
	if exists(kid.Workspace) {
		t.Fatal("child checkout wasn't removed")
	}
	if gitIn(t, project, "branch", "--list", kid.Branch) != "" {
		t.Fatal("unchanged child branch stayed because cleanup compared with the main checkout's HEAD instead of the linked project HEAD")
	}
}

func TestAWaitingRemovalDoesNotHoldBackAnotherCreate(t *testing.T) {
	path, root := dbPath(t), filepath.Join(t.TempDir(), "worktrees")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	gate := heldReconcile{Adapter: fake.New(fake.Turn(true)), entered: make(chan struct{}, 1), release: make(chan struct{})}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": gate.Adapter}, runtime.WithWorktrees(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill() })
	owner := asking(t, h, api.ThreadCreate{Workspace: repo(t), Worktree: &api.NewWorktree{}})
	create := isolated(owner)
	create.Worktree, create.Project, create.Branch = nil, owner.Project, owner.Branch
	kid := child(t, h, create)
	stopped(t, h, kid, owner)
	h.Kill()
	if _, err := st.Submit(ctx, api.Command{ID: api.NewID(), Target: owner.ID, Body: api.ThreadDelete{RemoveWorktree: true}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	gate.target = kid.ID
	h, err = runtime.New(ctx, st, provider.Mux{"fake": gate}, runtime.WithWorktrees(root))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-gate.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("reconcile did not gate shared close")
	}
	released := false
	defer func() {
		if !released {
			close(gate.release)
		}
	}()
	done := make(chan error, 1)
	project := repo(t)
	go func() {
		create := inWorktree(project, "")
		create.Text = ""
		_, err := attempt(h, api.NewID(), create)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unrelated worktree create blocked on an agent closing")
	}
	if !exists(owner.Workspace) {
		t.Fatal("the old checkout was removed before close")
	}
	close(gate.release)
	released = true
	w := worktreeHost{h: h, db: sideDB(t, path)}
	w.settle(t)
	if exists(owner.Workspace) {
		t.Fatal("old checkout stayed after shared agent.close finished")
	}
}
