package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/cliupdate"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
	"glitched/internal/transport"
)

// installed is the fake, as a CLI installed at path whose version is in
// the file version, as its updater leaves it.
type installed struct {
	*fake.Adapter
	path, version string
	forgot        atomic.Int32
}

func (c *installed) Describe(context.Context) (provider.Info, error) {
	v, err := os.ReadFile(c.version)
	if err != nil {
		return provider.Info{}, err
	}
	return provider.Info{Name: "claude", Version: strings.TrimSpace(string(v)), Path: c.path}, nil
}

func (c *installed) Forget() { c.forgot.Add(1) }

// versions answers every lookup with latest, and counts them.
type versions struct {
	latest string
	asked  atomic.Int32
}

func (v *versions) Latest(context.Context, cliupdate.Source) (string, error) {
	v.asked.Add(1)
	return v.latest, nil
}

// nativeClaude installs claude 2.1.323 as its native installer does, in a
// home of its own, with updater as the script its "claude update" runs;
// the CLI's version is in $HOME/version.
func nativeClaude(t *testing.T, updater string) *installed {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	bin := filepath.Join(home, ".local/share/claude/versions/2.1.323")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+updater), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".local/bin/claude")
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.Symlink(bin, path); err != nil {
		t.Fatal(err)
	}
	version := filepath.Join(home, "version")
	os.WriteFile(version, []byte("2.1.323\n"), 0o644)
	return &installed{Adapter: fake.New(fake.Turn(true)), path: path, version: version}
}

const updates = `echo "$@" >> "$HOME/ran"
echo "Updating Claude Code to 2.1.330"
echo 2.1.330 > "$HOME/version"
`

func ran(t *testing.T) string {
	b, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), "ran"))
	return string(b)
}

func startCLIs(t *testing.T, ad provider.Adapter, lookup cliupdate.Lookup) (*Daemon, string) {
	t.Helper()
	dir := stateDir(t)
	cfg := Config{Dir: dir, Hub: true, Epoch: 1, Listen: []string{"127.0.0.1:0"}, Providers: provider.Mux{"claude": ad, "fake": fake.New(nil)}}
	if lookup != nil {
		cfg.Versions = lookup
	}
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, dir
}

func claudeOf(t *testing.T, served []transport.ProviderInfo) transport.ProviderInfo {
	t.Helper()
	for _, p := range served {
		if p.Name == "claude" {
			return p
		}
	}
	t.Fatalf("no claude in %+v", served)
	return transport.ProviderInfo{}
}

// The engine looks a CLI's latest version up only when asked to check,
// and says so to its own socket and to a paired machine on the tailnet.
func TestProvidersChecksForUpdatesOnlyWhenAsked(t *testing.T) {
	ad := nativeClaude(t, updates)
	v := &versions{latest: "2.1.330"}
	d, dir := startCLIs(t, ad, v)
	local := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer local.Close()
	ctx := context.Background()

	served, err := local.Providers(ctx, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if p := claudeOf(t, served); p.Update != nil || v.asked.Load() != 0 {
		t.Errorf("unchecked: update %+v after %d lookups, want none", p.Update, v.asked.Load())
	}
	served, err = local.Providers(ctx, false, true)
	if err != nil {
		t.Fatal(err)
	}
	p := claudeOf(t, served)
	if u := p.Update; u == nil || u.Installed != "2.1.323" || u.Latest != "2.1.330" || !u.Available || u.Manual != "" || len(u.Command) != 2 || u.Command[1] != "update" {
		t.Errorf("checked: %+v", p.Update)
	}

	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	served, err = remote(t, d, token).Providers(ctx, false, true)
	if err != nil {
		t.Fatalf("providers over the tailnet: %v", err)
	}
	if p := claudeOf(t, served); p.Update == nil || !p.Update.Available || v.asked.Load() != 1 {
		t.Errorf("over the tailnet: %+v after %d lookups, want the day's answer", p.Update, v.asked.Load())
	}
	if ran(t) != "" {
		t.Error("a check ran the updater")
	}
}

// An update runs the CLI's own updater, then asks the provider again,
// which says the new version. It runs none while a turn of the provider
// runs, unless asked to wait for the turn's end.
func TestUpdateProviderNeverRunsMidTurn(t *testing.T) {
	ad := nativeClaude(t, updates)
	d, dir := startCLIs(t, ad, &versions{latest: "2.1.330"})
	ep := d.Endpoint()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	thread := api.NewID()
	submitTo(t, ep, thread, api.ThreadCreate{Workspace: t.TempDir(), Title: "t", Model: api.ModelRef{Provider: "claude", Model: "fake"}})
	submitTo(t, ep, thread, api.MessageDispatch{Text: "look", Mode: api.StartNow})
	th := threadUntil(t, ep, thread, func(th api.Thread) bool { return th.Run == api.RunWaiting && len(th.Requests) == 1 })

	local := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer local.Close()
	res, err := local.UpdateProvider(ctx, "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Busy || !strings.Contains(res.Error, "Claude Code has a turn running") || ran(t) != "" {
		t.Fatalf("mid-turn: %+v, ran %q; want it refused and nothing run", res, ran(t))
	}

	var wg sync.WaitGroup
	var idle transport.UpdateResult
	wg.Go(func() {
		idle, err = local.UpdateProvider(ctx, "claude", true)
	})
	time.Sleep(300 * time.Millisecond)
	if ran(t) != "" {
		t.Fatal("an update when idle ran mid-turn")
	}
	submitTo(t, ep, thread, api.RequestRespond{Request: th.Requests[0].ID, Generation: th.Requests[0].Generation, Answer: []byte(`{"allow":true}`)})
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if idle.Busy || idle.Error != "" || !strings.Contains(idle.Output, "Updating Claude Code to 2.1.330") || ran(t) != "update\n" {
		t.Errorf("when idle: %+v, ran %q", idle, ran(t))
	}
	if u := idle.Provider.Update; idle.Provider.Info.Version != "2.1.330" || u == nil || u.Available {
		t.Errorf("after the update the provider says %+v, %+v; want 2.1.330, current", idle.Provider.Info, u)
	}
	if ad.forgot.Load() == 0 {
		t.Error("the adapter wasn't told to forget what it said before the update")
	}
}

func TestAFailedUpdateSaysWhyWithItsOutput(t *testing.T) {
	ad := nativeClaude(t, "echo 'Error: EACCES' >&2\nexit 1\n")
	d, _ := startCLIs(t, ad, nil)
	res, err := d.Endpoint().(interface {
		UpdateProvider(context.Context, string, bool) (transport.UpdateResult, error)
	}).UpdateProvider(context.Background(), "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "exit status 1") || !strings.Contains(res.Output, "Error: EACCES") || res.Busy {
		t.Errorf("%+v, want the exit and the output", res)
	}
	if res.Provider.Update == nil || res.Provider.Update.Latest != "" {
		t.Errorf("an engine without lookups said %+v", res.Provider.Update)
	}
}

// hostFor is a host of ad alone, with no lookups, for the updates' own
// tests.
func hostFor(t *testing.T, ad provider.Adapter) (*clis, *runtime.Host) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mux := provider.Mux{"claude": ad}
	h, err := runtime.New(context.Background(), st, mux)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return newCLIs(context.Background(), t.TempDir(), mux, h, cliupdate.NewChecker(nil, "")), h
}

// An updater stopped at its timeout takes the installer it started with
// it, before the provider's turns may start again.
func TestAnUpdaterStoppedAtItsTimeoutEndsItsChildren(t *testing.T) {
	ad := nativeClaude(t, `(/bin/sleep 2; echo finished > "$HOME/child-finished") </dev/null >/dev/null 2>&1 &
echo started > "$HOME/child-started"
wait
`)
	c, h := hostFor(t, ad)
	was := updateTimeout
	updateTimeout = time.Second
	t.Cleanup(func() { updateTimeout = was })

	res, err := c.UpdateProvider(context.Background(), "claude", false)
	if err != nil || !strings.Contains(res.Error, "was stopped") {
		t.Fatalf("%+v, %v; want it stopped at its timeout", res, err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "child-started")); err != nil {
		t.Fatalf("the installer never started: %v", err)
	}
	release, err := h.Hold(context.Background(), "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "child-finished")); err == nil {
		t.Fatal("the updater's installer wrote after its hold was released")
	}
}

// cancelling is ad, whose Describe ends the caller's request, as a window
// that goes away while the provider is asked.
type cancelling struct {
	*installed
	cancel context.CancelFunc
}

func (a cancelling) Describe(ctx context.Context) (provider.Info, error) {
	defer a.cancel()
	return a.installed.Describe(ctx)
}

func TestAnUpdateWhoseCallerWentRunsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, _ := hostFor(t, cancelling{nativeClaude(t, updates), cancel})
	if _, err := c.UpdateProvider(ctx, "claude", false); err == nil || ran(t) != "" {
		t.Errorf("%v, ran %q; want the cancel, and nothing run", err, ran(t))
	}
}

// Glitched runs no updater it can't tell is the install's own.
func TestUpdateProviderRunsNothingItCantTellHowToRun(t *testing.T) {
	ad := nativeClaude(t, updates)
	ad.path = filepath.Join(os.Getenv("HOME"), "opt/claude")
	os.MkdirAll(filepath.Dir(ad.path), 0o755)
	os.WriteFile(ad.path, []byte("#!/bin/sh\n"+updates), 0o755)
	_, dir := startCLIs(t, ad, &versions{latest: "2.1.330"})
	local := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer local.Close()
	res, err := local.UpdateProvider(context.Background(), "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "can't tell how Claude Code was installed") || res.Output != "" || ran(t) != "" {
		t.Errorf("%+v, ran %q; want it refused with nothing run", res, ran(t))
	}
	if _, err := local.UpdateProvider(context.Background(), "nobody", false); err == nil {
		t.Errorf("an update of a provider the engine doesn't serve: %v", err)
	}
}

func submitTo(t *testing.T, ep api.Endpoint, thread api.ID, body api.Intent) {
	t.Helper()
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: body}); err != nil {
		t.Fatal(err)
	}
}

func threadUntil(t *testing.T, ep api.Endpoint, thread api.ID, ok func(api.Thread) bool) api.Thread {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		snap, err := ep.Read(context.Background(), api.Query{Thread: thread})
		if err == nil && len(snap.Threads) == 1 && ok(snap.Threads[0]) {
			return snap.Threads[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %+v %v", snap.Threads, err)
		}
	}
}

// Two windows that showed the same Update run its updater once: the
// second finds the CLI at the latest version and runs nothing.
func TestAnUpdateOfACurrentCLIRunsNothing(t *testing.T) {
	ad := nativeClaude(t, updates)
	c, _ := hostFor(t, ad)
	c.checker = cliupdate.NewChecker(&versions{latest: "2.1.330"}, filepath.Join(t.TempDir(), "cli-versions.json"))
	for i := range 2 {
		res, err := c.UpdateProvider(context.Background(), "claude", false)
		if err != nil || res.Error != "" || res.Provider.Update == nil || res.Provider.Update.Available {
			t.Fatalf("update %d: %+v, %v; want the CLI current", i+1, res, err)
		}
	}
	if got := ran(t); got != "update\n" {
		t.Errorf("the updater ran %q, want once", got)
	}
}

// An update that waited for a turn plans again once it holds the
// provider: the CLI may have been installed another way meanwhile.
func TestAnUpdateThatWaitedPlansAgain(t *testing.T) {
	ad := nativeClaude(t, updates)
	c, h := hostFor(t, ad)
	planned := asked{done: make(chan struct{})}
	c.checker = cliupdate.NewChecker(&planned, filepath.Join(t.TempDir(), "cli-versions.json"))
	thread := api.NewID()
	submitTo(t, h, thread, api.ThreadCreate{Workspace: t.TempDir(), Title: "t", Model: api.ModelRef{Provider: "claude", Model: "fake"}})
	submitTo(t, h, thread, api.MessageDispatch{Text: "look", Mode: api.StartNow})
	th := threadUntil(t, h, thread, func(th api.Thread) bool { return len(th.Requests) == 1 })

	type answer struct {
		res transport.UpdateResult
		err error
	}
	done := make(chan answer, 1)
	go func() {
		res, err := c.UpdateProvider(context.Background(), "claude", true)
		done <- answer{res, err}
	}()
	select {
	case <-planned.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the update never planned")
	}
	home := os.Getenv("HOME")
	other := filepath.Join(home, "opt/claude")
	os.MkdirAll(filepath.Dir(other), 0o755)
	os.WriteFile(other, []byte("#!/bin/sh\necho ran > \"$HOME/other-ran\"\n"), 0o755)
	os.Remove(ad.path)
	if err := os.Symlink(other, ad.path); err != nil {
		t.Fatal(err)
	}
	submitTo(t, h, thread, api.RequestRespond{Request: th.Requests[0].ID, Generation: th.Requests[0].Generation, Answer: []byte(`{"allow":true}`)})
	got := <-done
	if _, err := os.Stat(filepath.Join(home, "other-ran")); err == nil || ran(t) != "" {
		t.Fatalf("the update ran the plan from before its wait: %+v", got.res)
	}
	if got.err != nil || !strings.Contains(got.res.Error, "can't tell how Claude Code was installed") {
		t.Errorf("%+v, %v; want the install as it is now refused", got.res, got.err)
	}
}

// asked tells when the first plan of an update has detected the install
// and looks up the latest version; it knows none.
type asked struct {
	once sync.Once
	done chan struct{}
}

func (a *asked) Latest(context.Context, cliupdate.Source) (string, error) {
	a.once.Do(func() { close(a.done) })
	return "", errors.New("offline")
}

// placed is installed as a provider whose launch moves its folder by
// CLAUDE_CONFIG_DIR, as Claude Code's does.
type placed struct{ *installed }

func (placed) Placement() provider.Placement {
	return provider.Placement{Names: []string{"claude"}, HomeVar: "CLAUDE_CONFIG_DIR", DefaultHome: "~/.claude"}
}
func (placed) Place(provider.Launch)                                     {}
func (placed) Version(context.Context, provider.Program) (string, error) { return "2.1.323", nil }
func (placed) Command(prog provider.Program) []string                    { return []string{prog.Path} }

// The updater runs with the CLI's folder as engine.json's launch moves it,
// so it updates the install that launch runs.
func TestTheUpdaterRunsWithTheLaunchsFolder(t *testing.T) {
	ad := nativeClaude(t, `echo "$CLAUDE_CONFIG_DIR" > "$HOME/config-dir"
`+updates)
	c, _ := hostFor(t, placed{ad})
	c.dir = t.TempDir()
	folder := filepath.Join(os.Getenv("HOME"), "work-claude")
	os.MkdirAll(folder, 0o755)
	if _, err := Edit(c.dir, func(cfg *Config) error {
		cfg.Launch = map[string]provider.Launch{"claude": {Home: folder}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(os.Getenv("HOME"), "elsewhere"))
	if res, err := c.UpdateProvider(context.Background(), "claude", false); err != nil || res.Error != "" {
		t.Fatalf("%+v, %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), "config-dir")); strings.TrimSpace(string(b)) != folder {
		t.Errorf("the updater ran with CLAUDE_CONFIG_DIR %q, want the launch's %q", strings.TrimSpace(string(b)), folder)
	}
}
