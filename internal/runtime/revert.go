package runtime

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
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
	if !t.Exists() || t.Deleted || !t.Parent.IsZero() || t.Branch == "" || !c.Body.(api.ThreadRevert).Files {
		return h.store.Submit(ctx, c, h.clock())
	}
	if err := h.revertable(ctx, t, api.Thread.Live, func(api.Thread) bool { return true }); err != nil {
		return api.Receipt{}, &api.RejectedError{Reason: err.Error()}
	}
	store, target, release, err := h.reserve(ctx, t, c)
	if err != nil {
		return api.Receipt{}, err
	}
	defer release()
	var receipt api.Receipt
	saved, err := h.confirmFiles(ctx, t, c, store, target)
	if err != nil {
		err = &api.RejectedError{Reason: err.Error()}
	} else {
		checkpointHook(ctx, "saved")
		receipt, err = h.store.Submit(ctx, c, h.clock())
	}
	if saved && err != nil {
		if committed, again := h.store.Committed(h.drain, c.ID); again == nil && !committed {
			if unlock, again := h.checkpoints.lock(h.drain, store); again == nil {
				forgetConfirmation(h.drain, store, t.ID, c.ID)
				unlock()
			}
		}
	}
	return receipt, err
}

// reserve registers c among the confirmations, and finds the store and the
// commit of its target, "" where none is saved, under that store's lock,
// as the sweep reads the confirmations: a sweep either saw c, and keeps
// what c is to put back, or had ended when c looked, and c finds what it
// left. A recovery and a plan an earlier attempt at c saved are c's only
// once c committed; else c drops them, to save the files as they are now.
// release forgets c.
func (h *Host) reserve(ctx context.Context, t engine.Thread, c api.Command) (store, target string, release func(), err error) {
	checkpointHook(ctx, "reserving")
	to := c.Body.(api.ThreadRevert).To
	for {
		unlock := func() {}
		if h.checkpoints != nil {
			if _, store, err = h.checkpointAt(ctx, t.ID, to); err != nil && ctx.Err() != nil {
				return "", "", nil, ctx.Err()
			}
		}
		if store != "" {
			if unlock, err = h.checkpoints.lock(ctx, store); err != nil {
				return "", "", nil, err
			}
		}
		h.mu.Lock()
		other := h.confirming[c.ID].done
		if other == nil {
			done := make(chan struct{})
			h.confirming[c.ID] = confirmation{thread: t.ID, to: to, done: done}
			release = func() {
				h.mu.Lock()
				delete(h.confirming, c.ID)
				close(done)
				h.mu.Unlock()
			}
		}
		h.mu.Unlock()
		if other == nil {
			defer unlock()
			break
		}
		unlock()
		select {
		case <-other:
		case <-ctx.Done():
			return "", "", nil, ctx.Err()
		}
	}
	if store == "" {
		return "", "", release, nil
	}
	target, _ = storeGit(ctx, store, "", "", nil, "rev-parse", "--verify", "--quiet", checkpointRef(t.ID, engine.CaptureCheckpoint{Run: to.ID, Phase: to.Phase})+"^{commit}")
	committed, err := h.store.Committed(ctx, c.ID)
	switch {
	case err != nil:
		release()
		return "", "", nil, err
	case committed || target == "":
		return store, "", release, nil
	}
	if err := forgetConfirmation(ctx, store, t.ID, c.ID); err != nil {
		release()
		return "", "", nil, err
	}
	return store, target, release, nil
}

// forgetConfirmation deletes the recovery and the plan a confirmation of
// revert saved in store, whose lock the caller holds.
func forgetConfirmation(ctx context.Context, store string, thread, revert api.ID) error {
	update := "delete " + checkpointRef(thread, engine.CaptureCheckpoint{Run: revert, Phase: api.CheckpointRecovery}) + "\ndelete " + applyingRef(thread, revert) + "\n"
	_, err := storeGit(ctx, store, "", "", []byte(update), "update-ref", "--stdin")
	return err
}

// confirmFiles saves the files of t's worktree as they are when the user
// confirms c, before c commits, in store, which reserve found holds
// target, c's target: the revert's recovery checkpoint, and the plan of
// the paths it is to write. The revert, and any replay of it, goes by
// them, never by the files as they are by then. It saves nothing where
// reserve found no target, and says whether it saved the recovery there.
func (h *Host) confirmFiles(ctx context.Context, t engine.Thread, c api.Command, store, target string) (bool, error) {
	if target == "" {
		return false, nil
	}
	saved, err := h.captureIn(ctx, t, engine.CaptureCheckpoint{Run: c.ID, Phase: api.CheckpointRecovery})
	switch {
	case err != nil:
		return false, fmt.Errorf("the files as they are couldn't be saved first, so nothing changed: %w", err)
	case saved.Commit == "" || !holdsCommit(ctx, store, saved.Commit):
		return false, errors.New("the files as they are were saved in another repository's store, so nothing changed")
	}
	changes, err := diffTrees(ctx, store, saved.Commit, target)
	if err != nil {
		return true, err
	}
	top := h.ownsDir(t.ID, t.Workspace)
	if len(changes) == 0 || inTheWay(ctx, store, top, changes) != nil {
		return true, nil
	}
	_, err = h.markApplying(ctx, store, top, t.ID, c.ID, saved.Commit, changes)
	return true, err
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
		if b.Conversation {
			r.Conversation, r.Session, r.From, r.At = api.RewindFailed, "", "", ""
		}
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
	if b, _ := e.Decode(); holdsFiles(b) {
		return
	}
	if i := slices.IndexFunc(threads, func(th api.Thread) bool { return th.ID == e.Thread }); i >= 0 {
		h.markRestoring(e.Thread, threads[i].Workspace)
	}
}

// holdsFiles is whether b is a revert that leaves the files as they are.
func holdsFiles(b api.EventBody) bool {
	r, ok := b.(api.ThreadReverting)
	return ok && r.KeepFiles
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
// with from's mode and blob as was and wasBlob, and to's as mode and blob,
// "" where the tree has none.
type change struct {
	path, was, wasBlob, mode, blob string
}

// revertIn rewinds the conversation first, as T3 and Zeron do, once the
// files are known to be the thread's to put back and as the user
// confirmed them, so that a rewind that
// fails leaves them as they are: a replay that finds the plan an earlier
// attempt left, and so may have written some files already, puts the
// paths it names back as they were. A revert of the files that fails
// after the rewind leaves it unused, as revert reports it: a rewind
// changes nothing until the revert commits.
func (h *Host) revertIn(ctx context.Context, t engine.Thread, b engine.RevertThread) (api.ThreadReverted, error) {
	r := api.ThreadReverted{Revert: b.Revert, To: b.To}
	var top, target, store, recovery string
	if !b.KeepFiles {
		if h.checkpoints == nil {
			return r, errors.New("this engine keeps no checkpoints")
		}
		if err := h.revertable(ctx, t, h.mayWrite, h.mayWrite); err != nil {
			return r, err
		}
		top = h.ownsDir(t.ID, t.Workspace)
		var err error
		if target, store, err = h.checkpointAt(ctx, t.ID, b.To); err != nil {
			return r, err
		}
		if recovery, err = confirmed(ctx, store, top, t.ID, b.Revert, target); err != nil {
			return r, err
		}
	}
	if b.Conversation {
		if err := h.rewind(ctx, t, b, &r); err != nil {
			if b.KeepFiles {
				return r, fmt.Errorf("%w, so nothing changed", err)
			}
			return r, h.unrevert(ctx, t, b, store, &r, err)
		}
		checkpointHook(ctx, "rewound")
	}
	if b.KeepFiles {
		r.Status = api.RevertKept
		return r, nil
	}
	r.Recovery = recovery
	checkpointHook(ctx, "recovered")
	changed, err := h.putBack(ctx, store, top, t.ID, b.Revert, recovery, target)
	if err != nil {
		return r, err
	}
	r.Status, r.Changed = api.RevertRestored, changed
	if changed == 0 {
		r.Status = api.RevertUnchanged
	}
	return r, h.keepRecoveries(ctx, store, t.ID, b)
}

// putBack puts top's files as target has them, from recovery, the files
// the user confirmed revert with, and reports how many paths it changed.
// It writes only the paths of the revert's plan: a path that held the
// target's already when the user confirmed, as an ignored file can, it
// leaves as it is since. A revert that fails puts back what it wrote, as
// undo does.
func (h *Host) putBack(ctx context.Context, store, top string, thread, revert api.ID, recovery, target string) (int, error) {
	changes, err := diffTrees(ctx, store, recovery, target)
	if err != nil || len(changes) == 0 {
		return 0, err
	}
	plan, err := h.markApplying(ctx, store, top, thread, revert, recovery, changes)
	if err != nil {
		return 0, err
	}
	checkpointHook(ctx, "planned")
	if err := restore(ctx, store, top, revert, target, plan); err != nil {
		if ctx.Err() != nil {
			return 0, err
		}
		if again := undo(ctx, store, top, revert, recovery, plan); again != nil {
			return 0, fmt.Errorf("%w; putting the files back as they were failed too: %v", err, again)
		}
		return 0, fmt.Errorf("%w; the files were put back as they were", err)
	}
	checkpointHook(ctx, "applied")
	return len(plan), nil
}

// confirmed is the recovery checkpoint the user confirmed revert to
// target with, once top's files are as they were then, but for those the
// revert's journal says it wrote since and those that hold the target's
// already. A path the plan left out as the target's already when the user
// confirmed, as an ignored file can be, must still be. Where they aren't, someone changed them after the user
// confirmed, and the revert must not count those changes as what it puts
// back: it puts back what an earlier attempt wrote, leaves the rest as it
// is, and fails naming what changed.
func confirmed(ctx context.Context, store, top string, thread, revert api.ID, target string) (string, error) {
	recovery, err := storeGit(ctx, store, "", "", nil, "rev-parse", "--verify", "--quiet", checkpointRef(thread, engine.CaptureCheckpoint{Run: revert, Phase: api.CheckpointRecovery})+"^{commit}")
	if err != nil {
		return "", cmp.Or(ctx.Err(), errors.New("the files weren't saved when the revert was confirmed, so nothing changed"))
	}
	found, plan, err := readPlan(ctx, store, applyingRef(thread, revert))
	switch {
	case err != nil:
		return "", err
	case found != "" && found != recovery:
		return "", fmt.Errorf("the revert's plan follows %s, not the recovery checkpoint %s saved when it was confirmed, so nothing changed", found, recovery)
	}
	changes, err := diffTrees(ctx, store, recovery, target)
	if err != nil {
		return "", err
	}
	changed, err := changedSince(ctx, store, top, revert, recovery, changes)
	for _, c := range changes {
		if found != "" && !slices.ContainsFunc(plan, func(p change) bool { return p.path == c.path }) && !atTarget(ctx, store, top, c) && !slices.Contains(changed, c.path) {
			changed = append(changed, c.path)
		}
	}
	slices.Sort(changed)
	var why string
	switch {
	case ctx.Err() != nil:
		return "", ctx.Err()
	case err != nil:
		why = "the files couldn't be compared with those saved when the revert was confirmed: " + err.Error()
	case len(changed) > 0:
		why = strings.Join(changed, ", ") + " changed since the revert was confirmed"
	default:
		if err := inTheWay(ctx, store, top, changes); err != nil {
			why = strings.TrimSuffix(err.Error(), ", so nothing changed")
		}
	}
	if why == "" {
		return recovery, nil
	}
	last, err := readJournal(journalOf(store, revert))
	switch {
	case err != nil:
		return "", refusal("%s; the journal of an earlier attempt couldn't be read, so the files it restored were left as they are: %v", why, err)
	case found == "":
		return "", refusal("%s, so nothing changed", why)
	}
	if err := undo(ctx, store, top, revert, recovery, plan); err != nil && len(last) == 0 {
		return "", refusal("%s; with no change of an earlier attempt's on record, these were left as they are too: %v", why, err)
	} else if err != nil {
		return "", refusal("%s; putting back the files an earlier attempt restored failed too: %v", why, err)
	}
	if len(last) == 0 {
		return "", refusal("%s, so nothing changed", why)
	}
	return "", refusal("%s, so nothing changed: the files an earlier attempt restored were put back as they were", why)
}

// refusal is a replay's refusal, why it refused as format and args say,
// with what the user is to do then, whatever the files it found.
func refusal(format string, args ...any) error {
	return fmt.Errorf(format+"; revert again to start from the files as they are now", args...)
}

// changedSince lists the paths whose files in top differ from recovery's,
// as a checkpoint lists them, and those revert's journal names that don't
// hold what its last step left there, as a file it deleted that someone
// put back. It leaves out those whose last step left them as they are,
// and those of to, the changes from recovery to the target, that hold the
// target's already, which the revert leaves as they are: a file a deleted
// .gitignore no longer hides, say.
func changedSince(ctx context.Context, store, top string, revert api.ID, recovery string, to []change) ([]string, error) {
	paths, err := checkpointPaths(ctx, top, &api.CheckpointCaptured{})
	if err != nil {
		return nil, err
	}
	index := filepath.Join(store, "confirmed-"+revert.String()+".index")
	clean := func() {
		os.Remove(index)
		os.Remove(index + ".lock")
	}
	clean()
	defer clean()
	tree, err := writeTree(ctx, store, top, index, paths)
	if err != nil {
		return nil, err
	}
	diff, err := storeGit(ctx, store, "", "", nil, "diff-tree", "-r", "-z", "--no-renames", "--name-only", recovery, tree)
	if err != nil {
		return nil, err
	}
	last, err := readJournal(journalOf(store, revert))
	if err != nil {
		return nil, err
	}
	names := slices.DeleteFunc(strings.Split(diff, "\x00"), func(name string) bool { return name == "" })
	for name := range last {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	var changed []string
	for _, name := range names {
		path := filepath.Join(top, name)
		if stepHolds(ctx, store, path, last[name]) {
			continue
		}
		if i := slices.IndexFunc(to, func(c change) bool { return c.path == name }); i >= 0 && atTarget(ctx, store, top, to[i]) {
			continue
		}
		changed = append(changed, name)
	}
	return changed, nil
}

// atTarget is whether c's path in top is as c's to-side has it.
func atTarget(ctx context.Context, store, top string, c change) bool {
	path := filepath.Join(top, c.path)
	if c.mode == "" {
		_, err := os.Lstat(path)
		return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
	}
	done, _ := isBlob(ctx, store, path, c)
	return done
}

// stepHolds is whether path holds what step s of a journal left there.
func stepHolds(ctx context.Context, store, path string, s step) bool {
	fi, err := os.Lstat(path)
	switch s.kind {
	case wroteFile:
		if err != nil || fi.IsDir() {
			return false
		}
		mode, blob, err := blobAt(ctx, store, path)
		return err == nil && mode == s.mode && blob == s.blob && fi.Size() == s.size && fi.ModTime().UnixNano() == s.mtime
	case deletedFile, removedFolder:
		return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
	case madeFolder:
		return err == nil && fi.IsDir()
	}
	return false
}

// applyingRef is the mark a revert leaves in the store before it writes
// any of thread's files: a commit after the recovery checkpoint it took,
// whose plan file lists the paths it is to write. A replay that finds it
// undoes no others, and one that doesn't knows no file moved.
func applyingRef(thread, revert api.ID) string {
	return checkpointRefs(thread) + "applying/" + revert.String()
}

// markApplying leaves revert's applyingRef, with its plan: each of
// changes, from recovery to the target, whose path in top isn't as the
// target has it already, in git's raw diff format. A path that is, as an
// ignored file that holds the target's bytes, is never the revert's to put
// back. A replay keeps the plan the first attempt left, as the paths that
// attempt wrote are as the target has them by then. It returns the plan
// it left or found.
func (h *Host) markApplying(ctx context.Context, store, top string, thread, revert api.ID, recovery string, changes []change) ([]change, error) {
	ref := applyingRef(thread, revert)
	if found, plan, err := readPlan(ctx, store, ref); found != "" || err != nil {
		return plan, err
	}
	var plan []change
	var text strings.Builder
	for _, c := range changes {
		var done bool
		if c.mode == "" {
			_, err := os.Lstat(filepath.Join(top, c.path))
			done = errors.Is(err, os.ErrNotExist)
		} else {
			done, _ = isBlob(ctx, store, filepath.Join(top, c.path), c)
		}
		if done {
			continue
		}
		plan = append(plan, c)
		zero := strings.Repeat("0", max(len(c.wasBlob), len(c.blob)))
		fmt.Fprintf(&text, ":%s %s %s %s M\x00%s\x00", cmp.Or(c.was, "000000"), cmp.Or(c.mode, "000000"), cmp.Or(c.wasBlob, zero), cmp.Or(c.blob, zero), c.path)
	}
	unlock, err := h.checkpoints.lock(ctx, store)
	if err != nil {
		return nil, err
	}
	defer unlock()
	blob, err := storeGit(ctx, store, "", "", []byte(text.String()), "hash-object", "-w", "--stdin")
	if err != nil {
		return nil, err
	}
	tree, err := storeGit(ctx, store, "", "", []byte("100644 blob "+blob+"\tplan\n"), "mktree")
	if err != nil {
		return nil, err
	}
	commit, err := storeGit(ctx, store, "", "", nil, "commit-tree", "-m", "glitched applying", "-p", recovery, tree)
	if err != nil {
		return nil, err
	}
	_, err = storeGit(ctx, store, "", "", nil, "update-ref", ref, commit, "")
	return plan, err
}

// readPlan is the recovery commit and the plan of the applying mark at
// ref, "" with no mark.
func readPlan(ctx context.Context, store, ref string) (recovery string, plan []change, err error) {
	recovery, err = storeGit(ctx, store, "", "", nil, "rev-parse", "--verify", "--quiet", ref+"^1")
	if err != nil {
		return "", nil, nil
	}
	text, err := storeGit(ctx, store, "", "", nil, "cat-file", "blob", ref+":plan")
	if err == nil {
		plan, err = parseChanges(text)
	}
	return recovery, plan, err
}

// undo puts back what a revert wrote of plan's paths, as recovery has
// them: the same apply as the revert's, with each change's sides swapped,
// going by the revert's journal. It removes or replaces a path only where
// the journal says the revert wrote what the path still holds.
func undo(ctx context.Context, store, top string, revert api.ID, recovery string, plan []change) error {
	back := make([]change, len(plan))
	for i, c := range plan {
		back[i] = change{path: c.path, was: c.mode, wasBlob: c.blob, mode: c.was, blob: c.wasBlob}
	}
	return apply(ctx, store, top, revert, recovery, back, true)
}

// unrevert undoes, once the rewind failed with err, what an earlier
// attempt at b wrote of the paths its plan names, as undo does. With no
// plan and no journal no attempt moved a file, and nothing changed. A
// journal with no plan, or a plan that isn't after b's recovery
// checkpoint, leaves the files as they are, and says which.
func (h *Host) unrevert(ctx context.Context, t engine.Thread, b engine.RevertThread, store string, r *api.ThreadReverted, err error) error {
	recovery, plan, again := readPlan(ctx, store, applyingRef(t.ID, b.Revert))
	if again != nil {
		return fmt.Errorf("%w; the plan of an earlier attempt couldn't be read, so the files it restored were left as they are: %v", err, again)
	}
	if recovery == "" {
		last, again := readJournal(journalOf(store, b.Revert))
		switch {
		case again != nil:
			return fmt.Errorf("%w; the journal of an earlier attempt couldn't be read, so the files it restored were left as they are: %v", err, again)
		case len(last) > 0:
			return fmt.Errorf("%w; with its plan gone, the files an earlier attempt changed couldn't be put back, so they were left as they are: %s", err, strings.Join(slices.Sorted(maps.Keys(last)), ", "))
		}
		return fmt.Errorf("%w, so nothing changed", err)
	}
	if want, _ := storeGit(ctx, store, "", "", nil, "rev-parse", "--verify", "--quiet", checkpointRef(t.ID, engine.CaptureCheckpoint{Run: b.Revert, Phase: api.CheckpointRecovery})); want != recovery {
		return fmt.Errorf("%w; the plan of an earlier attempt follows %s, not this revert's recovery checkpoint %q, so the files it restored were left as they are", err, recovery, want)
	}
	if again := undo(ctx, store, h.ownsDir(t.ID, t.Workspace), b.Revert, recovery, plan); again != nil {
		r.Recovery = recovery
		return fmt.Errorf("%w; putting back the files an earlier attempt restored failed too: %v", err, again)
	}
	return fmt.Errorf("%w, so nothing changed: the files an earlier attempt restored were put back as they were", err)
}

// rewind rewinds t's conversation to the end of b's run, as its provider
// can, into r: natively, or as a new conversation the thread's next run
// hands the kept turns to. The conversation is t's as the revert runs,
// which a rewind decided before it may have cut since: the run must
// still be in it, and drops only what it still holds.
func (h *Host) rewind(ctx context.Context, t engine.Thread, b engine.RevertThread, r *api.ThreadReverted) error {
	if b.To.Phase == api.CheckpointRecovery {
		return h.unrewind(ctx, t, b, r)
	}
	at := func(id api.ID) int {
		return slices.IndexFunc(t.Runs, func(run engine.Run) bool { return run.ID == id })
	}
	switch i := at(b.To.ID); {
	case i < 0:
		return errors.New("no run of the thread's has that checkpoint")
	case t.Runs[i].Dropped:
		return errors.New("A rewind took this turn out of the conversation")
	}
	dropped := slices.DeleteFunc(slices.Clone(b.Dropped), func(id api.ID) bool { return at(id) < 0 || t.Runs[at(id)].Dropped })
	if !slices.ContainsFunc(dropped, func(id api.ID) bool { return !t.Runs[at(id)].Silent }) {
		return errors.New("The conversation already ends at this turn")
	}
	heads, compacted, err := h.store.Heads(ctx, t.ID)
	if err != nil {
		return err
	}
	spec := provider.RewindSpec{Thread: t.ID, Revert: b.Revert, Workspace: t.Workspace, Session: t.Session, Head: heads[b.To.ID]}
	for _, run := range dropped {
		spec.Dropped = append(spec.Dropped, heads[run])
		spec.Compacted = spec.Compacted || compacted[run]
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	rw, err := h.providers.Rewind(ctx, t.Model.Provider, spec)
	if err != nil {
		return fmt.Errorf("the conversation couldn't be rewound: %w", err)
	}
	r.Conversation, r.Session, r.From, r.At, r.Dropped = api.RewindPortable, rw.Session, t.Session, rw.At, dropped
	if rw.Native {
		r.Conversation = api.RewindNative
	}
	return nil
}

// unrewind undoes, into r, the rewind of t's conversation whose recovery
// checkpoint b reverts to, as Zeron does: the conversation goes on from
// the one t had before the rewind, which a native rewind left as it was,
// as far as the last run the rewind dropped, and it holds those runs
// again. It undoes only the thread's latest revert, and only while no turn
// was admitted since that one was asked for, as the thread's log has them
// in order. Where the adapter forks that conversation now and checks
// where the fork ends, as Codex's does, the undo is native; where the
// provider no longer has it, or would branch off it only at the next run,
// unchecked, as Claude's would, the thread's next run is handed the
// turns, as Zeron's Claude is.
func (h *Host) unrewind(ctx context.Context, t engine.Thread, b engine.RevertThread, r *api.ThreadReverted) error {
	was, err := h.store.Reverted(ctx, t.ID, b.To.ID, b.Revert)
	switch {
	case err != nil:
		return err
	case was == nil || !was.Rewound() || len(was.Dropped) == 0:
		return errors.New("That revert didn't rewind the conversation")
	case was.Turned:
		return errors.New("A turn came after this rewind")
	case was.Reverted:
		return errors.New("A revert came after this rewind, and only the latest can be undone")
	}
	heads, compacted, err := h.store.Heads(ctx, t.ID)
	if err != nil {
		return err
	}
	spec := provider.RewindSpec{Thread: t.ID, Revert: b.Revert, Workspace: t.Workspace, Session: was.From, Head: heads[was.Dropped[len(was.Dropped)-1]]}
	for _, run := range was.Dropped {
		spec.Compacted = spec.Compacted || compacted[run]
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	rw, err := h.providers.Rewind(ctx, t.Model.Provider, spec)
	switch {
	case errors.Is(err, provider.ErrNoConversation):
		slog.Warn("runtime: the agent no longer has the conversation from before the rewind, so the undo hands its turns to the next run", "thread", t.ID, "err", err)
		rw = provider.Rewind{}
	case err != nil:
		return fmt.Errorf("the conversation couldn't be brought back: %w", err)
	}
	r.Conversation, r.Session, r.From, r.Restored = api.RewindPortable, rw.Session, was.From, was.Dropped
	if rw.Native && rw.At == "" {
		r.Conversation = api.RewindNative
	}
	return nil
}

// handoffCap is the most a handoff takes, in bytes, its own text and all.
const handoffCap = 48 << 10

// handoffFrame is what a handoff's turns go between: head, whose %s is
// the note on the turns left out, and tail.
type handoffFrame struct{ head, tail string }

var rewindFrame = handoffFrame{
	head: "<rewound_conversation>\nThe user rewound this conversation to an earlier turn, and it goes on in a new session. These are its turns up to there, newest first.%s\n",
	tail: "</rewound_conversation>\n\n",
}

// unrewindFrame is the frame of the turns an undo of a rewind brought
// back.
var unrewindFrame = handoffFrame{
	head: "<rewound_conversation>\nThe user undid a rewind of this conversation, and it goes on in a new session. These are its turns, newest first.%s\n",
	tail: "</rewound_conversation>\n\n",
}

// handoff is the turns a rewind kept of t's conversation, to and those
// the conversation held before it: what the user asked in each, and the
// reply, newest first, in at most handoffCap bytes, for a new
// conversation to go on from. The newest is always handed, cut to fit;
// the oldest are left out where the rest don't fit.
func (h *Host) handoff(ctx context.Context, t engine.Thread, to api.ID, frame handoffFrame) (string, error) {
	const (
		left  = " %d older turns didn't fit and are left out."
		shape = "<turn>\n<user>\n%s\n</user>\n<assistant>\n%s\n</assistant>\n</turn>\n"
		cut   = "\n[the rest didn't fit]"
	)
	head, tail := frame.head, frame.tail
	// A fork's conversation goes on from its source's, back to the first
	// thread of its line that is no fork.
	type part struct {
		t  engine.Thread
		to api.ID
	}
	line, runs := []part{{t, to}}, len(t.Runs)
	for {
		f, err := h.store.Origin(ctx, line[len(line)-1].t.ID)
		if err != nil {
			return "", err
		}
		if f == nil {
			break
		}
		src, err := h.forkSource(ctx, *f)
		if err != nil {
			return "", err
		}
		line, runs = append(line, part{src, f.Run}), runs+len(src.Runs)
	}
	// The turns get what the block's own text leaves, its note at its
	// longest included.
	room := handoffCap - len(fmt.Sprintf(head, fmt.Sprintf(left, runs))) - len(tail)
	clip := func(s string, n int) string {
		if len(s) <= n {
			return s
		}
		return strings.ToValidUTF8(s[:max(0, n-len(cut))], "") + cut
	}
	var turns []string
	size, omitted := 0, 0
	for _, p := range line {
		t, to := p.t, p.to
		end := slices.IndexFunc(t.Runs, func(run engine.Run) bool { return run.ID == to })
		for i := end; i >= 0; i-- {
			run := t.Runs[i]
			if run.Silent || run.Dropped {
				continue
			}
			if omitted > 0 {
				omitted++
				continue
			}
			var asked []string
			for _, m := range t.Queue {
				if m.Run == run.ID && m.State == engine.QueueDispatched {
					asked = append(asked, m.Text)
				}
			}
			reply, err := h.store.RunReply(ctx, t.ID, run.ID)
			if err != nil {
				return "", err
			}
			said, reply := strings.Join(asked, "\n\n"), strings.TrimSpace(reply)
			if len(turns) == 0 {
				said = clip(said, room/2)
				reply = clip(reply, room-len(shape)-len(said))
			}
			turn := fmt.Sprintf(shape, said, reply)
			if len(turns) > 0 && size+len(turn) > room {
				omitted++
				continue
			}
			size += len(turn)
			turns = append(turns, turn)
		}
	}
	note := ""
	if omitted > 0 {
		note = fmt.Sprintf(left, omitted)
	}
	return fmt.Sprintf(head, note) + strings.Join(turns, "") + tail, nil
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
	return parseChanges(out)
}

// parseChanges reads changes in git's raw diff format, with -z.
func parseChanges(out string) ([]change, error) {
	var changes []change
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(meta) != 5 {
			return nil, fmt.Errorf("git diff-tree said %q", fields[i])
		}
		c := change{path: fields[i+1]}
		if meta[0] != "000000" {
			c.was, c.wasBlob = meta[0], meta[2]
		}
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
			if c.was != "" {
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
	mode, blob, err := blobAt(ctx, store, path)
	return err == nil && mode != "" && mode == c.mode && blob == c.blob, err
}

// blobAt is the mode and blob, hashed with no filter, of the file or
// symlink at path, or "" for anything else there.
func blobAt(ctx context.Context, store, path string) (mode, blob string, err error) {
	fi, err := os.Lstat(path)
	switch {
	case err != nil:
		return "", "", err
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return "", "", err
		}
		blob, err = storeGit(ctx, store, "", "", []byte(target), "hash-object", "--no-filters", "--stdin")
		return "120000", blob, err
	case !fi.Mode().IsRegular():
		return "", "", nil
	case fi.Mode().Perm()&0o100 != 0:
		mode = "100755"
	default:
		mode = "100644"
	}
	blob, err = storeGit(ctx, store, "", "", nil, "hash-object", "--no-filters", "--", path)
	return mode, blob, err
}

// restore puts changes' to-sides in top as tree holds them, as a revert
// does, recording each change in the revert's journal.
func restore(ctx context.Context, store, top string, revert api.ID, tree string, changes []change) error {
	return apply(ctx, store, top, revert, tree, changes, false)
}

// journalOf is the file in store where revert records each change it
// makes to a worktree, once it is made, in order: what a path held after.
// An undo goes by it, never by what a path happens to hold.
func journalOf(store string, revert api.ID) string {
	return filepath.Join(journals(store), revert.String())
}

// journals is the folder of store's journals.
func journals(store string) string { return filepath.Join(store, "glitched-applying") }

// step is what a revert left at a path, as its journal records it: a file
// or a symlink it wrote, with the size and modification time the path had
// once written, or a file it deleted, or a folder it made or removed.
type step struct {
	kind, mode, blob string
	size, mtime      int64
}

// The kinds of step.
const (
	wroteFile     = "wrote"
	deletedFile   = "deleted"
	madeFolder    = "made"
	removedFolder = "removed"
)

// readJournal is each path's last step in the journal at path, none for a
// journal never written. A record cut short, as by a crash, is no step,
// and readJournal cuts it off the journal, synced, so that the next record
// appended starts a record of its own.
func readJournal(path string) (map[string]step, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]step{}, nil
	}
	if err != nil {
		return nil, err
	}
	if whole := bytes.LastIndexByte(data, 0) + 1; whole < len(data) {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		err = f.Truncate(int64(whole))
		if err == nil {
			err = f.Sync()
		}
		if err = errors.Join(err, f.Close()); err != nil {
			return nil, err
		}
		data = data[:whole]
	}
	last := map[string]step{}
	records := strings.Split(string(data), "\x00")
	for _, r := range records[:len(records)-1] {
		f := strings.SplitN(r, " ", 6)
		if len(f) != 6 {
			return nil, fmt.Errorf("the revert's journal %s says %q", path, r)
		}
		s := step{kind: f[0], mode: f[1], blob: f[2]}
		fmt.Sscan(f[3], &s.size)
		fmt.Sscan(f[4], &s.mtime)
		last[f[5]] = s
	}
	return last, nil
}

// applying is one apply of a revert's changes to top: forward, or an undo.
type applying struct {
	ctx                         context.Context
	store, top, staged, journal string
	undo                        bool
	// folders are the to-side's folders above the changes' files, and
	// found the from-side's.
	folders, found map[string]bool
	// last is each path's last step in the journal.
	last map[string]step
	// left are the paths apply refused, each named once.
	left []string
}

// apply puts changes' to-sides in top as tree holds them: it writes tree's
// files into a folder of the store's, then deletes, then writes, each path
// through put. Doing it again does the same. A path put refuses stays as
// it is, and fails the apply, by name, once the rest are done.
func apply(ctx context.Context, store, top string, revert api.ID, tree string, changes []change, undo bool) error {
	a := &applying{ctx: ctx, store: store, top: top, staged: filepath.Join(store, "restore-"+revert.String()), journal: journalOf(store, revert), undo: undo, folders: map[string]bool{}, found: map[string]bool{}}
	var err error
	if a.last, err = readJournal(a.journal); err != nil {
		return err
	}
	index := a.staged + ".index"
	clean := func() {
		os.RemoveAll(a.staged)
		os.Remove(index)
		os.Remove(index + ".lock")
	}
	clean()
	defer clean()
	var paths []byte
	for _, c := range changes {
		for d := filepath.Dir(c.path); c.was != "" && d != "."; d = filepath.Dir(d) {
			a.found[filepath.ToSlash(d)] = true
		}
		if c.mode == "" {
			continue
		}
		paths = append(append(paths, c.path...), 0)
		for d := filepath.Dir(c.path); d != "."; d = filepath.Dir(d) {
			a.folders[filepath.ToSlash(d)] = true
		}
	}
	if err := os.MkdirAll(a.staged, 0o700); err != nil {
		return err
	}
	if _, err := storeGit(ctx, store, a.staged, index, nil, "read-tree", tree); err != nil {
		return err
	}
	if _, err := storeGit(ctx, store, a.staged, index, paths, "checkout-index", "-z", "--stdin", "-f"); err != nil {
		return err
	}
	for _, deleting := range []bool{true, false} {
		for _, c := range changes {
			if (c.mode == "") != deleting {
				continue
			}
			s := "applying"
			if deleting {
				s = "deleting"
			}
			checkpointHook(ctx, s)
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := a.put(c); err != nil {
				return err
			}
		}
	}
	if len(a.left) > 0 {
		return fmt.Errorf("%s changed since the revert began, so it was left as it is", strings.Join(a.left, ", "))
	}
	return nil
}

// refuse names rel among the paths apply left.
func (a *applying) refuse(rel string) {
	if !slices.Contains(a.left, rel) {
		a.left = append(a.left, rel)
	}
}

// record appends a step at rel to the journal, synced: what the apply just
// left there, or a folder it is about to remove. An undo counts a folder
// with no step as one the revert never touched, so a removal is recorded
// first: a crash between leaves a folder the undo refuses.
func (a *applying) record(rel string, s step) error {
	checkpointHook(a.ctx, "journaling")
	if err := a.ctx.Err(); err != nil {
		return err
	}
	_, missing := os.Stat(a.journal)
	if err := os.MkdirAll(filepath.Dir(a.journal), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(a.journal, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%s %s %s %d %d %s\x00", s.kind, cmp.Or(s.mode, "-"), cmp.Or(s.blob, "-"), s.size, s.mtime, rel)
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err == nil && missing != nil {
		err = syncDir(filepath.Dir(a.journal))
	}
	if err == nil {
		a.last[rel] = s
	}
	return err
}

// syncDir makes the names in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// put is the one way a revert changes a path in top: it puts c's to-side
// there, the file or symlink staged holds, or nothing. It looks at the
// path just before it acts, and leaves a path that holds the to-side
// already. Otherwise it removes or replaces only what holds c's from-side:
// forward, what the revert found there; in an undo, only what the
// revert's journal says it left there, a file of the same size and time,
// or a file it deleted. Any other path it refuses, as it does a folder
// that isn't one the to-side has or the revert made. It never removes
// anything recursively, nor a folder that isn't empty, nor writes through
// a file or a symlink where a folder above the path was. Each change it
// makes it records in the journal.
func (a *applying) put(c change) error {
	for d := filepath.Dir(c.path); d != "."; d = filepath.Dir(d) {
		if fi, err := os.Lstat(filepath.Join(a.top, d)); err == nil && !fi.IsDir() {
			if c.mode != "" {
				a.refuse(filepath.ToSlash(d))
			}
			return nil
		}
	}
	path := filepath.Join(a.top, c.path)
	fi, err := os.Lstat(path)
	var mode, blob string
	switch {
	case errors.Is(err, os.ErrNotExist):
		fi = nil
	case err != nil:
		return err
	case !fi.IsDir():
		if mode, blob, err = blobAt(a.ctx, a.store, path); err != nil {
			return err
		}
		if mode == "" {
			a.refuse(c.path)
			return nil
		}
	}
	dir := fi != nil && fi.IsDir()
	last := a.last[c.path]
	// ours is whether a folder at path is one the apply may count as the
	// to-side's, or remove: forward, one the from-side or the to-side has;
	// in an undo, one the revert made, or one it never touched where the
	// to-side has one.
	ours := !a.undo && (a.found[c.path] || a.folders[c.path]) || a.undo && (last.kind == madeFolder || last.kind == "" && a.folders[c.path])
	switch {
	case c.mode != "" && mode == c.mode && blob == c.blob:
		return nil
	case c.mode == "" && fi == nil:
		return a.prune(c.path)
	case c.mode == "" && dir && a.folders[c.path] && ours:
		return nil
	}
	switch {
	case dir && c.was == "" && ours && isEmpty(path):
		if err := a.record(c.path, step{kind: removedFolder}); err != nil {
			return err
		}
		if os.Remove(path) != nil {
			a.refuse(c.path)
			return nil
		}
		if c.mode == "" {
			return a.prune(c.path)
		}
	case dir && !isEmpty(path):
		a.refuseFolder(c.path)
		return nil
	case dir:
		a.refuse(c.path)
		return nil
	case fi == nil && a.undo && last.kind != deletedFile && last.kind != removedFolder:
		a.refuse(c.path)
		return nil
	case fi == nil && !a.undo && c.was != "" && last.kind != deletedFile:
		a.refuse(c.path)
		return nil
	case fi == nil:
	case mode != c.was || blob != c.wasBlob:
		a.refuse(c.path)
		return nil
	case a.undo && (last.kind != wroteFile || last.mode != mode || last.blob != blob || last.size != fi.Size() || last.mtime != fi.ModTime().UnixNano()):
		a.refuse(c.path)
		return nil
	}
	if c.mode == "" {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("%s couldn't be deleted: %w", c.path, err)
		}
		if err := a.record(c.path, step{kind: deletedFile}); err != nil {
			return err
		}
		return a.prune(c.path)
	}
	if ok, err := a.folder(filepath.Dir(c.path)); err != nil || !ok {
		return err
	}
	if err := moveInto(filepath.Join(a.staged, c.path), path); err != nil {
		return fmt.Errorf("%s couldn't be put back: %w", c.path, err)
	}
	fi, err = os.Lstat(path)
	if err != nil {
		return err
	}
	return a.record(c.path, step{kind: wroteFile, mode: c.mode, blob: c.blob, size: fi.Size(), mtime: fi.ModTime().UnixNano()})
}

// refuseFolder refuses the folder at rel, naming everything it holds first.
func (a *applying) refuseFolder(rel string) {
	root := filepath.Join(a.top, rel)
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != root && len(a.left) < 20 {
			r, _ := filepath.Rel(a.top, p)
			a.refuse(filepath.ToSlash(r))
		}
		return nil
	})
	a.refuse(rel)
}

// isEmpty is whether the folder at path holds nothing.
func isEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}

// folder makes rel, a folder of top's, with each folder above it the apply
// makes, as the journal records. It refuses a folder there that an undo
// finds where the revert left anything but a folder it made, as one made
// since, and a file or a symlink in the way, reporting whether rel is a
// folder now.
func (a *applying) folder(rel string) (bool, error) {
	if rel == "." {
		return true, nil
	}
	if ok, err := a.folder(filepath.Dir(rel)); err != nil || !ok {
		return ok, err
	}
	rel = filepath.ToSlash(rel)
	fi, err := os.Lstat(filepath.Join(a.top, rel))
	switch {
	case err == nil && fi.IsDir() && a.undo && a.last[rel].kind != "" && a.last[rel].kind != madeFolder:
		a.refuseFolder(rel)
		return false, nil
	case err == nil && fi.IsDir():
		return true, nil
	case err == nil:
		a.refuse(rel)
		return false, nil
	case !errors.Is(err, os.ErrNotExist):
		return false, err
	}
	if err := os.Mkdir(filepath.Join(a.top, rel), 0o755); err != nil {
		return false, err
	}
	return true, a.record(rel, step{kind: madeFolder})
}

// prune removes each folder above rel that is empty now and the apply's
// to remove: forward, one it found; in an undo, one the revert made.
func (a *applying) prune(rel string) error {
	for d := filepath.ToSlash(filepath.Dir(rel)); d != "."; d = filepath.ToSlash(filepath.Dir(d)) {
		if a.undo && a.last[d].kind != madeFolder || !isEmpty(filepath.Join(a.top, d)) {
			return nil
		}
		if err := a.record(d, step{kind: removedFolder}); err != nil {
			return err
		}
		if os.Remove(filepath.Join(a.top, d)) != nil {
			return nil
		}
	}
	return nil
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

// keepRecoveries deletes the recovery checkpoints and the applying marks
// of thread's finished reverts, but the newest keptRecoveries of those
// recoveries, b's among them, b's mark and the checkpoint b put back, so
// that a replay finds them. The reverts still to run, and those being
// confirmed, keep their recovery, their mark and the checkpoint they are
// to put back, none of which counts against keptRecoveries. It reads the reverts being confirmed before those
// still to run: a confirmation saves its refs only once it is among them
// and leaves them only once it has committed, so a revert whose refs it
// lists is in one or the other.
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
	marks, err := storeGit(ctx, store, "", "", nil, "for-each-ref", "--format=%(refname)", checkpointRefs(thread)+"applying/")
	if err != nil {
		return err
	}
	var unfinished []engine.RevertThread
	h.mu.Lock()
	for id, c := range h.confirming {
		if c.thread == thread {
			unfinished = append(unfinished, engine.RevertThread{Revert: id, To: c.to})
		}
	}
	h.mu.Unlock()
	checkpointHook(ctx, "sweeping")
	pending, err := h.store.RevertsPending(ctx, thread)
	if err != nil {
		return err
	}
	kept := map[string]bool{}
	for _, r := range append(unfinished, pending...) {
		if r.Revert != b.Revert {
			kept[checkpointRef(thread, engine.CaptureCheckpoint{Run: r.Revert, Phase: api.CheckpointRecovery})] = true
			kept[checkpointRef(thread, engine.CaptureCheckpoint{Run: r.To.ID, Phase: r.To.Phase})] = true
			kept[applyingRef(thread, r.Revert)] = true
		}
	}
	var finished []string
	for _, ref := range strings.Split(listed, "\n") {
		if !kept[ref] {
			finished = append(finished, ref)
		}
	}
	took := checkpointRef(thread, engine.CaptureCheckpoint{Run: b.Revert, Phase: api.CheckpointRecovery})
	to := checkpointRef(thread, engine.CaptureCheckpoint{Run: b.To.ID, Phase: b.To.Phase})
	var update string
	for _, ref := range finished[:max(0, len(finished)-keptRecoveries)] {
		if ref != took && ref != to {
			update += "delete " + ref + "\n"
		}
	}
	for _, ref := range strings.Fields(marks) {
		if !kept[ref] && ref != applyingRef(thread, b.Revert) {
			update += "delete " + ref + "\n"
			os.Remove(filepath.Join(journals(store), filepath.Base(ref)))
		}
	}
	if update == "" {
		return nil
	}
	_, err = storeGit(ctx, store, "", "", []byte(update), "update-ref", "--stdin")
	return err
}
