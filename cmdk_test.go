package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// The palette lists threads by recency, opens one with its diff on ⌘↩,
// keeps to actions after >, and starts a thread in a chosen project.
func TestPaletteCommands(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.a.openCommandPalette()
	h.tt.Frame()
	var last time.Time
	n := 0
	for _, cmd := range h.a.commands() {
		if cmd.t == nil {
			continue
		}
		if n > 0 && cmd.t.View.UpdatedAt.After(last) {
			t.Errorf("%q, updated %v, listed after a thread updated %v", cmd.label, cmd.t.View.UpdatedAt, last)
		}
		last, n = cmd.t.View.UpdatedAt, n+1
	}
	if n != 5 {
		t.Errorf("%d threads listed, want 5", n)
	}
	h.tt.Type("websocket")
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	loop := h.thread("Investigate websocket reconnect loop")
	if h.a.cmdkOpen || h.a.selected != loop.id() || !h.a.showDiff {
		t.Errorf("⌘↩: open %v, selected %v, diff %v", h.a.cmdkOpen, h.a.selected, h.a.showDiff)
	}

	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type(">")
	for _, cmd := range h.a.commands() {
		if cmd.section != "Actions" {
			t.Fatalf("%q listed after >", cmd.label)
		}
	}
	h.tt.Type("new thread in mygo")
	h.tt.Key(0, ui.KeyEnter)
	if cur := h.a.current(); !cur.id().IsZero() || cur.View.Workspace != "/work/mygo" {
		t.Errorf("new thread %v in %q, want a draft in /work/mygo", cur.id(), cur.View.Workspace)
	}
}

// ⌘↩ in the palette is the palette's: it doesn't reach the approval
// behind it.
func TestPaletteKeepsCmdEnter(t *testing.T) {
	h, th := asking(t, runTests)
	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type("toggle terminal")
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.tt.Advance(time.Second)
	if r := lastRequest(th); r.State != api.RequestPending || len(th.Pending) != 0 {
		t.Errorf("⌘↩ in the palette answered the approval behind it: %v, %d pending", r.State, len(th.Pending))
	}
}

// Switch model opens the picker from a settings page too, over the
// chat's composer.
func TestPaletteSwitchModel(t *testing.T) {
	a := offline(t)
	tt := ui.NewTester(a.view, 1320, 860)
	a.router.Push("/settings/general")
	tt.Frame()
	a.openCommandPalette()
	tt.Frame()
	tt.Type("switch model")
	tt.Key(0, ui.KeyEnter)
	tt.Advance(time.Second)
	if a.router.Path() != "/" || !a.pickerOpen || !tt.HasText("OpenAI") {
		t.Errorf("path %q, picker %v", a.router.Path(), a.pickerOpen)
	}
}

// Two threads of one title are two rows of the palette.
func TestPaletteListsThreadsOfOneTitle(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "Fix the build")
	seed(t, d, "/work/mygo", "Fix the build")
	h := start(t, dir)
	h.thread("Fix the build")
	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type("fix the build")
	h.tt.Frame()
	n := 0
	for _, s := range h.tt.Texts() {
		if s == "Fix the build" {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("%q shows %d times, want twice in the palette and twice in the sidebar", "Fix the build", n)
	}
}
