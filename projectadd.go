package main

import (
	"log"
	"path/filepath"
	"slices"
	"time"

	"github.com/egoist/mygo"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/jsonfile"
)

// A project is the threads in a workspace on one machine, and a folder
// added there as one, kept in projects.json, which shows with no thread
// at all. The New project wizard (wizard.go) adds them.

// addedProject is a folder added as a project, as projects.json holds it.
type addedProject struct {
	Workspace string `json:"workspace"`
	// Server is the label of the server the folder is on, "" for this
	// machine, which files written before servers had projects mean.
	Server string `json:"server,omitempty"`
	// Engine is the engine the folder was added on. A server's folder is
	// on that engine alone, whatever label reaches it.
	Engine api.ID `json:"engine,omitzero"`
	// Added is RFC 3339, kept as text so that one written wrong hides
	// no project.
	Added string `json:"added"`
}

// on is whether the folder is on e: this machine's for one added here, the
// engine it was added on for a server's, never one not reached yet.
func (p addedProject) on(e *client.Env) bool {
	if e.Kind == client.Local {
		return p.Server == ""
	}
	return p.Server != "" && !p.Engine.IsZero() && p.Engine == e.ID
}

func (a *app) projectsPath() string { return filepath.Join(a.dir, "projects.json") }

// loadProjects takes the folders added.
func (a *app) loadProjects() {
	if err := jsonfile.Load(a.projectsPath(), &a.added); err != nil {
		log.Printf("glitched: projects: %v", err)
		a.added = nil
	}
}

// editProjects changes projects.json under its lock, onto what another
// window on the state directory added, and the list here with it. One it
// couldn't save still changes here, for as long as the app runs.
func (a *app) editProjects(change func(*[]addedProject)) {
	ps, err := jsonfile.EditOrReset(a.projectsPath(), func(ps *[]addedProject) error {
		change(ps)
		if *ps == nil {
			*ps = []addedProject{}
		}
		return nil
	})
	if err != nil {
		log.Printf("glitched: projects: %v", err)
		change(&a.added)
	} else {
		a.added = ps
	}
	a.addedEdits++
}

// recordProject keeps the folder ws as a project on e's machine, once.
func (a *app) recordProject(e *client.Env, ws string) {
	a.editProjects(func(ps *[]addedProject) {
		if !slices.ContainsFunc(*ps, func(p addedProject) bool { return p.Workspace == ws && p.on(e) }) {
			*ps = append(*ps, addedProject{ws, keyOf(e, "").server, e.ID, time.Now().UTC().Format(time.RFC3339)})
		}
	})
}

// forgetProject drops the folder ws on e's machine from the projects
// added, and no folder of the same path on another machine. It reads the
// file, not the list here: another window may have added it since this
// one loaded.
func (a *app) forgetProject(e *client.Env, ws string) {
	a.editProjects(func(ps *[]addedProject) {
		*ps = slices.DeleteFunc(*ps, func(p addedProject) bool { return p.Workspace == ws && p.on(e) })
	})
}

// chooseFolder opens the system's folder picker over the window, and is
// the folder picked; "" when none was, or there is no window.
var chooseFolder = func(a *app) string {
	if a.win == nil {
		return ""
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: a.win, Title: "Add project", ButtonLabel: "Add", Directory: true, CreateDirectories: true,
	})
	if err != nil || len(paths) == 0 {
		return ""
	}
	return paths[0]
}

// localEnv is this machine's engine, where a folder picked here is.
func (a *app) localEnv() *client.Env {
	for _, e := range a.backend.Envs() {
		if e.Kind == client.Local {
			return e
		}
	}
	return nil
}
