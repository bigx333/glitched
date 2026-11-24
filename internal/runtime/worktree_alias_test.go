package runtime_test

import (
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
)

func TestACopyBesideAWorktreeLeavesTheOriginalAlone(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	owner := worktreeOwner(t, w, project)
	dir, aside := owner.Workspace, owner.Workspace+".removing"
	registration := gitIn(t, dir, "rev-parse", "--absolute-git-dir")
	if out, err := osexec.Command("/bin/cp", "-R", dir, aside).CombinedOutput(); err != nil {
		t.Fatalf("copy checkout: %v: %s", err, out)
	}
	gitIn(t, dir, "status", "--porcelain")
	note := filepath.Join(aside, "backup-only.txt")
	writeFile(t, note, "uncommitted work in the backup copy\n")
	w.remove(t, owner.ID)
	effectState(t, w, "done")

	out, gitErr := osexec.Command("/usr/bin/git", "-C", dir, "status", "--porcelain").CombinedOutput()
	registered := exists(registration)
	branch := gitIn(t, project, "branch", "--list", owner.Branch)
	t.Logf("original exists=%t; backup note exists=%t; registration exists=%t; branch listing=%q; original git status: %v: %s", exists(dir), exists(note), registered, branch, gitErr, out)
	if exists(dir) && (!registered || branch == "" || gitErr != nil) {
		t.Error("cleanup orphaned an existing original checkout after mistaking the copy for a Git move")
	}
	if !exists(note) {
		t.Error("cleanup deleted uncommitted files from a backup copy that Git never moved")
	}
}

func TestAWorktreeThatHadSubmodulesRefusesTheDelete(t *testing.T) {
	for _, tc := range []string{"inactive", "deinitialized"} {
		t.Run(tc, func(t *testing.T) {
			w := serveWorktrees(t)
			project, sub := repo(t), repo(t)
			gitIn(t, project, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "dependency")
			gitIn(t, project, "commit", "-qm", "add submodule")
			owner := worktreeOwner(t, w, project)
			dir := owner.Workspace
			gitIn(t, dir, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--quiet")
			switch tc {
			case "inactive":
				gitIn(t, dir, "config", "submodule.dependency.active", "false")
				if !exists(filepath.Join(dir, "dependency", ".git")) {
					t.Fatal("fixture must have a populated but inactive submodule")
				}
			case "deinitialized":
				gitIn(t, dir, "submodule", "deinit", "--all")
				if exists(filepath.Join(dir, "dependency", ".git")) {
					t.Fatal("fixture must have a deinitialized submodule")
				}
			}
			if status := gitIn(t, dir, "submodule", "status"); !strings.HasPrefix(status, "-") {
				t.Fatalf("expected status to show a dash: %q", status)
			}
			registration := gitIn(t, dir, "rev-parse", "--absolute-git-dir")
			if !exists(filepath.Join(registration, "modules", "dependency")) {
				t.Fatal("fixture must retain the linked worktree's submodule repository")
			}
			if out, err := osexec.Command("/usr/bin/git", "-C", project, "worktree", "move", dir, dir+".probe").CombinedOutput(); err == nil || !strings.Contains(string(out), "working trees containing submodules cannot be moved or removed") {
				t.Fatalf("Git did not refuse the fixture move: %v: %s", err, out)
			}
			_, err := attempt(w.h, owner.ID, api.ThreadDelete{RemoveWorktree: true})
			if err == nil {
				w.settle(t)
				effectState(t, w, "done")
				snap, rerr := w.h.Read(ctx, api.Query{Thread: owner.ID})
				if !errors.Is(rerr, api.ErrNotFound) || len(snap.Threads) != 0 || !exists(dir) {
					t.Fatalf("unexpected cleanup outcome: %v, visible threads=%d, checkout exists=%t", rerr, len(snap.Threads), exists(dir))
				}
				t.Fatal("delete tombstoned the thread and marked removal done even though Git left the checkout in place")
			}
			refusal(t, err, "submodule")
		})
	}
}

func TestACreateThroughALinkToARemovedWorktreeIsRefused(t *testing.T) {
	w := serveWorktrees(t)
	owner := worktreeOwner(t, w, repo(t))
	alias := filepath.Join(t.TempDir(), "worktree-link")
	if err := os.Symlink(owner.Workspace, alias); err != nil {
		t.Fatal(err)
	}
	if !exists(alias) {
		t.Fatal("alias must resolve while the worktree exists")
	}
	_, wait, release := gitGate(t, `if [ "$3:$4:$5" = worktree:remove:--force ]; then`)
	submit(t, w.h, owner.ID, api.ThreadDelete{RemoveWorktree: true})
	wait()
	effectState(t, w, "leased")
	if !exists(owner.Workspace+".removing") || exists(alias) {
		t.Fatal("gate must hold the removal after the real move broke the alias")
	}
	if target, err := os.Readlink(alias); err != nil || target != owner.Workspace {
		t.Fatalf("fixture lost the symlink to the managed path: %q: %v", target, err)
	}
	id := api.NewID()
	done := make(chan error, 1)
	go func() {
		_, err := attempt(w.h, id, api.ThreadCreate{Workspace: alias, Title: "Open by symlink"})
		done <- err
	}()
	var err error
	finishedDuringCleanup := false
	select {
	case err = <-done:
		finishedDuringCleanup = true
	case <-time.After(300 * time.Millisecond):
	}
	release()
	w.settle(t)
	if !finishedDuringCleanup {
		select {
		case err = <-done:
		case <-time.After(4 * time.Second):
			t.Fatal("plain create never returned after cleanup")
		}
	}
	var rejected *api.RejectedError
	if err == nil {
		live := view(t, w.h, id)
		t.Fatalf("plain create stored nonexistent workspace %q, returned before cleanup unlocked=%t", live.Workspace, finishedDuringCleanup)
	}
	if !errors.As(err, &rejected) {
		t.Fatalf("want a clean refusal, got %v", err)
	}
}

func TestARecoveryLeavesAnotherRepositorysCheckoutAlone(t *testing.T) {
	var replacement, wantGitDir, wantBranch string
	crashAside(t, func(project string, _ api.Thread, aside string) {
		parent := filepath.Dir(aside)
		replacement = filepath.Join(parent, "unrelated-checkout")
		gitIn(t, project, "worktree", "add", "-qb", "old-project-branch", replacement)
		if err := os.Rename(replacement, replacement+"-saving"); err != nil {
			t.Fatal(err)
		}
		second := repo(t)
		wantBranch = "second-project-branch"
		gitIn(t, second, "worktree", "add", "-qb", wantBranch, replacement)
		wantGitDir = gitIn(t, replacement, "rev-parse", "--absolute-git-dir")
	})
	gotGitDir := gitIn(t, replacement, "rev-parse", "--absolute-git-dir")
	gotBranch := gitIn(t, replacement, "branch", "--show-current")
	if gotGitDir != wantGitDir || gotBranch != wantBranch {
		t.Fatalf("replay rewired an unrelated checkout to %q on %q, was %q on %q", gotGitDir, gotBranch, wantGitDir, wantBranch)
	}
}

func TestAThreadBesideAWorktreeKeepsIt(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	owner := worktreeOwner(t, w, project)
	aside := owner.Workspace + ".removing"
	gitIn(t, project, "worktree", "move", owner.Workspace, aside)
	id := api.NewID()
	submit(t, w.h, id, api.ThreadCreate{Workspace: aside, Title: "Open moved checkout"})
	note := filepath.Join(aside, "keep-live.txt")
	writeFile(t, note, "another live thread uses this worktree\n")
	w.remove(t, owner.ID)
	effectState(t, w, "done")
	if !exists(note) || view(t, w.h, id).Workspace != aside {
		t.Fatal("deletion removed the worktree at the Git-registered aside path of another thread")
	}
}

func TestARetriedCreateKeepsItsReceiptAfterItsWorktreeGoes(t *testing.T) {
	w := serveWorktrees(t)
	owner := worktreeOwner(t, w, repo(t))
	c := api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Workspace: owner.Workspace, Title: "Open existing checkout"}}
	first, err := w.h.Submit(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, w.h, c.Target, api.ThreadDelete{})
	w.remove(t, owner.ID)
	if exists(owner.Workspace) {
		t.Fatal("the worktree must be gone before the retry")
	}
	next, err := w.h.Submit(ctx, c)
	if err != nil || next != first {
		t.Fatalf("committed create must return the same receipt after deletion: got %+v, %v; want %+v", next, err, first)
	}
}

func TestASubmoduleNeverCheckedOutDoesNotBlockTheDelete(t *testing.T) {
	w := serveWorktrees(t)
	project, sub := repo(t), repo(t)
	gitIn(t, project, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "dependency")
	gitIn(t, project, "commit", "-qm", "add submodule")
	owner := worktreeOwner(t, w, project)
	dir := owner.Workspace
	registration := gitIn(t, dir, "rev-parse", "--absolute-git-dir")
	if exists(filepath.Join(registration, "modules")) {
		t.Fatal("the linked checkout must have never initialized the submodule")
	}
	w.remove(t, owner.ID)
	if exists(dir) {
		t.Fatal("a gitlink without populated submodule state should not block cleanup")
	}
}
