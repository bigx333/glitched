package main

import (
	"cmp"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// worktreeChoice is where the next new thread runs: in its folder or in a
// new worktree of it, as Settings › General says until the user picks in
// the workspace menu, and the branch that worktree starts from.
type worktreeChoice struct {
	picked bool
	on     bool
	base   string // "": the folder's current branch
	baseIn string // the folder base was picked in
}

// baseOf is the branch a new worktree of dir starts from: the one picked
// in dir while it is still there, else its current one.
func (a *app) baseOf(dir string) string {
	if a.worktree.baseIn == dir && slices.Contains(a.repoAt(dir).branches, a.worktree.base) {
		return a.worktree.base
	}
	return a.repoAt(dir).current
}

// repo is what git says of a folder a new thread could start in: whether
// it's in a repository with a commit, its current branch and its others.
type repo struct {
	ok       bool
	current  string
	branches []string
}

var repoOf = func(dir string) repo {
	git := func(args ...string) (string, bool) {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
		return strings.TrimSuffix(string(out), "\n"), err == nil
	}
	if _, ok := git("rev-parse", "--show-toplevel"); !ok {
		return repo{}
	}
	if _, ok := git("rev-parse", "--verify", "--quiet", "HEAD"); !ok {
		return repo{}
	}
	current, _ := git("branch", "--show-current")
	list, _ := git("for-each-ref", "--format=%(refname:lstrip=2)", "refs/heads")
	return repo{ok: true, current: current, branches: strings.FieldsFunc(list, func(r rune) bool { return r == '\n' })}
}

// newDraft makes d the draft, with the worktree choice Settings › General
// makes and every folder's branches read anew.
func (a *app) newDraft(d draft) {
	a.fresh, a.worktree, a.repos = d, worktreeChoice{}, nil
}

// repoAt is repoOf dir, asked once for each folder in each draft.
func (a *app) repoAt(dir string) repo {
	r, ok := a.repos[dir]
	if !ok {
		r = repoOf(dir)
		if a.repos == nil {
			a.repos = map[string]repo{}
		}
		a.repos[dir] = r
	}
	return r
}

// newWorktree is the worktree the draft t asks for, nil to run in its
// folder: one only of a repository on this machine.
func (a *app) newWorktree(t thread) *api.NewWorktree {
	on := a.worktree.on
	if !a.worktree.picked {
		on = a.settings.newThreadMode == 1
	}
	if !on || !t.local() || t.View.Workspace == a.scratchRoot() || !a.repoAt(t.View.Workspace).ok {
		return nil
	}
	b := a.baseOf(t.View.Workspace)
	if b == "" {
		return &api.NewWorktree{}
	}
	return &api.NewWorktree{Base: "refs/heads/" + b}
}

// worktreeLabel is what the workspace button says of where t runs: its
// branch, or the worktree a draft asks for; "" in its folder.
func (a *app) worktreeLabel(t thread) string {
	if !t.id().IsZero() {
		return t.View.Branch
	}
	w := a.newWorktree(t)
	if w == nil {
		return ""
	}
	return "New worktree from " + cmp.Or(strings.TrimPrefix(w.Base, "refs/heads/"), "HEAD")
}

// ownsWorktree is whether t is the thread whose worktree it runs in,
// not a child that shares its parent's.
func (a *app) ownsWorktree(t thread) bool {
	if t.View.Branch == "" {
		return false
	}
	p, ok := a.find(t.View.Parent)
	return !ok || p.View.Branch != t.View.Branch
}

// changesIn counts the changes git doesn't ignore in the worktree t runs
// in, from its root: the checkout of its project's repository on its
// branch, -1 for a thread on another machine or a worktree
// git can't read.
var changesIn = func(t thread) int {
	if !t.local() {
		return -1
	}
	dirs := func(dir string) (top, common string, ok bool) {
		git := func(arg string) (string, error) {
			out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", arg).Output()
			return strings.TrimSuffix(string(out), "\n"), err
		}
		top, err := git("--show-toplevel")
		common, err2 := git("--git-common-dir")
		return top, common, err == nil && err2 == nil
	}
	_, want, ok := dirs(t.View.Project)
	if !ok {
		return -1
	}
	dir := t.View.Workspace
	for _, err := os.Stat(dir); err != nil && filepath.Dir(dir) != dir; _, err = os.Stat(dir) {
		dir = filepath.Dir(dir)
	}
	for {
		top, common, ok := dirs(dir)
		if !ok || filepath.Dir(top) == top {
			return -1
		}
		if common == want && gitBranch(top) == t.View.Branch {
			dir = top
			break
		}
		dir = filepath.Dir(top)
	}
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=none").Output()
	if err != nil {
		return -1
	}
	return len(strings.FieldsFunc(string(out), func(r rune) bool { return r == '\n' }))
}

func gitBranch(dir string) string {
	out, _ := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	return strings.TrimSuffix(string(out), "\n")
}

// worktreeItems are the workspace menu's choice of where the draft t
// runs, after its folders, for a repository on this machine only.
func (a *app) worktreeItems(m *ui.Menu, t thread) {
	if !t.local() || t.View.Workspace == a.scratchRoot() {
		return
	}
	r := a.repoAt(t.View.Workspace)
	if !r.ok {
		return
	}
	on := a.newWorktree(t) != nil
	m.Separator()
	if m.Item("Local folder").Checked(!on).Chosen() {
		a.worktree.picked, a.worktree.on = true, false
	}
	if m.Item("New worktree").Checked(on).Chosen() {
		a.worktree.picked, a.worktree.on = true, true
	}
	if !on {
		return
	}
	base := a.baseOf(t.View.Workspace)
	m.Submenu("Base branch", func(m *ui.Menu) {
		for _, b := range r.branches {
			if m.Item(b).Checked(b == base).Chosen() {
				a.worktree.base, a.worktree.baseIn = b, t.View.Workspace
			}
		}
	})
}
