package main

import (
	"context"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/acp"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

func servingCustom(c *daemon.Config) {
	for _, p := range c.CustomACP {
		c.Providers[p.Provider] = reporting{fake.New(nil), []provider.Model{{ID: p.Provider + "-model", Name: "Own"}}}
	}
}

func customACP(t *testing.T, dir string) ([]acp.CustomProfile, map[string]provider.Launch) {
	t.Helper()
	cfg, err := daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.CustomACP, cfg.Launch
}

func setUp(t *testing.T, dir string, change func(*daemon.Config)) {
	t.Helper()
	if _, err := daemon.Edit(dir, func(cfg *daemon.Config) error { change(cfg); return nil }); err != nil {
		t.Fatal(err)
	}
}

func openCustomACP(h *harness) {
	h.t.Helper()
	ticking(h.t, h)
	openProviders(h)
	h.until("the custom ACP card to read the engine", func() bool { return !h.a.backend.Machine().Read.IsZero() })
	h.reveal("Add ACP provider")
}

func TestCustomACPCardShowsWhatEngineJSONHolds(t *testing.T) {
	dir := tempDir(t)
	setUp(t, dir, func(cfg *daemon.Config) {
		cfg.CustomACP = []acp.CustomProfile{
			{Provider: "mine", Name: "Mine", Binary: "/opt/mine", Args: []string{"acp", "a b"}},
			{Provider: "mine", Binary: "/opt/other"},
		}
	})
	h := start(t, dir)
	openCustomACP(h)
	h.until("the card's providers", func() bool { return h.tt.HasText("/opt/mine acp 'a b'") && h.tt.HasText("/opt/other") })
	if h.tt.HasText("No custom ACP providers configured. Add any ACP-compatible coding agent CLI below.") {
		t.Errorf("the card says it has none: %q", h.tt.Texts())
	}
}

func TestCustomACPAddSavesShellArgumentsAndAsksTheEngineAgain(t *testing.T) {
	dir := tempDir(t)
	h := startWith(t, dir, servingCustom)
	allAnswered(h)
	openCustomACP(h)
	h.a.customACP = acp.CustomProfile{Provider: "mine", Binary: "/opt/mine"}
	h.a.customACPArgs = `acp --name "a b"`
	h.tt.Frame()
	h.click("Add ACP provider")
	want := []acp.CustomProfile{{Provider: "mine", Binary: "/opt/mine", Args: []string{"acp", "--name", "a b"}}}
	if got, _ := customACP(t, dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("engine.json holds %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(h.a.customACP, acp.CustomProfile{}) || h.a.customACPArgs != "" || h.a.customACPErr != "" {
		t.Errorf("after the add, the form holds %+v %q, says %q", h.a.customACP, h.a.customACPArgs, h.a.customACPErr)
	}
	h.until("the picker to offer the new provider's model", func() bool {
		return slices.ContainsFunc(h.a.offeredModels(), func(m modelInfo) bool { return m.key == modelKey{"mine", "mine-model"} })
	})
	h.until("the card's new row", func() bool { return h.tt.HasText("/opt/mine acp --name 'a b'") })
}

func TestCustomACPAddRefusesArgumentsLeftOpen(t *testing.T) {
	dir := tempDir(t)
	h := start(t, dir)
	openCustomACP(h)
	h.a.customACP = acp.CustomProfile{Provider: "mine", Binary: "/opt/mine"}
	h.a.customACPArgs = `--name "a b`
	h.tt.Frame()
	h.click("Add ACP provider")
	if got, _ := customACP(t, dir); len(got) != 0 {
		t.Errorf("engine.json holds %+v", got)
	}
	if !strings.Contains(h.a.customACPErr, "left open") || h.a.customACPArgs != `--name "a b` {
		t.Errorf("the card says %q, with the arguments %q", h.a.customACPErr, h.a.customACPArgs)
	}
}

func TestCustomACPAddChecksEngineJSONNotThePagesSnapshot(t *testing.T) {
	dir := tempDir(t)
	h := start(t, dir)
	openCustomACP(h)
	setUp(t, dir, func(cfg *daemon.Config) {
		cfg.CustomACP = []acp.CustomProfile{{Provider: "mine", Binary: "/opt/mine"}}
	})
	h.a.customACP = acp.CustomProfile{Provider: "mine", Binary: "/opt/two"}
	if err := h.tt.Click("Add ACP provider"); err != nil {
		t.Fatal(err)
	}
	if got, _ := customACP(t, dir); !reflect.DeepEqual(got, []acp.CustomProfile{{Provider: "mine", Binary: "/opt/mine"}}) {
		t.Errorf("engine.json holds %+v, want the one it had", got)
	}
	if !strings.Contains(h.a.customACPErr, `"mine" already exists`) {
		t.Errorf("the card says %q", h.a.customACPErr)
	}
}

func TestCustomACPRemoveDropsItsLaunchSettings(t *testing.T) {
	dir := tempDir(t)
	setUp(t, dir, func(cfg *daemon.Config) {
		cfg.CustomACP = []acp.CustomProfile{{Provider: "mine", Binary: "/opt/mine"}}
		cfg.Launch = map[string]provider.Launch{"mine": {Path: "/opt/old"}, "claude": {Args: "--verbose"}}
	})
	h := start(t, dir)
	openCustomACP(h)
	h.reveal("Remove")
	h.click("Remove")
	h.until("engine.json without the provider", func() bool { got, _ := customACP(t, dir); return len(got) == 0 })
	if _, launch := customACP(t, dir); !reflect.DeepEqual(launch, map[string]provider.Launch{"claude": {Args: "--verbose"}}) {
		t.Errorf("engine.json keeps launch settings %+v", launch)
	}
}

func TestCustomACPSaveFailureKeepsTheFormAndSaysSo(t *testing.T) {
	dir := tempDir(t)
	h := start(t, dir)
	openCustomACP(h)
	h.a.customACP = acp.CustomProfile{Provider: "mine", Binary: "/opt/mine"}
	h.a.customACPArgs = "acp"
	h.tt.Frame()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	err := h.tt.Click("Add ACP provider")
	os.Chmod(dir, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h.a.customACPErr, "Couldn't save engine.json: ") {
		t.Fatalf("the card says %q", h.a.customACPErr)
	}
	if h.a.customACP.Provider != "mine" || h.a.customACPArgs != "acp" {
		t.Errorf("the form lost what it held: %+v %q", h.a.customACP, h.a.customACPArgs)
	}
	h.tt.Frame()
	if !h.tt.HasText(h.a.customACPErr) {
		t.Errorf("the page doesn't show %q: %q", h.a.customACPErr, h.tt.Texts())
	}
}

func TestCustomACPChangesWaitForRunningAgents(t *testing.T) {
	dir := tempDir(t)
	had := []acp.CustomProfile{{Provider: "mine", Binary: "/opt/mine"}}
	setUp(t, dir, func(cfg *daemon.Config) { cfg.CustomACP = had })
	h := startWith(t, dir, func(c *daemon.Config) { c.Providers = daemon.FakeProviders(fake.New(fake.Turn(true))) })
	openCustomACP(h)
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer ep.Close()
	th := api.NewID()
	for _, b := range []api.Intent{
		api.ThreadCreate{Workspace: "/work/t3code", Title: "Busy", Model: fakeModel},
		api.MessageDispatch{Text: "go", Mode: api.StartNow},
	} {
		if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: th, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	h.until("the agent at work", func() bool { return h.a.localRuns() > 0 })
	h.until("the card to say to stop the agents", func() bool { return h.tt.HasText(stopFirst) })
	h.a.customACP = acp.CustomProfile{Provider: "two", Binary: "/opt/two"}
	h.tt.Frame()
	h.click("Add ACP provider")
	h.click("Remove")
	if got, _ := customACP(t, dir); !reflect.DeepEqual(got, had) || !h.a.backend.Machine().Embedded || h.a.localRuns() == 0 {
		t.Errorf("under a running agent, engine.json went to %+v, and %d agents run", got, h.a.localRuns())
	}
}

func TestCustomACPOnAnotherProcessesEngineSaysWhenItApplies(t *testing.T) {
	dir := tempDir(t)
	engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	h := start(t, dir)
	openCustomACP(h)
	h.until("the card's note", func() bool { return h.tt.HasText("glitched serve applies a change here when it next starts.") })
}

// devboxCard is the card on a window with devbox paired and chosen, and
// devbox's state directory.
func devboxCard(t *testing.T, change func(*daemon.Config)) (*harness, string) {
	t.Helper()
	rdir := tempDir(t)
	setUp(t, rdir, change)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := startEngine(context.Background(), daemon.Config{Dir: rdir, Hub: true, Listen: []string{"127.0.0.1:0"}, Providers: provider.Mux{"claude": fake.New(nil)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remote.Close() })
	h := start(t, tempDir(t))
	if err := h.addServer(client.Server{Label: "devbox", Addr: remote.Addrs()[0].String(), Token: token}); err != nil {
		t.Fatal(err)
	}
	h.until("devbox online", func() bool { return len(h.a.launchMachines()) == 2 })
	openCustomACP(h)
	h.click("devbox")
	h.until("devbox's custom ACP providers", func() bool {
		return h.tt.HasText("Devbox's engine applies a change when it next starts.")
	})
	h.reveal("Add ACP provider")
	return h, rdir
}

// On devbox, the card adds a provider to devbox's engine.json, not this
// machine's, and removes it with its launch settings.
func TestCustomACPOnARemoteMachine(t *testing.T) {
	h, rdir := devboxCard(t, func(cfg *daemon.Config) {
		cfg.Launch = map[string]provider.Launch{"mine": {Args: "--old"}}
	})
	if h.tt.HasText("Browse…") {
		t.Errorf("devbox chosen, and the card offers this machine's file picker")
	}
	bin := demoProgram(t)
	h.a.customACP = acp.CustomProfile{Provider: "mine", Name: "Mine", Binary: bin}
	h.a.customACPArgs = "acp"
	h.tt.Frame()
	h.click("Add ACP provider")
	want := []acp.CustomProfile{{Provider: "mine", Name: "Mine", Binary: bin, Args: []string{"acp"}}}
	h.until("devbox's engine.json with the provider", func() bool { got, _ := customACP(t, rdir); return reflect.DeepEqual(got, want) })
	h.until("the card's new row", func() bool { return h.tt.HasText(shellLine(nil, []string{bin, "acp"})) })
	if h.a.customACPErr != "" || h.a.customACP.Provider != "" {
		t.Errorf("after the add, the form holds %+v, says %q", h.a.customACP, h.a.customACPErr)
	}
	if got, _ := customACP(t, h.a.dir); len(got) != 0 {
		t.Errorf("this machine's engine.json took devbox's provider: %+v", got)
	}

	h.reveal("Remove")
	h.click("Remove")
	h.until("devbox's engine.json without the provider", func() bool { got, _ := customACP(t, rdir); return len(got) == 0 })
	if _, launch := customACP(t, rdir); len(launch) != 0 {
		t.Errorf("devbox's engine.json keeps launch settings %+v", launch)
	}
	h.until("the card without the row", func() bool {
		return h.tt.HasText("No custom ACP providers configured. Add any ACP-compatible coding agent CLI below.")
	})
}

// A binary devbox doesn't have is devbox's engine's to refuse, and the
// card says why.
func TestCustomACPOnARemoteMachineRefusesABinaryItLacks(t *testing.T) {
	h, rdir := devboxCard(t, func(*daemon.Config) {})
	h.a.customACP = acp.CustomProfile{Provider: "mine", Binary: "/nowhere/mine"}
	h.tt.Frame()
	h.click("Add ACP provider")
	h.until("the engine's refusal", func() bool { return h.a.customACPErr != "" })
	if want := "Couldn't save to devbox: binary: there is no file at /nowhere/mine"; h.a.customACPErr != want {
		t.Errorf("the card says %q, want %q", h.a.customACPErr, want)
	}
	if got, _ := customACP(t, rdir); len(got) != 0 {
		t.Errorf("devbox's engine.json holds %+v", got)
	}
	if h.a.customACP.Binary != "/nowhere/mine" {
		t.Errorf("the form lost the binary: %+v", h.a.customACP)
	}
	h.tt.Frame()
	if !h.tt.HasText(h.a.customACPErr) {
		t.Errorf("the page doesn't show %q", h.a.customACPErr)
	}
}

func TestCustomACPBrowseFillsTheBinary(t *testing.T) {
	was := chooseBinary
	chooseBinary = func(*app) string { return "/opt/tools/my agent" }
	t.Cleanup(func() { chooseBinary = was })
	h := start(t, tempDir(t))
	openCustomACP(h)
	h.a.customACP.Provider = "mine"
	h.click("Browse…")
	if want := (acp.CustomProfile{Provider: "mine", Binary: "/opt/tools/my agent"}); !reflect.DeepEqual(h.a.customACP, want) {
		t.Errorf("after Browse, the form holds %+v", h.a.customACP)
	}
}
