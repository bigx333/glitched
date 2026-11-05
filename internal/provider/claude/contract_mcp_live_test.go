//go:build claudelive

package claude_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider/claude"
	"glitched/internal/provider/claude/transcript"
)

// mcpCLIs are the releases whose --mcp-config handling the contract
// below was checked against.
var mcpCLIs = []string{"2.1.317", "2.1.318"}

// TestContractMCPConfig pins how Claude Code takes glitched mcp, against
// the signed-in claude on PATH: an inline --mcp-config merges with a
// project's .mcp.json, glitched's tool is mcp__glitched__delegate_task,
// --allowedTools lets it run without asking, and the call carries Claude
// Code's tool use id in _meta. One haiku turn, in a scratch directory
// under /tmp; the child runs on the fake. With -record, the transcript
// goes to testdata for TestAClaudeParentDelegatesATask.
func TestContractMCPConfig(t *testing.T) {
	real, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	out, err := claude.Command(ctx, real, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if v, _, _ := strings.Cut(strings.TrimSpace(string(out)), " "); !slices.Contains(mcpCLIs, v) {
		t.Fatalf("claude on PATH is %s, want one of %v: check the contract against the new release, then add it", v, mcpCLIs)
	}
	tapes := t.TempDir()
	if *record {
		tapes = must(filepath.Abs("testdata"))
	}
	tape := filepath.Join(tapes, "delegate")
	t.Setenv(transcript.EnvReal, real)
	t.Setenv(transcript.EnvOut, tape)

	ws := must(os.MkdirTemp("/tmp", "glitched-contract-"))
	t.Cleanup(func() { os.RemoveAll(ws) })
	project, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"glitched-project": map[string]any{"type": "stdio", "command": must(os.Executable()), "args": []string{"mcp-toolless"}},
	}})
	if err := os.WriteFile(filepath.Join(ws, ".mcp.json"), project, 0o644); err != nil {
		t.Fatal(err)
	}

	h := delegateHost(t, nil)
	parent := api.NewID()
	submit(t, h, parent, api.ThreadCreate{Workspace: ws, Title: "probe", Access: api.AccessAsk,
		Model: api.ModelRef{Provider: "claude", Model: "haiku"}})
	got := turn(t, h, parent, delegatePrompt)
	waitExit(t, tape+".jsonl")

	if got.ended.Outcome != api.OutcomeCompleted || len(got.requests) != 0 {
		t.Errorf("the parent's turn: %+v, want it completed without a request", got)
	}
	kids := childrenOf(t, h, parent)
	if len(kids) != 1 {
		t.Fatalf("the parent has %d children, want 1", len(kids))
	}

	var argv []string
	var init struct {
		Tools      []string
		MCPServers []struct{ Name, Status string } `json:"mcp_servers"`
	}
	asked := false
	for _, l := range strings.Split(string(must(os.ReadFile(tape+".jsonl"))), "\n") {
		var e struct {
			Argv []string
			Recv json.RawMessage
		}
		json.Unmarshal([]byte(l), &e)
		if e.Argv != nil {
			argv = e.Argv
		}
		var m struct {
			Type, Subtype string
			Request       struct {
				Subtype  string
				ToolName string `json:"tool_name"`
			}
		}
		json.Unmarshal(e.Recv, &m)
		if m.Type == "system" && m.Subtype == "init" {
			json.Unmarshal(e.Recv, &init)
		}
		if m.Type == "control_request" && m.Request.Subtype == "can_use_tool" && strings.HasPrefix(m.Request.ToolName, "mcp__glitched__") {
			asked = true
		}
	}
	if !slices.Contains(argv, "--mcp-config") || slices.Contains(argv, "--strict-mcp-config") {
		t.Errorf("claude ran with %q, want an inline --mcp-config and no --strict-mcp-config", argv)
	}
	connected := map[string]bool{}
	for _, s := range init.MCPServers {
		connected[s.Name] = s.Status == "connected"
	}
	if !connected["glitched"] || !connected["glitched-project"] {
		t.Errorf("Claude Code's MCP servers %+v, want glitched's beside the project's, both connected", init.MCPServers)
	}
	if !slices.Contains(init.Tools, "mcp__glitched__delegate_task") {
		t.Errorf("Claude Code's tools %q, want mcp__glitched__delegate_task", init.Tools)
	}
	if asked {
		t.Error("a glitched tool asked to run; --allowedTools should let it run without asking")
	}

	sent, recv := mcpTraffic(t, tape+".jsonl")
	call := callIn(t, sent)
	if call.Meta["claudecode/toolUseId"] == "" {
		t.Errorf("the call's _meta %v, want Claude Code's tool use id", call.Meta)
	}
	if kids[0].Title != call.Arguments.Title {
		t.Errorf("the child %q, want the call's title %q", kids[0].Title, call.Arguments.Title)
	}
	discover := map[string]bool{}
	for _, m := range sent {
		if string(m["method"]) == `"server/discover"` {
			discover[string(m["id"])] = true
		}
	}
	for _, m := range recv {
		if discover[string(m["id"])] && !strings.Contains(string(m["error"]), "-32601") {
			t.Errorf("server/discover got %s, want method not found", m["error"])
		}
	}
}
