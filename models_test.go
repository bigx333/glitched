package main

import (
	"encoding/json"
	"fmt"
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

// allAnswered waits till the app has every family's engine's answer.
func allAnswered(h *harness) {
	h.t.Helper()
	h.until("every family's models", func() bool {
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
// as no longer offered, and a plugin can't move it to another.
func TestAFamilyWhoseEngineReportsNoneIsntOffered(t *testing.T) {
	h := startWith(t, tempDir(t), engineReports("claude", provider.Model{ID: "claude-opus-5-5"}))
	allAnswered(h)
	h.click("Model")
	pickerSettle(t, h)
	if pickerShows(h, "Codex") || !pickerShows(h, "Opus 5.5") {
		t.Fatalf("the draft's picker shows %q", h.tt.Texts())
	}
	h.click("Model")
	codex := api.ModelRef{Provider: "codex", Model: "gpt-5.5-codex", Effort: "high"}
	id := h.a.backend.Create(h.local(), t.TempDir(), "Mine", codex, api.AccessAsk)
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
	args := fmt.Sprintf(`{"thread": %q, "model": "GPT-5.5"}`, id)
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

// The models' column grows with the models, up to modelRows of them, past
// which they scroll, the effort staying under them; the keyboard reaches
// every one.
func TestThePickersModelsScrollPastModelRows(t *testing.T) {
	height := func(n int) float32 {
		h := startWith(t, tempDir(t), engineReports("claude", numbered(n)...))
		allAnswered(h)
		h.a.model = "Claude M 00"
		h.click("Model")
		pickerSettle(t, h)
		top, effort := pickerBox(t, h)
		return effort.Y - top.Y
	}
	one, three, eight, many := height(1), height(3), height(modelRows), height(30)
	if !(one < three && three < eight) || many != eight {
		t.Fatalf("the popover is %v, %v, %v and %v tall over its effort for 1, 3, %d and 30 models; want it to grow up to %d and stay", one, three, eight, many, modelRows, modelRows)
	}

	h := startWith(t, tempDir(t), engineReports("claude", numbered(30)...))
	allAnswered(h)
	h.a.model = "Claude M 00"
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
	if h.a.model != "Claude M 29" {
		t.Errorf("Space on the 30th model chose %q", h.a.model)
	}
}

// On a short window the popover keeps in it: its models scroll sooner.
func TestThePickerKeepsInAShortWindow(t *testing.T) {
	h := startWith(t, tempDir(t), engineReports("claude", numbered(30)...))
	allAnswered(h)
	h.tt.SetSize(1320, 460)
	h.tt.Frame()
	h.click("Model")
	pickerSettle(t, h)
	h.tt.Frame()
	top, effort := pickerBox(t, h)
	if top.Y < 0 || effort.Y+effort.H > 460 {
		t.Errorf("the popover runs from %v to %v, out of a window 460 tall", top, effort)
	}
}
