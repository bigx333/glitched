package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// BenchmarkCheckpoint50kFiles captures a worktree of 50,000 files of about
// 1 KiB each, the most a checkpoint holds: a repository's first, into an
// empty store it seeds from HEAD, with the checkout's objects loose, as
// just committed, and packed, as cloned; and every later one, into a store
// that holds the files.
func BenchmarkCheckpoint50kFiles(b *testing.B) {
	dir, err := filepath.EvalSymlinks(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	project, ws := filepath.Join(dir, "project"), filepath.Join(dir, "ws")
	line := strings.Repeat("checkpoint ", 93) + "\n"
	for i := range 500 {
		for j := range 100 {
			p := filepath.Join(project, fmt.Sprintf("d%03d", i), fmt.Sprintf("f%03d.txt", j))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(fmt.Sprintf("%d %d\n%s", i, j, line)), 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"commit", "-q", "-m", "start"}, {"worktree", "add", "-q", ws}} {
		cmd := exec.Command("git", append([]string{"-C", project}, args...)...)
		cmd.Env = isolatedGit()
		if out, err := cmd.CombinedOutput(); err != nil {
			b.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	h := &Host{ctx: context.Background()}
	WithCheckpoints(filepath.Join(dir, "checkpoints"))(h)
	capture := func(b *testing.B, thread api.ID) {
		c, err := h.captureIn(context.Background(), engine.Thread{ID: thread, Workspace: ws}, engine.CaptureCheckpoint{Run: api.NewID(), Phase: api.CheckpointRun})
		if err != nil || c.Files != 50_000 || c.Reason != "" {
			b.Fatalf("capture %+v, %v; want all 50000 files", c, err)
		}
	}
	first := func(b *testing.B) {
		for b.Loop() {
			b.StopTimer()
			os.RemoveAll(h.checkpoints.root)
			h.checkpoints.bytes = map[string]int64{}
			b.StartTimer()
			capture(b, api.NewID())
		}
	}
	b.Run("a repository's first, checkout loose", first)
	cmd := exec.Command("git", "-C", project, "repack", "-adq")
	cmd.Env = isolatedGit()
	if out, err := cmd.CombinedOutput(); err != nil {
		b.Fatalf("repack: %v\n%s", err, out)
	}
	b.Run("a repository's first, checkout packed", first)
	b.Run("a later one", func(b *testing.B) {
		thread := api.NewID()
		capture(b, thread)
		for b.Loop() {
			capture(b, thread)
		}
	})
}
