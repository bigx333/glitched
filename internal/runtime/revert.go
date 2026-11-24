package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/store"
)

// keptRecoveries is how many recovery checkpoints a thread keeps: the
// newest, each the files as they were before a revert.
var keptRecoveries = 10

// revertInWorktree refuses, before it commits, a revert whose files
// someone else may be writing: a worktree that isn't the thread's own, a
// thread under it at work, or another thread whose folder is in the
// worktree or around it. The engine refuses the rest.
func (h *Host) revertInWorktree(ctx context.Context, c api.Command) (api.Receipt, error) {
	t, err := h.store.Thread(ctx, c.Target)
	if err != nil {
		return api.Receipt{}, err
	}
	if !t.Exists() || t.Deleted || !t.Parent.IsZero() || t.Branch == "" {
		return h.store.Submit(ctx, c, h.clock())
	}
	if err := h.revertable(ctx, t); err != nil {
		return api.Receipt{}, &api.RejectedError{Reason: err.Error()}
	}
	return h.store.Submit(ctx, c, h.clock())
}

// revertable is nil when the files of t's worktree are its own to put
// back now, else why not. The revert checks again once it commits, as a
// start leased before the commit may since have begun.
func (h *Host) revertable(ctx context.Context, t engine.Thread) error {
	dir := h.ownsDir(t.ID, t.Workspace)
	if dir == "" {
		return errors.New("Revert needs this thread to run in its own worktree")
	}
	common, err := git(ctx, t.Project, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("its project's repository at %s can't be read", t.Project)
	}
	if !ours(ctx, dir, common, t.ID, t.Branch) {
		return fmt.Errorf("%s is no longer the thread's worktree on %s", dir, t.Branch)
	}
	snap, err := h.store.Read(ctx, api.Query{})
	if err != nil {
		return err
	}
	tree := below(snap.Threads, t.ID)
	for _, th := range tree {
		if th.Live() {
			return errors.New("Stop every thread under this one before reverting its files")
		}
	}
	for _, other := range snap.Threads {
		if other.ID != t.ID && !slices.ContainsFunc(tree, func(x api.Thread) bool { return x.ID == other.ID }) && (under(dir, other.Workspace) || under(other.Workspace, dir)) {
			return fmt.Errorf("the thread %q runs in %s too", other.Title, dir)
		}
	}
	return nil
}

// revert puts t's files back as b asks, and reports what it did. ok is
// false when the host closed mid-revert, which leaves the effect to the
// next host.
func (h *Host) revert(t engine.Thread, b engine.RevertThread) (o engine.Observation, ok bool) {
	r, err := h.revertIn(h.ctx, t, b)
	if h.ctx.Err() != nil {
		return nil, false
	}
	if err != nil {
		r.Status, r.Error = api.RevertFailed, err.Error()
	}
	return engine.Reverted{Revert: r}, true
}

// noteReverting marks the worktree of a revert that e commits as being
// restored, and clears the mark when the revert ends, for the starts that
// waited on it to go. h.mu is held.
func (h *Host) noteReverting(e api.Event, threads []api.Thread) {
	switch e.Type {
	case "thread.reverting":
		if i := slices.IndexFunc(threads, func(th api.Thread) bool { return th.ID == e.Thread }); i >= 0 {
			if dir := h.ownsDir(e.Thread, threads[i].Workspace); dir != "" {
				h.restoring[dir] = e.Thread
			}
		}
	case "thread.reverted", threadDeleted:
		for dir, thread := range h.restoring {
			if thread == e.Thread {
				delete(h.restoring, dir)
			}
		}
		clear(h.waiting)
	}
}

// bootReverting marks the worktrees of the reverts a host before this one
// left undone, before any effect is leased.
func (h *Host) bootReverting(ctx context.Context) error {
	threads, err := h.store.Reverting(ctx)
	if err != nil {
		return err
	}
	for _, id := range threads {
		t, err := h.store.Thread(ctx, id)
		if err != nil {
			return err
		}
		if dir := h.ownsDir(id, t.Workspace); dir != "" {
			h.mu.Lock()
			h.restoring[dir] = id
			h.mu.Unlock()
		}
	}
	return nil
}

// waitsOnRevert is whether e, a start, is in a worktree whose files
// another thread's revert puts back: it hands its lease back, and its
// thread waits until the revert ends.
func (h *Host) waitsOnRevert(e store.Effect) bool {
	h.mu.Lock()
	marked := maps.Clone(h.restoring)
	h.mu.Unlock()
	if len(marked) == 0 {
		return false
	}
	t, err := h.store.Thread(h.ctx, e.Thread)
	if err != nil {
		return false
	}
	for dir, thread := range marked {
		if thread != e.Thread && under(dir, t.Workspace) {
			h.mu.Lock()
			h.waiting[e.Thread] = true
			h.mu.Unlock()
			retry(h.drain, "release effect", func(ctx context.Context) error { return h.store.Release(ctx, e.ID) })
			return true
		}
	}
	return false
}

// change is a path whose file differs between two of a store's trees,
// with to's mode, "" where to has none; fresh is a path from has none.
type change struct {
	path, mode string
	fresh      bool
}

func (h *Host) revertIn(ctx context.Context, t engine.Thread, b engine.RevertThread) (api.ThreadReverted, error) {
	r := api.ThreadReverted{Revert: b.Revert, To: b.To}
	if h.checkpoints == nil {
		return r, errors.New("this engine keeps no checkpoints")
	}
	if err := h.revertable(ctx, t); err != nil {
		return r, err
	}
	top := h.ownsDir(t.ID, t.Workspace)
	target, store, err := h.checkpointAt(ctx, t.ID, b.To)
	if err != nil {
		return r, err
	}
	// The files as they are, first, so that the revert can be undone, and
	// put back should it fail. A replay finds the one it took: a tree
	// half put back must never become the recovery.
	recovery, err := h.captureIn(ctx, t, engine.CaptureCheckpoint{Run: b.Revert, Phase: api.CheckpointRecovery})
	switch {
	case err != nil:
		return r, fmt.Errorf("the files as they are couldn't be saved first, so nothing changed: %w", err)
	case recovery.Commit == "" || !holdsCommit(ctx, store, recovery.Commit):
		return r, errors.New("the files as they are were saved in another repository's store, so nothing changed")
	}
	r.Recovery = recovery.Commit
	checkpointHook(ctx, "recovered")
	changes, err := diffTrees(ctx, store, recovery.Commit, target)
	if err != nil {
		return r, err
	}
	if len(changes) == 0 {
		r.Status = api.RevertUnchanged
		return r, h.keepRecoveries(ctx, store, t.ID, b.Revert)
	}
	if err := inTheWay(top, changes); err != nil {
		return r, err
	}
	if done, err := restore(ctx, store, top, b.Revert, target, changes); err != nil {
		if ctx.Err() != nil {
			return r, err
		}
		back, again := diffTrees(ctx, store, target, recovery.Commit)
		back = slices.DeleteFunc(back, func(c change) bool { return !slices.Contains(done, c.path) })
		if again == nil {
			_, again = restore(ctx, store, top, b.Revert, recovery.Commit, back)
		}
		if again != nil {
			return r, fmt.Errorf("%w; putting the files back as they were failed too: %v", err, again)
		}
		return r, fmt.Errorf("%w; the files were put back as they were", err)
	}
	checkpointHook(ctx, "applied")
	r.Status, r.Changed = api.RevertRestored, len(changes)
	return r, h.keepRecoveries(ctx, store, t.ID, b.Revert)
}

// checkpointAt is the commit of thread's checkpoint to, and the store
// that holds it.
func (h *Host) checkpointAt(ctx context.Context, thread api.ID, to api.CheckpointRef) (commit, store string, err error) {
	ref := checkpointRef(thread, engine.CaptureCheckpoint{Run: to.ID, Phase: to.Phase})
	for _, store := range h.checkpoints.stores() {
		if commit, err := storeGit(ctx, store, "", "", nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return commit, store, nil
		}
	}
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	return "", "", errors.New("no checkpoint of the files was saved there")
}

func holdsCommit(ctx context.Context, store, commit string) bool {
	_, err := storeGit(ctx, store, "", "", nil, "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

// diffTrees is every path whose file differs from from's in to.
func diffTrees(ctx context.Context, store, from, to string) ([]change, error) {
	out, err := storeGit(ctx, store, "", "", nil, "diff-tree", "-r", "-z", "--no-renames", from, to)
	if err != nil {
		return nil, err
	}
	var changes []change
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(meta) != 5 {
			return nil, fmt.Errorf("git diff-tree said %q", fields[i])
		}
		c := change{path: fields[i+1], fresh: meta[0] == "000000"}
		if meta[1] != "000000" {
			c.mode = meta[1]
		}
		changes = append(changes, c)
	}
	return changes, nil
}

// inTheWay refuses a revert that would write over a file the recovery
// checkpoint didn't save, as an ignored one, or one written since: a
// path to write, or a folder above one, holding anything the revert
// doesn't delete.
func inTheWay(top string, changes []change) error {
	deleted := map[string]bool{}
	for _, c := range changes {
		if c.mode == "" {
			deleted[c.path] = true
		}
	}
	for _, c := range changes {
		if c.mode == "" {
			continue
		}
		for dir := filepath.Dir(c.path); dir != "."; dir = filepath.Dir(dir) {
			if fi, err := os.Lstat(filepath.Join(top, dir)); err == nil && !fi.IsDir() && !deleted[filepath.ToSlash(dir)] {
				return fmt.Errorf("%s is in the way of %s, and wasn't saved before the revert, so nothing changed", filepath.ToSlash(dir), c.path)
			}
		}
		fi, err := os.Lstat(filepath.Join(top, c.path))
		if err != nil || !c.fresh {
			continue
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s is in the way: it wasn't saved before the revert, so nothing changed", c.path)
		}
		err = filepath.WalkDir(filepath.Join(top, c.path), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(top, p)
			if !deleted[filepath.ToSlash(rel)] {
				return fmt.Errorf("%s is in the way of %s, and wasn't saved before the revert, so nothing changed", filepath.ToSlash(rel), c.path)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// restore puts the files of changes in top as tree holds them: it writes
// tree's into a folder of the store's, then deletes those tree hasn't got
// and moves each of the rest into place. Doing it again does the same.
// done is the paths it changed, all of them unless it failed.
func restore(ctx context.Context, store, top string, revert api.ID, tree string, changes []change) (done []string, err error) {
	staged := filepath.Join(store, "restore-"+revert.String())
	index := staged + ".index"
	os.RemoveAll(staged)
	os.Remove(index)
	defer os.RemoveAll(staged)
	defer os.Remove(index)
	var paths []byte
	for _, c := range changes {
		if c.mode != "" {
			paths = append(append(paths, c.path...), 0)
		}
	}
	if err := os.MkdirAll(staged, 0o700); err != nil {
		return nil, err
	}
	if _, err := storeGit(ctx, store, staged, index, nil, "read-tree", tree); err != nil {
		return nil, err
	}
	if _, err := storeGit(ctx, store, staged, index, paths, "checkout-index", "-z", "--stdin", "-f"); err != nil {
		return nil, err
	}
	for _, c := range changes {
		if c.mode != "" {
			continue
		}
		done = append(done, c.path)
		if err := os.Remove(filepath.Join(top, c.path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return done, fmt.Errorf("%s couldn't be deleted: %w", c.path, err)
		}
		dir := filepath.Dir(filepath.Join(top, c.path))
		for dir != top && os.Remove(dir) == nil {
			dir = filepath.Dir(dir)
		}
	}
	for _, c := range changes {
		if c.mode == "" {
			continue
		}
		checkpointHook(ctx, "applying")
		if err := ctx.Err(); err != nil {
			return done, err
		}
		to := filepath.Join(top, c.path)
		if fi, err := os.Lstat(to); err == nil && fi.IsDir() {
			done = append(done, c.path)
			if err := os.RemoveAll(to); err != nil {
				return done, fmt.Errorf("%s couldn't be put back: %w", c.path, err)
			}
		}
		err := os.MkdirAll(filepath.Dir(to), 0o755)
		if err == nil {
			err = moveInto(filepath.Join(staged, c.path), to)
		}
		if err != nil {
			return done, fmt.Errorf("%s couldn't be put back: %w", c.path, err)
		}
		if !slices.Contains(done, c.path) {
			done = append(done, c.path)
		}
	}
	return done, nil
}

// moveInto moves from over to, copying when they are on two disks.
func moveInto(from, to string) error {
	err := os.Rename(from, to)
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	fi, err := os.Lstat(from)
	if err != nil {
		return err
	}
	os.Remove(to)
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(from)
		if err != nil {
			return err
		}
		return os.Symlink(target, to)
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// keepRecoveries deletes thread's recovery checkpoints but the newest
// keptRecoveries, and the one revert took.
func (h *Host) keepRecoveries(ctx context.Context, store string, thread, revert api.ID) error {
	unlock, err := h.checkpoints.lock(ctx, store)
	if err != nil {
		return err
	}
	defer unlock()
	listed, err := storeGit(ctx, store, "", "", nil, "for-each-ref", "--sort=refname", "--format=%(refname)", checkpointRefs(thread)+"recovery/")
	if err != nil || listed == "" {
		return err
	}
	keep := checkpointRef(thread, engine.CaptureCheckpoint{Run: revert, Phase: api.CheckpointRecovery})
	refs := strings.Split(listed, "\n")
	var update string
	for _, ref := range refs[:max(0, len(refs)-keptRecoveries)] {
		if ref != keep {
			update += "delete " + ref + "\n"
		}
	}
	if update == "" {
		return nil
	}
	_, err = storeGit(ctx, store, "", "", []byte(update), "update-ref", "--stdin")
	return err
}
