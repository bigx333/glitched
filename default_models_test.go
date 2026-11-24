package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/jsonfile"
	"glitched/internal/plugins"
	"glitched/internal/provider"
	"glitched/internal/provider/acp/acptest"
	"glitched/internal/provider/codex/codextest"
	"glitched/internal/provider/fake"
)

// shifting is an adapter whose provider reports what models holds as it's
// asked: its program's default can move between two asks.
type shifting struct {
	provider.Adapter
	models *atomic.Pointer[[]provider.Model]
}

func (s shifting) Describe(ctx context.Context) (provider.Info, error) {
	info, err := s.Adapter.Describe(ctx)
	info.Models = *s.models.Load()
	return info, err
}

func (s shifting) Commands(context.Context, string) ([]provider.Command, error) { return nil, nil }

// cliModels are what a provider's engine reports in these tests: Beta is
// its program's own default, which it runs at Low, unless known is false,
// when the engine can't say which it runs.
func cliModels(known bool) []provider.Model {
	efforts := []provider.Effort{{ID: "low"}, {ID: "high"}}
	ms := []provider.Model{
		{ID: "alpha", Name: "Alpha", Efforts: efforts},
		{ID: "beta", Name: "Beta", Efforts: efforts},
		{ID: "gamma", Name: "Gamma", Efforts: efforts},
	}
	if known {
		ms[1].Default, ms[1].DefaultEffort = true, "low"
	}
	return ms
}

// startReporting starts the app on dir, whose composer.json holds file,
// "" for none, with provider p's engine reporting models, which a test
// may change.
func startReporting(t *testing.T, dir, p, file string, models []provider.Model) (*harness, *atomic.Pointer[[]provider.Model]) {
	t.Helper()
	if file != "" {
		os.WriteFile(filepath.Join(dir, "composer.json"), []byte(file), 0o600)
	}
	var now atomic.Pointer[[]provider.Model]
	now.Store(&models)
	h := startWith(t, dir, func(c *daemon.Config) {
		engineReportsBy(nil)(c)
		c.Providers[p] = shifting{c.Providers[p], &now}
	})
	allAnswered(h)
	h.until("the default to take the engine's answer", func() bool {
		h.tt.Frame()
		return !h.a.models.changed
	})
	return h, &now
}

func composerFile(t *testing.T, dir string) choice {
	t.Helper()
	var f choice
	if err := jsonfile.Load(filepath.Join(dir, "composer.json"), &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// providersFollowed are the providers whose own default a test follows,
// each as composer.json holds it: Claude Code's also with no file, a
// user who never chose.
var providersFollowed = []struct{ name, provider, file string }{
	{"claude, never chosen", "claude", ""},
	{"claude", "claude", `{"model":{"provider":"claude","id":""}}`},
	{"codex", "codex", `{"model":{"provider":"codex","id":""}}`},
	{"antigravity", "antigravity", `{"model":{"provider":"antigravity","id":""}}`},
}

// While the user hasn't chosen, a new thread starts on the provider's own
// default, at the effort its program runs it at: the picker shows it, and
// Settings › Providers fills it in. Check again takes a default that
// moved since.
func TestANewThreadStartsOnTheProgramsOwnDefault(t *testing.T) {
	for _, tc := range providersFollowed {
		t.Run(tc.name, func(t *testing.T) {
			h, now := startReporting(t, tempDir(t), tc.provider, tc.file, cliModels(true))
			if got := h.a.modelRef(); got.Provider != tc.provider || got.Model != "beta" || got.Effort != "low" {
				t.Fatalf("a new thread starts on %+v, want %s's beta at low", got, tc.provider)
			}
			if !h.tt.HasText("Beta") {
				t.Errorf("the picker doesn't show Beta: %q", h.tt.Texts())
			}
			openProviders(h, tc.provider)
			for _, s := range []string{"Beta · CLI default", "Default · Beta · Low · CLI default"} {
				if !h.tt.HasText(s) {
					t.Errorf("the page doesn't show %q: %q", s, h.tt.Texts())
				}
			}
			moved := cliModels(true)
			moved[1].Default, moved[2].Default, moved[2].DefaultEffort = false, true, "high"
			now.Store(&moved)
			h.a.router.Push("/")
			h.a.checkProviders()
			h.until("the moved default", func() bool {
				h.tt.Frame()
				return h.a.modelRef().Model == "gamma"
			})
			if got := h.a.modelRef(); got.Effort != "high" {
				t.Errorf("after the default moved, a new thread starts on %+v, want gamma at high", got)
			}
			if f := composerFile(t, h.a.dir); f.Model.ID != "" {
				t.Errorf("following the default wrote %+v to composer.json", f)
			}
		})
	}
}

// A model the user picks is theirs, though it's the program's default
// too: it stays as the engine's default moves, and through a restart.
func TestAModelThePickerChoosesWinsOverTheProgramsDefault(t *testing.T) {
	for _, tc := range providersFollowed {
		t.Run(tc.name, func(t *testing.T) {
			dir := tempDir(t)
			h, now := startReporting(t, dir, tc.provider, tc.file, cliModels(true))
			h.click("Model")
			pickerSettle(t, h)
			h.tt.Move(0, 0)
			h.click(familyOf(tc.provider).name)
			pickerSettle(t, h)
			h.click("Beta")
			pickerSettle(t, h)
			if f := composerFile(t, dir); f.Model != (modelKey{tc.provider, "beta"}) {
				t.Fatalf("after picking Beta, composer.json holds %+v", f)
			}
			moved := cliModels(true)
			moved[1].Default, moved[2].Default = false, true
			now.Store(&moved)
			h.a.checkProviders()
			allAnswered(h)
			h.tt.Frame()
			if got := h.a.modelRef(); got.Model != "beta" {
				t.Errorf("after the program's default moved, the user's pick became %+v", got)
			}
			openProviders(h, tc.provider)
			if h.tt.HasText("Default · Beta · Low · CLI default") || !h.tt.HasText("Default · Beta · Low") {
				t.Errorf("the page shows the user's pick as the program's own: %q", h.tt.Texts())
			}
			h.a.backend.Close()
			again, _ := startReporting(t, dir, tc.provider, "", moved)
			if got := again.a.modelRef(); got.Model != "beta" {
				t.Errorf("after a restart the user's pick became %+v", got)
			}
		})
	}
}

// Settings › Providers' control goes back to the program's own default,
// and its effort, from a model the user chose, for any provider's row.
func TestTheProvidersPageGoesBackToTheProgramsDefault(t *testing.T) {
	for _, tc := range providersFollowed[1:] {
		t.Run(tc.name, func(t *testing.T) {
			dir := tempDir(t)
			h, _ := startReporting(t, dir, tc.provider, `{"model":{"provider":"claude","id":"claude-opus-5-5"},"effort":"max"}`, cliModels(true))
			if got := h.a.modelRef(); got.Model != "claude-opus-5-5" {
				t.Fatalf("the user's choice became %+v", got)
			}
			openProviders(h, tc.provider)
			if tc.provider != "claude" && !h.tt.HasText("Beta · CLI default") {
				t.Errorf("another provider's row doesn't fill in its default: %q", h.tt.Texts())
			}
			h.click("Default model for new threads")
			h.tt.Advance(time.Second)
			h.click("CLI default · Beta")
			h.tt.Frame()
			if got := h.a.modelRef(); got.Provider != tc.provider || got.Model != "beta" || got.Effort != "low" {
				t.Errorf("back to the program's default, a new thread starts on %+v", got)
			}
			if f := composerFile(t, dir); f.Model != (modelKey{Provider: tc.provider}) || f.Effort != "" {
				t.Errorf("back to the program's default, composer.json holds %+v", f)
			}
			if !h.tt.HasText("Default · Beta · Low · CLI default") {
				t.Errorf("the page doesn't show the program's default: %q", h.tt.Texts())
			}
		})
	}
}

// An engine that can't say which model its program runs leaves the
// default as it was before Glitched asked: Opus 5.5 at High where Claude
// Code offers it, else the provider's first, at the app's own effort, and
// nothing reads as the program's own.
func TestAProgramsDefaultTheEngineCantSayIsTheAppsOwn(t *testing.T) {
	for _, tc := range providersFollowed {
		t.Run(tc.name, func(t *testing.T) {
			models := cliModels(false)
			if tc.provider == "claude" {
				models = append(models, provider.Model{ID: "claude-opus-5-5", Name: "Opus 5.5", Efforts: []provider.Effort{{ID: "low"}, {ID: "high"}}})
			}
			h, _ := startReporting(t, tempDir(t), tc.provider, tc.file, models)
			want := modelKey{tc.provider, "alpha"}
			if tc.provider == "claude" {
				want = ownChoice.Model
			}
			if got := h.a.modelRef(); got.Provider != want.Provider || got.Model != want.ID || got.Effort != "high" {
				t.Errorf("a new thread starts on %+v, want %+v at high", got, want)
			}
			openProviders(h, tc.provider)
			if strings.Contains(strings.Join(h.tt.Texts(), "\n"), "CLI default") {
				t.Errorf("the page reads a default no engine said: %q", h.tt.Texts())
			}
		})
	}
}

// A draft on another machine's engine names no model where the default
// follows the program's own, and no effort but one the user chose: that
// machine's program runs its own, which this machine's engine doesn't
// know, and the picker says so, checking no row of this machine's. A
// model the user picked goes as it is.
func TestARemoteDraftLeavesTheModelToItsProgram(t *testing.T) {
	for _, tc := range providersFollowed {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := startReporting(t, tempDir(t), tc.provider, tc.file, cliModels(true))
			remote := thread{env: &client.Env{Kind: client.Remote}, Thread: &client.Thread{}}
			if got := h.a.newThreadRef(remote); got.Provider != tc.provider || got.Model != "" || got.Effort != "" {
				t.Errorf("a remote draft's thread starts on %+v, want %s naming no model and no effort", got, tc.provider)
			}
			var picker composerModel
			ui.NewTester(func(c *ui.Context) { picker = h.a.composerModelOf(c, remote, plugins.ThreadRecord{}, "panel") }, 500, 500).Frame()
			if picker.Model != "CLI default" || picker.Key != (modelKey{}).String() {
				t.Errorf("a remote draft's picker shows %q and checks %q", picker.Model, picker.Key)
			}
			for _, m := range picker.Models {
				if strings.Contains(m.About, "CLI default") {
					t.Errorf("a remote draft's picker says %s is %q, though this machine's default needn't be the other's", m.Key, m.About)
				}
			}
			if got := h.a.newThreadRef(h.a.current()); got.Model != "" || got.Effort != "" {
				t.Errorf("this machine's draft's thread starts on %+v, want no model and no effort", got)
			}
			h.a.effort = "high"
			h.a.saveComposer()
			if got := h.a.newThreadRef(remote); got.Model != "" || got.Effort != "high" {
				t.Errorf("a remote draft's thread at the user's effort starts on %+v", got)
			}
			h.a.chooseModel(modelKey{tc.provider, "alpha"})
			if got := h.a.newThreadRef(remote); got.Model != "alpha" {
				t.Errorf("a remote draft's thread on the user's pick starts on %+v", got)
			}
		})
	}
}

// Check again takes a default that moved, though no draft shows: the
// page shows it at once.
func TestCheckAgainShowsAMovedDefaultOnTheProvidersPage(t *testing.T) {
	h, now := startReporting(t, tempDir(t), "claude", "", cliModels(true))
	openProviders(h, "claude")
	moved := cliModels(true)
	moved[1].Default, moved[2].Default, moved[2].DefaultEffort = false, true, "high"
	now.Store(&moved)
	h.a.checkProviders()
	allAnswered(h)
	h.tt.Frame()
	if !h.tt.HasText("Gamma · CLI default") || h.a.modelRef().Model != "gamma" {
		t.Errorf("after Check again, the page shows %q", h.tt.Texts())
	}
}

// Going back to the program's own default drops an effort the user chose
// with it: the effort moves with the program's after.
func TestGoingBackToTheProgramsDefaultDropsTheEffortChosen(t *testing.T) {
	dir := tempDir(t)
	h, now := startReporting(t, dir, "claude", `{"effort":"high"}`, cliModels(true))
	if got := h.a.modelRef().Effort; got != "high" {
		t.Fatalf("the effort chosen became %q", got)
	}
	openProviders(h, "claude")
	h.click("Default model for new threads")
	h.tt.Advance(time.Second)
	h.click("CLI default · Beta")
	h.tt.Frame()
	if got, f := h.a.modelRef().Effort, composerFile(t, dir); got != "low" || f.Effort != "" {
		t.Fatalf("back to the program's default, the effort is %q and composer.json holds %+v", got, f)
	}
	moved := cliModels(true)
	moved[1].DefaultEffort = "high"
	now.Store(&moved)
	h.a.checkProviders()
	allAnswered(h)
	h.tt.Frame()
	if got := h.a.modelRef().Effort; got != "high" {
		t.Errorf("after the program's effort moved, the default's is %q", got)
	}
}

// The effort a program runs its default at is the default's only while
// it follows that program: a model chosen without an effort, or a
// fallback where no engine said a default, keeps the app's own.
func TestAProgramsEffortIsOnlyItsOwnDefaults(t *testing.T) {
	h, _ := startReporting(t, tempDir(t), "codex", `{"model":{"provider":"codex","id":"beta"}}`, cliModels(true))
	if got := h.a.modelRef(); got.Model != "beta" || got.Effort != "high" {
		t.Errorf("a chosen Beta without an effort starts on %+v, want high", got)
	}
	unknown := cliModels(false)
	unknown[0].DefaultEffort = "low"
	h, _ = startReporting(t, tempDir(t), "codex", `{"model":{"provider":"codex","id":""}}`, unknown)
	if got := h.a.modelRef(); got.Model != "alpha" || got.Effort != "high" {
		t.Errorf("with no default said, a new thread starts on %+v, want alpha at high", got)
	}
}

// A composer.json that doesn't parse, repaired by a change, keeps
// following the program's own default.
func TestARepairedComposerKeepsFollowingTheProgram(t *testing.T) {
	dir := tempDir(t)
	h, _ := startReporting(t, dir, "claude", "{", cliModels(true))
	h.a.fast = true
	h.a.saveComposer()
	if f := composerFile(t, dir); f.Model != (modelKey{Provider: "claude"}) || f.Effort != "" || !f.Fast || !h.a.followsCLI {
		t.Errorf("the repair wrote %+v, following %t", f, h.a.followsCLI)
	}
}

// defaulting is a provider whose program's default depends on the
// workspace, as its settings there name one (provider.Defaulter).
type defaulting struct {
	shifting
	in map[string]string
}

func (d defaulting) DefaultModel(_ context.Context, workspace string) (string, error) {
	return d.in[workspace], nil
}

// A draft in a workspace whose settings name the program's default starts
// a thread on that one, as the picker shows; another draft's is the
// engine's.
func TestADraftsWorkspaceNamesTheProgramsDefault(t *testing.T) {
	ws := t.TempDir()
	models := cliModels(true)
	var now atomic.Pointer[[]provider.Model]
	now.Store(&models)
	h := startWith(t, tempDir(t), func(c *daemon.Config) {
		engineReportsBy(nil)(c)
		c.Providers["claude"] = defaulting{shifting{c.Providers["claude"], &now}, map[string]string{ws: "gamma"}}
	})
	allAnswered(h)
	h.a.startIn(draft{h.local(), ws})
	h.until("the workspace's default", func() bool {
		h.tt.Frame()
		return h.a.modelRef().Model == "gamma"
	})
	if !h.tt.HasText("Gamma") {
		t.Errorf("the picker doesn't show Gamma: %q", h.tt.Texts())
	}
	h.a.draft = "Say hi."
	h.a.send(h.a.current())
	h.until("the thread", func() bool { return !h.a.current().id().IsZero() })
	if got := h.a.current().View.Model; got.Model != "" || got.Effort != "" {
		t.Errorf("the thread starts on %+v, want no model and no effort, for claude to run its own there", got)
	}
	h.a.startIn(draft{h.local(), t.TempDir()})
	h.until("another workspace's default", func() bool {
		h.tt.Frame()
		return h.a.modelRef().Model == "beta"
	})
}

// The model Codex's config names is its default, at the config's effort:
// the picker shows it, and the thread's turn runs it.
func TestCodexsConfiguredModelIsTheOneItRuns(t *testing.T) {
	dir := tempDir(t)
	os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model":{"provider":"codex","id":""}}`), 0o600)
	h := startWith(t, dir, withCodex(t, "configured"))
	allAnswered(h)
	h.until("Codex's configured model", func() bool {
		h.tt.Frame()
		return h.a.modelRef().Model == "gpt-6-luna"
	})
	if !h.tt.HasText("GPT-6 Luna") || !h.tt.HasText("Low") {
		t.Fatalf("the picker doesn't show the configured GPT-6 Luna at Low: %q", h.tt.Texts())
	}
	sendNew(h, "Say hi.")
	namesNoModel(t, 1)
	h.until("the first turn's end", func() bool { return h.tt.HasText("hi") && !h.a.current().View.Working() })
	h.a.draft = "Say it again."
	h.a.send(h.a.current())
	namesNoModel(t, 2)
}

// namesNoModel fails the test where Codex's thread/start or turn/start
// named a model or an effort: a thread following Codex's own default
// leaves both to Codex, which runs its config's where the thread runs.
func namesNoModel(t *testing.T, turns int) {
	t.Helper()
	log := os.Getenv(codextest.Log)
	until := func(cond func() bool) {
		deadline := time.Now().Add(10 * time.Second)
		for !cond() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}
	until(func() bool { return len(wireEntries(t, log, "turn/start")) >= turns })
	if n := len(wireEntries(t, log, "turn/start")); n < turns {
		t.Fatalf("Codex got %d turn/start, want %d", n, turns)
	}
	for _, m := range []string{"thread/start", "turn/start"} {
		got := wireEntries(t, log, m)
		if len(got) == 0 {
			t.Fatalf("Codex got no %s", m)
		}
		for _, p := range got {
			var params map[string]any
			json.Unmarshal(p, &params)
			if _, ok := params["model"]; ok {
				t.Errorf("%s named model %v, want none", m, params["model"])
			}
			if _, ok := params["effort"]; ok {
				t.Errorf("%s named effort %v, want none", m, params["effort"])
			}
		}
	}
}

// wireEntries are what a fake program received of method, by its log.
func wireEntries(t *testing.T, log, method string) []json.RawMessage {
	t.Helper()
	b, _ := os.ReadFile(log)
	var out []json.RawMessage
	for line := range strings.Lines(string(b)) {
		var e struct {
			Method string
			Params json.RawMessage
		}
		if json.Unmarshal([]byte(line), &e) == nil && e.Method == method {
			out = append(out, e.Params)
		}
	}
	return out
}

// The model the picker shows of Codex's own default is the one Codex
// runs: the thread's turn names it, at the effort shown.
func TestCodexsOwnDefaultIsTheModelItRuns(t *testing.T) {
	dir := tempDir(t)
	os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model":{"provider":"codex","id":""}}`), 0o600)
	h := startWith(t, dir, withCodex(t, "hi"))
	allAnswered(h)
	h.until("Codex's default", func() bool {
		h.tt.Frame()
		return h.a.modelRef().Model == "gpt-6.2-sol"
	})
	if !h.tt.HasText("GPT-6.2 Sol") || !h.tt.HasText("Medium") {
		t.Fatalf("the picker doesn't show Codex's default at Medium: %q", h.tt.Texts())
	}
	sendNew(h, "Say hi.")
	namesNoModel(t, 1)
}

// The model the picker shows of Antigravity's own default is the one its
// session runs: the thread is on it, and nothing moves the session off
// the model and level it starts on.
func TestAntigravitysOwnDefaultIsTheModelItRuns(t *testing.T) {
	dir := tempDir(t)
	os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model":{"provider":"antigravity","id":""}}`), 0o600)
	h := startWith(t, dir, withAntigravity(t, "gui"))
	allAnswered(h)
	h.until("agy's default", func() bool {
		h.tt.Frame()
		return h.a.modelRef().Model == "fake-pro"
	})
	if got := h.a.modelRef(); got.Effort != "high" || !h.tt.HasText("Fake Pro") || !h.tt.HasText("High") {
		t.Fatalf("the picker shows %q, a thread starts on %+v; want Fake Pro at High", h.tt.Texts(), got)
	}
	probes := len(wireEntries(t, os.Getenv(acptest.Log), "session/set_config_option"))
	sendNew(h, "Add a line to the notes.")
	h.until("the card", func() bool { return h.tt.HasText("Antigravity wants to") })
	if got := h.a.current().View.Model; got.Model != "" || got.Effort != "" {
		t.Errorf("the thread starts on %+v, want no model and no effort", got)
	}
	if set := wireEntries(t, os.Getenv(acptest.Log), "session/set_config_option")[probes:]; len(set) > 0 {
		t.Errorf("the run moved the session off the model it starts on: %s", set)
	}
}

// The model the picker shows of Claude Code's own default is the one the
// thread runs.
func TestClaudesOwnDefaultIsTheModelItRuns(t *testing.T) {
	h, _ := startReporting(t, tempDir(t), "claude", "", cliModels(true))
	if !h.tt.HasText("Beta") {
		t.Fatalf("the picker doesn't show Beta: %q", h.tt.Texts())
	}
	sendNew(h, "Say hi.")
	h.until("the thread", func() bool { return !h.a.current().id().IsZero() })
	if got := h.a.current().View.Model; got.Provider != "claude" || got.Model != "" || got.Effort != "" {
		t.Errorf("the thread starts on %+v, want claude naming no model and no effort", got)
	}
	var picker composerModel
	ui.NewTester(func(c *ui.Context) { picker = h.a.composerModelOf(c, h.a.current(), plugins.ThreadRecord{}, "panel") }, 500, 500).Frame()
	if picker.Model != "Claude Beta" || picker.Effort != "Low" {
		t.Errorf("the thread's picker shows %q at %q, want the Claude Beta at Low claude runs", picker.Model, picker.Effort)
	}
}

// gated is a program whose default in a workspace is what answer says,
// once it answers.
type gated struct {
	shifting
	answer func(workspace string) (string, error)
}

func (g gated) DefaultModel(_ context.Context, workspace string) (string, error) {
	return g.answer(workspace)
}

// startGated is a harness whose Claude's default in a workspace is what
// answer says, and a draft in a new workspace.
func startGated(t *testing.T, answer func(string) (string, error)) (*harness, string) {
	t.Helper()
	ws := t.TempDir()
	models := cliModels(true)
	var now atomic.Pointer[[]provider.Model]
	now.Store(&models)
	h := startWith(t, tempDir(t), func(c *daemon.Config) {
		engineReportsBy(nil)(c)
		c.Providers["claude"] = gated{shifting{c.Providers["claude"], &now}, answer}
	})
	allAnswered(h)
	return h, ws
}

// An answer the engine gave of a workspace's default before Check again
// is not the one after: it doesn't replace what Check again found.
func TestAWorkspacesDefaultFromBeforeCheckAgainIsDropped(t *testing.T) {
	release := make(chan struct{})
	var asks atomic.Int32
	var ws string
	h, ws := startGated(t, func(in string) (string, error) {
		if in != ws {
			return "", nil
		}
		if asks.Add(1) == 1 {
			<-release
			return "gamma", nil
		}
		return "alpha", nil
	})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	h.a.startIn(draft{h.local(), ws})
	h.until("the first ask", func() bool { h.tt.Frame(); return asks.Load() == 1 })
	h.a.checkProviders()
	allAnswered(h)
	h.until("the ask after Check again", func() bool {
		h.tt.Frame()
		return h.a.modelRef().Model == "alpha"
	})
	once.Do(func() { close(release) })
	time.Sleep(200 * time.Millisecond)
	for range 5 {
		h.tt.Frame()
	}
	if got := h.a.modelRef().Model; got != "alpha" {
		t.Errorf("the draft's default is %q after the earlier answer came, want the alpha Check again found", got)
	}
}

// A workspace's default the engine couldn't say is asked again a while
// after, rather than taken as none for good.
func TestAWorkspacesDefaultTheEngineCouldntSayIsAskedAgain(t *testing.T) {
	var asks atomic.Int32
	var ws string
	h, ws := startGated(t, func(in string) (string, error) {
		if in != ws {
			return "", nil
		}
		if asks.Add(1) == 1 {
			return "", errors.New("the engine isn't up")
		}
		return "gamma", nil
	})
	h.a.startIn(draft{h.local(), ws})
	h.until("the ask after the failed one", func() bool {
		h.tt.Frame()
		return h.a.modelRef().Model == "gamma"
	})
	if n := asks.Load(); n > 3 {
		t.Errorf("the engine was asked %d times, want it asked again only after a while", n)
	}
}

// A thread that follows its program's default shows what the program
// reported it ran once it has, over the picker's guess.
func TestAThreadFollowingTheDefaultShowsWhatItsProgramRan(t *testing.T) {
	h, _ := startReporting(t, tempDir(t), "claude", "", cliModels(true))
	run := api.NewID()
	th := thread{env: h.local(), Thread: &client.Thread{View: api.Thread{ID: api.NewID(), Workspace: "/work",
		Model:     api.ModelRef{Provider: "claude"},
		LastTurn:  &api.TurnResult{Run: run, Model: api.ModelRef{Provider: "claude"}},
		Confirmed: api.Confirmed{Model: &api.ConfirmedModel{Run: run, Asked: api.ModelRef{Provider: "claude"}, Model: "gamma", Effort: "high"}},
	}}}
	if name, effort := h.a.runningModel(th, "Claude Beta"); name != "Claude Gamma" || effort.label != "High" {
		t.Errorf("the thread's button names %q at %q, want the Claude Gamma at High its program ran", name, effort.label)
	}
}

// An effort the user chose while following the program's default is the
// one a new thread names, though the model the picker expects doesn't
// run at it: the program may run another.
func TestAnEffortChosenWithTheProgramsDefaultIsSentAsChosen(t *testing.T) {
	dir := tempDir(t)
	os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model":{"provider":"codex","id":""},"effort":"xhigh"}`), 0o600)
	h := startWith(t, dir, withCodex(t, "configured"))
	allAnswered(h)
	sendNew(h, "Say hi.")
	log := os.Getenv(codextest.Log)
	h.until("Codex's turn", func() bool { return len(wireEntries(t, log, "turn/start")) > 0 })
	var turn map[string]any
	json.Unmarshal(wireEntries(t, log, "turn/start")[0], &turn)
	if turn["model"] != nil || turn["effort"] != "xhigh" {
		t.Errorf("turn/start names model %v at %v, want no model at the xhigh chosen", turn["model"], turn["effort"])
	}
}

// A program that reports the model it ran but not the effort leaves the
// effort unsaid on the button, rather than the picker's guess.
func TestAReportWithoutAnEffortShowsNone(t *testing.T) {
	h := startWith(t, tempDir(t), func(cfg *daemon.Config) {
		ad := fake.New(fake.Turn(false))
		ad.Confirms = func(provider.SessionSpec) []provider.Event {
			return []provider.Event{provider.Ran{Model: "claude-sonnet-5-5"}}
		}
		cfg.Providers["claude"] = ad
	})
	sendNew(h, "Say hi.")
	h.until("the report", func() bool {
		v := h.a.current().View
		return v.Confirmed.Model != nil && v.LastTurn != nil
	})
	pickerSettle(t, h)
	for _, lane := range []string{"the plugin's", "the built-in"} {
		if !h.tt.HasText("Claude Sonnet 5.5") || h.tt.HasText("Medium") {
			t.Errorf("%s button shows %q, want Claude Sonnet 5.5 and no effort", lane, h.tt.Texts())
		}
		if err := h.a.plugins.SetEnabled("model-picker", false); err != nil {
			t.Fatal(err)
		}
		h.a.pluginsChanged(nil)
		pickerSettle(t, h)
	}
}

// A thread that follows the program's default runs at the efforts of the
// model the program reported: the picker and threads.configure offer them.
func TestAFollowingThreadRunsAtItsReportedModelsEfforts(t *testing.T) {
	dir := tempDir(t)
	os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model":{"provider":"codex","id":""}}`), 0o600)
	h := startWith(t, dir, withCodex(t, "hi"))
	allAnswered(h)
	sendNew(h, "Say hi.")
	h.until("the report", func() bool {
		v := h.a.current().View
		return v.Confirmed.Model != nil && v.LastTurn != nil
	})
	v := h.a.current().View
	if v.Confirmed.Model.Model != "gpt-6.2-sol" || v.Model.Model != "" {
		t.Fatalf("the thread asked %q and ran %q, want none and gpt-6.2-sol", v.Model.Model, v.Confirmed.Model.Model)
	}
	h.a.threadControls(h.a.current(), func() {
		if !slices.ContainsFunc(h.a.efforts(), func(e effortInfo) bool { return e.id == "xhigh" }) {
			t.Errorf("the picker offers %v, not GPT-6.2 Sol's xhigh", h.a.efforts())
		}
	})
	if _, err := h.a.configureEffect([]byte(`{"thread":"` + v.ID.String() + `","effort":"xhigh"}`)); err != nil {
		t.Errorf("threads.configure refused GPT-6.2 Sol's xhigh: %v", err)
	}
}

// Picking the model a following thread's program reported pins it: the
// thread's later turns name it, in either picker.
func TestPickingTheReportedModelPinsIt(t *testing.T) {
	for _, native := range []bool{false, true} {
		lane := "built-in"
		if native {
			lane = "native"
		}
		t.Run(lane, func(t *testing.T) {
			dir := tempDir(t)
			if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model":{"provider":"codex","id":""}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			h := startWith(t, dir, withCodex(t, "hi"))
			allAnswered(h)
			sendNew(h, "first")
			h.until("confirmed Sol", func() bool {
				v := h.a.current().View
				return v.LastTurn != nil && v.Confirmed.Model != nil && v.Confirmed.Model.Model == "gpt-6.2-sol"
			})
			if h.a.current().View.Model.Model != "" {
				t.Fatal("fixture did not follow")
			}
			if native {
				if err := h.a.plugins.SetEnabled("model-picker", false); err != nil {
					t.Fatal(err)
				}
				h.a.pluginsChanged(nil)
			}
			pickerSettle(t, h)
			h.click("Model")
			pickerSettle(t, h)
			h.click("GPT-6.2 Sol")
			pickerSettle(t, h)
			h.a.draft = "second"
			h.a.send(h.a.current())
			log := os.Getenv(codextest.Log)
			h.until("next turn/start", func() bool { return len(wireEntries(t, log, "turn/start")) >= 2 })
			var p map[string]any
			json.Unmarshal(wireEntries(t, log, "turn/start")[1], &p)
			if p["model"] != "gpt-6.2-sol" {
				t.Errorf("clicked Sol; persisted model=%q, next turn model=%v effort=%v", h.a.current().View.Model.Model, p["model"], p["effort"])
			}
		})
	}
}

// An effort saved in an older spelling runs as its id on a program that
// matches ids exactly.
func TestAnEffortSavedInAnOldSpellingRunsAsItsID(t *testing.T) {
	dir := tempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model":{"provider":"antigravity","id":""},"effort":"Low"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := startWith(t, dir, withAntigravity(t, "gui"))
	allAnswered(h)
	if got := h.a.modelRef().Effort; got != "low" {
		t.Fatalf("picker did not recognize old Low value: %q", got)
	}
	sendNew(h, "local fixture")
	h.until("provider confirmed effort", func() bool { return h.a.current().View.Confirmed.Model != nil })
	v := h.a.current().View
	if v.Confirmed.Model.Effort != "low" {
		t.Errorf("legacy Low persisted as %q and Antigravity confirmed %q", v.Model.Effort, v.Confirmed.Model.Effort)
	}
	if len(wireEntries(t, os.Getenv(acptest.Log), "session/prompt")) < 1 {
		h.until("ACP prompt", func() bool { return len(wireEntries(t, os.Getenv(acptest.Log), "session/prompt")) >= 1 })
	}
}

// Changing only a following thread's effort leaves its model to its
// program.
func TestAnEffortChangeLeavesTheModelToTheProgram(t *testing.T) {
	dir := tempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model":{"provider":"codex","id":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := startWith(t, dir, withCodex(t, "hi"))
	allAnswered(h)
	sendNew(h, "first")
	h.until("confirmed Sol", func() bool {
		v := h.a.current().View
		return v.LastTurn != nil && v.Confirmed.Model != nil && v.Confirmed.Model.Model == "gpt-6.2-sol"
	})
	h.a.threadControls(h.a.current(), func() { h.a.effort = "xhigh" })
	h.until("effort saved", func() bool { return h.a.current().View.Model.Effort == "xhigh" })
	h.a.draft = "second"
	h.a.send(h.a.current())
	log := os.Getenv(codextest.Log)
	h.until("next turn/start", func() bool { return len(wireEntries(t, log, "turn/start")) >= 2 })
	var p map[string]any
	json.Unmarshal(wireEntries(t, log, "turn/start")[1], &p)
	if p["model"] != nil || p["effort"] != "xhigh" {
		t.Errorf("effort-only edit; actual model=%v effort=%v", p["model"], p["effort"])
	}
}
