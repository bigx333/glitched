package main

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/cliupdate"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// latestIs is a version source that says version for every CLI.
type latestIs string

func (l latestIs) Latest(context.Context, cliupdate.Source) (string, error) { return string(l), nil }

// versioned is a provider whose program is at path, at version.
type versioned struct {
	provider.Adapter
	version, path string
}

func (v versioned) Describe(ctx context.Context) (provider.Info, error) {
	info, err := v.Adapter.Describe(ctx)
	info.Version, info.Path = v.version, v.path
	return info, err
}

// agyAt is a home with agy installed as its installer puts it, and an
// engine config whose Antigravity runs it at version, with latest
// published.
func agyAt(t *testing.T, version, latest string) (home string, engine func(*daemon.Config)) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, ".local", "bin", "agy")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	return home, func(c *daemon.Config) {
		c.Versions = latestIs(latest)
		inner := versioned{cmp.Or(c.Providers["antigravity"], c.Providers["fake"]), version, bin}
		c.Providers["antigravity"] = reporting{inner, []provider.Model{{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash"}}}
	}
}

// pending has the local engine say of each provider that it runs
// installed and could run latest, as an Update would.
func pending(h *harness, providers ...string) {
	for _, p := range providers {
		h.a.models.served[p] = transport.ProviderInfo{Name: p, Info: provider.Info{Version: "1.0.0", Path: "/bin/" + p}, Update: &cliupdate.Status{
			Installed: "1.0.0", Latest: "1.1.0", Available: true, Command: []string{"/bin/" + p, "update"},
		}}
	}
	h.tt.Frame()
}

// updated is p's info once its update to 1.1.0 is in.
func updated(p string) transport.ProviderInfo {
	return transport.ProviderInfo{Name: p, Info: provider.Info{Version: "1.1.0", Path: "/bin/" + p}, Update: &cliupdate.Status{Installed: "1.1.0", Latest: "1.1.0"}}
}

// An agy older than its latest shows at the foot of the sidebar as soon
// as the engine answers, without Settings opened, and in its row there.
func TestAPendingCLIUpdateShowsInTheSidebar(t *testing.T) {
	_, engine := agyAt(t, "1.5.14", "1.5.15")
	h := startWith(t, tempDir(t), engine)
	h.until("the update", func() bool { return showing(h, "Agent CLI updates") })
	h.clickPlugin("Agent CLI updates")
	h.tt.Advance(time.Second)
	for _, s := range []string{"CLI updates", "Antigravity", "1.5.14 → 1.5.15", "Update"} {
		if !h.tt.HasText(s) {
			t.Errorf("the list doesn't show %q: %q", s, h.tt.Texts())
		}
	}
	h.clickPlugin("Agent CLI updates")
	openProviders(h, "antigravity")
	for _, s := range []string{"Update available", "Update to 1.5.15"} {
		if !h.tt.HasText(s) {
			t.Errorf("the Providers page doesn't show %q: %q", s, h.tt.Texts())
		}
	}
}

// A CLI at its latest version shows no update anywhere.
func TestACLIAtItsLatestShowsNoUpdate(t *testing.T) {
	_, engine := agyAt(t, "1.5.15", "1.5.15")
	h := startWith(t, tempDir(t), engine)
	h.until("antigravity's versions", func() bool { return h.a.models.served["antigravity"].Update != nil })
	h.tt.Frame()
	if showing(h, "Agent CLI updates") {
		t.Errorf("a current CLI shows the sidebar's update button: %q", h.tt.Texts())
	}
	openProviders(h, "antigravity")
	for _, s := range []string{"Agent CLI updates", "Update available"} {
		if h.tt.HasText(s) {
			t.Errorf("a current CLI shows %q", s)
		}
	}
	if !h.tt.HasText("v1.5.15") {
		t.Errorf("the row doesn't show the version: %q", h.tt.Texts())
	}
}

// With the check off, the engine looks nothing up and nothing shows.
func TestTheCLICheckTurnsOff(t *testing.T) {
	_, engine := agyAt(t, "1.5.14", "1.5.15")
	dir := tempDir(t)
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"checkCLIUpdates":false}`), 0o600)
	h := startWith(t, dir, engine)
	allAnswered(h)
	if u := h.a.models.served["antigravity"].Update; u != nil {
		t.Fatalf("the check is off, and the engine checked: %+v", u)
	}
	h.tt.Frame()
	if showing(h, "Agent CLI updates") {
		t.Errorf("the check is off, and the sidebar shows its update button: %q", h.tt.Texts())
	}
	openProviders(h, "antigravity")
	if showing(h, "Agent CLI updates") || h.tt.HasText("Update available") {
		t.Error("the check is off, and an update shows")
	}
	h.click("Check for CLI updates")
	h.until("the update", func() bool { return h.tt.HasText("Update to 1.5.15") })
}

// The sidebar's update stays, with why, when the update fails, and goes
// once the last CLI is current.
func TestTheSidebarsUpdateStaysTillTheLastCLIIsCurrent(t *testing.T) {
	h := start(t, tempDir(t))
	allAnswered(h)
	type call struct {
		name     string
		whenIdle bool
	}
	var calls []call
	answer := map[string]transport.UpdateResult{
		"claude": {Error: "/bin/claude update failed: exit status 1.", Output: "no space left on device", Provider: h.a.models.served["claude"]},
		"codex":  {Output: "updated to 1.1.0", Provider: updated("codex")},
	}
	h.a.updateRunner = runs(func(e *client.Env, name string, whenIdle bool, done func(transport.UpdateResult, error)) {
		calls = append(calls, call{name, whenIdle})
		res := answer[name]
		h.a.post(func() { done(res, nil) })
	})
	pending(h, "claude", "codex")
	answer["claude"] = transport.UpdateResult{Error: "/bin/claude update failed: exit status 1.", Output: "no space left on device", Provider: h.a.models.served["claude"]}
	if !showing(h, "Agent CLI updates") || !showing(h, "2") {
		t.Fatalf("two pending updates don't show: %q", h.tt.Texts())
	}
	h.clickPlugin("Agent CLI updates")
	h.tt.Advance(time.Second)
	h.click("Update all")
	h.until("both updates", func() bool { return len(calls) == 2 && !h.tt.HasText("Updating…") })
	if !showing(h, "Agent CLI updates") {
		t.Errorf("after one failed, the sidebar's update went: %q", h.tt.Texts())
	}
	for _, s := range []string{"The update failed: /bin/claude update failed: exit status 1.", "no space left on device"} {
		if !h.tt.HasText(s) {
			t.Errorf("after one failed, %q doesn't show: %q", s, h.tt.Texts())
		}
	}
	if p := h.a.pendingCLIs(); len(p) != 1 || p[0].key.provider != "claude" {
		t.Errorf("after codex's update, pending are %+v, want claude's alone", p)
	}
	answer["claude"] = transport.UpdateResult{Output: "updated to 1.1.0", Provider: updated("claude")}
	h.click("Update")
	h.until("the last update", func() bool { return !showing(h, "Agent CLI updates") })
	if len(h.a.pendingCLIs()) != 0 {
		t.Errorf("updates still pend: %+v", h.a.pendingCLIs())
	}
	for _, c := range calls {
		if c.whenIdle {
			t.Errorf("%s was updated when idle, and the user asked now", c.name)
		}
	}
}

// An update a turn holds off says so, and the user can have it run once
// the turn ends.
func TestAnUpdateRefusedForARunningTurnOffersToWait(t *testing.T) {
	h := start(t, tempDir(t))
	allAnswered(h)
	var idle []bool
	h.a.updateRunner = runs(func(e *client.Env, name string, whenIdle bool, done func(transport.UpdateResult, error)) {
		idle = append(idle, whenIdle)
		res := transport.UpdateResult{Busy: true, Error: "busy", Provider: h.a.models.served[name]}
		if whenIdle {
			res = transport.UpdateResult{Provider: updated(name)}
		}
		h.a.post(func() { done(res, nil) })
	})
	pending(h, "claude")
	h.clickPlugin("Agent CLI updates")
	h.tt.Advance(time.Second)
	h.click("Update")
	h.until("the refusal", func() bool { return h.tt.HasText("Update when it ends") })
	if !h.tt.HasText("Claude has a turn running on this machine, so it can't update now.") {
		t.Errorf("the refusal doesn't say why: %q", h.tt.Texts())
	}
	h.click("Update when it ends")
	h.until("the update", func() bool { return !showing(h, "Agent CLI updates") })
	if len(idle) != 2 || idle[0] || !idle[1] {
		t.Errorf("the updates asked were when idle %v, want [false true]", idle)
	}
}

// An update Glitched can't run shows the command to copy, and no Update.
func TestAnUpdateGlitchedWontRunShowsTheCommand(t *testing.T) {
	h := start(t, tempDir(t))
	allAnswered(h)
	ran := false
	h.a.updateRunner = runs(func(*client.Env, string, bool, func(transport.UpdateResult, error)) { ran = true })
	h.a.models.served["codex"] = transport.ProviderInfo{Name: "codex", Info: provider.Info{Version: "0.170.0", Path: "/usr/local/bin/codex"}, Update: &cliupdate.Status{
		Installed: "0.170.0", Latest: "0.171.0", Available: true,
		Command: []string{"sudo", "npm", "install", "-g", "@openai/codex@latest"},
		Manual:  "Codex is installed where only an administrator can change it.",
	}}
	h.tt.Frame()
	h.clickPlugin("Agent CLI updates")
	h.tt.Advance(time.Second)
	for _, s := range []string{"sudo npm install -g @openai/codex@latest", "Copy", "Codex is installed where only an administrator can change it. Run this in Terminal on this machine:"} {
		if !h.tt.HasText(s) {
			t.Errorf("the list doesn't show %q: %q", s, h.tt.Texts())
		}
	}
	if err := h.tt.Click("Update"); err == nil || ran {
		t.Error("Glitched offers to run an update it can't")
	}
}

// A paired machine's providers show on the Providers page with that
// machine's versions, and its update in the sidebar's list.
func TestAPairedMachineShowsItsOwnCLIVersions(t *testing.T) {
	_, remote := agyAt(t, "1.5.14", "1.5.15")
	rdir := tempDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	cfg := daemon.Config{Dir: rdir, Listen: []string{"127.0.0.1:0"}, Hub: true, Providers: daemon.FakeProviders(fake.New(nil))}
	remote(&cfg)
	r, err := startEngine(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	h := start(t, tempDir(t))
	if err := h.addServer(client.Server{Label: "devbox", Addr: r.Addrs()[0].String(), Token: token}); err != nil {
		t.Fatal(err)
	}
	h.until("devbox's update", func() bool { return showing(h, "Agent CLI updates") })
	openProviders(h)
	h.until("devbox's rows", func() bool { return h.tt.HasText("DEVBOX") })
	for _, s := range []string{"DEVBOX", "v1.5.14", "Update to 1.5.15"} {
		if !h.tt.HasText(s) {
			t.Errorf("the page doesn't show %q: %q", s, h.tt.Texts())
		}
	}
}

// A failed update whose provider then can't say its versions keeps the
// update it still needs, and says why it failed.
func TestAFailedUpdateKeepsItsNoticeThoughTheVersionIsUnknown(t *testing.T) {
	h := start(t, tempDir(t))
	allAnswered(h)
	pending(h, "claude")
	h.a.updateRunner = runs(func(_ *client.Env, n string, _ bool, done func(transport.UpdateResult, error)) {
		h.a.post(func() {
			done(transport.UpdateResult{Error: "updater failed", Output: "no space", Provider: transport.ProviderInfo{Name: n, Error: "the probe after it failed"}}, nil)
		})
	})
	h.a.updateCLI(h.a.pendingCLIs()[0], false)
	h.until("the update's end", func() bool { return !h.a.clis.runs[cliKey{h.local(), "claude"}].running })
	openProviders(h, "claude")
	if !h.tt.HasText("Update to 1.1.0") || !h.tt.HasText("The update failed: updater failed") {
		t.Errorf("a failed update lost its notice: %q", h.tt.Texts())
	}
}

// Two machines of one name are two machines: an update of each runs on
// each, and a machine removed takes its updates with it.
func TestCLIUpdatesKeepMachinesOfOneNameApart(t *testing.T) {
	dir := tempDir(t)
	listServers(t, dir, client.Server{Label: "devbox", Addr: "missing port"})
	h := start(t, dir)
	allAnswered(h)
	h.local().Name = "devbox"
	pending(h, "claude")
	remote := h.a.backend.Envs()[1]
	h.a.clis.served[remote] = []transport.ProviderInfo{h.a.models.served["claude"]}
	var on []*client.Env
	h.a.updateRunner = runs(func(e *client.Env, _ string, _ bool, _ func(transport.UpdateResult, error)) { on = append(on, e) })
	for _, en := range h.a.pendingCLIs() {
		h.a.updateCLI(en, false)
	}
	if len(on) != 2 || on[0] == on[1] {
		t.Fatalf("updates ran on %v, want one on each machine", on)
	}
	if err := h.a.backend.RemoveServer("devbox"); err != nil {
		t.Fatal(err)
	}
	listServers(t, dir, client.Server{Label: "devbox", Addr: "missing port"})
	h.a.askCLIs()
	for _, en := range h.a.pendingCLIs() {
		if en.env.Kind == client.Remote {
			t.Errorf("a removed machine's update stays: %+v", en)
		}
	}
}

// Check again, or the check turned on, while an ask runs asks again
// once it's done, rather than waiting for the next.
func TestTurningTheCheckOnMidAskChecks(t *testing.T) {
	_, engine := agyAt(t, "1.5.14", "1.5.15")
	dir := tempDir(t)
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"checkCLIUpdates":false}`), 0o600)
	gate := make(chan struct{})
	h := startWith(t, dir, func(c *daemon.Config) {
		engine(c)
		r := c.Providers["antigravity"].(reporting)
		c.Providers["antigravity"] = reporting{slowDescribe{r.Adapter, gate}, r.models}
	})
	h.until("the unchecked ask", func() bool { return h.a.models.asking })
	openProviders(h)
	h.click("Check for CLI updates")
	close(gate)
	h.until("the update", func() bool { return h.tt.HasText("Update available") })
}

// slowDescribe describes once gate closes.
type slowDescribe struct {
	provider.Adapter
	gate chan struct{}
}

func (s slowDescribe) Describe(ctx context.Context) (provider.Info, error) {
	select {
	case <-s.gate:
		return s.Adapter.Describe(ctx)
	case <-ctx.Done():
		return provider.Info{}, ctx.Err()
	}
}

// runs is an updateRunner that runs f and can't be given up.
func runs(f func(*client.Env, string, bool, func(transport.UpdateResult, error))) func(*client.Env, string, bool, func(transport.UpdateResult, error)) func() {
	return func(e *client.Env, name string, whenIdle bool, done func(transport.UpdateResult, error)) func() {
		f(e, name, whenIdle, done)
		return func() {}
	}
}

// An update waiting for a turn to end can be given up, and then never
// runs.
func TestAnUpdateWaitingForATurnCanBeCancelled(t *testing.T) {
	h := start(t, tempDir(t))
	allAnswered(h)
	ran := false
	h.a.updateRunner = func(_ *client.Env, name string, whenIdle bool, done func(transport.UpdateResult, error)) func() {
		if !whenIdle {
			res := transport.UpdateResult{Busy: true, Error: "busy", Provider: h.a.models.served[name]}
			h.a.post(func() { done(res, nil) })
			return func() {}
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-ctx.Done()
			h.a.post(func() { done(transport.UpdateResult{}, ctx.Err()) })
		}()
		return func() {
			if ctx.Err() == nil {
				cancel()
				return
			}
			ran = true
		}
	}
	pending(h, "claude")
	h.clickPlugin("Agent CLI updates")
	h.tt.Advance(time.Second)
	h.click("Update")
	h.until("the refusal", func() bool { return h.tt.HasText("Update when it ends") })
	h.click("Update when it ends")
	h.until("the wait", func() bool { return h.tt.HasText("Waiting for the turn to end…") })
	h.click("Cancel")
	h.until("the wait's end", func() bool { return !h.tt.HasText("Waiting for the turn to end…") })
	if ran || !h.tt.HasText("1.0.0 → 1.1.0") || h.tt.HasText("The update failed") {
		t.Errorf("a cancelled wait didn't leave the update as it was: %q", h.tt.Texts())
	}
}

// An answer that can't say the version installed, as when the probe
// failed, doesn't take a known update away.
func TestAProbeThatCantSayItsVersionKeepsTheUpdate(t *testing.T) {
	h := startWith(t, tempDir(t), func(c *daemon.Config) {
		c.Providers["claude"] = reporting{versioned{c.Providers["claude"], "", ""}, []provider.Model{{ID: "claude-opus-5-5", Name: "Opus"}}}
	})
	allAnswered(h)
	pending(h, "claude")
	h.a.checkProviders()
	h.until("the answer", func() bool { return !h.a.models.asking && !h.a.models.next.IsZero() })
	h.tt.Frame()
	if !showing(h, "Agent CLI updates") {
		t.Errorf("an answer without a version took the update away: %q", h.tt.Texts())
	}
}

// A machine paired again under its label reaches another engine, whose
// CLIs the window doesn't know till it answers.
func TestAMachinePairedAgainForgetsTheOldEnginesUpdates(t *testing.T) {
	dir := tempDir(t)
	listServers(t, dir, client.Server{Label: "devbox", Addr: "missing port"})
	h := start(t, dir)
	allAnswered(h)
	remote := h.a.backend.Envs()[1]
	remote.ID = api.NewID()
	h.a.clis.served[remote] = []transport.ProviderInfo{{Name: "claude", Info: provider.Info{Version: "1.0.0"}, Update: &cliupdate.Status{Installed: "1.0.0", Latest: "1.1.0", Available: true}}}
	h.a.clis.engine[remote] = remote.ID
	if len(h.a.pendingCLIs()) != 1 {
		t.Fatalf("the fixture's update doesn't pend: %+v", h.a.pendingCLIs())
	}
	remote.ID = api.NewID()
	h.tt.Frame()
	if p := h.a.pendingCLIs(); len(p) != 0 {
		t.Errorf("another engine under the label kept the old one's update: %+v", p)
	}
}

// A paired machine whose engine can't say its providers is asked again
// after modelsRetry, not on every frame: the window settles meanwhile.
func TestAMachineThatCantSayItsCLIsIsAskedAgainLater(t *testing.T) {
	dir := tempDir(t)
	_, addr := serverNode(t)
	listServers(t, dir, client.Server{Label: "build-box", Addr: addr, Token: "any"})
	h := start(t, dir)
	var asked time.Time
	h.until("build-box's providers to fail", func() bool {
		e := h.a.serverEnv("build-box")
		asked = h.a.clis.next[e]
		return e != nil && !h.a.clis.asking[e] && !asked.IsZero() && h.a.clis.engine[e] == api.ID{}
	})
	for range 5 {
		h.tt.Frame()
		settleTester(h.tt)
	}
	if next := h.a.clis.next[h.a.serverEnv("build-box")]; !next.Equal(asked) {
		t.Errorf("build-box was asked again at once: next %v, then %v", asked, next)
	}
}

// hoverCLIButton rests the pointer on the sidebar's CLI update button
// till its tooltip shows, and returns the tooltip's text.
func hoverCLIButton(h *harness) string {
	h.t.Helper()
	r, ok := h.tt.Find("Agent CLI updates")
	if !ok {
		h.t.Fatalf("no CLI update button: %q", h.tt.Texts())
	}
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Advance(2 * time.Second)
	for _, s := range h.tt.Texts() {
		if strings.Contains(s, "Claude Code") || strings.Contains(s, "Codex ") || strings.Contains(s, "This machine") {
			return s
		}
	}
	h.t.Fatalf("the button shows no tooltip: %q", h.tt.Texts())
	return ""
}

// The sidebar has one compact update button however many CLIs are
// behind, with their count when more than one is, and its tooltip names
// each, under its machine when the user has more than one.
func TestTheCLIUpdateButtonIsOneButtonHoweverManyArePending(t *testing.T) {
	dir := tempDir(t)
	h := start(t, dir)
	allAnswered(h)
	pending(h, "claude")
	if !showing(h, "Updates") || showing(h, "1") {
		t.Errorf("one pending CLI doesn't show a plain Updates: %q", h.tt.Texts())
	}
	if tip := hoverCLIButton(h); tip != "Claude Code 1.0.0 → 1.1.0" {
		t.Errorf("the tooltip says %q", tip)
	}

	dir = tempDir(t)
	listServers(t, dir, client.Server{Label: "build-box", Addr: "missing port"})
	h = start(t, dir)
	allAnswered(h)
	remote := h.a.serverEnv("build-box")
	h.a.clis.served[remote] = []transport.ProviderInfo{{Name: "antigravity", Info: provider.Info{Version: "1.5.14"}, Update: &cliupdate.Status{Installed: "1.5.14", Latest: "1.5.15", Available: true}}}
	h.a.clis.engine[remote] = remote.ID
	pending(h, "claude", "codex")
	if !showing(h, "Updates") || !showing(h, "3") || h.tt.HasText("Updates for") {
		t.Errorf("three pending CLIs don't show one Updates with their count: %q", h.tt.Texts())
	}
	want := "This machine\nClaude Code 1.0.0 → 1.1.0\nCodex 1.0.0 → 1.1.0\n\nbuild-box\nAntigravity 1.5.14 → 1.5.15"
	if tip := hoverCLIButton(h); tip != want {
		t.Errorf("the tooltip says\n%s\nwant\n%s", tip, want)
	}
}

// The button shows an update running, and one that failed or needs the
// user, and its tooltip says which.
func TestTheCLIUpdateButtonSaysWhichUpdateRunsFailedOrNeedsYou(t *testing.T) {
	h := start(t, tempDir(t))
	allAnswered(h)
	pending(h, "claude", "codex")
	local := h.a.backend.Envs()[0]
	h.a.models.served["codex"].Update.Manual = "Codex is installed where only an administrator can change it."
	h.a.clis.runs[cliKey{local, "claude"}] = &cliRun{running: true}
	settleTester(h.tt)
	if !showing(h, "Updating") {
		t.Errorf("an update running doesn't show: %q", h.tt.Texts())
	}
	if tip, want := hoverCLIButton(h), "Claude Code 1.0.0 → 1.1.0 · updating\nCodex 1.0.0 → 1.1.0 · update it yourself"; tip != want {
		t.Errorf("the tooltip says %q, want %q", tip, want)
	}
	h.a.clis.runs[cliKey{local, "claude"}] = &cliRun{failed: "claude update failed: exit status 1."}
	h.tt.Move(0, 0)
	settleTester(h.tt)
	if showing(h, "Updating") || !showing(h, "Updates") {
		t.Errorf("a failed update still shows as running: %q", h.tt.Texts())
	}
	if tip, want := hoverCLIButton(h), "Claude Code 1.0.0 → 1.1.0 · update failed\nCodex 1.0.0 → 1.1.0 · update it yourself"; tip != want {
		t.Errorf("the tooltip says %q, want %q", tip, want)
	}
}

// A CLI whose engine, asked again mid-update, can't say its versions
// stays on the button as updating, with no versions to name.
func TestAnUpdatingCLIWithNoVersionsStaysOnTheButton(t *testing.T) {
	h := start(t, tempDir(t))
	allAnswered(h)
	pending(h, "claude")
	h.a.clis.runs[cliKey{h.a.backend.Envs()[0], "claude"}] = &cliRun{running: true}
	p := h.a.models.served["claude"]
	p.Update = nil
	h.a.models.served["claude"] = p
	settleTester(h.tt)
	if !showing(h, "Updating") {
		t.Errorf("the update running doesn't show: %q", h.tt.Texts())
	}
	if tip := hoverCLIButton(h); tip != "Claude Code · updating" {
		t.Errorf("the tooltip says %q", tip)
	}
}

// An update Glitched runs says the command it will run before the click,
// as the Update button's tooltip, in Settings › Providers and in the
// sidebar's list.
func TestAnUpdateShowsItsCommandBeforeItRuns(t *testing.T) {
	h := start(t, tempDir(t))
	allAnswered(h)
	pending(h, "claude")
	tip := func() string {
		t.Helper()
		r, ok := h.tt.Find("Update")
		if !ok {
			t.Fatalf("no Update button: %q", h.tt.Texts())
		}
		h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
		h.tt.Advance(2 * time.Second)
		for _, s := range h.tt.Texts() {
			if strings.Contains(s, "Runs ") {
				return s
			}
		}
		t.Fatalf("Update shows no command: %q", h.tt.Texts())
		return ""
	}
	h.clickPlugin("Agent CLI updates")
	h.tt.Advance(time.Second)
	if s := tip(); s != "Runs /bin/claude update on this machine." {
		t.Errorf("the list's Update says %q", s)
	}
	h.tt.Move(0, 0)
	h.clickPlugin("Agent CLI updates")
	openProviders(h, "claude")
	if s := tip(); s != "Runs /bin/claude update on this machine." {
		t.Errorf("the Providers Update says %q", s)
	}
}
