package runtime

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
	if err := h.revertable(ctx, t, api.Thread.Live, func(api.Thread) bool { return true }); err != nil {
		return api.Receipt{}, &api.RejectedError{Reason: err.Error()}
	}
	return h.store.Submit(ctx, c, h.clock())
}

// revertable is nil when the files of t's worktree are its own to put
// back now, else why not: writing is whether a thread under t may be at
// work in them, and there whether another thread in them or around them
// may be. The revert checks again once it commits, as a start leased
// before the commit may since have begun; by then a start the restoring
// mark holds back writes nothing.
func (h *Host) revertable(ctx context.Context, t engine.Thread, writing, there func(api.Thread) bool) error {
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
		if writing(th) {
			return errors.New("Stop every thread under this one before reverting its files")
		}
	}
	for _, other := range snap.Threads {
		if other.ID != t.ID && !slices.ContainsFunc(tree, func(x api.Thread) bool { return x.ID == other.ID }) && (under(dir, other.Workspace) || under(other.Workspace, dir)) && there(other) {
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

// mayWrite is whether th's agent may be at work here: a run past its
// start, a start that got past the restoring mark, or an agent not yet
// closed. A start the mark holds back writes nothing.
func (h *Host) mayWrite(th api.Thread) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing[th.ID] || h.passing[th.ID] {
		return true
	}
	if !th.Live() {
		return false
	}
	return th.Run != api.RunStarting || th.Turn == nil || h.runs[th.Turn.Run] != nil
}

// A restoreMark is a worktree whose files reverts put back, and how many
// reverts of it are not done.
type restoreMark struct {
	dir string
	n   int
}

// noteReverting marks the worktree of a revert that e commits as being
// restored, until the revert's effect ends. h.mu is held.
func (h *Host) noteReverting(e api.Event, threads []api.Thread) {
	if e.Type != "thread.reverting" {
		return
	}
	if i := slices.IndexFunc(threads, func(th api.Thread) bool { return th.ID == e.Thread }); i >= 0 {
		h.markRestoring(e.Thread, threads[i].Workspace)
	}
}

// markRestoring counts one more revert of thread's worktree at workspace.
// h.mu is held.
func (h *Host) markRestoring(thread api.ID, workspace string) {
	dir := h.ownsDir(thread, workspace)
	if dir == "" {
		return
	}
	m := h.restoring[thread]
	if m == nil {
		m = &restoreMark{dir: dir}
		h.restoring[thread] = m
	}
	m.n++
}

// revertEnded is a revert of thread's worktree ended, done or skipped:
// once none is left, the starts that waited on it go.
func (h *Host) revertEnded(thread api.ID) {
	h.mu.Lock()
	if m := h.restoring[thread]; m != nil {
		if m.n--; m.n <= 0 {
			delete(h.restoring, thread)
			clear(h.waiting)
		}
	}
	h.mu.Unlock()
	h.poke()
}

// bootReverting marks the worktrees of the reverts a host before this one
// left undone, before any effect is leased, once for each.
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
		h.mu.Lock()
		h.markRestoring(id, t.Workspace)
		h.mu.Unlock()
	}
	return nil
}

// waitsOnRevert is whether e, a start, is in a worktree whose files
// another thread's revert puts back, or around one: it hands its lease
// back, and its thread waits until the revert ends. A start that goes is
// passing until it is tracked, so a revert marked meanwhile sees it.
func (h *Host) waitsOnRevert(e store.Effect) bool {
	t, err := h.store.Thread(h.ctx, e.Thread)
	if err != nil {
		return false
	}
	h.mu.Lock()
	waits := false
	for thread, m := range h.restoring {
		if thread != e.Thread && (under(m.dir, t.Workspace) || under(t.Workspace, m.dir)) {
			waits = true
			h.waiting[e.Thread] = true
			break
		}
	}
	if !waits {
		h.passing[e.Thread] = true
	}
	h.mu.Unlock()
	if !waits {
		return false
	}
	retry(h.drain, "release effect", func(ctx context.Context) error { return h.store.Release(ctx, e.ID) })
	h.poke()
	return true
}

// change is a path whose file differs between two of a store's trees,
// with to's mode and blob, "" where to has none; fresh is a path from has
// none.
type change struct {
	path, mode, blob string
	fresh            bool
}

func (h *Host) revertIn(ctx context.Context, t engine.Thread, b engine.RevertThread) (api.ThreadReverted, error) {
	r := api.ThreadReverted{Revert: b.Revert, To: b.To}
	if h.checkpoints == nil {
		return r, errors.New("this engine keeps no checkpoints")
	}
	if err := h.revertable(ctx, t, h.mayWrite, h.mayWrite); err != nil {
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
		return r, h.keepRecoveries(ctx, store, t.ID, b)
	}
	if err := inTheWay(ctx, store, top, changes); err != nil {
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
	return r, h.keepRecoveries(ctx, store, t.ID, b)
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
			c.mode, c.blob = meta[1], meta[3]
		}
		changes = append(changes, c)
	}
	return changes, nil
}

// inTheWay refuses a revert that would write over a file the recovery
// checkpoint didn't save, as an ignored one, or one written since: a
// path to write, or a folder above one, holding anything the revert
// doesn't delete or hasn't already written, as a replay finds it. A path
// to delete under anything but a folder is refused too, as deleting it
// would reach outside the worktree.
func inTheWay(ctx context.Context, store, top string, changes []change) error {
	deleted, written := map[string]bool{}, map[string]change{}
	for _, c := range changes {
		if c.mode == "" {
			deleted[c.path] = true
		} else {
			written[c.path] = c
		}
	}
	// spare is whether the file at rel is the revert's to change: one it
	// deletes, or one it wrote already, as tree has it.
	spare := func(rel string) (bool, error) {
		if deleted[rel] || slices.ContainsFunc(slices.Collect(maps.Keys(deleted)), func(d string) bool { return strings.EqualFold(d, rel) && sameFile(top, d, rel) }) {
			return true, nil
		}
		c, ok := written[rel]
		if !ok {
			return false, nil
		}
		return isBlob(ctx, store, filepath.Join(top, rel), c)
	}
	for _, c := range changes {
		for dir := filepath.Dir(c.path); dir != "."; dir = filepath.Dir(dir) {
			fi, err := os.Lstat(filepath.Join(top, dir))
			if err != nil || fi.IsDir() {
				continue
			}
			// A file where a folder was is one to delete, or, to a replay,
			// one the revert wrote and so of a path it deleted.
			if ok, err := spare(filepath.ToSlash(dir)); err != nil || !ok {
				return cmp.Or(err, fmt.Errorf("%s is in the way of %s, and wasn't saved before the revert, so nothing changed", filepath.ToSlash(dir), c.path))
			}
		}
		fi, err := os.Lstat(filepath.Join(top, c.path))
		if err != nil || (c.mode == "" && !fi.IsDir()) {
			continue
		}
		if !fi.IsDir() {
			if !c.fresh {
				continue
			}
			if ok, err := spare(c.path); err != nil || !ok {
				return cmp.Or(err, fmt.Errorf("%s is in the way: it wasn't saved before the revert, so nothing changed", c.path))
			}
			continue
		}
		err = filepath.WalkDir(filepath.Join(top, c.path), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(top, p)
			if ok, err := spare(filepath.ToSlash(rel)); err != nil || !ok {
				return cmp.Or(err, fmt.Errorf("%s is in the way of %s, and wasn't saved before the revert, so nothing changed", filepath.ToSlash(rel), c.path))
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// sameFile is whether a and b, two names in top, are one file, as two
// names that differ only in case are on a disk that ignores case.
func sameFile(top, a, b string) bool {
	fa, errA := os.Lstat(filepath.Join(top, a))
	fb, errB := os.Lstat(filepath.Join(top, b))
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// isBlob is whether the file at path is c's: a symlink for a symlink, an
// executable for an executable, and the same blob, hashed with no filter.
func isBlob(ctx context.Context, store, path string, c change) (bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	link := fi.Mode()&os.ModeSymlink != 0
	switch {
	case link != (c.mode == "120000"):
		return false, nil
	case !link && (!fi.Mode().IsRegular() || (fi.Mode().Perm()&0o100 != 0) != (c.mode == "100755")):
		return false, nil
	}
	var hash string
	if link {
		target, err := os.Readlink(path)
		if err != nil {
			return false, err
		}
		hash, err = storeGit(ctx, store, "", "", []byte(target), "hash-object", "--no-filters", "--stdin")
	} else {
		hash, err = storeGit(ctx, store, "", "", nil, "hash-object", "--no-filters", "--", path)
	}
	return err == nil && hash == c.blob, err
}

// restore puts the files of changes in top as tree holds them: it writes
// tree's into a folder of the store's, then deletes those tree hasn't got
// and moves each of the rest into place. Doing it again does the same.
// done is the paths it changed, all of them unless it failed.
func restore(ctx context.Context, store, top string, revert api.ID, tree string, changes []change) (done []string, err error) {
	staged := filepath.Join(store, "restore-"+revert.String())
	index := staged + ".index"
	clean := func() {
		os.RemoveAll(staged)
		os.Remove(index)
		os.Remove(index + ".lock")
	}
	clean()
	defer clean()
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
		if err := remove(top, c.path); err != nil {
			return done, fmt.Errorf("%s couldn't be deleted: %w", c.path, err)
		}
		done = append(done, c.path)
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

// remove deletes the file at rel in top. It is gone already when a file
// stands where a folder above it was; a folder there is one a replayed
// revert wrote, its files the revert's, as inTheWay checked.
func remove(top, rel string) error {
	for dir := filepath.Dir(rel); dir != "."; dir = filepath.Dir(dir) {
		if fi, err := os.Lstat(filepath.Join(top, dir)); err == nil && !fi.IsDir() {
			return nil
		}
	}
	path := filepath.Join(top, rel)
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case fi.IsDir():
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

// moveInto moves from over to. Where it can't, as from is on another
// disk, it copies from beside to first and then moves the copy over, so
// that to is either as it was or from, never half written.
func moveInto(from, to string) error {
	if os.Rename(from, to) == nil {
		return nil
	}
	fi, err := os.Lstat(from)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(to), ".glitched-restore-*")
	if err != nil {
		return err
	}
	beside := f.Name()
	if fi.Mode()&os.ModeSymlink != 0 {
		f.Close()
		os.Remove(beside)
		target, err := os.Readlink(from)
		if err == nil {
			err = os.Symlink(target, beside)
		}
		if err != nil {
			return err
		}
	} else if err := copyInto(from, f, fi.Mode().Perm()); err != nil {
		os.Remove(beside)
		return err
	}
	if err := os.Rename(beside, to); err != nil {
		os.Remove(beside)
		return err
	}
	return nil
}

// copyInto copies from into out, which it closes, with perm.
func copyInto(from string, out *os.File, perm os.FileMode) error {
	in, err := os.Open(from)
	if err == nil {
		defer in.Close()
		_, err = io.Copy(out, in)
	}
	if err == nil {
		err = out.Chmod(perm)
	}
	return errors.Join(err, out.Close())
}

// keepRecoveries deletes thread's recovery checkpoints but the newest
// keptRecoveries, the one b took, and the one it put back, so that a
// replay finds them.
func (h *Host) keepRecoveries(ctx context.Context, store string, thread api.ID, b engine.RevertThread) error {
	unlock, err := h.checkpoints.lock(ctx, store)
	if err != nil {
		return err
	}
	defer unlock()
	listed, err := storeGit(ctx, store, "", "", nil, "for-each-ref", "--sort=refname", "--format=%(refname)", checkpointRefs(thread)+"recovery/")
	if err != nil || listed == "" {
		return err
	}
	took := checkpointRef(thread, engine.CaptureCheckpoint{Run: b.Revert, Phase: api.CheckpointRecovery})
	to := checkpointRef(thread, engine.CaptureCheckpoint{Run: b.To.ID, Phase: b.To.Phase})
	refs := strings.Split(listed, "\n")
	var update string
	for _, ref := range refs[:max(0, len(refs)-keptRecoveries)] {
		if ref != took && ref != to {
			update += "delete " + ref + "\n"
		}
	}
	if update == "" {
		return nil
	}
	_, err = storeGit(ctx, store, "", "", []byte(update), "update-ref", "--stdin")
	return err
}
