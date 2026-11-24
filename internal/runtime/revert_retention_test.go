package runtime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/runtime"
)

// holds holds checkpoint steps where a test asks, each the first time
// it is reached.
type holds struct {
	mu   sync.Mutex
	list []*hold
}

type hold struct {
	step     string
	of       context.Context
	reached  chan struct{}
	let      chan struct{}
	once     sync.Once
	released sync.Once
}

// holding hooks the checkpoint steps for the test's length.
func holding(t *testing.T) *holds {
	h := &holds{}
	reset := runtime.SetCheckpointHook(func(ctx context.Context, step string) {
		h.mu.Lock()
		var at *hold
		for _, e := range h.list {
			if e.step == step && (e.of == nil || e.of == ctx) {
				at = e
				break
			}
		}
		h.mu.Unlock()
		if at == nil {
			return
		}
		at.once.Do(func() {
			close(at.reached)
			select {
			case <-at.let:
			case <-ctx.Done():
			}
		})
	})
	t.Cleanup(func() {
		h.mu.Lock()
		for _, e := range h.list {
			e.released.Do(func() { close(e.let) })
		}
		h.mu.Unlock()
		reset()
	})
	return h
}

// at holds the first step named step whose context is of, any when nil:
// reached is closed once it is reached, and release lets it go on.
func (h *holds) at(step string, of context.Context) (reached <-chan struct{}, release func()) {
	e := &hold{step: step, of: of, reached: make(chan struct{}), let: make(chan struct{})}
	h.mu.Lock()
	h.list = append(h.list, e)
	h.mu.Unlock()
	return e.reached, func() { e.released.Do(func() { close(e.let) }) }
}

func recoveryOf(revert api.ID) api.CheckpointRef {
	return api.CheckpointRef{Phase: api.CheckpointRecovery, ID: revert}
}

func reach(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// More reverts queued behind one than a thread keeps recoveries of keep
// theirs: the oldest of them still puts its files back.
func TestQueuedRevertsKeepTheirRecoveries(t *testing.T) {
	defer runtime.SetKeptRecoveries(2)()
	r := twoTurns(t, dbPath(t), tempDir(t), repo(t))
	write(t, filepath.Join(r.ws, "README"), "two\n")
	recovered, release := holding(t).at("recovered", nil)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.run), Files: true})
	reach(t, recovered, "the first revert's recovery")
	var queued []api.ID
	for range 4 {
		queued = append(queued, submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true}).Command)
	}
	release()
	if one := untilReverted(t, r.n.h, r.thread, 1); one.Status != api.RevertUnchanged {
		t.Fatalf("first revert %+v, want unchanged", one)
	}
	two := untilReverted(t, r.n.h, r.thread, 2)
	if two.Revert != queued[0] || two.Status != api.RevertRestored {
		t.Fatalf("oldest queued revert %+v, want %s restored", two, queued[0])
	}
	if got := read(t, filepath.Join(r.ws, "README")); got != "hi\n" {
		t.Fatalf("README %q after the oldest queued revert, want the first turn's", got)
	}
}

// An undo queued behind a revert keeps the recovery it puts back, though
// the revert ahead of it pushes that recovery past the newest a thread
// keeps.
func TestAQueuedUndoKeepsTheRecoveryItPutsBack(t *testing.T) {
	defer runtime.SetKeptRecoveries(2)()
	r := twoTurns(t, dbPath(t), tempDir(t), repo(t))
	undone := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true}).Command
	untilReverted(t, r.n.h, r.thread, 1)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
	untilReverted(t, r.n.h, r.thread, 2)
	recovered, release := holding(t).at("recovered", nil)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
	reach(t, recovered, "the third revert's recovery")
	undo := submit(t, r.n.h, r.thread, api.ThreadRevert{To: recoveryOf(undone), Files: true}).Command
	release()
	untilReverted(t, r.n.h, r.thread, 3)
	got := untilReverted(t, r.n.h, r.thread, 4)
	if got.Revert != undo || got.Status != api.RevertRestored {
		t.Fatalf("undo %+v, want %s restored", got, undo)
	}
	if got := read(t, filepath.Join(r.ws, "README")); got != "three\n" {
		t.Fatalf("README %q after the undo, want the one the first revert saved", got)
	}
}

// A revert that commits while the one ahead of it sweeps keeps its plan:
// an ignored file the plan left alone, which the user deleted after the
// confirmation, stays deleted.
func TestARevertCommittingDuringASweepKeepsItsPlan(t *testing.T) {
	r := twoTurns(t, dbPath(t), tempDir(t), repo(t))
	confirming, stop := context.WithCancel(context.Background())
	defer stop()
	h := holding(t)
	applied, apply := h.at("applied", nil)
	saved, commit := h.at("saved", confirming)
	sweeping, sweep := h.at("sweeping", nil)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
	reach(t, applied, "the first revert's files")
	draft := filepath.Join(r.ws, "new.txt")
	write(t, filepath.Join(r.ws, ".gitignore"), "new.txt\n")
	write(t, draft, "new\n")
	id := api.NewID()
	submitted := make(chan error, 1)
	go func() {
		_, err := r.n.h.Submit(confirming, api.Command{ID: id, Target: r.thread, Body: api.ThreadRevert{To: runCheckpoint(r.run), Files: true}})
		submitted <- err
	}()
	reach(t, saved, "the second revert's confirmation")
	if plan := gitIn(t, theStore(t, r.dir), "show", "refs/glitched/"+r.thread.String()+"/applying/"+id.String()+":plan"); strings.Contains(plan, "new.txt") {
		t.Fatalf("plan %q names the ignored file the target holds as it is", plan)
	}
	apply()
	reach(t, sweeping, "the first revert's sweep")
	commit()
	if err := <-submitted; err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(draft); err != nil {
		t.Fatal(err)
	}
	sweep()
	got := untilReverted(t, r.n.h, r.thread, 2)
	if got.Status != api.RevertFailed || !strings.Contains(got.Error, "new.txt") {
		t.Fatalf("second revert %+v, want it to refuse naming new.txt", got)
	}
	if exists(draft) {
		t.Fatalf("new.txt is back as %q, after the user deleted it", read(t, draft))
	}
}

// An Undo that comes while a sweep prunes its target waits for the sweep
// and finds the target gone before it saves anything: it fails as a revert
// to a checkpoint pruned before it was confirmed, and leaves no recovery
// or plan of its own.
func TestAnUndoDuringASweepFindsItsPrunedTargetGone(t *testing.T) {
	defer runtime.SetKeptRecoveries(2)()
	r := twoTurns(t, dbPath(t), tempDir(t), repo(t))
	old := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true}).Command
	untilReverted(t, r.n.h, r.thread, 1)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
	untilReverted(t, r.n.h, r.thread, 2)
	h := holding(t)
	sweeping, sweep := h.at("sweeping", nil)
	confirming, stop := context.WithCancel(context.Background())
	defer stop()
	reserving, reserve := h.at("reserving", confirming)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
	reach(t, sweeping, "the third revert's sweep")
	id := api.NewID()
	submitted := make(chan error, 1)
	go func() {
		_, err := r.n.h.Submit(confirming, api.Command{ID: id, Target: r.thread, Body: api.ThreadRevert{To: recoveryOf(old), Files: true}})
		submitted <- err
	}()
	reach(t, reserving, "the Undo's confirmation")
	reserve()
	sweep()
	if err := <-submitted; err != nil {
		t.Fatal(err)
	}
	got := untilReverted(t, r.n.h, r.thread, 4)
	if got.Revert != id || got.Status != api.RevertFailed || !strings.Contains(got.Error, "no checkpoint of the files was saved there") {
		t.Fatalf("Undo %+v, want it to fail on its pruned target", got)
	}
	for _, ref := range refsOf(t, theStore(t, r.dir), r.thread) {
		if strings.HasSuffix(ref, "/"+id.String()) || strings.HasSuffix(ref, "/recovery/"+old.String()) {
			t.Fatalf("ref %s is left, want the target pruned and nothing saved for the Undo", ref)
		}
	}
	if got := read(t, filepath.Join(r.ws, "README")); got != "hi\n" {
		t.Fatalf("README %q after the failed Undo", got)
	}
}

// A confirmation cancelled once it saved the files deletes what it saved,
// and a retry of the same command saves the files again, as they are
// then, past a sweep that runs meanwhile: an ignored file the new plan
// leaves alone, which the user deleted after the retry, stays deleted.
func TestARetryAfterACancelledConfirmationSavesTheFilesAgain(t *testing.T) {
	r := twoTurns(t, dbPath(t), tempDir(t), repo(t))
	first, stop := context.WithCancel(context.Background())
	defer stop()
	h := holding(t)
	applied, apply := h.at("applied", nil)
	saved, _ := h.at("saved", first)
	sweeping, sweep := h.at("sweeping", nil)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
	reach(t, applied, "the first revert's files")
	draft := filepath.Join(r.ws, "new.txt")
	write(t, filepath.Join(r.ws, ".gitignore"), "new.txt\n")
	write(t, draft, "new\n")
	c := api.Command{ID: api.NewID(), Target: r.thread, Body: api.ThreadRevert{To: runCheckpoint(r.run), Files: true}}
	cancelled := make(chan error, 1)
	go func() {
		_, err := r.n.h.Submit(first, c)
		cancelled <- err
	}()
	reach(t, saved, "the first confirmation's files")
	stop()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled confirmation returned %v", err)
	}
	for _, ref := range refsOf(t, theStore(t, r.dir), r.thread) {
		if strings.HasSuffix(ref, "/"+c.ID.String()) {
			t.Fatalf("the cancelled confirmation left %s", ref)
		}
	}
	apply()
	reach(t, sweeping, "the first revert's sweep")
	retried := make(chan error, 1)
	go func() {
		_, err := r.n.h.Submit(context.Background(), c)
		retried <- err
	}()
	sweep()
	if err := <-retried; err != nil {
		t.Fatal(err)
	}
	if plan := gitIn(t, theStore(t, r.dir), "show", "refs/glitched/"+r.thread.String()+"/applying/"+c.ID.String()+":plan"); strings.Contains(plan, "new.txt") {
		t.Fatalf("plan %q names the ignored file the target holds as it is", plan)
	}
	if err := os.Remove(draft); err != nil {
		t.Fatal(err)
	}
	got := untilReverted(t, r.n.h, r.thread, 2)
	if got.Status != api.RevertFailed || !strings.Contains(got.Error, "new.txt") {
		t.Fatalf("retried revert %+v, want it to refuse naming new.txt", got)
	}
	if exists(draft) {
		t.Fatalf("new.txt is back as %q, after the user deleted it", read(t, draft))
	}
}
