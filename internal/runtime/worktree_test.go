package runtime_test

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// opened records the workspace of every session its adapter opens.
type opened struct {
	provider.Adapter
	mu   sync.Mutex
	dirs []string
}

func (o *opened) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	o.mu.Lock()
	o.dirs = append(o.dirs, spec.Workspace)
	o.mu.Unlock()
	return o.Adapter.Open(ctx, spec)
}

func (o *opened) last() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.dirs) == 0 {
		return ""
	}
	return o.dirs[len(o.dirs)-1]
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := osexec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repo is a repository with one commit on main, and a branch other one
// commit ahead, checked out on main.
func repo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644)
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "start")
	gitIn(t, dir, "switch", "-q", "-c", "other")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "ahead")
	gitIn(t, dir, "switch", "-q", "main")
	return dir
}

type worktreeHost struct {
	h    *runtime.Host
	ad   *opened
	root string
}

func serveWorktrees(t *testing.T) worktreeHost { return serveWorktreesAsking(t, false) }

// serveWorktreesAsking is serveWorktrees, with turns that wait on an
// approval while ask.
func serveWorktreesAsking(t *testing.T, ask bool) worktreeHost {
	t.Helper()
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	ad := &opened{Adapter: fake.New(fake.Turn(ask))}
	root := filepath.Join(t.TempDir(), "worktrees")
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithWorktrees(root))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill(); st.Close() })
	return worktreeHost{h, ad, root}
}

func inWorktree(workspace, base string) api.ThreadCreate {
	return api.ThreadCreate{Workspace: workspace, Title: "Fix the login flow!", Model: api.ModelRef{Provider: "fake", Model: "fake"},
		Text: "go", Worktree: &api.NewWorktree{Base: base}}
}

func view(t *testing.T, ep api.Endpoint, thread api.ID) api.Thread {
	t.Helper()
	snap, err := ep.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	return snap.Threads[0]
}

// A thread asked for in a new worktree runs its agent in a worktree of
// the project's repository, on a branch of its own from HEAD, and stays
// the project's; the same create sent again is the same thread.
func TestAThreadInANewWorktreeRunsItsAgentThere(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	thread := api.NewID()
	c := api.Command{ID: api.NewID(), Target: thread, Body: inWorktree(project, "")}
	first, err := w.h.Submit(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	th := until(t, w.h, thread, "the first turn's end", func(th api.Thread) bool { return th.LastTurn != nil })
	if th.Project != project {
		t.Errorf("project %q, want %q", th.Project, project)
	}
	if !strings.HasPrefix(th.Branch, "glitched/fix-the-login-flow-") {
		t.Errorf("branch %q, want glitched/fix-the-login-flow-…", th.Branch)
	}
	if !strings.HasPrefix(th.Workspace, w.root+string(filepath.Separator)) {
		t.Errorf("workspace %q, want one under %q", th.Workspace, w.root)
	}
	if got := w.ad.last(); got != th.Workspace {
		t.Errorf("the agent ran in %q, want the worktree %q", got, th.Workspace)
	}
	if got := gitIn(t, th.Workspace, "branch", "--show-current"); got != th.Branch {
		t.Errorf("the worktree is on %q, want %q", got, th.Branch)
	}
	if got, want := gitIn(t, th.Workspace, "rev-parse", "HEAD"), gitIn(t, project, "rev-parse", "main"); got != want {
		t.Errorf("the worktree starts at %s, want main's %s", got, want)
	}
	if got := gitIn(t, project, "branch", "--show-current"); got != "main" {
		t.Errorf("the project's folder moved to %q", got)
	}
	again, err := w.h.Submit(ctx, c)
	if err != nil || again.Committed != first.Committed {
		t.Errorf("the create again: %+v, %v; want the first receipt %+v", again, err, first)
	}
}

// A worktree from another base starts at that branch, and a thread
// started from a subfolder of the repository runs in that subfolder of
// its worktree.
func TestAWorktreeStartsFromItsBaseInItsSubfolder(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	sub := filepath.Join(project, "app")
	os.Mkdir(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "main.go"), []byte("package app\n"), 0o644)
	gitIn(t, project, "add", "app")
	gitIn(t, project, "commit", "-q", "-m", "app")
	gitIn(t, project, "switch", "-q", "other")
	gitIn(t, project, "merge", "-q", "main")
	gitIn(t, project, "switch", "-q", "main")
	thread := api.NewID()
	submit(t, w.h, thread, inWorktree(sub, "other"))
	th := view(t, w.h, thread)
	if th.Project != sub || filepath.Base(th.Workspace) != "app" {
		t.Errorf("in %q of %q, want the worktree's app of %q", th.Workspace, th.Project, sub)
	}
	top := filepath.Dir(th.Workspace)
	if got, want := gitIn(t, top, "rev-parse", "HEAD"), gitIn(t, project, "rev-parse", "other"); got != want {
		t.Errorf("the worktree starts at %s, want other's %s", got, want)
	}
}

// A folder that isn't a repository, a repository with no commits and a
// base it lacks are refused with why, and no thread is made.
func TestAWorktreeTheRepositoryCantGiveIsRefused(t *testing.T) {
	w := serveWorktrees(t)
	empty := t.TempDir()
	unborn := t.TempDir()
	gitIn(t, unborn, "init", "-q")
	for _, tc := range []struct {
		name, workspace, base, reason string
	}{
		{"no repository", empty, "", "is not in a git repository"},
		{"no commits", unborn, "", "has no commits yet"},
		{"no such base", repo(t), "nope", "no branch or commit \"nope\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			thread := api.NewID()
			_, err := w.h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: inWorktree(tc.workspace, tc.base)})
			var rej *api.RejectedError
			if !errors.As(err, &rej) || !strings.Contains(rej.Reason, tc.reason) {
				t.Fatalf("got %v, want a refusal saying %q", err, tc.reason)
			}
			if _, err := w.h.Read(ctx, api.Query{Thread: thread}); !errors.Is(err, api.ErrNotFound) {
				t.Errorf("a refused create left a thread: %v", err)
			}
		})
	}
}

// Deleting a thread keeps its worktree unless asked; a removal is refused
// while the worktree has changes, which only a removal with changes throws
// away; and a branch with commits of its own stays.
func TestDeletingAThreadInAWorktree(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	make := func() api.Thread {
		thread := api.NewID()
		submit(t, w.h, thread, inWorktree(project, ""))
		return until(t, w.h, thread, "the first turn's end", func(th api.Thread) bool { return th.LastTurn != nil })
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	branches := func() string { return gitIn(t, project, "branch", "--list", "glitched/*") }

	kept := make()
	submit(t, w.h, kept.ID, api.ThreadDelete{})
	if !exists(kept.Workspace) || !strings.Contains(branches(), kept.Branch) {
		t.Errorf("a plain delete took the worktree or its branch")
	}

	clean := make()
	submit(t, w.h, clean.ID, api.ThreadDelete{Worktree: api.RemoveWorktree})
	if exists(clean.Workspace) || strings.Contains(branches(), clean.Branch) {
		t.Errorf("a clean worktree's removal left its folder or its branch with nothing on it:\n%s", branches())
	}

	dirty := make()
	os.WriteFile(filepath.Join(dirty.Workspace, "notes.txt"), []byte("mine\n"), 0o644)
	_, err := w.h.Submit(ctx, api.Command{ID: api.NewID(), Target: dirty.ID, Body: api.ThreadDelete{Worktree: api.RemoveWorktree}})
	var rej *api.RejectedError
	if !errors.As(err, &rej) || !strings.Contains(rej.Reason, "1 uncommitted change") {
		t.Fatalf("removing a worktree with changes: %v, want a refusal", err)
	}
	if view(t, w.h, dirty.ID).Deleted || !exists(filepath.Join(dirty.Workspace, "notes.txt")) {
		t.Fatalf("a refused removal deleted the thread or its changes")
	}
	gitIn(t, dirty.Workspace, "add", ".")
	gitIn(t, dirty.Workspace, "commit", "-q", "-m", "notes")
	os.WriteFile(filepath.Join(dirty.Workspace, "more.txt"), []byte("more\n"), 0o644)
	submit(t, w.h, dirty.ID, api.ThreadDelete{Worktree: api.RemoveWorktreeChanges})
	if exists(dirty.Workspace) {
		t.Errorf("a removal with changes left the worktree")
	}
	if !strings.Contains(branches(), dirty.Branch) {
		t.Errorf("the branch with a commit of its own went:\n%s", branches())
	}
}

func refusal(t *testing.T, err error, says string) {
	t.Helper()
	var rej *api.RejectedError
	if !errors.As(err, &rej) || !strings.Contains(rej.Reason, says) {
		t.Fatalf("got %v, want a refusal saying %q", err, says)
	}
}

// Changes git hides by its settings still keep a worktree from a plain
// removal, and an end the API doesn't name removes nothing.
func TestAWorktreeWithHiddenChangesIsNotRemoved(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	gitIn(t, project, "config", "status.showUntrackedFiles", "no")
	thread := api.NewID()
	submit(t, w.h, thread, inWorktree(project, ""))
	th := until(t, w.h, thread, "the first turn's end", func(th api.Thread) bool { return th.LastTurn != nil })
	note := filepath.Join(th.Workspace, "notes", "mine.txt")
	os.MkdirAll(filepath.Dir(note), 0o755)
	os.WriteFile(note, []byte("mine\n"), 0o644)
	for _, end := range []api.WorktreeEnd{api.RemoveWorktree, 3} {
		_, err := w.h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadDelete{Worktree: end}})
		if err == nil {
			t.Fatalf("a delete with worktree end %d went", end)
		}
		if _, err := os.Stat(note); err != nil || view(t, w.h, thread).Deleted {
			t.Fatalf("a refused delete with end %d took the thread or its note: %v", end, err)
		}
	}
}

// The worktree of a thread still at work stays: its agent may write
// there after the host looked.
func TestTheWorktreeOfAThreadAtWorkIsNotRemoved(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	project := repo(t)
	thread := api.NewID()
	submit(t, w.h, thread, inWorktree(project, ""))
	th := until(t, w.h, thread, "the approval", waiting)
	_, err := w.h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadDelete{Worktree: api.RemoveWorktreeChanges}})
	refusal(t, err, "stop the thread")
	if _, err := os.Stat(th.Workspace); err != nil {
		t.Fatalf("the refused delete took the worktree: %v", err)
	}
}

// A folder at the worktree's place that isn't the thread's worktree, as
// another repository's, is never removed for the thread.
func TestAReplacedWorktreeIsNotRemoved(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	thread := api.NewID()
	submit(t, w.h, thread, inWorktree(project, ""))
	th := until(t, w.h, thread, "the first turn's end", func(th api.Thread) bool { return th.LastTurn != nil })
	gitIn(t, project, "worktree", "move", th.Workspace, filepath.Join(t.TempDir(), "moved"))
	other := repo(t)
	gitIn(t, other, "worktree", "add", "-q", "-b", th.Branch, th.Workspace)
	note := filepath.Join(th.Workspace, "theirs.txt")
	os.WriteFile(note, []byte("theirs\n"), 0o644)
	_, err := w.h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadDelete{Worktree: api.RemoveWorktreeChanges}})
	refusal(t, err, "is no longer the thread's worktree")
	if _, err := os.Stat(note); err != nil {
		t.Fatalf("another repository's checkout lost its file: %v", err)
	}
}

// Two threads whose IDs end alike get worktrees of their own.
func TestThreadsWhoseIDsEndAlikeGetWorktreesOfTheirOwn(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	a, b := api.ID{6: 1}, api.ID{6: 2}
	submit(t, w.h, a, inWorktree(project, ""))
	_, err := w.h.Submit(ctx, api.Command{ID: api.NewID(), Target: b, Body: inWorktree(project, "")})
	if err == nil && view(t, w.h, b).Workspace == view(t, w.h, a).Workspace {
		t.Fatalf("two threads share the worktree %q", view(t, w.h, a).Workspace)
	}
}

// A create the engine would refuse runs no git, and the same command ID
// with another base is a conflict, sent again after the thread's delete
// gets its receipt and makes no worktree again.
func TestACreateIsCheckedBeforeGitAndKeepsItsReceipt(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	bad := inWorktree(project, "")
	bad.Access = 9
	_, err := w.h.Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: bad})
	refusal(t, err, "unknown access level")
	if entries, _ := os.ReadDir(w.root); len(entries) != 0 {
		t.Fatalf("a refused create made %d folders under the worktrees' root", len(entries))
	}

	thread := api.NewID()
	c := api.Command{ID: api.NewID(), Target: thread, Body: inWorktree(project, "")}
	first, err := w.h.Submit(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	th := until(t, w.h, thread, "the first turn's end", func(th api.Thread) bool { return th.LastTurn != nil })
	other := c
	other.Body = inWorktree(project, "other")
	if _, err := w.h.Submit(ctx, other); !errors.Is(err, api.ErrConflict) {
		t.Errorf("the same ID with another base: %v, want a conflict", err)
	}
	submit(t, w.h, thread, api.ThreadDelete{Worktree: api.RemoveWorktree})
	again, err := w.h.Submit(ctx, c)
	if err != nil || again.Committed != first.Committed {
		t.Errorf("the create again after the delete: %+v, %v; want the first receipt", again, err)
	}
	if _, err := os.Stat(th.Workspace); err == nil {
		t.Errorf("the create again made the worktree again")
	}
}

// A thread started from a folder the base doesn't have is refused, and
// the worktree made for it goes.
func TestAFolderTheBaseLacksIsRefused(t *testing.T) {
	w := serveWorktrees(t)
	project := repo(t)
	sub := filepath.Join(project, "scratch")
	os.Mkdir(sub, 0o755)
	_, err := w.h.Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: inWorktree(sub, "")})
	refusal(t, err, "is not a folder of the worktree")
	if out := gitIn(t, project, "worktree", "list", "--porcelain"); strings.Count(out, "worktree ") != 1 {
		t.Errorf("the refused create left a worktree:\n%s", out)
	}
}
