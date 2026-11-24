package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/provider"
)

func visibleLabel(tt *ui.Tester, label string) bool {
	r, ok := tt.Find(label)
	return ok && r.W > 0 && r.H > 0
}

// At 760 points, with the sidebar hidden by the user and the diff open,
// one Show sidebar, by its button, its shortcut or the palette, shows it
// in the diff's place.
func TestShowSidebarAtANarrowWindowTakesOneAction(t *testing.T) {
	for _, via := range []string{"button", "shortcut", "palette"} {
		t.Run(via, func(t *testing.T) {
			a, tt, _ := browsing(t)
			tt.SetSize(760, 860)
			tt.Key(ui.Cmd, ui.KeyB)
			tt.Key(ui.Cmd, ui.KeyD)
			tt.Advance(time.Second)
			if a.sidebarOpen != 0 || a.showSidebar || !a.showDiff || !visibleLabel(tt, "Show sidebar  ⌘B") {
				t.Fatal("want a manually hidden sidebar with a visible diff and Show sidebar button")
			}
			switch via {
			case "button":
				if err := tt.Click("Show sidebar  ⌘B"); err != nil {
					t.Fatal(err)
				}
			case "shortcut":
				tt.Key(ui.Cmd, ui.KeyB)
			case "palette":
				tt.Key(ui.Cmd, ui.KeyK)
				settleTester(tt)
				tt.Type("Toggle sidebar")
				click(t, tt, "Toggle sidebar")
			}
			tt.Advance(time.Second)
			if a.sidebarOpen != 1 {
				t.Errorf("one %s action leaves sidebar at %g (preference=%t, diff=%t); Show sidebar still visible=%t", via, a.sidebarOpen, a.showSidebar, a.showDiff, visibleLabel(tt, "Show sidebar  ⌘B"))
				tt.Key(ui.Cmd, ui.KeyB)
				tt.Advance(time.Second)
				t.Logf("one more shortcut leaves sidebar at %g (diff=%t)", a.sidebarOpen, a.showDiff)
			}
		})
	}
}

// Hide sidebar, clicked as the sidebar slides away for a panel, hides it
// and leaves the panel open.
func TestHideSidebarAsItGivesWayHidesIt(t *testing.T) {
	a, tt, _ := browsing(t)
	tt.SetSize(760, 860)
	tt.SetTitleBar(ui.TitleBar{Height: 52, Left: 78})
	tt.Advance(time.Second)
	tt.Key(ui.Cmd, ui.KeyD)
	if !visibleLabel(tt, "Hide sidebar  ⌘B") {
		t.Fatal("could not exercise the Hide sidebar while the slide starts")
	}
	t.Logf("after the diff opens, sidebar animation is %g; both controls are visible=%t", a.sidebarOpen, visibleLabel(tt, "Show sidebar  ⌘B"))
	if err := tt.Click("Hide sidebar  ⌘B"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if a.sidebarOpen != 0 || !a.showDiff {
		t.Errorf("Hide sidebar leaves sidebar at %g (preference=%t, diff=%t)", a.sidebarOpen, a.showSidebar, a.showDiff)
	}
}

// Dragging the preview wider never moves its edge away from the pointer,
// though the sidebar gave way to it.
func TestTheFirstPreviewDragKeepsItsEdgeAtThePointer(t *testing.T) {
	a, tt, _ := browsing(t)
	tt.SetSize(1070, 860)
	tt.Key(ui.Cmd|ui.Shift, ui.KeyP)
	tt.Advance(time.Second)
	r, ok := tt.Find("Resize preview")
	if !ok {
		t.Fatal("no preview edge")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	before := a.previewWidth()
	t.Logf("before drag, sidebar=%g; preview=%g; edge=%g", a.sidebarOpen, before, x)
	tt.Press(x, y)
	tt.Move(x-30, y)
	tt.Advance(time.Second)
	after, ok := tt.Find("Resize preview")
	at := after.X + after.W/2
	t.Logf("after left drag, sidebar=%g; preview=%g; edge=%g; pointer=%g; preference=%g", a.sidebarOpen, a.previewWidth(), at, x-30, a.previewDragged)
	if !ok || at > x+1 {
		t.Errorf("dragging the preview wider pushed its edge right from %g to %g", x, at)
	}
	tt.Release(x-30, y)
}

// The panel the chat shows decides whether the sidebar gives way, not one
// it covers.
func TestACoveredPluginTabLeavesThePreviewsFit(t *testing.T) {
	for _, dragged := range []float32{0, 400} {
		name := "default"
		if dragged != 0 {
			name = "dragged"
		}
		t.Run(name, func(t *testing.T) {
			a, tt, _ := browsing(t)
			w := 1085
			if dragged > 0 {
				w = 1065
			}
			tt.SetSize(w, 860)
			a.previewDragged = dragged
			a.openPluginTab(pluginTab{"example", "panel"}, true)
			tt.Frame()
			tt.Key(ui.Cmd|ui.Shift, ui.KeyP)
			tt.Advance(time.Second)
			if !visibleLabel(tt, "Resize preview") || !a.showPluginTab {
				t.Fatal("want a visible preview covering the plugin tab")
			}
			coveredOpen, coveredWidth := a.sidebarOpen, a.previewWidth()
			a.openPluginTab(a.pluginTabShown, false)
			tt.Advance(time.Second)
			t.Logf("%dpt window with covered plugin tab: sidebar=%g, preview=%g; without tab: sidebar=%g, preview=%g", w, coveredOpen, coveredWidth, a.sidebarOpen, a.previewWidth())
			if coveredOpen != a.sidebarOpen || coveredWidth != a.previewWidth() {
				t.Errorf("hidden plugin tab changed the visible preview's fit")
			}
		})
	}
}

// A sidebar dragged wider than the panel's fit allows comes back as the
// drag turns round.
func TestTheSidebarDragsBackAcrossThePanelsFit(t *testing.T) {
	a, tt, _ := browsing(t)
	tt.SetSize(1100, 860)
	tt.Key(ui.Cmd, ui.KeyD)
	tt.Advance(time.Second)
	r, ok := tt.Find("Resize sidebar")
	if !ok {
		t.Fatal("no sidebar edge")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x+60, y)
	tt.Advance(time.Second)
	w, slide := a.sidebarWidth, a.sidebarOpen
	tt.Move(x-20, y)
	tt.Advance(time.Second)
	end := a.sidebarWidth
	t.Logf("dragging right: width=%g open=%g; dragging back left without release: width=%g open=%g", w, slide, end, a.sidebarOpen)
	if a.sidebarOpen != 1 || end >= w {
		t.Errorf("could not drag sidebar back into view without releasing the mouse")
	}
	tt.Release(x-20, y)
}

// The sidebar's shortcut on the way to Settings toggles the sidebar there
// and leaves the chat's panel alone.
func TestTheSidebarShortcutOnTheWayToSettingsKeepsTheChatsPanel(t *testing.T) {
	a, tt, _ := browsing(t)
	tt.SetSize(760, 860)
	tt.Key(ui.Cmd, ui.KeyD)
	tt.Advance(time.Second)
	a.router.Push("/settings/general")
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Advance(time.Second)
	if a.router.Path() != "/settings/general" || !tt.HasText("Settings") {
		t.Fatal("want the settings page")
	}
	if !a.showDiff {
		t.Errorf("the sidebar shortcut at the start of the settings frame closed the chat's diff (sidebar preference=%t, sidebar=%g)", a.showSidebar, a.sidebarOpen)
	}
}

// The sidebar gives way only on the chat's page, and only to a panel
// beside it, not to the terminal.
func TestTheSidebarComesBackOnOtherPages(t *testing.T) {
	a, tt, _ := browsing(t)
	tt.SetSize(760, 860)
	tt.Key(ui.Cmd, ui.KeyJ)
	tt.Advance(time.Second)
	if a.sidebarOpen != 1 || !visibleLabel(tt, "Close terminal") {
		t.Fatal("the terminal alone displaced the sidebar")
	}
	tt.Key(ui.Cmd, ui.KeyD)
	tt.Advance(time.Second)
	if a.sidebarOpen != 0 || !visibleLabel(tt, "Close terminal") {
		t.Fatal("want the terminal with the diff and sidebar folded")
	}
	for _, path := range []string{"/settings/general", "/plugins/example/home"} {
		a.router.Push(path)
		tt.Advance(time.Second)
		if a.sidebarOpen != 1 || !a.showSidebar || !a.showDiff || visibleLabel(tt, "Close terminal") {
			t.Fatalf("%s changed layout state: sidebar=%g, preference=%t, diff=%t, terminal visible=%t", path, a.sidebarOpen, a.showSidebar, a.showDiff, visibleLabel(tt, "Close terminal"))
		}
		a.router.Push("/")
		tt.Advance(time.Second)
		if a.sidebarOpen != 0 || !visibleLabel(tt, "Close terminal") {
			t.Fatalf("return from %s: sidebar=%g, diff=%t, terminal visible=%t", path, a.sidebarOpen, a.showDiff, visibleLabel(tt, "Close terminal"))
		}
	}
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Advance(time.Second)
	if a.sidebarOpen != 1 || a.showDiff || !visibleLabel(tt, "Close terminal") {
		t.Fatal("showing the sidebar did not close only the visible diff")
	}
}

// Resized across the panel's fit, the sidebar slides one way each time,
// never under the window's buttons.
func TestTheSidebarCrossesThePanelsFitWithoutReversing(t *testing.T) {
	a, tt, _ := browsing(t)
	tt.SetSize(1080, 860)
	tt.SetTitleBar(ui.TitleBar{Height: 52, Left: 78})
	tt.Key(ui.Cmd, ui.KeyD)
	tt.Advance(time.Second)
	threshold := int(a.sidebarWidth + 420 + 380)
	for _, step := range []struct {
		width int
		want  float32
	}{{threshold, 1}, {threshold - 1, 0}, {threshold + 1, 1}, {threshold - 1, 0}} {
		tt.SetSize(step.width, 860)
		prev := a.sidebarOpen
		for range 24 {
			if r, ok := tt.Find("Hide sidebar  ⌘B"); ok && r.W > 0 && r.X < 78 {
				t.Errorf("at %dpt Hide sidebar under traffic lights: %+v", step.width, r)
			}
			if r, ok := tt.Find("Show sidebar  ⌘B"); ok && r.W > 0 && r.X < 78 {
				t.Errorf("at %dpt Show sidebar under traffic lights: %+v", step.width, r)
			}
			tt.Advance(16 * time.Millisecond)
			next := a.sidebarOpen
			if step.want == 0 && next > prev+0.001 || step.want == 1 && next < prev-0.001 {
				t.Fatalf("at %dpt sidebar reversed from %g to %g, target %g", step.width, prev, next, step.want)
			}
			prev = next
		}
		if a.sidebarOpen != step.want || !a.showDiff || !a.showSidebar {
			t.Errorf("at %dpt sidebar=%g want %g, stored preference=%t, diff=%t", step.width, a.sidebarOpen, step.want, a.showSidebar, a.showDiff)
		}
	}
}

// A new thread, which shows no panel, takes the sidebar back; the thread
// with the subagent's panel gives it way again.
func TestANewThreadTakesBackTheSidebarFromASubagentPanel(t *testing.T) {
	h := subagentThreads(t, []provider.Event{spawnStep("alpha"), nestedText("alpha", "Narrow probe subagent text.")}, "Parent")
	th := h.a.current()
	h.tt.SetSize(760, 860)
	openSubagentOf(t, h, "Agent alpha")
	if h.a.sidebarOpen != 0 || !h.tt.HasText("Narrow probe subagent text.") {
		t.Fatal("want the parent with the sidebar folded and its subagent open")
	}
	h.tt.Key(ui.Cmd, ui.KeyN)
	h.tt.Advance(time.Second)
	if !h.a.current().id().IsZero() || h.a.sidebarOpen != 1 || h.tt.HasText("Narrow probe subagent text.") {
		t.Fatalf("new-thread page kept the prior side panel or lost the sidebar: sidebar=%g, selection=%s", h.a.sidebarOpen, h.a.current().id())
	}
	h.a.select_(th)
	h.tt.Advance(time.Second)
	if h.a.sidebarOpen != 0 || !h.a.panels().Subagent {
		t.Fatalf("going back to the parent did not restore its subagent panel: sidebar=%g, subagent=%t", h.a.sidebarOpen, h.a.panels().Subagent)
	}
}

// The header fits the chat as the sidebar gives way to a panel and as the
// window is resized.
func TestTheHeaderFitsAsTheSidebarGivesWay(t *testing.T) {
	const title = "Review header with a long title"
	h := narrowHeader(t, title, false, nil)
	h.a.toggleSidebar(false)
	h.tt.Advance(time.Second)
	controls := append([]string{title, "Pull request #12", "Hide sidebar  ⌘B"}, headerControls...)
	for _, w := range []int{1320, 760, 1071, 1072, 1320} {
		h.tt.SetSize(w, 860)
		if !h.a.showDiff {
			h.tt.Key(ui.Cmd, ui.KeyD)
		}
		for range 24 {
			var visible []string
			for _, s := range controls {
				if _, ok := h.inHeader(s); ok {
					visible = append(visible, s)
				}
			}
			for _, problem := range h.misfits(w, visible...) {
				t.Error(problem)
			}
			h.p.run()
			h.tt.Advance(16 * time.Millisecond)
		}
		if want := w < 1072; visibleLabel(h.tt, "Show sidebar  ⌘B") != want {
			t.Errorf("at %dpt Show sidebar visibility, want %t", w, want)
		}
		if !near(h.a.chatWidth, h.a.chatRoom()) {
			t.Logf("at %dpt chat bounds=%g, header budget=%g, panel budget=%g, room=%g", w, h.a.chatWidth, h.a.chatRoom(), h.a.diffWidth(), h.a.roomBeside)
		}
	}
}
