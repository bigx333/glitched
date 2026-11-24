package runtime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// A call another machine's link makes reaches only the threads that link
// started here (docs/daemon/placement-and-handoff.md): it reads them,
// follows them, asks what their runs said, and sends them the commands
// linkIntents allows. It starts them with the closed link create alone,
// in a worktree this engine makes of a checkout of the repository it
// names. Every refusal of scope is api.ErrNotAllowed.

// owned is nil when thread is one link started here, and it isn't
// deleted; api.ErrNotFound when it is deleted; api.ErrNotAllowed for any
// other, there or not, so a link learns nothing of what isn't its own.
func (h *Host) owned(ctx context.Context, link api.Link, thread api.ID) error {
	if thread.IsZero() {
		return fmt.Errorf("%w: a link reads only a thread it started, by its id", api.ErrNotAllowed)
	}
	t, err := h.store.Thread(ctx, thread)
	if err != nil {
		return err
	}
	if !t.Exists() || t.Origin == nil || t.Origin.Link != link.ID {
		return fmt.Errorf("%w: the thread isn't one this link started", api.ErrNotAllowed)
	}
	if t.Deleted {
		return api.ErrNotFound
	}
	return nil
}

// linkCommand is the id c's command takes from link: its own and the
// link's, so a link never gets another's receipt, nor a link minted again
// under a label the receipt of the one it replaced.
func linkCommand(link, id api.ID) api.ID {
	sum := sha256.Sum256([]byte(link.String() + "/link/" + id.String()))
	return api.ID(sum[:16])
}

// submitFromLink commits c, a link's command, under the id linkCommand
// gives it, and answers with the receipt under c's own.
func (h *Host) submitFromLink(ctx context.Context, link api.Link, c api.Command) (api.Receipt, error) {
	own := c.ID
	c.ID = linkCommand(link.ID, c.ID)
	r, err := h.linkSubmit(ctx, link, c)
	if err == nil {
		r.Command = own
	}
	return r, err
}

func (h *Host) linkSubmit(ctx context.Context, link api.Link, c api.Command) (api.Receipt, error) {
	if c.Caller != nil {
		return api.Receipt{}, fmt.Errorf("%w: a link's command names no run of this engine's", api.ErrNotAllowed)
	}
	if b, ok := c.Body.(api.ThreadCreate); ok {
		return h.createFromLink(ctx, link, c, b)
	}
	if err := h.owned(ctx, link, c.Target); err != nil {
		return api.Receipt{}, err
	}
	t, err := h.store.Thread(ctx, c.Target)
	if err != nil {
		return api.Receipt{}, err
	}
	switch b := c.Body.(type) {
	case api.MessageDispatch:
		if b.Auto {
			return api.Receipt{}, fmt.Errorf("%w: only this engine continues a thread on its own", api.ErrNotAllowed)
		}
	case api.TurnInterrupt, api.InboxSet:
	case api.ThreadConfigure:
		if b.Model != nil || b.Effort != nil || b.Fast != nil || b.Access == nil || *b.Access > t.Access {
			return api.Receipt{}, fmt.Errorf("%w: a link only narrows a thread's access", api.ErrNotAllowed)
		}
	default:
		return api.Receipt{}, fmt.Errorf("%w: a link may not send %s", api.ErrNotAllowed, api.IntentType(c.Body))
	}
	return h.store.Submit(ctx, c, h.clock())
}

// createFromLink commits a link's create, b, of a new thread: only its
// origin, repository, title, model, access and task, its origin's link
// stamped from link, in a worktree of the one checkout here of the
// repository, from its branch. The worktree is made after the commit,
// by its effect. A create already committed gets its receipt.
func (h *Host) createFromLink(ctx context.Context, link api.Link, c api.Command, b api.ThreadCreate) (api.Receipt, error) {
	refuse := func(reason string) (api.Receipt, error) {
		return api.Receipt{}, &api.RejectedError{Reason: reason}
	}
	switch {
	case b.Origin == nil || b.Repository == nil:
		return refuse("a link's create names its origin and its repository")
	case b.Workspace != "" || !b.Parent.IsZero() || !b.Run.IsZero() || b.Worktree != nil || b.Project != "" || b.Branch != "" || b.Fork != nil || b.Remote != "":
		return refuse("a link's create gives only its origin, repository, title, model, access and task: this engine picks the folder, the worktree and its branch")
	case b.Origin.Engine.IsZero() || b.Origin.Thread.IsZero():
		return refuse("a link's create names the engine and the thread it came from")
	}
	origin := *b.Origin
	origin.Link = link.ID
	b.Origin = &origin
	c.Body = b
	if done, err := h.store.Committed(ctx, c.ID); err != nil || done {
		if err != nil {
			return api.Receipt{}, err
		}
		return h.store.Submit(ctx, c, h.clock())
	}
	if t, err := h.store.Thread(ctx, c.Target); err != nil {
		return api.Receipt{}, err
	} else if t.Exists() {
		return api.Receipt{}, fmt.Errorf("%w: a link creates only a thread that isn't there", api.ErrNotAllowed)
	}
	if h.worktrees == "" {
		return refuse("this engine makes no worktrees, so it takes no thread from another machine")
	}
	top, base, err := h.checkoutOf(ctx, *b.Repository)
	if err != nil {
		return refuse(err.Error())
	}
	in := b
	in.Project, in.Worktree = top, &api.NewWorktree{Base: base}
	return h.store.SubmitResolved(ctx, c, in, h.clock())
}

var rootCommit = regexp.MustCompile("^[0-9a-f]{40}([0-9a-f]{24})?$")

// checkoutOf is the one checkout here of the repository whose root commit
// is r.Root, among the folders the person's threads run in, and the
// commit r.Branch, a branch of it, is at. Checkouts that share a git
// folder, as worktrees of one clone do, are one.
func (h *Host) checkoutOf(ctx context.Context, r api.Repository) (top, commit string, err error) {
	if !rootCommit.MatchString(r.Root) {
		return "", "", fmt.Errorf("%q isn't a repository's root commit", r.Root)
	}
	if r.Branch == "" || strings.HasPrefix(r.Branch, "-") {
		return "", "", fmt.Errorf("%q isn't a branch", r.Branch)
	}
	if _, err := git(ctx, ".", "check-ref-format", "refs/heads/"+r.Branch); err != nil {
		return "", "", fmt.Errorf("%q isn't a branch", r.Branch)
	}
	dirs, err := h.store.Checkouts(ctx)
	if err != nil {
		return "", "", err
	}
	type checkout struct{ top, common string }
	var seen, matched []checkout
	for _, dir := range dirs {
		if under(h.worktrees, dir) {
			continue
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		t, err := git(ctx, dir, "rev-parse", "--show-toplevel")
		if err != nil {
			continue
		}
		common, err := git(ctx, t, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil || slices.ContainsFunc(seen, func(c checkout) bool { return c.common == common }) {
			continue
		}
		seen = append(seen, checkout{t, common})
		roots, _ := git(ctx, t, "rev-list", "--max-parents=0", "HEAD")
		if slices.Contains(strings.Fields(roots), r.Root) {
			matched = append(matched, checkout{t, common})
		}
	}
	list := func(cs []checkout) string {
		var names []string
		for _, c := range cs {
			names = append(names, filepath.Clean(c.top))
		}
		if len(names) == 0 {
			return "none"
		}
		return strings.Join(names, ", ")
	}
	switch len(matched) {
	case 0:
		return "", "", fmt.Errorf("no checkout here is of the repository whose root commit is %s; the checkouts here are: %s", r.Root, list(seen))
	case 1:
	default:
		return "", "", fmt.Errorf("several checkouts here are of the repository whose root commit is %s, and this engine can't tell which: %s", r.Root, list(matched))
	}
	top = matched[0].top
	commit, err = git(ctx, top, "rev-parse", "--verify", "--quiet", "refs/heads/"+r.Branch+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("the checkout at %s has no branch %s", top, r.Branch)
	}
	return top, commit, nil
}

// ChildReply is what q.Run, a run of q.Thread's, said last, as a local
// child's report gives it its parent: for a link, of a thread it started.
func (h *Host) ChildReply(ctx context.Context, q api.ChildReplyQuery) (api.ChildReply, error) {
	if link, ok := api.LinkOf(ctx); ok {
		if err := h.owned(ctx, link, q.Thread); err != nil {
			return api.ChildReply{}, err
		}
	}
	t, err := h.store.Thread(ctx, q.Thread)
	if err != nil {
		return api.ChildReply{}, err
	}
	if !t.Exists() || t.Deleted || !slices.ContainsFunc(t.Runs, func(r engine.Run) bool { return r.ID == q.Run }) {
		return api.ChildReply{}, api.ErrNotFound
	}
	text, err := h.store.RunReply(ctx, q.Thread, q.Run)
	return api.ChildReply{Text: text}, err
}
