package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/acp/acptest"
	"glitched/internal/provider/fake"
)

// held plays a provider whose agents say their first free events, then
// nothing until gate closes, as a model does while it thinks.
type held struct {
	*fake.Adapter
	gate chan struct{}
	free int
}

func (h held) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := h.Adapter.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &heldAgent{Agent: ag, gate: h.gate, free: h.free}, nil
}

type heldAgent struct {
	provider.Agent
	gate chan struct{}
	free int
}

// Next plays the script's first free events, and holds the rest until the
// gate opens; what the agent confirms it runs at plays free.
func (g *heldAgent) Next(ctx context.Context) (provider.Event, error) {
	if g.free == 0 {
		select {
		case <-g.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	e, err := g.Agent.Next(ctx)
	switch e.(type) {
	case provider.Granted, provider.Ran:
	default:
		g.free = max(g.free-1, 0)
	}
	return e, err
}

// holding starts an app whose Claude threads play script, the first free
// events of it at once and the rest once gate closes.
func holding(t *testing.T, script []provider.Event, free int) (*harness, chan struct{}) {
	gate := make(chan struct{})
	h := startWith(t, tempDir(t), func(cfg *daemon.Config) {
		cfg.Providers["claude"] = held{fake.New(script), gate, free}
	})
	return h, gate
}

// shows is whether the window shows text, after the frames a row takes to
// rise in.
func shows(h *harness, text string) bool {
	for range 5 {
		h.tt.Advance(100 * time.Millisecond)
	}
	return h.tt.HasText(text)
}

// A Claude thread says it is thinking from the moment its message is taken
// until the turn ends, though the agent says nothing for a while, and that
// it is stopping once stopped, until the agent hears it.
func TestAClaudeThreadShowsItThinksBeforeItsFirstWord(t *testing.T) {
	h, gate := holding(t, []provider.Event{provider.TextDelta{Text: "Here is the plan."}}, 0)
	sendNew(h, "Plan it.")
	h.until("the run", func() bool { return h.a.current().View.Working() })
	if p := h.a.current().View.Model.Provider; p != "claude" {
		t.Fatalf("the turn runs on %q", p)
	}
	if !shows(h, "Thinking…") {
		t.Fatalf("a Claude thread with no word yet shows no sign of work: %q", h.tt.Texts())
	}
	shoot(t, h, "claude-thinking")
	h.click("Stop")
	h.until("the run stopping", func() bool { return h.a.current().View.Run == api.RunStopping })
	if !shows(h, "Stopping…") {
		t.Fatalf("a thread whose run stops shows no sign of it: %q", h.tt.Texts())
	}
	close(gate)
	h.until("the turn's end", func() bool { return !h.a.current().View.Working() })
	if shows(h, "Thinking…") || shows(h, "Stopping…") {
		t.Fatalf("the thread still says it works after its turn: %q", h.tt.Texts())
	}
}

// Stopped while a step runs, the thread says it is stopping, whatever the
// steps card says.
func TestAThreadStoppedMidStepSaysItIsStopping(t *testing.T) {
	h, gate := holding(t, []provider.Event{provider.Work{Item: "run-1", Kind: "command", Title: "make", Status: api.WorkStarted}}, 1)
	defer close(gate)
	sendNew(h, "Build it.")
	h.until("the step", func() bool { return shows(h, "make") })
	h.click("Stop")
	h.until("the run stopping", func() bool { return h.a.current().View.Run == api.RunStopping })
	if !shows(h, "Stopping…") {
		t.Fatalf("a thread stopped mid-step shows no sign of it: %q", h.tt.Texts())
	}
}

// The thread says it is thinking under the turn it works on, above a
// message queued for the next one.
func TestThinkingShowsAboveAQueuedMessage(t *testing.T) {
	h, gate := holding(t, []provider.Event{provider.TextDelta{Text: "Here is the plan."}}, 0)
	defer close(gate)
	sendNew(h, "Plan it.")
	h.until("the run", func() bool { return h.a.current().View.Working() })
	th := h.a.current()
	above := func(when string, messages ...string) {
		t.Helper()
		thinking, ok := h.tt.Find("Thinking…")
		for _, m := range messages {
			queued, _ := h.tt.Find(m)
			if !ok || thinking.Y >= queued.Y {
				t.Fatalf("%s, Thinking… at %v and %q at %v; want it above: %q", when, thinking, m, queued, h.tt.Texts())
			}
		}
	}
	h.a.backend.Send(th.env, th.id(), "Then build it.", api.AfterActive)
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if !h.tt.HasText("Sending…") {
		t.Fatalf("the message on its way says nothing of it: %q", h.tt.Texts())
	}
	above("before the engine takes it", "Then build it.")
	h.until("the queued message", func() bool { return shows(h, "Then build it.") && shows(h, "Queued") })
	above("once queued", "Then build it.")
	shoot(t, h, "claude-thinking-queued")
	h.a.backend.Send(th.env, th.id(), "Then ship it.", api.AfterActive)
	h.until("both queued", func() bool { return len(th.Transcript.Queue) == 2 && shows(h, "Then ship it.") })
	above("with two queued", "Then build it.", "Then ship it.")
}

// An Antigravity thread says it is thinking from its message until its turn
// ends: before its first word whether or not agy sends thoughts, and
// between its steps, where its steps card no longer shimmers.
func TestAnAntigravityThreadShowsItThinks(t *testing.T) {
	for _, scenario := range []string{"thinking", "thinking-silent", "thinking-tools"} {
		t.Run(scenario, func(t *testing.T) {
			gate := filepath.Join(t.TempDir(), "gate")
			t.Setenv(acptest.Gate, gate)
			h := startWith(t, tempDir(t), withAntigravity(t, scenario))
			allAnswered(h)
			h.until("agy's models", func() bool { return len(h.a.models.reported["antigravity"]) == 2 })
			h.a.model = modelKey{"antigravity", "fake-pro"}
			sendNew(h, "Plan it.")
			h.until("the run", func() bool { return h.a.current().View.Working() })
			if scenario == "thinking-tools" {
				h.until("the first step", func() bool { return h.tt.HasText("Read notes.txt") || h.tt.HasText("Worked for 0s") })
			}
			if !shows(h, "Thinking…") {
				t.Fatalf("an Antigravity thread at work shows no sign of it: %q", h.tt.Texts())
			}
			shoot(t, h, "antigravity-"+scenario)
			if err := os.WriteFile(gate, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			h.until("the turn's end", func() bool { return !h.a.current().View.Working() })
			if shows(h, "Thinking…") {
				t.Fatalf("the thread still says it thinks after its turn: %q", h.tt.Texts())
			}
		})
	}
}

// A remote thread whose engine refuses this app any longer says nothing of
// work it can no longer see end.
func TestAThreadOutOfReachShowsNoWork(t *testing.T) {
	dir := tempDir(t)
	token, err := daemon.Pair(dir, "box")
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	defer close(gate)
	r := engine(t, daemon.Config{Dir: dir, Listen: []string{"127.0.0.1:0"}}, held{fake.New(nil), gate, 0})
	id := seed(t, r, "/srv", "Remote")
	h := start(t, tempDir(t))
	if err := h.addServer(client.Server{Label: "box", Addr: r.Addrs()[0].String(), Token: token}); err != nil {
		t.Fatal(err)
	}
	th := h.thread("Remote")
	h.a.select_(th)
	if _, err := r.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "Go.", Mode: api.AfterActive}}); err != nil {
		t.Fatal(err)
	}
	h.until("the remote run", func() bool { return th.View.Working() && h.tt.HasText("Go.") })
	if !shows(h, "Thinking…") {
		t.Fatalf("a remote thread at work shows no sign of it: %q", h.tt.Texts())
	}
	if err := daemon.Revoke(dir, "box"); err != nil {
		t.Fatal(err)
	}
	h.until("refused", func() bool { return th.env.Status == client.Status(client.Refused{}) })
	if shows(h, "Thinking…") {
		t.Fatalf("a thread out of reach still says it thinks: %q", h.tt.Texts())
	}
}
