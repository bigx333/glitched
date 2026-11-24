package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/plugins/pack"
)

func writeService(t *testing.T, state, id, extra, main, service string) {
	t.Helper()
	writeModule(t, state, id, extra, main)
	if service != "" {
		if err := os.WriteFile(filepath.Join(Dir(state), id, "service.js"), []byte(service), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	signAsOwn(t, state, filepath.Join(Dir(state), id))
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestToolsAnswerAgentsWithNoWindow(t *testing.T) {
	state := t.TempDir()
	writeModule(t, state, "words", `"capabilities": ["tools"], "glitched": { "tools": [
		{ "name": "count_words", "description": "Counts words.", "input": { "type": "object", "properties": { "text": { "type": "string" } } } }] }, `, `
		export function tool({ name, args, call }) {
			if (name !== "count_words") throw new Error("no tool " + name)
			return { words: (args.text ?? "").split(/\s+/).filter(Boolean).length, thread: call.thread }
		}`)
	writeModule(t, state, "quiet", "", `export function view() {}`)
	s := StartServices(state, ServiceOptions{Logf: t.Logf})
	defer s.Close()
	tools, _ := s.Tools(context.Background())
	if len(tools) != 1 || tools[0].Plugin != "words" || tools[0].Name != "count_words" || !strings.Contains(string(tools[0].Input), "text") {
		t.Fatalf("tools %+v", tools)
	}
	out, err := s.CallTool(context.Background(), ToolCall{Plugin: "words", Name: "count_words", Args: json.RawMessage(`{"text": "one two  three"}`), Thread: "t1"})
	if err != nil || string(out) != `{"words":3,"thread":"t1"}` {
		t.Fatalf("%s, %v", out, err)
	}
	if _, err := s.CallTool(context.Background(), ToolCall{Plugin: "words", Name: "nope"}); err == nil {
		t.Fatal("a tool the manifest doesn't list ran")
	}
	if _, err := s.CallTool(context.Background(), ToolCall{Plugin: "quiet", Name: "count_words"}); err == nil {
		t.Fatal("a plugin with no tools answered")
	}
}

func TestAServiceAnswersRunsItsTimersAndFollowsItsGrant(t *testing.T) {
	state := t.TempDir()
	writeService(t, state, "svc", `"capabilities": ["service", "clipboard"], "glitched": { "service": "service.js", "timers": [{ "id": "poll", "every": 30 }] }, `,
		`export function view() { return { body: { type: "text", text: "window" } } }`, `
		let calls = 0
		export function serve({ method, args }) {
			if (method === "ping") return { pong: args, calls: ++calls, polls: host.storage.get("polls") ?? 0 }
			throw new Error("no method " + method)
		}
		export async function timer({ id }) {
			host.storage.set("polls", (host.storage.get("polls") ?? 0) + 1)
			await glitched.clipboard.write("polled " + id)
		}`)
	var mu sync.Mutex
	var done []string
	s := StartServices(state, ServiceOptions{Logf: t.Logf, TimerScale: 0.002, Idle: 200 * time.Millisecond,
		Do: func(id string, e Effect) (any, error) {
			mu.Lock()
			done = append(done, id+" "+e.Type)
			mu.Unlock()
			return nil, nil
		}})
	defer s.Close()
	if _, err := s.Call(context.Background(), ServiceCall{Plugin: "svc", Method: "ping"}); err == nil {
		t.Fatal("a service ran without its grant")
	}
	grant(t, state, `{"grants": {"svc": ["service"]}}`)
	eventually(t, "the grant taken up", func() bool {
		_, err := s.Call(context.Background(), ServiceCall{Plugin: "svc", Method: "ping", Args: json.RawMessage(`1`)})
		return err == nil
	})
	eventually(t, "a timer's effect", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(done) > 0 && done[0] == "svc clipboard.write"
	})
	out, err := s.Call(context.Background(), ServiceCall{Plugin: "svc", Method: "ping", Args: json.RawMessage(`"x"`)})
	if err != nil || !strings.Contains(string(out), `"pong":"x"`) {
		t.Fatalf("%s, %v", out, err)
	}
	if _, err := s.Call(context.Background(), ServiceCall{Plugin: "svc", Method: "boom"}); err == nil || !strings.Contains(err.Error(), "no method boom") {
		t.Fatalf("a failing method: %v", err)
	}
	grant(t, state, `{"grants": {}}`)
	eventually(t, "the grant taken back", func() bool {
		_, err := s.Call(context.Background(), ServiceCall{Plugin: "svc", Method: "ping"})
		return err != nil
	})
}

func TestAnIdleServiceStopsAndStartsAgain(t *testing.T) {
	state := t.TempDir()
	writeService(t, state, "svc", `"capabilities": ["service"], "glitched": { "service": "service.js" }, `, `export function view() {}`, `
		let calls = 0
		export function serve() { return ++calls }`)
	grant(t, state, `{"grants": {"svc": ["service"]}}`)
	s := StartServices(state, ServiceOptions{Logf: t.Logf, Idle: 100 * time.Millisecond})
	defer s.Close()
	call := func() string {
		out, err := s.Call(context.Background(), ServiceCall{Plugin: "svc", Method: "n"})
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if call() != "1" || call() != "2" {
		t.Fatal("the service lost its count while busy")
	}
	time.Sleep(1500 * time.Millisecond)
	if got := call(); got != "1" {
		t.Fatalf("after idling: %s, want a fresh start", got)
	}
}

func grant(t *testing.T, state, body string) {
	t.Helper()
	path := filepath.Join(state, "plugins.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// A later write in the same instant must still read as a change.
	os.Chtimes(path, time.Now(), time.Now().Add(time.Duration(len(body))*time.Millisecond))
}

func TestTheWordToolsSample(t *testing.T) {
	state := t.TempDir()
	copyPlugin(t, state, "word-tools", "word-tools")
	s := StartServices(state, ServiceOptions{Logf: t.Logf})
	defer s.Close()
	call := func(name, args, thread string) string {
		out, err := s.CallTool(context.Background(), ToolCall{Plugin: "word-tools", Name: name, Args: json.RawMessage(args), Thread: thread})
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if got := call("count_words", `{"text": "two words\nand more"}`, "t1"); got != `{"words":4,"lines":2,"characters":18}` {
		t.Fatalf("count_words: %s", got)
	}
	call("note", `{"text": "first"}`, "t1")
	call("note", `{"text": "elsewhere"}`, "t2")
	if got := call("note", `{}`, "t1"); got != `{"notes":["first"]}` {
		t.Fatalf("notes: %s", got)
	}
}

// repo is a git repository with two files not yet added.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Skipf("no git: %v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "start").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)
	return dir
}

func TestTheRepoWatchServiceRunsGitOnItsTimer(t *testing.T) {
	state, folder := t.TempDir(), repo(t)
	copyPlugin(t, state, "repo-watch", "repo-watch")
	grant(t, state, `{"grants": {"repo-watch": ["service", "programs"]}, "settings": {"repo-watch": {"folder": "`+folder+`"}}}`)
	s := StartServices(state, ServiceOptions{Logf: t.Logf, TimerScale: 0.001})
	defer s.Close()
	out, err := s.Call(context.Background(), ServiceCall{Plugin: "repo-watch", Method: "check"})
	if err != nil || string(out) != `{"branch":"main","changed":2}` {
		t.Fatalf("check: %s, %v", out, err)
	}
	eventually(t, "a timer's check in the history", func() bool {
		out, err := s.Call(context.Background(), ServiceCall{Plugin: "repo-watch", Method: "history"})
		return err == nil && strings.Count(string(out), `"changed":2`) >= 2
	})

	grant(t, state, `{"grants": {"repo-watch": ["service"]}, "settings": {"repo-watch": {"folder": "`+folder+`"}}}`)
	eventually(t, "git refused once programs is taken back", func() bool {
		_, err := s.Call(context.Background(), ServiceCall{Plugin: "repo-watch", Method: "check"})
		return err != nil && strings.Contains(err.Error(), "programs")
	})
}

// writeTicker writes a service, as id, that answers who it is and copies
// who it is on each timer.
func writeTicker(t *testing.T, dst, id, who string) {
	t.Helper()
	files := map[string]string{
		"plugin.json": `{"id": "` + id + `", "name": "` + id + `", "main": "service.js", "contract": "glitched", "contractVersion": 1, "capabilities": ["service", "clipboard"], "glitched": {"service": "service.js", "timers": [{"id": "tick", "every": 30}]}}`,
		"service.js": `export function serve() { return "` + who + `" }
			export async function timer() { await glitched.clipboard.write("` + who + `") }`,
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dst, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A built-in plugin's service runs in the engine as a built-in, granted
// what it declares, from a folder of the engine's own; a copy of it runs
// in its place, and the built-in it shadows runs no service or timer.
func TestABuiltInsServiceRunsTillACopyTakesItsPlace(t *testing.T) {
	root := t.TempDir()
	key, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeTicker(t, filepath.Join(root, "ticker"), "ticker", "built-in")
	if err := pack.Sign(filepath.Join(root, "ticker"), key); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var told []string
	start := func(state string) *Services {
		s := StartServices(state, ServiceOptions{Logf: t.Logf, TimerScale: 0.002, Bundled: os.DirFS(root), ReleaseKeys: []string{key.Public()},
			Do: func(id string, e Effect) (any, error) {
				var a struct{ Text string }
				json.Unmarshal(e.Args, &a)
				mu.Lock()
				told = append(told, id+": "+a.Text)
				mu.Unlock()
				return nil, nil
			}})
		t.Cleanup(s.Close)
		return s
	}
	heard := func(want string) bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(told, want)
	}

	state := t.TempDir()
	s := start(state)
	if out, err := s.Call(context.Background(), ServiceCall{Plugin: "ticker", Method: "who"}); err != nil || string(out) != `"built-in"` {
		t.Fatalf("the built-in's service answered %s, %v", out, err)
	}
	eventually(t, "the built-in's timer", func() bool { return heard("ticker: built-in") })
	if _, err := os.Stat(BundledDir(state)); !os.IsNotExist(err) {
		t.Fatalf("the engine wrote the built-ins where the window does: %v", err)
	}
	s.Close()

	writeTicker(t, filepath.Join(Dir(state), "ticker"), "ticker", "copy")
	signAsOwn(t, state, filepath.Join(Dir(state), "ticker"))
	grant(t, state, `{"grants": {"ticker": ["service", "clipboard"]}}`)
	mu.Lock()
	told = nil
	mu.Unlock()
	s = start(state)
	if out, err := s.Call(context.Background(), ServiceCall{Plugin: "ticker", Method: "who"}); err != nil || string(out) != `"copy"` {
		t.Fatalf("with a copy, the service answered %s, %v", out, err)
	}
	eventually(t, "the copy's timer, twice", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(told) >= 2
	})
	mu.Lock()
	defer mu.Unlock()
	for _, w := range told {
		if w != "ticker: copy" {
			t.Fatalf("with a copy, the timers told %q", told)
		}
	}
}

// Taking service back from a built-in stops its service and its timers, as
// it never had it.
func TestABuiltInsServiceStopsWhenServiceIsTakenBack(t *testing.T) {
	root := t.TempDir()
	key, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeTicker(t, filepath.Join(root, "ticker"), "ticker", "built-in")
	if err := pack.Sign(filepath.Join(root, "ticker"), key); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var ticks int
	state := t.TempDir()
	s := StartServices(state, ServiceOptions{Logf: t.Logf, TimerScale: 0.002, Bundled: os.DirFS(root), ReleaseKeys: []string{key.Public()},
		Do: func(string, Effect) (any, error) {
			mu.Lock()
			ticks++
			mu.Unlock()
			return nil, nil
		}})
	t.Cleanup(s.Close)
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return ticks
	}
	eventually(t, "the built-in's timer", func() bool { return count() > 0 })

	grant(t, state, `{"revoked": {"ticker": ["service"]}}`)
	eventually(t, "the built-in's service stopping", func() bool {
		_, err := s.Call(context.Background(), ServiceCall{Plugin: "ticker", Method: "who"})
		return errors.Is(err, ErrNoService)
	})
	var running bool
	s.do(func() { running = s.h.running1("ticker") != nil })
	if running {
		t.Fatal("with service taken back, its service still runs, timers and all")
	}
}

// A call waits for no services' queue past its deadline, though the queue
// is full.
func TestAServiceCallWaitsOnNoFullQueuePastItsDeadline(t *testing.T) {
	s := StartServices(t.TempDir(), ServiceOptions{Logf: t.Logf})
	defer s.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	s.post(func() { close(entered); <-release })
	<-entered
	for range cap(s.queue) {
		s.post(func() {})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	answered := make(chan error, 1)
	go func() {
		_, err := s.Call(ctx, ServiceCall{Plugin: "absent"})
		answered <- err
	}()
	<-ctx.Done()
	select {
	case err := <-answered:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the call answered %v, want its deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call still waits on the queue, 5s past its deadline")
	}
}

// A call whose ctx ends while its service's runtime starts, by its
// deadline or its caller's cancel, answers then, and never runs serve.
func TestACallThatEndsAsItsServiceStartsNeverRunsIt(t *testing.T) {
	for _, end := range []string{"deadline", "cancel"} {
		t.Run(end, func(t *testing.T) {
			state := t.TempDir()
			writeService(t, state, "counter", `"capabilities": ["service"], "glitched": {"service": "service.js"},`, "export function view() {}", `
				console.log("starting")
				export function serve({ method }) {
					if (method === "write") host.storage.set("writes", (host.storage.get("writes") ?? 0) + 1)
					return host.storage.get("writes") ?? 0
				}`)
			grant(t, state, `{"grants": {"counter": ["service"]}}`)
			starting, started := make(chan struct{}), make(chan struct{})
			var once sync.Once
			s := StartServices(state, ServiceOptions{Logf: func(format string, args ...any) {
				if strings.Contains(fmt.Sprintf(format, args...), "starting") {
					once.Do(func() { close(starting); <-started })
				}
			}})
			t.Cleanup(s.Close)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			if end == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
			}
			defer cancel()
			answered := make(chan error, 1)
			go func() {
				_, err := s.Call(ctx, ServiceCall{Plugin: "counter", Method: "write"})
				answered <- err
			}()
			<-starting
			if end == "cancel" {
				cancel()
			}
			err := <-answered
			close(started)
			raw, readErr := s.Call(context.Background(), ServiceCall{Plugin: "counter", Method: "read"})
			if err == nil || ctx.Err() == nil || !errors.Is(err, ctx.Err()) || readErr != nil || string(raw) != "0" {
				t.Fatalf("the call answered %v as its ctx ended with %v; then writes %s, %v; want none", err, ctx.Err(), raw, readErr)
			}
		})
	}
}

// A timer runs one call at a time: a tick that comes while its last call
// still waits on an effect, past its period too, waits for the next.
func TestATimerRunsOneCallAtATime(t *testing.T) {
	state := t.TempDir()
	writeService(t, state, "hourglass", `"capabilities": ["service", "clipboard"], "glitched": {"service": "service.js", "timers": [{"id": "minute", "every": 60}]},`, "export function view() {}", `
		let started = 0, finished = []
		export function serve() { return { started, finished } }
		export async function timer() {
			const n = ++started
			await glitched.clipboard.write(String(n))
			finished.push(n)
		}`)
	grant(t, state, `{"grants": {"hourglass": ["service"]}}`)
	effects, waited := make(chan func(any, error), 4), make(chan struct{}, 1)
	s := StartServices(state, ServiceOptions{TimerScale: 0.01, Logf: func(format string, args ...any) {
		if strings.Contains(fmt.Sprintf(format, args...), "its last call still runs") {
			select {
			case waited <- struct{}{}:
			default:
			}
		}
	}, Do: func(string, Effect) (any, error) {
		return Pending(func(done func(any, error)) { effects <- done }), nil
	}})
	t.Cleanup(s.Close)
	first := <-effects
	select {
	case <-waited:
	case <-effects:
		t.Fatal("a tick called the timer while its last call still ran")
	}
	first(nil, nil)
	second := <-effects
	raw, err := s.Call(context.Background(), ServiceCall{Plugin: "hourglass"})
	second(nil, nil)
	if err != nil || string(raw) != `{"started":2,"finished":[1]}` {
		t.Fatalf("as the second call started, the timer was %s, %v; want the first finished", raw, err)
	}
}
