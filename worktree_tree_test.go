package main

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// Deleting a thread whose child runs in a worktree of its own asks what
// becomes of that worktree, though the thread runs in its project's
// folder: Delete worktree removes it, as the engine removes a tree's.
// DELETESHOT=<dir> writes the alert there.
func TestDeletingATreeAsksAboutItsChildsWorktree(t *testing.T) {
	ws := gitWorkspace(t)
	dir := tempDir(t)
	ad := fake.New(fake.Turn(true))
	d := engine(t, daemon.Config{Dir: dir}, ad)
	parentID := seed(t, d, ws, "Parent")
	h := start(t, dir)
	parent := h.thread("Parent")
	ep := d.Endpoint()
	ad.Spawns(ep, "split", fake.Child{Title: "Review", Task: "work", Worktree: true})
	run := api.NewID()
	if _, err := ep.Submit(context.Background(), api.Command{ID: run, Target: parentID, Body: api.MessageDispatch{Text: "split", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	childID := fake.ChildOf(run, 0)
	h.until("the child at work in its worktree", func() bool {
		kid, ok := h.a.find(childID)
		p, _ := h.a.find(parentID)
		return ok && kid.View.Branch != "" && kid.View.Run == api.RunWaiting && p.View.Run == api.RunIdle && p.View.LastTurn != nil
	})
	kid, _ := h.a.find(childID)
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: childID, Body: api.TurnInterrupt{}}); err != nil {
		t.Fatal(err)
	}
	h.until("the child stopped", func() bool { k, _ := h.a.find(childID); return k.View.Run == api.RunIdle })

	h.a.settings.confirmDelete = true
	h.a.askDelete(parent)
	h.tt.Advance(time.Second)
	if out := os.Getenv("DELETESHOT"); out != "" {
		f, err := os.Create(filepath.Join(out, "delete-tree-with-worktree-child.png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, h.tt.Image())
		f.Close()
	}
	for _, s := range []string{"One thread under it has a worktree on " + kid.View.Branch + ", which stays unless you delete it too.", "Keep worktree"} {
		if !h.tt.HasText(s) {
			t.Fatalf("the delete alert has no %q", s)
		}
	}
	h.click("Delete worktree")
	h.until("the tree's delete", func() bool { _, ok := h.a.find(parentID); return !ok })
	h.until("the child's worktree's removal", func() bool { _, err := os.Stat(kid.View.Workspace); return err != nil })
	if _, err := os.Stat(ws); err != nil {
		t.Errorf("the delete took the parent's folder: %v", err)
	}
}
