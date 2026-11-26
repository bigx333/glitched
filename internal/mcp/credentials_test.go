package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/mcp"
)

type asker struct {
	got []api.Ask
	out api.Asked
}

func (a *asker) AskCredentials(_ context.Context, in api.Ask) (api.Asked, error) {
	a.got = append(a.got, in)
	return a.out, nil
}

func rawServe(t *testing.T, tools *mcp.Tools, lines ...string) []map[string]json.RawMessage {
	t.Helper()
	var out bytes.Buffer
	if err := mcp.Serve(ctx, strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, tools); err != nil {
		t.Fatal(err)
	}
	var replies []map[string]json.RawMessage
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r map[string]json.RawMessage
		json.Unmarshal([]byte(l), &r)
		replies = append(replies, r)
	}
	return replies
}

// request_credentials is listed for every agent, loaded up front, and asks
// for the server's own thread and access with Claude Code's tool use id
// when the agent gives none. What it returns names files, never values.
func TestRequestCredentialsAsksForItsOrigin(t *testing.T) {
	o := mcp.Origin{Thread: api.NewID(), Access: api.AccessFull}
	a := &asker{out: api.Asked{Status: api.AskProvided, Names: []string{"GLX_PROBE"}, Env: "/v/env", Files: map[string]string{"GLX_PROBE": "/v/GLX_PROBE"}}}
	replies := rawServe(t, &mcp.Tools{Origin: o, Secrets: a},
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"request_credentials","arguments":{"reason":"probe","fields":[{"name":"GLX_PROBE"}]},"_meta":{"claudecode/toolUseId":"toolu_1"}}}`,
	)
	byID := map[string]map[string]json.RawMessage{}
	for _, r := range replies {
		byID[string(r["id"])] = r
	}
	var list struct {
		Tools []struct {
			Name string
			Meta map[string]any `json:"_meta"`
		}
	}
	json.Unmarshal(byID["1"]["result"], &list)
	found := false
	for _, tool := range list.Tools {
		if tool.Name == "request_credentials" {
			found = tool.Meta["anthropic/alwaysLoad"] == true
		}
	}
	if !found {
		t.Errorf("tools/list %s, want request_credentials loaded up front", byID["1"]["result"])
	}
	if len(a.got) != 1 || a.got[0].Thread != o.Thread || a.got[0].Access != o.Access || a.got[0].Call != "toolu_1" || a.got[0].Credentials.Names()[0] != "GLX_PROBE" {
		t.Fatalf("asked %+v", a.got)
	}
	var res struct {
		IsError           bool
		StructuredContent struct{ Status, Env, Note string }
	}
	json.Unmarshal(byID["2"]["result"], &res)
	if res.IsError || res.StructuredContent.Status != api.AskProvided || !strings.Contains(res.StructuredContent.Note, "/v/env") {
		t.Errorf("the call returned %s", byID["2"]["result"])
	}
}

// A call with a field the tool doesn't take, or to an engine that keeps
// no credentials, fails without asking.
func TestRequestCredentialsRefusals(t *testing.T) {
	a := &asker{}
	for _, tools := range []*mcp.Tools{{Secrets: a}, {}} {
		replies := rawServe(t, tools,
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"request_credentials","arguments":{"reason":"r","fields":[{"name":"A"}],"value":"x"}}}`,
		)
		var res struct{ IsError bool }
		json.Unmarshal(replies[0]["result"], &res)
		if !res.IsError {
			t.Errorf("the call returned %s, want an error", replies[0]["result"])
		}
	}
	if len(a.got) != 0 {
		t.Errorf("asked %+v", a.got)
	}
}

// listed is whether tools/list names tool.
func listed(t *testing.T, tools *mcp.Tools, tool string) bool {
	t.Helper()
	r := rawServe(t, tools, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	return strings.Contains(string(r[0]["result"]), `"`+tool+`"`)
}

// refused asks for a secret through tools, and is whether the call was
// refused.
func refused(t *testing.T, tools *mcp.Tools) bool {
	t.Helper()
	r := rawServe(t, tools, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"request_credentials","arguments":{"reason":"probe","fields":[{"name":"GLX_PROBE"}]}}}`)
	var res struct{ IsError bool }
	json.Unmarshal(r[0]["result"], &res)
	return res.IsError
}

// A child the user hasn't taken over has no request_credentials: it asks
// its parent, which its instructions say. The engine's word on whether the
// user took it over wins over the flag it started with.
func TestAChildAsksItsParentForSecrets(t *testing.T) {
	a := &asker{out: api.Asked{Status: api.AskDeclined}}
	kid := &mcp.Tools{Origin: mcp.Origin{Thread: api.NewID(), Access: api.AccessFull, Child: true}, Secrets: a}
	if listed(t, kid, "request_credentials") || !refused(t, kid) || len(a.got) != 0 {
		t.Fatalf("a child: listed %v, asked %+v; want it hidden and refused", listed(t, kid, "request_credentials"), a.got)
	}
	init := rawServe(t, kid, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if s := string(init[0]["result"]); !strings.Contains(s, "report the names of the secrets you need") || strings.Contains(s, "ask with request_credentials") {
		t.Fatalf("a child's instructions: %s", s)
	}

	h := engine(t)
	o := working(t, h)
	c := child(t, h, o, "Deploy it")
	live := &mcp.Tools{Engine: h, Origin: mcp.Origin{Thread: c.ID, Access: o.Access}, Secrets: a}
	if listed(t, live, "request_credentials") || !refused(t, live) {
		t.Fatal("a child the engine has answering to its parent lists or takes request_credentials")
	}
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: c.ID, Body: api.MessageDispatch{Text: "mine now", Mode: api.AfterActive}}); err != nil {
		t.Fatal(err)
	}
	until(t, h, c.ID, "the takeover", func(th api.Thread) bool { return th.TakenOver })
	live.Origin.Child = true
	if !listed(t, live, "request_credentials") || refused(t, live) || len(a.got) != 1 {
		t.Fatalf("a child the user took over: listed %v, asked %+v; want request_credentials its own", listed(t, live, "request_credentials"), a.got)
	}
	top := &mcp.Tools{Engine: h, Origin: o, Secrets: a}
	if !listed(t, top, "request_credentials") || refused(t, top) {
		t.Fatal("a thread of the user's lacks request_credentials")
	}
	s := (mcp.Launcher{Command: "glitched", Engine: "/tmp/sock"}).ForThread(c.ID, api.AccessAsk, api.RoleDefault, true)
	if s.Env[mcp.EnvChild] != "1" || strings.Contains(s.Instructions, "ask with request_credentials") {
		t.Fatalf("a child's MCP server: env %+v, instructions %q", s.Env, s.Instructions)
	}
	if s := (mcp.Launcher{}).For(o.Thread, api.AccessAsk); s.Env[mcp.EnvChild] != "" || !strings.Contains(s.Instructions, "ask with request_credentials") {
		t.Fatalf("a top-level thread's MCP server: env %+v, instructions %q", s.Env, s.Instructions)
	}
}
