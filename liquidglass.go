package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// liquidGlassVersion is whether macOS's product version v, as "26.2", is
// one with Liquid Glass: 26 or later.
func liquidGlassVersion(v string) bool {
	major, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(major)
	return err == nil && major[0] != '+' && n >= 26
}

// themeSource is the appearance the theme called name has the app take:
// a theme of named's own appearance, or the system's for System and for a
// name no theme has any more.
func themeSource(name string, named []theme) mygo.ThemeSource {
	i := slices.IndexFunc(named, func(t theme) bool { return t.name == name })
	switch {
	case i < 0:
		return mygo.ThemeSystem
	case named[i].dark:
		return mygo.ThemeDark
	}
	return mygo.ThemeLight
}

// tintHex is c in the syntax of Window.SetVibrancyTint, "#rrggbbaa", or ""
// for no tint when c is clear. It is c's 8-bit sRGB channels: what a
// wide-gamut color has beyond them is dropped.
func tintHex(c ui.Color) string {
	if c.A == 0 {
		return ""
	}
	return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A)
}

// syncAppearance has the app take the theme's appearance, on Liquid Glass,
// so the system's glass is the theme's light or dark: asked only as it
// changes, as the system's answer draws another frame.
func (a *app) syncAppearance() {
	if !a.nativeGlass || a.setAppearance == nil {
		return
	}
	if s := themeSource(a.settings.theme, slices.Concat(themes, a.pluginThemes)); s != a.askedAppearance {
		a.askedAppearance = s
		a.setAppearance(s)
	}
}

// syncTint hands the Glass tint to the system's glass, on Liquid Glass,
// as it changes: none while the sidebar shows no vibrancy.
func (a *app) syncTint() {
	if !a.nativeGlass || a.setTint == nil {
		return
	}
	t := ""
	if a.vibrant {
		t = tintHex(a.glassTint())
	}
	if t != a.appliedTint {
		a.appliedTint = t
		a.setTint(t)
	}
}
