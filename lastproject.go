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
// started: last-project.json holds that project's folder by machineKey,
// the scratch root for No project. An entry by a server's label, as files
// written before engine IDs hold them, is no server's.

func (a *app) lastProjectPath() string { return filepath.Join(a.dir, "last-project.json") }

func (a *app) loadLastProject() {
	if err := jsonfile.Load(a.lastProjectPath(), &a.lastProject); err != nil {
		log.Printf("glitched: last project: %v", err)
		a.lastProject = nil
	}
}

// rememberProject keeps the folder ws as the project last started in on
// e's machine. One it couldn't save is still remembered here, for as long
// as the app runs.
func (a *app) rememberProject(e *client.Env, ws string) {
	key, ok := machineKey(e)
	if !ok {
		return
	}
	m, err := jsonfile.EditOrReset(a.lastProjectPath(), func(m *map[string]string) error {
		if *m == nil {
			*m = map[string]string{}
		}
		(*m)[key] = ws
		return nil
	})
	if err != nil {
		log.Printf("glitched: last project: %v", err)
		if a.lastProject == nil {
			a.lastProject = map[string]string{}
		}
		a.lastProject[key] = ws
		return
	}
	a.lastProject = m
}

// lastDraft is a draft on e in the project last started in there, while it
// is still a project on e and, on this machine, its folder is still there;
// No project on this machine always is. A server's home row is no project
// of its own until a thread runs there.
func (a *app) lastDraft(e *client.Env) (draft, bool) {
	if e == nil {
		return draft{}, false
	}
	key, ok := machineKey(e)
	ws, last := a.lastProject[key]
	if !ok || !last {
		return draft{}, false
	}
	local := e.Kind != client.Remote
	if local && ws == a.scratchRoot() {
		return draft{e, ws}, true
	}
	k := keyOf(e, ws)
	if !slices.ContainsFunc(a.projects, func(p *project) bool { return p.env == e && p.key == k }) {
		return draft{}, false
	}
	if fi, err := os.Stat(ws); local && (err != nil || !fi.IsDir()) {
		return draft{}, false
	}
	return draft{e, ws}, true
}

// forgetLastProject drops the folder ws on e's machine from where the
// last threads were started, so that a project removed doesn't come back
// with the next new thread.
func (a *app) forgetLastProject(e *client.Env, ws string) {
	key, ok := machineKey(e)
	if !ok || a.lastProject[key] != ws {
		return
	}
	m, err := jsonfile.EditOrReset(a.lastProjectPath(), func(m *map[string]string) error {
		if (*m)[key] == ws {
			delete(*m, key)
		}
		return nil
	})
	if err != nil {
		log.Printf("glitched: last project: %v", err)
		delete(a.lastProject, key)
		return
	}
	a.lastProject = m
}
