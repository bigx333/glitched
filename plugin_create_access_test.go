package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/plugins"
)

// A plugin's threads.create in the window starts at Ask for approval, not
// at the access the composer's picker shows: only an access it names,
// which the host gates on threads:access, starts it wider.
func TestAPluginCreateStartsAtAskUnlessItNamesAnAccess(t *testing.T) {
	h := start(t, tempDir(t))
	h.a.access = accessFull
	create := func(args map[string]string) api.Access {
		t.Helper()
		raw, _ := json.Marshal(args)
		out, err := h.a.pluginEffect("starter", plugins.Effect{Type: "threads.create", Args: raw})
		if err != nil {
			t.Fatal(err)
		}
		id, err := api.ParseID(out.(map[string]string)["thread"])
		if err != nil {
			t.Fatal(err)
		}
		h.until("the thread", func() bool { _, ok := h.a.find(id); return ok })
		th, _ := h.a.find(id)
		return th.View.Access
	}
	ws := t.TempDir()
	if got := create(map[string]string{"prompt": "plain", "workspace": ws}); got != api.AccessAsk {
		t.Errorf("a create naming no access, with Full access in the picker, started at %v, want Ask for approval", got)
	}
	if got := create(map[string]string{"prompt": "wide", "workspace": ws, "access": accessFull}); got != api.AccessFull {
		t.Errorf("a create naming Full access started at %v", got)
	}
}

// composer.submit sends the new thread's draft as Send does, but a plugin
// without threads:access can't start it above Ask for approval.
func TestAPluginSubmitDoesNotStartANewThreadAboveAsk(t *testing.T) {
	h := start(t, tempDir(t))
	h.a.access = accessFull
	h.a.draft = "Go on."
	before := len(h.a.backend.Envs()[0].Threads)
	_, err := h.a.pluginEffect("submitter", plugins.Effect{Type: "composer.submit", Args: json.RawMessage(`{"thread": ""}`)})
	if !errors.Is(err, plugins.ErrDenied) || !strings.Contains(err.Error(), "threads:access") {
		t.Fatalf("composer.submit at Full access answered %v, want a denial naming threads:access", err)
	}
	if h.a.draft != "Go on." || len(h.a.backend.Envs()[0].Threads) != before {
		t.Fatalf("the refused submit sent the draft: draft %q, %d threads, %d before", h.a.draft, len(h.a.backend.Envs()[0].Threads), before)
	}
	h.a.access = accessAsk
	if _, err := h.a.pluginEffect("submitter", plugins.Effect{Type: "composer.submit", Args: json.RawMessage(`{"thread": ""}`)}); err != nil {
		t.Fatalf("composer.submit at Ask for approval: %v", err)
	}
}

// A plugin page's glitched.threads.create in the window takes the
// arguments a service's does: the model it names over the composer's, and
// a provider no engine here runs, or a key, it refuses, as only a service
// keeps a create to one across a restart.
func TestAPluginCreateInTheWindowTakesTheServicesArguments(t *testing.T) {
	dir, ws := tempDir(t), t.TempDir()
	dst := filepath.Join(plugins.Dir(dir), "starter")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"plugin.json": `{"id": "starter", "name": "Starter", "main": "index.js", "contract": "glitched", "contractVersion": 1, "capabilities": ["threads:write"], "contributes": {"pages": [{"id": "home", "title": "Starter"}]}}`,
		"index.js": `
			async function try_(f) { try { return { ok: await f() } } catch (e) { return { error: e.message } } }
			export async function init() {
				const m = (await glitched.models.list()).models[0]
				const model = { provider: m.provider, model: m.id, effort: m.efforts.at(-1) ?? "", fast: true }
				const workspace = WORKSPACE
				return { state: { model, answers: [
					await try_(() => glitched.threads.create("go", { workspace, title: "Named model", model })),
					await try_(() => glitched.threads.create("go", { workspace, model: { ...model, provider: "nope" } })),
					await try_(() => glitched.threads.create("go", { workspace, model, key: { id: "k", at: "2026-11-24T07:00:00Z" } })),
				] } }
			}
			export function view({ state }) { return { body: { type: "text", text: "answers " + JSON.stringify(state) } } }`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dst, name), []byte(strings.ReplaceAll(body, "WORKSPACE", strconv.Quote(ws))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	signAsOwn(t, dir, dst)
	if err := os.WriteFile(filepath.Join(dir, "plugins.json"), []byte(`{"grants": {"starter": ["threads:write"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	h.click("Starter")
	var got struct {
		Model   api.ModelRef
		Answers []struct {
			OK    *struct{ Thread string }
			Error string
		}
	}
	h.until("the answers", func() bool {
		for _, s := range h.tt.Texts() {
			if j, ok := strings.CutPrefix(s, "answers "); ok {
				return json.Unmarshal([]byte(j), &got) == nil
			}
		}
		return false
	})
	if len(got.Answers) != 3 || got.Answers[0].OK == nil {
		t.Fatalf("answers %+v", got.Answers)
	}
	id, err := api.ParseID(got.Answers[0].OK.Thread)
	if err != nil {
		t.Fatal(err)
	}
	h.until("the thread", func() bool { _, ok := h.a.find(id); return ok })
	if th, _ := h.a.find(id); th.View.Model != got.Model || th.View.Title != "Named model" {
		t.Errorf("the thread %q runs %+v, want %+v", th.View.Title, th.View.Model, got.Model)
	}
	if a := got.Answers[1]; a.OK != nil || !strings.Contains(a.Error, `provider "nope"`) {
		t.Errorf("a create naming a provider no engine runs answered %+v", a)
	}
	if a := got.Answers[2]; a.OK != nil || !strings.Contains(a.Error, "only in a service") {
		t.Errorf("a keyed create in the window answered %+v, want a refusal", a)
	}
	if n := len(h.local().Threads); n != 1 {
		t.Errorf("%d threads, want the named model's alone", n)
	}
}
