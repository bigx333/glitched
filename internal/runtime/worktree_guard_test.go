package runtime_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

func effectState(t *testing.T, w worktreeHost, want string) {
	t.Helper()
	var got string
	if err := w.db.QueryRow("SELECT state FROM effects WHERE kind = 'worktree.remove'").Scan(&got); err != nil || got != want {
		t.Fatalf("worktree.remove state=%q, want %q: %v", got, want, err)
	}
}

func TestACreateInARemovedWorktreeIsRefused(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	owner := worktreeOwner(t, w, project)
	_, wait, release := gitGate(t, `if [ "$3:$4" = worktree:move ]; then`)
	submit(t, w.h, owner.ID, api.ThreadDelete{RemoveWorktree: true})
	wait()
	effectState(t, w, "leased")
	if !exists(owner.Workspace) {
		t.Fatal("gate missed the original checkout")
	}
	id := api.NewID()
	started, finished := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := attempt(w.h, id, api.ThreadCreate{Workspace: owner.Workspace, Title: "Open in this checkout"})
		finished <- err
	}()
	<-started
	select {
	case err := <-finished:
		t.Fatalf("plain create bypassed the removal's reservation: %v", err)
	case <-time.After(150 * time.Millisecond):
		t.Log("plain create did not commit while removal was paused after its final read")
	}
	release()
	select {
	case err := <-finished:
		if err != nil {
			var rejected *api.RejectedError
			if !errors.As(err, &rejected) {
				t.Fatalf("not a clean refusal: %v", err)
			}
		} else {
			w.settle(t)
			effectState(t, w, "done")
			live := view(t, w.h, id)
			if !exists(live.Workspace) {
				t.Fatalf("plain create returned success and stored workspace %q, but removal deleted it before the create committed", live.Workspace)
			}
		}
	case <-time.After(4 * time.Second):
		t.Fatal("plain create never returned after Git finished")
	}
}

func worktreeOwnerBeyond(t *testing.T, w worktreeHost, excluded string) string {
	t.Helper()
	dir := filepath.Join(w.root, "unrelated")
	if filepath.Dir(excluded) == dir {
		t.Fatal("expected an unrelated workspace")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAFolderBesideAWorktreeIsNotRemovedWithIt(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	project := repo(t)
	owner := worktreeOwner(t, w, project)
	aside := owner.Workspace + ".removing"
	if err := os.Mkdir(aside, 0755); err != nil {
		t.Fatal(err)
	}
	live := asking(t, w.h, api.ThreadCreate{Workspace: aside})
	note := filepath.Join(aside, "live-uncommitted.txt")
	writeFile(t, note, "a separate live workspace\n")
	w.remove(t, owner.ID)
	effectState(t, w, "done")
	if !exists(note) {
		t.Fatalf("removed an unregistered folder the owner never used; owner checkout exists=%v, unrelated turn active=%v", exists(owner.Workspace), view(t, w.h, live.ID).Turn != nil)
	}
}

func TestAFolderPutWhereTheWorktreeWasMovedStays(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	owner := worktreeOwner(t, w, project)
	aside := owner.Workspace + ".removing"
	gate, wait, release := gitGate(t, `if [ "$3:$4:$5" = worktree:remove:--force ]; then`)
	wrapper := filepath.Join(gate, "git")
	script, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), "exec /usr/bin/git", `if [ "$3:$4:$5" = worktree:remove:--force ]; then
  /usr/bin/git "$@" > "$GITGATE_GATE/remove-output" 2>&1
  code=$?
  cat "$GITGATE_GATE/remove-output" >&2
  printf '%s\n' "$code" > "$GITGATE_GATE/remove-code"
  exit "$code"
fi
exec /usr/bin/git`, 1))
	if err := os.WriteFile(wrapper, script, 0755); err != nil {
		t.Fatal(err)
	}
	submit(t, w.h, owner.ID, api.ThreadDelete{RemoveWorktree: true})
	wait()
	effectState(t, w, "leased")
	moved := filepath.Join(t.TempDir(), "saved")
	if err := os.Rename(aside, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(aside, 0755); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(aside, "replacement.txt")
	writeFile(t, note, "replacement workspace\n")
	release()
	w.settle(t)
	effectState(t, w, "done")
	output, err := os.ReadFile(filepath.Join(gate, "remove-output"))
	if err != nil {
		t.Fatal(err)
	}
	code, err := os.ReadFile(filepath.Join(gate, "remove-code"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("git worktree remove returned %s: %s", strings.TrimSpace(string(code)), strings.TrimSpace(string(output)))
	if !exists(note) {
		t.Fatalf("removed the replacement folder at %q; original folder kept elsewhere=%v", aside, exists(moved))
	}
}

func TestAWorktreeWithSubmodules(t *testing.T) {
	w := serveWorktrees(t)
	project, sub := repo(t), repo(t)
	gitIn(t, project, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "dependency")
	gitIn(t, project, "commit", "-qm", "add submodule")
	owner := worktreeOwner(t, w, project)
	gitIn(t, owner.Workspace, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--quiet")
	if got, want := canonical(t, gitIn(t, filepath.Join(owner.Workspace, "dependency"), "rev-parse", "--show-toplevel")), canonical(t, filepath.Join(owner.Workspace, "dependency")); got != want {
		t.Fatalf("the submodule isn't checked out: %q, want %q", got, want)
	}
	_, err := attempt(w.h, owner.ID, api.ThreadDelete{RemoveWorktree: true})
	refusal(t, err, "submodule")
	if !exists(filepath.Join(owner.Workspace, "dependency")) {
		t.Fatal("the refused delete took the worktree")
	}
}

func canonical(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func TestABootKeepsALockedWorktreesRegistration(t *testing.T) {
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
	registration := gitIn(t, owner.Workspace, "rev-parse", "--absolute-git-dir")
	h.Kill()
	if _, err := st.Submit(ctx, api.Command{ID: api.NewID(), Target: owner.ID, Body: api.ThreadDelete{RemoveWorktree: true}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := st.Lease(ctx, nil)
	if _, removal := lease.Body.(engine.RemoveWorktree); err != nil || !ok || !removal {
		t.Fatalf("expected leased removal: %+v %v %v", lease, ok, err)
	}
	aside := owner.Workspace + ".removing"
	gitIn(t, project, "worktree", "move", owner.Workspace, aside)
	gitIn(t, project, "worktree", "lock", "--reason", "temporarily offline", aside)
	offline := filepath.Join(t.TempDir(), "offline")
	if err := os.Rename(aside, offline); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(registration, "locked")) {
		t.Fatal("expected a locked worktree entry with a temporarily missing folder")
	}
	h, err = runtime.New(ctx, st, nil, runtime.WithWorktrees(root))
	if err != nil {
		t.Fatal(err)
	}
	w := worktreeHost{h: h, db: sideDB(t, path)}
	w.settle(t)
	effectState(t, w, "done")
	if !exists(registration) {
		t.Fatalf("resume deleted the locked registration for a temporarily missing worktree; checkout is still available elsewhere=%v", exists(offline))
	}
}

func TestAWorktreeLockedAfterItsMoveStays(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	owner := worktreeOwner(t, w, project)
	writeFile(t, filepath.Join(owner.Workspace, "note.txt"), "keep me\n")
	_, wait, release := gitGate(t, `if [ "$3:$4:$5" = worktree:remove:--force ]; then`)
	submit(t, w.h, owner.ID, api.ThreadDelete{RemoveWorktree: true})
	wait()
	aside := owner.Workspace + ".removing"
	gitIn(t, project, "worktree", "lock", "--reason", "hands off", aside)
	release()
	w.settle(t)
	effectState(t, w, "done")
	if !exists(filepath.Join(aside, "note.txt")) {
		t.Fatal("Git lock between move and remove didn't preserve files")
	}
}

func TestAStopBeforeGitRemovesLeavesTheWorktreeForTheNextHost(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	owner := worktreeOwner(t, w, project)
	registration := gitIn(t, owner.Workspace, "rev-parse", "--absolute-git-dir")
	gate, wait, _ := gitGate(t, `if [ "$3:$4:$5" = worktree:remove:--force ]; then`)
	wrapper := filepath.Join(gate, "git")
	script, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), "exec /usr/bin/git", `if [ "$3:$4:$5" = worktree:remove:--force ]; then
  touch "$GITGATE_GATE/ran"
fi
exec /usr/bin/git`, 1))
	if err := os.WriteFile(wrapper, script, 0755); err != nil {
		t.Fatal(err)
	}
	submit(t, w.h, owner.ID, api.ThreadDelete{RemoveWorktree: true})
	wait()
	aside := owner.Workspace + ".removing"
	if !exists(aside) || !exists(registration) {
		t.Fatal("the checkout and Git registration should exist before Git remove")
	}
	w.h.Kill()
	effectState(t, w, "leased")
	if exists(filepath.Join(gate, "ran")) {
		t.Fatal("Git was allowed to run, so this did not simulate interruption before remove")
	}
	if !exists(aside) || !exists(registration) {
		t.Fatalf("cancellation erased the checkout (%v) or its registration (%v) outside Git, while leaving its effect leased", exists(aside), exists(registration))
	}
}

func TestADeleteWhoseProjectIsGoneIsRefused(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	owner := worktreeOwner(t, w, project)
	if err := os.Rename(project, project+"-moved"); err != nil {
		t.Fatal(err)
	}
	_, err := attempt(w.h, owner.ID, api.ThreadDelete{RemoveWorktree: true})
	refusal(t, err, "can't be read")
	view(t, w.h, owner.ID)
	if !exists(owner.Workspace) {
		t.Fatal("checkout did not survive the refused removal")
	}
}

func TestACreateBeforeTheRemovalChecksKeepsTheWorktree(t *testing.T) {
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
	go func() {
		_, err := attempt(h, api.NewID(), api.ThreadCreate{Workspace: owner.Workspace, Title: "Open existing checkout"})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("plain create blocked on an agent closing")
	}
	if !exists(owner.Workspace) {
		t.Fatal("the old checkout was removed before close")
	}
	close(gate.release)
	released = true
	w := worktreeHost{h: h, db: sideDB(t, path)}
	w.settle(t)
	effectState(t, w, "done")
	if !exists(owner.Workspace) {
		t.Fatal("newly committed plain thread did not protect its checkout")
	}
}
