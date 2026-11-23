package main

import (
	"log"
	"path/filepath"
	"slices"
	"time"

	"github.com/egoist/mygo"

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
	// Added is RFC 3339, kept as text so that one written wrong hides
	// no project.
	Added string `json:"added"`
}

func (p addedProject) key() projectKey { return projectKey{p.Server, p.Workspace} }

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

// recordProject keeps k's folder as a project on its machine, once.
func (a *app) recordProject(k projectKey) {
	a.editProjects(func(ps *[]addedProject) {
		if !slices.ContainsFunc(*ps, func(p addedProject) bool { return p.key() == k }) {
			*ps = append(*ps, addedProject{k.workspace, k.server, time.Now().UTC().Format(time.RFC3339)})
		}
	})
}

// forgetProject drops k from the projects added, and no folder of the
// same path on another machine. It reads the file, not the list here:
// another window may have added k since this one loaded.
func (a *app) forgetProject(k projectKey) {
	a.editProjects(func(ps *[]addedProject) {
		*ps = slices.DeleteFunc(*ps, func(p addedProject) bool { return p.key() == k })
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
