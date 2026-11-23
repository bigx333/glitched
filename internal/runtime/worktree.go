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

// worktreeNames are the branch and the folder of thread's worktree,
// from its title and its ID alone, so that a create sent again finds
// the worktree the first one made.
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
	id := thread.String()
	leaf := slug + "-" + strings.ToLower(id[len(id)-6:])
	sum := sha256.Sum256([]byte(top))
	return "glitched/" + leaf, filepath.Join(root, filepath.Base(top)+"-"+hex.EncodeToString(sum[:4]), leaf)
}

// makeWorktree makes the worktree c asks for and returns the create of a
// thread in it, and undo, which takes back a worktree this call made, or
// why it can't.
func (h *Host) makeWorktree(ctx context.Context, thread api.ID, c api.ThreadCreate) (api.ThreadCreate, func(), error) {
	refuse := func(format string, args ...any) (api.ThreadCreate, func(), error) {
		return c, nil, &api.RejectedError{Reason: "Can't make a worktree: " + fmt.Sprintf(format, args...)}
	}
	if h.worktrees == "" {
		return refuse("this engine makes none")
	}
	if !c.Parent.IsZero() || c.Project != "" {
		return refuse("a child runs in its parent's folder")
	}
	top, err := git(ctx, c.Workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return refuse("%s is not in a git repository", c.Workspace)
	}
	if _, err := git(ctx, top, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return refuse("the repository at %s has no commits yet", top)
	}
	base := baseOf(c.Worktree)
	if _, err := git(ctx, top, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		return refuse("the repository has no branch or commit %q", base)
	}
	rel := "."
	if ws, err := filepath.EvalSymlinks(c.Workspace); err == nil {
		if r, err := filepath.Rel(top, ws); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}
	branch, dir := worktreeNames(h.worktrees, top, thread, c.Title)
	undo := func() {}
	if _, err := os.Stat(dir); err == nil {
		if cur, err := git(ctx, dir, "branch", "--show-current"); err != nil || cur != branch {
			return refuse("%s is already there and isn't on %s", dir, branch)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return refuse("%v", err)
		}
		if _, err := git(ctx, top, "worktree", "add", "--no-track", "-b", branch, dir, base); err != nil {
			return refuse("%v", err)
		}
		undo = func() {
			_, err := git(context.WithoutCancel(ctx), top, "worktree", "remove", "--force", dir)
			if err == nil {
				_, err = git(context.WithoutCancel(ctx), top, "branch", "-D", branch)
			}
			if err != nil {
				slog.Warn("runtime: a refused thread's worktree stays", "worktree", dir, "err", err)
			}
		}
	}
	c.Project, c.Branch, c.Workspace, c.Worktree = c.Workspace, branch, filepath.Join(dir, rel), nil
	return c, undo, nil
}

// baseOf is the branch or commit w starts from.
func baseOf(w *api.NewWorktree) string {
	if w.Base == "" {
		return "HEAD"
	}
	return w.Base
}

// deleteInWorktree deletes the thread c names and, after the delete
// commits, removes its worktree as end says: never one with changes
// unless end throws them away, and never its branch while that has
// commits that aren't merged.
func (h *Host) deleteInWorktree(ctx context.Context, c api.Command, end api.WorktreeEnd) (api.Receipt, error) {
	refuse := func(format string, args ...any) (api.Receipt, error) {
		return api.Receipt{}, &api.RejectedError{Reason: "Can't remove the worktree: " + fmt.Sprintf(format, args...)}
	}
	t, err := h.store.Thread(ctx, c.Target)
	if err != nil {
		return api.Receipt{}, err
	}
	if t.Branch == "" {
		return refuse("the thread runs in its project's folder")
	}
	if !t.Parent.IsZero() {
		if p, err := h.store.Thread(ctx, t.Parent); err == nil && p.Exists() && !p.Deleted && p.Branch == t.Branch {
			return refuse("it is the worktree of the thread above this one too")
		}
	}
	top, err := git(ctx, t.Workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return h.store.Submit(ctx, c, time.Now())
	}
	common, err := git(ctx, top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return refuse("%v", err)
	}
	if end == api.RemoveWorktree {
		status, err := git(ctx, top, "status", "--porcelain")
		if err != nil {
			return refuse("%v", err)
		}
		if n := len(strings.Split(status, "\n")); status != "" {
			return refuse("it has %d uncommitted %s", n, plural(n, "change", "changes"))
		}
	}
	r, err := h.store.Submit(ctx, c, time.Now())
	if err != nil {
		return r, err
	}
	if _, err := git(ctx, common, "worktree", "remove", "--force", top); err != nil {
		slog.Warn("runtime: the deleted thread's worktree stays", "thread", c.Target, "worktree", top, "err", err)
		return r, nil
	}
	if _, err := git(ctx, common, "branch", "-d", t.Branch); err != nil {
		slog.Info("runtime: the deleted thread's branch stays", "thread", c.Target, "branch", t.Branch, "err", err)
	}
	return r, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
