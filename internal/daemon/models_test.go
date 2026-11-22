package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// The engine's socket says what each provider it serves runs, and why a
// provider couldn't say. The fake that plays codex, gemini and grok for a
// test runs nothing.
func TestTheSocketSaysWhatModelsEachProviderRuns(t *testing.T) {
	dir := stateDir(t)
	cfg, err := Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	runs := fake.New(nil)
	runs.Models = []provider.Model{{ID: "m-1", Name: "One", Description: "The first", Efforts: []provider.Effort{{ID: "low"}, {ID: "max"}}}}
	cfg.Providers["claude"] = runs
	cfg.Providers["gemini"] = undescribed{fake.New(nil)}
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	local := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer local.Close()
	served, err := local.Providers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range served {
		names = append(names, p.Name)
		switch {
		case p.Name == "gemini":
			if !strings.Contains(p.Error, "no answer") || len(p.Info.Models) != 0 {
				t.Errorf("gemini, which can't say, says %+v, %q", p.Info, p.Error)
			}
		case p.Error != "":
			t.Errorf("%s couldn't say: %s", p.Name, p.Error)
		case p.Name == "claude" && (len(p.Info.Models) != 1 || p.Info.Models[0].ID != "m-1" || len(p.Info.Models[0].Efforts) != 2):
			t.Errorf("claude runs %+v, want its one model and its two efforts", p.Info.Models)
		case p.Name != "claude" && len(p.Info.Models) != 0:
			t.Errorf("%s, the fake, runs %+v", p.Name, p.Info.Models)
		}
	}
	if want := []string{"claude", "codex", "fake", "gemini", "grok"}; !slices.Equal(names, want) {
		t.Errorf("the socket says the engine serves %q, want %q", names, want)
	}
}

// A real build's engine serves each ACP agent whose binary it finds, as
// the user installed it or engine.json's path names it, beside Claude
// Code; one it can't find, it doesn't offer.
func TestWithAgentsServesTheInstalledACPAgents(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	serves := func(agyPath string) []string {
		cfg, err := Defaults(stateDir(t))
		if err != nil {
			t.Fatal(err)
		}
		cfg.AgyPath = agyPath
		cfg.WithAgents()
		return cfg.Providers.Names()
	}
	if got := serves(""); !slices.Equal(got, []string{"claude"}) {
		t.Errorf("with no agy, serves %q", got)
	}
	agy := filepath.Join(home, ".local", "bin", "agy")
	if err := os.MkdirAll(filepath.Dir(agy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agy, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := serves(""); !slices.Equal(got, []string{"antigravity", "claude"}) {
		t.Errorf("with agy in ~/.local/bin, serves %q", got)
	}
	if got := serves(filepath.Join(bin, "no-agy")); !slices.Equal(got, []string{"claude"}) {
		t.Errorf("with agy_path naming nothing, serves %q", got)
	}
}

// undescribed is a provider that can't say what it runs.
type undescribed struct{ provider.Adapter }

func (undescribed) Describe(context.Context) (provider.Info, error) {
	return provider.Info{}, errors.New("no answer")
}

// A real build's engine serves Claude Code and nothing else: no thread
// gets the fake's canned turns.
func TestWithClaudeServesOnlyClaude(t *testing.T) {
	cfg, err := Defaults(stateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.WithClaude()
	if names := cfg.Providers.Names(); !slices.Equal(names, []string{"claude"}) {
		t.Errorf("a real build serves %q, want claude alone", names)
	}
}
