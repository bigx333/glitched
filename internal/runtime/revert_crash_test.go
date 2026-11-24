package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
)

// A revert stopped mid-apply where a file and a folder swap places, either
// way, finishes on the next host.
func TestAReplayFinishesAFileAndFolderSwap(t *testing.T) {
	for _, toDir := range []bool{false, true} {
		name := "directory to file"
		if toDir {
			name = "file to directory"
		}
		t.Run(name, func(t *testing.T) {
			path, dir := dbPath(t), tempDir(t)
			r := twoTurns(t, path, dir, repo(t))
			p := filepath.Join(r.ws, "README")
			z := filepath.Join(r.ws, "zzz")
			var target api.CheckpointCaptured
			if toDir {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(p, "nested"), "inside\n")
				write(t, z, "last\n")
				submit(t, r.n.h, r.thread, dispatch("capture a directory", api.StartNow))
				target = untilCheckpoints(t, r.n.h, r.thread, 4)[3]
				if err := os.RemoveAll(p); err != nil {
					t.Fatal(err)
				}
				write(t, p, "local file\n")
			} else {
				write(t, z, "last\n")
				submit(t, r.n.h, r.thread, dispatch("capture a file", api.StartNow))
				target = untilCheckpoints(t, r.n.h, r.thread, 4)[3]
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(p, "nested"), "local directory\n")
			}
			if err := os.Remove(z); err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			moves := 0
			reset := runtime.SetCheckpointHook(func(ctx context.Context, step string) {
				if step == "applying" {
					moves++
					if moves == 2 {
						close(entered)
						<-ctx.Done()
					}
				}
			})
			defer reset()
			submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(target), Files: true})
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("did not apply first move")
			}
			r.n.kill()
			reset()
			if exists(z) {
				t.Fatal("crash fixture already applied zzz")
			}
			n := serveCheckpoints(t, path, dir, fake.New(fake.Turn(false)))
			got := untilReverted(t, n.h, r.thread, 1)
			if got.Status != api.RevertRestored || !exists(z) {
				t.Fatalf("after restart: status=%s error=%q zzz exists=%t", got.Status, got.Error, exists(z))
			}
		})
	}
}

// A symlink in the way of a file is in the way, though its target's text
// hashes to the file's blob.
func TestASymlinkInTheWayIsntTakenForTheFileItNames(t *testing.T) {
	r := twoTurns(t, dbPath(t), tempDir(t), repo(t))
	p := filepath.Join(r.ws, "new.txt")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.ws, ".gitignore"), "/new.txt\n")
	if err := os.Symlink("new\n", p); err != nil {
		t.Fatal(err)
	}
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.run), Files: true})
	got := untilReverted(t, r.n.h, r.thread, 1)
	if actual, err := os.Readlink(p); err != nil || actual != "new\n" {
		t.Fatalf("ignored symlink changed despite no earlier apply: status=%s error=%q Readlink=%q, %v", got.Status, got.Error, actual, err)
	}
}

// A folder written since the recovery where a saved file was is in the
// way: neither the revert nor its rollback deletes it.
func TestAFailedDeleteLeavesAnUnsavedFolderAlone(t *testing.T) {
	r := twoTurns(t, dbPath(t), tempDir(t), repo(t))
	p := filepath.Join(r.ws, "new.txt")
	keep := filepath.Join(p, "unsaved")
	errs := make(chan error, 1)
	reset := runtime.SetCheckpointHook(func(_ context.Context, step string) {
		if step != "recovered" {
			return
		}
		if err := os.Remove(p); err != nil {
			errs <- err
			return
		}
		if err := os.Mkdir(p, 0o755); err != nil {
			errs <- err
			return
		}
		errs <- os.WriteFile(keep, []byte("not in recovery"), 0o644)
	})
	defer reset()
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
	got := untilReverted(t, r.n.h, r.thread, 1)
	select {
	case err := <-errs:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("recovery hook did not run")
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "not in recovery" {
		t.Fatalf("failed revert removed unsaved data: status=%s error=%q file=%q err=%v", got.Status, got.Error, data, err)
	}
}

// A thread around the folder whose start waits on the revert writes
// nothing, so the revert replayed on the next host goes ahead.
func TestAReplayGoesAheadWithANeighbourParked(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	r := twoTurns(t, path, dir, repo(t))
	p, z := filepath.Join(r.ws, "new.txt"), filepath.Join(r.ws, "zzz")
	write(t, z, "last\n")
	submit(t, r.n.h, r.thread, dispatch("capture both files", api.StartNow))
	target := untilCheckpoints(t, r.n.h, r.thread, 4)[3]
	for _, f := range []string{p, z} {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	entered := make(chan struct{})
	moves := 0
	reset := runtime.SetCheckpointHook(func(ctx context.Context, step string) {
		if step == "applying" {
			moves++
			if moves == 2 {
				close(entered)
				<-ctx.Done()
			}
		}
	})
	defer reset()
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(target), Files: true})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("did not write first file")
	}
	other := api.NewID()
	submit(t, r.n.h, other, api.ThreadCreate{Workspace: filepath.Dir(r.ws), Title: "neighbor", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Text: "start"})
	until(t, r.n.h, other, "the neighbor is parked", func(api.Thread) bool { return runtime.Waiting(r.n.h, other) })
	r.n.kill()
	reset()
	if !exists(p) || exists(z) {
		t.Fatal("bad partial apply fixture")
	}
	n := serveCheckpoints(t, path, dir, fake.New(fake.Turn(false)))
	got := untilReverted(t, n.h, r.thread, 1)
	if got.Status != api.RevertRestored || !exists(z) {
		t.Fatalf("replay with parked neighbor: status=%s error=%q zzz exists=%t", got.Status, got.Error, exists(z))
	}
}
