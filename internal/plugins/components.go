package plugins

import (
	"fmt"
	"slices"

	"glitched/internal/plugins/widget"
)

// Component is a named part of Glitched's UI that one plugin draws: a
// place of a plugin's with replaces set takes it over from the built-in.
type Component struct {
	Name string
	// Provides is what each copy is given as its context.
	Provides string
	// Needs is a capability a replacement must also be granted, as its
	// context holds what only it may read.
	Needs string
	About string
	// Locked is why the component stays Glitched's own, "" when a plugin
	// may replace it: widget.Locked, which also keeps it from being placed
	// as a native widget.
	Locked string
}

// Components are the parts of Glitched's UI a plugin may replace, and the
// ones it never may. docs/plugins.md lists them.
var Components = []Component{
	{Name: "thread.header.actions", Provides: "headerActions", About: "The buttons at the right of a thread's header: panel toggles, Open in, Commit & push."},
	{Name: "settings.page.general", Provides: "generalPage", Needs: "settings:write", About: "The General page of Settings: defaults for new threads and how the app behaves."},
	{Name: "settings.page.appearance", Provides: "appearancePage", Needs: "settings:write", About: "The theme and accent of Settings' Appearance page; the material below them stays Glitched's."},
	{Name: "settings.page.about", Provides: "aboutPage", About: "The About page of Settings: the version, the update check and the source."},
	{Name: "settings.page.keybindings", Provides: "keybindingsPage", About: "The Keybindings page of Settings: the app's shortcuts, with a search."},
	{Name: "thread.empty", Provides: "threadEmpty", About: "What a thread with no messages yet shows, when it isn't a new thread's hero."},
	{Name: "hero.heading", Provides: "", About: "The greeting over a new thread's composer. The hero fades it as the composer docks."},
	{Name: "sidebar.projects", Provides: "projectStrip", Needs: "threads:read", About: "The sidebar's project strip: All, a chip per project that filters the inbox to it, and Add project."},
	{Name: "thread.row.actions", Provides: "rowActions", Needs: "threads:write", About: "A sidebar row's actions, shown while it's pointed at: snooze, with its popover of times, settle, or back to the inbox."},
	{Name: "thread.panel.diff", Provides: "diffPanel", Needs: "threads:read", About: "The diff panel: its header, and its list of changed files over the native diff.body."},
	{Name: "approval.card", Locked: widget.Locked["approval.card"]},
	{Name: "question.card", Locked: widget.Locked["question.card"]},
	{Name: "composer.access", Locked: widget.Locked["composer.access"]},
	{Name: "composer.send", Locked: widget.Locked["composer.send"]},
	{Name: "settings.plugins", Locked: widget.Locked["settings.plugins"]},
	{Name: "settings.servers", Locked: widget.Locked["settings.servers"]},
	{Name: "confirm.destructive", Locked: widget.Locked["confirm.destructive"]},
}

// component is the component named name, if Glitched has one.
func component(name string) (Component, bool) {
	i := slices.IndexFunc(Components, func(c Component) bool { return c.Name == name })
	if i < 0 {
		return Component{}, false
	}
	return Components[i], true
}

// checkReplace says why the place p may not replace the component it names.
func checkReplace(p Place) error {
	c, ok := component(p.At)
	switch {
	case !ok:
		return fmt.Errorf("glitched.places: %s replaces %q, which is no component", p.ID, p.At)
	case c.Locked != "":
		return fmt.Errorf("glitched.places: %s replaces %s, which is off limits to plugins: %s", p.ID, p.At, c.Locked)
	}
	return nil
}

// Replacers are the running plugins that draw the component, best first:
// your plugins before the built-in ones, each in folder order, a built-in
// that your copy shadows included. Only a plugin allowed ui.replace, and
// what the component needs, is one.
func (h *Host) Replacers(name string) []PlaceAt {
	c, _ := component(name)
	var out []PlaceAt
	for _, in := range h.list() {
		id := in.manifest.ID
		if in.p == nil || !h.Allowed(id, "ui.replace") || c.Needs != "" && !h.Allowed(id, c.Needs) {
			continue
		}
		for _, p := range in.ext.Places {
			if p.Replaces && p.At == name {
				out = append(out, PlaceAt{in.manifest.ID, p})
			}
		}
	}
	return out
}

// Failed is what last went wrong in the plugin id's app session, which
// draws its places and components: nil when it went well, or when the
// session hasn't started.
func (h *Host) Failed(id string) error {
	if s := h.sessions[pageKey{plugin: id}]; s != nil {
		return s.Err
	}
	return nil
}
