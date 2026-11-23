package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/transport"
)

// TestProviderLaunchShot writes PNGs of Claude's expanded row on Settings
// › Providers to the folder PRSHOT names: as found, set up, and set up
// wrong.
func TestProviderLaunchShot(t *testing.T) {
	out := os.Getenv("PRSHOT")
	if out == "" {
		t.Skip()
	}
	os.MkdirAll(out, 0o755)
	ad := &claude.Adapter{}
	pl := ad.Placement()
	info := func(l provider.Launch, path string, env []string, args []string) transport.LaunchInfo {
		return transport.LaunchInfo{Provider: "claude", Launch: l, HomeVar: pl.HomeVar, DefaultHome: pl.DefaultHome, Reserved: pl.Flags(),
			Path: path, Version: "2.1.317 (Claude Code)", Env: env, Command: ad.Command(provider.Program{Path: path, Env: env, Args: args})}
	}
	custom := provider.Launch{Path: "/opt/claude/bin/claude", Home: "/srv/claude-work", Args: `--chrome --add-dir "/srv/shared docs"`}
	for _, state := range []string{"defaults", "custom", "invalid"} {
		h := startWith(t, tempDir(t), func(*daemon.Config) {})
		a := h.a
		providersFixture(a, "two")
		a.models.next = time.Now().Add(time.Hour)
		a.model, a.effort = modelKey{"claude", "claude-opus-5-5"}, "high"
		a.providersOpen["claude"] = true
		e := a.backend.Envs()[0]
		h.until("the engine answers", func() bool { return !e.ID.IsZero() })
		k := launchKey{e.ID, "claude"}
		s := a.launchState()
		r := &launchRead{at: time.Now().Add(time.Hour), infos: map[string]transport.LaunchInfo{}}
		s.read[e.ID] = r
		switch state {
		case "defaults":
			r.infos["claude"] = info(provider.Launch{}, "/usr/local/bin/claude", nil, nil)
		case "custom":
			r.infos["claude"] = info(custom, custom.Path, []string{"CLAUDE_CONFIG_DIR=/srv/claude-work"}, []string{"--chrome", "--add-dir", "/srv/shared docs"})
		case "invalid":
			r.infos["claude"] = info(provider.Launch{}, "/usr/local/bin/claude", nil, nil)
			draft := provider.Launch{Path: "/opt/claude/bin/claud", Home: "/srv/claude-wrok", Args: "--permission-mode bypassPermissions"}
			s.drafts[k] = draft
			s.refused[k] = refusedLaunch{draft, provider.LaunchError{Path: "there is no file at /opt/claude/bin/claud", Home: "there is no folder at /srv/claude-wrok", Args: "--permission-mode is set by Glitched itself"}}
		}
		tt := h.tt
		tt.SetSize(1320, 1500)
		settleTester(tt)
		a.router.Push("/settings/providers")
		settleTester(tt)
		tt.Advance(time.Second)
		f, err := os.Create(filepath.Join(out, state+".png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, tt.Image())
		f.Close()
	}
}
