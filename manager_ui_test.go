package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

func TestManagerUIHeaderSweepAndWakeDismiss(t *testing.T) {
	dir := tempDir(t)
	h := startWith(t, dir, func(c *daemon.Config) {
		c.Providers = daemon.FakeProviders(fake.New(fake.Turn(true)))
	})

	ws := gitWorkspace(t)
	mgrID := h.a.backend.CreateManager(h.local(), ws, "Workspace Coordinator", api.ModelRef{Provider: "claude", Model: "claude-opus-5-5"}, api.AccessAutoEdits, &api.SweepSchedule{IntervalSeconds: 1800})
	h.until("manager created", func() bool {
		th := h.local().Threads[mgrID]
		return th != nil && th.View.Role == api.RoleManager && th.View.Revision > 0
	})

	h.a.selected = mgrID
	h.a.backend.OpenThread(h.local(), mgrID)
	h.tt.Advance(time.Second)

	if !h.tt.HasText("Manager") || !h.tt.HasText("Sweep 30m") {
		t.Fatalf("header missing Manager/Sweep 30m pills: %q", h.tt.Texts())
	}

	h.click("Sweep 30m")
	h.until("sweep updated to 1h", func() bool {
		th := h.local().Threads[mgrID]
		return th != nil && th.View.Sweep != nil && th.View.Sweep.IntervalSeconds == 3600
	})
	h.tt.Frame()
	if !h.tt.HasText("Sweep 1h") {
		t.Fatalf("expected updated Sweep 1h pill, got %q", h.tt.Texts())
	}

	runID := h.a.backend.Send(h.local(), mgrID, "sweep turn", api.StartNow)
	h.until("manager turn waiting", func() bool {
		th := h.local().Threads[mgrID]
		return th != nil && th.View.Turn != nil && th.View.Run == api.RunWaiting
	})
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer ep.Close()
	_, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: mgrID, Body: api.WakeDismiss{Run: runID, Reason: "quiet"}})
	if !errors.As(err, new(*api.RejectedError)) {
		t.Fatalf("WakeDismiss of the user's turn = %v, want RejectedError", err)
	}
}

func TestManagerRowsShowTheirBadge(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/mygo", "A plain thread")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := d.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{
		Workspace: "/work/mygo", Title: "The coordinator", Model: fakeModel, Role: api.RoleManager,
	}}); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	h.thread("The coordinator")
	h.thread("A plain thread")
	badge := func(where string) {
		t.Helper()
		settlePlugins(t, h.a, h.tt, h.p)
		h.tt.Advance(time.Second)
		if n := slices.Index(h.tt.Texts(), "M"); n < 0 || slices.Index(h.tt.Texts()[n+1:], "M") >= 0 {
			t.Fatalf("%s: the badges are %q, want one", where, h.tt.Texts())
		}
		name, _ := h.tt.Find("The coordinator")
		m, _ := h.tt.Find("M")
		if gap := m.X - (name.X + name.W); gap < 0 || gap > 12 || m.Y > name.Y+name.H || name.Y > m.Y+m.H {
			t.Fatalf("%s: the badge %+v isn't right after its title %+v", where, m, name)
		}
	}
	badge("Glitched's own sidebar")
	if err := h.a.plugins.SetEnabled("sidebar", false); err != nil {
		t.Fatal(err)
	}
	h.a.pluginsChanged(nil)
	badge("the fallback")
}

func TestCmdKNewManagerTakesTheProjectAndTheNewThreadsModel(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	repo, worktree := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := d.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{
		Workspace: worktree, Project: repo, Branch: "feature", Title: "In a worktree", Model: fakeModel,
	}}); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	h.a.select_(h.thread("In a worktree"))
	h.a.show(choice{Model: modelKey{Provider: fakeModel.Provider}, Follow: true})
	mgr := h.newManager()
	if mgr.View.Workspace != repo {
		t.Errorf("the Manager runs in %q, want the project %q", mgr.View.Workspace, repo)
	}
	if mgr.View.Model.Model != "" || mgr.View.Model.Provider != fakeModel.Provider {
		t.Errorf("the Manager's model is %+v, want the program's own", mgr.View.Model)
	}
}

func TestCmdKNewManagerWithNoProjectHasItsOwnFolder(t *testing.T) {
	h := start(t, tempDir(t))
	h.a.startIn(draft{env: h.local(), workspace: h.a.scratchRoot()})
	before, _ := os.ReadDir(h.a.scratchRoot())
	h.a.actions()
	if listed, _ := os.ReadDir(h.a.scratchRoot()); len(listed) != len(before) {
		t.Fatalf("listing the actions made folders: %v, was %v", listed, before)
	}
	mgr := h.newManager()
	ws := mgr.View.Workspace
	if filepath.Dir(ws) != h.a.scratchRoot() {
		t.Fatalf("the Manager runs in %q, want a folder of its own under %q", ws, h.a.scratchRoot())
	}
	if fi, err := os.Stat(ws); err != nil || !fi.IsDir() {
		t.Fatalf("the Manager's folder %q: %v", ws, err)
	}
}

func (h *harness) newManager() *client.Thread {
	h.t.Helper()
	before := h.a.selected
	i := slices.IndexFunc(h.a.actions(), func(c command) bool { return c.id == "manager.new" })
	if i < 0 {
		h.t.Fatal("Cmd+K lists no New manager thread")
	}
	h.a.actions()[i].run()
	id := h.a.selected
	if id == before || id.IsZero() {
		h.t.Fatalf("New manager thread showed %s, not a new thread", id)
	}
	h.until("the Manager", func() bool {
		th := h.local().Threads[id]
		return th != nil && th.View.Role == api.RoleManager && th.View.Revision > 0
	})
	return h.local().Threads[id]
}

func TestManagerSweepButtonKeepsItsPrompt(t *testing.T) {
	const prompt = "Check CI and report only red builds."
	at := &api.SweepSchedule{IntervalSeconds: 1800, Prompt: prompt}
	for at.IntervalSeconds != 0 {
		next := nextSweepSchedule(at)
		if next.IntervalSeconds == at.IntervalSeconds || next.IntervalSeconds != 0 && next.Prompt != prompt {
			t.Fatalf("from %+v the button goes to %+v", at, next)
		}
		at = next
	}
}

func TestManagerFailedSweepSaysSo(t *testing.T) {
	p := client.Pending{Body: api.ThreadConfigure{Sweep: &api.SweepSchedule{IntervalSeconds: 3600}}, Err: errors.New("boom")}
	if got, want := failure(p), "Couldn't change the thread's sweep schedule: boom"; got != want {
		t.Fatalf("the row says %q, want %q", got, want)
	}
}
