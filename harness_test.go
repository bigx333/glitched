package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// posts stands in for the window's Update: the manager posts here, and the
// test runs what it posted on its own goroutine, between frames.
type posts struct {
	mu   sync.Mutex
	q    []func()
	wake chan struct{}
}

func (p *posts) post(fn func()) {
	p.mu.Lock()
	p.q = append(p.q, fn)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// step runs the first thing posted, and reports whether there was one.
func (p *posts) step() bool {
	p.mu.Lock()
	if len(p.q) == 0 {
		p.mu.Unlock()
		return false
	}
	fn := p.q[0]
	p.q = p.q[1:]
	p.mu.Unlock()
	fn()
	return true
}

// run runs what was posted, and reports whether there was any.
func (p *posts) run() bool {
	p.mu.Lock()
	q := p.q
	p.q = nil
	p.mu.Unlock()
	for _, fn := range q {
		fn()
	}
	return len(q) > 0
}

// offline is the app before its manager starts: no machines, no threads.
// What it posts waits for a tester made with newTester to run it.
func offline(t *testing.T) *app {
	p := &posts{wake: make(chan struct{}, 1)}
	a := testApp(t, t.TempDir(), nil, p.post)
	offlinePosts.Store(a, p)
	t.Cleanup(func() { offlinePosts.Delete(a) })
	return a
}

// testApp is newApp for a test, which it outlives by nothing: its
// plugins' runtimes stop as the test ends, as the app's do as it quits.
// A runtime left running holds the app, and with it the window the test
// drew, its surfaces and all, to the end of the test binary.
//
// Its engine's providers report the models the picker showed before it
// asked engines for theirs (todaysModels), unless the test's engine has
// them report others.
func testApp(t *testing.T, dir string, engine func(*daemon.Config), post func(func())) *app {
	a := newApp(dir, func(c *daemon.Config) {
		if engine != nil {
			engine(c)
		}
		todaysModels(c.Providers)
	}, post)
	t.Cleanup(a.settingsOut.wait)
	if a.plugins != nil {
		t.Cleanup(a.plugins.Close)
		failBuiltinFailures(t, a)
	}
	return a
}

// expectedBuiltinFailures are the tests that break a built-in plugin on
// purpose, which expectBuiltinFailures names.
var expectedBuiltinFailures sync.Map // *testing.T: true

// expectBuiltinFailures lets t's built-in plugins fail, as a test of what
// Glitched does then.
func expectBuiltinFailures(t *testing.T) { expectedBuiltinFailures.Store(t, true) }

// failBuiltinFailures fails t if a built-in plugin of a records a failure
// t doesn't expect: Glitched draws its own in the built-in's place, which
// looks the same, so a test would pass on the fallback unawares.
func failBuiltinFailures(t *testing.T, a *app) {
	var mu sync.Mutex
	var failed []string
	a.plugins.OnFailure(func(plugin string, builtin bool, err error) {
		if builtin {
			mu.Lock()
			failed = append(failed, plugin+": "+err.Error())
			mu.Unlock()
		}
	})
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := expectedBuiltinFailures.LoadAndDelete(t); !ok && len(failed) > 0 {
			t.Errorf("a built-in plugin failed, and Glitched drew its own in its place: %q", failed)
		}
	})
}

var (
	offlinePosts sync.Map // *app: *posts
	settlers     sync.Map // *ui.Tester: func()
)

// newTester is a tester of the offline app a, which click settles.
func newTester(t *testing.T, a *app, w, h int) *ui.Tester {
	tt := ui.NewTester(a.view, w, h)
	v, _ := offlinePosts.Load(a)
	p := v.(*posts)
	settlers.Store(tt, func() { settlePlugins(t, a, tt, p) })
	t.Cleanup(func() { settlers.Delete(tt) })
	return tt
}

// settleTester runs what the tester's app posted, and waits for its plugins,
// till both are done: a plugin draws, and does what it's clicked for,
// on its own goroutine.
func settleTester(tt *ui.Tester) {
	if f, ok := settlers.Load(tt); ok {
		f.(func())()
	}
}

func settlePlugins(t *testing.T, a *app, tt *ui.Tester, p *posts) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	tt.Frame()
	for {
		ran := p.run()
		if ran {
			tt.Frame()
		}
		if !ran && (a.plugins == nil || a.plugins.Idle()) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the plugins never settled; the window shows %q", tt.Texts())
		}
		if !ran {
			select {
			case <-p.wake:
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
}

// harness is the app on the engines its state directory reaches, with its
// manager started.
type harness struct {
	t  *testing.T
	a  *app
	tt *ui.Tester
	p  *posts
}

func start(t *testing.T, dir string) *harness {
	t.Helper()
	return startWith(t, dir, nil)
}

// startWith is start, with an engine embedded here serving as engine has
// it.
func startWith(t *testing.T, dir string, engine func(*daemon.Config)) *harness {
	t.Helper()
	p := &posts{wake: make(chan struct{}, 1)}
	a := testApp(t, dir, engine, p.post)
	if err := a.backend.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.backend.Close() })
	h := &harness{t, a, ui.NewTester(a.view, 1320, 860), p}
	h.until("this machine's engine", func() bool { return a.backend.Envs()[0].Status == client.Status(client.Online{}) })
	return h
}

// until runs what the manager posts, a frame after each batch, until ok
// holds.
func (h *harness) until(what string, ok func() bool) {
	h.t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		if h.p.run() {
			h.tt.Frame()
		}
		if ok() {
			return
		}
		select {
		case <-h.p.wake:
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s; the window shows %q", what, h.tt.Texts())
		}
	}
}

// addServer adds s as Settings does, and returns what AddServer said.
func (h *harness) addServer(s client.Server) error {
	h.t.Helper()
	var err error
	done := false
	h.a.backend.AddServer(s, func(e error) { err, done = e, true })
	h.until("adding "+s.Label, func() bool { return done })
	return err
}

func (h *harness) local() *client.Env { return h.a.backend.Envs()[0] }

// thread is the thread titled title, on whichever machine.
func (h *harness) thread(title string) thread {
	h.t.Helper()
	var found thread
	h.until("a thread titled "+title, func() bool {
		for _, p := range h.a.gather() {
			for _, t := range p.threads {
				if t.View.Title == title {
					found = t
					return true
				}
			}
		}
		return false
	})
	return found
}

// rightClick right-clicks what text names once the window shows it: a
// project's chip is drawn by a plugin, a moment after the project shows.
func (h *harness) rightClick(text string) {
	h.t.Helper()
	h.until(fmt.Sprintf("%q to right-click", text), func() bool { _, ok := h.tt.Find(text); return ok })
	if err := h.tt.RightClick(text); err != nil {
		h.t.Fatal(err)
	}
}

// hover points at the text's middle.
func (h *harness) hover(text string) {
	h.t.Helper()
	r, ok := h.tt.Find(text)
	if !ok {
		h.t.Fatalf("no %q to point at in %q", text, h.tt.Texts())
	}
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
}

// click clicks what text names once the window shows it, and waits for
// the plugins to do what the click asks of them.
func (h *harness) click(text string) {
	h.t.Helper()
	if _, ok := h.tt.Find(text); !ok {
		h.until(fmt.Sprintf("%q to click", text), func() bool { _, ok := h.tt.Find(text); return ok })
	}
	if err := h.tt.Click(text); err != nil {
		h.t.Fatal(err)
	}
	// A click a plugin took is done once its session is: what it asked
	// the app for is posted by then.
	if h.a.plugins != nil && !h.a.plugins.Idle() {
		h.until("the plugins to settle", h.a.plugins.Idle)
		h.p.run()
		h.tt.Frame()
	}
}

// tempDir is short: unix socket paths are limited to about 100 bytes.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// engine runs an engine whose agents adapter opens, for the app to attach
// to or reach.
func engine(t *testing.T, cfg daemon.Config, adapter provider.Adapter) *daemon.Daemon {
	t.Helper()
	sd := &seeding{Adapter: adapter, spawner: fake.New(nil), runs: map[api.ID]bool{}}
	cfg.Hub, cfg.Providers = true, daemon.FakeProviders(sd)
	todaysModels(cfg.Providers)
	d, err := daemon.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	sd.mu.Lock()
	sd.ep = d.Endpoint()
	sd.mu.Unlock()
	seeders.Store(d, sd)
	t.Cleanup(func() {
		d.Close()
		seeders.Delete(d)
	})
	return d
}

var fakeModel = api.ModelRef{Provider: "fake", Model: "fake"}

// seed creates a thread on the engine and sends it messages, returning
// once every turn they started has ended.
func seed(t *testing.T, d *daemon.Daemon, workspace, title string, messages ...string) api.ID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ep, id := d.Endpoint(), api.NewID()
	bodies := []api.Intent{api.ThreadCreate{Workspace: workspace, Title: title, Model: fakeModel}}
	for _, m := range messages {
		bodies = append(bodies, api.MessageDispatch{Text: m, Mode: api.AfterActive})
	}
	for _, b := range bodies {
		if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	for len(messages) > 0 {
		snap, err := ep.Read(ctx, api.Query{Thread: id})
		if err != nil {
			t.Fatal(err)
		}
		if v := snap.Threads[0]; v.LastTurn != nil && v.Run == api.RunIdle && v.Queued == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return id
}

// inbox is threads in three projects on this machine, as an engine in dir
// holds them: some with a finished turn, unread, the others never sent to.
func inbox(t *testing.T, dir string) *daemon.Daemon {
	t.Helper()
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "Fix terminal resize flicker on Windows", "Why does it flicker?")
	seed(t, d, "/work/t3code", "Add keyboard shortcut for new thread")
	seed(t, d, "/work/t3code", "Investigate websocket reconnect loop", "Why does it loop?")
	seed(t, d, "/work/mygo", "Markdown table column alignment")
	seed(t, d, "/work/agent-phone", "SIP re-INVITE handling")
	return d
}

// inboxDir is a state directory whose engine holds inbox's threads.
func inboxDir(t *testing.T) string {
	dir := tempDir(t)
	inbox(t, dir)
	return dir
}

// machines is the app on inbox's threads and on a server, devbox, with a
// thread older than all of those and one newer.
func machines(t *testing.T) *harness {
	t.Helper()
	rdir := tempDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	r := engine(t, daemon.Config{Dir: rdir, Listen: []string{"127.0.0.1:0"}}, fake.New(fake.Turn(false)))
	seed(t, r, "/srv/api", "Rotate the API keys", "Which keys are older than 90 days?")
	dir := inboxDir(t)
	seed(t, r, "/srv/billing", "Retry failed invoice webhooks")
	h := start(t, dir)
	if err := h.addServer(client.Server{Label: "devbox", Addr: r.Addrs()[0].String(), Token: token}); err != nil {
		t.Fatal(err)
	}
	h.thread("Retry failed invoice webhooks")
	h.tt.Advance(time.Second)
	return h
}

// listServers writes servers to gui.json in dir, as the app saved them
// before their engines changed.
func listServers(t *testing.T, dir string, servers ...client.Server) {
	t.Helper()
	b, err := json.Marshal(map[string][]client.Server{"servers": servers})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gui.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// texts is what the timeline shows of a thread's entries, for a failure.
func texts(es []entry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.key+" "+e.text+e.took+e.note)
	}
	return strings.Join(out, "\n")
}

// newer serves an engine as one a protocol ahead of this app would, on a
// loopback port whose one token is "any", and returns its address.
func newer(t *testing.T) string {
	t.Helper()
	d := engine(t, daemon.Config{Dir: tempDir(t)}, fake.New(fake.Turn(false)))
	tn := transport.Tailnet{Address: "127.0.0.1:0"}
	ctx, stop := context.WithCancel(context.Background())
	ln, err := tn.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		key := transport.KeyOf("any")
		admit := func(_ *http.Request, proved func(transport.Key) bool) (transport.Key, context.Context, error) {
			if !proved(key) {
				return transport.Key{}, nil, transport.ErrRefused
			}
			return key, ctx, nil
		}
		(&transport.Server{Transport: tn, Endpoint: ahead{d.Endpoint()}, Authorize: admit}).Serve(ctx, ln)
	}()
	t.Cleanup(func() { stop(); <-served })
	return ln.Addr().String()
}

type ahead struct{ api.Endpoint }

func (e ahead) Info(ctx context.Context) (api.Environment, error) {
	env, err := e.Endpoint.Info(ctx)
	env.Protocol++
	return env, err
}

// runTests is a turn that asks to run a command, then runs it.
var runTests = []provider.Event{
	provider.TextDelta{Text: "I'll run the sidebar's tests."},
	provider.Work{Item: "read-1", Kind: "read", Title: "Read package.json", Status: api.WorkCompleted, Path: "package.json"},
	provider.RequestOpened{ID: "run-1", Kind: "approval", Body: json.RawMessage(`{"operation":"run","command":"bun run test -- Sidebar","cwd":"/work/t3code","reason":"Runs the sidebar's tests to check the new sort."}`)},
	provider.Work{Item: "cmd-1", Kind: "command", Title: "bun run test -- Sidebar", Status: api.WorkCompleted},
	provider.TextDelta{Text: " The tests pass."},
}

// asking is the app on an engine whose turns are script, with a thread
// sent a message and waiting on the request the script opens.
func asking(t *testing.T, script []provider.Event) (*harness, thread) {
	t.Helper()
	dir := tempDir(t)
	engine(t, daemon.Config{Dir: dir}, fake.New(script))
	h := start(t, dir)
	h.a.draft = "Run the sidebar tests."
	h.tt.Frame()
	h.click("Send")
	h.until("the approval card", func() bool { return h.tt.HasText("Approval needed") })
	h.tt.Advance(time.Second)
	return h, h.a.current()
}

// checks is a turn whose commands pass and fail, and an edit.
var checks = []provider.Event{
	provider.TextDelta{Text: "Checking the build."},
	provider.Work{Item: "tc", Kind: "command", Title: "bun run typecheck", Status: api.WorkStarted},
	provider.Work{Item: "tc", Kind: "command", Title: "bun run typecheck", Status: api.WorkCompleted, Exit: new(0)},
	provider.Work{Item: "t", Kind: "command", Title: "bun run test", Status: api.WorkStarted},
	provider.Work{Item: "t", Kind: "command", Title: "bun run test", Status: api.WorkFailed, Exit: new(1), Output: " FAIL  Sidebar > sorts threads by updatedAt\n Test Files  1 failed (1)"},
	provider.Work{Item: "e", Kind: "edit", Title: "Edit " + sidebarPath, Status: api.WorkCompleted, Path: sidebarPath},
	provider.TextDelta{Text: " One test fails."},
}

// sidebarPath is the file the checks turn edits.
const sidebarPath = "apps/web/src/components/Sidebar.tsx"

const sidebarBefore = `import { useState } from "react";
import { SidebarGroup, SidebarGroupLabel } from "./ui/sidebar";
import { ThreadRow } from "./ThreadRow";
import { useThreads } from "../hooks/useThreads";

export function Sidebar({ projects }: SidebarProps) {
  return projects.map((project) => <ProjectGroup key={project.id} project={project} />);
}

function ProjectGroup({ project }: { project: Project }) {
  const threads = useThreads(project.id);
  const [expanded, setExpanded] = useState(true);
  const sorted = threads.sort((a, b) => b.updatedAt - a.updatedAt);

  return (
    <SidebarGroup>
      <SidebarGroupLabel>{project.name}</SidebarGroupLabel>
      {expanded && sorted.map((t) => (
        <ThreadRow key={t.id} thread={t} />
      ))}
    </SidebarGroup>
  );
}
`

const sidebarAfter = `import { useMemo, useState } from "react";
import { SidebarGroup, SidebarGroupLabel } from "./ui/sidebar";
import { ThreadRow } from "./ThreadRow";
import { ChevronIcon } from "./ChevronIcon";
import { useThreads } from "../hooks/useThreads";

export function Sidebar({ projects }: SidebarProps) {
  return projects.map((project) => <ProjectGroup key={project.id} project={project} />);
}

function ProjectGroup({ project }: { project: Project }) {
  const threads = useThreads(project.id);
  const [expanded, setExpanded] = useState(true);
  const sorted = useMemo(
    () => [...threads].sort((a, b) => b.updatedAt - a.updatedAt),
    [threads],
  );

  return (
    <SidebarGroup>
      <SidebarGroupLabel onClick={() => setExpanded(!expanded)}>
        <ChevronIcon open={expanded} />
        {project.name}
      </SidebarGroupLabel>
      {expanded && sorted.map((t) => (
        <ThreadRow key={t.id} thread={t} />
      ))}
    </SidebarGroup>
  );
}
`

// gitWorkspace is a repository, t3code, whose HEAD holds sidebarBefore at
// sidebarPath and whose working tree holds sidebarAfter there: the edit
// the checks turn names, made as an agent makes it.
func gitWorkspace(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	ws := filepath.Join(t.TempDir(), "t3code")
	file := filepath.Join(ws, sidebarPath)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(sidebarBefore), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "sidebar"}} {
		if out, err := exec.Command("git", append([]string{"-C", ws}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(file, []byte(sidebarAfter), 0o644); err != nil {
		t.Fatal(err)
	}
	return ws
}

// checked is the app showing a thread that took the checks turn in a
// gitWorkspace, which it returns.
func checked(t *testing.T) (*harness, thread) {
	t.Helper()
	ws := gitWorkspace(t)
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(checks))
	seed(t, d, ws, "Sort sidebar threads by recent activity", "Sort them by most recent activity.")
	h := start(t, dir)
	th := h.thread("Sort sidebar threads by recent activity")
	h.a.select_(th)
	h.until("its transcript", func() bool { return h.tt.HasText("One test fails.") })
	h.tt.Advance(time.Second)
	return h, th
}

// uncertainDir is a state directory whose engine holds a thread, Stuck,
// whose run it lost: the engine stopped while the agent waited on a
// request, and the next found no agent to take the run back.
func uncertainDir(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(true)))
	id := api.NewID()
	for _, b := range []api.Intent{api.ThreadCreate{Workspace: "/work/t3code", Title: "Stuck", Model: fakeModel}, api.MessageDispatch{Text: "Edit main.go.", Mode: api.StartNow}} {
		if _, err := d.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	runIs := func(d *daemon.Daemon, want api.RunState) {
		for {
			if snap, err := d.Endpoint().Read(ctx, api.Query{Thread: id}); err == nil && snap.Threads[0].Run == want {
				return
			}
			if ctx.Err() != nil {
				t.Fatalf("the run never got to %v", want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	runIs(d, api.RunWaiting)
	d.Close()
	runIs(engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(true))), api.RunUncertain)
	return dir
}

// diffsRead waits for the diffs the window asks for to be read, and shows
// them.
func (h *harness) diffsRead() {
	h.t.Helper()
	h.tt.Frame()
	h.until("the diffs read", func() bool { return len(h.a.reading) == 0 })
	h.tt.Advance(time.Second)
}

// diffOf is the viewer of a file of the thread's diff, once read.
func (h *harness) diffOf(th thread, path string) *fileDiff {
	h.t.Helper()
	for _, f := range changedFiles(entriesOf(th)) {
		if f.path == path {
			return readNow(h.t, h.a, h.p, th, f)
		}
	}
	h.t.Fatalf("no diff of %s", path)
	return nil
}
