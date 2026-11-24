package runtime_test

import (
	"context"
	"errors"
	"os"
	"sync"

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

func attempt(h *runtime.Host, target api.ID, b api.Intent) (api.Receipt, error) {
	return h.Submit(ctx, api.Command{ID: api.NewID(), Target: target, Body: b})
}

func TestAWorktreeStaysUntilItsAgentHasClosed(t *testing.T) {
	path, root := dbPath(t), filepath.Join(t.TempDir(), "worktrees")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	gate := onceClosing{Adapter: fake.New(fake.Turn(true)), once: new(sync.Once), entered: make(chan struct{}), release: make(chan struct{})}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": gate, "parent": fake.New(fake.Turn(true))}, runtime.WithWorktrees(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill() })
	w := worktreeHost{h: h, root: root, db: sideDB(t, path)}
	pid := api.NewID()
	submit(t, h, pid, api.ThreadCreate{Workspace: repo(t), Title: "Parent", Model: api.ModelRef{Provider: "parent"}, Text: "go"})
	p := until(t, h, pid, "parent waiting", waiting)
	kid := child(t, h, isolated(p))
	stopped(t, h, kid, p)
	select {
	case <-gate.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the child's agent never started closing")
	}
	submit(t, h, p.ID, api.ThreadDelete{RemoveWorktree: true})
	if !exists(kid.Workspace) {
		t.Fatal("the delete took the child's worktree while its agent was still closing")
	}
	close(gate.release)
	w.settle(t)
	if exists(kid.Workspace) {
		t.Fatal("the child's worktree stayed after its agent closed")
	}
}

func TestDeletingASharedChildRemovesItsChildsWorktreeAndKeepsTheParents(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	p := asking(t, w.h, api.ThreadCreate{Workspace: repo(t), Worktree: &api.NewWorktree{}})
	sharedCreate := isolated(p)
	sharedCreate.Worktree, sharedCreate.Project, sharedCreate.Branch = nil, p.Project, p.Branch
	shared := child(t, w.h, sharedCreate)
	kid := child(t, w.h, isolated(shared))
	stopped(t, w.h, kid, shared)
	if _, err := attempt(w.h, shared.ID, api.ThreadDelete{RemoveWorktree: true}); err != nil {
		t.Fatalf("cannot remove the owned worktree inside a shared subtree: %v", err)
	}
	w.settle(t)
	if !exists(p.Workspace) || exists(kid.Workspace) {
		t.Fatalf("expected retained parent checkout and removed grandchild: parent=%v grandchild=%v", exists(p.Workspace), exists(kid.Workspace))
	}
}

func TestALockedWorktreeUnderATreeRefusesTheDelete(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	project := repo(t)
	p := asking(t, w.h, api.ThreadCreate{Workspace: project})
	kid := child(t, w.h, isolated(p))
	stopped(t, w.h, kid, p)
	gitIn(t, project, "worktree", "lock", kid.Workspace)
	_, err := attempt(w.h, p.ID, api.ThreadDelete{RemoveWorktree: true})
	if err == nil {
		_, readErr := w.h.Read(ctx, api.Query{Thread: kid.ID})
		t.Fatalf("delete returned success for locked child, checkout exists=%v; child read error=%v", exists(kid.Workspace), readErr)
	}
}

func TestAReplayedRemovalKeepsABranchMadeAgain(t *testing.T) {
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
	project := repo(t)
	id := api.NewID()
	create := inWorktree(project, "")
	create.Text = ""
	submit(t, h, id, create)
	th := view(t, h, id)
	h.Kill()
	if _, err := st.Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: api.ThreadDelete{RemoveWorktree: true}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := st.Lease(ctx, nil)
	if err != nil || !ok {
		t.Fatalf("lease: %v %v", ok, err)
	}
	if _, ok := lease.Body.(engine.RemoveWorktree); !ok {
		t.Fatalf("unexpected lease: %+v", lease)
	}
	gitIn(t, project, "worktree", "remove", th.Workspace)
	gitIn(t, project, "branch", "-d", th.Branch)
	gitIn(t, project, "branch", th.Branch)
	h, err = runtime.New(ctx, st, nil, runtime.WithWorktrees(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill() })
	db := sideDB(t, path)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		var state string
		if err := db.QueryRow("SELECT state FROM effects WHERE id = ?", lease.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("effect state %q", state)
		}
	}
	if got := gitIn(t, project, "branch", "--list", th.Branch); !strings.Contains(got, th.Branch) {
		t.Fatal("replayed removal deleted an independently recreated branch")
	}
}

func TestDeletingChildrenOneByOneKeepsTheParentsWorktree(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	p := asking(t, w.h, api.ThreadCreate{Workspace: repo(t), Worktree: &api.NewWorktree{}})
	sharedCreate := isolated(p)
	sharedCreate.Worktree, sharedCreate.Project, sharedCreate.Branch = nil, p.Project, p.Branch
	shared := child(t, w.h, sharedCreate)
	kid := child(t, w.h, isolated(p))
	stopped(t, w.h, kid, shared)
	_, err := attempt(w.h, shared.ID, api.ThreadDelete{RemoveWorktree: true})
	if err == nil {
		t.Fatal("shared-only removal unexpectedly accepted")
	}
	_, err = attempt(w.h, shared.ID, api.ThreadDelete{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = attempt(w.h, kid.ID, api.ThreadDelete{RemoveWorktree: true})
	if err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if !exists(p.Workspace) || exists(kid.Workspace) {
		t.Fatal("expected just parent worktree")
	}
	if th := view(t, w.h, p.ID); th.Turn == nil {
		t.Fatal("parent stopped")
	}
}

func TestAWorktreeChildIsCheckedBeforeGit(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root := filepath.Join(t.TempDir(), "worktrees")
	h, err := runtime.New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(true))}, runtime.WithWorktrees(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill() })
	p := asking(t, h, api.ThreadCreate{Workspace: repo(t)})
	wide := isolated(p)
	wide.Access = api.AccessFull
	_, err = attempt(h, api.NewID(), wide)
	refusal(t, err, "access can't be wider")
	st.SetLimits(engine.Limits{All: 1})
	shared := isolated(p)
	shared.Worktree = nil
	child(t, h, shared)
	_, err = attempt(h, api.NewID(), isolated(p))
	refusal(t, err, "the most it may")
	if _, err = os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused create touched %s: %v", root, err)
	}
}

func TestACheckoutPutInPlaceAfterTheDeleteIsNotRemoved(t *testing.T) {
	path, root := dbPath(t), filepath.Join(t.TempDir(), "worktrees")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	gate := onceClosing{Adapter: fake.New(fake.Turn(true)), once: new(sync.Once), entered: make(chan struct{}), release: make(chan struct{})}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": gate, "parent": fake.New(fake.Turn(true))}, runtime.WithWorktrees(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill() })
	w := worktreeHost{h: h, root: root, db: sideDB(t, path)}
	project := repo(t)
	pid := api.NewID()
	submit(t, h, pid, api.ThreadCreate{Workspace: project, Title: "Parent", Model: api.ModelRef{Provider: "parent"}, Text: "go"})
	p := until(t, h, pid, "parent waiting", waiting)
	kid := child(t, h, isolated(p))
	stopped(t, h, kid)
	select {
	case <-gate.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the child's agent never started closing")
	}
	submit(t, h, kid.ID, api.ThreadDelete{RemoveWorktree: true})
	moved := filepath.Join(t.TempDir(), "old")
	gitIn(t, project, "worktree", "move", kid.Workspace, moved)
	gitIn(t, moved, "switch", "-q", "--detach")
	gitIn(t, project, "worktree", "add", "-q", kid.Workspace, kid.Branch)
	note := filepath.Join(kid.Workspace, "replacement.txt")
	if err := os.WriteFile(note, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	w.settle(t)
	if !exists(note) || !strings.Contains(gitIn(t, project, "branch", "--list", kid.Branch), kid.Branch) {
		t.Fatal("the removal took a checkout put at the worktree's path after the delete")
	}
}

func TestAnotherThreadInTheWorktreeKeepsIt(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	project := repo(t)
	p := asking(t, w.h, api.ThreadCreate{Workspace: project})
	kid := child(t, w.h, isolated(p))
	stopped(t, w.h, kid)
	live := asking(t, w.h, api.ThreadCreate{Workspace: kid.Workspace})
	if _, err := attempt(w.h, kid.ID, api.ThreadDelete{RemoveWorktree: true}); err != nil {
		return
	}
	branch := gitIn(t, project, "branch", "--list", kid.Branch)
	if view(t, w.h, live.ID).Turn != nil && (!exists(live.Workspace) || branch == "") {
		t.Fatalf("unrelated turn still live, checkout exists=%v branch exists=%v", exists(live.Workspace), branch != "")
	}
}

func TestARemovalFindsItsWorktreeThroughALinkedPath(t *testing.T) {
	real := t.TempDir()
	tmp := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, tmp); err != nil {
		t.Fatal(err)
	}
	path, root := filepath.Join(tmp, "host.db"), filepath.Join(tmp, "worktrees")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h, err := runtime.New(ctx, st, nil, runtime.WithWorktrees(root))
	if err != nil {
		t.Fatal(err)
	}
	project := repo(t)
	id := api.NewID()
	create := inWorktree(project, "")
	create.Text = ""
	submit(t, h, id, create)
	th := view(t, h, id)
	h.Kill()
	if _, err := st.Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: api.ThreadDelete{RemoveWorktree: true}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	h, err = runtime.New(ctx, st, nil, runtime.WithWorktrees(filepath.Join(real, "worktrees")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill() })
	db := sideDB(t, path)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		var state string
		if err := db.QueryRow("SELECT state FROM effects WHERE kind = 'worktree.remove'").Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("effect state %q", state)
		}
	}
	if exists(th.Workspace) {
		t.Fatalf("the removal was done but the worktree stayed, made under %s and removed under %s", tmp, real)
	}
}

func TestLimitsChangeWhileAWorktreeChildIsMade(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h, err := runtime.New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(true))}, runtime.WithWorktrees(filepath.Join(t.TempDir(), "worktrees")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill() })
	p := asking(t, h, api.ThreadCreate{Workspace: repo(t)})
	started, stop, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		st.SetLimits(engine.Limits{All: 10})
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
			}
			st.SetLimits(engine.Limits{All: 11})
			st.SetLimits(engine.Limits{All: 10})
		}
	}()
	defer func() { close(stop); <-exited }()
	<-started
	child(t, h, isolated(p))
}

func TestATreeRemovalKeepsAWorktreeItsChildKept(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	project := repo(t)
	p := asking(t, w.h, api.ThreadCreate{Workspace: project, Worktree: &api.NewWorktree{}})
	kid := child(t, w.h, isolated(p))
	stopped(t, w.h, kid, p)
	gate := t.TempDir()
	t.Setenv("GATE_GATE", gate)
	t.Setenv("PATH", gate+string(os.PathListSeparator)+os.Getenv("PATH"))
	wrapper := `#!/bin/sh
if [ "$3" = rev-parse ] && [ "$4" = --path-format=absolute ] && [ "$5" = --git-common-dir ]; then
  touch "$GATE_GATE/entered"
  n=0
  while [ ! -e "$GATE_GATE/release" ]; do
    n=$((n+1))
    if [ "$n" -gt 250 ]; then exit 42; fi
    sleep .02
  done
fi
exec /usr/bin/git "$@"
`
	if err := os.WriteFile(filepath.Join(gate, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	release := func() { _ = os.WriteFile(filepath.Join(gate, "release"), nil, 0o644) }
	defer release()
	done := make(chan error, 1)
	go func() {
		_, err := attempt(w.h, p.ID, api.ThreadDelete{RemoveWorktree: true})
		done <- err
	}()
	for deadline := time.Now().Add(4 * time.Second); !exists(filepath.Join(gate, "entered")); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("parent delete never reached preflight Git")
		}
	}
	if _, err := attempt(w.h, kid.ID, api.ThreadDelete{}); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(kid.Workspace, "kept.txt")
	if err := os.WriteFile(note, []byte("preserve this\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent delete never returned")
	}
	if !exists(note) {
		t.Fatal("successful child Keep worktree was overridden by a stale parent delete snapshot")
	}
}

func TestAWorktreeChildOfAThreadInAnEngineWorktreeWithNoProjectIsRefused(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	owner := asking(t, w.h, api.ThreadCreate{Workspace: repo(t), Worktree: &api.NewWorktree{}})
	plain := asking(t, w.h, api.ThreadCreate{Workspace: owner.Workspace})
	_, err := attempt(w.h, api.NewID(), isolated(plain))
	refusal(t, err, "no project of its own")
}

func TestAWorktreeChildRefusedAfterGitLeavesNoWorktree(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.SetLimits(engine.Limits{All: 1})
	h, err := runtime.New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(true))}, runtime.WithWorktrees(filepath.Join(t.TempDir(), "worktrees")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill() })
	project := repo(t)
	p := asking(t, h, api.ThreadCreate{Workspace: project})
	gate := t.TempDir()
	t.Setenv("GATE_GATE", gate)
	hook := `#!/bin/sh
touch "$GATE_GATE/entered"
n=0
while [ ! -e "$GATE_GATE/release" ]; do
  n=$((n+1))
  if [ "$n" -gt 250 ]; then exit 42; fi
  sleep .02
done
`
	if err := os.WriteFile(filepath.Join(project, ".git", "hooks", "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	release := func() { _ = os.WriteFile(filepath.Join(gate, "release"), nil, 0o644) }
	defer release()
	id := api.NewID()
	done := make(chan error, 1)
	go func() {
		_, err := attempt(h, id, isolated(p))
		done <- err
	}()
	for deadline := time.Now().Add(4 * time.Second); !exists(filepath.Join(gate, "entered")); time.Sleep(5 * time.Millisecond) {
		select {
		case err := <-done:
			t.Fatalf("isolated create returned before checkout hook: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("checkout hook never entered")
		}
	}
	shared := isolated(p)
	shared.Worktree = nil
	child(t, h, shared)
	release()
	select {
	case err := <-done:
		refusal(t, err, "the most it may")
	case <-time.After(5 * time.Second):
		t.Fatal("isolated create never returned")
	}
	if th, err := st.Thread(ctx, id); err != nil || th.Exists() {
		t.Fatalf("refused child exists: %v %v", th.Exists(), err)
	}
	if wt := gitIn(t, project, "worktree", "list", "--porcelain"); strings.Count(wt, "worktree ") != 1 {
		t.Fatalf("late limit refusal left a registered checkout and branch without a thread:\n%s", wt)
	}
}

type heldReconcile struct {
	provider.Adapter
	target           api.ID
	entered, release chan struct{}
}

func (g heldReconcile) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	if id.Thread == g.target {
		select {
		case g.entered <- struct{}{}:
		default:
		}
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.Adapter.Reconcile(ctx, id)
}

func TestARecoveredRemovalWaitsForASharedChildsClose(t *testing.T) {
	path, root := dbPath(t), filepath.Join(t.TempDir(), "worktrees")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ad := fake.New(fake.Turn(true))
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithWorktrees(root))
	if err != nil {
		t.Fatal(err)
	}
	p := asking(t, h, api.ThreadCreate{Workspace: repo(t), Worktree: &api.NewWorktree{}})
	create := isolated(p)
	create.Worktree, create.Project, create.Branch = nil, p.Project, p.Branch
	kid := child(t, h, create)
	stopped(t, h, kid, p)
	h.Kill()
	if _, err := st.Submit(ctx, api.Command{ID: api.NewID(), Target: p.ID, Body: api.ThreadDelete{RemoveWorktree: true}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	gate := heldReconcile{Adapter: ad, target: kid.ID, entered: make(chan struct{}, 1), release: make(chan struct{})}
	h, err = runtime.New(ctx, st, provider.Mux{"fake": gate}, runtime.WithWorktrees(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill() })
	defer close(gate.release)
	select {
	case <-gate.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("shared child never reached durable agent.close")
	}
	db := sideDB(t, path)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		var state string
		if err := db.QueryRow("SELECT state FROM effects WHERE kind='worktree.remove' AND thread=?", p.ID[:]).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "done" && !exists(p.Workspace) {
			t.Fatal("recovered removal deleted owner's checkout while shared child's agent.close was still blocked")
		}
	}
}

func TestABranchCheckedOutElsewhereStays(t *testing.T) {
	w := serveWorktreesAsking(t, true)
	project := repo(t)
	p := asking(t, w.h, api.ThreadCreate{Workspace: project})
	kid := child(t, w.h, isolated(p))
	stopped(t, w.h, kid)
	moved := filepath.Join(t.TempDir(), "moved")
	gitIn(t, project, "worktree", "move", kid.Workspace, moved)
	submit(t, w.h, kid.ID, api.ThreadDelete{RemoveWorktree: true})
	if !exists(moved) || !strings.Contains(gitIn(t, project, "branch", "--list", kid.Branch), kid.Branch) {
		t.Fatal("branch still checked out at another registered path was removed")
	}
}

type onceClosing struct {
	provider.Adapter
	once             *sync.Once
	entered, release chan struct{}
}

func (a onceClosing) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := a.Adapter.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return onceClosingAgent{ag, a}, nil
}

type onceClosingAgent struct {
	provider.Agent
	a onceClosing
}

func (g onceClosingAgent) Close(ctx context.Context) error {
	g.a.once.Do(func() { close(g.a.entered) })
	select {
	case <-g.a.release:
		return g.Agent.Close(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}
