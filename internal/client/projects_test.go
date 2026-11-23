package client

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"glitched/internal/api"
)

// An engine older than projects.folders answers that it doesn't know the
// call, which the GUI hears as ErrTooOld, as it does for a clone.
func TestProjectsOfAnOlderEngineAreTooOld(t *testing.T) {
	dir := stateDir(t)
	serveOn(t, dir, &longThread{view: api.Thread{ID: api.NewID(), Title: "older"}})
	m, g := manager(t, dir)
	env := m.Envs()[0]
	online(t, g, env)
	var listed, cloned error
	ended := 0
	m.Folders(env, "~", func(_ api.FolderList, err error) { listed = err; ended++ })
	m.Clone(env, api.CloneRequest{Source: "o/lighthouse", Dest: "~/Projects/lighthouse"}, func(string) {}, func(_ api.Cloned, err error) { cloned = err; ended++ })
	g.until(t, "both answers", func() bool { return ended == 2 })
	if !errors.Is(listed, ErrTooOld) || !errors.Is(cloned, ErrTooOld) {
		t.Fatalf("an older engine's listing: %v, and clone: %v; want ErrTooOld for both", listed, cloned)
	}
}

// An engine older than skills.list answers that it doesn't know the call,
// which the GUI hears as ErrTooOld.
func TestSkillsOfAnOlderEngineAreTooOld(t *testing.T) {
	dir := stateDir(t)
	serveOn(t, dir, &longThread{view: api.Thread{ID: api.NewID(), Title: "older"}})
	m, g := manager(t, dir)
	env := m.Envs()[0]
	online(t, g, env)
	var got error
	ended := false
	m.Skills(env, api.NewID(), func(_ api.SkillList, err error) { got, ended = err, true })
	g.until(t, "the answer", func() bool { return ended })
	if !errors.Is(got, ErrTooOld) {
		t.Fatalf("an older engine's skills: %v, want ErrTooOld", got)
	}
}

// The engine this GUI embeds or attaches to lists its folders.
func TestFoldersOfTheLocalEngine(t *testing.T) {
	dir := stateDir(t)
	m, g := manager(t, dir)
	env := m.Envs()[0]
	online(t, g, env)
	folder := t.TempDir()
	if err := os.Mkdir(filepath.Join(folder, "lighthouse"), 0o755); err != nil {
		t.Fatal(err)
	}
	var list api.FolderList
	var err error
	ended := false
	m.Folders(env, folder, func(l api.FolderList, e error) { list, err, ended = l, e, true })
	g.until(t, "the listing", func() bool { return ended })
	if err != nil || list.Path != folder || len(list.Folders) != 1 || list.Folders[0].Name != "lighthouse" {
		t.Fatalf("listed %+v, %v; want lighthouse in %s", list, err, folder)
	}
}
