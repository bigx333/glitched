package main

import (
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/jsonfile"
	"glitched/internal/provider/fake"
	skillsync "glitched/internal/skills"
	"glitched/internal/transport"
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

// syncedHeader is the app with its state in <user>/.glitched, on a thread
// in <user>/widgets on a paired machine, box, with its state in
// <user>/box, which has skills of its own
// and the project's. With laptop, box holds the skills another machine,
// laptop, synced to it three hours ago, and this app syncs none of its
// own. It returns the app and the name of the machine box's synced skills
// came from.
func syncedHeader(t *testing.T, user string, laptop bool) (*harness, string) {
	t.Helper()
	dir, work, rdir := filepath.Join(user, ".glitched"), filepath.Join(user, "widgets"), filepath.Join(user, "box")
	writeSkill(t, filepath.Join(dir, "skills"), "hello", "Say hello the way I do.")
	writeSkill(t, filepath.Join(dir, "skills"), "deploy", "Ship the app to staging.")
	writeSkill(t, filepath.Join(rdir, "skills"), "hello", "The box's own hello.")
	writeSkill(t, filepath.Join(rdir, "skills"), "notes", "Keep notes on the box.")
	writeSkill(t, filepath.Join(work, ".glitched", "skills"), "deploy", "Deploy as this project does.")
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	r := engine(t, daemon.Config{Dir: rdir, Listen: []string{"127.0.0.1:0"}}, fake.New(nil))
	seed(t, r, work, "Fix the flaky test")
	from := ""
	if laptop {
		from = "laptop"
		elsewhere := tempDir(t)
		writeSkill(t, filepath.Join(elsewhere, "skills"), "hello", "Say hello as on the laptop.")
		c := transport.NewEndpointClient(transport.Tailnet{Address: r.Addrs()[0].String(), Token: token}, api.ID{})
		defer c.Close()
		if _, err := skillsync.Push(context.Background(), elsewhere, api.NewID(), from, c); err != nil {
			t.Fatal(err)
		}
		_, err := jsonfile.Edit(filepath.Join(rdir, "skills-synced", "current.json"), func(o *map[string]any) error {
			(*o)["pulled"] = time.Now().Add(-3*time.Hour - time.Minute).Format(time.RFC3339Nano)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		off := false
		if _, err := daemon.Edit(dir, func(c *daemon.Config) error { c.SyncSkills = &off; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	installOwn(t, dir, "skills")
	h := start(t, dir)
	if err := h.a.plugins.SetGranted("skills", "threads:read", true); err != nil {
		t.Fatal(err)
	}
	h.until("this machine's engine", func() bool { return !h.local().ID.IsZero() })
	if from == "" {
		// The engine names this machine by its hostname, which a shot
		// mustn't show.
		from = "studio"
		h.local().Name = from
	}
	if err := h.addServer(client.Server{Label: "box", Addr: r.Addrs()[0].String(), Token: token}); err != nil {
		t.Fatal(err)
	}
	h.a.select_(h.thread("Fix the flaky test"))
	h.until("the skills pill", func() bool { h.tt.Frame(); return h.tt.HasText("Skills 3") })
	if err := h.tt.Click("Skills 3"); err != nil {
		t.Fatal(err)
	}
	settleTester(h.tt)
	h.until("the synced skills", func() bool { h.tt.Frame(); return h.tt.HasText("From " + from) })
	return h, from
}

// A thread on a paired machine shows this machine's skills synced to it,
// from this machine, the one that replaces the machine's own hello, the
// project's deploy that replaces the synced one, and when they synced.
func TestTheHeaderShowsWhereSyncedSkillsCameFrom(t *testing.T) {
	user := tempDir(t)
	t.Setenv("HOME", user)
	h, here := syncedHeader(t, user, false)
	for _, want := range []string{"Synced from " + here + " just now", "Say hello the way I do.", "replaces yours", "Deploy as this project does.", "replaces " + here + "'s", "Keep notes on the box."} {
		if !h.tt.HasText(want) {
			t.Errorf("the popover doesn't say %q: %q", want, h.tt.Texts())
		}
	}
	for _, gone := range []string{"The box's own hello.", "Ship the app to staging."} {
		if h.tt.HasText(gone) {
			t.Errorf("the popover lists %q, which another replaces", gone)
		}
	}
}

// A machine that keeps the skills another machine synced to it, as one no
// app of that machine reaches now, says how old they are.
func TestTheHeaderSaysHowOldSyncedSkillsAre(t *testing.T) {
	user := tempDir(t)
	t.Setenv("HOME", user)
	h, _ := syncedHeader(t, user, true)
	for _, want := range []string{"Synced from laptop 3h ago", "Say hello as on the laptop.", "From laptop"} {
		if !h.tt.HasText(want) {
			t.Errorf("the popover doesn't say %q: %q", want, h.tt.Texts())
		}
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
	for name, laptop := range map[string]bool{"synced": false, "synced-3h": true} {
		user := tempDir(t)
		t.Setenv("HOME", user)
		h, _ := syncedHeader(t, user, laptop)
		h.a.settings.theme = darkTheme.name
		h.tt.Advance(time.Second)
		if host, _ := os.Hostname(); host != "" {
			short, _, _ := strings.Cut(host, ".")
			for _, text := range h.tt.Texts() {
				if strings.Contains(text, host) || strings.Contains(text, short) {
					t.Fatalf("the %s shot shows this machine's hostname: %q", name, text)
				}
			}
		}
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

// Once the node takes a change of this machine's skills, here a drop as
// sync is turned off, the popover shows what the node holds now: no
// origin for skills it no longer has.
func TestThePopoverDropsAnOriginTheNodeDropped(t *testing.T) {
	user := tempDir(t)
	t.Setenv("HOME", user)
	h, from := syncedHeader(t, user, false)
	th := h.thread("Fix the flaky test")
	off := false
	if err := h.a.backend.Configure(func(cfg *daemon.Config) { cfg.SyncSkills = &off }); err != nil {
		t.Fatal(err)
	}
	before := th.View.Revision
	h.a.backend.Send(th.env, th.id(), "message after disabling sync", api.AfterActive)
	h.until("the turn took the command and the node dropped the set", func() bool {
		_, err := os.Stat(filepath.Join(user, "box", "skills-synced", "current.json"))
		return errors.Is(err, os.ErrNotExist) && len(th.Pending) == 0 && th.View.Revision > before && th.View.Run == api.RunIdle
	})
	h.until("the popover no longer says where the dropped set came from", func() bool {
		return !h.tt.HasText("From " + from)
	})
}
