package main

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/jsonfile"
	"glitched/internal/projects"
)

// The New project wizard makes a project on one of the machines Glitched
// reaches, as a folder only means something on one machine: it asks which
// machine, then for a folder there or a repository to clone there, and
// shows what it will make before it makes anything. A folder on this
// machine comes from the system's picker, one on a server from the
// folders its engine lists. A clone runs on the machine's own engine, this
// machine's too.

// wizardStep is the step the wizard shows.
type wizardStep int

const (
	stepMachine wizardStep = iota
	stepSource
	stepBrowse
	stepClone
	stepConfirm
)

// wizardSource is how the project's folder comes: one that's there, or
// one a clone makes.
type wizardSource int

const (
	sourceNone wizardSource = iota
	sourceFolder
	sourceClone
)

// wizard is the New project wizard as it shows. Back goes to the step
// before and keeps every choice; only choosing another machine drops the
// folders chosen on the last one.
type wizard struct {
	open bool
	step wizardStep
	// back are the steps shown before this one, the last the one Back
	// goes to.
	back []wizardStep
	// skipSource goes past Source once, on to Clone, for a wizard opened
	// with Clone chosen.
	skipSource bool
	// machine is the chosen machine's server label, "" for this machine.
	machine string
	source  wizardSource
	// folder is the existing folder chosen, absolute on the machine.
	folder string
	browse folderBrowser
	// repoList is the Clone step's repository list; its rows follow
	// their names as the machine's listing fills in.
	repoList ui.ListState
	// input is the clone's URL or owner/name, as typed.
	input string
	// parent is the folder the clone goes into; "" until Clone shows.
	parent string
	// name is the folder the clone makes; named is the name the source
	// last gave it, which a name not edited since follows.
	name, named string
	// run is the clone running, nil while none is; line the last line it
	// printed.
	run  *wizardRun
	line string
	// failure is why the last Create or clone failed, or why the engine
	// refused a folder picked on this machine; "" while none has.
	failure string
	// focus is the control each step focuses, which the arrows move.
	focus string
}

// wizardRun is one clone the wizard started. Its lines and its end show
// only while it's the wizard's run. One stopping was stopped by Escape,
// and goes back to Clone once it has ended.
type wizardRun struct {
	cancel   func()
	stopping bool
}

// folderBrowser is the folders of one folder on a server, as its engine
// listed them, and the path field over them.
type folderBrowser struct {
	field string
	// shown is the last listing that came, the folder Use this folder
	// chooses once none is pending.
	shown api.FolderList
	// pending is the listing asked for, nil while none is. While one is,
	// no folder can be chosen, and its rows don't show.
	pending *folderQuery
	failure string
	hidden  bool
	list    ui.ListState
}

type folderQuery struct{ _ byte }

// repoCache is the repositories one machine's gh reaches, as last
// listed, and the listing under way.
type repoCache struct {
	// repos is the last complete listing.
	repos     []api.Repo
	truncated bool
	// listed is when that listing completed, zero while none has.
	listed time.Time
	// fetch is the listing under way, nil while none is.
	fetch *repoFetch
	// failure is why the last listing failed, "" while none has.
	failure string
}

// repoFetch is one listing under way.
type repoFetch struct {
	// repos is the pages that have come.
	repos  []api.Repo
	cancel func()
}

// shown is the repositories to show: those the fetch under way has given
// so far, then the cached ones it hasn't replaced, or the cache alone
// while nothing fetches.
func (e *repoCache) shown() []api.Repo {
	if e.fetch == nil {
		return e.repos
	}
	seen := make(map[string]bool, len(e.fetch.repos))
	for _, r := range e.fetch.repos {
		seen[r.Name] = true
	}
	out := slices.Clone(e.fetch.repos)
	for _, r := range e.repos {
		if !seen[r.Name] {
			out = append(out, r)
		}
	}
	return out
}

// reposOf is the repository cache of e's machine; a machine machineKey
// can't name shares one loose cache, which a new wizard throws away.
func (a *app) reposOf(e *client.Env) *repoCache {
	key, ok := machineKey(e)
	if !ok {
		if a.repoLoose == nil {
			a.repoLoose = &repoCache{}
		}
		return a.repoLoose
	}
	if a.repoLists == nil {
		a.repoLists = map[string]*repoCache{}
	}
	c := a.repoLists[key]
	if c == nil {
		c = &repoCache{}
		a.repoLists[key] = c
	}
	return c
}

// reposFresh is how long a machine's last listing counts as fresh, which
// showing the Clone step again doesn't fetch another for.
const reposFresh = 30 * time.Second

// refreshRepos starts a listing of the repositories e's machine's gh
// reaches, unless one is under way — a reopened wizard doesn't start
// another — or the last is fresh and force doesn't say otherwise. Pages
// fill the cache as they come, and the wizard closing doesn't stop the
// listing; what answers a listing another replaced is dropped.
func (a *app) refreshRepos(e *client.Env, force bool) {
	if e == nil {
		return
	}
	c := a.reposOf(e)
	if c.fetch != nil || !force && !c.listed.IsZero() && a.now.Sub(c.listed) < reposFresh {
		return
	}
	f := &repoFetch{}
	c.fetch, c.failure = f, ""
	f.cancel = a.backend.Repos(e, func(p api.RepoPage) {
		if c.fetch != f {
			return
		}
		for _, r := range p.Repos {
			if !slices.ContainsFunc(f.repos, func(have api.Repo) bool { return have.Name == r.Name }) {
				f.repos = append(f.repos, r)
			}
		}
		if p.Last {
			c.truncated = p.Truncated
		}
	}, func(err error) {
		if c.fetch != f {
			return
		}
		if err == nil {
			c.repos, c.listed, c.fetch = f.repos, a.now, nil
			return
		}
		// Whatever pages came stay, merged with the cache, but the
		// listing still counts as none: the next showing asks again.
		c.repos, c.fetch = c.shown(), nil
		if errors.Is(err, client.ErrTooOld) {
			c.failure = "Update Glitched on " + reposMachine(e) + " to list your repositories"
		} else {
			c.failure = a.wizardError(err)
		}
	})
}

// reposMachine names e's machine as the wizard's messages do, which a
// listing that outlives the wizard still names right.
func reposMachine(e *client.Env) string {
	if name := keyOf(e, "").server; name != "" {
		return name
	}
	return "this machine"
}

// retryRepos lists the chosen machine's repositories again, whatever
// the cache holds.
func (a *app) retryRepos() {
	a.refreshRepos(a.wizardEnv(), true)
}

// filterRepos is the repositories whose name or description holds every
// word of query, whatever its case: the one named it first, then names
// starting with it, then those with every word in their name, then the
// rest, each rank in the order given.
func filterRepos(repos []api.Repo, query string) []api.Repo {
	q := strings.TrimSpace(query)
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return repos
	}
	lq := strings.ToLower(q)
	type ranked struct {
		repo api.Repo
		rank int
	}
	var out []ranked
	for _, repo := range repos {
		name := strings.ToLower(repo.Name)
		desc := strings.ToLower(repo.Description)
		// A repository matches when each word is in its name or its
		// description; the rank is where they matched.
		matched, named := true, true
		for _, w := range words {
			if !strings.Contains(name, w) {
				named = false
				if !strings.Contains(desc, w) {
					matched = false
					break
				}
			}
		}
		if !matched {
			continue
		}
		part := name
		if i := strings.LastIndex(name, "/"); i >= 0 {
			part = name[i+1:]
		}
		rank := 3
		switch {
		case strings.EqualFold(repo.Name, q) || strings.EqualFold(part, q):
			rank = 0
		case strings.HasPrefix(part, lq) || strings.HasPrefix(name, lq):
			rank = 1
		case named:
			rank = 2
		}
		out = append(out, ranked{repo, rank})
	}
	slices.SortStableFunc(out, func(a, b ranked) int { return a.rank - b.rank })
	repos = make([]api.Repo, len(out))
	for i, r := range out {
		repos[i] = r.repo
	}
	return repos
}

// visible are the folders shown, those whose names start with a dot only
// when hidden ones are, and none while a listing is pending.
func (b *folderBrowser) visible() []api.Folder {
	if b.pending != nil {
		return nil
	}
	if b.hidden {
		return b.shown.Folders
	}
	return slices.DeleteFunc(slices.Clone(b.shown.Folders), func(f api.Folder) bool { return strings.HasPrefix(f.Name, ".") })
}

// chooseCloneParent opens the system's folder picker over the window for
// the folder a clone goes into; "" when none was picked.
var chooseCloneParent = func(a *app) string {
	if a.win == nil {
		return ""
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: a.win, Title: "Clone into", ButtonLabel: "Choose", Directory: true, CreateDirectories: true,
	})
	if err != nil || len(paths) == 0 {
		return ""
	}
	return paths[0]
}

// cloneFile is clone.json in the state directory: the parent folder last
// cloned into on this machine, and on each server by its engine's ID. The
// servers by label that files written before engine IDs hold are read as
// none.
type cloneFile struct {
	Parent  string            `json:"parent"`
	Engines map[string]string `json:"engines,omitempty"`
}

func (a *app) cloneFilePath() string { return filepath.Join(a.dir, "clone.json") }

// cloneParent is where a clone on e's machine goes: the folder the last
// one there went into, else ~/Projects there, which a server's engine
// expands. A server gone from Settings, nil, has none of its own.
func (a *app) cloneParent(e *client.Env) string {
	var f cloneFile
	if err := jsonfile.Load(a.cloneFilePath(), &f); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("glitched: clone: %v", err)
	}
	if e == nil || e.Kind == client.Remote {
		if key, ok := machineKey(e); ok && f.Engines[key] != "" {
			return f.Engines[key]
		}
		return "~/Projects"
	}
	if f.Parent != "" {
		return f.Parent
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Projects")
}

// rememberParent keeps parent as where the next clone on e's machine goes.
func (a *app) rememberParent(e *client.Env, parent string) {
	key, ok := machineKey(e)
	if !ok {
		return
	}
	_, err := jsonfile.EditOrReset(a.cloneFilePath(), func(f *cloneFile) error {
		if key == "" {
			f.Parent = parent
			return nil
		}
		if f.Engines == nil {
			f.Engines = map[string]string{}
		}
		f.Engines[key] = parent
		return nil
	})
	if err != nil {
		log.Printf("glitched: clone: %v", err)
	}
}

// openWizard shows the wizard at its first step: Machine, or Source with
// one machine to choose from. With Clone chosen it goes past Source.
func (a *app) openWizard(src wizardSource) {
	envs := a.backend.Envs()
	if a.wizard.open || len(envs) == 0 {
		return
	}
	a.repoLoose = nil
	a.wizard = wizard{open: true, step: stepMachine, source: src, skipSource: src == sourceClone}
	if len(envs) > 1 {
		a.enterStep()
		return
	}
	a.wizard.step = stepSource
	a.wizardPast()
}

// closeWizard closes it and stops its clone, whose folder goes with it.
func (a *app) closeWizard() {
	if r := a.wizard.run; r != nil {
		r.cancel()
	}
	a.wizard = wizard{}
}

// wizardTo goes on to step, which Back comes back from.
func (a *app) wizardTo(step wizardStep) {
	w := &a.wizard
	w.back = append(w.back, w.step)
	w.step = step
	a.enterStep()
}

// wizardBack goes to the step before, and closes the wizard from the
// first.
func (a *app) wizardBack() {
	w := &a.wizard
	if len(w.back) == 0 {
		a.closeWizard()
		return
	}
	w.step, w.back = w.back[len(w.back)-1], w.back[:len(w.back)-1]
	w.skipSource = false
	a.enterStep()
}

// wizardPast goes past Source to Clone for a wizard opened with Clone
// chosen, once.
func (a *app) wizardPast() {
	w := &a.wizard
	if w.step == stepSource && w.skipSource {
		w.skipSource = false
		a.wizardTo(stepClone)
		return
	}
	a.enterStep()
}

// enterStep focuses the step's list or field, so that the keyboard drives
// it, and readies what it shows.
func (a *app) enterStep() {
	w := &a.wizard
	w.failure = ""
	switch w.step {
	case stepMachine:
		w.focus = "machine:" + w.machine
		if e := a.wizardEnv(); e == nil || !a.reachable(e) {
			w.focus = ""
			for _, e := range a.backend.Envs() {
				if a.reachable(e) {
					w.focus = "machine:" + keyOf(e, "").server
					break
				}
			}
		}
	case stepSource:
		w.focus = "source:folder"
		if w.source == sourceClone {
			w.focus = "source:clone"
		}
	case stepBrowse:
		w.focus = "browse:path"
		if shown := w.browse.visible(); len(shown) > 0 {
			w.focus = "row:" + shown[0].Name
		}
	case stepClone:
		if w.parent == "" {
			w.parent = a.cloneParent(a.wizardEnv())
		}
		w.focus = "clone:input"
		a.refreshRepos(a.wizardEnv(), false)
	case stepConfirm:
		w.focus = "confirm:create"
		if w.source == sourceClone {
			if src, ok := projects.ParseSource(w.input); ok && w.name == w.named {
				w.name, w.named = src.Name, src.Name
			}
			w.focus = "confirm:name"
		}
	}
}

// wizardEnv is the machine chosen, nil once it's gone from Settings.
func (a *app) wizardEnv() *client.Env {
	if a.wizard.machine == "" {
		return a.localEnv()
	}
	return a.serverEnv(a.wizard.machine)
}

// reachable is whether e's engine answers, as the one that shows it.
func (a *app) reachable(e *client.Env) bool {
	_, ok := e.Status.(client.Online)
	return ok && a.backend.ShownBy(e) == e
}

// unreachable is why e can't be chosen, "" while it can.
func (a *app) unreachable(e *client.Env) string {
	if o := a.backend.ShownBy(e); o != e {
		return "Same engine as " + o.Name
	}
	switch s := e.Status.(type) {
	case client.Online:
		return ""
	case client.Offline:
		if d := s.RetryAt.Sub(a.now).Round(time.Second); d > 0 {
			return "Offline, retrying in " + d.String()
		}
		return "Offline, retrying now"
	case client.Refused:
		return "Refused the token"
	case client.Incompatible:
		return "Incompatible: update Glitched there or here"
	}
	return "Connecting…"
}

// machineName names the chosen machine in a sentence.
func (a *app) machineName() string {
	if a.wizard.machine == "" {
		return "this machine"
	}
	return a.wizard.machine
}

// machineTitle names the chosen machine at the start of a line.
func (a *app) machineTitle() string {
	if a.wizard.machine == "" {
		return "This machine"
	}
	return a.wizard.machine
}

// wizardError is what the wizard says of an engine's err.
func (a *app) wizardError(err error) string {
	if errors.Is(err, client.ErrTooOld) {
		return "Update Glitched on " + a.machineName() + " to add a project there."
	}
	return err.Error()
}

// joinOn joins a folder and a name as the chosen machine's paths are: a
// server's are a unix machine's, whatever this one's are.
func (a *app) joinOn(dir, name string) string {
	if a.wizard.machine == "" {
		return filepath.Join(dir, name)
	}
	return path.Join(dir, name)
}

func (a *app) baseOn(dir string) string {
	if a.wizard.machine == "" {
		return filepath.Base(dir)
	}
	return path.Base(dir)
}

// chooseMachine goes on from Machine on the machine label names. Another
// machine than the last one chosen has none of its folders.
func (a *app) chooseMachine(label string) {
	w := &a.wizard
	if label != w.machine {
		w.machine, w.folder, w.parent = label, "", ""
		w.browse = folderBrowser{hidden: w.browse.hidden}
		w.repoList = ui.ListState{}
	}
	w.back = append(w.back, w.step)
	w.step = stepSource
	a.wizardPast()
}

// chooseSource goes on from Source: to the system's picker for a folder on
// this machine, to the folders a server lists for one there, or to Clone.
func (a *app) chooseSource(src wizardSource) {
	w := &a.wizard
	w.source = src
	switch {
	case src == sourceClone:
		a.wizardTo(stepClone)
	case w.machine != "":
		if w.browse.shown.Path == "" && w.browse.pending == nil {
			a.listFolders("", false)
		}
		a.wizardTo(stepBrowse)
	default:
		a.pickFolder()
	}
}

// pickFolder asks for a folder on this machine, off the main thread, which
// the picker would block. One picked that the engine lists goes on to
// Confirm; none leaves the wizard on Source. One picker opens at a time.
func (a *app) pickFolder() {
	if a.picking {
		return
	}
	a.picking = true
	go func() {
		dir := chooseFolder(a)
		a.post(func() {
			a.checkPicked(dir, stepSource, func(dir string) {
				a.wizard.folder = dir
				a.wizardTo(stepConfirm)
			})
		})
	}()
}

// checkPicked has this machine's engine list dir, the folder the system's
// picker gave on step, and gives use the folder once it does, while the
// wizard is still on step there. One the engine refuses, as its state
// folder, stays unused, and the wizard says why.
func (a *app) checkPicked(dir string, step wizardStep, use func(string)) {
	e := a.localEnv()
	if dir == "" || e == nil {
		a.picking = false
		return
	}
	a.backend.Folders(e, filepath.Clean(dir), func(list api.FolderList, err error) {
		a.picking = false
		if w := &a.wizard; !w.open || w.step != step || w.machine != "" {
			return
		}
		if err != nil {
			a.wizard.failure = a.wizardError(err)
			return
		}
		a.wizard.failure = ""
		use(list.Path)
	})
}

// chooseParent asks for the folder the clone goes into: the system's
// picker on this machine, the folders a server lists on one.
func (a *app) chooseParent() {
	w := &a.wizard
	if w.machine != "" {
		a.listFolders(w.parent, true)
		a.wizardTo(stepBrowse)
		return
	}
	if a.picking {
		return
	}
	a.picking = true
	go func() {
		dir := chooseCloneParent(a)
		a.post(func() {
			a.checkPicked(dir, stepClone, func(dir string) {
				a.wizard.parent, a.wizard.focus = dir, "clone:input"
			})
		})
	}()
}

// listFolders asks the server for the folders in dir. One it can't list
// says why, and the browser stays where it was; or, with orHome and
// nothing listed yet, as a clone's parent that isn't there yet, it lists
// the home folder instead.
func (a *app) listFolders(dir string, orHome bool) {
	e := a.wizardEnv()
	if e == nil {
		return
	}
	q := &folderQuery{}
	b := &a.wizard.browse
	b.pending, b.failure = q, ""
	if dir != "" {
		b.field = dir
	}
	a.backend.Folders(e, dir, func(list api.FolderList, err error) {
		w := &a.wizard
		if b.pending != q {
			return
		}
		b.pending = nil
		if err != nil {
			if orHome && b.shown.Path == "" {
				a.listFolders("", false)
				return
			}
			b.failure, b.field = a.wizardError(err), b.shown.Path
			return
		}
		b.shown, b.field, b.failure = list, list.Path, ""
		b.list.ScrollTo(0, ui.Start)
		if w.step == stepBrowse && w.focus != "browse:path" {
			a.enterStep()
		}
	})
}

// browseInto lists the folder name in the one shown.
func (a *app) browseInto(name string) {
	a.listFolders(a.joinOn(a.wizard.browse.shown.Path, name), false)
}

// useFolder chooses the folder shown: as the clone's parent, back on
// Clone, or as the project's folder, on to Confirm. While another is
// being listed, it chooses none.
func (a *app) useFolder() {
	w := &a.wizard
	dir := w.browse.shown.Path
	if dir == "" || w.browse.pending != nil || w.step != stepBrowse {
		return
	}
	if w.source == sourceClone {
		w.parent = dir
		a.wizardBack()
		return
	}
	w.folder = dir
	a.wizardTo(stepConfirm)
}

// chooseRepo chooses a listed repository: the field takes its
// owner/name, which Confirm names the clone's folder after.
func (a *app) chooseRepo(name string) {
	w := &a.wizard
	w.input = name
	a.wizardTo(stepConfirm)
}

// cloneNext goes on from Clone, once the field names a repository.
func (a *app) cloneNext() {
	if _, ok := projects.ParseSource(a.wizard.input); ok && a.wizard.step == stepClone {
		a.wizardTo(stepConfirm)
	}
}

// cloneName is the folder name the clone makes, and whether it's one.
func (w *wizard) cloneName() (string, bool) {
	name := strings.TrimSpace(w.name)
	return name, name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`)
}

// moveFocus moves the focus by d among the step's rows.
func (a *app) moveFocus(c *ui.Context, d int) {
	w := &a.wizard
	var keys []string
	switch w.step {
	case stepMachine:
		for _, e := range a.backend.Envs() {
			if a.reachable(e) {
				keys = append(keys, "machine:"+keyOf(e, "").server)
			}
		}
	case stepSource:
		keys = []string{"source:folder", "source:clone"}
	case stepBrowse:
		keys = []string{"browse:path"}
		for _, f := range w.browse.visible() {
			keys = append(keys, "row:"+f.Name)
		}
	case stepClone:
		keys = []string{"clone:input"}
		for _, r := range filterRepos(a.reposOf(a.wizardEnv()).shown(), w.input) {
			keys = append(keys, "repo:"+r.Name)
		}
	}
	if len(keys) == 0 {
		return
	}
	// From outside them, as the hidden folders' box, the arrows go to
	// the first row.
	i := slices.Index(keys, ui.FocusedValue(c, &w.focus))
	switch {
	case i < 0 && (w.step == stepBrowse || w.step == stepClone):
		i = 1
	case i < 0:
		i = 0
	default:
		i += d
	}
	i = max(0, min(i, len(keys)-1))
	w.focus = keys[i]
	if i > 0 {
		switch w.step {
		case stepBrowse:
			w.browse.list.ScrollIntoView(i - 1)
		case stepClone:
			w.repoList.ScrollIntoView(i - 1)
		}
	}
}

// createProject does what Confirm shows, once the machine still answers:
// keeps the folder as a project, or clones into it first.
func (a *app) createProject() {
	w := &a.wizard
	e := a.wizardEnv()
	if w.run != nil {
		return
	}
	if e == nil || !a.reachable(e) {
		w.failure = a.machineTitle() + " went offline"
		return
	}
	if w.source == sourceFolder {
		a.recordProject(e, w.folder)
		a.finishWizard(e, w.folder)
		return
	}
	name, ok := w.cloneName()
	if !ok {
		return
	}
	parent := w.parent
	run := &wizardRun{}
	w.run, w.line, w.failure = run, "", ""
	run.cancel = a.backend.Clone(e, api.CloneRequest{Source: w.input, Dest: a.joinOn(parent, name)}, func(line string) {
		if a.wizard.run == run {
			a.wizard.line = line
		}
	}, func(cloned api.Cloned, err error) {
		// A clone that finished is a project, though the wizard was
		// cancelled as it ended: its folder is there either way.
		if err == nil {
			a.recordProject(e, cloned.Path)
			a.rememberParent(e, parent)
		}
		if a.wizard.run != run {
			return
		}
		a.wizard.run = nil
		switch {
		case err == nil:
			a.finishWizard(e, cloned.Path)
		case run.stopping:
			a.wizardBack()
		default:
			a.wizard.failure = a.wizardError(err)
		}
	})
}

// finishWizard closes the wizard and shows a new thread in the project.
// One in an existing folder keeps the draft's worktree choice, as Add
// project always has; a clone's starts fresh.
func (a *app) finishWizard(e *client.Env, dir string) {
	keep := a.wizard.source == sourceFolder && a.selected.IsZero()
	a.wizard = wizard{}
	w := a.worktree
	a.startIn(draft{e, dir})
	if keep {
		a.worktree.picked, a.worktree.on = w.picked, w.on
	}
	a.draftArea.Focus()
}

// wizardDialog draws the wizard as the app's alerts are (MyGo's
// AlertDialog), with their measures but wider, for a path: a title, the
// step, and Back, Cancel and the step's button. Escape goes back a step,
// once a clone running has stopped; a click outside does nothing, as
// outside an alert, which would lose the choices.
func (a *app) wizardDialog(c *ui.Context) {
	w := &a.wizard
	if !w.open {
		return
	}
	running := w.run != nil
	a.dialog(c, &w.open, func(backdrop, panel ui.Element) {
		if backdrop.Clicked() {
			w.open = true
		}
		t := c.Theme()
		width, _ := c.Size()
		room := width - t.Space(10)
		backdrop.Background(ui.RGBA(0, 0, 0, 0.4))
		panel.Width(min(t.Space(115), room)).MaxWidth(room).Padding(t.Space(5)).Gap(t.Space(3)).Radius(t.Space(2.5)).
			Background(t.Background).Label("New project")
		panel.Shadow(0, 10, 30, 0, ui.RGBA(0, 0, 0, 0.3))
		wrapped := func(s string) ui.Element { return ui.Text(c, s).Width(0).MinWidthPercent(100) }
		title := func(s string) { wrapped(s).Bold().FontSize(t.FontSize + 1) }
		primary := ""
		act := func() {}
		ready := true
		switch w.step {
		case stepMachine:
			title("New project")
			wrapped("Which machine is it on?").TextColor(t.TextMuted)
			a.machineRows(c)
		case stepSource:
			title("New project on " + a.machineName())
			a.sourceRows(c)
			if w.failure != "" {
				wrapped(w.failure).TextColor(palette.red)
			}
		case stepBrowse:
			if w.source == sourceClone {
				title("Clone into a folder on " + a.machineName())
				primary = "Clone here"
			} else {
				title("Choose a folder on " + a.machineName())
				primary = "Use this folder"
			}
			act, ready = a.useFolder, w.browse.shown.Path != "" && w.browse.pending == nil
			a.browseStep(c)
		case stepClone:
			title("Clone a repository onto " + a.machineName())
			_, ok := projects.ParseSource(w.input)
			primary, act, ready = "Next", a.cloneNext, ok
			a.cloneStep(c, ok)
			if w.failure != "" {
				wrapped(w.failure).TextColor(palette.red)
			}
		case stepConfirm:
			title("Create the project")
			primary, act = "Create", a.createProject
			if w.source == sourceClone {
				primary = "Clone and create"
				if w.failure != "" {
					primary = "Retry"
				}
				_, ok := w.cloneName()
				ready = ok && !running
			}
			a.confirmStep(c, wrapped)
		}
		ui.Row(c).Wrap().Gap(t.Space(2)).Justify(ui.End).Margin(t.Space(2), 0, 0, 0).Children(func() {
			if len(w.back) > 0 {
				ui.Button(c.Key("wizard back"), "Back").Disabled(running).MaxWidthPercent(100).OnClick(a.wizardBack)
				ui.Spacer(c)
			}
			ui.Button(c.Key("wizard cancel"), "Cancel").MaxWidthPercent(100).OnClick(a.closeWizard)
			if primary != "" {
				b := ui.PrimaryButton(c.Key("wizard go"), primary).Disabled(!ready).MaxWidthPercent(100).OnClick(act)
				if w.step == stepConfirm && w.source == sourceFolder {
					b.FocusBind(&w.focus, "confirm:create")
				}
			}
		})
		if w.step == stepBrowse {
			c.OnShortcut(ui.Cmd, ui.KeyEnter, a.useFolder)
		}
	})
	if w.open {
		return
	}
	w.open = true
	if r := w.run; r != nil {
		r.stopping = true
		r.cancel()
		return
	}
	a.wizardBack()
}

// machineRows are this machine and each server, each with a dot of how
// it's reached and, while it can't be chosen, why.
func (a *app) machineRows(c *ui.Context) {
	w := &a.wizard
	t := c.Theme()
	ui.Column(c).Gap(t.Space(0.5)).OnShortcut(0, ui.KeyDown, func() { a.moveFocus(c, 1) }).
		OnShortcut(0, ui.KeyUp, func() { a.moveFocus(c, -1) }).Children(func() {
		for _, e := range a.backend.Envs() {
			label := keyOf(e, "").server
			name, detail := e.Name, ""
			if e.Kind != client.Remote {
				name, detail = "This machine", e.Name
			}
			if why := a.unreachable(e); why != "" {
				detail = why
			}
			key := "machine:" + label
			col, _ := a.envStatus(e)
			r := row(c, key, ui.FocusedValue(c, &w.focus) == key).Disabled(!a.reachable(e)).FocusBind(&w.focus, key).Label(name)
			r.Children(func() {
				ui.Box(c).Size(5, 5).Radius(3).Background(col).Shrink(0)
				ui.Column(c).Grow(1).Shrink(1).Children(func() {
					ui.Text(c, name).TextColor(palette.text).SingleLine()
					if detail != "" {
						ui.Text(c, detail).FontSize(t.FontSize - 1).TextColor(palette.muted).SingleLine()
					}
				})
			})
			r.OnClick(func() { a.chooseMachine(label) })
		}
	})
}

// sourceRows are the two ways a project's folder comes, on the machine.
func (a *app) sourceRows(c *ui.Context) {
	w := &a.wizard
	t := c.Theme()
	ui.Column(c).Gap(t.Space(0.5)).OnShortcut(0, ui.KeyDown, func() { a.moveFocus(c, 1) }).
		OnShortcut(0, ui.KeyUp, func() { a.moveFocus(c, -1) }).Children(func() {
		for _, s := range []struct {
			src                wizardSource
			key, label, detail string
			icon               *ui.SVG
		}{
			{sourceFolder, "source:folder", "Existing folder", "A folder on " + a.machineName(), icFolderOpen},
			{sourceClone, "source:clone", "Clone a repository", "From your GitHub repositories, or a URL", icBranch},
		} {
			r := row(c, s.key, ui.FocusedValue(c, &w.focus) == s.key).Disabled(a.picking).FocusBind(&w.focus, s.key).Label(s.label)
			r.Children(func() {
				ui.Icon(c, s.icon).FontSize(t.FontSize + 2).Shrink(0)
				ui.Column(c).Grow(1).Shrink(1).Children(func() {
					ui.Text(c, s.label).TextColor(palette.text).SingleLine()
					ui.Text(c, s.detail).FontSize(t.FontSize - 1).TextColor(palette.muted).SingleLine()
				})
			})
			r.OnClick(func() { a.chooseSource(s.src) })
		}
	})
}

// browseRows is how many folders the browser shows at once.
const browseRows = 8

// browseStep is a server's folders: the path field, the folders of the
// folder it names, and why one couldn't be listed. The list is as tall as
// browseRows of the row helper's rows: its padding, 6 above and below,
// round the taller of the icon and a line of the interface font.
func (a *app) browseStep(c *ui.Context) {
	w := &a.wizard
	b := &w.browse
	t := c.Theme()
	shown := b.visible()
	// The arrows move the focus from the path field too, which keeps
	// them, as the palette's search does.
	switch {
	case c.Shortcut(0, ui.KeyDown):
		a.moveFocus(c, 1)
	case c.Shortcut(0, ui.KeyUp):
		a.moveFocus(c, -1)
	}
	ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
		a.iconButton(c, "browse up", icArrowLeft, "Enclosing folder", false).Disabled(b.shown.Parent == "").
			OnClick(func() { a.listFolders(b.shown.Parent, false) })
		ui.TextInput(c.Key("browse path"), &b.field).Placeholder("Path").Label("Path").
			FocusBind(&w.focus, "browse:path").Grow(1).Shrink(1).Width(0).
			OnSubmit(func() { a.listFolders(strings.TrimSpace(b.field), false) })
	})
	ui.Checkbox(c.Key("browse hidden"), &b.hidden, "Show hidden folders")
	icon := t.FontSize + 1
	rowH := 12 + max(icon, lineHeight(c, t.FontSize, 0))
	l := &b.list
	l.Key = func(i int) any { return shown[i].Name }
	ui.List(c.Key("browse folders"), l, len(shown), func(i int) {
		f := shown[i]
		key := "row:" + f.Name
		r := row(c, key, ui.FocusedValue(c, &w.focus) == key).FocusBind(&w.focus, key).Label(f.Name)
		r.Children(func() {
			glyph := icFolder
			if f.Repo {
				glyph = icBranch
			}
			ui.Icon(c, glyph).FontSize(icon).Shrink(0)
			ui.Text(c, f.Name).TextColor(palette.text).SingleLine().Grow(1).Shrink(1)
		})
		r.OnClick(func() { a.browseInto(f.Name) })
	}).Height(browseRows*rowH).Radius(t.Space(1.5)).Border(1, palette.border).
		OnShortcut(0, ui.KeyLeft, func() {
			if b.shown.Parent != "" {
				a.listFolders(b.shown.Parent, false)
			}
		}).
		OnShortcut(0, ui.KeyRight, func() {
			if name, ok := strings.CutPrefix(ui.FocusedValue(c, &w.focus), "row:"); ok {
				a.browseInto(name)
			}
		}).
		Children(func() {
			switch {
			case b.pending != nil:
				ui.Text(c, "Listing…").TextColor(palette.muted).Padding(t.Space(3)).TextAlign(ui.Center)
			case len(shown) == 0 && b.shown.Path != "":
				ui.Text(c, "No folders here").TextColor(palette.muted).Padding(t.Space(3)).TextAlign(ui.Center)
			}
		})
	if b.shown.Truncated {
		ui.Text(c, "Only the first "+strconv.Itoa(len(b.shown.Folders))+" folders are listed.").TextColor(t.TextMuted).Width(0).MinWidthPercent(100)
	}
	if b.failure != "" {
		ui.Text(c, b.failure).TextColor(palette.red).Width(0).MinWidthPercent(100)
	}
}

// cloneStep is the repository's field, under it the repositories the
// machine's gh reaches filtered by what the field holds, and the folder
// the clone goes into. A row, or Enter on the first of them, chooses it;
// a URL or owner/name typed goes on as it always has. The list is as
// tall as browseRows of the row helper's rows, so the dialog doesn't
// jump.
func (a *app) cloneStep(c *ui.Context, ok bool) {
	w := &a.wizard
	t := c.Theme()
	cache := a.reposOf(a.wizardEnv())
	fetching := cache.fetch != nil
	shown := cache.shown()
	filtered := filterRepos(shown, w.input)
	// The arrows move the focus from the field too, which keeps them, as
	// the browser's path field does.
	switch {
	case c.Shortcut(0, ui.KeyDown):
		a.moveFocus(c, 1)
	case c.Shortcut(0, ui.KeyUp):
		a.moveFocus(c, -1)
	}
	ui.TextInput(c.Key("clone source"), &w.input).Placeholder("Search your repositories, or paste a URL").Label("Repository").
		FocusBind(&w.focus, "clone:input").Width(0).MinWidthPercent(100).OnSubmit(func() {
		if len(filtered) > 0 {
			a.chooseRepo(filtered[0].Name)
			return
		}
		a.cloneNext()
	})
	if strings.TrimSpace(w.input) != "" && !ok && len(filtered) == 0 {
		ui.Text(c, "Enter a clone URL or owner/name").TextColor(palette.red).Width(0).MinWidthPercent(100)
	}
	icon := t.FontSize + 1
	rowH := 12 + max(icon, lineHeight(c, t.FontSize, 0))
	l := &w.repoList
	l.Key = func(i int) any { return filtered[i].Name }
	ui.List(c.Key("clone repos"), l, len(filtered), func(i int) {
		repo := filtered[i]
		key := "repo:" + repo.Name
		// While the focus is in the field, the first row shows what
		// Enter picks.
		focus := ui.FocusedValue(c, &w.focus)
		r := row(c, key, focus == key || i == 0 && focus == "clone:input").FocusBind(&w.focus, key).Label(repo.Name)
		r.Children(func() {
			ui.Icon(c, icBranch).FontSize(icon).Shrink(0)
			ui.Text(c, repo.Name).TextColor(palette.text).SingleLine()
			if repo.Description != "" {
				ui.Text(c, repo.Description).FontSize(t.FontSize - 1).TextColor(palette.muted).SingleLine().Grow(1).Shrink(1)
			} else {
				ui.Spacer(c)
			}
			if repo.Private {
				ui.Text(c, "Private").FontSize(t.FontSize - 1).TextColor(palette.muted).Shrink(0)
			}
		})
		r.OnClick(func() { a.chooseRepo(repo.Name) })
	}).Height(browseRows*rowH).Radius(t.Space(1.5)).Border(1, palette.border).
		Children(func() {
			switch {
			case len(filtered) > 0:
			case fetching:
				ui.Text(c, "Loading your repositories…").TextColor(palette.muted).Padding(t.Space(3)).TextAlign(ui.Center)
			case cache.failure != "" && len(shown) == 0:
				ui.Text(c, cache.failure).TextColor(palette.muted).Padding(t.Space(3), t.Space(3), t.Space(1), t.Space(3)).TextAlign(ui.Center)
				ui.Row(c).Justify(ui.Center).Padding(0, 0, t.Space(3), 0).Children(func() {
					ui.Button(c.Key("repos retry"), "Retry").OnClick(a.retryRepos)
				})
			case !cache.listed.IsZero() || len(shown) > 0:
				ui.Text(c, "No repositories match").TextColor(palette.muted).Padding(t.Space(3)).TextAlign(ui.Center)
			}
		})
	switch {
	case len(filtered) > 0 && fetching:
		ui.Text(c, "Checking for new repositories…").TextColor(t.TextMuted).Width(0).MinWidthPercent(100)
	case len(shown) > 0 && cache.failure != "":
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, cache.failure).TextColor(t.TextMuted).Width(0).Grow(1).Shrink(1)
			ui.Button(c.Key("repos retry"), "Retry").FontSize(t.FontSize - 1).OnClick(a.retryRepos)
		})
	case cache.truncated:
		ui.Text(c, "Only your 1000 most recently pushed repositories are listed.").TextColor(t.TextMuted).Width(0).MinWidthPercent(100)
	}
	ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Into "+w.parent).TextColor(t.TextMuted).Width(0).Grow(1).Shrink(1)
		ui.Button(c.Key("clone parent"), "Choose…").Disabled(a.picking).OnClick(a.chooseParent)
	})
}

// confirmStep is what Create makes: the machine, the name and the path.
func (a *app) confirmStep(c *ui.Context, wrapped func(string) ui.Element) {
	w := &a.wizard
	t := c.Theme()
	running := w.run != nil
	line := func(label string, value func()) {
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).TextColor(t.TextMuted).Width(t.Space(18)).Shrink(0)
			ui.Box(c).Grow(1).Shrink(1).Width(0).Children(value)
		})
	}
	machine := a.machineTitle()
	if e := a.wizardEnv(); e != nil && w.machine == "" {
		machine += " (" + e.Name + ")"
	}
	line("Machine", func() { ui.Text(c, machine) })
	dir := w.folder
	if w.source == sourceClone {
		line("Name", func() {
			ui.TextInput(c.Key("clone name"), &w.name).Label("Name").FocusBind(&w.focus, "confirm:name").
				Disabled(running).Width(0).MinWidthPercent(100).OnSubmit(a.createProject)
		})
		name, _ := w.cloneName()
		dir = a.joinOn(w.parent, name)
	} else {
		line("Name", func() { ui.Text(c, a.baseOn(dir)) })
	}
	line("Path", func() { ui.Text(c, dir).TextColor(t.TextMuted).Width(0).MinWidthPercent(100) })
	if e := a.wizardEnv(); w.source == sourceFolder && e != nil &&
		slices.ContainsFunc(a.projects, func(p *project) bool { return p.env == e && p.key == keyOf(e, dir) }) {
		wrapped(a.baseOn(dir) + " is already a project on " + a.machineName() + ". Create shows it.").TextColor(t.TextMuted)
	}
	if running {
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Label("Cloning")
			if w.run.stopping {
				ui.Text(c, "Stopping the clone…")
			} else {
				ui.Text(c, "Cloning…")
			}
		})
		if w.line != "" {
			wrapped(w.line).TextColor(t.TextMuted).SingleLine()
		}
	}
	if w.failure != "" {
		wrapped(w.failure).TextColor(palette.red)
	}
}
