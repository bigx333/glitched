package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

const pickerManifest = `{"id": "mine", "name": "mine", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": ["ui.place", "ui.replace", "threads:read"], "glitched": {"places": [{"id": "picker", "at": "composer.model", "replaces": true}]}}`

// pickerShows is whether the window shows the text s, whole.
func pickerShows(h *harness, s string) bool {
	_, ok := h.tt.Find(s)
	return ok
}

// pickerSettle lets the plugins draw what changed, and the frame after.
func pickerSettle(t *testing.T, h *harness) {
	t.Helper()
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
}

// The model picker is the built-in Model picker plugin's, till a plugin of
// yours replaces it; yours failing brings the built-in back, and with the
// built-in off Glitched draws its own.
func TestTheModelPickerIsABuiltInPluginYoursReplaces(t *testing.T) {
	h := start(t, tempDir(t))
	h.click("Model")
	pickerSettle(t, h)
	builtin := func() bool {
		app := h.a.plugins.App("model-picker")
		return pickerShows(h, "Sonnet 5.5") && app != nil && app.PlaceView("picker", "draft/button") != nil && app.PlaceView("picker", "draft/panel") != nil
	}
	if !builtin() {
		t.Fatalf("the built-in doesn't draw the model picker: %q", h.tt.Texts())
	}
	expectBuiltinFailures(t)
	writeOwnPlugin(t, h.a, "mine", pickerManifest, mineModule)
	h.a.plugins.SetGranted("mine", "ui.replace", true)
	h.a.plugins.SetGranted("mine", "threads:read", true)
	pickerSettle(t, h)
	if !pickerShows(h, "Mine") || pickerShows(h, "Sonnet 5.5") {
		t.Fatalf("yours doesn't replace the built-in: %q", h.tt.Texts())
	}
	writeOwnPlugin(t, h.a, "mine", pickerManifest, brokenModule)
	pickerSettle(t, h)
	if !builtin() {
		t.Fatalf("yours that throws left no built-in: %q", h.tt.Texts())
	}
	h.a.plugins.SetEnabled("mine", false)
	if err := h.a.plugins.SetEnabled("model-picker", false); err != nil {
		t.Fatal(err)
	}
	h.a.pluginsChanged(nil)
	pickerSettle(t, h)
	if !pickerShows(h, "Sonnet 5.5") || len(h.a.plugins.Replacers("composer.model")) > 0 {
		t.Fatalf("with no plugin to draw it, Glitched's own picker doesn't show: %q", h.tt.Texts())
	}
}

// A copy of the picker made before models had keys, which keys its rows
// by the names they show, still chooses: a family's row shows its models,
// and a model's row chooses it.
func TestACopyOfThePickerMadeBeforeKeysStillChooses(t *testing.T) {
	h := start(t, tempDir(t))
	js, err := os.ReadFile(filepath.Join("testdata", "model-picker-before-keys.js"))
	if err != nil {
		t.Fatal(err)
	}
	writeOwnPlugin(t, h.a, "mine", pickerManifest, string(js))
	h.a.plugins.SetGranted("mine", "ui.replace", true)
	h.a.plugins.SetGranted("mine", "threads:read", true)
	h.click("Model")
	pickerSettle(t, h)
	if app := h.a.plugins.App("mine"); app == nil || app.PlaceView("picker", "draft/panel") == nil {
		t.Fatalf("the copy doesn't draw the picker: %q", h.tt.Texts())
	}
	h.tt.Move(0, 0)
	h.click("Codex")
	pickerSettle(t, h)
	h.click("GPT-5.5")
	pickerSettle(t, h)
	if h.a.model != (modelKey{"codex", "gpt-5.5"}) {
		t.Errorf("a click on the copy's GPT-5.5 chose %+v: %q", h.a.model, h.tt.Texts())
	}
}

// The built-in's rows are as tall as Glitched's own popover draws them,
// and its popover as tall, at any interface font size: they follow the
// font's lines, as the text does on each platform, not a height measured
// once on one of them.
func TestTheModelPickersRowsAreAsTallAsGlitchedsOwn(t *testing.T) {
	for _, size := range []float64{11, 13, 16} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			gaps := func(builtin bool) [3]float32 {
				h := start(t, tempDir(t))
				h.a.settings.fontSize = size
				if !builtin {
					if err := h.a.plugins.SetEnabled("model-picker", false); err != nil {
						t.Fatal(err)
					}
					h.a.pluginsChanged(nil)
				}
				h.click("Model")
				pickerSettle(t, h)
				if drawn := h.a.plugins.App("model-picker") != nil && h.a.plugins.App("model-picker").PlaceView("picker", "draft/panel") != nil; drawn != builtin {
					t.Fatalf("the built-in drew the picker: %v, want %v", drawn, builtin)
				}
				y := func(text string) float32 {
					t.Helper()
					r, ok := h.tt.Find(text)
					if !ok {
						t.Fatalf("no %q in %q", text, h.tt.Texts())
					}
					return r.Y
				}
				return [3]float32{y("Codex") - y("Claude"), y("Sonnet 5.5") - y("Opus 5.5"), y("Effort") - y("Claude")}
			}
			if own, builtin := gaps(false), gaps(true); own != builtin {
				t.Errorf("at %vpt the built-in's family rows, model rows and effort are %v apart, Glitched's own %v", size, builtin, own)
			}
		})
	}
}

// With the built-in off and yours failing, Glitched draws its own picker,
// and Settings says so beside the failure.
func TestAFailedModelPickerSaysGlitchedDrawsItsOwn(t *testing.T) {
	h := start(t, tempDir(t))
	if err := h.a.plugins.SetEnabled("model-picker", false); err != nil {
		t.Fatal(err)
	}
	h.a.pluginsChanged(nil)
	writeOwnPlugin(t, h.a, "mine", pickerManifest, brokenModule)
	h.a.plugins.SetGranted("mine", "ui.replace", true)
	h.a.plugins.SetGranted("mine", "threads:read", true)
	h.click("Model")
	pickerSettle(t, h)
	if h.a.plugins.Failed("mine") == nil || !pickerShows(h, "Sonnet 5.5") {
		t.Fatalf("yours that throws didn't leave Glitched's own picker: %v %q", h.a.plugins.Failed("mine"), h.tt.Texts())
	}
	p := installed(h.a, "mine")
	h.a.pluginsOpen[p.Folder] = true
	h.a.router.Push("/settings/plugins")
	h.tt.Advance(time.Second)
	if why := h.a.partState(p, "composer.model").why; !h.tt.HasText(why) || !strings.HasPrefix(why, "It failed, so Glitched draws its own:") {
		t.Errorf("Settings says %q of the failed picker", why)
	}
}

// The built-in's rows do what the picker's own did: pointing at a family
// shows its models, a click on a model chooses it, and the effort's
// slider and fast mode's switch are Glitched's.
func TestTheModelPickersRowsKeepTheirWays(t *testing.T) {
	h := start(t, tempDir(t))
	h.click("Model")
	pickerSettle(t, h)
	if h.a.plugins.App("model-picker").PlaceView("picker", "draft/panel") == nil {
		t.Fatal("the built-in doesn't draw the popover")
	}
	claude, _ := h.tt.Find("Claude")
	h.hover("Codex")
	pickerSettle(t, h)
	if !pickerShows(h, "GPT-5.5 Codex") || pickerShows(h, "Sonnet 5.5") {
		t.Fatalf("pointing at Codex doesn't show its models: %q", h.tt.Texts())
	}
	if moved, _ := h.tt.Find("Claude"); moved != claude {
		t.Errorf("Claude moved from %v to %v as Codex opened", claude, moved)
	}
	h.click("GPT-5.5")
	pickerSettle(t, h)
	if h.a.model != keyNamed("GPT-5.5") {
		t.Errorf("a click on GPT-5.5 chose %q", h.a.model)
	}
	h.click("Fast mode")
	pickerSettle(t, h)
	if !h.a.fast {
		t.Error("fast mode's switch didn't turn it on")
	}
	if _, ok := h.tt.AccessByRole(ui.RoleSlider, "Effort"); !ok {
		t.Error("the popover has no effort slider")
	}
}

// A copy that lists more than the picker offers chooses nothing more: in
// a Claude thread, a row of another provider's model, or of no model,
// does nothing on a click.
func TestAModelPickersCopyChoosesOnlyWhatThePickerOffers(t *testing.T) {
	h := start(t, tempDir(t))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	id := h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAsk, nil)
	h.thread("Mine")
	h.a.selected = id
	h.tt.Frame()
	writeOwnPlugin(t, h.a, "mine", pickerManifest, `
export function place({items}) {
  return items.map(({context}) => context.part === "button" ? {type: "text", text: "Pick"} : {
    type: "list", id: "models", label: "All", noKeys: true, height: 300, rowHeight: 30,
    items: [...context.models.map(m => ({key: m.key, label: m.name})), {key: "Made up", label: "Made up"}],
  });
}
export function rows({keys, place}) {
  return keys.map(key => ({type: "text", text: "Row " + (place.context.models.find(m => m.key === key)?.name ?? key)}));
}
`)
	h.a.plugins.SetGranted("mine", "ui.replace", true)
	h.a.plugins.SetGranted("mine", "threads:read", true)
	pickerSettle(t, h)
	h.click("Pick")
	pickerSettle(t, h)
	for _, row := range []string{"Row GPT-5.5 Codex", "Row Made up"} {
		h.click(row)
		pickerSettle(t, h)
	}
	if m := h.thread("Mine").View.Model; m != opus {
		t.Fatalf("a copy's rows changed a Claude thread's model to %+v", m)
	}
	h.click("Row Claude Haiku 5.5")
	want := api.ModelRef{Provider: "claude", Model: "claude-haiku-5-5", Effort: "high"}
	h.until("the thread's new model", func() bool { return h.thread("Mine").View.Model == want })
}
