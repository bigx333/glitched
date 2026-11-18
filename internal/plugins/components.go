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
	About    string
	// Locked is why the component stays Glitched's own, "" when a plugin
	// may replace it: widget.Locked, which also keeps it from being placed
	// as a native widget.
	Locked string
}

// Components are the parts of Glitched's UI a plugin may replace, and the
// ones it never may. docs/plugins.md lists them.
var Components = []Component{
	{Name: "thread.header.actions", Provides: "headerActions", About: "The buttons at the right of a thread's header: panel toggles, Open in, Commit & push."},
	{Name: "thread.panel.diff", Provides: "diffPanel", About: "The diff panel: its header, and its list of changed files over the native diff.body."},
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
// that your copy shadows included. Only a plugin allowed ui.replace is one.
func (h *Host) Replacers(name string) []PlaceAt {
	var out []PlaceAt
	for _, in := range h.list() {
		if in.p == nil || !h.Allowed(in.manifest.ID, "ui.replace") {
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
