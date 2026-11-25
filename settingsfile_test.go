package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Each setting the Settings pages edit is as it was set once the app
// starts anew on the same state directory.
func TestSettingsOutliveARestart(t *testing.T) {
	dir := t.TempDir()
	a := testApp(t, dir, nil, func(func()) {})
	tt := ui.NewTester(a.view, 1320, 860)
	a.router.Push("/settings/appearance")
	tt.Frame()
	click(t, tt, "Tint the panels")
	tt.Frame()
	if !a.settings.tintPanels {
		t.Fatal("the switch left Tint panels off")
	}
	s := &a.settings
	s.confirmDelete, s.theme, s.accent, s.glass, s.vibrancy, s.wind, s.fontSize = false, "Light", 3, false, 0.25, false, 15
	s.timeFormat, s.streaming, s.newThreadMode, s.notifications, s.zen = "24-hour", false, 1, false, true
	tt.Frame()
	a.settingsOut.wait()
	want := fileOf(a.settings)
	if reflect.DeepEqual(want, fileOf(offline(t).settings)) {
		t.Fatal("the test sets every setting to its default")
	}
	if got := fileOf(testApp(t, dir, nil, func(func()) {}).settings); !reflect.DeepEqual(got, want) {
		t.Fatalf("after a restart the settings are\n%+v\nwant\n%+v", got, want)
	}
}

// Zen mode is off at first, a switch on the Appearance page turns it on,
// settings.json keeps it, and a restart finds it on.
func TestZenModeIsAnAppearanceSwitchKeptAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	a := testApp(t, dir, nil, func(func()) {})
	if a.settings.zen {
		t.Fatal("Zen mode is on by default")
	}
	// Tall enough to show the page's last card.
	tt := ui.NewTester(a.view, 1320, 1400)
	a.router.Push("/settings/appearance")
	tt.Frame()
	if !tt.HasText("CHAT") || !tt.HasText("Fold each turn's steps and in-between replies into one line you can open. Approvals, questions and errors always show.") {
		t.Fatalf("the Appearance page has no Zen mode row: %q", tt.Texts())
	}
	// The switch, not the row's title, which says the same.
	sw, ok := tt.AccessByRole(ui.RoleSwitch, "Zen mode")
	if !ok {
		t.Fatal("no Zen mode switch")
	}
	tt.ClickAt(sw.Bounds.X+sw.Bounds.W/2, sw.Bounds.Y+sw.Bounds.H/2)
	tt.Frame()
	if !a.settings.zen {
		t.Fatal("the switch left Zen mode off")
	}
	a.settingsOut.wait()
	if b, err := os.ReadFile(filepath.Join(dir, "settings.json")); err != nil || !strings.Contains(string(b), `"zen": true`) {
		t.Fatalf("settings.json doesn't keep Zen mode (%v):\n%s", err, b)
	}
	if !testApp(t, dir, nil, func(func()) {}).settings.zen {
		t.Fatal("after a restart Zen mode is off")
	}
}

// The palette's Toggle Zen mode turns Zen mode on and off, as settings.json
// keeps it.
func TestThePaletteTogglesZenMode(t *testing.T) {
	dir := t.TempDir()
	a := testApp(t, dir, nil, func(func()) {})
	tt := ui.NewTester(a.view, 1320, 860)
	tt.Frame()
	var zen command
	for _, cmd := range a.actions() {
		if cmd.id == "zen.toggle" {
			zen = cmd
		}
	}
	if zen.label != "Toggle Zen mode" || zen.section != "Actions" || appIconNamed(zen.icon) == nil {
		t.Fatalf("the palette's Zen action is %+v", zen)
	}
	if err := a.runCommand("zen.toggle", ""); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.settingsOut.wait()
	if b, err := os.ReadFile(filepath.Join(dir, "settings.json")); !a.settings.zen || err != nil || !strings.Contains(string(b), `"zen": true`) {
		t.Fatalf("after Toggle Zen mode, zen %v, settings.json (%v):\n%s", a.settings.zen, err, b)
	}
	if err := a.runCommand("zen.toggle", ""); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.settings.zen {
		t.Fatal("Toggle Zen mode again left it on")
	}
}

// A settings.json that's missing, older than a setting, names what the
// pages no longer offer or doesn't parse keeps the defaults for what it
// doesn't give.
func TestASettingsFileGivesOnlyWhatThePagesOffer(t *testing.T) {
	defaults := offline(t).settings
	with := func(change func(*settings)) settingsFile {
		s := defaults
		change(&s)
		return fileOf(s)
	}
	for _, tc := range []struct {
		name, file string
		want       settingsFile
	}{
		{"missing", "", fileOf(defaults)},
		{"older", `{"theme":"Light","wind":false}`, with(func(s *settings) { s.theme, s.wind = "Light", false })},
		{"zen", `{"zen":true}`, with(func(s *settings) { s.zen = true })},
		{"mistyped zen", `{"zen":"on"}`, fileOf(defaults)},
		{"stale", `{"theme":"Neon","accent":"Mauve","fontSize":99,"glassTint":-1,"timestamps":"Mayan","newThread":"Cloud","claudeModel":"Claude 2","codexModel":"GPT-1"}`, fileOf(defaults)},
		{"mistyped", `{"wind":"yes","glass":false}`, with(func(s *settings) { s.glass = false })},
		{"corrupt", `{"theme": `, fileOf(defaults)},
		{"not an object", `[1, 2]`, fileOf(defaults)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.file != "" {
				if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(tc.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := fileOf(testApp(t, dir, nil, func(func()) {}).settings); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("the settings are\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

// A change saved over a settings.json that doesn't parse replaces it, and
// leaves the app's other files as they were.
func TestACorruptSettingsFileIsReplacedAlone(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("settings.json", "{oops")
	write("composer.json", `{"model":"Claude Sonnet 5.5"}`)
	write("layout.json", `{"previewWidth":500}`)
	a := testApp(t, dir, nil, func(func()) {})
	a.settings.wind = false
	a.saveSettings()
	a.settingsOut.wait()
	again := testApp(t, dir, nil, func(func()) {})
	if again.settings.wind || again.model != keyNamed("Claude Sonnet 5.5") || again.previewDragged != 500 {
		t.Fatalf("after the save, wind %v, model %q, preview %v", again.settings.wind, again.model, again.previewDragged)
	}
}

// Two windows on one state directory each change a setting: neither undoes
// the other's, and a key this version doesn't know stays in the file.
func TestTwoWindowsKeepEachOthersSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"fromANewerVersion":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	one, two := testApp(t, dir, nil, func(func()) {}), testApp(t, dir, nil, func(func()) {})
	one.settings.theme = "Dark"
	one.saveSettings()
	one.settingsOut.wait()
	one.settings.timeFormat = "24-hour"
	one.saveSettings()
	one.settingsOut.wait()
	two.settings.fontSize, two.settings.notifications = 14, false
	two.saveSettings()
	two.settingsOut.wait()
	again := testApp(t, dir, nil, func(func()) {}).settings
	if again.theme != "Dark" || again.fontSize != 14 || again.timeFormat != "24-hour" || again.notifications {
		t.Fatalf("after both saved, theme %q, font size %v, times %q, notifications %v", again.theme, again.fontSize, again.timeFormat, again.notifications)
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), `"fromANewerVersion": 42`) {
		t.Fatalf("settings.json lost the newer version's key (%v):\n%s", err, b)
	}
}
