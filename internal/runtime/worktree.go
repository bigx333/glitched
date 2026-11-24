package runtime

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/plugins/guard"
)

// WithWorktrees has the host make the worktrees threads ask for under
// root, one folder per repository. Without it, it makes none.
func WithWorktrees(root string) Option { return func(h *Host) { h.worktrees = root } }

// git runs git in dir and returns its output trimmed, or its complaint.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	return gitWith(ctx, nil, dir, args...)
}

// gitWith is git run in env; nil is the engine's own.
func gitWith(ctx context.Context, env []string, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// worktreeNames are the branches and the folder of thread's worktree.
// The folder is named by the whole of the thread's ID, so no other
// thread's is ever taken for it. The branch is named by the title and the
// ID's end, short, or by the whole ID, long, where another has the short.
func worktreeNames(root, top string, thread api.ID, title string) (short, long, dir string) {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if b.Len() >= 40 {
			break
		}
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "thread"
	}
	id := strings.ToLower(thread.String())
	sum := sha256.Sum256([]byte(top))
	return "glitched/" + slug + "-" + id[len(id)-6:], "glitched/" + slug + "-" + id, filepath.Join(root, filepath.Base(top)+"-"+hex.EncodeToString(sum[:4]), id)
}

// worktreeDir is the folder of the worktree a thread in one runs in: the
// one under root its workspace is in, "" for a workspace not under root.
func worktreeDir(root, workspace string) string {
	rel, err := filepath.Rel(root, workspace)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(rel, string(filepath.Separator), 3)
	if len(parts) < 2 || parts[0] == ".." {
		return ""
	}
	return filepath.Join(root, parts[0], parts[1])
}

// within is whether path is dir or in it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// createInWorktree makes the worktree c asks for and commits the thread
// in it under c's receipt. A create already committed gets its receipt
// with no git. One the engine would refuse is refused before git runs;
// one refused after leaves the worktree for its changes, unless it has
// none.
func (h *Host) createInWorktree(ctx context.Context, c api.Command, b api.ThreadCreate) (api.Receipt, error) {
	h.making.Lock()
	defer h.making.Unlock()
	if done, err := h.store.Committed(ctx, c.ID); err != nil || done {
		if err != nil {
			return api.Receipt{}, err
		}
		return h.store.Submit(ctx, c, time.Now())
	}
	refuse := func(format string, args ...any) (api.Receipt, error) {
		return api.Receipt{}, &api.RejectedError{Reason: "Can't make a worktree: " + fmt.Sprintf(format, args...)}
	}
	if h.worktrees == "" {
		return refuse("this engine makes none")
	}
	if b.Project != "" || b.Branch != "" {
		return refuse("a thread asks for a worktree from its folder alone")
	}
	// A child's worktree starts from the HEAD of its parent's folder, and
	// belongs to the parent's project, the folder a person chose, never
	// to a worktree under the engine's own folder that may go first.
	project := b.Workspace
	if !b.Parent.IsZero() {
		parent, err := h.store.Thread(ctx, b.Parent)
		if err != nil {
			return api.Receipt{}, err
		}
		if parent.Exists() && b.Workspace != parent.Workspace {
			return refuse("a child's worktree starts from its parent's folder")
		}
		project = cmp.Or(parent.Project, parent.Workspace)
		if parent.Project == "" && worktreeDir(canon(h.worktrees), canon(parent.Workspace)) != "" {
			return refuse("the parent runs in this engine's worktrees with no project of its own; start it from its project's folder")
		}
	}
	// A fork from its source's own worktree belongs to the source's
	// project too, which outlives that worktree.
	if b.Fork != nil {
		src, err := h.store.Thread(ctx, b.Fork.Thread)
		if err != nil {
			return api.Receipt{}, err
		}
		if src.Exists() && canon(b.Workspace) == canon(src.Workspace) && src.Project != "" {
			project = src.Project
		}
	}
	target, err := h.store.Thread(ctx, c.Target)
	if err != nil {
		return api.Receipt{}, err
	}
	dry := b
	dry.Worktree, dry.Project, dry.Branch = nil, project, "glitched/dry"
	if b.Fork != nil {
		if dry.Forked, err = h.store.CheckFork(ctx, b); err != nil {
			return api.Receipt{}, err
		}
	}
	if _, _, err := engine.Decide(target, api.Command{ID: c.ID, Target: c.Target, Revision: c.Revision, Expires: c.Expires, Body: dry}, time.Now()); err != nil {
		return api.Receipt{}, err
	}
	if !b.Parent.IsZero() {
		if err := h.store.CheckChild(ctx, dry); err != nil {
			return api.Receipt{}, err
		}
	}
	run := git
	if b.Worktree.Guarded {
		hooks, err := os.MkdirTemp("", "glitched-nohooks-")
		if err != nil {
			return refuse("%v", err)
		}
		defer os.RemoveAll(hooks)
		env := append(os.Environ(), guard.GitEnv(hooks)...)
		run = func(ctx context.Context, dir string, args ...string) (string, error) {
			return gitWith(ctx, env, dir, args...)
		}
	}
	top, err := run(ctx, b.Workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return refuse("%s is not in a git repository", b.Workspace)
	}
	// The folder is named by the project's repository, so a child's sits
	// beside its parent's and the project's other threads'.
	named := top
	if project != b.Workspace {
		if named, err = run(ctx, project, "rev-parse", "--show-toplevel"); err != nil {
			return refuse("%s is not in a git repository", project)
		}
	}
	rel := "."
	if ws, err := filepath.EvalSymlinks(b.Workspace); err == nil && within(top, ws) {
		rel, _ = filepath.Rel(top, ws)
	}
	short, long, dir := worktreeNames(h.worktrees, named, c.Target, b.Title)
	branch, base := "", ""
	if _, err := os.Stat(dir); err == nil {
		common, _ := run(ctx, top, "rev-parse", "--path-format=absolute", "--git-common-dir")
		own, ok := ownedBy(ctx, run, dir, common, c.Target)
		if _, err := os.Stat(filepath.Join(own, "locked")); own != "" && err == nil {
			return refuse("the worktree at %s was never finished; it stays for what is in it", dir)
		}
		if !ok {
			return refuse("%s is there, and isn't the thread's worktree of %s", dir, top)
		}
		if branch, _ = run(ctx, dir, "branch", "--show-current"); branch != short && branch != long {
			return refuse("%s is already there and isn't on the thread's branch", dir)
		}
	} else {
		if _, err := run(ctx, top, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
			return refuse("the repository at %s has no commits yet", top)
		}
		from := b.Worktree.Base
		if from == "" {
			from = "HEAD"
		}
		commit, err := run(ctx, top, "rev-parse", "--verify", "--quiet", from+"^{commit}")
		if err != nil {
			return refuse("the repository has no branch or commit %q", from)
		}
		if rel != "." {
			if kind, err := run(ctx, top, "cat-file", "-t", commit+":"+filepath.ToSlash(rel)); err != nil || kind != "tree" {
				return refuse("%s is not a folder on %s", rel, from)
			}
		}
		branch = short
		if _, err := run(ctx, top, "rev-parse", "--verify", "--quiet", "refs/heads/"+short); err == nil {
			branch = long
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return refuse("%v", err)
		}
		// Locked until made, so that a retry can tell a worktree git
		// finished from one it stopped in.
		if _, err := run(ctx, top, "worktree", "add", "--lock", "--reason", "glitched: making", "--no-track", "-b", branch, dir, commit); err != nil {
			return refuse("%v", err)
		}
		base = commit
		own, err := run(ctx, dir, "rev-parse", "--absolute-git-dir")
		if err == nil {
			err = os.WriteFile(filepath.Join(own, ownerFile), []byte(c.Target.String()), 0o644)
		}
		if err == nil {
			_, err = run(ctx, top, "worktree", "unlock", dir)
		}
		if err != nil {
			return refuse("%v", err)
		}
	}
	workspace := filepath.Join(dir, rel)
	realDir, _ := filepath.EvalSymlinks(dir)
	if real, err := filepath.EvalSymlinks(workspace); err != nil || !within(realDir, real) {
		return refuse("%s in the worktree at %s isn't a folder of it; the worktree stays for what is in it", rel, dir)
	} else if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return refuse("%s in the worktree at %s isn't a folder; the worktree stays for what is in it", rel, dir)
	}
	in := b
	in.Project, in.Branch, in.Workspace, in.Worktree = project, branch, workspace, nil
	r, err := h.store.SubmitResolved(ctx, c, in, time.Now())
	if err == nil || base == "" {
		return r, err
	}
	// The create was refused after this call made the worktree, as a
	// child past the limits another took the last place under: it goes,
	// with its branch, while nothing in it is new.
	status, serr := run(ctx, dir, "status", "--porcelain", "--ignored")
	head, herr := run(ctx, dir, "rev-parse", "HEAD")
	if serr != nil || herr != nil || status != "" || head != base {
		slog.Warn("runtime: a refused thread's worktree stays for what is in it", "worktree", dir, "err", err)
		return r, err
	}
	if _, rerr := run(ctx, top, "worktree", "remove", dir); rerr != nil {
		slog.Warn("runtime: a refused thread's worktree stays", "worktree", dir, "err", rerr)
		return r, err
	}
	if _, rerr := run(ctx, top, "branch", "-d", branch); rerr != nil {
		slog.Warn("runtime: a refused thread's branch stays", "branch", branch, "err", rerr)
	}
	return r, err
}

// ownerFile, in a worktree's own git folder, names the thread it was made
// for. Git makes that folder anew for every worktree it adds, even at a
// path it used before, so the file goes with the worktree it was written
// for.
const ownerFile = "glitched-thread"

// ownedBy is whether dir is a worktree of the repository whose git folder
// is common, made for thread and registered by git at dir, and its own
// git folder.
func ownedBy(ctx context.Context, git func(context.Context, string, ...string) (string, error), dir, common string, thread api.ID) (string, bool) {
	own, ok := worktreeAt(ctx, git, dir, common)
	owner, _ := os.ReadFile(filepath.Join(own, ownerFile))
	return own, ok && string(owner) == thread.String()
}

// worktreeAt is whether dir is a worktree of the repository whose git
// folder is common, registered by git at dir, and its own git folder.
func worktreeAt(ctx context.Context, git func(context.Context, string, ...string) (string, error), dir, common string) (string, bool) {
	top, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	own, _ := git(ctx, dir, "rev-parse", "--absolute-git-dir")
	theirs, _ := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	real, _ := filepath.EvalSymlinks(dir)
	at, _ := os.ReadFile(filepath.Join(own, "gitdir"))
	gitdir := strings.TrimSuffix(string(at), "\n")
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(own, gitdir)
	}
	registered, _ := filepath.EvalSymlinks(filepath.Dir(gitdir))
	return own, top == real && theirs == common && registered == real
}

// makingReason is the lock reason of the worktree a link's thread's
// worktree.make is making, which git writes with the worktree.
func makingReason(thread api.ID) string { return "glitched: making " + thread.String() }

// makeWorktree makes the worktree of t, a link's thread, as b says, and
// says how it went; ok is false when the host closed as git ran, which
// leaves the effect for the next host. Git adds it locked, t named in the
// reason, then this writes t's owner file and unlocks it, so a replay
// finds a locked worktree that names t and finishes it, or the one it
// finished. One locked for anything else, or holding changes, stays for
// what is in it, and t fails.
func (h *Host) makeWorktree(ctx context.Context, t engine.Thread, b engine.MakeWorktree) (o engine.Observation, ok bool) {
	h.making.Lock()
	defer h.making.Unlock()
	fail := func(format string, args ...any) (engine.Observation, bool) {
		if ctx.Err() != nil {
			return nil, false
		}
		return engine.WorktreeFailed{Error: fmt.Sprintf(format, args...)}, true
	}
	if h.worktrees == "" {
		return fail("this engine makes none")
	}
	top, err := git(ctx, b.Project, "rev-parse", "--show-toplevel")
	if err != nil {
		return fail("%s is not in a git repository", b.Project)
	}
	common, err := git(ctx, top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fail("%v", err)
	}
	short, long, dir := worktreeNames(h.worktrees, top, t.ID, b.Title)
	reason := makingReason(t.ID)
	if _, err := os.Stat(dir); err != nil {
		branch := short
		if _, err := git(ctx, top, "rev-parse", "--verify", "--quiet", "refs/heads/"+short); err == nil {
			branch = long
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return fail("%v", err)
		}
		if _, err := git(ctx, top, "worktree", "add", "--lock", "--reason", reason, "--no-track", "-b", branch, dir, b.Base); err != nil {
			return fail("%v", err)
		}
	}
	own, at := worktreeAt(ctx, git, dir, common)
	if !at {
		return fail("%s is there, and isn't a worktree of %s; it stays for what is in it", dir, top)
	}
	lock, err := os.ReadFile(filepath.Join(own, "locked"))
	switch locked := err == nil; {
	case locked && strings.TrimSpace(string(lock)) != reason:
		return fail("the worktree at %s is locked for something else; it stays for what is in it", dir)
	case locked:
		if status, err := git(ctx, dir, "status", "--porcelain"); err != nil || status != "" {
			return fail("the worktree at %s holds changes; it stays for what is in it", dir)
		}
		if err := os.WriteFile(filepath.Join(own, ownerFile), []byte(t.ID.String()), 0o644); err != nil {
			return fail("%v", err)
		}
		if _, err := git(ctx, top, "worktree", "unlock", dir); err != nil {
			return fail("%v", err)
		}
	default:
		if _, mine := ownedBy(ctx, git, dir, common, t.ID); !mine {
			return fail("%s is there, and isn't the thread's worktree; it stays for what is in it", dir)
		}
	}
	branch, _ := git(ctx, dir, "branch", "--show-current")
	if branch != short && branch != long {
		return fail("the worktree at %s isn't on the thread's branch; it stays for what is in it", dir)
	}
	return engine.WorktreeReady{Workspace: dir, Branch: branch}, true
}

// deleteInWorktree deletes the thread c names and the threads under it,
// once each worktree a thread of them has of its own can go: none is
// locked, none was put in its place, no thread outside them runs in it.
// The delete's own commit leaves each removal as a worktree.remove
// effect, which removeWorktree does once every agent there has closed,
// or the next host does after a crash.
func (h *Host) deleteInWorktree(ctx context.Context, c api.Command) (api.Receipt, error) {
	refuse := func(format string, args ...any) (api.Receipt, error) {
		return api.Receipt{}, &api.RejectedError{Reason: "Can't remove the worktree: " + fmt.Sprintf(format, args...)}
	}
	t, err := h.store.Thread(ctx, c.Target)
	if err != nil {
		return api.Receipt{}, err
	}
	if !t.Exists() || t.Deleted {
		return h.store.Submit(ctx, c, time.Now())
	}
	snap, err := h.store.Read(ctx, api.Query{})
	if err != nil {
		return api.Receipt{}, err
	}
	tree := below(snap.Threads, t.ID)
	if i := slices.IndexFunc(snap.Threads, func(th api.Thread) bool { return th.ID == t.ID }); i >= 0 {
		tree = append(tree, snap.Threads[i])
	}
	var owners []api.Thread
	for _, th := range tree {
		if th.Branch != "" && h.ownsDir(th.ID, th.Workspace) != "" {
			owners = append(owners, th)
		}
	}
	switch {
	case len(owners) > 0:
	case t.Branch == "":
		return refuse("the thread runs in its project's folder")
	case h.worktrees == "" || worktreeDir(h.worktrees, t.Workspace) == "":
		return refuse("it isn't one this engine made")
	default:
		return refuse("it is the worktree of the thread above this one too")
	}
	for _, th := range owners {
		dir := h.ownsDir(th.ID, th.Workspace)
		common, err := git(ctx, th.Project, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return refuse("its project's repository at %s can't be read, so the worktree can't be told apart from another", th.Project)
		}
		if !ours(ctx, dir, common, th.ID, th.Branch) {
			return refuse("%s is no longer the thread's worktree on %s", dir, th.Branch)
		}
		if own, err := git(ctx, dir, "rev-parse", "--absolute-git-dir"); err == nil {
			if _, err := os.Stat(filepath.Join(own, "locked")); err == nil {
				return refuse("it is locked; unlock it with git worktree unlock %s first", dir)
			}
			if _, err := os.Stat(filepath.Join(own, "modules")); err == nil {
				return refuse("%s has had submodules, and git won't move or remove a worktree with them; delete the thread and keep its worktree, then remove it by hand", dir)
			}
		}
		for _, other := range snap.Threads {
			if !slices.ContainsFunc(tree, func(x api.Thread) bool { return x.ID == other.ID }) && under(dir, other.Workspace) {
				return refuse("the thread %q runs in %s too", other.Title, dir)
			}
		}
	}
	return h.store.Submit(ctx, c, time.Now())
}

// below is every thread of threads under top, its children's children
// too, as far down as api.MaxDepth.
func below(threads []api.Thread, top api.ID) []api.Thread {
	var out []api.Thread
	level := []api.ID{top}
	for depth := 0; depth < api.MaxDepth && len(level) > 0; depth++ {
		var next []api.ID
		for _, th := range threads {
			if slices.Contains(level, th.Parent) {
				out = append(out, th)
				next = append(next, th.ID)
			}
		}
		level = next
	}
	return out
}

// canon is p with its symlinks resolved, as far as it exists: the rest of
// it, gone, joined on, so a folder named two ways, as /var and
// /private/var on macOS, is one path whether it is still there or not.
func canon(p string) string {
	return canonIn(p, 40)
}

// canonIn is canon following at most hops links that lead nowhere.
func canonIn(p string, hops int) string {
	p = filepath.Clean(p)
	for rest := ""; ; {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(real, rest)
		}
		if to, err := os.Readlink(p); err == nil && hops > 0 {
			if !filepath.IsAbs(to) {
				to = filepath.Join(filepath.Dir(p), to)
			}
			return canonIn(filepath.Join(to, rest), hops-1)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// ownsDir is the worktree a thread in workspace has of its own, made by
// this engine and named by the whole of the thread's ID; "" where it runs
// in another's, or in no worktree of this engine's.
func (h *Host) ownsDir(thread api.ID, workspace string) string {
	if h.worktrees == "" {
		return ""
	}
	dir := worktreeDir(canon(h.worktrees), canon(workspace))
	if dir == "" || filepath.Base(dir) != strings.ToLower(thread.String()) {
		return ""
	}
	return dir
}

// ours is whether dir is still the worktree made for thread, in the
// repository whose git folder is common and on branch, or gone, in which
// case git is never asked to remove what it registered there.
func ours(ctx context.Context, dir, common string, thread api.ID, branch string) bool {
	if _, err := os.Stat(dir); err != nil {
		return true
	}
	_, ok := ownedBy(ctx, git, dir, common, thread)
	cur, _ := git(ctx, dir, "branch", "--show-current")
	return ok && cur == branch
}

// removing is where a worktree goes while it is removed: beside it, so
// git's move takes it there in one step, whatever then takes its place.
// Only removeWorktree makes a folder there.
func removing(dir string) string { return dir + ".removing" }

// removeWorktree removes the worktree w names for thread, once every agent
// that ran in it has closed and no thread runs in it, and its branch where
// nothing of its own is on it. Git moves it aside first, refusing a locked
// one, and what git then registers there is checked to be the thread's
// worktree, so one put in its place after any check stays; git removes it
// there, refusing one locked after the move. A retry after a crash finishes
// what is aside. Where the folder is already gone the branch stays: it may
// be another's now. What it leaves it logs: the delete committed, and
// nobody waits on it. It is done unless ctx ended under it, as a closing
// host's does, for the next host to do again.
func (h *Host) removeWorktree(ctx context.Context, thread api.ID, w engine.RemoveWorktree) (done bool) {
	defer func() { done = ctx.Err() == nil }()
	dir := h.ownsDir(thread, w.Workspace)
	if dir == "" {
		return
	}
	if !h.agentsClosed(ctx, dir) {
		return
	}
	h.making.Lock()
	defer h.making.Unlock()
	common, err := git(ctx, w.Project, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		slog.Warn("runtime: the deleted thread's worktree stays: its project can't be read", "thread", thread, "project", w.Project, "err", err)
		return
	}
	aside := removing(dir)
	snap, err := h.store.Read(ctx, api.Query{})
	if err != nil {
		return
	}
	if i := slices.IndexFunc(snap.Threads, func(th api.Thread) bool { return under(dir, th.Workspace) || under(aside, th.Workspace) }); i >= 0 {
		slog.Warn("runtime: the deleted thread's worktree stays: another thread runs in it", "thread", thread, "worktree", dir, "other", snap.Threads[i].ID)
		return
	}
	if _, err := os.Stat(aside); err != nil {
		if _, err := os.Stat(dir); err != nil {
			return
		}
		if !ours(ctx, dir, common, thread, w.Branch) {
			slog.Warn("runtime: what is at the deleted thread's worktree isn't its worktree, and stays", "thread", thread, "worktree", dir)
			return
		}
		if _, err := git(ctx, common, "worktree", "move", dir, aside); err != nil {
			slog.Warn("runtime: the deleted thread's worktree stays", "thread", thread, "worktree", dir, "err", err)
			return
		}
	}
	own := registered(common, aside)
	owner, _ := os.ReadFile(filepath.Join(own, ownerFile))
	head, _ := os.ReadFile(filepath.Join(own, "HEAD"))
	if own == "" || string(owner) != thread.String() || strings.TrimSpace(string(head)) != "ref: refs/heads/"+w.Branch {
		slog.Warn("runtime: what is aside from the deleted thread's worktree isn't its worktree, and stays", "thread", thread, "at", aside)
		if _, err := os.Stat(dir); own != "" && err != nil {
			git(ctx, common, "worktree", "move", aside, dir)
		}
		return
	}
	if _, err := git(ctx, common, "worktree", "remove", "--force", aside); err != nil {
		slog.Warn("runtime: the deleted thread's worktree stays", "thread", thread, "at", aside, "err", err)
		return
	}
	if _, err := git(ctx, w.Project, "branch", "-d", w.Branch); err != nil {
		slog.Info("runtime: the deleted thread's branch stays", "thread", thread, "branch", w.Branch, "err", err)
	}
	return
}

// registered is the git folder of the worktree the repository whose git
// folder is common registers at at, whether or not anything is there now,
// or "" for none.
func registered(common, at string) string {
	entries, _ := os.ReadDir(filepath.Join(common, "worktrees"))
	for _, e := range entries {
		own := filepath.Join(common, "worktrees", e.Name())
		b, err := os.ReadFile(filepath.Join(own, "gitdir"))
		if err != nil {
			continue
		}
		gitdir := strings.TrimSpace(string(b))
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(own, gitdir)
		}
		if canon(filepath.Dir(gitdir)) == canon(at) {
			return own
		}
	}
	return ""
}

// under is whether path is dir or in it, however either is spelled: a
// folder on the way up from path that is dir itself, as a case-insensitive
// volume or a link can make of another spelling, counts.
func under(dir, path string) bool {
	if path == "" {
		return false
	}
	if within(canon(dir), canon(path)) {
		return true
	}
	d, err := os.Stat(dir)
	if err != nil {
		return false
	}
	for _, start := range []string{filepath.Clean(path), canon(path)} {
		for p := start; ; p = filepath.Dir(p) {
			if fi, err := os.Stat(p); err == nil && os.SameFile(d, fi) {
				return true
			}
			if filepath.Dir(p) == p {
				break
			}
		}
	}
	return false
}

// agentsClosed waits for every agent that ran in dir to close, the agent.close
// effects of their threads done and no run's pump still closing one, and
// says whether they did before ctx ended.
func (h *Host) agentsClosed(ctx context.Context, dir string) bool {
	return retry(ctx, "wait for the worktree's agents to close", func(ctx context.Context) error {
		threads, err := h.store.Closing(ctx)
		if err != nil {
			return err
		}
		h.mu.Lock()
		for id := range h.closing {
			threads = append(threads, id)
		}
		h.mu.Unlock()
		for _, id := range threads {
			t, err := h.store.Thread(ctx, id)
			if err != nil {
				return err
			}
			if t.Exists() && under(dir, t.Workspace) {
				return fmt.Errorf("the agent of thread %s, which ran in %s, hasn't closed", id, dir)
			}
		}
		return nil
	})
}
