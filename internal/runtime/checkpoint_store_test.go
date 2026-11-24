package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

func gitOK(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(context.Background(), dir, isolatedGit(), nil, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func writeFile(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(name, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// committed is a new repository whose one commit holds kept and files.
func committed(t *testing.T, files map[string][]byte) string {
	t.Helper()
	r := t.TempDir()
	gitOK(t, r, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(r, "kept"), []byte("from HEAD\n"))
	for name, data := range files {
		writeFile(t, filepath.Join(r, name), data)
	}
	gitOK(t, r, "add", ".")
	gitOK(t, r, "commit", "-qm", "fixture")
	return r
}

// checkpointHost is a host that keeps checkpoints in a state folder of
// its own, beside the folders other parts of the engine keep there, which
// it must leave as they were.
func checkpointHost(t *testing.T) *Host {
	state := t.TempDir()
	for _, name := range []string{"run-skills/skill/SKILL.md", "logs/providers/fake.log"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(state, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(state, name), []byte(name))
	}
	before := hashes(t, state)
	t.Cleanup(func() {
		after := hashes(t, state)
		for p, sum := range after {
			if !strings.HasPrefix(p, filepath.Join(state, "checkpoints")+string(filepath.Separator)) && before[p] != sum {
				t.Errorf("checkpoints wrote %s", p)
			}
		}
		for p := range before {
			if _, ok := after[p]; !ok {
				t.Errorf("checkpoints removed %s", p)
			}
		}
		if entries, _ := os.ReadDir(state); len(entries) != 3 {
			t.Errorf("the state folder holds %v, want checkpoints, logs and run-skills", entries)
		}
	})
	h := &Host{ctx: context.Background()}
	WithCheckpoints(filepath.Join(state, "checkpoints"))(h)
	return h
}

func runCapture(ws string) (engine.Thread, engine.CaptureCheckpoint) {
	return engine.Thread{ID: api.NewID(), Workspace: ws}, engine.CaptureCheckpoint{Run: api.NewID(), Phase: api.CheckpointRun}
}

// storeOf is the store c is in, which holds kept as want, passes a full
// fsck, and borrows no objects.
func storeOf(t *testing.T, h *Host, c api.CheckpointCaptured, want string) string {
	t.Helper()
	for _, s := range h.checkpoints.stores() {
		if _, err := runGit(context.Background(), s, isolatedGit(), nil, "cat-file", "-e", c.Commit); err != nil {
			continue
		}
		if got := gitOK(t, s, "show", c.Commit+":kept"); got != want {
			t.Fatalf("kept in the checkpoint: %q, want %q", got, want)
		}
		gitOK(t, s, "fsck", "--full", "--no-dangling")
		if _, err := os.Lstat(filepath.Join(s, "objects", "info", "alternates")); !os.IsNotExist(err) {
			t.Fatalf("the store borrows objects: %v", err)
		}
		return s
	}
	t.Fatalf("no store holds %s", c.Commit)
	return ""
}

func holds(store, oid string) bool {
	_, err := runGit(context.Background(), store, isolatedGit(), nil, "cat-file", "-e", oid)
	return err == nil
}

// hashes hashes every file under dir by its path.
func hashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		out[p] = fmt.Sprintf("%x", sha256.Sum256(b))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// wrapGit puts a git first on PATH that runs script, in sh, when one of
// its arguments is arg, and the real git otherwise.
func wrapGit(t *testing.T, arg, script string) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("the git wrapper is a shell script")
	}
	core, err := runGit(context.Background(), "", nil, nil, "--exec-path")
	if err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(core, "git")
	bin := t.TempDir()
	wrapper := fmt.Sprintf("#!/bin/sh\nfor arg do\n if [ \"$arg\" = %q ]; then\n  %s\n fi\ndone\nexec %q \"$@\"\n", arg, script, git)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A blobless clone's checkout fetches nothing for a capture: the store
// isn't seeded from it, and its .git stays as it was, byte for byte.
func TestASeedNeverFetchesIntoAPartialClone(t *testing.T) {
	source := committed(t, map[string][]byte{"omitted": []byte("promised\n")})
	gitOK(t, source, "config", "uploadpack.allowFilter", "true")
	gitOK(t, source, "config", "uploadpack.allowAnySHA1InWant", "true")
	at := t.TempDir()
	gitOK(t, at, "clone", "-q", "--no-local", "--no-checkout", "--filter=blob:none", "file://"+source, "clone")
	ws := filepath.Join(at, "clone")
	gitOK(t, ws, "read-tree", "HEAD")
	writeFile(t, filepath.Join(ws, "kept"), []byte("from HEAD\n"))
	before := hashes(t, filepath.Join(ws, ".git"))

	h := checkpointHost(t)
	thread, effect := runCapture(ws)
	c, err := h.captureIn(context.Background(), thread, effect)
	if err != nil {
		t.Fatal(err)
	}
	storeOf(t, h, c, "from HEAD")
	after := hashes(t, filepath.Join(ws, ".git"))
	for p, sum := range after {
		if before[p] != sum {
			t.Errorf("the capture wrote %s", p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			t.Errorf("the capture removed %s", p)
		}
	}
	if c.Status != api.CheckpointReady || !strings.Contains(c.Reason, "partial clone") {
		t.Fatalf("checkpoint %+v, want ready, saying the store wasn't seeded from a partial clone", c)
	}
}

// A drop's gc that runs out of its budget is killed with every git it
// started, so the drop gives the store's lock back on time.
func TestADropsGCEndsWithinItsBudget(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("git's children outlive it on Windows")
	}
	source := committed(t, nil)
	h := checkpointHost(t)
	gone, first := runCapture(source)
	if _, err := h.captureIn(context.Background(), gone, first); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "large"), randomBytes(t, 32<<20))
	kept, second := runCapture(source)
	if _, err := h.captureIn(context.Background(), kept, second); err != nil {
		t.Fatal(err)
	}
	store := h.checkpoints.stores()[0]
	gitOK(t, store, "config", "pack.threads", "1")
	gitOK(t, store, "config", "core.compression", "9")
	old := dropGCBudget
	dropGCBudget = 80 * time.Millisecond
	defer func() { dropGCBudget = old }()
	began := time.Now()
	if err := h.dropIn(context.Background(), store, gone.ID); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 350*time.Millisecond {
		t.Fatalf("the drop held the store's lock for %v, with a gc budget of %v", took, dropGCBudget)
	}
	ps, err := exec.Command("ps", "-A", "-o", "args=").Output()
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(ps), "\n") {
		if strings.Contains(line, store) {
			t.Errorf("a git of the store outlived the drop: %s", line)
		}
	}
}

// A drop whose gc is cut short leaves the store to be reclaimed: by the
// drop's replay, or by a capture that would find the stores full, and
// until then a full store doesn't tell the user to delete threads.
func TestADropWhoseGCWasCutShortIsReclaimed(t *testing.T) {
	for _, tc := range []struct {
		name string
		// gc is the script of a gc cut short: once, after which a gc gets
		// its full budget, or every time.
		gc   string
		then func(t *testing.T, h *Host, store string, gone api.ID)
	}{
		{"by the drop's replay", "once", func(t *testing.T, h *Host, store string, gone api.ID) {
			if err := h.dropIn(context.Background(), store, gone); err != nil {
				t.Fatal(err)
			}
			if h.checkpoints.total() >= checkpointLimit {
				t.Fatalf("the store holds %d bytes after the drop's replay, as it did before", h.checkpoints.total())
			}
		}},
		{"by a capture of another repository", "once", func(t *testing.T, h *Host, _ string, _ api.ID) {
			other, e := runCapture(committed(t, nil))
			if _, err := h.captureIn(context.Background(), other, e); err != nil {
				t.Fatalf("a capture after a drop's gc was cut short: %v", err)
			}
		}},
		{"until a gc finishes", "always", func(t *testing.T, h *Host, _ string, _ api.ID) {
			other, e := runCapture(committed(t, nil))
			_, err := h.captureIn(context.Background(), other, e)
			want := fmt.Sprintf("Checkpoints are full (%s) while deleted threads' space is reclaimed; a later turn tries again.", readable(checkpointLimit))
			if err == nil || err.Error() != want {
				t.Fatalf("a capture while the stores are full of no thread's checkpoints: %v, want %q", err, want)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := committed(t, nil)
			h := checkpointHost(t)
			gone, first := runCapture(source)
			if _, err := h.captureIn(context.Background(), gone, first); err != nil {
				t.Fatal(err)
			}
			store := h.checkpoints.stores()[0]
			oldLimit := checkpointLimit
			checkpointLimit = h.checkpoints.total()
			defer func() { checkpointLimit = oldLimit }()
			script := "exec sleep 10"
			if tc.gc == "once" {
				ran := filepath.Join(t.TempDir(), "ran")
				script = fmt.Sprintf("[ -e %q ] || { touch %q; exec sleep 10; }", ran, ran)
			}
			wrapGit(t, "--prune=now", script)
			oldBudget := dropGCBudget
			dropGCBudget = 100 * time.Millisecond
			defer func() { dropGCBudget = oldBudget }()
			if err := h.dropIn(context.Background(), store, gone.ID); err != nil {
				t.Fatal(err)
			}
			if got := gitOK(t, store, "for-each-ref"); got != "" {
				t.Fatalf("refs left after the drop: %s", got)
			}
			if h.checkpoints.total() < checkpointLimit {
				t.Fatalf("the store holds %d bytes after a gc cut short, want at least %d", h.checkpoints.total(), checkpointLimit)
			}
			if tc.gc == "once" {
				dropGCBudget = oldBudget
			}
			tc.then(t, h, store, gone.ID)
		})
	}
}

// A store's seed holds only the HEAD versions of the files the capture
// admits, and only while those fit the capture's byte budget with them.
func TestASeedHoldsOnlyWhatTheCaptureAdmits(t *testing.T) {
	oldBytes, oldLimit := maxCheckpointBytes, checkpointLimit
	maxCheckpointBytes, checkpointLimit = 1024, 64<<10
	defer func() { maxCheckpointBytes, checkpointLimit = oldBytes, oldLimit }()
	for _, tc := range []struct {
		name   string
		reason string
		mutate func(t *testing.T, source string)
	}{
		{"a file the worktree no longer has", "", func(t *testing.T, source string) {
			if err := os.Remove(filepath.Join(source, "large")); err != nil {
				t.Fatal(err)
			}
		}},
		{"a file whose HEAD version is over the budget", "HEAD's versions of the files would take the capture over 1024 B, so the store wasn't seeded from HEAD and every file was hashed", func(t *testing.T, source string) {
			writeFile(t, filepath.Join(source, "large"), []byte("now small\n"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := committed(t, map[string][]byte{"large": randomBytes(t, 128<<10)})
			tc.mutate(t, source)
			h := checkpointHost(t)
			thread, effect := runCapture(source)
			c, err := h.captureIn(context.Background(), thread, effect)
			if err != nil {
				t.Fatal(err)
			}
			store := storeOf(t, h, c, "from HEAD")
			if c.Reason != tc.reason {
				t.Fatalf("reason %q, want %q", c.Reason, tc.reason)
			}
			if large := gitOK(t, source, "rev-parse", "HEAD:large"); holds(store, large) {
				t.Fatalf("the store holds HEAD's large, %d bytes in all, for a checkpoint of %d bytes", h.checkpoints.total(), c.Bytes)
			}
			if !holds(store, gitOK(t, source, "rev-parse", "HEAD:kept")) {
				t.Fatal("the store holds no HEAD version of kept")
			}
			other, e := runCapture(committed(t, nil))
			if _, err := h.captureIn(context.Background(), other, e); err != nil {
				t.Fatalf("a capture of another repository after a %d-byte checkpoint: %v", c.Bytes, err)
			}
		})
	}
}
