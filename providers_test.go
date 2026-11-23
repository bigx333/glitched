package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/daemon"
	"glitched/internal/jsonfile"
	"glitched/internal/provider"
	"glitched/internal/transport"
)

// needing is a provider whose user must act first: Describe says what.
type needing struct {
	provider.Adapter
	kind error
	help string
}

func (n needing) Describe(context.Context) (provider.Info, error) {
	return provider.Info{}, provider.Needs{Kind: n.kind, Help: n.help}
}

// openProviders shows Settings › Providers with the rows of providers
// open.
func openProviders(h *harness, providers ...string) {
	h.t.Helper()
	for _, p := range providers {
		h.a.providersOpen[p] = true
	}
	h.a.router.Push("/settings/providers")
	h.tt.Frame()
	h.tt.Advance(time.Second)
}

// Settings › Providers lists each provider its engine serves, with the
// models it reports, one Glitched never heard of in its engine's words,
// and no family whose engine offers none.
func TestTheProvidersPageListsWhatTheEngineReports(t *testing.T) {
	h := startWith(t, tempDir(t), engineReports("claude",
		provider.Model{ID: "claude-opus-5-5", Name: "Opus"},
		provider.Model{ID: "claude-fable-5-2", Name: "Fable 5.2", Description: "Most capable for your hardest tasks"},
	))
	allAnswered(h)
	openProviders(h, "claude")
	h.click("Default model for new threads")
	h.tt.Advance(time.Second)
	for _, s := range []string{"Claude", "Ready", "Default · Opus 5.5 · High", "Opus 5.5 · High", "Opus 5.5", "Fable 5.2"} {
		if !h.tt.HasText(s) {
			t.Errorf("the page doesn't show %q: %q", s, h.tt.Texts())
		}
	}
	for _, s := range []string{"Sonnet 5.5", "Codex", "Gemini", "Grok", "Claude Code", "v2.4.1", "Custom models"} {
		if h.tt.HasText(s) {
			t.Errorf("the page shows %q, which no engine reports", s)
		}
	}
}

// A default the engine no longer offers stays chosen on the page, in its
// provider's row, and says so.
func TestTheProvidersPageKeepsADefaultNoLongerOffered(t *testing.T) {
	dir := tempDir(t)
	os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model":{"provider":"claude","id":"claude-sonnet-5-5"},"effort":"high"}`), 0o600)
	h := startWith(t, dir, engineReports("claude", provider.Model{ID: "claude-fable-5-2", Name: "Fable 5.2"}))
	allAnswered(h)
	openProviders(h, "claude")
	for _, s := range []string{"Default · Sonnet 5.5 · High · No longer offered", "Sonnet 5.5 · High", "No longer offered"} {
		if !h.tt.HasText(s) {
			t.Errorf("the page doesn't show %q: %q", s, h.tt.Texts())
		}
	}
	h.click("Default model for new threads")
	h.tt.Advance(time.Second)
	for _, s := range []string{"Fable 5.2", "Sonnet 5.5"} {
		if !h.tt.HasText(s) {
			t.Errorf("the page doesn't show %q: %q", s, h.tt.Texts())
		}
	}
	if got := h.a.modelRef(); got.Model != "claude-sonnet-5-5" {
		t.Errorf("the page changed the default to %+v", got)
	}
}

// The page's default is the picker's: one chosen on either is the other's,
// with its effort, and it is composer.json's.
func TestTheProvidersPageAndThePickerChooseOneDefault(t *testing.T) {
	dir := tempDir(t)
	h := startWith(t, dir, engineReports("claude",
		provider.Model{ID: "claude-opus-5-5", Efforts: []provider.Effort{{ID: "low"}, {ID: "high"}}},
		provider.Model{ID: "claude-fable-5-2", Name: "Fable 5.2", Efforts: []provider.Effort{{ID: "low"}, {ID: "medium"}}},
	))
	allAnswered(h)
	openProviders(h, "claude")
	h.click("Default model for new threads")
	h.tt.Advance(time.Second)
	h.click("Fable 5.2")
	h.tt.Frame()
	if got := h.a.modelRef(); got.Model != "claude-fable-5-2" || got.Effort != "medium" {
		t.Fatalf("after choosing Fable on the page, a new thread is on %+v, want Fable at medium", got)
	}
	var file choice
	if err := jsonfile.Load(filepath.Join(dir, "composer.json"), &file); err != nil || file.Model != (modelKey{"claude", "claude-fable-5-2"}) || file.Effort != "medium" {
		t.Errorf("composer.json holds %+v (%v), want Fable at medium", file, err)
	}
	h.a.router.Push("/")
	h.tt.Frame()
	if !h.tt.HasText("Fable 5.2") {
		t.Errorf("the picker doesn't show the page's default: %q", h.tt.Texts())
	}
	h.click("Model")
	pickerSettle(t, h)
	h.click("Opus 5.5")
	pickerSettle(t, h)
	openProviders(h)
	if !h.tt.HasText("Default · Opus 5.5 · Low") || !h.tt.HasText("Opus 5.5 · Low") {
		t.Errorf("after choosing Opus in the picker, the page doesn't show it as the default: %q", h.tt.Texts())
	}
}

// A provider that isn't installed, or is signed out, says so on its row
// and what to do in its detail; one that isn't installed is an answer,
// not asked again soon.
func TestTheProvidersPageSaysWhatAProviderNeeds(t *testing.T) {
	h := startWith(t, tempDir(t), func(c *daemon.Config) {
		engineReports("claude", provider.Model{ID: "claude-opus-5-5"})(c)
		c.Providers["antigravity"] = needing{c.Providers["claude"], provider.ErrSignedOut, "Antigravity is signed out. Run agy in Terminal to sign in."}
		c.Providers["grok"] = needing{c.Providers["claude"], provider.ErrNotInstalled, "Grok is not installed."}
	})
	allAnswered(h)
	if got := h.a.models.served["grok"].State; got != transport.ProviderNotInstalled {
		t.Errorf("grok's state is %q, want not installed", got)
	}
	openProviders(h, "claude", "antigravity", "grok")
	for _, s := range []string{"Signed out", "Sign in to Antigravity", "Antigravity is signed out. Run agy in Terminal to sign in.", "Not installed", "Install Grok", "Grok is not installed.", "Check again"} {
		if !h.tt.HasText(s) {
			t.Errorf("the page doesn't show %q: %q", s, h.tt.Texts())
		}
	}
	h.click("Default model for new threads")
	h.tt.Advance(time.Second)
	h.click("High")
	h.tt.Frame()
	if got := h.a.modelRef(); got.Model != "claude-opus-5-5" || got.Effort != "high" {
		t.Errorf("choosing High in the control made the default %+v", got)
	}
	if got := h.a.models.reported["grok"]; len(got) != 0 {
		t.Errorf("grok, not installed, offers %+v", got)
	}
}

// Before the engine first answers, the page shows the providers it last
// offered models of, from models.json, as being checked: it waits on
// nothing.
func TestTheProvidersPageWaitsOnNoEngine(t *testing.T) {
	a := offline(t)
	a.models.reported = map[string][]provider.Model{"antigravity": {{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash"}}}
	a.model = modelKey{"claude", "claude-opus-5-5"}
	tt := newTester(t, a, 1320, 860)
	a.router.Push("/settings/providers")
	settleTester(tt)
	for _, s := range []string{"Claude", "Antigravity", "Checking…"} {
		if !tt.HasText(s) {
			t.Errorf("before the engine answers, the page doesn't show %q: %q", s, tt.Texts())
		}
	}
	if strings.Contains(strings.Join(tt.Texts(), "\n"), "Ready") {
		t.Errorf("before the engine answers, a provider reads Ready: %q", tt.Texts())
	}
}

// A provider Glitched has no family for, whose engine says the user must
// sign in first, still gets its row.
func TestTheProvidersPageListsAnUnknownProviderThatNeedsSomething(t *testing.T) {
	h := startWith(t, tempDir(t), func(c *daemon.Config) {
		engineReports("claude", provider.Model{ID: "claude-opus-5-5"})(c)
		c.Providers["new-agent"] = needing{c.Providers["claude"], provider.ErrSignedOut, "New Agent is signed out."}
	})
	h.until("new-agent's answer", func() bool { return h.a.models.served["new-agent"].Error != "" })
	openProviders(h, "new-agent")
	for _, s := range []string{"Signed out", "New Agent is signed out."} {
		if !h.tt.HasText(s) {
			t.Errorf("the page doesn't show %q for a provider it has no family for: %q", s, h.tt.Texts())
		}
	}
}

// catalog is a provider whose model runs at the one effort it says.
type catalog struct {
	provider.Adapter
	effort *atomic.Value
}

func (c catalog) Describe(context.Context) (provider.Info, error) {
	return provider.Info{Models: []provider.Model{{ID: "model", Name: "Test Model", Efforts: []provider.Effort{{ID: c.effort.Load().(string)}}}}}, nil
}

// When the engine stops offering the default's effort, the page shows the
// effort the composer now runs it at, as the picker does.
func TestTheProvidersPageShowsTheEffortThePickerRuns(t *testing.T) {
	var effort atomic.Value
	effort.Store("high")
	h := startWith(t, tempDir(t), func(c *daemon.Config) {
		engineReports("claude", provider.Model{ID: "claude-opus-5-5"})(c)
		c.Providers["other"] = catalog{c.Providers["claude"], &effort}
	})
	offers := func(id string) func() bool {
		return func() bool {
			ms := h.a.models.reported["other"]
			return len(ms) == 1 && len(ms[0].Efforts) == 1 && ms[0].Efforts[0].ID == id
		}
	}
	h.until("high", offers("high"))
	openProviders(h, "other")
	h.click("Default model for new threads")
	h.tt.Advance(time.Second)
	h.click("Test Model")
	h.tt.Frame()
	if !h.tt.HasText("Test Model · High") {
		t.Fatalf("the chosen default isn't at High: %q", h.tt.Texts())
	}
	effort.Store("low")
	h.a.checkProviders()
	h.until("low", offers("low"))
	h.tt.Frame()
	if !h.tt.HasText("Test Model · Low") || slices.ContainsFunc(h.tt.Texts(), func(s string) bool { return strings.Contains(strings.ToLower(s), "test model · high") }) {
		t.Errorf("after the engine offers only Low, the page shows %q; the picker runs %q", h.tt.Texts(), h.a.modelRef().Effort)
	}
}
