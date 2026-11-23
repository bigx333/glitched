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

// worktreeNames are the branch and the folder of thread's worktree. The
// folder is named by the whole of the thread's ID, so no other thread's
// is ever taken for it; the branch by the title and the ID's end.
func worktreeNames(root, top string, thread api.ID, title string) (branch, dir string) {
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
	return "glitched/" + slug + "-" + id[len(id)-6:], filepath.Join(root, filepath.Base(top)+"-"+hex.EncodeToString(sum[:4]), id)
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
	dry := b
	dry.Worktree, dry.Project, dry.Branch = nil, b.Workspace, "glitched/dry"
	if _, _, err := engine.Decide(engine.Thread{}, api.Command{ID: c.ID, Target: c.Target, Revision: c.Revision, Expires: c.Expires, Body: dry}, time.Now()); err != nil {
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
	branch, dir := worktreeNames(h.worktrees, top, c.Target, b.Title)
	made := false
	if _, err := os.Stat(dir); err == nil {
		if cur, err := git(ctx, dir, "branch", "--show-current"); err != nil || cur != branch {
			return refuse("%s is already there and isn't on %s", dir, branch)
		}
	} else {
		if _, err := git(ctx, top, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
			return refuse("the repository at %s has no commits yet", top)
		}
		base := b.Worktree.Base
		if base == "" {
			base = "HEAD"
		}
		if _, err := git(ctx, top, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
			return refuse("the repository has no branch or commit %q", base)
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return refuse("%v", err)
		}
		if _, err := git(ctx, top, "worktree", "add", "--no-track", "-b", branch, dir, base); err != nil {
			return refuse("%v", err)
		}
		made = true
	}
	workspace := filepath.Join(dir, rel)
	var r api.Receipt
	realDir, _ := filepath.EvalSymlinks(dir)
	if real, rerr := filepath.EvalSymlinks(workspace); rerr != nil || !within(realDir, real) {
		r, err = refuse("%s is not a folder of the worktree on that base", rel)
	} else {
		in := b
		in.Project, in.Branch, in.Workspace, in.Worktree = b.Workspace, branch, workspace, nil
		r, err = h.store.SubmitResolved(ctx, c, in, time.Now())
	}
	if made && err != nil && !errors.Is(err, ctx.Err()) {
		ctx := context.WithoutCancel(ctx)
		if _, rmErr := git(ctx, top, "worktree", "remove", dir); rmErr == nil {
			git(ctx, top, "branch", "-d", branch)
		} else {
			slog.Warn("runtime: a refused thread's worktree stays for what is in it", "worktree", dir, "err", rmErr)
		}
	}
	return r, err
}

// deleteInWorktree deletes the thread c names and, once the delete
// commits, removes its worktree as end says. It removes only the worktree
// Glitched made for the thread, still on the thread's branch, and with
// RemoveWorktree only one git shows no change in, untracked files and
// submodules' changes included; ignored files go with it. A branch with
// commits that aren't merged stays.
func (h *Host) deleteInWorktree(ctx context.Context, c api.Command, end api.WorktreeEnd) (api.Receipt, error) {
	refuse := func(format string, args ...any) (api.Receipt, error) {
		return api.Receipt{}, &api.RejectedError{Reason: "Can't remove the worktree: " + fmt.Sprintf(format, args...)}
	}
	if end > api.RemoveWorktreeChanges {
		return refuse("unknown worktree end %d", end)
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
		return refuse("its project's repository at %s can't be read: %v", t.Project, err)
	}
	if _, err := os.Stat(dir); err == nil {
		top, err := git(ctx, dir, "rev-parse", "--show-toplevel")
		if err != nil {
			return refuse("%v", err)
		}
		theirs, _ := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		cur, _ := git(ctx, dir, "branch", "--show-current")
		if real, _ := filepath.EvalSymlinks(dir); top != real || theirs != common || cur != t.Branch {
			return refuse("%s is no longer the thread's worktree on %s", dir, t.Branch)
		}
		if end == api.RemoveWorktree {
			status, err := git(ctx, dir, "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=none")
			if err != nil {
				return refuse("%v", err)
			}
			if status != "" {
				n := strings.Count(status, "\n") + 1
				return refuse("it has %d uncommitted %s", n, map[bool]string{true: "change", false: "changes"}[n == 1])
			}
		}
	}
	r, err := h.store.Submit(ctx, c, time.Now())
	if err != nil {
		return r, err
	}
	ctx = context.WithoutCancel(ctx)
	if _, err := git(ctx, common, "worktree", "remove", "--force", dir); err != nil {
		os.RemoveAll(dir)
		if _, err := git(ctx, common, "worktree", "prune"); err != nil {
			slog.Warn("runtime: the deleted thread's worktree stays registered", "thread", c.Target, "worktree", dir, "err", err)
		}
	}
	if _, err := git(ctx, common, "branch", "-d", t.Branch); err != nil {
		slog.Info("runtime: the deleted thread's branch stays", "thread", c.Target, "branch", t.Branch, "err", err)
	}
	return r, nil
}
