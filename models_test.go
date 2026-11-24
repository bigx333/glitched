package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
)

// engineReports has the engine's provider p report models, and codex, gemini and
// grok none, as the placeholders they are.
func engineReports(p string, models ...provider.Model) func(*daemon.Config) {
	return func(c *daemon.Config) {
		for _, q := range []string{"claude", "codex", "gemini", "grok"} {
			c.Providers[q] = reporting{c.Providers[q], nil}
		}
		c.Providers[p] = reporting{c.Providers[p], models}
	}
}

// allAnswered waits till the app has every family's engine's answer: an
// answer came, not only the models testApp starts with.
func allAnswered(h *harness) {
	h.t.Helper()
	h.until("every family's models", func() bool {
		if h.a.models.next.IsZero() || h.a.models.asking {
			return false
		}
		for _, f := range families {
			if _, ok := h.a.models.reported[strings.ToLower(f.name)]; !ok {
				return false
			}
		}
		return true
	})
}

// The picker offers what the family's engine reports, a model Glitched
// never heard of with its engine's words for it, and a thread made on it
// names it by its engine's id. models.list lists the same, and a restart
// offers it before the engine answers again.
func TestThePickerOffersTheModelsItsEngineReports(t *testing.T) {
	dir := tempDir(t)
	h := startWith(t, dir, engineReports("claude",
		provider.Model{ID: "claude-opus-5-5", Name: "Opus"},
		provider.Model{ID: "claude-fable-5-2", Name: "Fable 5.2", Description: "Most capable for your hardest tasks"},
	))
	allAnswered(h)
	h.click("Model")
	pickerSettle(t, h)
	for _, s := range []string{"Opus 5.5", "Most capable, for complex work", "Fable 5.2", "Most capable for your hardest tasks"} {
		if !pickerShows(h, s) {
			t.Errorf("the picker doesn't show %q: %q", s, h.tt.Texts())
		}
	}
	for _, s := range []string{"Sonnet 5.5", "Codex", "Gemini", "Grok"} {
		if pickerShows(h, s) {
			t.Errorf("the picker shows %q, which no engine reports", s)
		}
	}
	h.click("Fable 5.2")
	pickerSettle(t, h)
	if got := h.a.modelRef(); got.Provider != "claude" || got.Model != "claude-fable-5-2" {
		t.Errorf("a new thread on Fable is %+v, want claude's claude-fable-5-2", got)
	}
	want := []string{"Claude Opus 5.5", "Claude Fable 5.2"}
	var records struct {
		Models []struct{ Name string } `json:"models"`
	}
	b, _ := json.Marshal(h.a.modelRecords())
	json.Unmarshal(b, &records)
	var listed []string
	for _, m := range records.Models {
		listed = append(listed, m.Name)
	}
	if !slices.Equal(listed, want) {
		t.Errorf("models.list lists %q, want %q", listed, want)
	}
	again := testApp(t, dir, nil, func(func()) {})
	if got := again.modelNames(); !slices.Equal(got, want) {
		t.Errorf("after a restart, before the engine answers, the picker offers %q, want %q", got, want)
	}
}

// A family whose engine reports no model, as a placeholder's, isn't
// offered. A thread already on one of its models keeps it, which shows
// as no longer offered, in a popover as tall as its one row needs, and a
// plugin can't move it to another.
func TestAFamilyWhoseEngineReportsNoneIsntOffered(t *testing.T) {
	h := startWith(t, tempDir(t), engineReports("claude", append([]provider.Model{{ID: "claude-opus-5-5"}}, numbered(3)...)...))
	allAnswered(h)
	h.click("Model")
	pickerSettle(t, h)
	if pickerShows(h, "Codex") || !pickerShows(h, "Opus 5.5") {
		t.Fatalf("the draft's picker shows %q", h.tt.Texts())
	}
	top, effort := pickerBox(t, h)
	draft := effort.Y - top.Y
	h.click("Model")
	codex := api.ModelRef{Provider: "codex", Model: "gpt-5.5-codex", Effort: "high"}
	id := h.a.backend.Create(h.local(), t.TempDir(), "Mine", codex, api.AccessAsk, nil)
	h.thread("Mine")
	h.a.selected = id
	h.tt.Frame()
	h.click("Model")
	pickerSettle(t, h)
	for _, s := range []string{"Codex", "GPT-5.5 Codex", goneModel} {
		if !pickerShows(h, s) {
			t.Errorf("the Codex thread's picker doesn't show %q: %q", s, h.tt.Texts())
		}
	}
	top, _ = h.tt.Find("Codex")
	if effort, _ = h.tt.Find("Effort"); effort.Y-top.Y >= draft {
		t.Errorf("the Codex thread's popover is %v tall over its effort for one row, as the draft's %v for Claude's four", effort.Y-top.Y, draft)
	}
	args := fmt.Sprintf(`{"thread": %q, "model": "gpt-5.5"}`, id)
	if _, err := h.a.configureEffect([]byte(args)); err == nil {
		t.Error("threads.configure set a model no engine offers")
	}
	if m := h.thread("Mine").View.Model; m != codex {
		t.Errorf("the thread's model is %+v, want its own %+v", m, codex)
	}
}

// pickerBox is where the popover's first family row, and its effort,
// are drawn.
func pickerBox(t *testing.T, h *harness) (top, effort ui.Rect) {
	t.Helper()
	top, ok := h.tt.Find("Claude")
	if !ok {
		t.Fatalf("no family row: %q", h.tt.Texts())
	}
	effort, ok = h.tt.Find("Effort")
	if !ok {
		t.Fatalf("no effort: %q", h.tt.Texts())
	}
	return top, effort
}

func numbered(n int) []provider.Model {
	var out []provider.Model
	for i := range n {
		out = append(out, provider.Model{ID: fmt.Sprintf("m-%02d", i), Name: fmt.Sprintf("M %02d", i), Description: fmt.Sprintf("About %02d", i)})
	}
	return out
}

// The models' column grows a row with each model, up to five of them and
// half of the sixth, past which they scroll, in a window with room for
// many more; the effort stays under them, and the keyboard reaches every
// one.
func TestThePickersModelsScrollPastFiveAndAHalfRows(t *testing.T) {
	var pitch float32
	height := func(n int) float32 {
		h := startWith(t, tempDir(t), engineReports("claude", numbered(n)...))
		allAnswered(h)
		h.tt.SetSize(1320, 2000)
		h.a.model = modelKey{"claude", "m-00"}
		h.tt.Frame()
		h.click("Model")
		pickerSettle(t, h)
		top, effort := pickerBox(t, h)
		if first, ok := h.tt.Find("M 00"); ok && pitch == 0 {
			second, _ := h.tt.Find("M 01")
			pitch = second.Y - first.Y
		}
		return effort.Y - top.Y
	}
	four, five, six, seven, many := height(4), height(5), height(6), height(7), height(30)
	if pitch <= 0 || abs32(five-four-pitch) > 0.5 || abs32(six-five-pitch/2) > 0.5 || seven != six || many != six {
		t.Fatalf("over its effort the popover is %v, %v, %v, %v and %v tall for 4, 5, 6, 7 and 30 models, a row %v; want a row more for the 5th, half a row more for the 6th, and no more after", four, five, six, seven, many, pitch)
	}

	h := startWith(t, tempDir(t), engineReports("claude", numbered(30)...))
	allAnswered(h)
	h.a.model = modelKey{"claude", "m-00"}
	h.click("Model")
	pickerSettle(t, h)
	_, effort := pickerBox(t, h)
	if r, ok := h.tt.Find("M 29"); ok && r.Y < effort.Y {
		t.Fatalf("the 30th model shows above the effort before it's scrolled to: %v", r)
	}
	for range 80 {
		if h.tt.Focused("M 29") {
			break
		}
		h.tt.Key(0, ui.KeyTab)
		pickerSettle(t, h)
	}
	if !h.tt.Focused("M 29") {
		t.Fatal("Tab never reached the 30th model")
	}
	r, ok := h.tt.Find("M 29")
	if _, still := pickerBox(t, h); !ok || r.Y+r.H > still.Y || still != effort {
		t.Fatalf("the 30th model, focused, is at %v (shown %v), the effort at %v, was %v", r, ok, still, effort)
	}
	h.tt.Key(0, ui.KeySpace)
	pickerSettle(t, h)
	if h.a.model != (modelKey{"claude", "m-29"}) {
		t.Errorf("Space on the 30th model chose %q", h.a.model)
	}
}

// The popover opens on the chosen model, though it is far down a long
// family's list, and the user scrolls from there; a short family's models
// show whole wherever the long list was scrolled, and pointing back at the
// long family shows the chosen model again. Glitched's own popover does as
// the built-in's does.
func TestThePickerOpensOnTheChosenModelOfALongList(t *testing.T) {
	for name, builtin := range map[string]bool{"built-in": true, "Glitched's own": false} {
		t.Run(name, func(t *testing.T) {
			h := startWith(t, tempDir(t), engineReportsBy(map[string][]provider.Model{
				"codex":  numbered(12),
				"claude": {{ID: "k-00", Name: "K 00", Description: "Short 00"}, {ID: "k-01", Name: "K 01", Description: "Short 01"}},
			}))
			allAnswered(h)
			if !builtin {
				if err := h.a.plugins.SetEnabled("model-picker", false); err != nil {
					t.Fatal(err)
				}
				h.a.pluginsChanged(nil)
			}
			h.a.model = modelKey{"codex", "m-10"}
			h.tt.Frame()
			h.click("Model")
			pickerSettle(t, h)
			if drawn := h.a.plugins.App("model-picker") != nil && h.a.plugins.App("model-picker").PlaceView("picker", "draft/panel") != nil; drawn != builtin {
				t.Fatalf("the built-in drew the popover: %v, want %v", drawn, builtin)
			}
			top, effort := pickerBox(t, h)
			shown := func(name, about string) bool {
				n, ok := h.tt.Find(name)
				a, there := h.tt.Find(about)
				return ok && there && n.Y >= top.Y-5 && a.Y+a.H <= effort.Y-12
			}
			if !shown("M 10", "About 10") {
				t.Errorf("the popover opened without the chosen M 10 in view: %v, %v between %v and %v", boxOf(h.tt.Find("M 10")), boxOf(h.tt.Find("About 10")), top, effort)
			}
			if shown("M 00", "About 00") && shown("M 11", "About 11") {
				t.Errorf("all twelve models show at once: the column didn't stop growing")
			}
			first, _ := h.tt.Find("About 10")
			h.tt.Scroll(first.X, first.Y, 0, -2000)
			pickerSettle(t, h)
			if !shown("M 00", "About 00") || shown("M 10", "About 10") {
				t.Errorf("scrolled to the top, the models show M 00 %v and M 10 %v", shown("M 00", "About 00"), shown("M 10", "About 10"))
			}
			h.hover("Claude")
			pickerSettle(t, h)
			h.hover("Codex")
			pickerSettle(t, h)
			if !shown("M 10", "About 10") {
				t.Errorf("pointing back at Codex doesn't show the chosen M 10: %v", boxOf(h.tt.Find("M 10")))
			}
			h.tt.Scroll(first.X, first.Y, 0, 2000)
			pickerSettle(t, h)
			h.hover("Claude")
			pickerSettle(t, h)
			if !shown("K 00", "Short 00") || !shown("K 01", "Short 01") {
				t.Errorf("Claude's two models don't show whole after Codex's were scrolled to the end: %v, %v", boxOf(h.tt.Find("K 00")), boxOf(h.tt.Find("Short 01")))
			}
			h.tt.Key(0, ui.KeyEscape)
			pickerSettle(t, h)
			h.tt.Move(0, 0)
			h.tt.Key(ui.Cmd, ui.KeyK)
			h.tt.Type("switch model")
			h.tt.Key(0, ui.KeyEnter)
			pickerSettle(t, h)
			if !shown("M 10", "About 10") {
				t.Errorf("Switch model, after Claude was last pointed at, opened without the chosen M 10: %v", boxOf(h.tt.Find("M 10")))
			}
		})
	}
}

// A copy of the picker made before models had keys opens on its chosen
// model too, which it knows by name.
func TestACopyOfThePickerMadeBeforeKeysOpensOnTheChosenModel(t *testing.T) {
	h := startWith(t, tempDir(t), engineReportsBy(map[string][]provider.Model{"claude": numbered(20)}))
	allAnswered(h)
	js, err := os.ReadFile(filepath.Join("testdata", "model-picker-before-keys.js"))
	if err != nil {
		t.Fatal(err)
	}
	writeOwnPlugin(t, h.a, "mine", pickerManifest, string(js))
	h.a.plugins.SetGranted("mine", "ui.replace", true)
	h.a.plugins.SetGranted("mine", "threads:read", true)
	h.tt.SetSize(1320, 2000)
	h.a.chooseModel(modelKey{"claude", "m-19"})
	h.tt.Frame()
	h.click("Model")
	pickerSettle(t, h)
	if app := h.a.plugins.App("mine"); app == nil || app.PlaceView("picker", "draft/panel") == nil {
		t.Fatalf("the copy doesn't draw the picker: %q", h.tt.Texts())
	}
	_, effort := pickerBox(t, h)
	if r, ok := h.tt.Find("About 19"); !ok || r.Y+r.H > effort.Y-12 {
		t.Errorf("the copy opened without the chosen M 19 in view: %v, the effort at %v", r, effort)
	}
}

// boxOf is the box Find found, or none.
func boxOf(r ui.Rect, _ bool) ui.Rect { return r }

// On a short window the popover keeps between the window's top and the
// button: its models scroll sooner, the whole effort control shows and
// takes a click, and the keyboard reaches a model below the first screen.
func TestThePickerKeepsInAShortWindow(t *testing.T) {
	h := startWith(t, tempDir(t), engineReports("claude", numbered(30)...))
	allAnswered(h)
	h.a.model = modelKey{"claude", "m-00"}
	h.tt.SetSize(1320, 460)
	h.tt.Frame()
	h.click("Model")
	pickerSettle(t, h)
	h.tt.Frame()
	top, effort := pickerBox(t, h)
	button, _ := h.tt.Find("Model")
	faster, ok := h.tt.Find("Faster")
	if !ok || top.Y < 0 || faster.Y+faster.H > button.Y {
		t.Fatalf("the popover runs from %v to its effort's foot %v, out of the window's top and the button at %v", top, faster, button)
	}
	at, y := effortAt(t, h.tt)
	h.tt.Press(at(3), y)
	h.tt.Release(at(3), y)
	pickerSettle(t, h)
	if h.a.effort != "max" {
		t.Errorf("a click on the effort's last stop chose %q", h.a.effort)
	}
	for range 80 {
		if h.tt.Focused("M 29") {
			break
		}
		h.tt.Key(0, ui.KeyTab)
		pickerSettle(t, h)
	}
	if r, ok := h.tt.Find("M 29"); !h.tt.Focused("M 29") || !ok || r.Y < top.Y || r.Y+r.H > effort.Y {
		t.Errorf("the 30th model, tabbed to, is at %v (focused %v), out of the models between %v and %v", r, h.tt.Focused("M 29"), top, effort)
	}
}

// engineReportsBy has each provider in by report its models, and every
// other family's provider none.
func engineReportsBy(by map[string][]provider.Model) func(*daemon.Config) {
	return func(c *daemon.Config) {
		for _, f := range families {
			// A family no fake plays, as Antigravity, gets the fake's.
			inner := cmp.Or(c.Providers[f.provider], c.Providers["fake"])
			c.Providers[f.provider] = reporting{inner, by[f.provider]}
		}
	}
}

// A model is the provider's that offers it, though another offers one
// by the same id: the thread made on it runs on the provider it was
// picked from.
func TestAModelIDTwoProvidersShareGoesToTheOnePicked(t *testing.T) {
	h := startWith(t, tempDir(t), engineReportsBy(map[string][]provider.Model{
		"claude": {{ID: "shared", Name: "Shared Claude"}},
		"codex":  {{ID: "shared", Name: "Shared Codex"}},
	}))
	allAnswered(h)
	for _, pick := range []struct{ family, row, provider string }{{"Codex", "Shared Codex", "codex"}, {"Claude", "Shared Claude", "claude"}} {
		if !h.a.pickerOpen {
			h.click("Model")
		}
		pickerSettle(t, h)
		h.tt.Move(0, 0)
		h.click(pick.family)
		pickerSettle(t, h)
		h.click(pick.row)
		pickerSettle(t, h)
		if got := h.a.modelRef(); got.Provider != pick.provider || got.Model != "shared" {
			t.Errorf("picking %s made a thread on %+v, want %s's shared", pick.row, got, pick.provider)
		}
		id := h.a.backend.Create(h.local(), t.TempDir(), pick.row, h.a.modelRef(), api.AccessAsk, nil)
		h.until("the thread", func() bool { _, ok := h.a.find(id); return ok })
		if th, _ := h.a.find(id); h.a.threadChoice(th).Model != (modelKey{pick.provider, "shared"}) {
			t.Errorf("the thread made on %s shows %+v", pick.row, h.a.threadChoice(th).Model)
		}
	}
}

// The effort slider has the levels the chosen model's engine says it runs
// at, in Glitched's words for those it knows and the engine's for others,
// and Low to Max for a model whose engine says none. A thread made at one
// runs at its id.
func TestTheEffortSliderHasTheModelsLevels(t *testing.T) {
	h := startWith(t, tempDir(t), engineReports("claude",
		provider.Model{ID: "five", Name: "Five", Efforts: []provider.Effort{{ID: "low"}, {ID: "medium"}, {ID: "high"}, {ID: "xhigh"}, {ID: "turbo", Label: "Turbo"}}},
		provider.Model{ID: "plain", Name: "Plain"},
	))
	allAnswered(h)
	ids := func() (out []string) {
		for _, e := range h.a.efforts() {
			out = append(out, e.label)
		}
		return out
	}
	h.a.model = modelKey{"claude", "plain"}
	if got := ids(); !slices.Equal(got, []string{"Low", "Medium", "High", "Max"}) {
		t.Errorf("a model whose engine names no efforts runs at %q", got)
	}
	h.a.model = modelKey{"claude", "five"}
	h.click("Model")
	pickerSettle(t, h)
	if got := ids(); !slices.Equal(got, []string{"Low", "Medium", "High", "Extra high", "Turbo"}) {
		t.Errorf("Five runs at %q", got)
	}
	at, y := effortAt(t, h.tt)
	h.tt.Press(at(3), y)
	h.tt.Release(at(3), y)
	pickerSettle(t, h)
	if !pickerShows(h, "Turbo") || h.a.modelRef().Effort != "turbo" {
		t.Errorf("the slider's last stop chose %q: %q", h.a.modelRef().Effort, h.tt.Texts())
	}
}
