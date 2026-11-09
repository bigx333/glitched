package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeService(t *testing.T, state, id, extra, main, service string) {
	t.Helper()
	writeModule(t, state, id, extra, main)
	if service != "" {
		if err := os.WriteFile(filepath.Join(Dir(state), id, "service.js"), []byte(service), 0o644); err != nil {
			t.Fatal(err)
		}
	}
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
		export function timer({ id }) {
			host.storage.set("polls", (host.storage.get("polls") ?? 0) + 1)
			return { effects: [{ type: "clipboard.write", text: "polled " + id }] }
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
