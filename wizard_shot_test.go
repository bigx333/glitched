package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
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
	h.click("Clone a repository")
	source := "acme/lighthouse"
	if step == "failed" {
		source = "file://" + filepath.ToSlash(filepath.Join(root, "missing.git"))
	}
	h.replaceIn("URL or owner/name", source)
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
