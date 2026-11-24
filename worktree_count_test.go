package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

func TestTheDeleteAlertCountsTheChangesInEveryWorktreeItRemoves(t *testing.T) {
	ctx := context.Background()
	ws, dir := gitWorkspace(t), tempDir(t)
	ad := fake.New(fake.Turn(true))
	d := engine(t, daemon.Config{Dir: dir}, ad)
	h := start(t, dir)
	ep := d.Endpoint()
	pid := api.NewID()
	send := func(id api.ID, b api.Intent) api.ID {
		t.Helper()
		cmd := api.NewID()
		if _, err := ep.Submit(ctx, api.Command{ID: cmd, Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	send(pid, api.ThreadCreate{Workspace: ws, Title: "Parent", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Worktree: &api.NewWorktree{}})
	ad.Spawns(ep, "split", fake.Child{Title: "Isolated", Task: "work", Worktree: true}, fake.Child{Title: "Shared", Task: "work"})
	run := send(pid, api.MessageDispatch{Text: "split", Mode: api.StartNow})
	isolated, shared := fake.ChildOf(run, 0), fake.ChildOf(run, 1)
	h.until("children waiting plus parent idle", func() bool {
		p, ok := h.a.find(pid)
		i, io := h.a.find(isolated)
		s, so := h.a.find(shared)
		return ok && io && so && p.View.LastTurn != nil && p.View.Run == api.RunIdle && i.View.Run == api.RunWaiting && s.View.Run == api.RunWaiting
	})
	send(isolated, api.TurnInterrupt{})
	send(shared, api.TurnInterrupt{})
	h.until("children idle", func() bool {
		i, _ := h.a.find(isolated)
		s, _ := h.a.find(shared)
		return i.View.Run == api.RunIdle && s.View.Run == api.RunIdle
	})
	p, _ := h.a.find(pid)
	i, _ := h.a.find(isolated)
	for _, file := range []string{filepath.Join(p.View.Workspace, "one"), filepath.Join(i.View.Workspace, "two"), filepath.Join(i.View.Workspace, "three")} {
		if err := os.WriteFile(file, []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.a.settings.confirmDelete = true
	h.a.askDelete(p)
	h.tt.Advance(time.Second)
	for _, s := range []string{"The 2 worktrees", "They have 3 uncommitted changes.", "Delete worktrees and changes", "Keep worktrees"} {
		if !h.tt.HasText(s) {
			t.Fatalf("dialog has no %q; texts=%q", s, h.tt.Texts())
		}
	}
}
