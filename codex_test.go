package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/daemon"
	"glitched/internal/provider/codex"
	"glitched/internal/provider/codex/codextest"
)

// withCodex has the engine run Codex threads through the Codex adapter,
// the test binary playing codex app-server in scenario.
func withCodex(t *testing.T, scenario string) func(*daemon.Config) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(codextest.Scenario, scenario)
	t.Setenv(codextest.Log, filepath.Join(t.TempDir(), "wire.jsonl"))
	return func(cfg *daemon.Config) {
		cfg.Providers[codex.Provider] = &codex.Adapter{Path: self}
	}
}

// The picker offers the models Codex reports under Codex, its default
// first, with each one's efforts; a thread on one streams Codex's text
// and its command, whose approval is Glitched's own card, naming Codex.
func TestACodexThread(t *testing.T) {
	h := startWith(t, tempDir(t), withCodex(t, "command"))
	allAnswered(h)
	h.until("Codex's models", func() bool {
		m := h.a.models.reported["codex"]
		return len(m) == 2 && m[0].ID == "gpt-6.2-sol"
	})
	h.a.model = modelKey{"codex", "gpt-6.2-sol"}
	h.tt.Frame()
	h.click("Model")
	pickerSettle(t, h)
	for _, want := range []string{"Codex", "GPT-6.2 Sol", "GPT-6 Luna"} {
		if !h.tt.HasText(want) {
			t.Fatalf("the picker shows no %q: %q", want, h.tt.Texts())
		}
	}
	var levels []string
	for _, e := range h.a.efforts() {
		levels = append(levels, e.id)
	}
	if len(levels) != 4 || levels[0] != "low" || levels[3] != "xhigh" {
		t.Fatalf("GPT-6.2 Sol's efforts %q, want Codex's", levels)
	}
	shoot(t, h, "codex-picker")
	h.click("GPT-6 Luna")
	pickerSettle(t, h)

	sendNew(h, "List the files.")
	h.until("the card", func() bool { return h.tt.HasText("Codex wants to run") })
	if got := h.a.current().View.Model; got.Provider != "codex" || got.Model != "gpt-6-luna" {
		t.Fatalf("the thread runs %+v", got)
	}
	for range 50 {
		h.tt.Advance(100 * time.Millisecond)
	}
	for _, want := range []string{"ls -a", "list the files", "Allow once", "Deny"} {
		if !h.tt.HasText(want) {
			t.Fatalf("the thread shows no %q: %q", want, h.tt.Texts())
		}
	}
	shoot(t, h, "codex-approval")
	h.click("Allow once")
	h.until("the turn's end", func() bool { return !h.tt.HasText("Stop") })
	for range 50 {
		h.tt.Advance(100 * time.Millisecond)
	}
	if !h.tt.HasText("decision accept") {
		t.Fatalf("the thread shows no answer: %q", h.tt.Texts())
	}
	shoot(t, h, "codex-answered")
}

// Settings › Providers has a row for Codex from Codex's own answer: its
// version and the program Glitched runs, or, signed out, how to sign in.
func TestCodexsRowInSettings(t *testing.T) {
	for scenario, want := range map[string][]string{
		"hi":         {"Codex", "Ready", "v0.171.0", "2 models", "The program Glitched runs."},
		"signed-out": {"Codex", "Signed out", "Sign in to Codex", "Codex is signed out. Run codex login in Terminal to sign in.", "Check again"},
	} {
		t.Run(scenario, func(t *testing.T) {
			h := startWith(t, tempDir(t), withCodex(t, scenario))
			allAnswered(h)
			h.until("Codex's answer", func() bool {
				info, ok := h.a.models.served["codex"]
				return ok && (info.State != "" || len(info.Info.Models) > 0)
			})
			openProviders(h, "codex")
			for _, s := range want {
				if !h.tt.HasText(s) {
					t.Errorf("the page doesn't show %q: %q", s, h.tt.Texts())
				}
			}
			shoot(t, h, "codex-settings-"+scenario)
		})
	}
}
