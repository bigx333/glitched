package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/bundled"
	"glitched/internal/plugins/pack"
)

// schedulesPage is schedulesApp showing the Schedules page in Settings.
func schedulesPage(t *testing.T) *harness {
	t.Helper()
	h := schedulesApp(t)
	h.a.router.Push(pluginKey{"schedules", "schedules"}.settingsPath())
	h.frames("the Schedules page", func() bool { return h.tt.HasText("No schedules yet.") })
	return h
}

// schedulesApp is the app on an engine, with this checkout's Schedules
// as a copy signed by this device, which runs in the built-in's place, so
// the page and its service run as changed here, signed or not by the
// release key; and a project on this machine, orchard.
func schedulesApp(t *testing.T) *harness {
	t.Helper()
	dir := tempDir(t)
	ws := filepath.Join(t.TempDir(), "orchard")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	rememberedState(t, dir, []string{ws}, "")
	sub, err := fs.Sub(bundled.FS, "schedules")
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(plugins.Dir(dir), "schedules")
	if err := os.CopyFS(dst, sub); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dst, pack.SignatureFile))
	signAsOwn(t, dir, dst)
	grant(t, dir, `{"grants": {"schedules": ["service", "threads:read", "threads:write", "threads:access"]}}`)
	h := start(t, dir)
	h.tt.SetSize(1320, 1100) // the whole form, without scrolling to Save
	return h
}

// frames runs what the app posts, with the frame the window's Update draws
// after it, and the frames the app asks for, 16 ms of the tester's clock
// at a time, till ok holds: no frame the app didn't ask for.
func (h *harness) frames(what string, ok func() bool) {
	h.t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		if h.p.run() {
			h.tt.Frame() // as the window's Update draws one after what it runs
		}
		h.tt.Advance(16 * time.Millisecond)
		if ok() {
			return
		}
		select {
		case <-h.p.wake:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s; the window shows %q", what, h.tt.Texts())
		}
	}
}

// press clicks the control named s once it shows.
func (h *harness) press(s string) {
	h.t.Helper()
	h.frames(fmt.Sprintf("%q to click", s), func() bool { _, ok := h.tt.Find(s); return ok })
	if err := h.tt.Click(s); err != nil {
		h.t.Fatal(err)
	}
	h.tt.Advance(16 * time.Millisecond)
}

// fill types text into the plugin's input under label.
func (h *harness) fill(label, text string) {
	h.t.Helper()
	r, ok := h.tt.Find(label)
	if !ok {
		h.t.Fatalf("no %q in %q", label, h.tt.Texts())
	}
	h.tt.ClickAt(r.X+20, r.Y+r.H-14) // the field: its label over its input
	h.tt.Advance(16 * time.Millisecond)
	h.tt.Type(text)
	h.tt.Advance(16 * time.Millisecond)
}

// service calls the Schedules service as the page does.
func (h *harness) service(method string, args, out any) {
	h.t.Helper()
	raw, _ := json.Marshal(map[string]any{"plugin": "schedules", "method": method, "args": args})
	b, err := h.a.serviceClient(h.a.dir).PluginCall(context.Background(), raw)
	if err != nil {
		h.t.Fatalf("schedules.%s: %v", method, err)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			h.t.Fatalf("schedules.%s: %s: %v", method, b, err)
		}
	}
}

type pageSchedule struct {
	ID, Title, Zone, NextDue, LastThread, LastState string
	Enabled                                         bool
}

func (h *harness) schedules() []pageSchedule {
	h.t.Helper()
	var page struct{ Schedules []pageSchedule }
	h.service("list", nil, &page)
	return page.Schedules
}

func (h *harness) engineThreads() []api.Thread {
	h.t.Helper()
	snap, err := h.a.serviceClient(h.a.dir).Read(context.Background(), api.Query{})
	if err != nil {
		h.t.Fatal(err)
	}
	return snap.Threads
}

// nextRun is how the page says when s runs next.
func nextRun(t *testing.T, s pageSchedule) string {
	t.Helper()
	zone, err := time.LoadLocation(s.Zone)
	if err != nil {
		t.Fatal(err)
	}
	due, err := time.Parse(time.RFC3339, s.NextDue)
	if err != nil {
		t.Fatalf("next run %q: %v", s.NextDue, err)
	}
	return "Next run " + due.In(zone).Format("Mon 2 Jan, 15:04")
}

// added fills the form New schedule opens and adds the schedule, which
// the list then shows with its next run.
func (h *harness) added(title string) pageSchedule {
	h.t.Helper()
	h.press("New schedule")
	h.frames("the form", func() bool { return h.tt.HasText("Add schedule") })
	h.fill("Title", title)
	h.fill("Prompt", "Read the open issues and sort them.")
	h.press("Add schedule")
	var s pageSchedule
	h.frames("the schedule's row", func() bool {
		for _, x := range h.schedules() {
			if x.Title == title {
				s = x
			}
		}
		return s.ID != "" && !h.tt.HasText("Add schedule") && h.tt.HasText(title)
	})
	return s
}

// shot writes the window to the folder SCHEDSHOT names, if it names one.
func (h *harness) shot(name string) {
	h.t.Helper()
	out := os.Getenv("SCHEDSHOT")
	if out == "" {
		return
	}
	os.MkdirAll(out, 0o755)
	f, err := os.Create(filepath.Join(out, name+".png"))
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, h.tt.Image()); err != nil {
		h.t.Fatal(err)
	}
}

// A schedule saved from the form is a row the list shows with its next
// run, its project and when it runs in words.
func TestASavedScheduleIsARowWithItsNextRun(t *testing.T) {
	h := schedulesPage(t)
	h.shot("before")
	s := h.added("Morning triage")
	if !s.Enabled || s.NextDue == "" {
		t.Fatalf("saved %+v, want it on with a next run", s)
	}
	line := nextRun(t, s) + " · No runs yet"
	h.frames("its next run", func() bool { return h.tt.HasText(line) })
	if words := "orchard · Weekdays at 09:00, " + s.Zone; !h.tt.HasText(words) {
		t.Fatalf("no %q in %q", words, h.tt.Texts())
	}
	h.shot("after")
	h.press("Edit")
	h.frames("the form", func() bool { return h.tt.HasText("EDIT SCHEDULE") })
	h.shot("form")
}

// The enable switch turns a schedule off: it has no next run.
func TestTheEnableSwitchClearsTheNextRun(t *testing.T) {
	h := schedulesPage(t)
	s := h.added("Morning triage")
	line := nextRun(t, s) + " · No runs yet"
	h.frames("its next run", func() bool { return h.tt.HasText(line) })
	h.press("Run Morning triage")
	h.frames("the schedule off", func() bool { return h.tt.HasText("Off · No runs yet") })
	if got := h.schedules(); len(got) != 1 || got[0].Enabled || got[0].NextDue != "" {
		t.Fatalf("after the switch the service has %+v, want it off with no next run", got)
	}
	// An edit saved after keeps it off.
	h.press("Edit")
	h.frames("the form", func() bool { return h.tt.HasText("EDIT SCHEDULE") })
	h.press("Save")
	h.frames("the edit saved", func() bool { return !h.tt.HasText("EDIT SCHEDULE") && h.tt.HasText("Off · No runs yet") })
	if got := h.schedules(); len(got) != 1 || got[0].Enabled || got[0].NextDue != "" {
		t.Fatalf("after an edit the service has %+v, want it still off", got)
	}
}

// Run now starts one thread; the same Run now tried again, by its op,
// starts none.
func TestRunNowStartsOneThread(t *testing.T) {
	h := schedulesPage(t)
	s := h.added("Morning triage")
	h.press("Run now")
	h.frames("the run's thread", func() bool { return h.tt.HasText("Open thread") })
	th := h.engineThreads()
	if len(th) != 1 {
		t.Fatalf("Run now made %d threads, want 1", len(th))
	}
	var runs []struct{ Key, Thread string }
	h.service("runs", map[string]any{"id": s.ID}, &runs)
	if len(runs) != 1 || runs[0].Thread != th[0].ID.String() {
		t.Fatalf("runs %+v, want one with the thread %s", runs, th[0].ID)
	}
	op := runs[0].Key[strings.LastIndex(runs[0].Key, ":")+1:]
	var again struct{ Thread string }
	h.service("runNow", map[string]any{"id": s.ID, "op": op}, &again)
	if again.Thread != th[0].ID.String() {
		t.Fatalf("the same op again answered %q, want %s", again.Thread, th[0].ID)
	}
	if n := len(h.engineThreads()); n != 1 {
		t.Fatalf("the same op again made %d threads, want the 1", n)
	}
}

// Delete takes the schedule's row away, and keeps the threads its runs
// started.
func TestDeleteRemovesTheRowAndKeepsTheThreads(t *testing.T) {
	h := schedulesPage(t)
	h.added("Morning triage")
	h.press("Run now")
	h.frames("the run's thread", func() bool { return h.tt.HasText("Open thread") })
	h.press("Delete")
	h.press("Delete schedule")
	h.frames("no row", func() bool { return h.tt.HasText("No schedules yet.") && !h.tt.HasText("Morning triage") })
	if got := h.schedules(); len(got) != 0 {
		t.Fatalf("after Delete the service has %+v", got)
	}
	if n := len(h.engineThreads()); n != 1 {
		t.Fatalf("after Delete the engine has %d threads, want the run's 1", n)
	}
}

// The page shows a thousand schedules of the longest titles and prompts,
// in a folder of the longest name, the most there can be, 100 rows at a
// time: under the widgets a view may have and the state a page may keep.
func TestThePageShowsTheMostSchedulesThereCanBe(t *testing.T) {
	h := schedulesPage(t)
	prompt := strings.Repeat("検", 4000)
	title := func(i int) string { return fmt.Sprintf("s%04d ", i) + strings.Repeat("検", 194) }
	folder := "/src/" + strings.Repeat("フ", 85)
	for i := range 1000 {
		h.service("save", map[string]any{"schedule": map[string]any{"title": title(i), "prompt": prompt, "workspace": folder,
			"provider": "fake", "model": "fake", "kind": "interval", "every": 60}}, nil)
	}
	h.press("Refresh")
	h.frames("the first 100", func() bool {
		return h.tt.HasText("1–100 of 1000") && h.tt.HasText(title(99)) && !h.tt.HasText(title(100))
	})
	h.press("Next")
	h.frames("the next 100", func() bool { return h.tt.HasText("101–200 of 1000") && h.tt.HasText(title(100)) })
	if s, err := h.a.plugins.Session("schedules", "schedules"); err != nil || s.Err != nil {
		t.Fatalf("the page: %v, %v", err, s.Err)
	}
}

// Schedules: New schedule opens the page on an empty form: the page not
// yet opened, then left open elsewhere, then shown.
func TestNewScheduleOpensThePageOnAnEmptyForm(t *testing.T) {
	h := schedulesApp(t)
	h.frames("the app", func() bool { return h.tt.HasText("Settings") })
	for _, from := range []string{"", "/", ""} {
		if from != "" {
			h.a.router.Push(from)
			h.tt.Advance(16 * time.Millisecond)
		}
		h.a.plugins.RunCommand("schedules", "new", nil, func(err error) {
			if err != nil {
				t.Error(err)
			}
		})
		h.frames("the empty form", func() bool {
			return h.a.router.Path() == (pluginKey{"schedules", "schedules"}).settingsPath() && h.tt.HasText("Add schedule")
		})
		h.press("Cancel")
		h.frames("no form", func() bool { return !h.tt.HasText("Add schedule") })
	}
}
