package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/plugins"
	"glitched/internal/provider"
)

// placing is a provider whose program a launch places, as the real
// adapters', which answers --version without running anything.
type placing struct {
	provider.Adapter
	mu     sync.Mutex
	placed []provider.Launch
}

func (p *placing) Placement() provider.Placement {
	return provider.Placement{Names: []string{"glitched-demo-cli"}, HomeVar: "CLAUDE_CONFIG_DIR", DefaultHome: "~/.claude", Reserved: []provider.Reserved{{Flags: []string{"--model"}}}}
}

func (p *placing) Place(l provider.Launch) {
	p.mu.Lock()
	p.placed = append(p.placed, l)
	p.mu.Unlock()
}

func (p *placing) Version(context.Context, provider.Program) (string, error) { return "2.1.330", nil }

func (p *placing) Command(prog provider.Program) []string {
	return append([]string{prog.Path, "-p"}, prog.Args...)
}

func (p *placing) last() []provider.Launch {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.Launch{}, p.placed...)
}

// demoProgram is a program for a launch to name, at a path with a space.
func demoProgram(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "my tools")
	os.MkdirAll(dir, 0o755)
	bin := filepath.Join(dir, "demo cli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func launchesOn(t *testing.T, dir string) map[string]provider.Launch {
	t.Helper()
	cfg, err := daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Launch
}

// Claude's row on this machine says where its program is and how it
// starts, refuses a flag Glitched sets, and saves a launch to this
// machine's engine.json and its running engine at once.
func TestAProvidersRowSetsHowItsProgramStarts(t *testing.T) {
	dir := tempDir(t)
	p := &placing{}
	h := startWith(t, dir, func(c *daemon.Config) {
		engineReports("claude", provider.Model{ID: "claude-opus-5-5", Name: "Opus"})(c)
		p.Adapter = c.Providers["claude"]
		c.Providers["claude"] = p
	})
	allAnswered(h)
	openProviders(h, "claude")
	h.until("the launch settings", func() bool { return h.tt.HasText("CLAUDE_CONFIG_DIR") })
	if h.tt.HasText("Machine") {
		t.Errorf("one machine, and the row offers a choice of machines")
	}
	k := launchKey{h.local().ID, "claude"}
	bin, home := demoProgram(t), t.TempDir()

	h.a.launches.drafts[k] = provider.Launch{Path: bin, Home: home, Args: "--model opus"}
	h.tt.Frame()
	h.click("Save")
	h.until("the refusal", func() bool { return h.tt.HasText("--model is set by Glitched itself") })
	if l := launchesOn(t, dir); l != nil || len(p.last()) != 0 {
		t.Fatalf("a refused launch was kept: %+v, placed %+v", l, p.last())
	}
	if !h.tt.HasText("Threads that ran in the old folder won't find their sessions in this one: each starts a new conversation without its earlier context.") {
		t.Errorf("a new folder, and the row doesn't warn of the threads' sessions: %q", h.tt.Texts())
	}

	want := provider.Launch{Path: bin, Home: home, Args: `--chrome --add-dir "/tmp/a b"`}
	h.a.launches.drafts[k] = want
	h.tt.Frame()
	h.click("Save")
	h.until("the launch kept", func() bool { _, editing := h.a.launches.drafts[k]; return !editing && !h.a.launches.saving[k] })
	if got := launchesOn(t, dir)["claude"]; got != want {
		t.Errorf("engine.json keeps %+v, want %+v", got, want)
	}
	if got := p.last(); len(got) != 1 || got[0] != want {
		t.Errorf("the running engine placed %+v", got)
	}
	line := "CLAUDE_CONFIG_DIR=" + home + " '" + bin + "' -p --chrome --add-dir '/tmp/a b'"
	h.until("the command", func() bool { return h.tt.HasText(line) })
	if !h.tt.HasText("Saved settings apply to the next launch on this machine. Running turns keep how they started.") {
		t.Errorf("the row doesn't say when a change applies: %q", h.tt.Texts())
	}
}

// With another machine paired, the row asks which machine's settings it
// shows, and saves to that machine's engine alone.
func TestARemoteMachinesRowSetsItsOwnLaunch(t *testing.T) {
	rdir := tempDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	rp := &placing{}
	remote, err := daemon.Start(context.Background(), daemon.Config{Dir: rdir, Hub: true, Listen: []string{"127.0.0.1:0"}, Providers: provider.Mux{"claude": rp}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remote.Close() })
	dir := tempDir(t)
	p := &placing{}
	h := startWith(t, dir, func(c *daemon.Config) {
		engineReports("claude", provider.Model{ID: "claude-opus-5-5", Name: "Opus"})(c)
		p.Adapter = c.Providers["claude"]
		c.Providers["claude"] = p
	})
	rp.Adapter = p.Adapter
	if err := h.addServer(client.Server{Label: "devbox", Addr: remote.Addrs()[0].String(), Token: token}); err != nil {
		t.Fatal(err)
	}
	h.until("devbox online", func() bool { return len(h.a.launchMachines()) == 2 })
	allAnswered(h)
	openProviders(h, "claude")
	h.until("the machine choice", func() bool { return h.tt.HasText("devbox") && h.tt.HasText("This machine") })
	h.click("devbox")
	h.until("devbox's launch", func() bool {
		return h.a.launchEnv().Name == "devbox" && h.tt.HasText("Saved settings apply to the next launch on devbox. Running turns keep how they started.")
	})

	want := provider.Launch{Path: demoProgram(t), Args: "--chrome"}
	k := launchKey{h.a.launchEnv().ID, "claude"}
	h.a.launches.drafts[k] = want
	h.tt.Frame()
	h.click("Save")
	h.until("the launch kept", func() bool { _, editing := h.a.launches.drafts[k]; return !editing && !h.a.launches.saving[k] })
	if got := launchesOn(t, rdir)["claude"]; got != want {
		t.Errorf("devbox's engine.json keeps %+v, want %+v", got, want)
	}
	if got := rp.last(); len(got) != 1 || got[0] != want {
		t.Errorf("devbox's engine placed %+v", got)
	}
	if l := launchesOn(t, dir); l != nil || len(p.last()) != 0 {
		t.Errorf("this machine's engine took devbox's launch: %+v, %+v", l, p.last())
	}
}

// A plugin has no way to set how a provider's program starts: no effect
// names it, and the settings a plugin sets are the app's own.
func TestAPluginCantSetAProvidersLaunch(t *testing.T) {
	a := offline(t)
	b, _ := json.Marshal(map[string]any{"key": "providers", "value": map[string]any{"claude": map[string]any{"path": "/bin/sh"}}})
	if _, err := a.setAppSetting(b); err == nil || !strings.Contains(err.Error(), "no setting a plugin sets") {
		t.Errorf("app.settings.set providers: %v, want it refused", err)
	}
	for _, effect := range []string{"provider.launch", "provider.launches", "providers.launch"} {
		if _, err := a.pluginEffect("demo", plugins.Effect{Type: effect, Args: json.RawMessage(`{"provider":"claude","launch":{"path":"/bin/sh"}}`)}); err == nil {
			t.Errorf("a plugin's %s was taken", effect)
		}
	}
}
