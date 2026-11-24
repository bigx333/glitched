//go:build !windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"glitched/internal/api"
)

// entry is a file, a symlink or a folder of a tree, as a path holds it.
type entry struct {
	kind string // "file", "link" or "dir"
	exec bool
	text string
}

func file(text string) entry { return entry{kind: "file", text: text} }

// recovered is the worktree a revert's recovery checkpoint holds, and
// target the one it reverts to: a file edited, added and deleted, a folder
// that becomes a file, a file and a symlink that become folders, a folder
// that becomes a symlink, an executable bit set, and tracked and ignored
// files the revert leaves.
var (
	recovered = map[string]entry{
		".gitignore":     file("*.tmp\n"),
		"edit.txt":       file("R edit\n"),
		"gone.txt":       file("R gone\n"),
		"dir2file/a":     file("R a\n"),
		"dir2file/sub/b": file("R b\n"),
		"file2dir":       file("R f2d\n"),
		"link2dir":       {kind: "link", text: "edit.txt"},
		"dir2link/e":     file("R e\n"),
		"exec.sh":        file("#!/bin/sh\n"),
		"keep.txt":       file("R keep\n"),
		"scratch.tmp":    file("R scratch, ignored\n"),
	}
	target = map[string]entry{
		".gitignore":  file("*.tmp\n"),
		"edit.txt":    file("T edit\n"),
		"add.txt":     file("T add\n"),
		"dir2file":    file("T d2f\n"),
		"file2dir/c":  file("T c\n"),
		"link2dir/d":  file("T d\n"),
		"dir2link":    {kind: "link", text: "add.txt"},
		"exec.sh":     {kind: "file", exec: true, text: "#!/bin/sh\n"},
		"keep.txt":    file("R keep\n"),
		"scratch.tmp": file("R scratch, ignored\n"),
	}
	folders = []string{"dir2file", "dir2file/sub", "file2dir", "link2dir", "dir2link"}
)

// lay writes tree into dir.
func lay(t *testing.T, dir string, tree map[string]entry) {
	t.Helper()
	must(os.MkdirAll(dir, 0o755))
	for rel, e := range tree {
		must(place(filepath.Join(dir, rel), e))
	}
}

// place writes e at p, with the folders above it.
func place(p string, e entry) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	switch e.kind {
	case "link":
		return os.Symlink(e.text, p)
	case "dir":
		return os.Mkdir(p, 0o755)
	}
	mode := os.FileMode(0o644)
	if e.exec {
		mode = 0o755
	}
	if err := os.WriteFile(p, []byte(e.text), mode); err != nil {
		return err
	}
	return os.Chmod(p, mode)
}

// filesIn is every file, symlink and folder under dir.
func filesIn(t *testing.T, dir string) map[string]entry {
	t.Helper()
	got := map[string]entry{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		e, err := entryAt(p)
		got[filepath.ToSlash(rel)] = e
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// entryAt is what p holds.
func entryAt(p string) (entry, error) {
	fi, err := os.Lstat(p)
	switch {
	case err != nil:
		return entry{}, err
	case fi.IsDir():
		return entry{kind: "dir"}, nil
	case fi.Mode()&os.ModeSymlink != 0:
		to, err := os.Readlink(p)
		return entry{kind: "link", text: to}, err
	}
	data, err := os.ReadFile(p)
	return entry{kind: "file", exec: fi.Mode().Perm()&0o100 != 0, text: string(data)}, err
}

// withFolders is tree with each folder above its files.
func withFolders(tree map[string]entry) map[string]entry {
	out := map[string]entry{}
	for p, e := range tree {
		out[p] = e
		for d := filepath.Dir(p); d != "."; d = filepath.Dir(d) {
			out[filepath.ToSlash(d)] = entry{kind: "dir"}
		}
	}
	return out
}

// commitOf is the commit of tree in store, as a capture would take it.
func commitOf(t *testing.T, ctx context.Context, store string, tree map[string]entry) string {
	t.Helper()
	dir := t.TempDir()
	lay(t, dir, tree)
	index := filepath.Join(t.TempDir(), "index")
	if _, err := storeGit(ctx, store, dir, index, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	id, err := storeGit(ctx, store, dir, index, nil, "write-tree")
	if err == nil {
		id, err = storeGit(ctx, store, "", "", nil, "commit-tree", "-m", "fixture", id)
	}
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// act is something the user does at a path p, while the host is down or
// as recovery reaches a step. It reports whether it applied there.
type act struct {
	name string
	do   func(p, rel, mark string) bool
}

// Each mark the user writes is the cell's own, so no step of a revert can
// write it; the acts that write a checkpoint's bytes write exactly those.
var acts = []act{
	{"creates a file", func(p, _, mark string) bool {
		return absent(p) && place(p, file(mark)) == nil
	}},
	{"edits a file", func(p, _, mark string) bool {
		return isFile(p) && os.WriteFile(p, []byte(mark), 0o644) == nil
	}},
	{"replaces a file with a folder", func(p, _, mark string) bool {
		if fi, err := os.Lstat(p); err != nil || fi.IsDir() {
			return false
		}
		must(os.Remove(p))
		return place(filepath.Join(p, "user.tmp"), file(mark)) == nil
	}},
	{"replaces a folder with a file", func(p, _, mark string) bool {
		if fi, err := os.Lstat(p); err != nil || !fi.IsDir() {
			return false
		}
		must(os.RemoveAll(p))
		return place(p, file(mark)) == nil
	}},
	{"replaces it with a symlink", func(p, _, mark string) bool {
		if _, err := os.Lstat(p); err != nil {
			return false
		}
		must(os.RemoveAll(p))
		return os.Symlink(mark, p) == nil
	}},
	{"writes the target's exact bytes", func(p, rel, _ string) bool { return rewrite(p, target[rel]) }},
	{"writes the recovery's exact bytes", func(p, rel, _ string) bool { return rewrite(p, recovered[rel]) }},
	{"changes only the mode", func(p, _, _ string) bool {
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			return false
		}
		return os.Chmod(p, fi.Mode().Perm()^0o111) == nil
	}},
	{"makes an empty folder", func(p, _, _ string) bool {
		return absent(p) && os.MkdirAll(p, 0o755) == nil
	}},
	{"truncates it to an empty file", func(p, _, _ string) bool {
		fi, err := os.Lstat(p)
		return err == nil && fi.Mode().IsRegular() && fi.Size() > 0 && os.Truncate(p, 0) == nil
	}},
}

// rewrite writes e over the file or symlink at p, or where p is absent,
// once the bytes there differ: none where a tree has no file at the path.
func rewrite(p string, e entry) bool {
	if e.kind == "" || !absent(p) && !isLeaf(p) {
		return false
	}
	if now, err := entryAt(p); err == nil && now == e {
		return false
	}
	if !absent(p) {
		must(os.Remove(p))
	}
	return place(p, e) == nil
}

func absent(p string) bool {
	_, err := os.Lstat(p)
	return errors.Is(err, fs.ErrNotExist) && !blockedAbove(p)
}

func isFile(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

func isLeaf(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && !fi.IsDir()
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// blockedAbove is whether a folder above p is a file or a symlink, where
// nothing can be made.
func blockedAbove(p string) bool {
	for dir := filepath.Dir(p); ; dir = filepath.Dir(dir) {
		if fi, err := os.Lstat(dir); err == nil {
			return !fi.IsDir()
		}
		if filepath.Dir(dir) == dir {
			return false
		}
	}
}

// copyTree copies the folders, files and symlinks of from into to, with
// their modification times, as a revert's journal records them.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		e, err := entryAt(p)
		if err != nil {
			return err
		}
		if e.kind == "dir" {
			return os.MkdirAll(dst, 0o755)
		}
		if err := place(dst, e); err != nil {
			return err
		}
		ts := unix.NsecToTimespec(fi.ModTime().UnixNano())
		return unix.UtimesNanoAt(unix.AT_FDCWD, dst, []unix.Timespec{ts, ts}, unix.AT_SYMLINK_NOFOLLOW)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// stepKey holds a cell's hook in its context: the host's hook is global,
// and cells run at once.
type stepKey struct{}

// A revert's recovery never removes or replaces what it didn't write. The
// host stops at each step of a revert, or recovery reaches each of its own
// steps, and the user does each of acts at each path the revert touches,
// each folder, and a file in each folder; then the replay's rewind is
// refused and recovery puts the files back. Every byte the user wrote is
// still there after. Either recovery succeeded, every path the plan names
// is as the recovery checkpoint has it and the tree is as the checkpoint
// holds it everywhere but where the user acted, or it failed naming the
// path the user acted at.
func TestRecoveryKeepsEveryByteTheUserWrote(t *testing.T) {
	ctx := context.Background()
	defer SetCheckpointHook(func(ctx context.Context, step string) {
		if f, ok := ctx.Value(stepKey{}).(func(string)); ok {
			f(step)
		}
	})()
	root := t.TempDir()
	store := filepath.Join(root, "store.git")
	if err := initStore(ctx, store); err != nil {
		t.Fatal(err)
	}
	h := &Host{}
	WithCheckpoints(root)(h)
	recovery, to := commitOf(t, ctx, store, recovered), commitOf(t, ctx, store, target)
	changes, err := diffTrees(ctx, store, recovery, to)
	if err != nil {
		t.Fatal(err)
	}
	thread, planned := api.NewID(), api.NewID()
	template := filepath.Join(root, "template")
	lay(t, template, recovered)
	if _, err := h.markApplying(ctx, store, template, thread, planned, recovery, changes); err != nil {
		t.Fatal(err)
	}
	var planPaths, targets []string
	for _, c := range changes {
		planPaths = append(planPaths, c.path)
	}
	targets = slices.Clone(planPaths)
	for _, f := range folders {
		if !slices.Contains(targets, f) {
			targets = append(targets, f)
		}
		targets = append(targets, f+"/user.tmp")
	}
	// undoing is the paths of recovery's steps in the order it takes
	// them: the plan's with its sides swapped, deletes first.
	var undoing []string
	for _, deleting := range []bool{true, false} {
		for _, c := range changes {
			if (c.was == "") == deleting {
				undoing = append(undoing, c.path)
			}
		}
	}

	// The revert, stopped at each of its hooks: -3 before the plan, -2
	// after it, k at its kth hook, -1 after the last.
	type stopped struct {
		dir, at string
		revert  api.ID
	}
	states := map[int]stopped{}
	// forward runs the revert into a fresh copy of the recovery tree, cut
	// at its hook stop, or to the end where stop is -1.
	forward := func(stop int) (stopped, int) {
		s := stopped{dir: filepath.Join(root, "stopped", fmt.Sprint(stop)), revert: api.NewID()}
		lay(t, s.dir, recovered)
		steps := 0
		cut, cancel := context.WithCancel(ctx)
		defer cancel()
		cut = context.WithValue(cut, stepKey{}, func(step string) {
			if steps == stop {
				s.at = step
				cancel()
			}
			steps++
		})
		if err := restore(cut, store, s.dir, s.revert, to, changes); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("the revert, cut at its hook %d: %v", stop, err)
		}
		return s, steps
	}
	for _, stop := range []int{-3, -2} {
		s := stopped{dir: filepath.Join(root, "stopped", fmt.Sprint(stop)), revert: api.NewID()}
		lay(t, s.dir, recovered)
		states[stop] = s
	}
	whole, hooks := forward(-1)
	states[-1] = whole
	for stop := range hooks {
		states[stop], _ = forward(stop)
	}

	// still is the control: the user does nothing.
	still := act{"does nothing", func(string, string, string) bool { return true }}
	type cell struct {
		stop  int    // the revert's stop, a key of states
		race  int    // recovery's step the user acts at, or -1 while the host is down
		path  string // where the user acts
		act   act
		ran   bool
		lost  string   // what broke the rule, if anything
		named []string // the paths a failed recovery named, sorted
	}
	var cells []*cell
	for stop := range states {
		cells = append(cells, &cell{stop: stop, race: -1, path: ".", act: still})
		for _, p := range targets {
			for _, a := range acts {
				cells = append(cells, &cell{stop: stop, race: -1, path: p, act: a})
			}
		}
		if stop == -3 {
			continue
		}
		for step, at := range undoing {
			for _, p := range targets {
				if p != at && !strings.HasPrefix(p, at+"/") && !strings.HasPrefix(at, p+"/") {
					continue
				}
				for _, a := range acts {
					cells = append(cells, &cell{stop: stop, race: step, path: p, act: a})
				}
			}
		}
	}
	slices.SortStableFunc(cells, func(a, b *cell) int { return a.stop - b.stop })

	// The controls run first: where a crash cut a change off from its
	// record, recovery refuses with nothing changed, and a cell may fail
	// naming just what its control names, as the user's act was no reason.
	controls := map[int][]string{}
	want := withFolders(recovered)
	run := func(i int, c *cell) {
		s := states[c.stop]
		dir := filepath.Join(root, "w", fmt.Sprint(i))
		copyTree(t, s.dir, dir)
		revert := api.NewID()
		if data, err := os.ReadFile(journalOf(store, s.revert)); err == nil {
			must(os.MkdirAll(filepath.Dir(journalOf(store, revert)), 0o700))
			must(os.WriteFile(journalOf(store, revert), data, 0o600))
		}
		mark := fmt.Sprintf("the user's own, cell %d\n", i)
		var left map[string]entry
		under := func(p string) bool { return c.path != "." && (p == c.path || strings.HasPrefix(p, c.path+"/")) }
		doAct := func() {
			if c.ran = c.act.do(filepath.Join(dir, c.path), c.path, mark); c.ran {
				left = without(filesIn(t, dir), func(p string) bool { return !under(p) })
			}
		}
		if c.race == -1 {
			if doAct(); !c.ran {
				return
			}
		}
		var err error
		if c.stop != -3 {
			back, plan, read := readPlan(ctx, store, applyingRef(thread, planned))
			if read != nil {
				t.Errorf("the plan: %v", read)
				return
			}
			steps := 0
			undoCtx := context.WithValue(ctx, stepKey{}, func(step string) {
				if step == "journaling" {
					return
				}
				if steps == c.race {
					doAct()
				}
				steps++
			})
			err = undo(undoCtx, store, dir, revert, back, plan)
		}
		if !c.ran {
			return
		}
		now := filesIn(t, dir)
		for p, e := range left {
			if got, ok := now[p]; !ok || got != e {
				c.lost = fmt.Sprintf("the user left %+v at %s, and it is now %+v (recovery said %v)", e, p, got, err)
				return
			}
		}
		for p := range now {
			if _, ok := left[p]; under(p) && !ok {
				c.lost = fmt.Sprintf("%s holds %+v, which the user didn't leave there (recovery said %v)", p, now[p], err)
				return
			}
		}
		if err != nil {
			msg, _, _ := strings.Cut(err.Error(), " changed since the revert began")
			c.named = strings.Split(msg, ", ")
			slices.Sort(c.named)
			switch {
			case c.act.name == still.name && s.at != "journaling":
				c.lost = "recovery failed with nothing changed since: " + err.Error()
			case c.act.name == still.name:
			case !slices.Contains(c.named, c.path) && !slices.Equal(c.named, controls[c.stop]):
				c.lost = "recovery failed without naming " + c.path + ": " + err.Error()
			}
			return
		}
		for p, e := range want {
			if got, ok := now[p]; !under(p) && (!ok || got != e) {
				c.lost = fmt.Sprintf("recovery reported success, but %s is %+v, not recovery's %+v", p, got, e)
				return
			}
		}
		for p, e := range now {
			if _, ok := want[p]; !under(p) && !ok {
				c.lost = fmt.Sprintf("recovery reported success, but %s holds %+v, which recovery hasn't", p, e)
				return
			}
		}
		if c.stop == -3 {
			return
		}
		for step, p := range undoing {
			got, ok := now[p]
			if w, has := want[p]; step >= c.race && (has != ok || has && w != got) {
				c.lost = fmt.Sprintf("recovery reported success, but the plan's %s, which it reached after the user acted, is %+v, not recovery's %+v", p, got, w)
				return
			}
		}
	}
	start := time.Now()
	for _, control := range []bool{true, false} {
		work := make(chan int)
		var wg sync.WaitGroup
		for range runtime.NumCPU() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range work {
					run(i, cells[i])
				}
			}()
		}
		for i, c := range cells {
			if (c.act.name == still.name) == control {
				work <- i
			}
		}
		close(work)
		wg.Wait()
		for _, c := range cells {
			if control && c.act.name == still.name && c.named != nil {
				controls[c.stop] = c.named
			}
		}
	}

	ran, broke, failed := 0, 0, 0
	by := map[string]int{}
	var first []string
	shown := map[string]int{}
	for _, c := range cells {
		if !c.ran {
			continue
		}
		ran++
		if c.named != nil {
			failed++
		}
		if c.lost == "" {
			continue
		}
		broke++
		kind := "as recovery ran"
		switch {
		case c.act.name == still.name:
			kind = "with nothing changed"
		case c.race == -1:
			kind = "while the host was down"
		}
		by[kind]++
		line := fmt.Sprintf("stop %d (%s), race %d: the user %s at %s: %s", c.stop, states[c.stop].at, c.race, c.act.name, c.path, c.lost)
		t.Log("broke: " + line)
		if shown[kind]++; shown[kind] <= 4 {
			first = append(first, line)
		}
	}
	t.Logf("%d cells of %d ran in %v (%d stops, %d recovery steps, %d paths, %d acts); %d failed, %d broke the rule %v", ran, len(cells), time.Since(start).Round(time.Second), len(states), len(undoing), len(targets), len(acts), failed, broke, by)
	if broke > 0 {
		t.Errorf("%d of %d cells broke the rule; the first:\n%s", broke, ran, strings.Join(first, "\n"))
	}
}

// without is tree less the paths skip is true of.
func without(tree map[string]entry, skip func(string) bool) map[string]entry {
	out := map[string]entry{}
	for p, e := range tree {
		if !skip(p) {
			out[p] = e
		}
	}
	return out
}
