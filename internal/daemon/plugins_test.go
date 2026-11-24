package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/pack"
	"glitched/internal/provider"
	"glitched/internal/provider/codex"
	"glitched/internal/provider/codex/codextest"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// codexAt is an engine on dir serving Codex, played by the test binary,
// with its socket's client, and its plugins' timers scaled by scale, or
// not. stop closes both, once; the test's end runs it if the test didn't.
func codexAt(t *testing.T, dir string, scale ...float64) (d *Daemon, ep *transport.EndpointClient, stop func()) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Dir: dir, Hub: true, Providers: provider.Mux{"codex": &codex.Adapter{Path: self}}}
	if len(scale) > 0 {
		cfg.PluginTimerScale = scale[0]
	}
	d, err = Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ep = transport.NewEndpointClient(Socket(dir), api.ID{})
	var once sync.Once
	stop = func() {
		once.Do(func() {
			ep.Close()
			if err := d.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return d, ep, stop
}

// installStarters installs in dir, each signed with this machine's own key,
// a plugin per id whose service starts the thread a call names with
// glitched.threads.create, granted what grants gives it.
func installStarters(t *testing.T, dir string, grants map[string][]string) {
	t.Helper()
	key, err := pack.LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	for id := range grants {
		folder := filepath.Join(plugins.Dir(dir), id)
		files := map[string]string{
			"plugin.json": `{"id": "` + id + `", "name": "` + id + `", "main": "index.js", "contract": "glitched", "contractVersion": 1, "capabilities": ["service", "threads:write", "threads:access"], "glitched": {"service": "service.js"}}`,
			"index.js":    `export function view() {}`,
			"service.js": `
				export async function serve({ args }) {
					try { return await glitched.threads.create(args.prompt, args.options) }
					catch (e) { return { code: e.code ?? "", message: e.message } }
				}`,
		}
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(folder, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := pack.Sign(folder, key); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal(map[string]any{"grants": grants})
	if err := os.WriteFile(filepath.Join(dir, "plugins.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// created is what a starter's service answers: the thread it started, or
// why it couldn't.
type created struct{ Thread, Code, Message string }

// pluginCreate is plugin's service, called over the engine's socket,
// calling glitched.threads.create(prompt, options).
func pluginCreate(t *testing.T, ep *transport.EndpointClient, plugin, prompt string, options any) created {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"plugin": plugin, "method": "create", "args": map[string]any{"prompt": prompt, "options": options}})
	out, err := ep.PluginCall(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var c created
	if err := json.Unmarshal(out, &c); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	return c
}

// mustCreate is pluginCreate's thread, which it must have started.
func mustCreate(t *testing.T, ep *transport.EndpointClient, plugin, prompt string, options any) api.ID {
	t.Helper()
	c := pluginCreate(t, ep, plugin, prompt, options)
	id, err := api.ParseID(c.Thread)
	if err != nil {
		t.Fatalf("%s's create answered %+v", plugin, c)
	}
	return id
}

// endedLog is thread's log once a turn of it has ended.
func endedLog(t *testing.T, ep api.Endpoint, thread api.ID) []api.EventBody {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	stream, err := ep.Open(ctx, api.Subscription{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for ended := false; !ended; {
		item, err := stream.Next(ctx)
		if err != nil {
			t.Fatalf("no turn of %s ended: %v", thread, err)
		}
		var events []api.Event
		switch {
		case item.Kind == api.StreamEvent:
			events = []api.Event{*item.Event}
		case item.Kind == api.StreamSnapshot && item.Snapshot != nil:
			events = item.Snapshot.Events
		}
		for _, e := range events {
			if b, err := e.Decode(); err == nil {
				if _, ended = b.(api.TurnEnded); ended {
					break
				}
			}
		}
	}
	return threadLog(t, ep, thread)
}

func threadLog(t *testing.T, ep api.Endpoint, thread api.ID) []api.EventBody {
	t.Helper()
	snap, err := ep.Read(context.Background(), api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	var out []api.EventBody
	for _, e := range snap.Events {
		b, err := e.Decode()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

// tally counts a log's creates, first messages and turns.
func tally(log []api.EventBody) (created []api.ThreadCreated, queued []api.MessageQueued, started, ended int) {
	for _, b := range log {
		switch b := b.(type) {
		case api.ThreadCreated:
			created = append(created, b)
		case api.MessageQueued:
			queued = append(queued, b)
		case api.TurnStarted:
			started++
		case api.TurnEnded:
			ended++
		}
	}
	return
}

func codexTurns(t *testing.T, log string) int {
	t.Helper()
	entries, err := codextest.Read(log)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.Method == "turn/start" {
			n++
		}
	}
	return n
}

// fakeCodex has the test binary play codex, with its wire log in a file
// the test returns, and HOME a folder of the test's.
func fakeCodex(t *testing.T) (wire string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(codextest.Scenario, "memory")
	wire = filepath.Join(t.TempDir(), "wire.jsonl")
	t.Setenv(codextest.Log, wire)
	return wire
}

var luna = map[string]any{"provider": "codex", "model": "gpt-6-luna", "effort": "low"}

// A service with threads:write alone never starts a thread above Ask for
// approval, whatever the thread the user worked in last runs at: only an
// access it names, which the host gates on threads:access, starts wider.
func TestAServiceCreateStartsAtAskUnlessItNamesAnAccess(t *testing.T) {
	fakeCodex(t)
	dir, ws := stateDir(t), t.TempDir()
	installStarters(t, dir, map[string][]string{"starter": {"service", "threads:write"}, "wide": {"service", "threads:write", "threads:access"}})
	_, ep, _ := codexAt(t, dir)
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Workspace: ws, Title: "last", Model: api.ModelRef{Provider: "codex", Model: "gpt-6-luna"}, Access: api.AccessFull}}); err != nil {
		t.Fatal(err)
	}
	accessOf := func(plugin string, options map[string]any) api.Access {
		t.Helper()
		made, _, _, _ := tally(endedLog(t, ep, mustCreate(t, ep, plugin, "go", options)))
		if len(made) != 1 {
			t.Fatalf("creates %+v", made)
		}
		return made[0].Access
	}
	if got := accessOf("starter", map[string]any{"workspace": ws}); got != api.AccessAsk {
		t.Errorf("a create naming no access started at %v, want Ask for approval (%v)", got, api.AccessAsk)
	}
	if c := pluginCreate(t, ep, "starter", "wide", map[string]any{"workspace": ws, "access": "Full access"}); c.Code != "denied" || !strings.Contains(c.Message, "threads:access") {
		t.Errorf("a create naming Full access without threads:access answered %+v, want denied", c)
	}
	if got := accessOf("wide", map[string]any{"workspace": ws, "access": "Full access"}); got != api.AccessFull {
		t.Errorf("a create naming Full access started at %v, want %v", got, api.AccessFull)
	}
	if c := pluginCreate(t, ep, "wide", "odd", map[string]any{"workspace": ws, "access": "root"}); c.Thread != "" || !strings.Contains(c.Message, "is no access") {
		t.Errorf("a create naming no level the picker offers answered %+v", c)
	}
}

// A service's create under a key makes one thread however often it comes:
// twice in a row, and again from the next engine after a restart, it
// answers the first thread, with one first message and one turn. Another
// prompt under the key is refused, and the same key from another plugin
// is another thread.
func TestAKeyedServiceCreateMakesOneThreadAcrossARestart(t *testing.T) {
	wire := fakeCodex(t)
	dir, ws := stateDir(t), t.TempDir()
	installStarters(t, dir, map[string][]string{"schedules": {"service", "threads:write"}, "schedules-copy": {"service", "threads:write"}})
	options := map[string]any{"workspace": ws, "model": luna, "key": map[string]any{"id": "nightly", "at": "2026-11-24T07:00:00Z"}}

	_, ep, stop := codexAt(t, dir)
	first := mustCreate(t, ep, "schedules", "Check the build.", options)
	if again := mustCreate(t, ep, "schedules", "Check the build.", options); again != first {
		t.Fatalf("the repeat made thread %s, want the first, %s", again, first)
	}
	endedLog(t, ep, first)
	stop()

	_, ep, _ = codexAt(t, dir)
	if again := mustCreate(t, ep, "schedules", "Check the build.", options); again != first {
		t.Fatalf("the repeat after a restart made thread %s, want the first, %s", again, first)
	}
	if c := pluginCreate(t, ep, "schedules", "Check the tests.", options); c.Thread != "" || c.Message != api.ErrConflict.Error() {
		t.Fatalf("another prompt under the key answered %+v, want a conflict", c)
	}
	snap, err := ep.Read(context.Background(), api.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Threads) != 1 || snap.Threads[0].ID != first {
		t.Fatalf("threads %+v, want %s alone", snap.Threads, first)
	}
	made, queued, started, ended := tally(threadLog(t, ep, first))
	if len(made) != 1 || len(queued) != 1 || queued[0].Text != "Check the build." || started != 1 || ended != 1 {
		t.Fatalf("the thread's log has %d creates, messages %+v, %d turns started and %d ended; want one create, one message and one turn", len(made), queued, started, ended)
	}
	if n := codexTurns(t, wire); n != 1 {
		t.Errorf("codex got %d turns, want 1", n)
	}

	other := mustCreate(t, ep, "schedules-copy", "Check the build.", options)
	if other == first {
		t.Fatal("another plugin's create under the same key answered the first plugin's thread")
	}
	endedLog(t, ep, other)
}

// The model a service's create names is the one thread.created records,
// effort and fast mode with it; one of a provider the engine doesn't run
// is refused.
func TestAServiceCreateStartsAtTheModelItNames(t *testing.T) {
	fakeCodex(t)
	dir, ws := stateDir(t), t.TempDir()
	installStarters(t, dir, map[string][]string{"starter": {"service", "threads:write"}})
	_, ep, _ := codexAt(t, dir)
	want := api.ModelRef{Provider: "codex", Model: "gpt-6-luna", Effort: "low", Fast: true}
	made, _, _, _ := tally(endedLog(t, ep, mustCreate(t, ep, "starter", "go", map[string]any{"workspace": ws, "model": want})))
	if len(made) != 1 || made[0].Model != want {
		t.Fatalf("thread.created %+v, want one at %+v", made, want)
	}
	if c := pluginCreate(t, ep, "starter", "go", map[string]any{"workspace": ws, "model": map[string]any{"provider": "nope", "model": "m"}}); c.Thread != "" || !strings.Contains(c.Message, `runs no provider "nope"`) {
		t.Fatalf("a create naming a provider the engine doesn't run answered %+v", c)
	}
}

// A service's create that names no model takes the model of the thread
// the user worked in last, as it always has.
func TestAServiceCreateWithNoModelTakesTheLastThreadsModel(t *testing.T) {
	fakeCodex(t)
	dir, ws := stateDir(t), t.TempDir()
	installStarters(t, dir, map[string][]string{"starter": {"service", "threads:write"}})
	_, ep, _ := codexAt(t, dir)
	last := api.ModelRef{Provider: "codex", Model: "gpt-6.2-sol", Effort: "high"}
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Workspace: ws, Title: "last", Model: last}}); err != nil {
		t.Fatal(err)
	}
	made, _, _, _ := tally(endedLog(t, ep, mustCreate(t, ep, "starter", "go", map[string]any{"workspace": ws})))
	if len(made) != 1 || made[0].Model != last {
		t.Fatalf("thread.created %+v, want one at the last thread's %+v", made, last)
	}
}

// project is a disposable repository with one commit and a post-checkout
// hook that appends to the file ran names.
func project(t *testing.T) (dir, ran string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "start")
	ran = filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "post-checkout"), []byte("#!/bin/sh\necho post-checkout >> '"+ran+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, ran
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// worktreesOf are the worktrees of the repository at dir besides its own.
func worktreesOf(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(git(t, dir, "worktree", "list", "--porcelain"), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok && p != dir {
			out = append(out, p)
		}
	}
	return out
}

// A keyed create in a new worktree, repeated, and repeated again after a
// restart, makes one worktree, the thread's.
func TestAKeyedServiceCreateInAWorktreeMakesOneAcrossARestart(t *testing.T) {
	fakeCodex(t)
	repo, _ := project(t)
	dir := stateDir(t)
	installStarters(t, dir, map[string][]string{"schedules": {"service", "threads:write"}})
	options := map[string]any{"workspace": repo, "model": luna, "worktree": map[string]any{}, "key": map[string]any{"id": "tidy", "at": "2026-11-24T07:00:00Z"}}

	_, ep, stop := codexAt(t, dir)
	first := mustCreate(t, ep, "schedules", "Tidy up.", options)
	if again := mustCreate(t, ep, "schedules", "Tidy up.", options); again != first {
		t.Fatalf("the repeat made thread %s, want the first, %s", again, first)
	}
	endedLog(t, ep, first)
	stop()

	_, ep, _ = codexAt(t, dir)
	if again := mustCreate(t, ep, "schedules", "Tidy up.", options); again != first {
		t.Fatalf("the repeat after a restart made thread %s, want the first, %s", again, first)
	}
	snap, err := ep.Read(context.Background(), api.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Threads) != 1 || snap.Threads[0].Project != repo || snap.Threads[0].Workspace == repo {
		t.Fatalf("threads %+v, want one in a worktree of %s", snap.Threads, repo)
	}
	if own, _ := filepath.EvalSymlinks(snap.Threads[0].Workspace); fmt.Sprint(worktreesOf(t, repo)) != fmt.Sprint([]string{own}) {
		t.Fatalf("worktrees %q besides the project's, want the thread's %s alone", worktreesOf(t, repo), own)
	}
	if _, queued, started, _ := tally(threadLog(t, ep, first)); len(queued) != 1 || started != 1 {
		t.Fatalf("the thread has %d messages and %d turns, want one of each", len(queued), started)
	}
}

// git makes a worktree a plugin asked for with the repository's hooks off,
// as it runs a plugin's own git; the user's own still runs them.
func TestAPluginsWorktreeRunsNoGitHooks(t *testing.T) {
	fakeCodex(t)
	repo, ran := project(t)
	dir := stateDir(t)
	installStarters(t, dir, map[string][]string{"starter": {"service", "threads:write"}})
	_, ep, _ := codexAt(t, dir)
	thread := mustCreate(t, ep, "starter", "go", map[string]any{"workspace": repo, "model": luna, "worktree": map[string]any{}})
	endedLog(t, ep, thread)
	if trees := worktreesOf(t, repo); len(trees) != 1 {
		t.Fatalf("worktrees %q, want the plugin's thread's", trees)
	}
	if b, err := os.ReadFile(ran); err == nil {
		t.Fatalf("the plugin's worktree ran the repository's hook: %q", b)
	}
	mine := api.NewID()
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: mine, Body: api.ThreadCreate{Workspace: repo, Title: "mine", Model: api.ModelRef{Provider: "codex", Model: "gpt-6-luna"}, Worktree: &api.NewWorktree{}}}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(ran); err != nil || string(b) != "post-checkout\n" {
		t.Fatalf("the user's own worktree ran the hook as %q, %v; want once", b, err)
	}
}

// Messages a service sends one thread, all at once and however it spells
// the thread's id, reach it in the order the host took them.
func TestAServicesMessagesToOneThreadKeepTheirOrder(t *testing.T) {
	no := false
	d, err := Start(context.Background(), Config{Dir: stateDir(t), Hub: true, KeepAwake: &no, Providers: provider.Mux{"fake": fake.New(nil)}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	thread := api.NewID()
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{Workspace: t.TempDir(), Model: api.ModelRef{Provider: "fake", Model: "fake"}}}); err != nil {
		t.Fatal(err)
	}
	const n = 16
	var want []string
	answers := make(chan error, n)
	for i := range n {
		want = append(want, fmt.Sprintf("message %02d", i))
		id := thread.String()
		if i%2 == 1 {
			id = strings.ToLower(id)
		}
		args, _ := json.Marshal(map[string]string{"thread": id, "text": want[i]})
		v, err := d.pluginEffect("starter", plugins.Effect{Type: "threads.send", Args: args})
		if err != nil {
			t.Fatal(err)
		}
		v.(plugins.Pending)(func(_ any, err error) { answers <- err })
	}
	for range n {
		if err := <-answers; err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, b := range threadLog(t, d.Endpoint(), thread) {
		if m, ok := b.(api.MessageQueued); ok {
			got = append(got, m.Text)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the thread has the messages %q, want %q", got, want)
	}
}
