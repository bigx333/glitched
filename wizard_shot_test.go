package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// wizardShot is the New project wizard at step, on this machine and two
// servers: build-box, online, whose folders are a test's, and laptop,
// offline.
func wizardShot(t *testing.T, step string) *harness {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"api", "billing/.git", "docs", "lighthouse/.git", "notes", "scratch", ".config"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if step == "failed" {
		// The clone goes into ~/Projects on build-box, whose engine is in
		// this process: a home of the test's keeps it off the real one.
		t.Setenv("HOME", t.TempDir())
	}
	h, box := wizardMachines(t, inboxDir(t))
	h.local().Name = "studio"
	h.thread("SIP re-INVITE handling")
	newProject(h)
	if step == "machine" {
		h.key(0, ui.KeyDown)
		return h
	}
	h.click("build-box")
	switch step {
	case "source":
		return h
	case "browse":
		h.click("Existing folder")
		h.untilShown("the browser", "Choose a folder on build-box")
		h.replaceIn("Path", root)
		h.key(0, ui.KeyEnter)
		h.until("the folders", func() bool { return showing(h, "lighthouse") })
		h.key(0, ui.KeyDown)
		h.key(0, ui.KeyDown)
		return h
	case "offline":
		h.click("Existing folder")
		h.untilShown("the browser", "Choose a folder on build-box")
		h.replaceIn("Path", filepath.Join(root, "lighthouse"))
		h.key(0, ui.KeyEnter)
		h.until("the folder", func() bool { return h.a.wizard.browse.shown.Path == filepath.Join(root, "lighthouse") })
		h.click("Use this folder")
		box.stop()
		h.until("build-box offline", func() bool { return !h.a.reachable(h.a.serverEnv("build-box")) })
		h.click("Create")
		return h
	}
	if step == "repos" {
		stubRepos(t,
			api.Repo{Name: "acme/lighthouse", Description: "Status page and beacon service", Private: true},
			api.Repo{Name: "acme/harbor", Description: "Deploy previews"},
			api.Repo{Name: "acme/billing"},
			api.Repo{Name: "infra/terraform", Description: "Network and clusters"},
			api.Repo{Name: "me/dotfiles"},
			api.Repo{Name: "me/notes", Description: "Scratch notes"},
		)
		h.click("Clone a repository")
		h.untilShown("the repositories", "acme/lighthouse")
		h.click("Back")
		// Back with a stale cache and a gh that won't answer, so the
		// cached list shows under "Checking for new repositories…".
		stubGH(t, "[ \"$1\" = auth ] && exit 0\n/bin/sleep 25\nexit 0")
		h.tt.Advance(31 * time.Second)
		h.tt.Frame()
		h.click("Clone a repository")
		h.untilShown("the refresh", "Checking for new repositories…")
		h.replaceIn("Repository", "acme")
		return h
	}
	h.click("Clone a repository")
	source := "acme/lighthouse"
	if step == "failed" {
		source = "file://" + filepath.ToSlash(filepath.Join(root, "missing.git"))
	}
	h.replaceIn("Repository", source)
	if step == "clone" {
		return h
	}
	h.click("Next")
	switch step {
	case "cloning":
		box.hold = true
		h.click("Clone and create")
		h.untilShown("the clone's progress", "Receiving objects:  10% (1/10)")
	case "failed":
		h.click("Clone and create")
		h.until("the failure", func() bool { return h.a.wizard.failure != "" })
	}
	return h
}
