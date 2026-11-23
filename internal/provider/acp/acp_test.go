package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/acp/acptest"
)

func TestMain(m *testing.M) {
	if path := os.Getenv(envLaunch); path != "" {
		noteLaunch(path)
		if slices.Contains(os.Args[1:], "--version") {
			os.Stdout.WriteString("1.5.14\n")
			os.Exit(0)
		}
	}
	if len(os.Args) >= 3 && os.Args[1] == "wire-fake" {
		runWireFake(os.Args[2])
		return
	}
	if acptest.Main() {
		return
	}
	os.Exit(m.Run())
}

// fake is an adapter whose agent is the test binary playing scenario,
// and the log the fake writes what it received to.
func fake(t *testing.T, scenario string) (*Adapter, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "wire.jsonl")
	t.Setenv(acptest.Scenario, scenario)
	t.Setenv(acptest.Log, log)
	a := &Adapter{Profile: Antigravity, Path: self}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Errorf("close the adapter: %v", err)
		}
	})
	return a, log
}

func spec(t *testing.T) provider.SessionSpec {
	return provider.SessionSpec{
		Thread: api.NewID(), Run: api.NewID(), Generation: 1, Workspace: t.TempDir(),
		Model: api.ModelRef{Provider: "antigravity"}, Access: api.AccessAsk,
		MCP: []provider.MCPServer{{Name: "glitched", Command: "/bin/glitched", Args: []string{"mcp"},
			Env: map[string]string{"GLITCHED_THREAD": "t"}, Instructions: "Use delegate_task.", Trusted: true}},
	}
}

func open(t *testing.T, a *Adapter, s provider.SessionSpec) provider.Agent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g, err := a.Open(ctx, s)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return g
}

func do(t *testing.T, g provider.Agent, act provider.Action) {
	t.Helper()
	if err := g.Do(context.Background(), act); err != nil {
		t.Fatalf("%T: %v", act, err)
	}
}

// next is the run's next event, within a few seconds.
func next(t *testing.T, g provider.Agent) provider.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	e, err := g.Next(ctx)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	return e
}

// turn is every event of the run up to and with its TurnEnded.
func turn(t *testing.T, g provider.Agent) []provider.Event {
	t.Helper()
	var out []provider.Event
	for {
		e := next(t, g)
		out = append(out, e)
		if _, ok := e.(provider.TurnEnded); ok {
			return out
		}
	}
}

func text(events []provider.Event) string {
	var b strings.Builder
	for _, e := range events {
		if d, ok := e.(provider.TextDelta); ok {
			b.WriteString(d.Text)
		}
	}
	return b.String()
}

func ended(t *testing.T, events []provider.Event) provider.TurnEnded {
	t.Helper()
	return events[len(events)-1].(provider.TurnEnded)
}

func closeRun(t *testing.T, g provider.Agent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := g.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// wire is what the fake received, by method, in order.
func wire(t *testing.T, log string) []acptest.Entry {
	t.Helper()
	entries, err := acptest.Read(log)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func methods(entries []acptest.Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Method)
	}
	return out
}

func find(t *testing.T, entries []acptest.Entry, method string) []acptest.Entry {
	t.Helper()
	return slices.DeleteFunc(slices.Clone(entries), func(e acptest.Entry) bool { return e.Method != method })
}

// A turn streams the agent's text, leaves out its thoughts, and ends
// completed; the session gets the run's MCP servers, and its first prompt
// their instructions in front of the user's words.
func TestATurnStreamsTheAgentsText(t *testing.T) {
	a, log := fake(t, "hi")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Say hi."})
	events := turn(t, g)
	closeRun(t, g)
	if got := text(events); got != "Hi there." {
		t.Fatalf("text %q, want %q", got, "Hi there.")
	}
	if e := ended(t, events); e.Outcome != api.OutcomeCompleted || e.Error != "" {
		t.Fatalf("ended %+v", e)
	}
	w := wire(t, log)
	if got := methods(w)[:3]; !slices.Equal(got, []string{"initialize", "session/new", "session/prompt"}) {
		t.Fatalf("wire %v", got)
	}
	var created struct {
		Cwd        string
		McpServers []struct {
			Name, Command string
			Args          []string
			Env           []struct{ Name, Value string }
		}
	}
	json.Unmarshal(find(t, w, "session/new")[0].Params, &created)
	if created.Cwd != s.Workspace || len(created.McpServers) != 1 || created.McpServers[0].Name != "glitched" ||
		created.McpServers[0].Command != "/bin/glitched" || created.McpServers[0].Env[0].Value != "t" {
		t.Fatalf("session/new %+v", created)
	}
	var prompt struct{ Prompt []struct{ Text string } }
	json.Unmarshal(find(t, w, "session/prompt")[0].Params, &prompt)
	if got := prompt.Prompt[0].Text; !strings.HasPrefix(got, "<instructions>\nUse delegate_task.\n</instructions>") || !strings.HasSuffix(got, "Say hi.") {
		t.Fatalf("prompt %q", got)
	}
}

// The agent's tool calls are the run's steps, filled in as their updates
// come; its thinking is no step.
func TestToolCallsAreSteps(t *testing.T) {
	a, _ := fake(t, "tools")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Look around."})
	var steps []provider.Work
	for _, e := range turn(t, g) {
		if w, ok := e.(provider.Work); ok {
			steps = append(steps, w)
		}
	}
	closeRun(t, g)
	want := []provider.Work{
		{Item: "read-1", Kind: "read", Title: "Read main.go", Status: api.WorkStarted, Path: "main.go"},
		{Item: "read-1", Kind: "read", Title: "Read main.go", Status: api.WorkCompleted, Path: "main.go", Output: "package main"},
		{Item: "run-1", Kind: "command", Title: "ls", Status: api.WorkStarted},
		{Item: "run-1", Kind: "command", Title: "ls", Status: api.WorkFailed, Output: "ls: no such file"},
	}
	if len(steps) != len(want) {
		t.Fatalf("steps %+v", steps)
	}
	for i := range want {
		w := steps[i]
		w.Exit = nil
		if w != want[i] {
			t.Errorf("step %d: %+v, want %+v", i, w, want[i])
		}
	}
}

// An approval the agent asks for is Glitched's own card. Allow answers
// the agent's allow-once option, deny its reject-once one.
func TestPermissionsAskTheUser(t *testing.T) {
	for _, tc := range []struct {
		allow bool
		want  string
	}{{true, "allow-once"}, {false, "reject-once"}} {
		a, log := fake(t, "permission")
		g := open(t, a, spec(t))
		do(t, g, provider.Start{Text: "Edit notes."})
		var opened provider.RequestOpened
		for {
			if r, ok := next(t, g).(provider.RequestOpened); ok {
				opened = r
				break
			}
		}
		var body map[string]string
		json.Unmarshal(opened.Body, &body)
		if opened.Kind != api.KindApproval || body["operation"] != "edit" || body["path"] != "notes.txt" || body["reason"] != "Edit notes.txt" {
			t.Fatalf("opened %s %s", opened.Kind, opened.Body)
		}
		answer, _ := json.Marshal(map[string]bool{"allow": tc.allow})
		do(t, g, provider.Respond{Request: opened.ID, Answer: answer})
		do(t, g, provider.Respond{Request: opened.ID, Answer: answer})
		events := turn(t, g)
		closeRun(t, g)
		if got := text(events); got != "Answer: "+tc.want {
			t.Fatalf("text %q", got)
		}
		if got := find(t, wire(t, log), "permission answered"); len(got) != 1 {
			t.Fatalf("answered %d times", len(got))
		}
	}
}

// An agent that offers no way to allow a call only once can't be allowed
// from the card, which never picks an option that outlasts the call; it
// can still be denied.
func TestNoAllowOnceCannotBeAllowed(t *testing.T) {
	a, _ := fake(t, "permission-no-allow-once")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Edit notes."})
	events := turn(t, g)
	var opened, withdrawn string
	for _, e := range events {
		switch e := e.(type) {
		case provider.RequestOpened:
			opened = e.ID
			if strings.Contains(string(e.Body), "always") {
				t.Errorf("a card that can only be declined offers Always allow: %s", e.Body)
			}
		case provider.RequestWithdrawn:
			if e.ID == opened && strings.Contains(e.Reason, "only once") {
				withdrawn = e.ID
			}
		}
	}
	if opened == "" || withdrawn != opened {
		t.Fatalf("the card was not opened and declined with why: %#v", events)
	}
	if got := text(events); got != "Answer: reject-once" {
		t.Fatalf("text %q", got)
	}
	if end := ended(t, events); end.Outcome != api.OutcomeCompleted {
		t.Fatalf("end %+v", end)
	}
	closeRun(t, g)
}

// Two requests for one call are two cards, each answered on its own.
func TestOverlappingPermissions(t *testing.T) {
	a, _ := fake(t, "permission-overlap")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Clean."})
	var ids []string
	for len(ids) < 2 {
		if r, ok := next(t, g).(provider.RequestOpened); ok {
			var body map[string]string
			json.Unmarshal(r.Body, &body)
			if body["command"] != "rm -rf build" {
				t.Fatalf("body %s", r.Body)
			}
			ids = append(ids, r.ID)
		}
	}
	if ids[0] == ids[1] {
		t.Fatalf("one id for two requests: %v", ids)
	}
	do(t, g, provider.Respond{Request: ids[1], Answer: json.RawMessage(`{"allow":false}`)})
	do(t, g, provider.Respond{Request: ids[0], Answer: json.RawMessage(`{"allow":true}`)})
	got := text(turn(t, g))
	closeRun(t, g)
	if !strings.Contains(got, "allow-once") || !strings.Contains(got, "reject-once") {
		t.Fatalf("text %q", got)
	}
}

// A call of glitched mcp's, a trusted server whose engine decides each
// call itself, never asks.
func TestTrustedServersDontAsk(t *testing.T) {
	a, log := fake(t, "permission-glitched")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Delegate."})
	for _, e := range turn(t, g) {
		if _, ok := e.(provider.RequestOpened); ok {
			t.Fatalf("asked: %+v", e)
		}
	}
	closeRun(t, g)
	var answer string
	json.Unmarshal(find(t, wire(t, log), "permission answered")[0].Params, &answer)
	if answer != "allow-once" {
		t.Fatalf("answered %q", answer)
	}
}

// Stop cancels the turn, which ends interrupted, and the agent that ran
// it is stopped rather than kept.
func TestStopCancelsTheTurn(t *testing.T) {
	a, log := fake(t, "cancel")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Work."})
	e := next(t, g)
	for len(reports([]provider.Event{e})) > 0 {
		e = next(t, g)
	}
	if d, ok := e.(provider.TextDelta); !ok || d.Text != "Working" {
		t.Fatal("no text before the stop")
	}
	do(t, g, provider.Interrupt{})
	do(t, g, provider.Interrupt{})
	if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeInterrupted {
		t.Fatalf("ended %+v", e)
	}
	closeRun(t, g)
	w := methods(wire(t, log))
	if !slices.Contains(w, "session/cancel") || !slices.Contains(w, "cancelled") || !slices.Contains(w, "stdin closed") {
		t.Fatalf("wire %v", w)
	}
}

// A stop with a card open withdraws the card and answers the agent's
// request cancelled, as ACP asks of a client.
func TestStopWithdrawsOpenRequests(t *testing.T) {
	a, log := fake(t, "cancel-with-pending-permission")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Build."})
	var id string
	for id == "" {
		if r, ok := next(t, g).(provider.RequestOpened); ok {
			id = r.ID
		}
	}
	do(t, g, provider.Interrupt{})
	events := turn(t, g)
	if !slices.ContainsFunc(events, func(e provider.Event) bool { w, ok := e.(provider.RequestWithdrawn); return ok && w.ID == id }) {
		t.Fatalf("not withdrawn: %+v", events)
	}
	if e := ended(t, events); e.Outcome != api.OutcomeInterrupted {
		t.Fatalf("ended %+v", e)
	}
	if err := g.Do(context.Background(), provider.Respond{Request: id, Answer: json.RawMessage(`{"allow":true}`)}); !errors.Is(err, provider.ErrWithdrawn) {
		t.Fatalf("respond after the stop: %v", err)
	}
	closeRun(t, g)
	var answer string
	json.Unmarshal(find(t, wire(t, log), "permission answered")[0].Params, &answer)
	if answer != "cancelled" {
		t.Fatalf("answered %q", answer)
	}
}

// A signed-out agent fails the run with how to sign in, as does one that
// prints that its login went stale.
func TestSignedOut(t *testing.T) {
	for _, scenario := range []string{"signed-out", "stale-login-line"} {
		a, _ := fake(t, scenario)
		_, err := a.Open(context.Background(), spec(t))
		if !errors.Is(err, provider.ErrSignedOut) || !strings.Contains(err.Error(), "Run agy in Terminal to sign in.") {
			t.Fatalf("%s: %v", scenario, err)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("%s: the error carries the sign-in link: %v", scenario, err)
		}
		if _, err := a.Describe(context.Background()); !errors.Is(err, provider.ErrSignedOut) {
			t.Fatalf("%s: describe: %v", scenario, err)
		}
	}
}

// Describe lists the agent's models with each one's efforts, from a
// session it opens and closes without a prompt, and answers again from
// what it found. The model and level the session starts on are its
// defaults.
func TestDescribeListsModelsAndEfforts(t *testing.T) {
	a, log := fake(t, "hi")
	for range 2 {
		info, err := a.Describe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		want := provider.Info{Name: "Antigravity", Version: "9.9.9", Models: []provider.Model{
			{ID: "fake-pro", Name: "Fake Pro", Efforts: []provider.Effort{{ID: "low", Label: "Low"}, {ID: "high", Label: "High"}}, Default: true, DefaultEffort: "high"},
			{ID: "fake-flash", Name: "Fake Flash"},
		}, Path: a.Path}
		got, _ := json.Marshal(info)
		wantJSON, _ := json.Marshal(want)
		if string(got) != string(wantJSON) {
			t.Fatalf("info %s, want %s", got, wantJSON)
		}
	}
	w := methods(wire(t, log))
	if slices.Contains(w, "session/prompt") || len(find(t, wire(t, log), "initialize")) != 1 || !slices.Contains(w, "session/close") {
		t.Fatalf("wire %v", w)
	}
}

// A run's model and effort are set on the session before its prompt, and
// only where they differ from the session's.
func TestTheRunsModelIsSet(t *testing.T) {
	a, log := fake(t, "hi")
	s := spec(t)
	s.Model = api.ModelRef{Provider: "antigravity", Model: "fake-pro", Effort: "low"}
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Hi."})
	turn(t, g)
	closeRun(t, g)
	var sets []string
	for _, e := range find(t, wire(t, log), "session/set_config_option") {
		var p struct{ ConfigId, Value string }
		json.Unmarshal(e.Params, &p)
		sets = append(sets, p.ConfigId+"="+p.Value)
	}
	if !slices.Equal(sets, []string{"thought_level=low"}) {
		t.Fatalf("sets %v", sets)
	}
}

// A model the agent refuses fails the run with the agent's reason.
func TestARefusedModelFailsTheRun(t *testing.T) {
	a, _ := fake(t, "reject-model")
	s := spec(t)
	s.Model.Model = "fake-flash"
	if _, err := a.Open(context.Background(), s); err == nil || !strings.Contains(err.Error(), "Model not available on your plan") {
		t.Fatalf("open: %v", err)
	}
	s.Model.Model = "no-such-model"
	if _, err := a.Open(context.Background(), s); err == nil || !strings.Contains(err.Error(), "doesn't offer the model no-such-model") {
		t.Fatalf("open: %v", err)
	}
}

// An agent that dies mid-turn fails it with what the agent printed.
func TestACrashFailsTheTurn(t *testing.T) {
	a, _ := fake(t, "crash-mid-turn")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Go."})
	e := ended(t, turn(t, g))
	if e.Outcome != api.OutcomeFailed || !strings.Contains(e.Error, "out of memory") {
		t.Fatalf("ended %+v", e)
	}
	closeRun(t, g)
}

// A missing binary says how to install it.
func TestAMissingAgentSaysHowToInstall(t *testing.T) {
	a := &Adapter{Profile: Antigravity, Path: filepath.Join(t.TempDir(), "agy")}
	if _, err := a.Open(context.Background(), spec(t)); err == nil || !strings.Contains(err.Error(), "antigravity.google/cli/install.sh") {
		t.Fatalf("open: %v", err)
	}
}

// A run asked to steer says it can't: the composer queues the message for
// the next turn instead.
func TestSteerIsUnsupported(t *testing.T) {
	a, _ := fake(t, "cancel")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Work."})
	if err := g.Do(context.Background(), provider.Steer{Text: "faster"}); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("steer: %v", err)
	}
	do(t, g, provider.Interrupt{})
	turn(t, g)
	closeRun(t, g)
}

// Updates the agent sends before its session is open, as a session's
// history, are no part of the run.
func TestUpdatesBeforeTheSessionGoNowhere(t *testing.T) {
	a, _ := fake(t, "updates-before-new-response")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Say hi."})
	if got := text(turn(t, g)); got != "Hi there." {
		t.Fatalf("text %q", got)
	}
	closeRun(t, g)
}
