package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// A theme chosen in Appearance colors the app and the terminal drawer
// alike, and ⇧⌘L flips between Light and Dark from any theme.
func TestThemeColorsTheTerminal(t *testing.T) {
	a := offline(t)
	tt := ui.NewTester(a.view, 1320, 860)
	a.router.Push("/settings/appearance")
	tt.Frame()
	if err := tt.Click("Mocha"); err != nil {
		t.Fatal(err)
	}
	a.router.Push("/")
	a.showTerminal = true
	tt.Frame()
	r, ok := tt.Find("✓ No type errors")
	if !ok {
		t.Fatalf("no terminal output; texts %q", tt.Texts())
	}
	// The drawer's padding, left of its text.
	got := tt.Image().RGBAAt(int(r.X)-4, int(r.Y+r.H/2))
	want := ui.Hex("#1e1e2e") // Catppuccin Mocha's terminal background
	if got.R != want.R || got.G != want.G || got.B != want.B {
		t.Errorf("drawer is %v under Mocha, want %v", got, want)
	}

	tt.Key(ui.Cmd|ui.Shift, ui.KeyL)
	tt.Frame()
	if a.settings.theme != "Light" {
		t.Errorf("⇧⌘L from Mocha chose %q, want Light", a.settings.theme)
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyL)
	tt.Frame()
	if a.settings.theme != "Dark" {
		t.Errorf("⇧⌘L from Light chose %q, want Dark", a.settings.theme)
	}
}

// Every theme's muted and faint text reads at 4.5:1 or better on the
// chat's background and the sidebar's.
func TestTextContrast(t *testing.T) {
	for _, th := range themes {
		for _, bg := range []struct {
			name  string
			color ui.Color
		}{{"background", th.colors.bg}, {"sidebar", th.colors.sidebar}} {
			for _, fg := range []struct {
				name  string
				color ui.Color
			}{{"muted", th.colors.muted}, {"faint", th.colors.faint}} {
				if r := contrast(fg.color, bg.color); r < 4.5 {
					t.Errorf("%s: %s text on the %s is %.2f:1", th.name, fg.name, bg.name, r)
				}
			}
		}
	}
}
