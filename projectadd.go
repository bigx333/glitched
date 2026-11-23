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

// Adding a project picks a folder on this machine, keeps it in
// projects.json, and starts a new thread in it. A project is the threads
// in a workspace, and on this machine a folder added too, which shows
// with no thread at all.

// addedProject is a folder added as a project on this machine, as
// projects.json holds it.
type addedProject struct {
	Workspace string    `json:"workspace"`
	Added     time.Time `json:"added"`
}

func (a *app) projectsPath() string { return filepath.Join(a.dir, "projects.json") }

// loadProjects takes the folders added on this machine.
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

// recordProject keeps dir as a project, once.
func (a *app) recordProject(dir string) {
	a.editProjects(func(ps *[]addedProject) {
		if !slices.ContainsFunc(*ps, func(p addedProject) bool { return p.Workspace == dir }) {
			*ps = append(*ps, addedProject{dir, time.Now().UTC().Truncate(time.Second)})
		}
	})
}

// forgetProject drops dir from the projects added, if it is one.
func (a *app) forgetProject(dir string) {
	if !slices.ContainsFunc(a.added, func(p addedProject) bool { return p.Workspace == dir }) {
		return
	}
	a.editProjects(func(ps *[]addedProject) {
		*ps = slices.DeleteFunc(*ps, func(p addedProject) bool { return p.Workspace == dir })
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

// addProject asks for a folder, off the main thread, which the picker
// would block, keeps it as a project and shows a new thread in it. One
// picker opens at a time.
func (a *app) addProject() {
	if a.picking {
		return
	}
	a.picking = true
	go func() {
		dir := chooseFolder(a)
		a.post(func() {
			a.picking = false
			// This machine's engine is always listed first, so never one
			// shown by another's.
			if e := a.localEnv(); dir != "" && e != nil {
				dir = filepath.Clean(dir)
				a.recordProject(dir)
				a.startIn(draft{e, dir})
				a.draftArea.Focus()
			}
		})
	}()
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
