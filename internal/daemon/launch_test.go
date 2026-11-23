package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/codex"
	"glitched/internal/provider/codex/codextest"
	"glitched/internal/transport"
)

// An engine.json from before providers keeps its programs' paths, and a
// key this engine doesn't know survives a save; the old keys move into
// providers.
func TestAnOldEngineJSONKeepsItsPathsAndUnknownKeys(t *testing.T) {
	dir := stateDir(t)
	old := `{"hub": true, "epoch": 3, "claude_path": "/opt/claude/bin/claude", "codex_path": "/opt/codex", "agy_path": "/opt/agy", "from_a_newer_engine": {"x": [1, 2]}}`
	if err := os.WriteFile(filepath.Join(dir, configFile), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]provider.Launch{"claude": {Path: "/opt/claude/bin/claude"}, "codex": {Path: "/opt/codex"}, "antigravity": {Path: "/opt/agy"}}
	if !reflect.DeepEqual(cfg.Launch, want) || cfg.Epoch != 3 {
		t.Fatalf("read %+v, want epoch 3 and %+v", cfg, want)
	}
	cfg.WithAgents()
	if p := cfg.Providers["codex"].(*codex.Adapter).Path; p != "/opt/codex" {
		t.Errorf("the codex adapter runs %q", p)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	b, _ := os.ReadFile(filepath.Join(dir, configFile))
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["from_a_newer_engine"]) != `{"x":[1,2]}` && !strings.Contains(string(b), `"from_a_newer_engine"`) {
		t.Errorf("the unknown key went: %s", b)
	}
	for _, k := range []string{"claude_path", "codex_path", "agy_path"} {
		if _, ok := raw[k]; ok {
			t.Errorf("%s stayed beside providers: %s", k, b)
		}
	}
	again, err := Defaults(dir)
	if err != nil || !reflect.DeepEqual(again.Launch, want) {
		t.Errorf("read back %+v, %v", again.Launch, err)
	}
}

// codexEngine is an engine serving Codex, played by the test binary, on
// its socket and, for a paired client, on loopback TCP.
func codexEngine(t *testing.T) (dir string, d *Daemon, token string) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(codextest.Scenario, "command")
	t.Setenv(codextest.Log, filepath.Join(t.TempDir(), "wire.jsonl"))
	dir = stateDir(t)
	if token, err = Pair(dir, "laptop"); err != nil {
		t.Fatal(err)
	}
	ad := &codex.Adapter{Path: self}
	d, err = Start(context.Background(), Config{Dir: dir, Hub: true, Listen: []string{"127.0.0.1:0"}, Providers: provider.Mux{"codex": ad}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return dir, d, token
}

// A launch is set for the engine's next start of the program: a wrong one
// says what is wrong and is kept nowhere, a right one goes to engine.json
// and the adapter at once, and the probe after runs it.
func TestALaunchIsCheckedKeptAndRun(t *testing.T) {
	dir, _, _ := codexEngine(t)
	ep := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer ep.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	self, _ := os.Executable()
	tools := filepath.Join(t.TempDir(), "my tools")
	home := filepath.Join(t.TempDir(), "codex home")
	os.MkdirAll(tools, 0o755)
	os.MkdirAll(home, 0o755)
	bin := filepath.Join(tools, "my codex")
	if err := os.Symlink(self, bin); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		l    provider.Launch
		want provider.LaunchError
	}{
		{provider.Launch{Path: "codex"}, provider.LaunchError{Path: `"codex" is relative: give the whole path, from / or ~`}},
		{provider.Launch{Path: bin, Home: filepath.Join(home, "none")}, provider.LaunchError{Home: "there is no folder at " + filepath.Join(home, "none")}},
		{provider.Launch{Path: bin, Args: "-c sandbox_mode=danger-full-access"}, provider.LaunchError{Args: "-c sandbox_mode is set by Glitched itself"}},
	} {
		info, err := ep.SetLaunch(ctx, "codex", c.l)
		if err != nil {
			t.Fatal(err)
		}
		if info.Saved || !strings.HasPrefix(info.Errors.Path, c.want.Path) || info.Errors.Home != c.want.Home || info.Errors.Args != c.want.Args {
			t.Errorf("%+v: %+v, want it refused with %+v", c.l, info, c.want)
		}
	}
	if cfg, _ := Defaults(dir); cfg.Launch != nil {
		t.Fatalf("a refused launch was kept: %+v", cfg.Launch)
	}

	l := provider.Launch{Path: bin, Home: home, Args: `--enable "x y"`}
	info, err := ep.SetLaunch(ctx, "codex", l)
	if err != nil || !info.Saved || info.Errors != (provider.LaunchError{}) {
		t.Fatalf("SetLaunch = %+v, %v", info, err)
	}
	if info.Path != bin || info.HomeVar != "CODEX_HOME" || info.Version != "codex-cli 0.200.0" || !reflect.DeepEqual(info.Env, []string{"CODEX_HOME=" + home}) ||
		!reflect.DeepEqual(info.Command, []string{bin, "app-server", "--enable", "x y"}) {
		t.Errorf("SetLaunch = %+v", info)
	}
	if cfg, _ := Defaults(dir); !reflect.DeepEqual(cfg.Launch, map[string]provider.Launch{"codex": l}) {
		t.Errorf("engine.json keeps %+v", cfg.Launch)
	}
	served, err := ep.Providers(ctx, true, false)
	if err != nil || len(served) != 1 || served[0].Info.Path != bin {
		t.Errorf("the probe after ran %+v, %v; want %s", served, err, bin)
	}
	all, err := ep.Launches(ctx)
	if err != nil || len(all) != 1 || all[0].Launch != l || !all[0].Saved && all[0].Path != bin {
		t.Errorf("Launches = %+v, %v", all, err)
	}

	info, err = ep.SetLaunch(ctx, "codex", provider.Launch{})
	if err != nil || !info.Saved {
		t.Fatalf("clearing: %+v, %v", info, err)
	}
	if cfg, _ := Defaults(dir); len(cfg.Launch) != 0 {
		t.Errorf("a cleared launch stays: %+v", cfg.Launch)
	}
	if _, err := ep.SetLaunch(ctx, "claude", l); err == nil {
		t.Error("a launch for a provider the engine doesn't run was taken")
	}
}

// Another machine sets a launch only with a token this engine paired.
func TestOnlyAPairedClientSetsALaunchFromAnotherMachine(t *testing.T) {
	dir, d, token := codexEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for name, tok := range map[string]string{"no token": "", "a wrong token": token + "x"} {
		if _, err := remote(t, d, tok).SetLaunch(ctx, "codex", provider.Launch{Args: "--enable x"}); !errors.Is(err, transport.ErrRefused) {
			t.Errorf("%s: %v, want ErrRefused", name, err)
		}
	}
	if cfg, _ := Defaults(dir); cfg.Launch != nil {
		t.Fatalf("an unpaired client's launch was kept: %+v", cfg.Launch)
	}
	c := remote(t, d, token)
	info, err := c.SetLaunch(ctx, "codex", provider.Launch{Args: "--enable x"})
	if err != nil || !info.Saved || info.Path != "" {
		t.Fatalf("the paired client's SetLaunch = %+v, %v", info, err)
	}
	if all, err := c.Launches(ctx); err != nil || len(all) != 1 || all[0].Launch.Args != "--enable x" {
		t.Errorf("the paired client reads %+v, %v", all, err)
	}
}

// unanswering is a provider whose program doesn't answer --version.
type unanswering struct{ codex.Adapter }

func (*unanswering) Version(context.Context, provider.Program) (string, error) {
	return "", errors.New("exit status 2")
}

// A program the launch names must answer --version before it is kept; one
// found on PATH that doesn't is the program's own trouble.
func TestALaunchedProgramMustAnswerItsVersion(t *testing.T) {
	self, _ := os.Executable()
	dir := stateDir(t)
	mux := provider.Mux{"codex": &unanswering{}}
	s := &launches{dir: dir, mux: mux, commands: newCommandLists(t.Context(), mux)}
	info, err := s.SetLaunch(context.Background(), "codex", provider.Launch{Path: self})
	if err != nil || info.Saved || info.Errors.Path != self+" didn't answer --version: exit status 2" {
		t.Errorf("SetLaunch = %+v, %v", info, err)
	}
	t.Setenv("PATH", filepath.Dir(self))
	info, err = s.SetLaunch(context.Background(), "codex", provider.Launch{Args: "--enable x"})
	if err != nil || !info.Saved || info.Errors != (provider.LaunchError{}) {
		t.Errorf("with the program on PATH: %+v, %v", info, err)
	}
}

// What engine.json keeps of a provider that this engine doesn't read, and
// of providers it doesn't run, survives every save.
func TestASaveKeepsWhatAProviderHasThatItDoesntKnow(t *testing.T) {
	dir := stateDir(t)
	file := filepath.Join(dir, configFile)
	old := `{"future_top": 3, "providers": {"claude": {"path": "/bin/old", "future_provider": true}, "newcli": {"future_setting": 42}}}`
	if err := os.WriteFile(file, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Edit(dir, func(c *Config) error { c.Hub = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := Edit(dir, func(c *Config) error { delete(c.Launch, "claude"); return nil }); err != nil {
		t.Fatal(err)
	}
	var got struct {
		FutureTop int                                   `json:"future_top"`
		Providers map[string]map[string]json.RawMessage `json:"providers"`
	}
	b, _ := os.ReadFile(file)
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	claude, newcli := got.Providers["claude"], got.Providers["newcli"]
	if got.FutureTop != 3 || string(claude["future_provider"]) != "true" || claude["path"] != nil || string(newcli["future_setting"]) != "42" {
		t.Errorf("engine.json after the saves: %s", b)
	}
}

// An older engine.json's relative path ran as exec found it: from where
// the engine started, or on PATH. It is read as that whole path.
func TestAnOldRelativePathIsReadAsTheProgramItRan(t *testing.T) {
	tools := t.TempDir()
	bin := filepath.Join(tools, "fake-cli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tools)
	t.Setenv("PATH", tools)
	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, configFile), []byte(`{"codex_path": "./fake-cli", "agy_path": "fake-cli", "claude_path": "./gone"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.Stat(bin)
	for _, name := range []string{"codex", "antigravity"} {
		path := cfg.Launch[name].Path
		got, err := os.Stat(path)
		if !filepath.IsAbs(path) || err != nil || !os.SameFile(got, want) {
			t.Errorf("%s's old path reads as %q", name, path)
		}
	}
	if path := cfg.Launch["claude"].Path; path != "./gone" {
		t.Errorf("a path that finds nothing reads as %q; it should stay, for Settings to show what's wrong", path)
	}
}

type commanding struct {
	provider.Placed
	args string
}

func (c *commanding) Placement() provider.Placement { return provider.Placement{} }
func (c *commanding) Place(l provider.Launch)       { c.args = l.Args }
func (c *commanding) Commands(context.Context, string) ([]provider.Command, error) {
	return []provider.Command{{Name: c.args}}, nil
}

// A saved launch has the next ask for the provider's slash commands list
// them as its program now starts.
func TestANewLaunchListsTheSlashCommandsAgain(t *testing.T) {
	p := &commanding{args: "old"}
	mux := provider.Mux{"claude": p}
	cmds := newCommandLists(t.Context(), mux)
	s := &launches{dir: stateDir(t), mux: mux, commands: cmds}
	workspace := t.TempDir()
	if got, err := cmds.Commands(t.Context(), "claude", workspace); err != nil || len(got) != 1 || got[0].Name != "old" {
		t.Fatalf("first listing: %v, %v", got, err)
	}
	if info, err := s.SetLaunch(t.Context(), "claude", provider.Launch{Args: "new"}); err != nil || !info.Saved {
		t.Fatalf("SetLaunch = %+v, %v", info, err)
	}
	if got, err := cmds.Commands(t.Context(), "claude", workspace); err != nil || len(got) != 1 || got[0].Name != "new" {
		t.Errorf("after the save the commands are %v, %v", got, err)
	}
}

// A caller who gave up before the launch was kept is told it wasn't, and
// it isn't.
func TestACanceledSaveKeepsNothing(t *testing.T) {
	dir := stateDir(t)
	p := &commanding{args: "before"}
	mux := provider.Mux{"codex": p}
	s := &launches{dir: dir, mux: mux, commands: newCommandLists(t.Context(), mux)}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	info, err := s.SetLaunch(ctx, "codex", provider.Launch{Args: "--enable review"})
	cfg, rerr := Defaults(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err == nil || info.Saved || cfg.Launch != nil || p.args != "before" {
		t.Errorf("canceled: %+v, %v; engine.json %+v; placed %q", info, err, cfg.Launch, p.args)
	}
}

// The arguments reach the engine as typed: a trailing escaped space is
// part of the last one.
func TestTheArgumentsAreSavedAsTyped(t *testing.T) {
	dir, _, _ := codexEngine(t)
	ep := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer ep.Close()
	self, _ := os.Executable()
	info, err := ep.SetLaunch(t.Context(), "codex", provider.Launch{Path: self, Args: `--enable suffix\ `})
	if err != nil || !info.Saved || !slices.Equal(info.Command[len(info.Command)-2:], []string{"--enable", "suffix "}) {
		t.Errorf("SetLaunch = %+v, %v", info, err)
	}
}
