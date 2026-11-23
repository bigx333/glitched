package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Extension is plugin.json's "glitched" block: what a plugin adds to
// Glitched beyond mygo-plugin's manifest. It has a block of its own so
// that mygo-plugin's fields, settings among them, keep their meaning.
type Extension struct {
	// Places are widgets the plugin puts in Glitched's own UI.
	Places []Place `json:"places,omitempty"`
	// Tabs are tabs of its own in a thread's side panel.
	Tabs []Tab `json:"tabs,omitempty"`
	// Timers wake the plugin now and then, every at least MinEvery.
	Timers []Timer `json:"timers,omitempty"`
	// Events are the app events the plugin hears.
	Events []string `json:"events,omitempty"`
	// Settings are drawn in Settings › Plugins.
	Settings []Setting `json:"settings,omitempty"`
	// Tools are tools Glitched's agents can call.
	Tools []Tool `json:"tools,omitempty"`
	// Programs are the programs it may run, each by name.
	Programs []string `json:"programs,omitempty"`
	// Service is its headless half's module, run by the engine.
	Service string `json:"service,omitempty"`
	// Shortcuts bind its commands to keys.
	Shortcuts []Shortcut `json:"shortcuts,omitempty"`
	// SettingsPages are pages of its own (contributes.pages) listed in
	// Settings' nav, rather than over Settings in the sidebar.
	SettingsPages []string `json:"settingsPages,omitempty"`
	// Themes are looks it adds to Appearance's: colors only.
	Themes []Theme `json:"themes,omitempty"`
	// Menus put its commands in Glitched's menus: a thread's, a
	// project's, a message's actions.
	Menus []Menu `json:"menus,omitempty"`
}

// Menu puts one of the plugin's commands (contributes.commands) in a menu
// of Glitched's, after its own items: At is one of MenuSlots. A message's
// actions are buttons, which show Icon, one of the app's icons.
type Menu struct {
	Command string `json:"command"`
	At      string `json:"at"`
	Icon    string `json:"icon,omitempty"`
}

// MenuSlots are the menus a plugin's commands may join, and what the
// command is passed when chosen there.
var MenuSlots = map[string]string{
	"thread.menu":     "{thread}",
	"project.menu":    "{project: {name, workspace, machine}}",
	"message.actions": "{thread, message: {role, text}}",
}

// Theme is a look a plugin adds to Appearance: its name, whether it is
// dark, a color for each of ThemeColors as #rrggbb or #rrggbbaa, and the
// Ghostty theme the terminal takes, if any.
type Theme struct {
	Name     string            `json:"name"`
	Dark     bool              `json:"dark,omitempty"`
	Colors   map[string]string `json:"colors"`
	Terminal string            `json:"terminal,omitempty"`
}

// ThemeColors are the colors a theme names, as the app's palette has them.
var ThemeColors = []string{"bg", "sidebar", "surface", "hover", "active", "border", "text", "muted", "faint", "green", "red", "amber", "blue"}

var (
	themeName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,31}$`)
	themeColor = regexp.MustCompile(`^#([0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
)

// Place is a widget a plugin puts at a slot of Glitched's UI, or, with
// Replaces, a component of Glitched's it draws in place of the built-in.
type Place struct {
	ID       string `json:"id"`
	At       string `json:"at"`
	Replaces bool   `json:"replaces,omitempty"`
}

// Tab is a tab in a thread's side panel.
type Tab struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`
}

// Timer wakes the plugin's app session, or its service, each Every
// seconds.
type Timer struct {
	ID    string `json:"id"`
	Every int    `json:"every"`
}

// Tool is a tool for agents: name, what it does, and a JSON Schema of its
// input.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Input       json.RawMessage `json:"input,omitempty"`
}

// Shortcut binds a command of the plugin's to a key.
type Shortcut struct {
	Command string `json:"command"`
	Key     string `json:"key"`
}

// Slot is a named place in Glitched's UI where plugins put widgets.
type Slot struct {
	Name string
	// Stable slots keep their name and context across Glitched's updates.
	Stable bool
	// Provides is what each place there is given as its context.
	Provides string
	About    string
}

// Slots are the places Glitched offers. describe_elements in Zeron's
// terms: docs/plugins.md lists them.
var Slots = []Slot{
	{"thread.header", true, "thread", "A thread's header, after its title."},
	{"thread.row", true, "thread", "Each row of the sidebar's thread list, after its title. Built once per row."},
	{"sidebar.footer", false, "", "The sidebar's foot, over Settings."},
}

// Events are the app events a plugin may hear, and the capability each
// needs.
var Events = map[string]string{
	"thread.changed":       "threads:read",
	"thread.moved":         "threads:read",
	"thread.message":       "messages:read",
	"thread.files.changed": "threads:read",
	"setting.changed":      "",
	"theme.changed":        "",
	"window.focused":       "",
	"place.shown":          "",
	"place.hidden":         "",
}

// MinEvery is the shortest a timer may be, in seconds.
const MinEvery = 30

var (
	toolName   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	localID    = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,63}$`)
	programRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]{0,63}$`)
)

// readExtension reads plugin.json's glitched block, and checks it. A
// plugin without one has none.
func readExtension(folder string) (Extension, error) {
	var raw struct {
		Glitched *Extension `json:"glitched"`
	}
	b, err := os.ReadFile(filepath.Join(folder, "plugin.json"))
	if err != nil {
		return Extension{}, err
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return Extension{}, err
	}
	if raw.Glitched == nil {
		return Extension{}, nil
	}
	x := *raw.Glitched
	return x, x.check(folder)
}

func (x Extension) check(folder string) error {
	ids := map[string]bool{}
	unique := func(kind, id string) error {
		if !localID.MatchString(id) {
			return fmt.Errorf("glitched.%s: id %q is 1 to 64 letters, digits or _.-", kind, id)
		}
		if ids[kind+":"+id] {
			return fmt.Errorf("glitched.%s: id %q twice", kind, id)
		}
		ids[kind+":"+id] = true
		return nil
	}
	for _, p := range x.Places {
		if err := unique("places", p.ID); err != nil {
			return err
		}
		if p.Replaces {
			if err := checkReplace(p); err != nil {
				return err
			}
			continue
		}
		if _, ok := component(p.At); ok {
			return fmt.Errorf("glitched.places: %s is a component, which a place takes over with \"replaces\": true", p.At)
		}
		if !slices.ContainsFunc(Slots, func(s Slot) bool { return s.Name == p.At }) {
			return fmt.Errorf("glitched.places: no slot %q", p.At)
		}
	}
	for _, t := range x.Tabs {
		if err := unique("tabs", t.ID); err != nil {
			return err
		}
		if t.Title == "" {
			return fmt.Errorf("glitched.tabs: %s has no title", t.ID)
		}
	}
	for _, t := range x.Timers {
		if err := unique("timers", t.ID); err != nil {
			return err
		}
		if t.Every < MinEvery {
			return fmt.Errorf("glitched.timers: %s runs every %d s, under the %d s allowed", t.ID, t.Every, MinEvery)
		}
	}
	for _, e := range x.Events {
		if _, ok := Events[e]; !ok {
			return fmt.Errorf("glitched.events: no event %q", e)
		}
	}
	for _, s := range x.Settings {
		if err := unique("settings", s.ID); err != nil {
			return err
		}
		if err := s.check(); err != nil {
			return fmt.Errorf("glitched.settings: %s: %w", s.ID, err)
		}
	}
	for _, t := range x.Tools {
		if !toolName.MatchString(t.Name) || strings.Contains(t.Name, "__") {
			return fmt.Errorf("glitched.tools: name %q is a lowercase letter, then up to 40 of a-z, 0-9 and _, without __", t.Name)
		}
		if err := unique("tools", t.Name); err != nil {
			return err
		}
		if t.Description == "" {
			return fmt.Errorf("glitched.tools: %s has no description", t.Name)
		}
		if len(t.Input) > 0 {
			var schema map[string]any
			if json.Unmarshal(t.Input, &schema) != nil || schema["type"] != "object" {
				return fmt.Errorf("glitched.tools: %s's input is a JSON Schema of type object", t.Name)
			}
		}
	}
	for _, p := range x.Programs {
		if !programRef.MatchString(p) {
			return fmt.Errorf("glitched.programs: %q is a program's name, never a path", p)
		}
	}
	if x.Service != "" {
		if strings.Contains(x.Service, "..") || filepath.IsAbs(x.Service) {
			return fmt.Errorf("glitched.service: %q is a file in the plugin's folder", x.Service)
		}
		if _, err := os.Stat(filepath.Join(folder, x.Service)); err != nil {
			return fmt.Errorf("glitched.service: %w", err)
		}
	}
	for _, s := range x.Shortcuts {
		if _, err := parseKey(s.Key); err != nil {
			return fmt.Errorf("glitched.shortcuts: %s: %w", s.Command, err)
		}
	}
	for _, m := range x.Menus {
		if _, ok := MenuSlots[m.At]; !ok {
			return fmt.Errorf("glitched.menus: no menu %q", m.At)
		}
		if (m.At == "message.actions") != (m.Icon != "") || len(m.Icon) > 64 {
			return fmt.Errorf("glitched.menus: %s: a message's actions show an icon, which no menu's item does", m.Command)
		}
		if ids["menus:"+m.At+":"+m.Command] {
			return fmt.Errorf("glitched.menus: %s twice in %s", m.Command, m.At)
		}
		ids["menus:"+m.At+":"+m.Command] = true
	}
	for _, t := range x.Themes {
		if !themeName.MatchString(t.Name) {
			return fmt.Errorf("glitched.themes: name %q is 1 to 32 letters, digits, spaces or ._-", t.Name)
		}
		if ids["themes:"+t.Name] {
			return fmt.Errorf("glitched.themes: %q twice", t.Name)
		}
		ids["themes:"+t.Name] = true
		for _, k := range ThemeColors {
			if !themeColor.MatchString(t.Colors[k]) {
				return fmt.Errorf("glitched.themes: %s: colors.%s is #rrggbb or #rrggbbaa, not %q", t.Name, k, t.Colors[k])
			}
		}
		if len(t.Colors) != len(ThemeColors) {
			return fmt.Errorf("glitched.themes: %s: colors are %s, no others", t.Name, strings.Join(ThemeColors, ", "))
		}
		if len(t.Terminal) > 64 {
			return fmt.Errorf("glitched.themes: %s: terminal is a Ghostty theme's name, up to 64 bytes", t.Name)
		}
	}
	return nil
}

func (x Extension) replaces() bool {
	return slices.ContainsFunc(x.Places, func(p Place) bool { return p.Replaces })
}

// needs are the capabilities the extension's parts need declared.
func (x Extension) needs() []string {
	var out []string
	add := func(c string) {
		if c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(x.Places) > 0 || len(x.Tabs) > 0 || len(x.Menus) > 0 {
		add("ui.place")
	}
	if x.replaces() {
		add("ui.replace")
	}
	for _, e := range x.Events {
		add(Events[e])
	}
	if len(x.Tools) > 0 {
		add("tools")
	}
	if len(x.Programs) > 0 {
		add("programs")
	}
	if x.Service != "" {
		add("service")
	}
	return out
}
