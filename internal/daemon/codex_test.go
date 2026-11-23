package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/codex"
	"glitched/internal/provider/codex/codextest"
	"glitched/internal/transport"
)

// An engine runs a Codex thread end to end, the test binary playing
// codex: the turn streams Codex's text and its command, the approval
// comes to the user and the answer to Codex, and the thread's next turn
// goes to the same app-server.
func TestACodexThreadRunsEndToEnd(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "wire.jsonl")
	t.Setenv(codextest.Scenario, "command")
	t.Setenv(codextest.Log, log)
	dir := stateDir(t)
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Providers: provider.Mux{"codex": &codex.Adapter{Path: self}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ep := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer ep.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	thread := api.NewID()
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{
		Workspace: t.TempDir(), Title: "t", Model: api.ModelRef{Provider: "codex", Model: "gpt-6-luna", Effort: "low"},
	}}); err != nil {
		t.Fatal(err)
	}
	stream, err := ep.Open(ctx, api.Subscription{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for i, allow := range []bool{true, false} {
		if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "List the files.", Mode: api.StartNow}}); err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		var steps []api.WorkItem
		var asked []api.RequestOpened
		var end api.TurnEnded
		for end.Run.IsZero() {
			item, err := stream.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if item.Kind != api.StreamEvent {
				continue
			}
			body, err := item.Event.Decode()
			if err != nil {
				t.Fatal(err)
			}
			switch b := body.(type) {
			case api.AssistantDelta:
				text.WriteString(b.Text)
			case api.WorkItem:
				steps = append(steps, b)
			case api.RequestOpened:
				asked = append(asked, b)
				answer, _ := json.Marshal(map[string]bool{"allow": allow})
				if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.RequestRespond{Request: b.Request, Generation: b.Generation, Answer: answer}}); err != nil {
					t.Fatal(err)
				}
			case api.TurnEnded:
				end = b
			}
		}
		want := "decision " + map[bool]string{true: "accept", false: "decline"}[allow]
		if end.Outcome != api.OutcomeCompleted || text.String() != want {
			t.Fatalf("turn %d ended %+v with text %q", i, end, text.String())
		}
		var card map[string]string
		if len(asked) != 1 || json.Unmarshal(asked[0].Body, &card) != nil || card["command"] != "ls -a" {
			t.Fatalf("turn %d asked %+v", i, asked)
		}
		last := steps[len(steps)-1]
		if last.Kind != "command" || last.Status != map[bool]api.WorkStatus{true: api.WorkCompleted, false: api.WorkFailed}[allow] {
			t.Fatalf("turn %d steps %+v", i, steps)
		}
	}
	entries, err := codextest.Read(log)
	if err != nil {
		t.Fatal(err)
	}
	var starts, threads int
	for _, e := range entries {
		switch e.Method {
		case "initialize":
			starts++
		case "thread/start":
			threads++
		}
	}
	if starts != 1 || threads != 1 {
		t.Fatalf("%d app-servers and %d threads for two turns, want one of each", starts, threads)
	}
}

// A real build's engine serves Codex when it finds codex, as the user
// installed it or engine.json's codex_path names it; one it can't find,
// it doesn't offer.
func TestWithAgentsServesAnInstalledCodex(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	serves := func(codexPath string) []string {
		cfg, err := Defaults(stateDir(t))
		if err != nil {
			t.Fatal(err)
		}
		cfg.CodexPath = codexPath
		cfg.WithAgents()
		return cfg.Providers.Names()
	}
	if got := serves(""); !slices.Equal(got, []string{"antigravity", "claude"}) {
		t.Errorf("with no codex, serves %q", got)
	}
	own := filepath.Join(t.TempDir(), "my-codex")
	if err := os.WriteFile(own, []byte("#!/bin/sh\\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := serves(own); !slices.Equal(got, []string{"antigravity", "claude", "codex"}) {
		t.Errorf("with codex_path naming one, serves %q", got)
	}
	local := filepath.Join(home, ".local", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("#!/bin/sh\\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := serves(""); !slices.Equal(got, []string{"antigravity", "claude", "codex"}) {
		t.Errorf("with codex in ~/.local/bin, serves %q", got)
	}
	if got := serves(filepath.Join(bin, "no-codex")); !slices.Equal(got, []string{"antigravity", "claude"}) {
		t.Errorf("with codex_path naming nothing, serves %q", got)
	}
}
