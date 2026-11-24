//go:build !windows

package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// A record a crash cut short is cut off the journal before the next one is
// appended, so the records after it still count.
func TestATornJournalRecordKeepsTheNextOne(t *testing.T) {
	ctx := context.Background()
	store := filepath.Join(t.TempDir(), "store.git")
	must(initStore(ctx, store))
	h := &Host{}
	WithCheckpoints(t.TempDir())(h)
	recovery, to := commitOf(t, ctx, store, map[string]entry{}), commitOf(t, ctx, store, map[string]entry{"a": file("A"), "b": file("B"), "c": file("C")})
	changes, err := diffTrees(ctx, store, recovery, to)
	must(err)
	top, revert := t.TempDir(), api.NewID()
	plan, err := h.markApplying(ctx, store, top, api.NewID(), revert, recovery, changes)
	must(err)
	cut, cancel := context.WithCancel(ctx)
	records := 0
	defer SetCheckpointHook(func(_ context.Context, step string) {
		if step == "journaling" {
			if records++; records == 2 {
				cancel()
			}
		}
	})()
	if err := restore(cut, store, top, revert, to, plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("the revert, cut as it records b: %v", err)
	}
	f, err := os.OpenFile(journalOf(store, revert), os.O_APPEND|os.O_WRONLY, 0)
	must(err)
	_, err = f.WriteString("wro")
	must(errors.Join(err, f.Close()))
	must(restore(ctx, store, top, revert, to, plan))
	err = undo(ctx, store, top, revert, recovery, plan)
	left := map[string]bool{}
	for _, p := range []string{"a", "b", "c"} {
		_, e := os.Lstat(filepath.Join(top, p))
		left[p] = e == nil
	}
	if left["a"] || !left["b"] || left["c"] || err == nil || !strings.HasPrefix(err.Error(), "b changed") {
		t.Fatalf("undo left %v and said %v; want only b, which the cut left unrecorded, kept and named", left, err)
	}
}

// revertedOnce is a thread whose revert of edit and add ran to the end in
// its own worktree, and the refused replay that puts the files back.
func revertedOnce(t *testing.T) (h *Host, store, top string, thread engine.Thread, revert api.ID, replay func() error) {
	ctx := context.Background()
	root := t.TempDir()
	store = filepath.Join(root, "store.git")
	must(initStore(ctx, store))
	h = &Host{worktrees: filepath.Join(root, "w")}
	WithCheckpoints(root)(h)
	recovered := map[string]entry{"edit": file("R")}
	recovery, to := commitOf(t, ctx, store, recovered), commitOf(t, ctx, store, map[string]entry{"edit": file("T"), "add": file("X")})
	changes, err := diffTrees(ctx, store, recovery, to)
	must(err)
	thread = engine.Thread{ID: api.NewID()}
	thread.Workspace = filepath.Join(h.worktrees, "repo", strings.ToLower(thread.ID.String()))
	lay(t, thread.Workspace, recovered)
	revert = api.NewID()
	_, err = storeGit(ctx, store, "", "", nil, "update-ref", checkpointRef(thread.ID, engine.CaptureCheckpoint{Run: revert, Phase: api.CheckpointRecovery}), recovery)
	must(err)
	plan, err := h.markApplying(ctx, store, thread.Workspace, thread.ID, revert, recovery, changes)
	must(err)
	must(restore(ctx, store, thread.Workspace, revert, to, plan))
	return h, store, thread.Workspace, thread, revert, func() error {
		return h.unrevert(ctx, thread, engine.RevertThread{Revert: revert}, store, &api.ThreadReverted{}, errors.New("the provider refused"))
	}
}

// A journal whose plan is gone says the files it changed couldn't be put
// back, by name, and never that nothing changed.
func TestAJournalWithNoPlanNamesTheFilesItChanged(t *testing.T) {
	_, store, top, thread, revert, replay := revertedOnce(t)
	_, err := storeGit(context.Background(), store, "", "", nil, "update-ref", "-d", applyingRef(thread.ID, revert))
	must(err)
	err = replay()
	edit, _ := os.ReadFile(filepath.Join(top, "edit"))
	add, _ := os.ReadFile(filepath.Join(top, "add"))
	if string(edit) != "T" || string(add) != "X" || err == nil || !strings.HasSuffix(err.Error(), "couldn't be put back, so they were left as they are: add, edit") || strings.Contains(err.Error(), "nothing changed") {
		t.Fatalf("edit=%q add=%q, and the replay said %v", edit, add, err)
	}
}

// A plan that doesn't follow the revert's own recovery checkpoint puts
// nothing back.
func TestAPlanNotAfterTheRevertsRecoveryPutsNothingBack(t *testing.T) {
	h, store, top, thread, revert, replay := revertedOnce(t)
	ctx := context.Background()
	other := commitOf(t, ctx, store, map[string]entry{"edit": file("UNRELATED")})
	changes, err := diffTrees(ctx, store, other, commitOf(t, ctx, store, map[string]entry{"edit": file("T"), "add": file("X")}))
	must(err)
	elsewhere := api.NewID()
	_, err = h.markApplying(ctx, store, t.TempDir(), thread.ID, elsewhere, other, changes)
	must(err)
	commit, err := storeGit(ctx, store, "", "", nil, "rev-parse", applyingRef(thread.ID, elsewhere))
	must(err)
	_, err = storeGit(ctx, store, "", "", nil, "update-ref", applyingRef(thread.ID, revert), commit)
	must(err)
	err = replay()
	edit, _ := os.ReadFile(filepath.Join(top, "edit"))
	if string(edit) != "T" || err == nil || !strings.Contains(err.Error(), "not this revert's recovery checkpoint") {
		t.Fatalf("edit=%q, and the replay said %v", edit, err)
	}
}
