package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
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
		at := []float32{y("YOURS  6"), disclosure("Scratchpad").Bounds.Y, y("FROM OTHERS  1"), disclosure("Linear sync").Bounds.Y, y("BUILT INTO GLITCHED  6"), disclosure("Diff panel").Bounds.Y, y("ADD AND UPDATE")}
		if !slices.IsSorted(at) {
			t.Errorf("yours, then others', then the built-ins, then installing: %v", at)
		}
	})

	t.Run("opens what waits on you, and nothing else", func(t *testing.T) {
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
		if !tt.HasText(a.plugins.Name("release-captain") + ": It draws it once you grant it ui.replace.") {
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
		// The built-in's row, the second named Diff panel, says the copy
		// runs and reverts to itself.
		if !tt.HasText("Your copy runs in its place") {
			t.Fatalf("the built-in doesn't say your copy runs: %q", tt.Texts())
		}
		click("Revert to built-in, Diff panel")
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

// With only the built-ins, the page says how to make something yours,
// and offers no filter.
func TestThePluginsPageWithNoneOfYours(t *testing.T) {
	a := offline(t)
	a.router.Push("/settings/plugins")
	tt := newTester(t, a, 1320, 1600)
	settleTester(tt)
	if !tt.HasText("YOURS  0") || !tt.HasText(groupPlugins(nil)[0].empty) {
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
