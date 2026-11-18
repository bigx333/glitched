package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	// Working, then completed, and maybe more as the turn ends: two at
	// least, each counted on its row.
	h.until("the changes counted on its row", func() bool {
		for _, text := range h.tt.Texts() {
			if n, ok := strings.CutSuffix(text, "×"); ok {
				if v, err := strconv.Atoi(n); err == nil && v >= 2 {
					return true
				}
			}
		}
		return false
	})
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
	h.tt.Frame()
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

// A plugin that hears thread.message is told of each new message of the
// thread shown once, and not of those it had when first shown.
func TestNewMessagesAreToldOnceEach(t *testing.T) {
	dir := tempDir(t)
	dst := filepath.Join(plugins.Dir(dir), "ears")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "plugin.json"), []byte(`{"id": "ears", "name": "Ears", "main": "index.js",
		"capabilities": ["ui.place", "messages:read"], "glitched": {"events": ["thread.message"], "places": [{"id": "count", "at": "sidebar.footer"}]}}`), 0o644)
	os.WriteFile(filepath.Join(dst, "index.js"), []byte(`
		export function init() { return { state: [] } }
		export function update({ state, event }) { return event.type === "thread.message" ? { state: [...state, event.data.role + ":" + event.data.text] } : {} }
		export function place({ state, items }) { return items.map(() => ({ type: "text", text: "heard " + state.length + " " + state.join(" | ") })) }`), 0o644)
	signAsOwn(t, dir, dst)
	grant(t, dir, `{"grants": {"ears": ["messages:read"]}}`)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	id := seed(t, d, "/work/glitched", "Fix the sort", "first")
	h := start(t, dir)
	th := h.thread("Fix the sort")
	h.a.select_(th)
	h.until("the transcript", func() bool { return th.Transcript != nil && len(entriesOf(th)) > 0 })
	h.until("nothing told of what it had", func() bool { return h.tt.HasText("heard 0") })
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "second"}}); err != nil {
		t.Fatal(err)
	}
	// The fake agent answers in two messages, around its work.
	h.until("the new message and its replies", func() bool {
		return h.tt.HasText("heard 3 user:second | assistant:Looking at the code. | assistant:")
	})
	for range 10 {
		h.p.run()
		h.tt.Frame()
	}
	if h.tt.HasText("heard 4") || h.tt.HasText("user:first") {
		t.Fatalf("told twice, or of what it had: %q", h.tt.Texts())
	}

	// Away from it, and back: what came meanwhile is told on the return.
	seed(t, d, "/work/glitched", "Write the docs")
	other := h.thread("Write the docs")
	h.a.select_(other)
	h.until("the other thread", func() bool { return other.Transcript != nil })
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "third"}}); err != nil {
		t.Fatal(err)
	}
	h.until("the turn done", func() bool {
		return h.thread("Fix the sort").View.LastTurn != nil && !h.thread("Fix the sort").View.Working() && h.thread("Fix the sort").View.Queued == 0
	})
	h.a.select_(h.thread("Fix the sort"))
	h.until("what came while away", func() bool { return h.tt.HasText("heard 6") && h.tt.HasText("user:third") })
}
