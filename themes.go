package main

import (
	"math"
	"slices"

	"github.com/egoist/mygo/ui"
)

type colors struct {
	bg, sidebar, surface, hover, active, border, text, muted, faint ui.Color
	accent, green, red, amber, blue                                 ui.Color
}

// theme is one of the app's looks: its colors, and the terminal's, a
// theme of Ghostty's where the theme has a matching one.
type theme struct {
	name   string
	dark   bool
	colors colors
	term   string
}

// The two looks the system's appearance picks between.
var (
	lightTheme = theme{"Light", false, lightColors, ""}
	darkTheme  = theme{"Dark", true, darkColors, ""}
)

// themes are the looks the settings choose from, after System.
var themes = []theme{lightTheme, darkTheme}

// systemTheme is the choice that follows the system's appearance.
const systemTheme = "System"

// themeNames are the settings' choices: the system's appearance, then
// each theme.
func (a *app) themeNames() []string {
	names := []string{systemTheme}
	for _, t := range slices.Concat(themes, a.pluginThemes) {
		names = append(names, t.name)
	}
	return names
}

// readPluginThemes reads the running plugins' themes: one whose name the
// app or an earlier plugin took is left out.
func (a *app) readPluginThemes() []theme {
	var out []theme
	for _, t := range a.plugins.Themes() {
		if slices.ContainsFunc(slices.Concat(themes, out), func(th theme) bool { return th.name == t.Name }) || t.Name == systemTheme {
			continue
		}
		hex := func(k string) ui.Color { return ui.Hex(t.Colors[k]) }
		out = append(out, theme{t.Name, t.Dark, colors{
			bg: hex("bg"), sidebar: hex("sidebar"), surface: hex("surface"), hover: hex("hover"), active: hex("active"),
			border: hex("border"), text: hex("text"), muted: hex("muted"), faint: hex("faint"),
			green: hex("green"), red: hex("red"), amber: hex("amber"), blue: hex("blue"),
		}, t.Terminal})
	}
	return out
}

// forgetPluginTerminals drops the terminal colors read for plugins'
// themes, which a plugin's change may have changed.
func (a *app) forgetPluginTerminals() {
	for _, t := range a.pluginThemes {
		delete(terminalThemes, t.name)
	}
}

// themeFor is the theme a settings choice names; System, or a name
// that is no theme's, is the system's appearance, Dark or Light.
func (a *app) themeFor(name string, systemDark bool) theme {
	for _, t := range slices.Concat(themes, a.pluginThemes) {
		if t.name == name {
			return t
		}
	}
	if systemDark {
		return darkTheme
	}
	return lightTheme
}

var (
	darkColors = colors{
		bg:      ui.Hex("#0c0c0e"),
		sidebar: ui.Hex("#111113"),
		surface: ui.Hex("#18181b"),
		hover:   ui.RGBA(255, 255, 255, 0.05),
		active:  ui.RGBA(255, 255, 255, 0.09),
		border:  ui.RGBA(255, 255, 255, 0.08),
		text:    ui.Hex("#ececf0"),
		muted:   ui.Hex("#8e8e98"),
		faint:   ui.Hex("#85858d"),
		green:   ui.Hex("#4ade80"),
		red:     ui.Hex("#f87171"),
		amber:   ui.Hex("#fbbf24"),
		blue:    ui.Hex("#60a5fa"),
	}
	lightColors = colors{
		bg:      ui.Hex("#ffffff"),
		sidebar: ui.Hex("#f6f6f7"),
		surface: ui.Hex("#ffffff"),
		hover:   ui.RGBA(0, 0, 0, 0.04),
		active:  ui.RGBA(0, 0, 0, 0.07),
		border:  ui.RGBA(0, 0, 0, 0.09),
		text:    ui.Hex("#18181b"),
		muted:   ui.Hex("#64646d"),
		faint:   ui.Hex("#6f6f78"),
		green:   ui.Hex("#16a34a"),
		red:     ui.Hex("#dc2626"),
		amber:   ui.Hex("#d97706"),
		blue:    ui.Hex("#2563eb"),
	}
	accents = []struct {
		name  string
		color ui.Color
	}{
		{"Violet", ui.Hex("#8b7cf6")},
		{"Blue", ui.Hex("#3b82f6")},
		{"Teal", ui.Hex("#14b8a6")},
		{"Orange", ui.Hex("#f97316")},
		{"Pink", ui.Hex("#ec4899")},
	}
	// palette is the colors of the frame being built, set from the
	// settings at the start of each.
	palette = darkColors
)

// readThemes reads the plugins' themes, and takes the theme settings.json
// chose once it is one of them.
func (a *app) readThemes() {
	a.pluginThemes = a.readPluginThemes()
	if a.pendingTheme != "" && slices.Contains(a.themeNames(), a.pendingTheme) {
		a.settings.theme, a.pendingTheme = a.pendingTheme, ""
	}
}

// applyTheme sets the theme the settings ask for: the system's appearance,
// which the default choice follows, or a theme of the app's or a plugin's.
func (a *app) applyTheme(c *ui.Context) {
	a.readThemes()
	a.theme = a.themeFor(a.settings.theme, c.Theme().Dark)
	a.syncAppearance()
	palette = a.theme.colors
	palette.accent = accents[a.settings.accent].color

	t := *c.Theme()
	t.Dark = a.theme.dark
	t.Background = palette.bg
	t.Surface = palette.surface
	t.SurfaceHover = palette.surface.Mix(palette.text, 0.06)
	t.SurfacePressed = palette.surface.Mix(palette.text, 0.1)
	t.Border = palette.border
	t.Text = palette.text
	t.TextMuted = palette.muted
	t.Accent = palette.accent
	t.AccentHover = palette.accent.Mix(ui.Hex("#ffffff"), 0.12)
	t.AccentPressed = palette.accent.Mix(ui.Hex("#000000"), 0.12)
	t.AccentText = ui.Hex("#ffffff")
	t.Selection = palette.accent.Alpha(0.35)
	t.Focus = palette.accent.Alpha(0.6)
	t.Scrollbar = palette.text.Alpha(0.18)
	t.Inverse, t.InverseText = palette.surface.Mix(palette.text, 0.1), palette.text
	t.Radius = 8
	t.FontSize = float32(a.settings.fontSize)
	c.SetTheme(&t)
	if a.plugins != nil && (a.pluginDark == nil || *a.pluginDark != t.Dark) {
		dark, first := t.Dark, a.pluginDark == nil
		a.pluginDark = &dark
		if !first {
			appearance := map[bool]string{true: "dark", false: "light"}[dark]
			a.plugins.Emit("theme.changed", func(string) any { return map[string]string{"appearance": appearance} })
		}
	}
}

// contrast is the WCAG contrast ratio of two colors.
func contrast(a, b ui.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(c ui.Color) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// toggleLightDark switches to Light from any dark theme, and to Dark from
// any light one.
func (a *app) toggleLightDark() {
	name := lightTheme.name
	if !a.theme.dark {
		name = darkTheme.name
	}
	a.chooseTheme(name)
}

// chooseTheme is the user's choice of a theme. It drops a plugin's theme
// settings.json chose that the app still waits for, and saves the choice
// even as the theme that stood in for it.
func (a *app) chooseTheme(name string) {
	if a.pendingTheme != "" {
		a.savedSettings.Theme, a.pendingTheme = a.pendingTheme, ""
	}
	a.settings.theme = name
}
