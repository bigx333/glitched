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
