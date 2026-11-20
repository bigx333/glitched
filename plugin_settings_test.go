package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
	"glitched/internal/plugins/pack"
)

// Settings › Plugins, on the plugins pluginShotState installs: built-ins,
// a copy of one, and plugins that wait for a grant, for consent, for
// trust, and that didn't load.
func TestThePluginsPage(t *testing.T) {
	a := offline(t)
	pluginShotState(t, a, "mixed")
	a.router.Push("/settings/plugins")
	tt := newTester(t, a, 1320, 6000)
	settle := func() {
		settleTester(tt)
		tt.Advance(300 * time.Millisecond)
	}
	settle()
	folder := func(name string) string { return filepath.Join(plugins.Dir(a.dir), name) }
	y := func(s string) float32 {
		t.Helper()
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("no %q in %q", s, tt.Texts())
		}
		return r.Y
	}
	click := func(s string) {
		t.Helper()
		if err := tt.Click(s); err != nil {
			t.Fatal(err)
		}
		settle()
	}
	disclosure := func(name string) ui.AccessInfo {
		t.Helper()
		d, ok := tt.AccessByRole(ui.RoleDisclosure, name)
		if !ok {
			t.Fatalf("no row for %s", name)
		}
		return d
	}

	t.Run("groups plugins by whose they are", func(t *testing.T) {
		at := []float32{y("ADDED BY YOU  7"), disclosure("Linear sync").Bounds.Y, disclosure("Scratchpad").Bounds.Y, y("BUILT INTO GLITCHED  8"), disclosure("Diff panel").Bounds.Y, y("ADD AND UPDATE")}
		if !slices.IsSorted(at) {
			t.Errorf("what you added, then the built-ins, then installing: %v", at)
		}
		if !tt.HasText("Someone else's") || !tt.HasText("Your copy") {
			t.Errorf("no word of whose Linear sync and the copy of Greetings are: %q", tt.Texts())
		}
		if !tt.HasText("3 to look at") {
			t.Errorf("no count of the three that wait or failed: %q", tt.Texts())
		}
	})

	t.Run("opens what waits on you, and nothing else", func(t *testing.T) {
		const captain = "Release captain for the whole monorepo and its satellites"
		if d := disclosure(captain); d.Expanded || !strings.HasPrefix(d.Description, "Not granted to draw. ") {
			t.Errorf("a plugin that lacks a grant reads as something to do: %+v", d)
		}
		if _, ok := tt.Access("Remove Scratchpad"); ok {
			t.Error("an unsigned plugin can be removed, which only Trust as mine was offered for")
		}
		for name, open := range map[string]bool{"Linear sync": true, "Scratchpad": true, "Broken loader": true, "Focus timer": false, "Diff panel": false} {
			if got := disclosure(name).Expanded; got != open {
				t.Errorf("%s open: %v, want %v", name, got, open)
			}
		}
		for _, want := range []string{"Linear sync is someone else's. It asks to:", "• Reach api.linear.app", "Trust as mine", "It didn't load", "approval.card, which is off limits to plugins"} {
			if !tt.HasText(want) {
				t.Errorf("no %q", want)
			}
		}
		if tt.HasText("Show the timer") {
			t.Error("Focus timer's settings show before its row is opened")
		}
	})

	t.Run("says how each stands", func(t *testing.T) {
		for name, want := range map[string]string{"Linear sync": "Waiting for your OK", "Scratchpad": "Not trusted yet", "Broken loader": "Didn't load", "Focus timer": "On"} {
			if d := disclosure(name).Description; !strings.HasPrefix(d, want+". ") {
				t.Errorf("%s reads %q, want %q", name, d, want)
			}
		}
		builtin := slices.IndexFunc(a.plugins.Installed(), func(p plugins.Plugin) bool { return p.ShadowedBy != "" })
		if s, _ := a.pluginStanding(a.plugins.Installed()[builtin]); s != "Your copy runs instead" {
			t.Errorf("the built-in a copy replaces reads %q", s)
		}
	})

	t.Run("a row opens and closes from the keyboard", func(t *testing.T) {
		for i := 0; i < 200 && !tt.Focused("Focus timer"); i++ {
			tt.Key(0, ui.KeyTab)
		}
		if !tt.Focused("Focus timer") {
			t.Fatal("Tab doesn't reach Focus timer's row")
		}
		tt.Key(0, ui.KeyEnter)
		settle()
		if !disclosure("Focus timer").Expanded || !tt.HasText("Show the timer") {
			t.Fatalf("Enter didn't open it: %q", tt.Texts())
		}
		tt.Key(0, ui.KeySpace)
		settle()
		if disclosure("Focus timer").Expanded || tt.HasText("Show the timer") {
			t.Fatal("Space didn't close it")
		}
		if !tt.Focused("Focus timer") {
			t.Error("its row lost the focus")
		}
	})

	t.Run("a grant holds, and its row stays where it was", func(t *testing.T) {
		const name = "Release captain for the whole monorepo and its satellites"
		click(name)
		was := disclosure(name).Bounds.Y
		if !tt.HasText("They run as you, with your files and your accounts.") || !tt.HasText("git: "+a.plugins.Installed()[slices.IndexFunc(a.plugins.Installed(), func(p plugins.Plugin) bool { return p.ID == "release-captain" })].Programs[0].Risk) {
			t.Errorf("the programs grant doesn't say what it risks: %q", tt.Texts())
		}
		if a.plugins.Allowed("release-captain", "ui.replace") {
			t.Fatal("granted before the switch")
		}
		click("Grant ui.replace to " + name)
		if !a.plugins.Allowed("release-captain", "ui.replace") {
			t.Fatal("the switch didn't grant ui.replace")
		}
		if s, _ := a.pluginStanding(a.plugins.Installed()[slices.IndexFunc(a.plugins.Installed(), func(p plugins.Plugin) bool { return p.ID == "release-captain" })]); s != "On" {
			t.Errorf("once granted it reads %q", s)
		}
		if !disclosure(name).Expanded || disclosure(name).Bounds.Y != was {
			t.Error("the grant that settled it closed or moved its row")
		}
		click("Grant ui.replace to " + name)
		if a.plugins.Allowed("release-captain", "ui.replace") {
			t.Fatal("the switch didn't take the grant back")
		}
	})

	t.Run("its switch turns a plugin off and on", func(t *testing.T) {
		click("Enable Focus timer")
		if installed(a, "focus-timer").Enabled {
			t.Fatal("still on")
		}
		if d := disclosure("Focus timer").Description; !strings.HasPrefix(d, "Off. ") {
			t.Errorf("off, it reads %q", d)
		}
		click("Enable Focus timer")
		if !installed(a, "focus-timer").Enabled {
			t.Fatal("still off")
		}
	})

	t.Run("the parts of Glitched say who draws each", func(t *testing.T) {
		click("Parts of Glitched")
		for _, c := range plugins.Components {
			if !tt.HasText(partTitle(c.Name)) {
				t.Errorf("no %s (%s)", partTitle(c.Name), c.Name)
			}
		}
		if !tt.HasText("An approval answers for you, so only Glitched draws it.") {
			t.Error("the locked parts don't say why")
		}
		if !tt.HasText(a.plugins.Name("release-captain") + ": It isn't granted ui.replace, so the built-in draws it.") {
			t.Errorf("no word of why yours doesn't draw the header's buttons: %q", tt.Texts())
		}
		if _, ok := tt.Access("Show Header actions, which draws Thread header buttons"); !ok {
			t.Error("the built-in isn't named as drawing the header's buttons")
		}
		click("Show Greetings, which draws New thread greeting")
		if a.pluginsView != 0 || !a.pluginsOpen[folder("greetings")] {
			t.Fatal("it didn't show your copy of Greetings in the list")
		}
		if !tt.HasText("Yours, in place of the built-in") {
			t.Errorf("your copy's row isn't open: %q", tt.Texts())
		}
	})

	t.Run("a long list filters", func(t *testing.T) {
		search, ok := tt.Access("Filter plugins")
		if !ok {
			t.Fatal("no filter over thirteen plugins")
		}
		tt.ClickAt(search.Bounds.X+search.Bounds.W/2, search.Bounds.Y+search.Bounds.H/2)
		tt.Type("linear network")
		settle()
		if _, ok := tt.AccessByRole(ui.RoleDisclosure, "Focus timer"); ok {
			t.Error("the filter kept Focus timer")
		}
		disclosure("Linear sync")
		tt.Type(" zzz")
		settle()
		if !tt.HasText("No plugin matches “linear network zzz”.") {
			t.Errorf("no word that nothing matches: %q", tt.Texts())
		}
		a.pluginsQuery = ""
		settle()
	})

	t.Run("a copy reverts from its row, and from the built-in's", func(t *testing.T) {
		click("Revert to built-in, Greetings")
		if p := installed(a, "greetings"); p.ID != "" {
			t.Fatalf("your copy still runs: %+v", p)
		}
		click("Diff panel")
		click("Make a copy of Diff panel")
		if p := installed(a, "diff-panel"); !p.Shadows || !a.pluginsOpen[p.Folder] {
			t.Fatalf("no copy of Diff panel, open in the list: %+v", p)
		}
		if got := disclosure("Diff panel").Description; !strings.HasPrefix(got, "On. ") {
			t.Errorf("the copy reads %q", got)
		}
		// The built-in's row says the copy runs, and reverts to itself:
		// with the copy's row closed, the one Revert is the built-in's.
		a.pluginsOpen[installed(a, "diff-panel").Folder] = false
		settle()
		if !tt.HasText("Your copy runs in its place") {
			t.Fatalf("the built-in doesn't say your copy runs: %q", tt.Texts())
		}
		if tt.HasText("Yours, in place of the built-in") {
			t.Fatal("the copy's row is still open")
		}
		click("Revert to the built-in Diff panel")
		if p := installed(a, "diff-panel"); p.ID != "" {
			t.Fatalf("the copy still runs: %+v", p)
		}
	})

	t.Run("every control has a name of its own", func(t *testing.T) {
		for _, view := range []int{0, 1} {
			a.pluginsView = view
			for _, p := range a.plugins.Installed() {
				a.pluginsOpen[p.Folder] = true
			}
			settle()
			seen := map[string]bool{}
			for _, n := range tt.AccessAll() {
				if !slices.Contains([]ui.Role{ui.RoleButton, ui.RoleSwitch, ui.RoleDisclosure, ui.RoleTextField, ui.RoleSearch}, n.Role) {
					continue
				}
				// MyGo's NumberInput names its row and not the field in
				// it, here Focus timer's Length.
				if n.Name == "" && !(n.Role == ui.RoleTextField && n.Step != 0) {
					t.Errorf("a control with no name at %v", n.Bounds)
				}
				// The rows of a built-in and its copy share a name, as
				// they share a plugin; the rest are told apart by name.
				if n.Role == ui.RoleSwitch || strings.HasPrefix(n.Name, "Grant ") || strings.HasPrefix(n.Name, "Remove ") || strings.HasPrefix(n.Name, "Make a copy ") {
					if seen[n.Name] {
						t.Errorf("two controls named %q", n.Name)
					}
					seen[n.Name] = true
				}
			}
		}
		a.pluginsView = 0
	})

	t.Run("a narrow window keeps every control in it", func(t *testing.T) {
		for _, p := range a.plugins.Installed() {
			a.pluginsOpen[p.Folder] = p.ID == "release-captain" || p.ID == "focus-timer" || p.ID == "linear-sync"
		}
		tt.SetSize(760, 6000)
		settle()
		for _, n := range tt.AccessAll() {
			if (n.Role == ui.RoleSwitch || n.Role == ui.RoleButton) && n.Bounds.W > 0 && n.Bounds.X+n.Bounds.W > 760 {
				t.Errorf("%q runs off the window: %v", n.Name, n.Bounds)
			}
		}
		for _, s := range []string{"Install a plugin", "Updates", "Plugins folder"} {
			if r, _ := tt.Find(s); r.W < 100 {
				t.Errorf("%q is squeezed to %v wide", s, r.W)
			}
		}
		tt.SetSize(1320, 6000)
		settle()
	})
}

// What the page says of states the host is rarely in: a plugin that
// failed as it drew, one with an update, one changed since it was signed,
// and a built-in that draws for a copy turned off.
func TestThePluginsPageSaysWhoDrawsAndWhatFailed(t *testing.T) {
	a := offline(t)
	pluginShotState(t, a, "mixed")
	// The app's own window first: Thrower fails as it draws the strip.
	tt := newTester(t, a, 1320, 6000)
	settleTester(tt)
	a.router.Push("/settings/plugins")
	settleTester(tt)
	tt.Advance(300 * time.Millisecond)
	thrower := installed(a, "thrower")
	if s, _ := a.pluginStanding(thrower); s != "Failed" {
		t.Fatalf("Thrower reads %q", s)
	}
	if !tt.HasText("It failed while it ran") || !tt.HasText("Copy error") {
		t.Errorf("no notice of the failure, open from the start: %q", tt.Texts())
	}
	if d, ok := a.partDrawer("sidebar.projects"); !ok || !d.Builtin {
		t.Errorf("with Thrower failed, %q draws the project strip, not the built-in", d.Name)
	}
	if why := a.partState(thrower, "sidebar.projects").why; !strings.HasPrefix(why, "It failed, so the built-in draws it: ") {
		t.Errorf("of the strip, Thrower's row says %q", why)
	}

	// A switch is lit only beside a pill that says its plugin runs.
	lit := func(name string) bool {
		t.Helper()
		sw, ok := tt.Access("Enable " + name)
		if !ok {
			t.Fatalf("no switch for %s", name)
		}
		return sw.Checked
	}
	for name, want := range map[string]bool{
		"Broken loader": false, // Didn't load
		"Thrower":       false, // Failed
		"Linear sync":   false, // Waiting for your OK
		"Scratchpad":    false, // Not trusted yet
		"Focus timer":   true,  // On
		"Release captain for the whole monorepo and its satellites": true, // Not granted to draw, and running
	} {
		if lit(name) != want {
			t.Errorf("the switch of %s is lit: %v, want %v", name, !want, want)
		}
	}
	// Turned on while something stops it, a plugin stays the user's to
	// allow or mend: the click never turns it off.
	for name, id := range map[string]string{"Broken loader": "broken-loader", "Linear sync": "linear-sync", "Scratchpad": "scratchpad"} {
		tt.Click("Enable " + name)
		settleTester(tt)
		if !installed(a, id).Enabled || lit(name) {
			t.Errorf("a click on the unlit switch of %s: enabled %v, lit %v", name, installed(a, id).Enabled, lit(name))
		}
	}
	for _, label := range []string{"Allow Linear sync", "Trust as mine, Scratchpad", "Copy error of Broken loader"} {
		if _, ok := tt.Access(label); !ok {
			t.Errorf("after the click, no %q", label)
		}
	}
	// Its notice turns it off, and the switch turns it on again.
	tt.Click("Turn off Broken loader")
	settleTester(tt)
	if p := installed(a, "broken-loader"); p.Enabled || lit("Broken loader") {
		t.Errorf("Turn off left it enabled: %v", p.Enabled)
	} else if s, _ := a.pluginStanding(p); s != "Off" {
		t.Errorf("turned off, it reads %q", s)
	}
	if _, ok := tt.Access("Turn off Broken loader"); ok {
		t.Error("Turn off stays on a plugin that is off")
	}
	tt.Click("Enable Broken loader")
	settleTester(tt)
	if p := installed(a, "broken-loader"); !p.Enabled || lit("Broken loader") {
		t.Errorf("turned on again: enabled %v, lit %v", p.Enabled, lit("Broken loader"))
	} else if s, _ := a.pluginStanding(p); s != "Didn't load" {
		t.Errorf("turned on again, it reads %q", s)
	}
	tt.Click("Enable Focus timer")
	settleTester(tt)
	if p := installed(a, "focus-timer"); p.Enabled || lit("Focus timer") {
		t.Errorf("Focus timer turned off: enabled %v, lit %v", p.Enabled, lit("Focus timer"))
	}
	tt.Click("Enable Focus timer")
	settleTester(tt)
	if !lit("Focus timer") {
		t.Error("Focus timer turned on again, and its switch is not lit")
	}

	// A copy turned off: the built-in it shadows draws, under its own id.
	if err := a.plugins.SetEnabled("greetings", false); err != nil {
		t.Fatal(err)
	}
	a.pluginsChanged(nil)
	settleTester(tt)
	if d, ok := a.partDrawer("hero.heading"); !ok || !d.Builtin {
		t.Errorf("with the copy off, %q draws the greeting (found: %v)", d.Name, ok)
	}
	shadowed := slices.IndexFunc(a.plugins.Installed(), func(p plugins.Plugin) bool { return p.ShadowedBy != "" })
	if s, _ := a.pluginStanding(a.plugins.Installed()[shadowed]); s != "Draws for your copy" {
		t.Errorf("with the copy off, the built-in reads %q", s)
	}
	if !tt.HasText("Draws for your copy") {
		t.Error("the built-in's row doesn't say it draws")
	}
	if why := a.partState(installed(a, "greetings"), "hero.heading").why; why != "This plugin is off, so the built-in draws it." {
		t.Errorf("the copy's row says %q", why)
	}

	a.pluginUpdates = []pack.Found{{Result: pack.Result{ID: "focus-timer", Version: "9.9.9"}}}
	if s, tone := a.pluginStanding(installed(a, "focus-timer")); s != "Update to 9.9.9" || tone != toneNews {
		t.Errorf("with an update found, Focus timer reads %q", s)
	}

	who, how := trustWords(plugins.Plugin{Trust: plugins.TrustTampered})
	if who != "Changed since it was signed" || strings.Contains(how, "no key") {
		t.Errorf("a tampered plugin reads %q, %q", who, how)
	}
	if originTag(plugins.Plugin{Trust: plugins.TrustTampered}) != "Someone else's" {
		t.Error("a tampered plugin isn't marked as someone else's")
	}
}

// A manifest may name a component or a program twice, and an error may be
// one long word: the page draws them.
func TestThePluginsPageDrawsOddManifests(t *testing.T) {
	a := offline(t)
	writeOwnPlugin(t, a, "twice", `{"id": "twice", "name": "Twice", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["ui.place", "ui.replace", "programs"], "glitched": {"programs": ["git", "git"], "places": [
			{"id": "one", "at": "thread.header.actions", "replaces": true}, {"id": "two", "at": "thread.header.actions", "replaces": true}]}}`, "export function place() { return [] }")
	a.router.Push("/settings/plugins")
	a.pluginsOpen[filepath.Join(plugins.Dir(a.dir), "twice")] = true
	tt := newTester(t, a, 1320, 2400)
	settleTester(tt)
	tt.Advance(300 * time.Millisecond)
	if p := installed(a, "twice"); p.Err != nil || len(p.Replaces) != 2 || len(p.Programs) != 2 {
		t.Fatalf("the manifest didn't load as written: %+v", p)
	}
	if !tt.HasText("Grant programs to Twice") {
		t.Errorf("its row isn't drawn: %q", tt.Texts())
	}
	if got := clip(strings.Repeat("é", 5000), 1200); len([]rune(got)) != 1201 {
		t.Errorf("a long error is cut to %d runes", len([]rune(got)))
	}
}

// With only the built-ins, the page says how to make something yours,
// and offers no filter.
func TestThePluginsPageWithNoneOfYours(t *testing.T) {
	a := offline(t)
	a.router.Push("/settings/plugins")
	tt := newTester(t, a, 1320, 1600)
	settleTester(tt)
	if !tt.HasText("ADDED BY YOU  0") || !tt.HasText(groupPlugins(nil)[0].empty) {
		t.Errorf("no word of how to make a plugin yours: %q", tt.Texts())
	}
	if _, ok := tt.Access("Filter plugins"); ok {
		t.Error("a filter over six plugins")
	}
}

// Every component has a name the user reads, and the grants the brief of
// the page calls dangerous say what they risk; a lighter one says nothing.
func TestPluginsPageWords(t *testing.T) {
	for _, c := range plugins.Components {
		if partTitles[c.Name] == "" {
			t.Errorf("no title for the component %s", c.Name)
		}
	}
	for _, name := range []string{"programs", plugins.Network, "threads:write", "settings:write", "ui.replace", "one-from-the-future"} {
		if capRisk(name) == "" {
			t.Errorf("%s says nothing of what it risks", name)
		}
	}
	for _, c := range plugins.Capabilities {
		if _, heavy := capRisks[c.Name]; !heavy && capRisk(c.Name) != "" {
			t.Errorf("%s reads as weighty: %q", c.Name, capRisk(c.Name))
		}
	}
}
