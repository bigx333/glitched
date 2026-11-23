package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// WithWorktrees has the host make the worktrees threads ask for under
// root, one folder per repository. Without it, it makes none.
func WithWorktrees(root string) Option { return func(h *Host) { h.worktrees = root } }

// git runs git in dir and returns its output trimmed, or its complaint.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
	if !b.Parent.IsZero() || b.Project != "" || b.Branch != "" {
		return refuse("a child runs in its parent's folder")
	}
	target, err := h.store.Thread(ctx, c.Target)
	if err != nil {
		return api.Receipt{}, err
	}
	dry := b
	dry.Worktree, dry.Project, dry.Branch = nil, b.Workspace, "glitched/dry"
	if _, _, err := engine.Decide(target, api.Command{ID: c.ID, Target: c.Target, Revision: c.Revision, Expires: c.Expires, Body: dry}, time.Now()); err != nil {
		return api.Receipt{}, err
	}
	top, err := git(ctx, b.Workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return refuse("%s is not in a git repository", b.Workspace)
	}
	rel := "."
	if ws, err := filepath.EvalSymlinks(b.Workspace); err == nil && within(top, ws) {
		rel, _ = filepath.Rel(top, ws)
	}
	short, long, dir := worktreeNames(h.worktrees, top, c.Target, b.Title)
	branch := ""
	if _, err := os.Stat(dir); err == nil {
		common, _ := git(ctx, top, "rev-parse", "--path-format=absolute", "--git-common-dir")
		own, ok := ownedBy(ctx, dir, common, c.Target)
		if _, err := os.Stat(filepath.Join(own, "locked")); own != "" && err == nil {
			return refuse("the worktree at %s was never finished; it stays for what is in it", dir)
		}
		if !ok {
			return refuse("%s is there, and isn't the thread's worktree of %s", dir, top)
		}
		if branch, _ = git(ctx, dir, "branch", "--show-current"); branch != short && branch != long {
			return refuse("%s is already there and isn't on the thread's branch", dir)
		}
	} else {
		if _, err := git(ctx, top, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
			return refuse("the repository at %s has no commits yet", top)
		}
		base := b.Worktree.Base
		if base == "" {
			base = "HEAD"
		}
		commit, err := git(ctx, top, "rev-parse", "--verify", "--quiet", base+"^{commit}")
		if err != nil {
			return refuse("the repository has no branch or commit %q", base)
		}
		if rel != "." {
			if kind, err := git(ctx, top, "cat-file", "-t", commit+":"+filepath.ToSlash(rel)); err != nil || kind != "tree" {
				return refuse("%s is not a folder on %s", rel, base)
			}
		}
		branch = short
		if _, err := git(ctx, top, "rev-parse", "--verify", "--quiet", "refs/heads/"+short); err == nil {
			branch = long
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return refuse("%v", err)
		}
		// Locked until made, so that a retry can tell a worktree git
		// finished from one it stopped in.
		if _, err := git(ctx, top, "worktree", "add", "--lock", "--reason", "glitched: making", "--no-track", "-b", branch, dir, commit); err != nil {
			return refuse("%v", err)
		}
		own, err := git(ctx, dir, "rev-parse", "--absolute-git-dir")
		if err == nil {
			err = os.WriteFile(filepath.Join(own, ownerFile), []byte(c.Target.String()), 0o644)
		}
		if err == nil {
			_, err = git(ctx, top, "worktree", "unlock", dir)
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
	in.Project, in.Branch, in.Workspace, in.Worktree = b.Workspace, branch, workspace, nil
	r, err := h.store.SubmitResolved(ctx, c, in, time.Now())
	if err != nil {
		slog.Warn("runtime: a refused thread's worktree stays for what is in it", "worktree", dir, "err", err)
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
func ownedBy(ctx context.Context, dir, common string, thread api.ID) (string, bool) {
	top, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	own, _ := git(ctx, dir, "rev-parse", "--absolute-git-dir")
	theirs, _ := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	real, _ := filepath.EvalSymlinks(dir)
	owner, _ := os.ReadFile(filepath.Join(own, ownerFile))
	at, _ := os.ReadFile(filepath.Join(own, "gitdir"))
	registered, _ := filepath.EvalSymlinks(filepath.Dir(strings.TrimSpace(string(at))))
	return own, top == real && theirs == common && registered == real && string(owner) == thread.String()
}

// deleteInWorktree deletes the thread c names and, once the delete
// commits, removes its worktree with what is in it: only the worktree
// Glitched made for the thread, in its project's repository and on its
// branch, then as now, and never one git won't remove, as a locked one.
// A branch with commits that aren't merged stays.
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
	if t.Branch == "" {
		return refuse("the thread runs in its project's folder")
	}
	if !t.Parent.IsZero() {
		if p, err := h.store.Thread(ctx, t.Parent); err == nil && p.Exists() && !p.Deleted && p.Branch == t.Branch {
			return refuse("it is the worktree of the thread above this one too")
		}
	}
	dir := worktreeDir(h.worktrees, t.Workspace)
	if h.worktrees == "" || dir == "" {
		return refuse("it isn't one this engine made")
	}
	common, err := git(ctx, t.Project, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return refuse("its project's repository at %s can't be read, so the worktree can't be told apart from another", t.Project)
	}
	// ours is whether dir is still the worktree made for the thread, in
	// its project's repository and on its branch, or gone, in which case
	// git is never asked to remove what it registered there.
	ours := func(ctx context.Context) bool {
		if _, err := os.Stat(dir); err != nil {
			return true
		}
		_, ok := ownedBy(ctx, dir, common, t.ID)
		cur, _ := git(ctx, dir, "branch", "--show-current")
		return ok && cur == t.Branch
	}
	if !ours(ctx) {
		return refuse("%s is no longer the thread's worktree on %s", dir, t.Branch)
	}
	if own, err := git(ctx, dir, "rev-parse", "--absolute-git-dir"); err == nil {
		if _, err := os.Stat(filepath.Join(own, "locked")); err == nil {
			return refuse("it is locked; unlock it with git worktree unlock %s first", dir)
		}
	}
	r, err := h.store.Submit(ctx, c, time.Now())
	if err != nil {
		return r, err
	}
	ctx = context.WithoutCancel(ctx)
	if _, err := os.Stat(dir); err != nil {
		slog.Info("runtime: the deleted thread's worktree was already gone", "thread", c.Target, "worktree", dir)
	} else if !ours(ctx) {
		slog.Warn("runtime: the deleted thread's worktree stays: another took its place", "thread", c.Target, "worktree", dir)
		return r, nil
	} else if _, err := git(ctx, common, "worktree", "remove", "--force", dir); err != nil {
		slog.Warn("runtime: the deleted thread's worktree stays", "thread", c.Target, "worktree", dir, "err", err)
		return r, nil
	}
	if _, err := git(ctx, common, "branch", "-d", t.Branch); err != nil {
		slog.Info("runtime: the deleted thread's branch stays", "thread", c.Target, "branch", t.Branch, "err", err)
	}
	return r, nil
}
