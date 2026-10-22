package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// The palette lists threads by recency, opens one with its diff on ⌘↩,
// keeps to actions after >, and starts a thread in a chosen project.
func TestPaletteCommands(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	a.openCommandPalette()
	tt.Frame()
	var threads []string
	for _, cmd := range a.commands() {
		if cmd.t != nil {
			threads = append(threads, cmd.t.age)
		}
	}
	if len(threads) < 3 || threads[0] != "now" || threads[1] != "12m" || threads[2] != "2h" {
		t.Errorf("threads by age %q", threads)
	}
	tt.Type("websocket")
	tt.Key(ui.Cmd, ui.KeyEnter)
	if a.cmdkOpen || a.selected != "t4" || !a.showDiff {
		t.Errorf("⌘↩: open %v, selected %q, diff %v", a.cmdkOpen, a.selected, a.showDiff)
	}
	// ⌘↩ in the palette is the palette's: it doesn't reach the approval
	// behind it.
	a.selected = "t2"
	a.openCommandPalette()
	tt.Frame()
	tt.Type("websocket")
	tt.Key(ui.Cmd, ui.KeyEnter)
	if _, th := a.current(); th.id != "t4" || a.projects[0].threads[1].pending() == nil {
		t.Errorf("⌘↩ in the palette reached the approval behind it")
	}

	a.openCommandPalette()
	tt.Frame()
	tt.Type(">")
	for _, cmd := range a.commands() {
		if cmd.section != "Actions" {
			t.Fatalf("%q listed after >", cmd.label)
		}
	}
	tt.Type("new thread in mygo")
	tt.Key(0, ui.KeyEnter)
	p, th := a.current()
	if p.name != "mygo" || th.title != "New thread" {
		t.Errorf("new thread in %q titled %q", p.name, th.title)
	}
}

// Switch model opens the picker from a settings page too, over the
// chat's composer.
func TestPaletteSwitchModel(t *testing.T) {
	a := newApp()
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
