package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// writeSkill puts a skill named name in root, as the user would.
func writeSkill(t *testing.T, root, name, description string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nDo it well.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

// skillsHeader is the app with its state in dir, on a thread in work whose
// engine finds the skills set puts in its home and project folders, with
// the built-in Skills drawing the header's pill, and the pill's label once
// it shows.
func skillsHeader(t *testing.T, dir, work string, set func(home, project string), want string) *harness {
	t.Helper()
	set(filepath.Join(dir, "skills"), filepath.Join(work, ".glitched", "skills"))
	d := engine(t, daemon.Config{Dir: dir}, fake.New(nil))
	seed(t, d, work, "Fix the flaky test")
	installOwn(t, dir, "skills")
	h := start(t, dir)
	if err := h.a.plugins.SetGranted("skills", "threads:read", true); err != nil {
		t.Fatal(err)
	}
	h.a.select_(h.thread("Fix the flaky test"))
	h.until("the skills pill", func() bool { h.tt.Frame(); return h.tt.HasText(want) })
	return h
}

func clashingSkills(t *testing.T) func(home, project string) {
	return func(home, project string) {
		writeSkill(t, home, "deploy", "Ship the app to staging.")
		writeSkill(t, home, "review", "Review a diff the way I like.")
		writeSkill(t, project, "review", "Review the diff as this project does.")
		writeSkill(t, project, "Bad_Name", "Never shown.")
	}
}

// The header of a thread shows the skills its engine finds for it, as the
// built-in Skills draws them, the project's replacing the user's own of
// a name, and one the engine skips with why; a click opens the popover.
func TestTheHeaderShowsTheThreadsSkills(t *testing.T) {
	h := skillsHeader(t, tempDir(t), t.TempDir(), clashingSkills(t), "Skills 2")
	if h.tt.HasText("replaces yours") {
		t.Fatal("the popover shows before a click")
	}
	if err := h.tt.Click("Skills 2"); err != nil {
		t.Fatal(err)
	}
	settleTester(h.tt)
	h.until("the popover", func() bool { h.tt.Frame(); return h.tt.HasText("replaces yours") })
	for _, want := range []string{"deploy", "Ship the app to staging.", "Yours", "review", "Review the diff as this project does.", "Project", "Skipped", "Bad_Name"} {
		if !h.tt.HasText(want) {
			t.Errorf("the popover doesn't say %q: %q", want, h.tt.Texts())
		}
	}
	if h.tt.HasText("Review a diff the way I like.") {
		t.Errorf("the popover lists the user's review the project's replaces: %q", h.tt.Texts())
	}
}

// TestSkillsShot writes PNGs of the Skills pill's popover to the folder
// SKILLSHOT names: the user's skills alone, and the user's and the
// project's with a clash and a skipped one. Glitched's folder and the
// project are in a home of the test's, so no path of this machine shows.
func TestSkillsShot(t *testing.T) {
	out := os.Getenv("SKILLSHOT")
	if out == "" {
		t.Skip()
	}
	os.MkdirAll(out, 0o755)
	home := func(home, _ string) {
		writeSkill(t, home, "deploy", "Ship the app to staging.")
		writeSkill(t, home, "release-notes", "Write the release notes from the merged pull requests since the last tag.")
	}
	for name, set := range map[string]func(string, string){"home": home, "clash": clashingSkills(t)} {
		user := t.TempDir()
		t.Setenv("HOME", user)
		h := skillsHeader(t, filepath.Join(user, ".glitched"), filepath.Join(user, "widgets"), set, "Skills 2")
		h.a.settings.theme = darkTheme.name
		if err := h.tt.Click("Skills 2"); err != nil {
			t.Fatal(err)
		}
		settleTester(h.tt)
		h.until("the popover", func() bool { h.tt.Frame(); return h.tt.HasText("Ship the app to staging.") })
		h.tt.Advance(time.Second)
		f, err := os.Create(filepath.Join(out, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, h.tt.Image())
		f.Close()
	}
}

// A skill added while the window was away shows once it is back: the
// header asks the engine again for every thread it shows.
func TestComingBackToTheWindowReadsTheSkillsAgain(t *testing.T) {
	var home string
	h := skillsHeader(t, tempDir(t), t.TempDir(), func(h, project string) {
		home = h
		clashingSkills(t)(h, project)
	}, "Skills 2")
	writeSkill(t, home, "release-notes", "Write the release notes.")
	h.tt.SetWindowActive(false)
	h.tt.Frame()
	h.tt.SetWindowActive(true)
	h.until("the new skill's pill", func() bool { h.tt.Frame(); return h.tt.HasText("Skills 3") })
}

// The Skills popover shuts as its thread's header goes, and coming back to
// the thread doesn't open it again without a click.
func TestTheSkillsPopoverStaysShutAfterSwitchingThreads(t *testing.T) {
	dir, work := tempDir(t), t.TempDir()
	const description = "Review the diff without changing it."
	writeSkill(t, filepath.Join(work, ".glitched", "skills"), "review", description)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(nil))
	seed(t, d, work, "Thread with skills")
	seed(t, d, t.TempDir(), "Thread without skills")
	installOwn(t, dir, "skills")
	h := start(t, dir)
	if err := h.a.plugins.SetGranted("skills", "threads:read", true); err != nil {
		t.Fatal(err)
	}
	h.a.select_(h.thread("Thread with skills"))
	h.until("first header's Skills pill", func() bool { h.tt.Frame(); return h.tt.HasText("Skills 1") })
	if err := h.tt.Click("Skills 1"); err != nil {
		t.Fatal(err)
	}
	h.until("open Skills popover", func() bool { h.tt.Frame(); return h.tt.HasText(description) })
	h.a.select_(h.thread("Thread without skills"))
	h.until("other header", func() bool {
		h.tt.Frame()
		return !h.tt.HasText("Skills 1") && !h.tt.HasText(description)
	})
	settleTester(h.tt)
	h.a.select_(h.thread("Thread with skills"))
	h.until("original header", func() bool { h.tt.Frame(); return h.tt.HasText("Skills 1") })
	settleTester(h.tt)
	if h.tt.HasText(description) {
		t.Fatal("the Skills popover reopened without a click")
	}
}
