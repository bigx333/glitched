package main

import (
	"log"
	"os"
	"path/filepath"
	"slices"

	"glitched/internal/client"
	"glitched/internal/jsonfile"
)

// A new thread's draft starts where the last thread on its machine was
// started: last-project.json holds that project's folder by the server's
// label, "" for this machine, the scratch root for No project.

func (a *app) lastProjectPath() string { return filepath.Join(a.dir, "last-project.json") }

func (a *app) loadLastProject() {
	if err := jsonfile.Load(a.lastProjectPath(), &a.lastProject); err != nil {
		log.Printf("glitched: last project: %v", err)
		a.lastProject = nil
	}
}

// rememberProject keeps k as the project last started in on its machine.
// One it couldn't save is still remembered here, for as long as the app
// runs.
func (a *app) rememberProject(k projectKey) {
	m, err := jsonfile.EditOrReset(a.lastProjectPath(), func(m *map[string]string) error {
		if *m == nil {
			*m = map[string]string{}
		}
		(*m)[k.server] = k.workspace
		return nil
	})
	if err != nil {
		log.Printf("glitched: last project: %v", err)
		if a.lastProject == nil {
			a.lastProject = map[string]string{}
		}
		a.lastProject[k.server] = k.workspace
		return
	}
	a.lastProject = m
}

// lastDraft is a draft on e in the project last started in there, while it
// is still one the workspace menu offers and, on this machine, its folder
// is still there.
func (a *app) lastDraft(e *client.Env) (draft, bool) {
	if e == nil {
		return draft{}, false
	}
	ws, ok := a.lastProject[keyOf(e, "").server]
	if !ok {
		return draft{}, false
	}
	k := keyOf(e, ws)
	if !slices.ContainsFunc(a.workspacesOn(e), func(p *project) bool { return p.key == k }) {
		return draft{}, false
	}
	if e.Kind != client.Remote && ws != a.scratchRoot() {
		if fi, err := os.Stat(ws); err != nil || !fi.IsDir() {
			return draft{}, false
		}
	}
	return draft{e, ws}, true
}
