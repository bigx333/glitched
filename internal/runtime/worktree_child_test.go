package runtime_test

import (
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

// asking starts a thread of the user's in workspace whose turn waits on
// an approval, so its run can start children.
func asking(t *testing.T, ep api.Endpoint, create api.ThreadCreate) api.Thread {
	t.Helper()
	thread := api.NewID()
	create.Title, create.Model, create.Text = "Parent", api.ModelRef{Provider: "fake", Model: "fake"}, "go"
	submit(t, ep, thread, create)
	return until(t, ep, thread, "the parent's approval", waiting)
}

// isolated is a create of a child of parent in a worktree of its own.
func isolated(parent api.Thread) api.ThreadCreate {
	return api.ThreadCreate{Workspace: parent.Workspace, Title: "Review it", Model: api.ModelRef{Provider: "fake", Model: "fake"},
		Parent: parent.ID, Run: parent.Turn.Run, Text: "review", Worktree: &api.NewWorktree{}}
}

// child starts a child of parent as create asks and returns it once its
// turn waits.
func child(t *testing.T, ep api.Endpoint, create api.ThreadCreate) api.Thread {
	t.Helper()
	id := api.NewID()
	submit(t, ep, id, create)
	return until(t, ep, id, "the child's approval", waiting)
}

func stopped(t *testing.T, ep api.Endpoint, threads ...api.Thread) {
	t.Helper()
	for _, th := range threads {
		submit(t, ep, th.ID, api.TurnInterrupt{})
		until(t, ep, th.ID, "the turn to stop", idle)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// A child asked for in a worktree runs in one of its own, on a new branch
// from its parent folder's HEAD, without the folder's uncommitted changes,
// and belongs to the parent's project.
func TestAWorktreeChildStartsFromItsParentsHead(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	project := repo(t)
	os.WriteFile(filepath.Join(project, "dirty.txt"), []byte("not committed\n"), 0o644)
	parent := asking(t, w.h, api.ThreadCreate{Workspace: project})
	kid := child(t, w.h, isolated(parent))
	if kid.Project != project || !strings.HasPrefix(kid.Branch, "glitched/review-it-") || !strings.HasPrefix(kid.Workspace, w.root+string(filepath.Separator)) {
		t.Fatalf("the child is in %q on %q of %q; want a worktree under %q of the project %q", kid.Workspace, kid.Branch, kid.Project, w.root, project)
	}
	if got := w.ad.last(); got != kid.Workspace {
		t.Errorf("the child's agent ran in %q, want its worktree %q", got, kid.Workspace)
	}
	if got, want := gitIn(t, kid.Workspace, "rev-parse", "HEAD"), gitIn(t, project, "rev-parse", "HEAD"); got != want {
		t.Errorf("the child's worktree starts at %s, want the parent's HEAD %s", got, want)
	}
	if exists(filepath.Join(kid.Workspace, "dirty.txt")) {
		t.Errorf("the parent's uncommitted file was copied")
	}
	if got := gitIn(t, project, "branch", "--show-current"); got != "main" {
		t.Errorf("the parent's folder moved to %q", got)
	}
}

// A child of a thread in a worktree starts from that worktree's HEAD, in
// a folder beside it, and belongs to the project the parent's worktree was
// made from, not to the worktree.
func TestAWorktreeChildOfAWorktreeThreadStartsFromItsHead(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	project := repo(t)
	parent := asking(t, w.h, api.ThreadCreate{Workspace: project, Worktree: &api.NewWorktree{}})
	gitIn(t, parent.Workspace, "commit", "-q", "--allow-empty", "-m", "the parent's work")
	kid := child(t, w.h, isolated(parent))
	if kid.Project != project || kid.Branch == parent.Branch {
		t.Errorf("the child is of %q on %q; want the project %q on a branch of its own", kid.Project, kid.Branch, project)
	}
	if got, want := gitIn(t, kid.Workspace, "rev-parse", "HEAD"), gitIn(t, parent.Workspace, "rev-parse", "HEAD"); got != want {
		t.Errorf("the child starts at %s, want the parent's worktree HEAD %s", got, want)
	}
	if filepath.Dir(kid.Workspace) != filepath.Dir(parent.Workspace) {
		t.Errorf("the child's worktree %q isn't beside the parent's %q", kid.Workspace, parent.Workspace)
	}
}

// A child create the engine would refuse makes no worktree.
func TestARefusedWorktreeChildMakesNoWorktree(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	project := repo(t)
	parent := asking(t, w.h, api.ThreadCreate{Workspace: project})
	stale := isolated(parent)
	stale.Run = api.NewID()
	_, err := w.h.Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: stale})
	refusal(t, err, "only the parent's live run")
	elsewhere := isolated(parent)
	elsewhere.Workspace = repo(t)
	_, err = w.h.Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: elsewhere})
	refusal(t, err, "from its parent's folder")
	if exists(w.root) {
		t.Errorf("a refused child made %s", w.root)
	}
}

// Deleting a tree removes the worktree of each child that has one of its
// own when the person says so, branches with nothing on them too, and
// leaves the parent's folder and a child's committed branch; a plain
// delete leaves every worktree.
func TestDeletingATreeRemovesItsChildrensWorktreesWhenAsked(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	project := repo(t)
	branches := func() string { return gitIn(t, project, "branch", "--list", "glitched/*") }

	parent := asking(t, w.h, api.ThreadCreate{Workspace: project})
	clean := child(t, w.h, isolated(parent))
	committed := child(t, w.h, isolated(parent))
	gitIn(t, committed.Workspace, "commit", "-q", "--allow-empty", "-m", "the child's work")
	shared := api.ThreadCreate{Workspace: project, Title: "In the folder", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Parent: parent.ID, Run: parent.Turn.Run, Text: "x"}
	inFolder := child(t, w.h, shared)
	stopped(t, w.h, clean, committed, inFolder, parent)
	w.remove(t, parent.ID)
	if exists(clean.Workspace) || exists(committed.Workspace) {
		t.Errorf("a removal left a child's worktree")
	}
	if strings.Contains(branches(), clean.Branch) || !strings.Contains(branches(), committed.Branch) {
		t.Errorf("branches after the removal:\n%s\nwant %s gone and %s, with a commit, kept", branches(), clean.Branch, committed.Branch)
	}
	if !exists(filepath.Join(project, "README")) {
		t.Errorf("the removal took the parent's folder")
	}

	kept := asking(t, w.h, api.ThreadCreate{Workspace: project})
	kid := child(t, w.h, isolated(kept))
	stopped(t, w.h, kid, kept)
	submit(t, w.h, kept.ID, api.ThreadDelete{})
	if !exists(kid.Workspace) || !strings.Contains(branches(), kid.Branch) {
		t.Errorf("a plain delete took the child's worktree or branch")
	}
}

// A host that stops after a tree delete commits and before its worktrees
// go leaves their removal in the outbox, and the next host removes them.
func TestAWorktreeRemovalCutOffByACrashIsDoneOnBoot(t *testing.T) {
	path, root := dbPath(t), filepath.Join(t.TempDir(), "worktrees")
	open := func() (*store.Store, *runtime.Host) {
		t.Helper()
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		h, err := runtime.New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(true))}, runtime.WithWorktrees(root))
		if err != nil {
			st.Close()
			t.Fatal(err)
		}
		return st, h
	}
	st, h := open()
	project := repo(t)
	parent := asking(t, h, api.ThreadCreate{Workspace: project})
	kid := child(t, h, isolated(parent))
	stopped(t, h, kid, parent)
	h.Kill()
	// The delete commits, as the host's own does, but no removal follows
	// it: what a crash just after the commit leaves.
	if _, err := st.Submit(ctx, api.Command{ID: api.NewID(), Target: parent.ID, Body: api.ThreadDelete{RemoveWorktree: true}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if !exists(kid.Workspace) {
		t.Fatal("the worktree went with no host")
	}
	st, h = open()
	t.Cleanup(func() { h.Kill(); st.Close() })
	worktreeHost{h: h, db: sideDB(t, path)}.settle(t)
	if exists(kid.Workspace) || strings.Count(gitIn(t, project, "worktree", "list", "--porcelain"), "worktree ") != 1 || strings.Contains(gitIn(t, project, "branch", "--list", kid.Branch), kid.Branch) {
		t.Fatalf("after the next host's removal the worktree is there: %v; git lists:\n%s", exists(kid.Workspace), gitIn(t, project, "worktree", "list", "--porcelain"))
	}
	assertReplay(t, st)
}

// A worktree child's report to its parent's agent names the child's
// branch, for the parent to merge.
func TestAWorktreeChildsReportNamesItsBranch(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "worktrees")
	h, err := runtime.New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(true)), "quick": fake.New(fake.Turn(false))}, runtime.WithWorktrees(root))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill(); st.Close() })
	parent := asking(t, h, api.ThreadCreate{Workspace: repo(t)})
	create := isolated(parent)
	create.Model.Provider = "quick"
	id := api.NewID()
	submit(t, h, id, create)
	kid := until(t, h, id, "the child's turn to end", func(th api.Thread) bool { return th.LastTurn != nil && th.Branch != "" })
	until(t, h, parent.ID, "the child's report", func(th api.Thread) bool { return th.LastReport != nil })
	var wake string
	for _, e := range events(t, h, parent.ID) {
		if b, err := e.Decode(); err == nil {
			if q, ok := b.(api.MessageQueued); ok && q.Report == kid.ID {
				wake = q.Text
			}
		}
	}
	if !strings.Contains(wake, `branch="`+kid.Branch+`"`) {
		t.Errorf("the parent's wake %q doesn't name the child's branch %s", wake, kid.Branch)
	}
}
