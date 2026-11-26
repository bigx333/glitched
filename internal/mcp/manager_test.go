package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
)

// gated is a fake whose agents say nothing until open is closed: a run
// stays running until then.
type gated struct {
	*fake.Adapter
	open chan struct{}
}

type gatedAgent struct {
	provider.Agent
	open chan struct{}
}

func (g *gated) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := g.Adapter.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &gatedAgent{ag, g.open}, nil
}

func (a *gatedAgent) Next(ctx context.Context) (provider.Event, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-a.open:
		return a.Agent.Next(ctx)
	}
}

// workingManager is a Manager on provider on, whose run, the user's with a
// text and a sweep without, has reached state.
func workingManager(t *testing.T, h *runtime.Host, on, text string, state api.RunState) mcp.Origin {
	t.Helper()
	thread := api.NewID()
	create := api.ThreadCreate{
		Workspace: t.TempDir(),
		Title:     "Coord",
		Model:     api.ModelRef{Provider: on, Model: "fake"},
		Access:    api.AccessAutoEdits,
		Role:      api.RoleManager,
		Text:      text,
	}
	if text == "" {
		create.Sweep = &api.SweepSchedule{IntervalSeconds: 1}
	}
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: create}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		snap, err := h.Read(ctx, api.Query{Thread: thread})
		if err == nil && len(snap.Threads) == 1 && snap.Threads[0].Run == state {
			return mcp.Origin{Thread: thread, Access: api.AccessAutoEdits, Role: api.RoleManager}
		}
		if time.Now().After(deadline) {
			t.Fatalf("manager run never reached %v: %+v %v", state, snap.Threads, err)
		}
	}
}

// toolList is the names tools/list gives o.
func toolList(t *testing.T, h *runtime.Host, o mcp.Origin) string {
	t.Helper()
	var buf bytes.Buffer
	if err := mcp.Serve(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"), &buf, &mcp.Tools{Engine: h, Origin: o}); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestNotifyUserIsATopLevelManagersAndRaisesItsWake(t *testing.T) {
	gate := &gated{fake.New(fake.Turn(true)), make(chan struct{})}
	h := engineOf(t, provider.Mux{"fake": fake.New(fake.Turn(true)), "gated": gate})
	call := func(id int) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"notify_user","arguments":{"reason":"a worker needs a key"}}}`, id)
	}

	reg := working(t, h)
	if l := toolList(t, h, reg); strings.Contains(l, `"notify_user"`) || strings.Contains(l, `"dismiss_wake"`) {
		t.Fatalf("a plain thread's tools/list: %s; want neither notify_user nor dismiss_wake", l)
	}
	if out := serve(t, h, reg, call(2)); !out[2].Result.IsError {
		t.Fatalf("a plain thread's notify_user succeeded: %+v", out[2].Result)
	}
	forged := reg
	forged.Role = api.RoleManager
	if out := serve(t, h, forged, call(3)); !out[3].Result.IsError {
		t.Fatalf("a plain thread's notify_user under a forged role succeeded: %+v", out[3].Result)
	}

	mgr := workingManager(t, h, "gated", "", api.RunRunning)
	if l := toolList(t, h, mgr); !strings.Contains(l, `"notify_user"`) || strings.Contains(l, `"dismiss_wake"`) {
		t.Fatalf("a Manager's tools/list: %s; want notify_user and no dismiss_wake", l)
	}
	if th := threadOf(t, h, mgr.Thread); th.Turn == nil || !th.Turn.Silent || th.Working() {
		t.Fatalf("a sweep's turn: %+v; want it quiet", th)
	}
	if out := serve(t, h, mgr, delegate(4, `{"task":"Read the project","title":"Worker","client_request_id":"quiet-sweep-child"}`, "")); out[4].Result.IsError {
		t.Fatalf("delegating from a quiet sweep failed: %+v", out[4].Result)
	}
	worker := children(t, h, mgr.Thread)
	if len(worker) != 1 {
		t.Fatalf("the quiet sweep's workers = %+v, want the one it delegated", worker)
	}
	if l := toolList(t, h, mcp.Origin{Thread: worker[0].ID, Access: mgr.Access, Role: api.RoleManager}); strings.Contains(l, `"notify_user"`) {
		t.Fatalf("a Manager's worker, whatever its role says, lists notify_user: %s", l)
	}
	if out := serve(t, h, mgr, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"dismiss_wake","arguments":{}}}`); out[5].Error == nil {
		t.Fatalf("dismiss_wake is still a tool: %+v", out[5])
	}
	out := serve(t, h, mgr, call(6))
	if out[6].Result.IsError || len(out[6].Result.Content) == 0 {
		t.Fatalf("a Manager's notify_user failed: %+v", out[6].Result)
	}
	var got struct {
		Notified bool   `json:"notified"`
		Run      string `json:"run"`
		Reason   string `json:"reason"`
	}
	json.Unmarshal([]byte(out[6].Result.Content[0].Text), &got)
	if th := threadOf(t, h, mgr.Thread); !got.Notified || got.Reason != "a worker needs a key" || th.Turn == nil || got.Run != th.Turn.Run.String() || th.Turn.Silent {
		t.Fatalf("notify_user replied %+v; the turn after: %+v; want it raised", got, th.Turn)
	}
	if out := serve(t, h, mgr, call(7)); out[7].Result.IsError {
		t.Fatalf("notify_user again: %+v; want it done already", out[7].Result)
	}
	close(gate.open)

	s := (mcp.Launcher{Command: "glitched", Engine: "/tmp/sock"}).ForRole(mgr.Thread, api.AccessAutoEdits, api.RoleManager)
	if s.Env[mcp.EnvRole] != string(api.RoleManager) || !strings.Contains(s.Instructions, "notify_user") || strings.Contains(s.Instructions, "dismiss_wake") {
		t.Fatalf("a Manager's MCP server: env %+v, instructions %q", s.Env, s.Instructions)
	}
	plain := (mcp.Launcher{Command: "glitched", Engine: "/tmp/sock"}).For(reg.Thread, api.AccessAsk)
	if _, ok := plain.Env[mcp.EnvRole]; ok || strings.Contains(plain.Instructions, "notify_user") {
		t.Fatalf("a plain thread's MCP server has a Manager's role or instructions: %+v", plain.Env)
	}
}

// A Manager's MCP server tells it how to run its workers: what to delegate,
// how to gatekeep, to verify before reporting, when to stay quiet and which
// git and safety rails hold. A plain thread's server says none of it.
func TestManagerInstructionsCoverTheRulesetOnlyForManagers(t *testing.T) {
	l := mcp.Launcher{Command: "glitched", Engine: "/tmp/sock"}
	thread := api.NewID()
	plain := l.For(thread, api.AccessAutoEdits).Instructions
	mgr := l.ForRole(thread, api.AccessAutoEdits, api.RoleManager).Instructions

	if !strings.HasPrefix(mgr, plain) {
		t.Fatalf("manager instructions drop the plain thread's:\nplain=%q\nmanager=%q", plain, mgr)
	}
	rules := map[string][]string{
		"shared state":         {"WORKERS.md", "NOTES.md"},
		"delegation boundary":  {`isolation "worktree"`, "one bounded unit of work per worker", "Keep the root checkout clean"},
		"gatekeeping":          {"pass-through relay", "self-contained brief", "definition of done", "task_answer", "request_credentials", "never the values", "task_cancel", "task_send", "Escalate only legitimate approvals"},
		"verify before ready":  {"completion summary is a claim", "branch, diff, commits and checks", "Never pass unverified output"},
		"quiet by default":     {"notify_user", "quiet by default", "unless the user must act", "unresolved blocker or failure", "verified deliverable ready for review", "Lead every update to the user with the verdict"},
		"git and safety rails": {"Never merge, push, delete branches", "reset --hard", "Preserve uncommitted changes", "Never fabricate test or check results", "Never expose or commit secrets"},
	}
	for area, phrases := range rules {
		for _, p := range phrases {
			if !strings.Contains(mgr, p) {
				t.Errorf("manager instructions miss %s rule %q", area, p)
			}
		}
	}
	if strings.Contains(mgr, "dismiss_wake") {
		t.Errorf("manager instructions still mention dismiss_wake")
	}
	for _, p := range []string{"Manager", "notify_user", "dismiss_wake", "WORKERS.md", "NOTES.md", "pass-through relay", "completion summary is a claim", "quiet by default", "Never merge"} {
		if strings.Contains(plain, p) {
			t.Errorf("plain thread instructions mention %q", p)
		}
	}
}
