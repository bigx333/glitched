package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
)

// A default the user chose stays chosen though its engine no longer
// offers it, as the picker says, till another is chosen; one never
// chosen is the engine's first where it doesn't offer the app's own.
func TestADefaultModelTheEngineNoLongerOffersStaysChosen(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model":"Claude Sonnet 5.5","effort":"High"}`), 0o600)
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"claude":[{"id":"claude-fable-5-2","name":"Fable 5.2"}]}`), 0o600)
	a := testApp(t, dir, nil, func(func()) {})
	if got, want := a.modelRef(), (api.ModelRef{Provider: "claude", Model: "claude-sonnet-5-5", Effort: "high"}); got != want {
		t.Errorf("a default of Sonnet, which the engine no longer offers, starts as %+v, want %+v", got, want)
	}
	if rows := a.pickerRows(familyOf("claude")); len(rows) != 2 || rows[1].desc != goneModel {
		t.Errorf("the picker's Claude rows are %+v, want Fable and Sonnet, no longer offered", rows)
	}

	fresh := t.TempDir()
	os.WriteFile(filepath.Join(fresh, "models.json"), []byte(`{"claude":[{"id":"claude-fable-5-2","name":"Fable 5.2"}]}`), 0o600)
	if got := testApp(t, fresh, nil, func(func()) {}).modelRef(); got.Model != "claude-fable-5-2" {
		t.Errorf("with no default chosen, and Opus not offered, a new thread is on %+v, want Fable", got)
	}
}

// Before its engine first answers, a family offers nothing: only the
// model chosen shows, as it is, not as no longer offered.
func TestAFamilyItsEngineHasntAnsweredOffersNothing(t *testing.T) {
	a := offline(t)
	a.models.reported = map[string][]provider.Model{}
	if got := a.offeredModels(); len(got) != 0 {
		t.Errorf("before any engine answers, the picker offers %+v", got)
	}
	fs := a.modelFamilies()
	if len(fs) != 1 || fs[0].provider != "claude" {
		t.Fatalf("before any engine answers, the picker shows the families %+v, want Claude's alone", fs)
	}
	if rows := a.pickerRows(fs[0]); len(rows) != 1 || rows[0].key != ownChoice.Model || rows[0].desc == goneModel {
		t.Errorf("before Claude's engine answers, its rows are %+v, want the default's alone", rows)
	}
}

// The effort a model is chosen at is one it runs at, the one the picker
// shows: the one chosen before, where the model runs at it.
func TestChoosingAModelTakesAnEffortItRunsAt(t *testing.T) {
	h := startWith(t, tempDir(t), engineReports("claude",
		provider.Model{ID: "claude-opus-5-5", Efforts: []provider.Effort{{ID: "low"}, {ID: "high"}, {ID: "max"}}},
		provider.Model{ID: "small", Name: "Small", Efforts: []provider.Effort{{ID: "low"}, {ID: "medium"}}},
	))
	allAnswered(h)
	h.tt.Frame()
	h.click("Model")
	pickerSettle(t, h)
	h.click("Small")
	pickerSettle(t, h)
	shown := h.a.efforts()[h.a.effortLevel()].id
	if m := h.a.modelRef(); m.Model != "small" || m.Effort != "medium" || shown != "medium" || h.a.effort != "medium" {
		t.Errorf("Small, chosen at High, shows %s and a new thread on it is %+v; want Medium", shown, m)
	}
	h.a.effort = "low"
	h.click("Opus 5.5")
	pickerSettle(t, h)
	if m := h.a.modelRef(); m.Model != "claude-opus-5-5" || m.Effort != "low" {
		t.Errorf("Opus, chosen at Low, which it runs at, gives %+v", m)
	}
}

// threads.configure that sets the model alone sets an effort it runs at.
func TestConfiguringTheModelAloneTakesAnEffortItRunsAt(t *testing.T) {
	h := startWith(t, tempDir(t), engineReports("claude",
		provider.Model{ID: "wide", Name: "Wide", Efforts: []provider.Effort{{ID: "low"}, {ID: "max"}}},
		provider.Model{ID: "small", Name: "Small", Efforts: []provider.Effort{{ID: "low"}, {ID: "medium"}}},
	))
	allAnswered(h)
	id := h.a.backend.Create(h.local(), t.TempDir(), "Mine", api.ModelRef{Provider: "claude", Model: "wide", Effort: "max"}, api.AccessAsk)
	h.thread("Mine")
	if _, err := h.a.configureEffect([]byte(fmt.Sprintf(`{"thread":%q,"model":"small"}`, id))); err != nil {
		t.Fatal(err)
	}
	h.until("the model configured", func() bool { return h.thread("Mine").View.Model.Model == "small" })
	if m := h.thread("Mine").View.Model; m.Effort != "medium" {
		t.Errorf("Wide at Max configured to Small is %+v, want Small at Medium", m)
	}
}

// The model's efforts changing under the held thumb end its press: let
// go, it chooses nothing of the efforts it no longer has. A model of one
// effort shows its one stop.
func TestTheEffortsChangingUnderAHeldThumbEndItsPress(t *testing.T) {
	a := offline(t)
	five := []provider.Effort{{ID: "low"}, {ID: "medium"}, {ID: "high"}, {ID: "xhigh"}, {ID: "max"}}
	a.models.reported["claude"] = []provider.Model{{ID: a.model.ID, Efforts: five}}
	tt := newTester(t, a, 1320, 860)
	click(t, tt, "Model")
	p, _ := offlinePosts.Load(a)
	h := &harness{t: t, a: a, tt: tt, p: p.(*posts)}
	pickerSettle(t, h)
	at, y := effortAt(t, tt)
	x := at(2.85) // between Extra high and Max
	tt.Press(x, y)
	if !a.effortHeld || a.effortLastAt < 3.5 {
		t.Fatalf("the thumb isn't held near Max: held %v at %v", a.effortHeld, a.effortLastAt)
	}
	a.models.reported["claude"][0].Efforts = five[:2]
	tt.Release(x, y)
	pickerSettle(t, h)
	if m := a.modelRef(); m.Effort != "medium" || !pickerShows(h, "Medium") {
		t.Errorf("let go after the efforts became Low and Medium, a new thread is %+v: %q", m, tt.Texts())
	}
	a.models.reported["claude"][0].Efforts = five[:1]
	tt.Frame()
	pickerSettle(t, h)
	tt.Press(at(3), y)
	tt.Release(at(3), y)
	pickerSettle(t, h)
	if m := a.modelRef(); m.Effort != "low" || !pickerShows(h, "Low") {
		t.Errorf("a model of one effort gives %+v: %q", m, tt.Texts())
	}
}

// A models.json of null is no cache: the engine's answer fills it.
func TestAModelsFileOfNullIsNoCache(t *testing.T) {
	dir := tempDir(t)
	os.WriteFile(filepath.Join(dir, "models.json"), []byte("null"), 0o600)
	h := startWith(t, dir, engineReports("claude", provider.Model{ID: "one", Name: "One"}))
	allAnswered(h)
	if got := h.a.offeredModels(); len(got) != 1 || got[0].key != (modelKey{"claude", "one"}) {
		t.Errorf("the picker offers %+v, want claude's one", got)
	}
}

// A provider the engine serves that Glitched has no family for is
// offered by its key, and a model of it is that provider's.
func TestAProviderGlitchedDoesntKnowIsOffered(t *testing.T) {
	h := startWith(t, tempDir(t), func(c *daemon.Config) {
		engineReports("claude", provider.Model{ID: "claude-opus-5-5"})(c)
		c.Providers["another"] = reporting{c.Providers["claude"], []provider.Model{{ID: "one", Name: "One"}}}
	})
	h.until("another's models", func() bool { _, ok := h.a.models.reported["another"]; return ok })
	if !slices.ContainsFunc(h.a.modelFamilies(), func(f family) bool { return f.provider == "another" }) {
		t.Errorf("the picker's families are %+v, without another", h.a.modelFamilies())
	}
	if !slices.ContainsFunc(h.a.offeredModels(), func(m modelInfo) bool { return m.key == (modelKey{"another", "one"}) && m.name == "another One" }) {
		t.Errorf("the picker offers %+v, without another's One", h.a.offeredModels())
	}
}

// A provider that couldn't say what it runs keeps what it said before,
// and is asked again soon, not at the next recheck.
func TestAProviderThatCouldntSayIsAskedAgainSoon(t *testing.T) {
	dir := tempDir(t)
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"claude":[{"id":"before","name":"Before"}]}`), 0o600)
	h := startWith(t, dir, func(c *daemon.Config) {
		engineReports("codex", provider.Model{ID: "gpt-5.5"})(c)
		c.Providers["claude"] = undescribed{c.Providers["claude"]}
	})
	h.until("codex's models", func() bool { _, ok := h.a.models.reported["codex"]; return ok })
	h.until("the answer", func() bool { return !h.a.models.asking })
	if got := h.a.models.reported["claude"]; len(got) != 1 || got[0].ID != "before" {
		t.Errorf("claude, which couldn't say, offers %+v, want what it said before", got)
	}
	if wait := time.Until(h.a.models.next); wait > modelsRetry {
		t.Errorf("claude, which couldn't say, is asked again in %v, want within %v", wait, modelsRetry)
	}
}

// Each model offered has a name of its own, which the settings choose
// the default by: two the engine names alike go to the one chosen.
func TestModelsNamedAlikeAreChosenApart(t *testing.T) {
	h := startWith(t, tempDir(t), engineReports("claude",
		provider.Model{ID: "a", Name: "Same"},
		provider.Model{ID: "b", Name: "Same"},
	))
	allAnswered(h)
	names := h.a.modelNames()
	if len(slices.Compact(slices.Sorted(slices.Values(names)))) != len(names) {
		t.Fatalf("the settings offer %q, two alike", names)
	}
	second := modelKey{"claude", "b"}.String()
	if !slices.Contains(names, "Claude Same") || !slices.Contains(names, second) {
		t.Fatalf("the settings offer %q, want Claude Same and %s", names, second)
	}
	value, _ := json.Marshal(second)
	if _, err := h.a.setAppSetting([]byte(fmt.Sprintf(`{"key":"defaultModel","value":%s}`, value))); err != nil {
		t.Fatal(err)
	}
	if h.a.model != (modelKey{"claude", "b"}) {
		t.Errorf("choosing %q made the default %+v, want claude's b", second, h.a.model)
	}
}
