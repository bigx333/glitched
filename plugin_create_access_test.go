package main

import (
	"encoding/json"
	"errors"
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
