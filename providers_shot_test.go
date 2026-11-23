package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/provider"
	"glitched/internal/transport"
)

// providersFixture has a's engine report the providers of state: one
// (Claude Code), two (and Antigravity) or four (and a signed-out Codex
// and a Grok that isn't installed).
func providersFixture(a *app, state string) {
	home := homeDir()
	claudeModels := []provider.Model{
		{ID: "claude-opus-5-5", Name: "Opus 5.5", Description: "Most capable, for the hardest work", Efforts: []provider.Effort{{ID: "low"}, {ID: "medium"}, {ID: "high"}, {ID: "xhigh"}, {ID: "max"}}},
		{ID: "claude-sonnet-5-5", Name: "Sonnet 5.5", Description: "Fast and capable, for everyday work", Efforts: []provider.Effort{{ID: "low"}, {ID: "medium"}, {ID: "high"}, {ID: "max"}}},
		{ID: "claude-haiku-5-5", Name: "Haiku 5.5", Description: "Fastest, for quick answers"},
	}
	agyModels := []provider.Model{
		{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash", Efforts: []provider.Effort{{ID: "low", Label: "Low"}, {ID: "medium", Label: "Medium"}, {ID: "high", Label: "High"}}},
		{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6 (Thinking)"},
		{ID: "claude-opus-4-6", Name: "Claude Opus 4.6 (Thinking)"},
		{ID: "gpt-oss-120b-medium", Name: "GPT-OSS 120B (Medium)"},
	}
	a.models.reported = map[string][]provider.Model{"claude": claudeModels}
	a.models.served = map[string]transport.ProviderInfo{
		"claude": {Name: "claude", Info: provider.Info{Name: "claude", Version: "2.1.317", Models: claudeModels, Path: filepath.Join(home, ".local/bin/claude")}},
	}
	if state == "one" {
		return
	}
	a.models.reported["antigravity"] = agyModels
	a.models.served["antigravity"] = transport.ProviderInfo{Name: "antigravity", Info: provider.Info{Name: "Antigravity", Version: "1.5.14", Models: agyModels, Path: filepath.Join(home, ".local/bin/agy")}}
	if state == "two" {
		return
	}
	a.models.reported["codex"] = []provider.Model{{ID: "gpt-5.5-codex", Name: "GPT-5.5 Codex"}, {ID: "gpt-5.5", Name: "GPT-5.5"}}
	a.models.served["codex"] = transport.ProviderInfo{Name: "codex", State: transport.ProviderSignedOut, Error: "Codex is signed out. Run codex login in Terminal to sign in."}
	a.models.reported["grok"] = []provider.Model{}
	a.models.served["grok"] = transport.ProviderInfo{Name: "grok", State: transport.ProviderNotInstalled, Error: "Grok is not installed.\nInstall its CLI with: curl -fsSL https://x.ai/cli/install.sh | bash"}
}

// TestProvidersShot writes PNGs of Settings › Providers to the folder
// PRSHOT names, one per state, theme and window size, in the layout
// PVARIANT names (A or B).
func TestProvidersShot(t *testing.T) {
	out := os.Getenv("PRSHOT")
	if out == "" {
		t.Skip()
	}
	os.MkdirAll(out, 0o755)
	if v := os.Getenv("PVARIANT"); v != "" {
		providersVariant = v
	}
	for _, state := range []string{"one", "two", "four"} {
		a := offline(t)
		providersFixture(a, state)
		a.model, a.effort = modelKey{"claude", "claude-opus-5-5"}, "high"
		tt := newTester(t, a, 1320, 860)
		settleTester(tt)
		a.router.Push("/settings/providers")
		for _, v := range []struct {
			name  string
			w, h  int
			light bool
			open  bool
		}{
			{"dark", 1320, 860, false, false},
			{"light", 1320, 860, true, false},
			{"dark-open", 1320, 860, false, true},
			{"light-open", 1320, 860, true, true},
			{"dark-narrow", 760, 860, false, false},
			{"dark-narrow-open", 760, 860, false, true},
			{"light-narrow-open", 760, 860, true, true},
		} {
			clear(a.providersOpen)
			a.providersTab = 0
			if v.open {
				a.providersOpen["claude"] = true
				if state == "four" {
					a.providersOpen["claude"], a.providersOpen["codex"] = false, true
					a.providersTab = 2
				}
			}
			a.settings.theme = darkTheme.name
			if v.light {
				a.settings.theme = lightTheme.name
			}
			tt.SetSize(v.w, v.h)
			settleTester(tt)
			tt.Advance(time.Second)
			f, err := os.Create(filepath.Join(out, fmt.Sprintf("%s-%s.png", state, v.name)))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, tt.Image())
			f.Close()
		}
	}
}
