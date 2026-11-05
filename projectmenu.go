package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// A project is the threads in one workspace, so removing it deletes them,
// each with the threads under it, and the project goes from the sidebar
// with its last thread. The folder on disk isn't touched: starting a thread
// in it again brings the project back.

// projectMenu is a project chip's context menu.
func (a *app) projectMenu(c *ui.Context, m *ui.Menu, p *project) {
	if m.Item("New thread").Disabled(a.duplicate(p.env)).Chosen() {
		a.startThread(p)
	}
	m.Separator()
	// As a thread is stopped before it's deleted, so is every thread at the
	// top of the project's trees; those under them stop as they go.
	if m.Item("Remove project…").Disabled(projectLive(p)).Chosen() {
		a.askRemove(p)
	}
}

// workspaceItems end the draft's workspace menu: adding a project, and
// removing one, each stopped first as the chip's menu has it.
func (a *app) workspaceItems(m *ui.Menu, several bool) {
	m.Separator()
	if m.Item("Add project…").Chosen() {
		a.addProject()
	}
	if len(a.projects) == 0 {
		return
	}
	m.Submenu("Remove project", func(m *ui.Menu) {
		for _, p := range a.projects {
			name := p.name()
			if several {
				name += " on " + p.env.Name
			}
			if m.Item(name + "…").Disabled(projectLive(p)).Chosen() {
				a.askRemove(p)
			}
		}
	})
}

// askRemove opens the alert asking before p is removed.
func (a *app) askRemove(p *project) { a.removing, a.removeOpen = p.key, true }

// roots are the project's threads that no other thread of it is under:
// deleting each with its tree deletes every thread of the project.
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
// run, an uncertain one too, which the user stops first.
func projectLive(p *project) bool {
	for _, t := range p.roots() {
		if t.View.Live() {
			return true
		}
	}
	return false
}

// removeProject deletes every thread of the project, and with them the
// threads under them, then forgets the project in the sidebar's filter.
// A draft in its workspace starts over in the home folder, so that the
// project doesn't come back with the next thread.
func (a *app) removeProject(p *project) {
	for _, t := range p.roots() {
		a.deleteTree(t)
	}
	delete(a.projectFilter, p.key)
	if a.fresh.env == p.env && a.fresh.workspace == p.key.workspace {
		a.fresh = draft{env: p.env}
	}
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
	// Every thread under the project's goes with it, one in another
	// workspace too; those working stop first.
	n, stopping := 0, 0
	seen := map[api.ID]bool{}
	for _, t := range p.roots() {
		for _, d := range append(a.below(t), node{t, 0}) {
			if seen[d.t.id()] {
				continue
			}
			seen[d.t.id()] = true
			n++
			if d.t.View.Working() {
				stopping++
			}
		}
	}
	head := "Remove “" + p.name() + "” and its thread?"
	body := "It's removed from every list and can't be brought back. Its history stays in the engine's log."
	if n > 1 {
		head = fmt.Sprintf("Remove “%s” and its %d threads?", p.name(), n)
		body = "They're removed from every list and can't be brought back. Their history stays in the engine's log."
	}
	if stopping > 0 {
		body = fmt.Sprintf("%d of them %s still working and will be stopped. ", stopping, isAre(stopping)) + body
	}
	body += " The folder, " + p.key.workspace + ", isn't touched."
	switch a.alert(c, &a.removeOpen, head, body, "Cancel", "Remove") {
	case 1:
		a.removeProject(p)
		a.removing = projectKey{}
	case 0, ui.AlertDismissed:
		a.removing = projectKey{}
	}
}
