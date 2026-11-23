package main

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"

	"github.com/egoist/mygo/ui"
)

// A header too narrow for its row gives way in steps, the actions to
// their icons first, and never draws one control under another or past
// the window: at 760 points, with the sidebar hidden, the pull request's
// pill is where a click opens it. NEL-131.
func TestANarrowHeaderGivesWayAndKeepsEachControlClickable(t *testing.T) {
	work := githubBranch(t)
	playGh(t)
	dir := tempDir(t)
	f := fake.New(nil)
	f.Tokens = 82000
	d := engine(t, daemon.Config{Dir: dir}, f)
	const title = "Header fixture for the narrow window"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := api.NewID()
	for _, b := range []api.Intent{
		api.ThreadCreate{Workspace: work, Title: title, Model: fakeModel, Project: work, Branch: "glitched/header-fixture-3x1sdh"},
		api.MessageDispatch{Text: "hello", Mode: api.AfterActive},
	} {
		if _, err := d.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	installOwn(t, dir, "pull-requests")
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
	at := func(w int) {
		t.Helper()
		h.tt.SetSize(w, 860)
		for range 60 {
			h.p.run()
			h.tt.Frame()
			time.Sleep(5 * time.Millisecond)
		}
	}
	controls := []string{"Show sidebar  ⌘B", title, "Pull request #12", "Open in", "Commit & push", "Toggle terminal  ⌘J", "Toggle diff  ⌘D", "Toggle preview  ⇧⌘P"}
	inHeader := func(s string) (ui.Rect, bool) {
		r, ok := h.tt.Find(s)
		return r, ok && r.Y < headerHeight
	}

	at(1320)
	for _, s := range []string{"work", "glitched/header-fixture-3x1sdh", "82.0k tokens", "#12"} {
		if _, ok := inHeader(s); !ok {
			t.Errorf("at 1320 points the header gave %q way: %q", s, h.tt.Texts())
		}
	}
	wide, _ := inHeader("Commit & push")

	for _, w := range []int{760, 600} {
		at(w)
		var shown []ui.Rect
		for _, s := range controls {
			r, ok := inHeader(s)
			if !ok {
				t.Errorf("at %d points the header doesn't show %q: %q", w, s, h.tt.Texts())
				continue
			}
			if r.X < 0 || r.X+r.W > float32(w) {
				t.Errorf("at %d points %q is past the window: %+v", w, s, r)
			}
			for i, o := range shown {
				if r.X < o.X+o.W && o.X < r.X+r.W {
					t.Errorf("at %d points %q is under %q: %+v, %+v", w, s, controls[i], r, o)
				}
			}
			shown = append(shown, r)
		}
		if r, _ := inHeader("Commit & push"); r.W >= wide.W {
			t.Errorf("at %d points Commit & push kept its label: %+v", w, r)
		}
	}

	at(760)
	pr, _ := inHeader("Pull request #12")
	h.tt.ClickAt(pr.X+pr.W/2, pr.Y+pr.H/2)
	h.until("the pull request's popover", func() bool { h.tt.Frame(); return h.tt.HasText("Open on GitHub") })

	at(1320)
	if r, _ := inHeader("Commit & push"); r.W != wide.W {
		t.Errorf("wide again, Commit & push is %+v, not %+v", r, wide)
	}
	if _, ok := inHeader("#12"); !ok {
		t.Errorf("wide again, the pull request's pill keeps to its icon: %q", h.tt.Texts())
	}
}
