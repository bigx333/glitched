package runtime

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// WithCheckpoints has the host keep checkpoints of the files of the threads
// that keep them, in bare repositories under root, one per repository the
// threads' worktrees are of. Without it, it keeps none.
func WithCheckpoints(root string) Option {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return func(h *Host) {
		h.checkpoints = &checkpointStores{root: root, locks: map[string]chan struct{}{}, bytes: map[string]int64{}}
	}
}

// The budgets of a capture: all of the base, which the thread's first turn
// waits on, and each git call of a run's, which the next turn waits on. A
// worktree with more files or bytes than these is skipped, as hashing it
// would hold every turn. checkpointLimit is a threshold, not a reservation:
// a capture starts only while the stores hold less, so they hold at most
// it plus what the captures under way add.
var (
	baseBudget               = 5 * time.Second
	gitBudget                = 120 * time.Second
	maxCheckpointFiles       = 50_000
	maxCheckpointBytes int64 = 256 << 20
	checkpointLimit    int64 = 2 << 30
	keptRuns                 = 50 // the run checkpoints a thread keeps
	// seedBudget bounds seeding an empty store from the checkout's HEAD,
	// and dropGCBudget a gc that prunes what a drop let go, which holds
	// the store's lock.
	seedBudget   = 60 * time.Second
	dropGCBudget = 30 * time.Second
	// checkpointHook is called as a store's refs change: after a capture's
	// ref is written, and after a drop's are deleted.
	checkpointHook = func(ctx context.Context, step string) {}
)

// checkpointStores are the host's checkpoint stores, under root. The git
// work on each store runs one at a time, and bytes is what each holds on
// disk, as git last counted it.
type checkpointStores struct {
	root  string
	mu    sync.Mutex
	locks map[string]chan struct{}
	bytes map[string]int64
}

// lock takes store for the caller's git work, or gives up when ctx ends.
// Every write to a store runs under its lock, and one engine owns the
// state folder, so a git lock file found once it is taken is one a killed
// git left, and is removed.
func (s *checkpointStores) lock(ctx context.Context, store string) (unlock func(), err error) {
	s.mu.Lock()
	l := s.locks[store]
	if l == nil {
		l = make(chan struct{}, 1)
		s.locks[store] = l
	}
	s.mu.Unlock()
	select {
	case l <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	os.Remove(filepath.Join(store, "packed-refs.lock"))
	filepath.WalkDir(filepath.Join(store, "refs"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".lock") {
			os.Remove(p)
		}
		return nil
	})
	return func() { <-l }, nil
}

func (s *checkpointStores) stores() []string {
	entries, _ := os.ReadDir(s.root)
	var stores []string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".git") {
			stores = append(stores, filepath.Join(s.root, e.Name()))
		}
	}
	return stores
}

func (s *checkpointStores) total() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, b := range s.bytes {
		n += b
	}
	return n
}

// measure has git count what store holds on disk.
func (s *checkpointStores) measure(ctx context.Context, store string) {
	out, err := storeGit(ctx, store, "", "", nil, "count-objects", "-v")
	if err != nil {
		slog.Warn("runtime: checkpoint store unreadable", "store", store, "err", err)
		return
	}
	var kib int64
	for line := range strings.SplitSeq(out, "\n") {
		k, v, _ := strings.Cut(line, ": ")
		if k == "size" || k == "size-pack" || k == "size-garbage" {
			n, _ := strconv.ParseInt(v, 10, 64)
			kib += n
		}
	}
	s.mu.Lock()
	s.bytes[store] = kib << 10
	s.mu.Unlock()
}

// runGit runs git in dir with env, nil for the engine's own, each call
// within gitBudget. A call that ran out of time, or whose ctx did, returns
// ctx's error once no git it started is left.
func runGit(ctx context.Context, dir string, env []string, stdin []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitBudget)
	defer cancel()
	cmd := gitCommand(ctx, args...)
	cmd.Dir, cmd.Env = dir, env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && ctx.Err() != nil {
		reap(cmd)
		return "", ctx.Err()
	}
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// gitCommand is git with args, in a process group of its own where the
// platform has them, which ctx ending kills whole: a gc starts repack,
// which starts pack-objects, and none may outlive the call, nor hold its
// output open.
func gitCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = time.Second
	ownGroup(cmd)
	return cmd
}

// checkoutGit reads the checkout in dir with its own git and the user's
// config, every ignore source included, but no hook of theirs that writes
// to it, as FSMonitor's, no optional lock, and no fetch of an object a
// partial clone lacks.
func checkoutGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runGit(ctx, dir, checkoutEnv(), nil, append([]string{"-c", "core.fsmonitor=false"}, args...)...)
}

func checkoutEnv() []string {
	return append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1")
}

// storeGit runs git on the checkpoint store, with top as its work tree and
// index as its index when they are given. No git setting of the
// environment's, the user's or the checkout's reaches it, so it reads
// nothing of the checkout's but its files, and writes nothing of it.
func storeGit(ctx context.Context, store, top, index string, stdin []byte, args ...string) (string, error) {
	env := append(isolatedGit(), "GIT_DIR="+store)
	if top != "" {
		env = append(env, "GIT_WORK_TREE="+top)
	}
	if index != "" {
		env = append(env, "GIT_INDEX_FILE="+index)
	}
	return runGit(ctx, top, env, stdin, append([]string{"-c", "core.fsync=committed", "-c", "core.fsyncMethod=batch"}, args...)...)
}

// isolatedGit is the engine's environment without git's variables, with
// no config but the repository's, no lazy fetch, and Glitched as the
// author.
func isolatedGit() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_LAZY_FETCH=1",
		"GIT_AUTHOR_NAME=Glitched", "GIT_AUTHOR_EMAIL=checkpoints@glitched.invalid",
		"GIT_COMMITTER_NAME=Glitched", "GIT_COMMITTER_EMAIL=checkpoints@glitched.invalid")
}

// independent is a context for reading what a capture already wrote, which
// the capture's own budget running out must not hide.
func independent(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), gitBudget)
}

// errSkipped is a capture that a budget or the worktree's shape refuses.
type errSkipped string

func (e errSkipped) Error() string { return string(e) }

func skipf(format string, args ...any) error { return errSkipped(fmt.Sprintf(format, args...)) }

func checkpointRefs(thread api.ID) string { return "refs/glitched/" + thread.String() + "/" }

// checkpointRef is b's ref: a recovery checkpoint's Run is the revert
// that took it.
func checkpointRef(thread api.ID, b engine.CaptureCheckpoint) string {
	switch b.Phase {
	case api.CheckpointBase:
		return checkpointRefs(thread) + "base"
	case api.CheckpointRecovery:
		return checkpointRefs(thread) + "recovery/" + b.Run.String()
	}
	return checkpointRefs(thread) + "run/" + b.Run.String()
}

// capture saves the files of t's worktree as b asks, in the store of its
// repository, and reports what it saved, or why it saved nothing. A
// capture whose ref is there already reports what the ref holds, and
// writes nothing. ok is false when the host closed mid-capture, which
// leaves the effect to the next host.
func (h *Host) capture(t engine.Thread, b engine.CaptureCheckpoint) (o engine.Observation, ok bool) {
	if h.checkpoints == nil {
		return nil, true
	}
	began := time.Now()
	ctx, budget := h.ctx, "a git call took longer than "+gitBudget.String()
	if b.Phase == api.CheckpointBase {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, baseBudget)
		defer cancel()
		budget = "it took longer than " + baseBudget.String()
	}
	c, err := h.captureIn(ctx, t, b)
	if h.ctx.Err() != nil {
		return nil, false
	}
	if err != nil {
		c.Status, c.Reason, c.Ms = api.CheckpointFailed, err.Error(), time.Since(began).Milliseconds()
		var skipped errSkipped
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			c.Status, c.Reason = api.CheckpointSkipped, budget
		case errors.As(err, &skipped):
			c.Status = api.CheckpointSkipped
		}
	}
	return engine.Captured{Checkpoint: c}, true
}

// receipt is the checkpoint b's ref holds, in whichever store has it.
func (h *Host) receipt(ctx context.Context, t engine.Thread, b engine.CaptureCheckpoint) (c api.CheckpointCaptured, found bool, err error) {
	ctx, cancel := independent(ctx)
	defer cancel()
	ref := checkpointRef(t.ID, b)
	for _, store := range h.checkpoints.stores() {
		commit, err := storeGit(ctx, store, "", "", nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
		if err != nil {
			continue
		}
		body, err := storeGit(ctx, store, "", "", nil, "show", "-s", "--format=%b", commit)
		if err == nil {
			err = json.Unmarshal([]byte(body), &c)
		}
		c.Commit = commit
		return c, true, err
	}
	return c, false, nil
}

func (h *Host) captureIn(ctx context.Context, t engine.Thread, b engine.CaptureCheckpoint) (api.CheckpointCaptured, error) {
	began := time.Now()
	if c, found, err := h.receipt(ctx, t, b); found {
		return c, err
	}
	c := api.CheckpointCaptured{Run: b.Run, Phase: b.Phase, Status: api.CheckpointReady}
	// A turn may remove the folder of the worktree it started in, but the
	// worktree is what a checkpoint holds.
	from := worktreeDir(h.worktrees, t.Workspace)
	if h.worktrees == "" || from == "" {
		from = t.Workspace
	}
	at, err := checkoutGit(ctx, from, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "HEAD")
	if errors.Is(err, context.DeadlineExceeded) {
		return c, err
	}
	parts := strings.Split(at, "\n")
	if err != nil || len(parts) != 3 {
		return c, skipf("the workspace isn't in a git repository with a commit")
	}
	top, common, head := parts[0], parts[1], parts[2]
	sum := sha256.Sum256([]byte(common))
	s := h.checkpoints
	store := filepath.Join(s.root, hex.EncodeToString(sum[:8])+".git")
	// What a drop let go and its gc didn't prune is pruned before the
	// store's next capture, and before any capture the stores are too
	// full for.
	for _, pending := range s.stores() {
		if pending != store && s.total() < checkpointLimit {
			continue
		}
		if err := s.reclaim(ctx, pending); err != nil {
			return c, err
		}
	}
	unlock, err := s.lock(ctx, store)
	if err != nil {
		return c, err
	}
	defer unlock()
	if err := initStore(ctx, store); err != nil {
		return c, err
	}
	if s.total() >= checkpointLimit {
		if held, err := s.held(ctx); err != nil || held {
			return c, skipf("Checkpoints are full (%s); delete old threads to free space.", readable(checkpointLimit))
		}
		return c, skipf("Checkpoints are full (%s) while deleted threads' space is reclaimed; a later turn tries again.", readable(checkpointLimit))
	}
	paths, err := checkpointPaths(ctx, top, &c)
	if err != nil {
		return c, err
	}
	// A store with no checkpoint yet takes HEAD's versions of the files
	// this capture holds from the checkout in one pack, so it writes only
	// the files HEAD hasn't got, not one loose object per file. It is a
	// copy: the store borrows nothing of the checkout's, so the
	// checkout's gc never reaches it.
	if any, err := storeGit(ctx, store, "", "", nil, "for-each-ref", "--count=1", "--format=%(refname)", "refs/glitched/"); err != nil {
		return c, err
	} else if any == "" {
		var unseeded errSkipped
		switch err := seedStore(ctx, top, store, paths, c.Bytes); {
		case err != nil && ctx.Err() != nil:
			return c, ctx.Err()
		case errors.As(err, &unseeded):
			c.Reason = err.Error()
		case errors.Is(err, context.DeadlineExceeded):
			c.Reason = "seeding the store from HEAD took longer than " + seedBudget.String() + ", so every file was hashed"
		case err != nil:
			c.Reason = "seeding the store from HEAD failed, so every file was hashed: " + err.Error()
		}
	}
	index := filepath.Join(store, "capture-"+b.Run.String()+"-"+string(b.Phase)+".index")
	os.Remove(index)
	defer os.Remove(index)
	if _, err := storeGit(ctx, store, top, index, nil, "read-tree", "--empty"); err != nil {
		return c, err
	}
	if _, err := storeGit(ctx, store, top, index, paths, "update-index", "--add", "-z", "--stdin"); err != nil {
		return c, err
	}
	tree, err := storeGit(ctx, store, top, index, nil, "write-tree")
	if err != nil {
		return c, err
	}
	refs, err := storeGit(ctx, store, "", "", nil, "for-each-ref", "--format=%(refname)", checkpointRefs(t.ID)+"run/")
	if err != nil {
		return c, err
	}
	runs := runRefs(t, refs)
	ref := checkpointRef(t.ID, b)
	if b.Phase == api.CheckpointRun {
		since := checkpointRefs(t.ID) + "base"
		if len(runs) > 0 {
			since = runs[len(runs)-1]
		}
		prev, err := storeGit(ctx, store, "", "", nil, "rev-parse", "--verify", "--quiet", since+"^{commit}")
		if errors.Is(err, context.DeadlineExceeded) {
			return c, err
		}
		if err == nil {
			diff, err := storeGit(ctx, store, "", "", nil, "diff-tree", "-r", "-z", "--no-renames", "--name-only", prev, tree)
			if err != nil {
				return c, err
			}
			c.Since, c.Changed = prev, strings.Count(diff, "\x00")
		}
		runs = append(runs, ref)
	}
	c.Head, c.Ms = head, time.Since(began).Milliseconds()
	msg, _ := json.Marshal(c)
	commit, err := storeGit(ctx, store, "", "", nil, "commit-tree", "-m", "glitched checkpoint", "-m", string(msg), tree)
	if err != nil {
		return c, err
	}
	update := "create " + ref + " " + commit + "\n"
	for _, old := range runs[:max(0, len(runs)-keptRuns)] {
		update += "delete " + old + "\n"
	}
	if _, err := storeGit(ctx, store, "", "", []byte(update), "update-ref", "--stdin"); err != nil {
		// A call that ran out of time may have written the ref, which is
		// then the checkpoint, as a replay would report it.
		check, cancel := independent(ctx)
		got, _ := storeGit(check, store, "", "", nil, "rev-parse", "--verify", "--quiet", ref)
		cancel()
		if got != commit {
			return c, err
		}
	}
	c.Commit = commit
	checkpointHook(ctx, "captured")
	if ctx.Err() == nil {
		if _, err := storeGit(ctx, store, "", "", nil, "-c", "gc.autoDetach=false", "gc", "--auto", "--quiet"); err != nil {
			slog.Warn("runtime: checkpoint store gc", "store", store, "err", err)
		}
	}
	measure, cancel := independent(ctx)
	s.measure(measure, store)
	cancel()
	return c, nil
}

// seedStore packs HEAD's versions of paths, the files a capture of n
// bytes holds, from the checkout at top, which it only reads, into store,
// within seedBudget. It seeds nothing from a partial clone, whose missing
// objects only a fetch into the checkout would get, nor what would take
// the capture over maxCheckpointBytes.
func seedStore(ctx context.Context, top, store string, paths []byte, n int64) error {
	ctx, cancel := context.WithTimeout(ctx, seedBudget)
	defer cancel()
	promisors, _ := checkoutGit(ctx, top, "config", "--get-regexp", `^(extensions\.partialclone|remote\..*\.promisor)$`)
	for line := range strings.SplitSeq(promisors, "\n") {
		key, value, _ := strings.Cut(line, " ")
		if key != "" && (key == "extensions.partialclone" || !slices.Contains([]string{"false", "no", "off", "0"}, strings.ToLower(value))) {
			return skipf("the repository is a partial clone, so the store wasn't seeded from HEAD and every file was hashed")
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	admitted := map[string]bool{}
	for name := range strings.SplitSeq(string(paths), "\x00") {
		admitted[name] = true
	}
	tree, err := checkoutGit(ctx, top, "ls-tree", "-r", "-z", "-l", "HEAD")
	if err != nil {
		return err
	}
	var blobs []byte
	for entry := range strings.SplitSeq(tree, "\x00") {
		meta, name, _ := strings.Cut(entry, "\t")
		f := strings.Fields(meta)
		if len(f) != 4 || f[1] != "blob" || !admitted[name] {
			continue
		}
		size, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			return err
		}
		if n += size; n > maxCheckpointBytes {
			return skipf("HEAD's versions of the files would take the capture over %s, so the store wasn't seeded from HEAD and every file was hashed", readable(maxCheckpointBytes))
		}
		blobs = append(append(blobs, f[2]...), '\n')
	}
	pack := gitCommand(ctx, "-c", "core.fsmonitor=false", "pack-objects", "--stdout", "-q", "--window=0")
	pack.Dir, pack.Env, pack.Stdin = top, checkoutEnv(), bytes.NewReader(blobs)
	index := gitCommand(ctx, "-c", "core.fsync=committed", "-c", "core.fsyncMethod=batch", "index-pack", "--stdin")
	index.Env = append(isolatedGit(), "GIT_DIR="+store)
	var packErr, indexErr bytes.Buffer
	pack.Stderr, index.Stderr = &packErr, &indexErr
	if index.Stdin, err = pack.StdoutPipe(); err != nil {
		return err
	}
	if err := pack.Start(); err != nil {
		return err
	}
	err = index.Run()
	if waited := pack.Wait(); err == nil {
		err = waited
	}
	if ctx.Err() != nil {
		reap(pack)
		reap(index)
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(packErr.String()+" "+indexErr.String()))
	}
	return nil
}

// readable is n bytes as a person reads it.
func readable(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%d GiB", n>>30)
	case n >= 1<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%d B", n)
}

// initStore makes store a bare repository whose attributes turn off every
// filter, so that a checkpoint holds the files' bytes as they are on disk.
// It checks the attributes every time: an engine killed between the two
// leaves a store without them.
func initStore(ctx context.Context, store string) error {
	if _, err := os.Stat(filepath.Join(store, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
			return err
		}
		if _, err := runGit(ctx, "", isolatedGit(), nil, "init", "--bare", "--quiet", store); err != nil {
			return err
		}
	}
	attributes, want := filepath.Join(store, "info", "attributes"), []byte("* -text -filter -ident -working-tree-encoding\n")
	if got, err := os.ReadFile(attributes); err == nil && bytes.Equal(got, want) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(attributes), 0o755); err != nil {
		return err
	}
	return os.WriteFile(attributes, want, 0o644)
}

// checkpointPaths lists the files a checkpoint of top holds, NUL after
// each, as the checkout's own git lists them, with each of the user's
// ignore sources: tracked files, ignored or not, and untracked ones that
// aren't ignored. It counts them into c. A submodule, a special file, a
// file under a symlinked folder, or more files or bytes than the budget
// skip the capture. A folder listed is a repository nested in top, left
// out. A name is git's, so a case-only rename on a disk that ignores case
// keeps the spelling git has.
func checkpointPaths(ctx context.Context, top string, c *api.CheckpointCaptured) ([]byte, error) {
	staged, err := checkoutGit(ctx, top, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	for entry := range strings.SplitSeq(staged, "\x00") {
		if strings.HasPrefix(entry, "160000 ") {
			_, path, _ := strings.Cut(entry, "\t")
			return nil, skipf("it has a submodule at %s", path)
		}
	}
	listed, err := checkoutGit(ctx, top, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	var paths []byte
	seen := map[string]bool{} // a conflicted file is listed once per stage
	linked := map[string]bool{}
	for name := range strings.SplitSeq(listed, "\x00") {
		if name == "" || strings.HasSuffix(name, "/") || seen[name] {
			continue
		}
		seen[name] = true
		for dir := filepath.Dir(name); dir != "."; dir = filepath.Dir(dir) {
			is, known := linked[dir]
			if !known {
				fi, err := os.Lstat(filepath.Join(top, dir))
				is = err == nil && fi.Mode()&os.ModeSymlink != 0
				linked[dir] = is
			}
			if is {
				return nil, skipf("%s is a symlink, so the files under it can't be saved", dir)
			}
		}
		fi, err := os.Lstat(filepath.Join(top, name))
		switch {
		case errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
			continue
		case err != nil:
			return nil, err
		case fi.IsDir():
			continue
		case !fi.Mode().IsRegular() && fi.Mode()&os.ModeSymlink == 0:
			return nil, skipf("%s is neither a file nor a symlink", name)
		}
		c.Files++
		c.Bytes += fi.Size()
		if c.Files > maxCheckpointFiles {
			return nil, skipf("it holds more than %d files", maxCheckpointFiles)
		}
		if c.Bytes > maxCheckpointBytes {
			return nil, skipf("it holds more than %d MiB", maxCheckpointBytes>>20)
		}
		paths = append(append(paths, name...), 0)
	}
	return paths, nil
}

// runRefs are the run checkpoints of t that listed names, in the order t
// ran them, which isn't their IDs': a run's ID is its message's, and a
// message can wait behind one sent after it.
func runRefs(t engine.Thread, listed string) []string {
	at := map[string]int{}
	for i, r := range t.Runs {
		at[r.ID.String()] = i
	}
	pos := func(ref string) int {
		if i, ok := at[ref[strings.LastIndexByte(ref, '/')+1:]]; ok {
			return i
		}
		return -1
	}
	var refs []string
	for ref := range strings.SplitSeq(listed, "\n") {
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	slices.SortStableFunc(refs, func(a, b string) int { return cmp.Compare(pos(a), pos(b)) })
	return refs
}

// dropCheckpoints deletes thread's checkpoints from every store, retrying
// until git does, and has git prune what no checkpoint holds any longer.
// ok is false when the host closed first.
func (h *Host) dropCheckpoints(thread api.ID) (ok bool) {
	if h.checkpoints == nil {
		return true
	}
	for _, store := range h.checkpoints.stores() {
		if !retry(h.ctx, "drop checkpoints", func(ctx context.Context) error { return h.dropIn(ctx, store, thread) }) {
			return false
		}
	}
	return true
}

func (h *Host) dropIn(ctx context.Context, store string, thread api.ID) error {
	unlock, err := h.checkpoints.lock(ctx, store)
	if err != nil {
		return err
	}
	defer unlock()
	listed, err := storeGit(ctx, store, "", "", nil, "for-each-ref", "--format=delete %(refname)", checkpointRefs(thread))
	if err != nil {
		return err
	}
	if listed != "" {
		if err := leavePending(store); err != nil {
			return err
		}
		if _, err := storeGit(ctx, store, "", "", []byte(listed+"\n"), "update-ref", "--stdin"); err != nil {
			return err
		}
	}
	checkpointHook(ctx, "dropped")
	h.checkpoints.prune(ctx, store)
	return nil
}

// pendingGC is the file a drop leaves in store, before it deletes any
// ref, until a gc has pruned what the drop let go.
func pendingGC(store string) string { return filepath.Join(store, "glitched-gc-pending") }

func leavePending(store string) error {
	f, err := os.Create(pendingGC(store))
	if err != nil {
		return err
	}
	err = f.Sync()
	if closed := f.Close(); err == nil {
		err = closed
	}
	return err
}

// reclaim prunes what a drop let go in store, if a drop left it to,
// under the store's lock.
func (s *checkpointStores) reclaim(ctx context.Context, store string) error {
	if _, err := os.Stat(pendingGC(store)); err != nil {
		return nil
	}
	unlock, err := s.lock(ctx, store)
	if err != nil {
		return err
	}
	defer unlock()
	s.prune(ctx, store)
	return nil
}

// prune has git prune what no ref of store's holds, if a drop left it to,
// and counts the store again. The caller holds the store's lock, so no
// capture is under way, and what no ref holds is what drops let go. The
// gc holds that lock, and the next capture in the store waits on it, so
// it gets dropGCBudget: one cut short leaves the store pending, for the
// next drop or capture to prune.
func (s *checkpointStores) prune(ctx context.Context, store string) {
	if _, err := os.Stat(pendingGC(store)); err != nil {
		return
	}
	gc, cancel := context.WithTimeout(ctx, dropGCBudget)
	_, err := storeGit(gc, store, "", "", nil, "-c", "gc.autoDetach=false", "gc", "--quiet", "--prune=now")
	cancel()
	if err == nil {
		err = os.Remove(pendingGC(store))
	}
	if err != nil && ctx.Err() == nil {
		slog.Warn("runtime: checkpoint store gc", "store", store, "err", err)
	}
	s.measure(ctx, store)
}

// held is whether any store holds a checkpoint.
func (s *checkpointStores) held(ctx context.Context) (bool, error) {
	for _, store := range s.stores() {
		any, err := storeGit(ctx, store, "", "", nil, "for-each-ref", "--count=1", "--format=%(refname)", "refs/glitched/")
		if err != nil || any != "" {
			return true, err
		}
	}
	return false, nil
}
