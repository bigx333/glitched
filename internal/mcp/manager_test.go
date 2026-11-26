package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestDismissWakeExposedOnlyToManagersAndSilencesRun(t *testing.T) {
	gate := &gated{fake.New(fake.Turn(true)), make(chan struct{})}
	h := engineOf(t, provider.Mux{"fake": fake.New(fake.Turn(true)), "gated": gate})

	reg := working(t, h)
	var regBuf bytes.Buffer
	if err := mcp.Serve(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"), &regBuf, &mcp.Tools{Engine: h, Origin: reg}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(regBuf.String(), `"dismiss_wake"`) {
		t.Fatalf("non-manager tools/list unexpectedly exposed dismiss_wake: %s", regBuf.String())
	}
	regReplies := serve(t, h, reg,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dismiss_wake","arguments":{}}}`,
	)
	if !regReplies[2].Result.IsError {
		t.Fatalf("expected non-manager dismiss_wake call to fail cleanly")
	}

	forged := reg
	forged.Role = api.RoleManager
	if out := serve(t, h, forged, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"dismiss_wake","arguments":{}}}`); !out[3].Result.IsError {
		t.Fatalf("a plain thread's dismiss_wake under a forged role succeeded: %+v", out[3].Result)
	}

	asked := workingManager(t, h, "fake", "Coordinate", api.RunWaiting)
	if out := serve(t, h, asked, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"dismiss_wake","arguments":{}}}`); !out[4].Result.IsError {
		t.Fatalf("dismiss_wake on the user's turn succeeded: %+v", out[4].Result)
	}

	mgr := workingManager(t, h, "gated", "", api.RunRunning)
	var mgrBuf bytes.Buffer
	if err := mcp.Serve(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`+"\n"), &mgrBuf, &mcp.Tools{Engine: h, Origin: mgr}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mgrBuf.String(), `"dismiss_wake"`) {
		t.Fatalf("manager tools/list missing dismiss_wake: %s", mgrBuf.String())
	}
	srvOut := serve(t, h, mgr,
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"dismiss_wake","arguments":{"reason":"all green"}}}`,
	)
	if srvOut[10].Result.IsError {
		t.Fatalf("manager dismiss_wake failed: %+v", srvOut[10].Result)
	}
	var got struct {
		Dismissed bool   `json:"dismissed"`
		Reason    string `json:"reason"`
	}
	if len(srvOut[10].Result.Content) > 0 {
		_ = json.Unmarshal([]byte(srvOut[10].Result.Content[0].Text), &got)
	}
	if !got.Dismissed || got.Reason != "all green" {
		t.Fatalf("dismiss_wake reply = %+v, raw=%+v", got, srvOut[10].Result)
	}

	snap, err := h.Read(ctx, api.Query{Thread: mgr.Thread})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Threads[0].Turn == nil || !snap.Threads[0].Turn.Silent || snap.Threads[0].Working() {
		t.Fatalf("expected live turn Silent=true, got %+v", snap.Threads[0])
	}

	if out := serve(t, h, mgr, delegate(11, `{"task":"Read the project","title":"Worker","client_request_id":"dismissed-sweep-child"}`, "")); out[11].Result.IsError {
		t.Fatalf("delegating from a dismissed sweep failed: %+v", out[11].Result)
	}
	if kids := children(t, h, mgr.Thread); len(kids) != 1 {
		t.Fatalf("the dismissed sweep's workers = %+v, want the one it delegated", kids)
	}

	close(gate.open)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		snap, err := h.Read(ctx, api.Query{Thread: mgr.Thread})
		if err != nil {
			t.Fatal(err)
		}
		if th := snap.Threads[0]; len(th.Requests) > 0 {
			if th.Turn == nil || th.Turn.Silent {
				t.Fatalf("the sweep's approval is hidden: %+v", th)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sweep's approval never opened")
		}
	}
	if out := serve(t, h, mgr, `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"dismiss_wake","arguments":{}}}`); !out[12].Result.IsError {
		t.Fatalf("dismiss_wake once the sweep asked the user succeeded: %+v", out[12].Result)
	}

	s := (mcp.Launcher{Command: "glitched", Engine: "/tmp/sock"}).ForRole(mgr.Thread, api.AccessAutoEdits, api.RoleManager)
	if s.Env[mcp.EnvRole] != string(api.RoleManager) || !strings.Contains(s.Instructions, "dismiss_wake") {
		t.Fatalf("unexpected manager MCP server config: env=%+v instructions=%q", s.Env, s.Instructions)
	}
	plain := (mcp.Launcher{Command: "glitched", Engine: "/tmp/sock"}).For(reg.Thread, api.AccessAsk)
	if _, ok := plain.Env[mcp.EnvRole]; ok || strings.Contains(plain.Instructions, "dismiss_wake") {
		t.Fatalf("non-manager MCP server unexpectedly has manager role/instructions: %+v", plain.Env)
	}
}
