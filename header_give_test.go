package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"

	"github.com/egoist/mygo/ui"
)

const headerBranch = "glitched/header-fixture-3x1sdh"

// narrowHeader is a thread titled title, or a child so titled, with a
// branch, 82k tokens and an open pull request, shown with the sidebar
// hidden under macOS's title bar. add installs plugins in the state
// directory before the app opens it; its agent's turn plays script.
func narrowHeader(t *testing.T, title string, child bool, add func(dir string), script ...provider.Event) *harness {
	t.Helper()
	return headerFixture(t, title, child, nil, add, script...)
}

func headerFixture(t *testing.T, title string, child bool, as func(*api.ThreadCreate), add func(dir string), script ...provider.Event) *harness {
	t.Helper()
	work := githubBranch(t)
	playGh(t)
	dir := tempDir(t)
	f := fake.New(script)
	f.Tokens = 82000
	d := engine(t, daemon.Config{Dir: dir}, f)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	submit := func(id api.ID, b api.Intent) {
		t.Helper()
		if _, err := d.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	id := api.NewID()
	if child {
		submit(id, api.ThreadCreate{Workspace: work, Title: "The parent, with a title long enough to fill its crumb", Model: fakeModel, Project: work, Branch: headerBranch})
		seedChild(t, d, id, title, "hello")
	} else {
		create := api.ThreadCreate{Workspace: work, Title: title, Model: fakeModel, Project: work, Branch: headerBranch}
		if as != nil {
			as(&create)
		}
		submit(id, create)
		submit(id, api.MessageDispatch{Text: "hello", Mode: api.AfterActive})
	}
	installOwn(t, dir, "pull-requests")
	if add != nil {
		add(dir)
	}
	h := start(t, dir)
	for _, c := range []string{"threads:read", "programs"} {
		if err := h.a.plugins.SetGranted("pull-requests", c, true); err != nil {
			t.Fatal(err)
		}
	}
	h.tt.SetTitleBar(ui.TitleBar{Height: 52, Left: 78})
	h.a.select_(h.thread(title))
	h.until("the pull request's pill", func() bool { h.tt.Frame(); return h.tt.HasText("#12") })
	h.a.toggleSidebar(true)
	return h
}

// at shows the window w points wide, and lets the header settle.
func (h *harness) at(w int) {
	h.t.Helper()
	h.tt.SetSize(w, 860)
	for range 60 {
		h.p.run()
		h.tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
}

// inHeader is where the header draws s, if it does.
func (h *harness) inHeader(s string) (ui.Rect, bool) {
	r, ok := h.tt.Find(s)
	return r, ok && r.Y < headerHeight && r.W > 0
}

// fitsHeader says how each of controls doesn't fit the header w points
// wide: missing, past the window, or under another.
func (h *harness) fitsHeader(w int, controls ...string) {
	h.t.Helper()
	for _, p := range h.misfits(w, controls...) {
		h.t.Error(p)
	}
}

// misfits are how each of controls doesn't fit the header w points wide.
func (h *harness) misfits(w int, controls ...string) []string {
	var problems []string
	var shown []ui.Rect
	for _, s := range controls {
		r, ok := h.inHeader(s)
		if !ok {
			problems = append(problems, fmt.Sprintf("at %d points the header doesn't show %q: %q", w, s, h.tt.Texts()))
			continue
		}
		if r.X < 0 || r.X+r.W > float32(w) {
			problems = append(problems, fmt.Sprintf("at %d points %q is past the window: %+v", w, s, r))
		}
		for i, o := range shown {
			if r.X < o.X+o.W && o.X < r.X+r.W {
				problems = append(problems, fmt.Sprintf("at %d points %q is under %q: %+v, %+v", w, s, controls[i], r, o))
			}
		}
		shown = append(shown, r)
	}
	return problems
}

var headerControls = []string{"Show sidebar  ⌘B", "Open in", "Commit & push", "Toggle terminal  ⌘J", "Toggle diff  ⌘D", "Toggle preview  ⇧⌘P"}

// A header too narrow for its row gives way in steps, the actions to
// their icons first, and never draws one control under another or past
// the window: at 760 points, with the sidebar hidden, the pull request's
// pill is where a click opens it. NEL-131.
func TestANarrowHeaderGivesWayAndKeepsEachControlClickable(t *testing.T) {
	const title = "Header fixture for the narrow window"
	h := narrowHeader(t, title, false, nil)
	h.at(1320)
	for _, s := range []string{"work", headerBranch, "82.0k tokens", "#12"} {
		if _, ok := h.inHeader(s); !ok {
			t.Errorf("at 1320 points the header gave %q way: %q", s, h.tt.Texts())
		}
	}
	wide, _ := h.inHeader("Commit & push")

	for _, w := range []int{760, 600} {
		h.at(w)
		h.fitsHeader(w, append([]string{title, "Pull request #12"}, headerControls...)...)
		if r, _ := h.inHeader("Commit & push"); r.W >= wide.W {
			t.Errorf("at %d points Commit & push kept its label: %+v", w, r)
		}
	}

	h.at(760)
	pr, _ := h.inHeader("Pull request #12")
	h.tt.ClickAt(pr.X+pr.W/2, pr.Y+pr.H/2)
	h.until("the pull request's popover", func() bool { h.tt.Frame(); return h.tt.HasText("Open on GitHub") })

	h.at(1320)
	if r, _ := h.inHeader("Commit & push"); r.W != wide.W {
		t.Errorf("wide again, Commit & push is %+v, not %+v", r, wide)
	}
	if _, ok := h.inHeader("#12"); !ok {
		t.Errorf("wide again, the pull request's pill keeps to its icon: %q", h.tt.Texts())
	}
}

func TestANarrowHeaderGivesWayForAManagersPills(t *testing.T) {
	const title = "Header fixture for the narrow window"
	h := headerFixture(t, title, false, func(c *api.ThreadCreate) {
		c.Role, c.Sweep = api.RoleManager, &api.SweepSchedule{IntervalSeconds: 1800}
	}, nil)
	h.at(1320)
	for _, s := range []string{"Manager", "Sweep 30m", "#12"} {
		if _, ok := h.inHeader(s); !ok {
			t.Errorf("at 1320 points the header gave %q way: %q", s, h.tt.Texts())
		}
	}
	for _, w := range []int{760, 600} {
		h.at(w)
		controls := append([]string{title, "Pull request #12"}, headerControls...)
		if _, ok := h.inHeader("Sweep 30m"); ok {
			controls = append(controls, "Sweep 30m")
		}
		h.fitsHeader(w, controls...)
	}
}

// A child's way back up takes the header's room as the rest of the row
// does: a short title leaves no button past the window.
func TestAChildsHeaderGivesWayForItsCrumbs(t *testing.T) {
	h := narrowHeader(t, "Fix", true, nil)
	h.at(1320)
	h.at(760)
	h.fitsHeader(760, append([]string{"Fix", "Pull request #12"}, headerControls...)...)
}

// What the header gave way comes back once what took its room goes, as
// the usage pill as the thread's tokens are cleared: it draws as a header
// opened at that width does.
func TestTheHeaderTakesBackWhatItGaveWay(t *testing.T) {
	h := narrowHeader(t, "Header fixture for the narrow window", false, nil)
	h.at(1320)
	h.at(850)
	tt := h.a.current()
	tt.View.Tokens, tt.View.Context = 0, nil
	h.at(850)
	_, branch := h.inHeader(headerBranch)
	_, project := h.inHeader("work")
	h.at(690)
	h.at(850)
	_, fresh := h.inHeader(headerBranch)
	_, freshProject := h.inHeader("work")
	if branch != fresh || project != freshProject {
		t.Errorf("the pills after the tokens went: branch %v, project %v; opened at that width: branch %v, project %v", branch, project, fresh, freshProject)
	}
}

// A plugin slow to draw its copy compact neither stalls a frame nor leaves
// its copy out: the header measures the step once the copy is drawn for
// it.
func TestASlowCompactCopyNeitherStallsNorGoes(t *testing.T) {
	was := runPluginProgram
	runPluginProgram = func(ctx context.Context, id string, allowed, denied []string, raw json.RawMessage) (any, error) {
		var a struct{ Args []string }
		json.Unmarshal(raw, &a)
		if id == "slow-pill" {
			time.Sleep(450 * time.Millisecond)
			return map[string]any{"status": 0, "stdout": "", "stderr": ""}, nil
		}
		return was(ctx, id, allowed, denied, raw)
	}
	t.Cleanup(func() { runPluginProgram = was })
	h := narrowHeader(t, "Header fixture for the narrow window", false, func(dir string) {
		folder := filepath.Join(dir, "plugins", "slow-pill")
		os.MkdirAll(folder, 0o700)
		os.WriteFile(filepath.Join(folder, "plugin.json"), []byte(`{"id":"slow-pill","name":"Slow pill","main":"main.js","contract":"glitched","contractVersion":2,"capabilities":["ui.place","programs"],"glitched":{"programs":["git"],"places":[{"id":"pill","at":"thread.header"}]}}`), 0o600)
		os.WriteFile(filepath.Join(folder, "main.js"), []byte(`
export async function place({ items }) {
  return Promise.all(items.map(async ({ compact }) => {
    if (compact) await glitched.run("git", ["status"], { cwd: "/" });
    return { type: "button", id: "pill", style: "outline", icon: "pull-request", label: compact ? "Slow compact" : "Slow and very wide pill of a plugin", compact: Boolean(compact) };
  }));
}`), 0o600)
		signAsOwn(t, dir, folder)
	})
	if err := h.a.plugins.SetGranted("slow-pill", "programs", true); err != nil {
		t.Fatal(err)
	}
	h.at(1320)
	if _, ok := h.inHeader("Slow and very wide pill of a plugin"); !ok {
		t.Fatalf("the slow plugin's pill: %q", h.tt.Texts())
	}
	h.tt.SetSize(680, 860)
	var longest time.Duration
	for range 150 {
		start := time.Now()
		h.p.run()
		h.tt.Frame()
		longest = max(longest, time.Since(start))
		time.Sleep(5 * time.Millisecond)
	}
	if longest > 200*time.Millisecond {
		t.Errorf("a frame waited %v on a plugin's drawing", longest)
	}
	_, wide := h.inHeader("Slow and very wide pill of a plugin")
	_, compact := h.inHeader("Slow compact")
	if !wide && !compact {
		t.Errorf("the header left the slow plugin's pill out at step %d: %q", h.a.headerGive.step, h.tt.Texts())
	}
	h.fitsHeader(680, headerControls...)
}
