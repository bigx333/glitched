package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// runLegacyFake is an agent that lists a session's models as Gemini CLI
// 0.71.1 does, in session/new's "models" with no model config option,
// and switches them with session/set_model alone.
func runLegacyFake() {
	w := json.NewEncoder(os.Stdout)
	current := "auto"
	re := func(id json.RawMessage, result any) {
		w.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	fail := func(id json.RawMessage, code int, msg string) {
		w.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
	}
	session := func() map[string]any {
		return map[string]any{
			"configOptions": []any{},
			"modes": map[string]any{"currentModeId": "default", "availableModes": []any{
				map[string]any{"id": "default", "name": "Default"}, map[string]any{"id": "autoEdit", "name": "Auto Edit"},
				map[string]any{"id": "yolo", "name": "YOLO"}, map[string]any{"id": "plan", "name": "Plan"},
			}},
			"models": map[string]any{"currentModelId": current, "availableModels": []any{
				map[string]any{"modelId": "auto", "name": "Auto", "description": "Let Gemini CLI decide"},
				map[string]any{"modelId": "pro", "name": "Pro"},
				map[string]any{"modelId": "flash", "name": "Flash"},
				map[string]any{"modelId": "broken", "name": "Broken"},
			}},
		}
	}
	r := bufio.NewScanner(os.Stdin)
	for r.Scan() {
		var req struct {
			ID     json.RawMessage
			Method string
			Params struct {
				SessionID string `json:"sessionId"`
				ModelID   string `json:"modelId"`
			}
		}
		if err := json.Unmarshal(r.Bytes(), &req); err != nil {
			panic(err)
		}
		switch req.Method {
		case "initialize":
			re(req.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}})
		case "session/new":
			s := session()
			s["sessionId"] = "legacy-1"
			re(req.ID, s)
		case "session/load":
			w.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": req.Params.SessionID, "update": map[string]any{"sessionUpdate": "user_message_chunk", "content": map[string]any{"type": "text", "text": "earlier"}},
			}})
			re(req.ID, session())
		case "session/set_model":
			switch req.Params.ModelID {
			case "auto", "pro", "flash":
				current = req.Params.ModelID
				re(req.ID, map[string]any{})
			case "broken":
				fail(req.ID, -32603, "Model broken is over quota")
			default:
				fail(req.ID, -32602, "Unknown model")
			}
		case "session/set_mode":
			re(req.ID, map[string]any{})
		case "session/prompt":
			// An update of the session's other options says nothing of its
			// models.
			w.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": req.Params.SessionID, "update": map[string]any{"sessionUpdate": "config_option_update", "configOptions": []any{}},
			}})
			w.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": req.Params.SessionID, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "model=" + current}},
			}})
			re(req.ID, map[string]any{"stopReason": "end_turn"})
		default:
			if len(req.ID) > 0 {
				fail(req.ID, -32601, "Method not found: "+req.Method)
			}
		}
	}
}

// legacyFake is an adapter whose agent is runLegacyFake, and the file
// its wire is captured in.
func legacyFake(t *testing.T) (*Adapter, string) {
	t.Helper()
	a, _ := fake(t, "")
	a.Profile = Gemini
	a.Profile.Args = []string{"legacy-fake"}
	a.Profile.Access = provider.Grants{api.AccessAsk: {Mode: "default"}}
	capture := filepath.Join(t.TempDir(), "wire")
	t.Setenv(wireEnv, capture)
	return a, capture
}

// captured are the capture's lines with prefix, the client's ">> " or the
// agent's "<< ".
func captured(t *testing.T, path, prefix string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for line := range strings.Lines(string(b)) {
		if s, ok := strings.CutPrefix(line, prefix); ok {
			out = append(out, s)
		}
	}
	return out
}

func sentMethods(t *testing.T, path string) []string {
	t.Helper()
	var out []string
	for _, line := range captured(t, path, ">> ") {
		var m struct{ Method string }
		json.Unmarshal([]byte(line), &m)
		if m.Method != "" {
			out = append(out, m.Method)
		}
	}
	return out
}

// An agent that lists its models only in a session's "models" field, as
// Gemini CLI 0.71.1 does, lists them all, its current one the default,
// without a model set on the session to look.
func TestLegacySessionModelsAreDescribed(t *testing.T) {
	a, capture := legacyFake(t)
	info, err := a.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.Model{
		{ID: "auto", Name: "Auto", Description: "Let Gemini CLI decide", Default: true},
		{ID: "pro", Name: "Pro"},
		{ID: "flash", Name: "Flash"},
		{ID: "broken", Name: "Broken"},
	}
	got, _ := json.Marshal(info.Models)
	wantJSON, _ := json.Marshal(want)
	if string(got) != string(wantJSON) {
		t.Fatalf("models %s, want %s", got, wantJSON)
	}
	if m := sentMethods(t, capture); slices.Contains(m, "session/set_model") || slices.Contains(m, "session/set_config_option") {
		t.Errorf("describing set a model on the agent: %q", m)
	}
}

// A run's model reaches a legacy agent by session/set_model, before its
// prompt, and the run reports it runs that model. The capture holds what
// the agent sent and got, the agent's own words.
func TestALegacyAgentRunsTheRunsModel(t *testing.T) {
	a, capture := legacyFake(t)
	s := spec(t)
	s.Model = api.ModelRef{Provider: "gemini", Model: "pro"}
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "hi"})
	events := turn(t, g)
	closeRun(t, g)
	if got := text(events); got != "model=pro" {
		t.Errorf("the agent ran %q, want model=pro", got)
	}
	if !slices.ContainsFunc(events, func(e provider.Event) bool { r, ok := e.(provider.Ran); return ok && r.Model == "pro" && r.Why == "" }) {
		t.Errorf("the run reported no Ran on pro: %#v", reports(events))
	}
	sent := sentMethods(t, capture)
	if i, j := slices.Index(sent, "session/set_model"), slices.Index(sent, "session/prompt"); i < 0 || i > j {
		t.Errorf("sent %q, want session/set_model before the prompt", sent)
	}
	if slices.Contains(sent, "session/set_config_option") {
		t.Errorf("a config option went to an agent that has none: %q", sent)
	}
	var setModel string
	for _, line := range captured(t, capture, ">> ") {
		if strings.Contains(line, `"session/set_model"`) {
			setModel = line
		}
	}
	var req struct{ Params map[string]string }
	json.Unmarshal([]byte(setModel), &req)
	if req.Params["sessionId"] != "legacy-1" || req.Params["modelId"] != "pro" || len(req.Params) != 2 {
		t.Errorf("session/set_model went as %s", setModel)
	}
	for _, line := range captured(t, capture, "<< ") {
		if strings.Contains(line, `"category":"model"`) {
			t.Errorf("the capture shows the agent saying what it never said: %s", line)
		}
	}
}

// A model a legacy agent doesn't list fails the run before it reaches the
// agent; one it refuses fails it in the agent's words.
func TestALegacyAgentsRefusedModelFailsTheRun(t *testing.T) {
	a, _ := legacyFake(t)
	s := spec(t)
	s.Model = api.ModelRef{Provider: "gemini", Model: "nope"}
	if _, err := a.Open(context.Background(), s); err == nil || !strings.Contains(err.Error(), "doesn't offer the model nope") {
		t.Fatalf("open on an unlisted model: %v", err)
	}
	s = spec(t)
	s.Model = api.ModelRef{Provider: "gemini", Model: "broken"}
	if _, err := a.Open(context.Background(), s); err == nil || !strings.Contains(err.Error(), "Model broken is over quota") {
		t.Fatalf("open on a refused model: %v", err)
	}
}

// A legacy session taken up again, by session/load, whose answer names no
// session, still has its models.
func TestALoadedLegacySessionHasItsModels(t *testing.T) {
	a, _ := legacyFake(t)
	s := spec(t)
	s.Session = "legacy-old"
	s.Model = api.ModelRef{Provider: "gemini", Model: "flash"}
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "hi"})
	events := turn(t, g)
	closeRun(t, g)
	if got := text(events); got != "model=flash" {
		t.Errorf("the loaded session ran %q, want model=flash", got)
	}
}

// An update of a legacy session's config options keeps its models: the
// thread's next run, on the agent it kept, picks another of them.
func TestALegacySessionKeepsItsModelsThroughAnOptionsUpdate(t *testing.T) {
	a, capture := legacyFake(t)
	s := spec(t)
	for _, model := range []string{"pro", "flash"} {
		s.Model = api.ModelRef{Provider: "gemini", Model: model}
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		if got := text(turn(t, g)); got != "model="+model {
			t.Errorf("the run ran %q, want model=%s", got, model)
		}
		closeRun(t, g)
	}
	if sent := sentMethods(t, capture); len(slices.DeleteFunc(slices.Clone(sent), func(m string) bool { return m != "initialize" })) != 1 {
		t.Errorf("the thread's second run started another agent: %q", sent)
	}
}
