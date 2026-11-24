package codex

import (
	"io"
	"reflect"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// An agent Codex spawns works on a thread of its own. Its text, its steps
// and its end, with what its thread spent, nest under the item that
// spawned it, which Codex says twice and is one step; none of it is the
// run's text or tokens, and a thread the run never spawned is nobody's.
func TestASpawnedAgentNestsUnderItsSpawn(t *testing.T) {
	a, _ := fake(t, "subagent")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "spawn one"})
	events := turn(t, g)
	closeRun(t, g)
	if got := text(events); got != "the agent is done" {
		t.Errorf("the run said %q, want only its own text", got)
	}
	nest := func(e provider.Event) provider.Event { return provider.Nested{Parent: "call-spawn", Event: e} }
	exit := 0
	want := []provider.Event{
		provider.Work{Item: "call-spawn", Kind: "tool", Title: "Agent alpha", Status: api.WorkCompleted},
		nest(provider.TextDelta{Text: "Looking."}),
		nest(provider.Work{Item: "cmd-alpha", Kind: "command", Title: "go test ./...", Status: api.WorkStarted}),
		nest(provider.Work{Item: "cmd-alpha", Kind: "command", Title: "go test ./...", Status: api.WorkCompleted, Exit: &exit, Output: "ok\n"}),
		nest(provider.TurnEnded{Outcome: api.OutcomeCompleted, Tokens: 5000}),
	}
	var got []provider.Event
	for _, e := range events {
		switch e := e.(type) {
		case provider.Nested:
			got = append(got, e)
		case provider.Work:
			if e.Item == "call-spawn" {
				got = append(got, e)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read\n%+v\nwant\n%+v", got, want)
	}
}

// bare is a run already started on the app-server of thread root, fed
// the lines Codex sends.
func bare(t *testing.T, root string) (*proc, *Agent) {
	t.Helper()
	p := &proc{thread: root}
	p.conn = newConn(io.Discard, p.handle)
	t.Cleanup(p.conn.end)
	g := newAgent(provider.SessionSpec{Workspace: "/src/app"}, p, nil)
	t.Cleanup(g.cancel)
	g.started, g.turn = true, "t-1"
	return p, g
}

// feed has p take lines, and returns what the run made of them.
func feed(p *proc, g *Agent, lines ...string) []provider.Event {
	for _, l := range lines {
		p.conn.line([]byte(l))
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.out
}

const (
	spawnAlpha = `{"method":"item/started","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"subAgentActivity","id":"spawn-alpha","kind":"started","agentThreadId":"child-alpha","agentPath":"/root/alpha"}}}`
	alphaSays  = `{"method":"item/agentMessage/delta","params":{"threadId":"child-alpha","turnId":"ct-1","itemId":"m","delta":"early"}}`
	alphaEnds  = `{"method":"turn/completed","params":{"threadId":"child-alpha","turn":{"id":"ct-1","status":"completed"}}}`
)

// A child's work that comes before the item naming it waits for that item,
// then plays under it in the order it came.
func TestAChildsWorkBeforeItsSpawnPlaysUnderIt(t *testing.T) {
	p, g := bare(t, "th-1")
	got := feed(p, g, alphaSays, alphaEnds, spawnAlpha)
	want := []provider.Event{
		provider.Work{Item: "spawn-alpha", Kind: "tool", Title: "Agent alpha", Status: api.WorkCompleted},
		provider.Nested{Parent: "spawn-alpha", Event: provider.TextDelta{Text: "early"}},
		provider.Nested{Parent: "spawn-alpha", Event: provider.TurnEnded{Outcome: api.OutcomeCompleted}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read\n%+v\nwant\n%+v", got, want)
	}
}

// A child's turn ends once, after its open steps fail, however often Codex
// says so.
func TestAChildsTurnEndsOnceAfterItsSteps(t *testing.T) {
	p, g := bare(t, "th-1")
	cmd := `{"method":"item/started","params":{"threadId":"child-alpha","turnId":"ct-1","item":{"type":"commandExecution","id":"c","command":"sleep 9","status":"inProgress"}}}`
	got := feed(p, g, spawnAlpha, cmd, alphaEnds, alphaEnds)
	want := []provider.Event{
		provider.Work{Item: "spawn-alpha", Kind: "tool", Title: "Agent alpha", Status: api.WorkCompleted},
		provider.Nested{Parent: "spawn-alpha", Event: provider.Work{Item: "c", Kind: "command", Title: "sleep 9", Status: api.WorkStarted}},
		provider.Nested{Parent: "spawn-alpha", Event: provider.Work{Item: "c", Kind: "command", Title: "sleep 9", Status: api.WorkFailed}},
		provider.Nested{Parent: "spawn-alpha", Event: provider.TurnEnded{Outcome: api.OutcomeCompleted}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read\n%+v\nwant\n%+v", got, want)
	}
}

// Only the item that spawned a thread is a step: news of it later, another
// start of it, and the run's own thread are not, so none comes between the
// run's words.
func TestOnlyASpawnIsAStep(t *testing.T) {
	p, g := bare(t, "th-1")
	activity := func(id, kind, thread, path string) string {
		return `{"method":"item/completed","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"subAgentActivity","id":"` + id + `","kind":"` + kind + `","agentThreadId":"` + thread + `","agentPath":"` + path + `"}}}`
	}
	got := feed(p, g, spawnAlpha,
		activity("done-alpha", "completed", "child-alpha", "/root/alpha"),
		activity("again-alpha", "started", "child-alpha", "/root/alpha"),
		activity("failed-alpha", "failed", "child-alpha", "/root/alpha"),
		activity("root", "started", "th-1", "/root"))
	want := []provider.Event{provider.Work{Item: "spawn-alpha", Kind: "tool", Title: "Agent alpha", Status: api.WorkCompleted}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read\n%+v\nwant\n%+v", got, want)
	}
}
