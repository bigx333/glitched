package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/mcp"
	"glitched/internal/plugins/pack"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// An agent lists a plugin's tool beside delegate_task, and calls it
// through the engine, which answers from the plugin with no window.
func TestAnAgentCallsAPluginsTool(t *testing.T) {
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	folder := filepath.Join(dir, "plugins", "words")
	os.MkdirAll(folder, 0o755)
	os.WriteFile(filepath.Join(folder, "plugin.json"), []byte(`{"id": "words", "name": "Words", "main": "index.js",
		"capabilities": ["tools"], "glitched": {"tools": [{"name": "count_words", "description": "Counts the words of a text.",
		"input": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}}]}}`), 0o644)
	os.WriteFile(filepath.Join(folder, "index.js"), []byte(`export function tool({ name, args, call }) {
		return { words: args.text.split(/\s+/).filter(Boolean).length, thread: call.thread } }`), 0o644)
	key, err := pack.LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(folder, key); err != nil {
		t.Fatal(err)
	}
	d, err := daemon.Start(ctx, daemon.Config{Dir: dir, Hub: true, Providers: provider.Mux{"fake": fake.New(nil)}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer ep.Close()
	thread := api.NewID()
	var out bytes.Buffer
	in := strings.Join([]string{
		`{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}`,
		`{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "words__count_words", "arguments": {"text": "one two three"}}}`,
		`{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "words__nope", "arguments": {}}}`,
	}, "\n") + "\n"
	if err := mcp.Serve(ctx, strings.NewReader(in), &out, &mcp.Tools{Engine: ep, Plugins: ep, Origin: mcp.Origin{Thread: thread}}); err != nil {
		t.Fatal(err)
	}
	got := map[int]json.RawMessage{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		json.Unmarshal([]byte(l), &r)
		got[r.ID] = r.Result
	}
	if !strings.Contains(string(got[1]), `"name":"words__count_words"`) || !strings.Contains(string(got[1]), `"name":"delegate_task"`) || !strings.Contains(string(got[1]), `"required":["text"]`) {
		t.Fatalf("tools/list: %s", got[1])
	}
	want := `{\"words\":3,\"thread\":\"` + thread.String() + `\"}`
	if !strings.Contains(string(got[2]), want) || !strings.Contains(string(got[2]), `"isError":false`) {
		t.Fatalf("tools/call: %s, want %s", got[2], want)
	}
	if !strings.Contains(string(got[3]), `"isError":true`) {
		t.Fatalf("an unknown tool: %s", got[3])
	}
}

type fakePlugins struct{ called string }

func (f *fakePlugins) PluginTools(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`[{"plugin": "my__plug", "name": "count", "description": "d"}]`), nil
}

func (f *fakePlugins) PluginTool(_ context.Context, call json.RawMessage) (json.RawMessage, error) {
	f.called = string(call)
	return json.RawMessage(`1`), nil
}

// A plugin's id may hold __, a tool's name may not: the name splits on its
// last __.
func TestAPluginIDWithUnderscoresStillReachesItsTool(t *testing.T) {
	f := &fakePlugins{}
	var out bytes.Buffer
	in := `{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "my__plug__count", "arguments": {}}}` + "\n"
	if err := mcp.Serve(ctx, strings.NewReader(in), &out, &mcp.Tools{Plugins: f}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.called, `"plugin":"my__plug"`) || !strings.Contains(f.called, `"name":"count"`) {
		t.Fatalf("called %s", f.called)
	}
}
