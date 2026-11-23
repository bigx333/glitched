package main

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
)

// A project is the threads in one workspace, and on this machine a folder
// added as one, so removing it deletes them, each with the threads under
// it, and forgets the folder. The folder on disk isn't touched: starting a
// thread in it again brings the project back.

// projectMenu is a project chip's context menu.
func (a *app) projectMenu(m *ui.Menu, p *project) {
	if m.Item("New thread").Chosen() {
		a.startThread(p)
	}
	a.pluginMenuItems(m, "project.menu", func(plugin string) map[string]any {
		project := map[string]string{"name": p.name()}
		if a.plugins.Allowed(plugin, "threads:read") {
			project["workspace"], project["machine"] = p.key.workspace, p.env.Name
		}
		return map[string]any{"project": project}
	})
	m.Separator()
	// As a thread is stopped before it's deleted, so is every thread at the
	// top of the project's trees; those under them stop as they go.
	if m.Item("Remove project…").Disabled(a.projectLive(p)).Chosen() {
		a.askRemove(p)
	}
}

// workspaceItems end the draft's workspace menu: adding a project, and
// removing one, each stopped first as the chip's menu has it.
func (a *app) workspaceItems(m *ui.Menu) {
	m.Separator()
	if m.Item("Add project…").Disabled(a.picking).Chosen() {
		a.addProject()
	}
	if len(a.projects) == 0 {
		return
	}
	m.Submenu("Remove project", func(m *ui.Menu) {
		for _, p := range a.projects {
			if m.Item(a.projectLabel(p) + "…").Disabled(a.projectLive(p)).Chosen() {
				a.askRemove(p)
			}
		}
	})
}

// projectLabel is the project's name, and its machine's when there are
// several; its folder when another of the machine's has the same name.
func (a *app) projectLabel(p *project) string {
	name := p.name()
	for _, x := range a.projects {
		if x != p && x.env == p.env && x.name() == name {
			name = p.key.workspace
			break
		}
	}
	if len(a.backend.Envs()) > 1 {
		name += " on " + p.env.Name
	}
	return name
}

// askRemove opens the alert asking before p is removed.
func (a *app) askRemove(p *project) { a.removing, a.removeOpen = p.key, true }

// roots are the project's threads that no other thread of it is under.
func (p *project) roots() []thread {
	in := map[api.ID]bool{}
	for _, t := range p.threads {
		in[t.id()] = true
	}
	var out []thread
	for _, t := range p.threads {
		if !in[t.View.Parent] {
			out = append(out, t)
		}
	}
	return out
}

// projectLive is whether a thread at the top of the project's trees has a
// run, an uncertain one too, which the user stops first. One under another
// of them, by way of another workspace, stops as its tree goes.
func (a *app) projectLive(p *project) bool {
	tops, _ := a.projectTrees(p)
	for _, t := range tops {
		if t.View.Live() {
			return true
		}
	}
	return false
}

// projectTrees are what removing p deletes: the tops, the roots under no
// other root, each with its tree, and every thread of those trees once,
// one in another workspace too.
func (a *app) projectTrees(p *project) (tops []thread, all []node) {
	roots := p.roots()
	under := map[api.ID]bool{}
	for _, t := range roots {
		for _, n := range a.below(t) {
			under[n.t.id()] = true
		}
	}
	seen := map[api.ID]bool{}
	for _, t := range roots {
		if under[t.id()] {
			continue // deleted with the root above it
		}
		tops = append(tops, t)
		for _, n := range append([]node{{t, 0}}, a.below(t)...) {
			if !seen[n.t.id()] {
				seen[n.t.id()] = true
				all = append(all, n)
			}
		}
	}
	return tops, all
}

// removeProject deletes every thread of the project, and with them the
// threads under them, and forgets the folder if it was added here. A draft
// in its workspace starts over in the home folder, so that the project
// doesn't come back with the next thread, unless the project is the home
// folder's, where every new thread starts.
func (a *app) removeProject(p *project) {
	tops, all := a.projectTrees(p)
	for _, t := range tops {
		a.deleteTree(t, false)
	}
	// Deletes that failed as they were sent, with no engine to take them,
	// leave the project, and the draft in it.
	if len(tops) > 0 && !slices.ContainsFunc(tops, deleting) {
		return
	}
	if p.key.server == "" {
		a.forgetProject(p.key.workspace)
	}
	// A draft with no machine is on the first, as current() has it.
	e := a.fresh.env
	if envs := a.backend.Envs(); e == nil && len(envs) > 0 {
		e = envs[0]
	}
	// So does one in a workspace whose every thread went with the trees,
	// as the shown thread's does when the delete leaves the draft there.
	k := keyOf(e, a.fresh.workspace)
	emptied := k != p.key && slices.ContainsFunc(all, func(n node) bool { return keyOf(n.t.env, n.t.project()) == k }) &&
		!slices.ContainsFunc(a.gather(), func(x *project) bool { return x.key == k })
	if k == p.key || emptied {
		a.newDraft(a.homeDraft(e))
	}
}

// homeDraft is where a draft on e starts over: this machine's home folder,
// or on a server the workspace of its newest project left. The app doesn't
// know a server's home, and its engine runs an agent in the workspace as
// written, so "~" there is no folder; with no project left on the server,
// the draft goes to the app's default, this machine's home.
func (a *app) homeDraft(e *client.Env) draft {
	if e == nil || e.Kind != client.Remote {
		return draft{e, homeDir()}
	}
	var newest *project
	for _, p := range a.gather() {
		if p.env == e && (newest == nil || newestFirst(p.threads[0], newest.threads[0]) < 0) {
			newest = p
		}
	}
	if newest == nil {
		return draft{}
	}
	return draft{e, newest.key.workspace}
}

// removeAlert asks before a project is removed, always: it deletes every
// thread in it at once. It closes if the project goes meanwhile.
func (a *app) removeAlert(c *ui.Context) {
	if !a.removeOpen {
		return
	}
	var p *project
	for _, x := range a.projects {
		if x.key == a.removing {
			p = x
		}
	}
	if p == nil {
		a.removeOpen, a.removing = false, projectKey{}
		return
	}
	_, all := a.projectTrees(p)
	name := a.projectLabel(p)
	head := "Remove “" + name + "” and its thread?"
	body := "It's removed from every list and can't be brought back. Its history stays in the engine's log."
	if len(all) == 0 {
		head, body = "Remove “"+name+"”?", "It has no threads, so it only leaves the lists."
	}
	if len(all) > 1 {
		head = fmt.Sprintf("Remove “%s” and its %d threads?", name, len(all))
		body = "They're removed from every list and can't be brought back. Their history stays in the engine's log."
		// A thread under one of the project's goes with it, though its
		// workspace, and so its project, is another.
		if n := len(all) - len(p.threads); n > 0 {
			head = fmt.Sprintf("Remove “%s” and %d threads?", name, len(all))
			body = fmt.Sprintf("%d of them %s under its threads, in other workspaces. ", n, isAre(n)) + body
		}
		if n := working(all); n > 0 {
			body = fmt.Sprintf("%d of them %s still working and will be stopped. ", n, isAre(n)) + body
		}
	}
	body += " The folder, " + p.key.workspace + ", isn't touched."
	// A thread at the top that started a run since the alert opened is
	// stopped first, as the menus have it: the alert says so, not that it
	// will be stopped.
	live := a.projectLive(p)
	if live {
		body = "An agent is working in " + name + ": stop it first, and nothing is removed until then. The folder, " + p.key.workspace + ", isn't touched."
	}
	switch a.alert(c, &a.removeOpen, head, body, "Cancel", "Remove") {
	case 1:
		if live {
			c.AddToast(ui.Toast{Title: "Not removed", Description: "Stop the agent working in " + name + " first.", Type: "info"})
		} else {
			a.removeProject(p)
		}
		a.removing = projectKey{}
	case 0, ui.AlertDismissed:
		a.removing = projectKey{}
	}
}
