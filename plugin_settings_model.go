package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
)

// What Settings › Plugins says of a plugin, worked out apart from how it
// is drawn.

// tone is how a state reads, each graver than the one before: nothing to
// say, well, news, waiting on the user, wrong.
type tone uint8

const (
	toneQuiet tone = iota
	toneGood
	toneNews
	toneWarn
	toneBad
)

func (t tone) color() ui.Color {
	return [...]ui.Color{palette.muted, palette.green, palette.blue, palette.amber, palette.red}[t]
}

// partTitles name Glitched's components as the user reads them.
var partTitles = map[string]string{
	"thread.header.actions":     "Thread header buttons",
	"settings.page.general":     "Settings › General",
	"settings.page.appearance":  "Settings › Appearance",
	"settings.page.about":       "Settings › About",
	"settings.page.keybindings": "Settings › Keybindings",
	"thread.empty":              "Empty thread",
	"hero.heading":              "New thread greeting",
	"sidebar.projects":          "Project strip",
	"thread.row.actions":        "Thread row actions",
	"thread.panel.diff":         "Diff panel",
	"thread.panel.subagent":     "Subagent panel",
	"thread.panel.terminal":     "Terminal panel",
	"thread.panel.preview":      "Preview panel",
	"selection.actions":         "Selection actions",
	"sidebar.footer.updates":    "CLI updates",
	"settings.machine":          "Settings › Machine",
	"approval.card":             "Approval card",
	"question.card":             "Question card",
	"credentials.card":          "Credentials card",
	"composer":                  "Composer",
	"timeline.message":          "Timeline rows",
	"timeline.report":           "Report card",
	"message.toolbar":           "Message toolbar",
	"composer.queue":            "Queue",
	"thread.header.breadcrumb":  "Breadcrumb",
	"thread.header.usage":       "Token usage",
	"thread.header.plan":        "Plan",
	"palette":                   "Command palette",
	"sidebar.body":              "Sidebar threads",
	"agents.body":               "Agents panel",
	"composer.access":           "Access picker",
	"composer.model":            "Model picker",
	"composer.send":             "Send and stop",
	"composer.workspace":        "Workspace",
	"settings.plugins":          "Settings › Plugins",
	"settings.servers":          "Settings › Servers",
	"settings.providers":        "Settings › Providers",
	"confirm.destructive":       "Delete confirmation",
}

func partTitle(name string) string { return cmpOr(partTitles[name], name) }

// capRisks say what the weightier grants risk: those that let a plugin
// act as the user, send what it reads away, or draw what the user takes
// for Glitched's own.
var capRisks = map[string]string{
	"programs":       "They run as you, with your files and your accounts.",
	plugins.Network:  "It can send what it reads to these hosts.",
	"threads:write":  "Agents it starts work with the access you gave them.",
	"settings:write": "It can change how Glitched looks and behaves, without asking again.",
	"ui.replace":     "What you see in those parts is the plugin's, not Glitched's.",
	"commands":       "It does what choosing them in the palette does, unasked: it can start threads, switch the theme and open your settings.",
	"threads:access": "It draws the control that sets what agents may do, and can give them Full access to your computer without the hold Glitched asks for.",
	"models:ask":     "What it asks goes to the model's provider, on your account's usage.",
}

// capRisk is what granting the capability risks, "" for a lighter grant.
// One this version of Glitched doesn't know is taken for the worst.
func capRisk(name string) string {
	if r, ok := capRisks[name]; ok {
		return r
	}
	if !slices.ContainsFunc(plugins.Capabilities, func(c plugins.Capability) bool { return c.Name == name }) {
		return "This version of Glitched doesn't know what it lets a plugin do."
	}
	return ""
}

// grantListed is whether Settings lists the grant cp of p. A built-in
// Glitched's release key signed draws Glitched's own parts, so its
// ui.replace isn't listed with a warning that they're the plugin's while
// it holds; a copy, or a built-in that fails its signature, lists it.
func grantListed(p plugins.Plugin, cp plugins.CapabilityState) bool {
	return cp.Grant && !(cp.Name == "ui.replace" && cp.Granted && p.Trust == plugins.TrustBuiltin)
}

// pluginStanding is how the plugin stands, in the word or two its row
// shows: whether it runs, and what it waits for.
func (a *app) pluginStanding(p plugins.Plugin) (string, tone) {
	switch {
	case p.ShadowedBy != "" && !a.copyRuns(p):
		return "Draws for your copy", toneQuiet
	case p.ShadowedBy != "":
		return "Your copy runs instead", toneQuiet
	case p.Trust == plugins.TrustWaiting:
		return "Waiting for your OK", toneWarn
	case p.Trust == plugins.TrustUnsigned:
		return "Not trusted yet", toneWarn
	case p.Trust == plugins.TrustTampered:
		return "Changed since signed", toneBad
	case !p.Enabled:
		return "Off", toneQuiet
	case p.Err != nil:
		return "Didn't load", toneBad
	case a.plugins.Failed(p.ID) != nil:
		return "Failed", toneBad
	}
	// Not a thing to do: the grant it lacks is one the user may well
	// mean to withhold.
	for _, name := range p.Replaces {
		if a.partState(p, name).word == "Not granted" {
			return "Not granted to draw", toneQuiet
		}
	}
	for _, f := range a.pluginUpdates {
		if f.Err == nil && f.ID == p.ID {
			return "Update to " + f.Version, toneNews
		}
	}
	return "On", toneGood
}

// pluginRuns is whether the plugin runs: on, and nothing stops it. Its
// switch is lit by it, so the switch and the pill say the same.
func (a *app) pluginRuns(p plugins.Plugin) bool {
	_, t := a.pluginStanding(p)
	return p.Enabled && p.ShadowedBy == "" && t != toneWarn && t != toneBad
}

// copyRuns is whether the user's copy of the shadowed built-in p runs.
// While it doesn't, the built-in draws in its place.
func (a *app) copyRuns(p plugins.Plugin) bool {
	for _, q := range a.plugins.Installed() {
		if q.ShadowedBy == "" && q.ID == strings.TrimSuffix(p.ID, plugins.FallbackSuffix) {
			return a.pluginRuns(q)
		}
	}
	return false
}

// rowName is the plugin's name to assistive tools: a built-in under a
// copy has the copy's name, so it says which it is.
func rowName(p plugins.Plugin) string {
	if p.ShadowedBy != "" {
		return p.Name + ", built-in"
	}
	return p.Name
}

// partState is whether a plugin draws a component it replaces: a word for
// it, and why, with who draws it when the plugin doesn't.
type partState struct {
	word string
	tone tone
	why  string
}

func (a *app) partState(p plugins.Plugin, name string) partState {
	var comp plugins.Component
	for _, c := range plugins.Components {
		if c.Name == name {
			comp = c
		}
	}
	// Who draws it when this one doesn't: the built-in, another plugin
	// of the user's ahead of it, or nothing.
	instead := "."
	switch drawer, drawn := a.partDrawer(name); {
	case !drawn && (comp.Native || comp.Own):
		instead = ", so Glitched draws its own."
	case !drawn:
		instead = ", and nothing draws it."
	case drawer.Folder == p.Folder:
	case drawer.Builtin:
		instead = ", so the built-in draws it."
	default:
		instead = ", so " + drawer.Name + " draws it."
	}
	switch {
	case !p.Enabled:
		return partState{"Off", toneQuiet, "This plugin is off" + instead}
	case p.Err != nil:
		return partState{"Not drawing", toneBad, "It isn't running" + instead}
	case !a.plugins.Allowed(p.ID, "ui.replace"):
		return partState{"Not granted", toneQuiet, "It isn't granted ui.replace" + instead}
	case comp.Needs != "" && !a.plugins.Allowed(p.ID, comp.Needs):
		return partState{"Not granted", toneQuiet, "It isn't granted " + comp.Needs + ", which this part's context needs" + instead}
	case a.plugins.Failed(p.ID) != nil:
		return partState{"Failed", toneBad, "It failed" + strings.TrimSuffix(instead, ".") + ": " + a.plugins.Failed(p.ID).Error()}
	}
	nulls := a.nullCopies(p.ID, name)
	shown := len(a.copiesShown(name))
	if len(nulls) > 0 && len(nulls) == shown {
		return partState{"Answered null", toneQuiet, "It answered null for every copy shown" + a.drawnInstead(nulls, "it")}
	}
	if drawer, drawn := a.partDrawer(name); drawn && drawer.Folder != p.Folder {
		return partState{"Standing by", toneQuiet, drawer.Name + " draws it in place of this one."}
	}
	if drew := slices.ContainsFunc(a.copiesShown(name), func(cp copyDrawn) bool { return cp.plugin == p.ID }); shown > 0 && !drew {
		why := "It hasn't drawn the copies shown yet"
		if len(nulls) > 0 {
			why = fmt.Sprintf("It answered null for %d of the %d copies shown, and hasn't drawn the others yet", len(nulls), shown)
		}
		return partState{"Not drawn yet", toneQuiet, why + a.drawnInstead(a.copiesShown(name), "it")}
	}
	if len(nulls) > 0 {
		return partState{"Drawing some", toneGood, fmt.Sprintf("It answered null for %d of the %d copies shown%s", len(nulls), shown, a.drawnInstead(nulls, "those"))}
	}
	return partState{"Drawing it now", toneGood, cmpOr(comp.About, "It draws it now.")}
}

// copiesShown are who drew the copies of the component name the app
// shows, as the last frame to draw the thread shown, or the app's own
// parts, drew them: none when that frame didn't draw it, or when what
// decides who draws it changed since: which plugins replace it, in what
// order, their sessions, or whether they failed.
func (a *app) copiesShown(name string) []copyDrawn {
	d := a.partsDrawn[name]
	if d == nil || d.frame != a.scopeFrames[scopeOf(d)] || d.thread != nil && *d.thread != a.current().id() {
		return nil
	}
	rs := a.plugins.Replacers(name)
	if len(rs) != len(d.replacers) {
		return nil
	}
	for i, r := range d.replacers {
		if rs[i].Plugin != r.id || a.plugins.Live(r.id) != r.session || (a.plugins.Failed(r.id) != nil) != r.failed {
			return nil
		}
	}
	return d.copies
}

// nullCopies are the copies shown of the component name that the plugin
// id answered null for.
func (a *app) nullCopies(id, name string) []copyDrawn {
	var out []copyDrawn
	for _, cp := range a.copiesShown(name) {
		if slices.Contains(cp.nulls, id) {
			out = append(out, cp)
		}
	}
	return out
}

// drawnInstead says who draws the copies a plugin answered null for:
// the plugins and Glitched that do, and whether some have nothing. it is
// what to call them.
func (a *app) drawnInstead(copies []copyDrawn, it string) string {
	list := a.plugins.Installed()
	var who []string
	glitched, none := false, false
	for _, cp := range copies {
		switch {
		case cp.glitched:
			glitched = true
		case cp.plugin == "":
			none = true
		default:
			name := a.plugins.Name(cp.plugin)
			if i := slices.IndexFunc(list, func(p plugins.Plugin) bool { return p.ID == cp.plugin }); i >= 0 && list[i].Builtin {
				name = "the built-in"
			}
			if !slices.Contains(who, name) {
				who = append(who, name)
			}
		}
	}
	switch {
	case len(who) == 0 && !glitched:
		return ", and nothing draws " + it + "."
	case len(who) == 0 && !none && it == "it":
		return ", so Glitched draws its own."
	case len(who) == 0 && !none:
		return ", so Glitched draws its own for " + it + "."
	case len(who) == 1 && !glitched && !none:
		return ", so " + who[0] + " draws " + it + "."
	}
	if glitched {
		who = append(who, "Glitched")
	}
	if none {
		return ", so " + andList(who) + " draw some, and nothing draws the rest."
	}
	return ", so " + andList(who) + " draw them."
}

// andList joins words as a sentence does: a, b and c.
func andList(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// partDrawer is the plugin that draws the component now, if one does.
// As the copies shown were drawn, it is the first that replaces it that
// drew one; else the first that replaces it whose last drawing went well,
// as pluginComponent picks it, else the first, whose last good drawing
// stays; none of a native component's failed ones, as Glitched draws its
// own instead.
func (a *app) partDrawer(name string) (plugins.Plugin, bool) {
	rs := a.plugins.Replacers(name)
	if len(rs) == 0 {
		return plugins.Plugin{}, false
	}
	id := ""
	if shown := a.copiesShown(name); len(shown) > 0 {
		for _, r := range rs {
			if slices.ContainsFunc(shown, func(cp copyDrawn) bool { return cp.plugin == r.Plugin }) {
				id = r.Plugin
				break
			}
		}
	} else {
		if comp := plugins.ComponentNamed(name); !comp.Native && !comp.Own {
			id = rs[0].Plugin
		}
		for _, r := range rs {
			if a.plugins.Failed(r.Plugin) == nil {
				id = r.Plugin
				break
			}
		}
	}
	list := a.plugins.Installed()
	i := slices.IndexFunc(list, func(p plugins.Plugin) bool { return p.ID == id })
	if i < 0 {
		return plugins.Plugin{}, false
	}
	return list[i], true
}

// pluginGroup is the plugins of one origin, under its title; empty is
// what it says when it has none, "" for a group left out then.
type pluginGroup struct {
	title, empty string
	list         []plugins.Plugin
}

// groupPlugins sorts the plugins into those the user added and those
// built in, which no switch, grant or failure changes: a row never moves
// under the pointer. Whose an added one is isn't known while it's off,
// so its row says it, not its group.
func groupPlugins(list []plugins.Plugin) []pluginGroup {
	gs := []pluginGroup{
		{title: "ADDED BY YOU", empty: "Nothing added yet. Make a copy of a built-in to change a part of Glitched, or install a plugin below."},
		{title: "BUILT INTO GLITCHED"},
	}
	for _, p := range list {
		gs[btoi(p.Builtin)].list = append(gs[btoi(p.Builtin)].list, p)
	}
	return gs
}

// originTag marks an added plugin's row with whose it is, "" for one of
// the user's own or one not yet known.
func originTag(p plugins.Plugin) string {
	switch {
	case p.Shadows:
		return "Your copy"
	case p.Trust == plugins.TrustConsented || p.Trust == plugins.TrustWaiting || p.Trust == plugins.TrustTampered:
		return "Someone else's"
	}
	return ""
}

// matchesPlugin is whether the filter's words are all in what the plugin
// is, draws or may do.
func matchesPlugin(p plugins.Plugin, query string) bool {
	hay := []string{p.Name, p.ID, p.Description}
	for _, name := range p.Replaces {
		hay = append(hay, name, partTitle(name))
	}
	for _, cp := range p.Capabilities {
		hay = append(hay, cp.Name, cp.About)
	}
	all := strings.ToLower(strings.Join(hay, "\n"))
	for _, w := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(all, w) {
			return false
		}
	}
	return true
}

// pluginSummary is a line of what the plugin does to the app: the parts
// it draws and how many grants it asks for, or what it says of itself.
func pluginSummary(p plugins.Plugin) string {
	var out []string
	switch n := len(p.Replaces); {
	case n > 2:
		out = append(out, fmt.Sprintf("Draws %d parts of Glitched", n))
	case n > 0:
		names := make([]string, n)
		for i, name := range p.Replaces {
			names[i] = partTitle(name)
		}
		out = append(out, "Draws "+strings.Join(names, ", "))
	}
	asked, given := 0, 0
	for _, cp := range p.Capabilities {
		if grantListed(p, cp) {
			asked++
			given += btoi(cp.Granted)
		}
	}
	if asked > 0 {
		out = append(out, fmt.Sprintf("granted %d of %d", given, asked))
	}
	if len(out) == 0 {
		return cmpOr(p.Description, "Needs no grant")
	}
	s := strings.Join(out, " · ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// trustWords say whose the plugin is, and which key says so.
func trustWords(p plugins.Plugin) (who, how string) {
	switch {
	case p.Trust == plugins.TrustBuiltin:
		return "Built into Glitched", "Signed by Glitched's release key."
	case p.Trust == plugins.TrustOwn && p.Shadows:
		return "Yours, in place of the built-in", "Signed by this device's key or one you trust: " + p.Signer + "."
	case p.Trust == plugins.TrustOwn:
		return "Yours", "Signed by this device's key or one you trust: " + p.Signer + "."
	case p.Trust == plugins.TrustConsented:
		return "Someone else's, allowed", "Signed by key " + p.Signer + "."
	case p.Trust == plugins.TrustWaiting:
		return "Someone else's", "Signed by key " + p.Signer + "."
	case p.Trust == plugins.TrustTampered:
		return "Changed since it was signed", "Its files no longer match its signature."
	case p.Trust == plugins.TrustUnsigned:
		return "Not signed", "No key says whose it is."
	}
	return "", ""
}
