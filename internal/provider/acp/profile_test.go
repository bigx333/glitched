package acp

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"glitched/internal/provider"
)

func TestCustomProfileToProfile(t *testing.T) {
	p, err := (CustomProfile{
		Provider: "my-acp",
		Name:     "My ACP",
		Binary:   "/usr/local/bin/my-acp",
		Args:     []string{"--acp"},
		Env:      map[string]string{"FOO": "bar"},
	}).ToProfile()
	if err != nil {
		t.Fatal(err)
	}
	if p.Provider != "my-acp" || p.Name != "My ACP" || p.Binaries[0] != "/usr/local/bin/my-acp" || p.Env["FOO"] != "bar" || p.Env["BROWSER"] != "true" {
		t.Errorf("unexpected profile: %+v", p)
	}

	// An empty name is the id. No arguments stay none, for an agent that
	// speaks ACP as it starts, as codex-acp does.
	def, err := (CustomProfile{Provider: "tool2", Binary: "tool2"}).ToProfile()
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "tool2" || len(def.Args) != 0 {
		t.Errorf("defaulted profile = %+v", def)
	}

	for _, bad := range []CustomProfile{
		{Provider: "", Binary: "bin"},
		{Provider: "BadCaps", Binary: "bin"},
		{Provider: "claude", Binary: "bin"},
		{Provider: "gemini", Binary: "bin"},
		{Provider: "good-id", Binary: ""},
	} {
		if _, err := bad.ToProfile(); err == nil {
			t.Errorf("expected error for %+v", bad)
		}
	}
}

// A custom agent with no arguments starts with none, and one named from
// the home folder starts from there.
func TestACustomAgentStartsAsConfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin", "codex-acp")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := CustomProfile{Provider: "codex-acp", Binary: "~/bin/codex-acp"}.ToProfile()
	if err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Profile: p}
	prog, err := a.program()
	if err != nil {
		t.Fatalf("a custom agent in ~/bin: %v", err)
	}
	if prog.Path != bin || len(slices.Concat(p.Args, prog.Args)) != 0 {
		t.Errorf("starts %s with %q, want %s with no arguments", prog.Path, slices.Concat(p.Args, prog.Args), bin)
	}
	if _, err := (&Adapter{Profile: Profile{Name: "x", Binaries: []string{"~/bin/missing"}}}).program(); !errors.Is(err, provider.ErrNotInstalled) {
		t.Errorf("a missing one: %v, want not installed", err)
	}
}
