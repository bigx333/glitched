package main

import (
	"bytes"
	"slices"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Liquid Glass is macOS 26's and every later version's.
func TestLiquidGlassVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"26":     true,
		"26.0":   true,
		"26.2":   true,
		"27.0.1": true,
		"100.1":  true,
		"25.9":   false,
		"15.7.1": false,
		"2":      false,
		"":       false,
		"abc":    false,
		"2x.1":   false,
		".26":    false,
		"-26":    false,
	} {
		if got := liquidGlassVersion(v); got != want {
			t.Errorf("liquidGlassVersion(%q) = %v, want %v", v, got, want)
		}
	}
}

// The app's appearance is the chosen theme's, light or dark, or the
// system's for System and for a theme that is no longer there.
func TestThemeSource(t *testing.T) {
	named := slices.Concat(themes, []theme{{name: "Dracula", dark: true}, {name: "Paper", dark: false}})
	for name, want := range map[string]mygo.ThemeSource{
		systemTheme: mygo.ThemeSystem,
		"Dark":      mygo.ThemeDark,
		"Light":     mygo.ThemeLight,
		"Dracula":   mygo.ThemeDark,
		"Paper":     mygo.ThemeLight,
		"Gone":      mygo.ThemeSystem,
		"":          mygo.ThemeSystem,
	} {
		if got := themeSource(name, named); got != want {
			t.Errorf("themeSource(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestTintHex(t *testing.T) {
	for _, c := range []struct {
		c    ui.Color
		want string
	}{
		{ui.Color{}, ""},
		{ui.Color{R: 12, G: 12, B: 14}, ""},
		{ui.Color{R: 12, G: 12, B: 14, A: 77}, "#0c0c0e4d"},
		{ui.Color{R: 255, G: 255, B: 255, A: 77}, "#ffffff4d"},
		{ui.Color{R: 0, G: 171, B: 205, A: 255}, "#00abcdff"},
	} {
		if got := tintHex(c.c); got != c.want {
			t.Errorf("tintHex(%v) = %q, want %q", c.c, got, c.want)
		}
	}
}

// glassSystem plays the system for a's appearance seam: it records each
// appearance the app asks for and, after the frame that asked, switches
// the tester to it, as macOS does (System is the tester's own, light).
type glassSystem struct {
	t       *testing.T
	tt      *ui.Tester
	calls   []mygo.ThemeSource
	pending *mygo.ThemeSource
}

func newGlassSystem(t *testing.T, a *app, tt *ui.Tester) *glassSystem {
	s := &glassSystem{t: t, tt: tt}
	a.setAppearance = func(src mygo.ThemeSource) {
		s.calls = append(s.calls, src)
		s.pending = &src
	}
	return s
}

// frame draws a frame, then lets the system switch the appearance asked
// for; the tester settles that switch with frames of its own. A switch
// that asks for another without end fails, as the app would never rest.
func (s *glassSystem) frame() {
	s.t.Helper()
	s.tt.Frame()
	for n := 0; s.pending != nil; n++ {
		if n == 10 {
			s.t.Fatalf("each switch of the appearance asks for another: %v", s.calls[len(s.calls)-10:])
		}
		src := *s.pending
		s.pending = nil
		s.tt.SetDark(src == mygo.ThemeDark)
	}
}

// On Liquid Glass, the app's appearance follows the theme, asked of the
// system only as the theme changes: asked every frame, the system's
// answer would draw another frame, without end.
func TestAppearanceFollowsThemeOnlyOnChange(t *testing.T) {
	a := offline(t)
	a.nativeGlass = true
	tt := ui.NewTester(a.view, 1000, 600)
	sys := newGlassSystem(t, a, tt)
	tt.SetDark(false)
	sys.frame()
	if len(sys.calls) != 0 {
		t.Fatalf("System asked for an appearance: %v", sys.calls)
	}
	for _, name := range []string{"Dark", "Dark", "Light", systemTheme} {
		a.settings.theme = name
		sys.frame()
		sys.frame()
	}
	want := []mygo.ThemeSource{mygo.ThemeDark, mygo.ThemeLight, mygo.ThemeSystem}
	if !slices.Equal(sys.calls, want) {
		t.Fatalf("appearances asked: %v, want %v", sys.calls, want)
	}
	for range 5 {
		sys.frame()
	}
	if !slices.Equal(sys.calls, want) {
		t.Fatalf("frames go on asking: %v", sys.calls)
	}

	// Without Liquid Glass, the appearance stays the system's.
	b := offline(t)
	bt := ui.NewTester(b.view, 1000, 600)
	bs := newGlassSystem(t, b, bt)
	for _, name := range []string{"Dark", "Light", systemTheme} {
		b.settings.theme = name
		bs.frame()
	}
	if len(bs.calls) != 0 {
		t.Fatalf("asked for an appearance without Liquid Glass: %v", bs.calls)
	}
}

// On Liquid Glass, the Glass tint goes to the system's glass, sent as it
// changes: the theme's background, by as much as the setting leaves, or
// none when the glass is off or clear.
func TestTintFollowsGlass(t *testing.T) {
	a := offline(t)
	a.nativeGlass = true
	a.settings.theme, a.settings.glass, a.settings.vibrancy = "Dark", true, 0.7
	var tints []string
	a.setTint = func(s string) { tints = append(tints, s) }
	tt := ui.NewTester(a.view, 1000, 600)
	sys := newGlassSystem(t, a, tt)
	sys.frame()
	if len(tints) != 0 {
		t.Fatalf("tinted with no glass to show: %q", tints)
	}
	tt.SetVibrancy(true)
	sys.frame()
	// 1 - 0.7 of 255 is 76.5, which rounds to 77, 0x4d; the dark
	// background is #0c0c0e, the light #ffffff.
	want := []string{"#0c0c0e4d"}
	check := func(why string) {
		t.Helper()
		if !slices.Equal(tints, want) {
			t.Fatalf("%s: tints sent %q, want %q", why, tints, want)
		}
	}
	check("vibrancy on")
	for range 3 {
		sys.frame()
	}
	check("frames on")

	a.settings.glass = false
	sys.frame()
	want = append(want, "")
	check("glass off")

	a.settings.glass = true
	sys.frame()
	want = append(want, "#0c0c0e4d")
	check("glass on")

	a.settings.vibrancy = 1
	sys.frame()
	want = append(want, "")
	check("clear")

	a.settings.vibrancy = 0.7
	a.settings.theme = "Light"
	sys.frame()
	sys.frame()
	want = append(want, "#ffffff4d")
	check("light")

	a.settings.theme = "Dark"
	sys.frame()
	sys.frame()
	want = append(want, "#0c0c0e4d")
	check("dark")
}

// On Liquid Glass, the bars are clear, the system's glass carrying the
// tint: the window draws the same whatever the tint. Over the older
// material, the bars carry it themselves.
func TestChromeClearOnNativeGlass(t *testing.T) {
	shot := func(native bool, tint float64) []byte {
		a := offline(t)
		a.nativeGlass = native
		a.settings.theme, a.settings.glass, a.settings.vibrancy = "Light", true, tint
		tt := ui.NewTester(a.view, 1000, 600)
		tt.SetDark(false)
		tt.SetVibrancy(true)
		if !a.vibrant {
			t.Fatal("the window shows no vibrancy")
		}
		return tt.Image().Pix
	}
	if !bytes.Equal(shot(true, 0.7), shot(true, 1)) {
		t.Fatal("the bars are tinted over Liquid Glass")
	}
	if bytes.Equal(shot(false, 0.7), shot(false, 1)) {
		t.Fatal("the bars are not tinted over the older material")
	}
}

// A theme settings.json chose that wasn't known as the settings loaded, as
// a plugin's, is taken by readThemes as soon as it is; one still unknown
// waits. (The window's start calls readThemes too, in main, which no test
// reaches.)
func TestPendingThemeTakenOnceKnown(t *testing.T) {
	a := offline(t)
	a.pendingTheme = "Dark"
	a.readThemes()
	if a.settings.theme != "Dark" || a.pendingTheme != "" {
		t.Fatalf("theme %q, pending %q; want Dark, none", a.settings.theme, a.pendingTheme)
	}
	a.pendingTheme = "Dracula"
	a.readThemes()
	if a.settings.theme != "Dark" || a.pendingTheme != "Dracula" {
		t.Fatalf("theme %q, pending %q; want Dark, Dracula", a.settings.theme, a.pendingTheme)
	}
}
