//go:build linux || darwin

package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// exits waits a few seconds for pid to exit.
func exits(t *testing.T, pid int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); alive(pid); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d still runs", pid)
		}
	}
}

func pids(t *testing.T, log, method string) []int {
	t.Helper()
	var out []int
	for _, e := range find(t, wire(t, log), method) {
		out = append(out, e.PID)
	}
	return out
}

// A thread's next run takes the agent its last one left, on the same
// session, with no new start; a run at another access starts its own.
func TestAThreadKeepsItsAgent(t *testing.T) {
	a, log := fake(t, "hi")
	s := spec(t)
	for range 2 {
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "Hi."})
		turn(t, g)
		closeRun(t, g)
	}
	if n := len(find(t, wire(t, log), "initialize")); n != 1 {
		t.Fatalf("%d starts for two runs", n)
	}
	prompts := find(t, wire(t, log), "session/prompt")
	var second struct {
		SessionId string
		Prompt    []struct{ Text string }
	}
	json.Unmarshal(prompts[1].Params, &second)
	if second.SessionId != "session-1" || second.Prompt[0].Text != "Hi." {
		t.Fatalf("second prompt %s", prompts[1].Params)
	}
	first := pids(t, log, "initialize")[0]
	s.Access = api.AccessFull
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Hi."})
	turn(t, g)
	closeRun(t, g)
	if n := len(find(t, wire(t, log), "initialize")); n != 2 {
		t.Fatalf("%d starts; a run at another access needs its own", n)
	}
	exits(t, first)
}

// An agent that speaks while parked is stopped, and the thread's next
// run starts another.
func TestAnAgentThatSpeaksWhileParkedIsStopped(t *testing.T) {
	a, log := fake(t, "speaks-while-parked")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Hi."})
	turn(t, g)
	closeRun(t, g)
	first := pids(t, log, "initialize")[0]
	exits(t, first)
	g = open(t, a, s)
	do(t, g, provider.Start{Text: "Again."})
	if got := text(turn(t, g)); got != "Done." {
		t.Fatalf("text %q", got)
	}
	closeRun(t, g)
	if n := len(find(t, wire(t, log), "initialize")); n != 2 {
		t.Fatalf("%d starts", n)
	}
}

// What an agent started goes with it, in a session of its own included,
// whether the agent is stopped or parked and then let go.
func TestWhatAnAgentStartedGoesWithIt(t *testing.T) {
	for _, stopped := range []bool{true, false} {
		a, log := fake(t, "child-process-survives")
		g := open(t, a, spec(t))
		do(t, g, provider.Start{Text: "Start a child."})
		turn(t, g)
		var child int
		json.Unmarshal(find(t, wire(t, log), "child")[0].Params, &child)
		if !alive(child) {
			t.Fatal("no child")
		}
		if stopped {
			if err := g.Do(context.Background(), provider.Interrupt{}); err != nil {
				t.Fatal(err)
			}
			g.(*Agent).p.halted.Store(true)
		}
		closeRun(t, g)
		if !stopped {
			if !alive(child) {
				t.Fatal("a parked agent's child was stopped")
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
		}
		exits(t, child)
		exits(t, pids(t, log, "initialize")[0])
	}
}

// An agent that never answers initialize fails the open within its
// deadline, and is stopped.
func TestAHungStartIsStopped(t *testing.T) {
	a, log := fake(t, "hang-on-initialize")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := a.Open(ctx, spec(t)); err == nil {
		t.Fatal("open succeeded")
	}
	exits(t, pids(t, log, "started")[0])
}

// An agent that doesn't end a cancelled turn is stopped after
// interruptGrace, and the turn ends interrupted.
func TestASlowCancelIsStopped(t *testing.T) {
	old := interruptGrace
	interruptGrace = 300 * time.Millisecond
	t.Cleanup(func() { interruptGrace = old })
	a, log := fake(t, "slow-prompt")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Work."})
	next(t, g)
	do(t, g, provider.Interrupt{})
	if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeInterrupted {
		t.Fatalf("ended %+v", e)
	}
	closeRun(t, g)
	exits(t, pids(t, log, "initialize")[0])
}

// An agent whose output ends while it still runs fails the turn and is
// stopped.
func TestABrokenPipeFailsTheTurn(t *testing.T) {
	a, log := fake(t, "broken-pipe")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Go."})
	if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeFailed {
		t.Fatalf("ended %+v", e)
	}
	closeRun(t, g)
	exits(t, pids(t, log, "initialize")[0])
}

// Reconcile, as a restarted engine runs it, stops what an earlier engine's
// agent left of the thread.
func TestReconcileStopsWhatAnEarlierEngineLeft(t *testing.T) {
	a, log := fake(t, "cancel")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Work."})
	next(t, g)
	pid := pids(t, log, "initialize")[0]
	later := &Adapter{Profile: Antigravity, Path: a.Path}
	rec, err := later.Reconcile(context.Background(), provider.RuntimeIdentity{Thread: s.Thread, Run: s.Run, Generation: 1, Provider: "antigravity"})
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := rec.(provider.Lost); !ok || !strings.Contains(l.Reason, "was stopped") {
		t.Fatalf("recovery %+v", rec)
	}
	exits(t, pid)
	rec, _ = later.Reconcile(context.Background(), provider.RuntimeIdentity{Thread: s.Thread, Run: s.Run, Generation: 1, Provider: "antigravity"})
	if l, ok := rec.(provider.Lost); !ok || !strings.Contains(l.Reason, "no longer runs") {
		t.Fatalf("second recovery %+v", rec)
	}
	closeRun(t, g)
}

// Without the acplive tag, a test can't run any agent but the test binary.
func TestATestCantRunARealAgent(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "acplive") {
			t.Fatalf("recovered %v", r)
		}
	}()
	launch(context.Background(), Antigravity, provider.Program{Path: "/bin/sh"}, t.TempDir(), "test-"+api.NewID().String(), nil)
}
