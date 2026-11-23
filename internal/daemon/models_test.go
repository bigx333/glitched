package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/acp"
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
	served, err := local.Providers(context.Background(), false)
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

// A real build's engine serves each ACP agent beside Claude Code, found as
// the user installed it or engine.json's path names it. One it can't find
// is served too, and says how to install it.
func TestWithAgentsServesEveryACPAgent(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	agyOf := func(agyPath string) *acp.Adapter {
		cfg, err := Defaults(stateDir(t))
		if err != nil {
			t.Fatal(err)
		}
		cfg.Launch = map[string]provider.Launch{"antigravity": {Path: agyPath}}
		cfg.WithAgents()
		if got := cfg.Providers.Names(); !slices.Equal(got, []string{"antigravity", "claude", "codex"}) {
			t.Errorf("with agy_path %q, serves %q", agyPath, got)
		}
		return cfg.Providers["antigravity"].(*acp.Adapter)
	}
	missing := func(agyPath string) {
		_, err := agyOf(agyPath).Describe(context.Background())
		if !errors.Is(err, provider.ErrNotInstalled) || !strings.HasSuffix(err.Error(), acp.Antigravity.Install) {
			t.Errorf("with agy_path %q and no agy, Describe = %v, want not installed with how to install it", agyPath, err)
		}
	}
	missing("")
	missing(filepath.Join(bin, "no-agy"))
	agy := filepath.Join(home, ".local", "bin", "agy")
	if err := os.MkdirAll(filepath.Dir(agy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agy, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := agyOf("").Placement().Program(provider.Launch{}); got.Path != agy || err != nil {
		t.Errorf("with agy in ~/.local/bin, runs %q (%v)", got, err)
	}
}

// needing is a provider whose user must act before it runs.
type needing struct {
	provider.Adapter
	kind   error
	forgot *atomic.Bool
}

func (n needing) Describe(context.Context) (provider.Info, error) {
	return provider.Info{}, provider.Needs{Kind: n.kind, Help: "do this first"}
}

func (n needing) Forget() { n.forgot.Store(true) }

// The socket says what a provider's user must do before it runs: install
// it, or sign in, in the provider's words. A fresh ask has each provider
// that keeps its answer ask its program again.
func TestTheSocketSaysWhatAProviderNeeds(t *testing.T) {
	var out, absent atomic.Bool
	cfg, err := Defaults(stateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := fake.New(nil)
	cfg.Providers = provider.Mux{
		"fake":   fake,
		"out":    needing{fake, provider.ErrSignedOut, &out},
		"absent": needing{fake, provider.ErrNotInstalled, &absent},
		"broken": undescribed{fake},
	}
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	local := transport.NewEndpointClient(Socket(cfg.Dir), api.ID{})
	defer local.Close()
	for _, fresh := range []bool{false, true} {
		served, err := local.Providers(context.Background(), fresh)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]transport.ProviderInfo{
			"absent": {State: transport.ProviderNotInstalled, Error: "do this first"},
			"out":    {State: transport.ProviderSignedOut, Error: "do this first"},
			"broken": {Error: "no answer"},
			"fake":   {},
		}
		for _, p := range served {
			if w := want[p.Name]; p.State != w.State || p.Error != w.Error {
				t.Errorf("fresh %v: %s says %q, %q; want %q, %q", fresh, p.Name, p.State, p.Error, w.State, w.Error)
			}
		}
		if out.Load() != fresh || absent.Load() != fresh {
			t.Errorf("after an ask with fresh %v, the providers forgot their answers: %v, %v", fresh, out.Load(), absent.Load())
		}
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
