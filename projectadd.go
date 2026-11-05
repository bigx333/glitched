package main

import (
	"github.com/egoist/mygo"

	"glitched/internal/client"
)

// Adding a project picks a folder on this machine and starts a new thread
// in it: a project is the threads in a workspace, so it shows in the
// sidebar once that thread is sent.

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
// would block, and shows a new thread in it.
func (a *app) addProject() {
	go func() {
		dir := chooseFolder(a)
		if dir == "" {
			return
		}
		a.post(func() {
			if e := a.localEnv(); e != nil {
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
