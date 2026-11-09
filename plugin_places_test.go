package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/plugins"
	"glitched/internal/provider/fake"
)

// grant writes plugins.json's grants, as the user leaves them in Settings.
func grant(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "plugins.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestARowBadgeDrawsOnEachThreadAndHearsItChange(t *testing.T) {
	dir := tempDir(t)
	install(t, dir, "row-status")
	grant(t, dir, `{"grants": {"row-status": ["threads:read"]}}`)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	one := seed(t, d, "/work/glitched", "Fix the sort")
	seed(t, d, "/work/glitched", "Write the docs")
	h := start(t, dir)
	h.thread("Fix the sort")
	h.until("a badge on each row", func() bool {
		app := h.a.plugins.App("row-status")
		n := 0
		for _, p := range h.a.gather() {
			for _, th := range p.threads {
				if app.PlaceView("badge", th.id().String()) != nil {
					n++
				}
			}
		}
		return n == 2 && h.tt.HasText("idle")
	})
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: one, Body: api.MessageDispatch{Text: "go"}}); err != nil {
		t.Fatal(err)
	}
	h.until("the changes counted on its row", func() bool { return h.tt.HasText("2×") }) // working, then completed
}

func TestARowBadgeWithoutItsGrantDrawsNothing(t *testing.T) {
	dir := tempDir(t)
	install(t, dir, "row-status")
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/glitched", "Fix the sort")
	h := start(t, dir)
	h.thread("Fix the sort")
	app := h.a.plugins.App("row-status")
	h.until("the place drawn", func() bool { return app.Rev > 0 })
	h.tt.Frame()
	if h.tt.HasText("idle") {
		t.Fatalf("a record without threads:read gave its status: %q", h.tt.Texts())
	}
}

func TestAHeaderWidgetItsTabItsSettingsAndItsShortcut(t *testing.T) {
	dir := tempDir(t)
	install(t, dir, "focus-timer")
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/glitched", "Fix the sort")
	h := start(t, dir)
	h.tt.SetSize(1320, 1000)
	th := h.thread("Fix the sort")
	h.a.select_(th)
	h.until("the header's button", func() bool { return h.tt.HasText("Focus 25 min") })

	h.click("Focus 25 min")
	h.until("the block running", func() bool { return h.tt.HasText("25 min") && h.tt.HasText("Stop") })

	// Its tab, in the thread's panel, about the thread.
	h.a.openPluginTab(pluginTab{"focus-timer", "log"}, true)
	h.until("the tab", func() bool { return h.tt.HasText("Focus blocks in this thread: 1") })
	h.a.showPluginTab = false
	h.tt.Frame()

	// Its settings, drawn natively; a change it hears redraws its place.
	h.click("Stop")
	h.until("stopped", func() bool { return h.tt.HasText("Focus 25 min") })
	h.a.router.Push("/settings/plugins")
	h.tt.Frame()
	for _, want := range []string{"Show the timer", "Length", "minutes", "When it ends", "Notify", "Stay quiet"} {
		if !h.tt.HasText(want) {
			t.Errorf("no %q in Settings: %q", want, h.tt.Texts())
		}
	}
	if err := h.a.plugins.SetSetting("focus-timer", "length", 40); err != nil {
		t.Fatal(err)
	}
	h.a.router.Push("/")
	h.until("the new length", func() bool { return h.tt.HasText("Focus 40 min") })

	// Its shortcut runs its command; its app session hears it.
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyF)
	h.until("the command's block", func() bool { return h.tt.HasText("40 min") && h.tt.HasText("Stop") })
	if v := h.a.plugins.SettingValues("focus-timer"); v["length"] != 40.0 {
		t.Fatalf("settings %v", v)
	}
	if st := h.a.plugins.Settings("focus-timer")[0].Status; st == nil || st.Badge != "Running" {
		t.Fatalf("status %+v", st)
	}
}

func TestSlotsAreStableNames(t *testing.T) {
	for _, s := range plugins.Slots {
		if s.Name == "thread.row" || s.Name == "thread.header" {
			if !s.Stable {
				t.Errorf("%s isn't stable", s.Name)
			}
		}
	}
}

// A page asks its plugin's service, which the engine runs with no window,
// and the service runs git as the user.
func TestAPageAsksItsServiceWhichRunsGit(t *testing.T) {
	dir, repo := tempDir(t), t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "start"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git: %v %s", err, out)
		}
	}
	os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x"), 0o644)
	install(t, dir, "repo-watch")
	grant(t, dir, `{"grants": {"repo-watch": ["service", "programs"]}, "settings": {"repo-watch": {"folder": "`+repo+`"}}}`)
	h := start(t, dir)
	h.click("Repo watch")
	h.until("the service's check", func() bool { return h.tt.HasText("1 changed") && h.tt.HasText("On main") })
	h.click("Check now")
	h.until("a second check in the table", func() bool {
		n := 0
		for _, s := range h.tt.Texts() {
			if s == "main" {
				n++
			}
		}
		return n >= 2
	})
}
