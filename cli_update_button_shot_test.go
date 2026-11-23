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

// TestCLIUpdateButtonShot writes PNGs of the sidebar's CLI update button
// to the folder CLIBUTTONSHOT names: one CLI behind, and three on two
// machines, each as the button, its tooltip and its list, in dark and
// light; and the three while one updates, one failed and one needs the
// user, as the button and its tooltip.
func TestCLIUpdateButtonShot(t *testing.T) {
	out := os.Getenv("CLIBUTTONSHOT")
	if out == "" {
		t.Skip()
	}
	os.MkdirAll(out, 0o755)
	home := "/Users/you"
	cli := func(name, installed, latest string) transport.ProviderInfo {
		newer, _ := cliupdate.Newer(installed, latest)
		path := filepath.Join(home, ".local/bin", name)
		return transport.ProviderInfo{Name: name, Info: provider.Info{Name: name, Version: installed, Path: path}, Update: &cliupdate.Status{
			Installed: installed, Latest: latest, Available: newer, Command: []string{path, "update"},
		}}
	}
	for _, scene := range []string{"one", "three", "three-updating", "three-failed", "three-manual"} {
		dir := tempDir(t)
		several := scene != "one"
		if several {
			listServers(t, dir, client.Server{Label: "build-box", Addr: "missing port"})
		}
		h := start(t, dir)
		allAnswered(h)
		h.a.model, h.a.effort = modelKey{"claude", "claude-opus-5-5"}, "high"
		h.a.updateRunner = runs(func(*client.Env, string, bool, func(transport.UpdateResult, error)) {})
		h.a.models.served["claude"] = cli("claude", "2.1.330", "2.1.331")
		h.a.models.served["codex"] = cli("codex", "0.171.0", "0.171.0")
		local := h.a.backend.Envs()[0]
		if several {
			h.a.models.served["codex"] = cli("codex", "0.170.0", "0.171.0")
			h.local().Name = "my-mac"
			remote := h.a.backend.Envs()[1]
			remote.Status = client.Online{}
			agy := cli("agy", "1.5.14", "1.5.15")
			if scene == "three-manual" {
				agy.Update.Manual = "agy is installed where only an administrator can change it."
			}
			h.a.clis.served[remote], h.a.clis.engine[remote] = []transport.ProviderInfo{agy}, remote.ID
		}
		switch scene {
		case "three-updating":
			h.a.clis.runs[cliKey{local, "claude"}] = &cliRun{running: true}
		case "three-failed":
			h.a.clis.runs[cliKey{local, "codex"}] = &cliRun{failed: "codex update failed: exit status 1."}
		}
		shoot := func(name string) {
			h.tt.Advance(time.Second)
			f, err := os.Create(filepath.Join(out, scene+"-"+name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, h.tt.Image())
			f.Close()
		}
		button := func() (x, y float32) {
			for _, s := range []string{"Agent CLI updates", "Update Claude", "Updates for 3 CLIs"} {
				if r, ok := h.tt.Find(s); ok {
					return r.X + r.W/2, r.Y + r.H/2
				}
			}
			t.Fatalf("no CLI update button; the window shows %q", h.tt.Texts())
			return 0, 0
		}
		themes := []string{"dark", "light"}
		if scene != "one" && scene != "three" {
			themes = themes[:1]
		}
		for _, theme := range themes {
			h.a.settings.theme = darkTheme.name
			if theme == "light" {
				h.a.settings.theme = lightTheme.name
			}
			h.a.router.Push("/")
			h.tt.Advance(time.Second)
			h.tt.Move(1000, 300)
			shoot("button-" + theme)
			bx, by := button()
			h.tt.Move(bx, by)
			h.tt.Advance(2 * time.Second)
			shoot("tooltip-" + theme)
			if scene == "one" || scene == "three" {
				h.tt.ClickAt(bx, by)
				shoot("list-" + theme)
				h.tt.ClickAt(bx, by)
				h.tt.Move(1000, 300)
			}
		}
	}
}
