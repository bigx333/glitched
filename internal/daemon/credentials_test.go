package daemon

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/acp"
	"glitched/internal/provider/acp/acptest"
	"glitched/internal/transport"
)

// An Antigravity agent asks for a credential through glitched mcp: the
// card opens in its thread, the value the user gives reaches the agent's
// file in the thread's vault, and the agent's echo of it is redacted,
// though it came split across two messages. Nothing the engine keeps or
// logs outside the vault holds the value.
func TestAnACPAgentAsksForCredentials(t *testing.T) {
	const value = "probe-value-0123"
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	wire := filepath.Join(t.TempDir(), "wire.jsonl")
	t.Setenv(acptest.Scenario, "credentials")
	t.Setenv(acptest.Log, wire)
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
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "Set up the key.", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var asked []api.RequestOpened
	var answers []string
	for done := false; !done; {
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
		case api.RequestOpened:
			asked = append(asked, b)
			if err := ep.ProvideCredentials(ctx, api.Provide{Thread: thread, Request: b.Request, Generation: b.Generation,
				Values: map[string]string{"GLX_PROBE": value}}); err != nil {
				t.Fatal(err)
			}
		case api.RequestAnswer:
			answers = append(answers, string(b.Answer))
		case api.TurnEnded:
			done = true
		}
	}
	if len(asked) != 1 || asked[0].Kind != api.KindCredentials || len(answers) != 1 || answers[0] != `{"provided":["GLX_PROBE"]}` {
		t.Fatalf("requests %+v answered %q, want one credentials request answered with its name", asked, answers)
	}
	if got, want := text.String(), "provided: the key is [GLX_PROBE]."; got != want {
		t.Errorf("the thread shows %q, want %q", got, want)
	}
	vault := filepath.Join(dir, "secrets", thread.String())
	if b, _ := os.ReadFile(filepath.Join(vault, "GLX_PROBE")); string(b) != value {
		t.Errorf("the vault's GLX_PROBE holds %q", b)
	}
	d.Close()
	filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			if path == filepath.Join(dir, "secrets") {
				return filepath.SkipDir
			}
			return nil
		}
		// Each half too, as text the agent streamed in two parts is kept.
		b, _ := os.ReadFile(path)
		for _, part := range []string{value[:8], value[8:]} {
			if bytes.Contains(b, []byte(part)) {
				t.Errorf("%s holds %q of the value", path, part)
			}
		}
		return nil
	})
	if bytes.Contains(logs.Bytes(), []byte(value)) {
		t.Error("the engine logged the value")
	}
	if b, _ := os.ReadFile(wire); bytes.Contains(b, []byte(value)) {
		t.Error("the agent was sent the value, not only its file")
	}
}
