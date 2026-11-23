package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/client"
	"glitched/internal/cliupdate"
	"glitched/internal/provider"
	"glitched/internal/transport"
)

// TestCLIUpdatesShot writes PNGs of the CLI updates, in Settings ›
// Providers and at the foot of the sidebar, to the folder CLISHOT names:
// up to date, an update available, one updating, one failed, and one
// Glitched won't run.
func TestCLIUpdatesShot(t *testing.T) {
	out := os.Getenv("CLISHOT")
	if out == "" {
		t.Skip()
	}
	os.MkdirAll(out, 0o755)
	home := "/Users/you"
	claude := func(installed, latest string) transport.ProviderInfo {
		newer, _ := cliupdate.Newer(installed, latest)
		return transport.ProviderInfo{Name: "claude", Info: provider.Info{Name: "claude", Version: installed, Path: filepath.Join(home, ".local/bin/claude")}, Update: &cliupdate.Status{
			Installed: installed, Latest: latest, Available: newer, Command: []string{filepath.Join(home, ".local/bin/claude"), "update"},
		}}
	}
	for _, state := range []string{"current", "available", "updating", "failed", "manual"} {
		h := start(t, tempDir(t))
		allAnswered(h)
		h.a.model, h.a.effort = modelKey{"claude", "claude-opus-5-5"}, "high"
		h.a.updateRunner = runs(func(e *client.Env, name string, whenIdle bool, done func(transport.UpdateResult, error)) {
			if state == "failed" {
				done(transport.UpdateResult{Error: "claude update failed: exit status 1.", Output: "Checking for updates...\nError: EACCES: permission denied, rename '/opt/claude/versions/2.1.331'", Provider: h.a.models.served[name]}, nil)
			}
		})
		h.a.models.served["claude"] = claude("2.1.330", "2.1.331")
		if state == "current" {
			h.a.models.served["claude"] = claude("2.1.331", "2.1.331")
		}
		if state == "manual" {
			u := h.a.models.served["claude"].Update
			u.Manual = "Claude Code is installed where only an administrator can change it."
			u.Command = []string{"sudo", "npm", "install", "-g", "@anthropic-ai/claude-code@latest"}
		}
		h.a.models.served["codex"] = transport.ProviderInfo{Name: "codex", Info: provider.Info{Name: "Codex", Version: "0.171.0", Path: filepath.Join(home, ".local/bin/codex")}, Update: &cliupdate.Status{Installed: "0.171.0", Latest: "0.171.0"}}
		shoot := func(name string) {
			h.tt.Advance(time.Second)
			f, err := os.Create(filepath.Join(out, state+"-"+name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, h.tt.Image())
			f.Close()
		}
		for _, theme := range []string{"dark", "light"} {
			h.a.settings.theme = darkTheme.name
			if theme == "light" {
				h.a.settings.theme = lightTheme.name
			}
			clear(h.a.providersOpen)
			clear(h.a.clis.runs)
			h.a.router.Push("/")
			h.tt.Advance(time.Second)
			if state != "current" {
				h.click("Update Claude")
				if state == "updating" || state == "failed" {
					h.tt.Advance(time.Second)
					h.click("Update")
				}
			}
			shoot("sidebar-" + theme)
			if state != "current" {
				h.click("Update Claude")
			}
			openProviders(h, "claude")
			shoot("providers-" + theme)
		}
	}
}
