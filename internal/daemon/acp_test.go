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
	"glitched/internal/provider/acp"
	"glitched/internal/provider/acp/acptest"
	"glitched/internal/provider/codex/codextest"
	"glitched/internal/transport"
)

func TestMain(m *testing.M) {
	if acptest.Main() || codextest.Main() {
		return
	}
	os.Exit(m.Run())
}

// An engine runs an Antigravity thread through the ACP adapter end to
// end, the test binary playing agy: the turn streams the agent's text and
// steps, its approval comes to the user and the answer to the agent, and
// the thread's next turn goes to the same agent.
func TestAnACPThreadRunsEndToEnd(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "wire.jsonl")
	t.Setenv(acptest.Scenario, "permission")
	t.Setenv(acptest.Log, log)
	dir := stateDir(t)
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Providers: provider.Mux{
		"antigravity": &acp.Adapter{Profile: acp.Antigravity, Path: self},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ep := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer ep.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	thread := api.NewID()
	workspace := t.TempDir()
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{
		Workspace: workspace, Title: "t", Model: api.ModelRef{Provider: "antigravity", Model: "fake-pro", Effort: "low"},
	}}); err != nil {
		t.Fatal(err)
	}
	stream, err := ep.Open(ctx, api.Subscription{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for i, allow := range []bool{true, false} {
		if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "Edit notes.", Mode: api.StartNow}}); err != nil {
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
		want := map[bool]string{true: "allow-once", false: "reject-once"}[allow]
		if end.Outcome != api.OutcomeCompleted || text.String() != "Answer: "+want {
			t.Fatalf("turn %d ended %+v with text %q", i, end, text.String())
		}
		var card map[string]string
		if len(asked) != 1 || json.Unmarshal(asked[0].Body, &card) != nil || card["operation"] != "edit" || card["path"] != "notes.txt" {
			t.Fatalf("turn %d asked %+v", i, asked)
		}
		last := steps[len(steps)-1]
		if last.Kind != "edit" || last.Status != map[bool]api.WorkStatus{true: api.WorkCompleted, false: api.WorkFailed}[allow] {
			t.Fatalf("turn %d steps %+v", i, steps)
		}
	}
	entries, err := acptest.Read(log)
	if err != nil {
		t.Fatal(err)
	}
	var starts, sets int
	for _, e := range entries {
		switch e.Method {
		case "initialize":
			starts++
		case "session/set_config_option":
			sets++
		}
	}
	if starts != 1 || sets != 1 {
		t.Fatalf("%d starts and %d config sets for two turns, want one of each: %v", starts, sets, slices.Collect(func(yield func(string) bool) {
			for _, e := range entries {
				yield(e.Method)
			}
		}))
	}
}

// A request the agent offers no allow-once for comes to the user as a
// card already declined, with why, and the turn goes on: Glitched never
// allows past the one call, and an Allow it can't deliver would stop it.
func TestARequestWithNoAllowOnceIsDeclinedAndTheTurnGoesOn(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(acptest.Scenario, "permission-no-allow-once")
	t.Setenv(acptest.Log, filepath.Join(t.TempDir(), "wire.jsonl"))
	dir := stateDir(t)
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Providers: provider.Mux{
		"antigravity": &acp.Adapter{Profile: acp.Antigravity, Path: self},
	}})
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
		Workspace: t.TempDir(), Title: "t", Model: api.ModelRef{Provider: "antigravity", Model: "fake-pro", Effort: "low"},
	}}); err != nil {
		t.Fatal(err)
	}
	stream, err := ep.Open(ctx, api.Subscription{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "Edit notes.", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	var opened, declined bool
	for {
		item, err := stream.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if item.Kind != api.StreamEvent {
			continue
		}
		b, err := item.Event.Decode()
		if err != nil {
			t.Fatal(err)
		}
		switch e := b.(type) {
		case api.RequestOpened:
			opened = true
		case api.RequestInvalidation:
			declined = strings.Contains(e.Reason, "only once")
		case api.TurnEnded:
			if !opened || !declined || e.Outcome != api.OutcomeCompleted {
				t.Fatalf("opened %v, declined with why %v, turn %v", opened, declined, e.Outcome)
			}
			return
		}
	}
}

// A thread remembers across an engine restart: the engine stores the
// session its first turn bound, and the next engine's agent resumes it.
func TestAnACPThreadRemembersAcrossARestart(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "wire.jsonl")
	t.Setenv(acptest.Scenario, "memory")
	t.Setenv(acptest.Log, log)
	dir := stateDir(t)
	thread := api.NewID()
	workspace := t.TempDir()
	turn := func(text string, create bool) string {
		t.Helper()
		d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Providers: provider.Mux{
			"antigravity": &acp.Adapter{Profile: acp.Antigravity, Path: self},
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		ep := transport.NewEndpointClient(Socket(dir), api.ID{})
		defer ep.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if create {
			if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{
				Workspace: workspace, Title: "t", Model: api.ModelRef{Provider: "antigravity", Model: "fake-pro", Effort: "low"},
			}}); err != nil {
				t.Fatal(err)
			}
		}
		stream, err := ep.Open(ctx, api.Subscription{Thread: thread})
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		sent := api.NewID()
		if _, err := ep.Submit(ctx, api.Command{ID: sent, Target: thread, Body: api.MessageDispatch{Text: text, Mode: api.StartNow}}); err != nil {
			t.Fatal(err)
		}
		var said strings.Builder
		for {
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
				if b.Run == sent {
					said.WriteString(b.Text)
				}
			case api.TurnEnded:
				if b.Run == sent {
					if b.Outcome != api.OutcomeCompleted {
						t.Fatalf("turn %q ended %+v", text, b)
					}
					return said.String()
				}
			}
		}
	}
	turn("Remember the word kumquat.", true)
	if got := turn("What word did I ask you to remember?", false); !strings.Contains(got, "kumquat") {
		t.Fatalf("after a restart the thread answers %q", got)
	}
	entries, err := acptest.Read(log)
	if err != nil {
		t.Fatal(err)
	}
	var news, resumes int
	for _, e := range entries {
		switch e.Method {
		case "session/new":
			news++
		case "session/resume":
			resumes++
		}
	}
	if news != 1 || resumes != 1 {
		t.Fatalf("%d new sessions and %d resumes, want one of each", news, resumes)
	}
}
