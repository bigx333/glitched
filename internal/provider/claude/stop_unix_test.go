//go:build unix

package claude_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

// processes has runs started from now on note their processes, and
// returns the pid of the one in a role. Each is killed after the test.
func processes(t *testing.T) func(role string) int {
	t.Helper()
	file := filepath.Join(t.TempDir(), "pids")
	t.Setenv(envPids, file)
	pid := func(role string) int {
		data, _ := os.ReadFile(file)
		for _, l := range strings.Split(string(data), "\n") {
			var r string
			var p int
			if n, _ := fmt.Sscan(l, &r, &p); n == 2 && r == role {
				return p
			}
		}
		return 0
	}
	t.Cleanup(func() {
		for _, role := range []string{"claude", "tool", "holder"} {
			if p := pid(role); p > 0 {
				syscall.Kill(p, syscall.SIGKILL)
			}
		}
	})
	return pid
}

// exits is whether pid is gone, reaped, within d.
func exits(pid int, d time.Duration) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(10 * time.Millisecond) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

func open(t *testing.T) provider.Agent {
	t.Helper()
	return openIn(t, t.TempDir())
}

// openIn opens a run of the fake claude in workspace ws.
func openIn(t *testing.T, ws string) provider.Agent {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ag, err := (&claude.Adapter{Path: must(os.Executable())}).Open(ctx, provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	return ag
}

// within is f's result, or a failure once d passes without one.
func within[T any](t *testing.T, d time.Duration, what string, f func() T) T {
	t.Helper()
	got := make(chan T, 1)
	go func() { got <- f() }()
	select {
	case v := <-got:
		return v
	case <-time.After(d):
		t.Fatalf("%s took longer than %v", what, d)
		panic("unreachable")
	}
}

// Claude Code runs a tool call in a shell of its own session, out of
// reach of any signal to claude or its group but SIGTERM, which claude
// passes on.
func TestDeletingAThreadStopsItsToolCall(t *testing.T) {
	replay(t, "busy-tool", "")
	pid := processes(t)
	h := host(t, nil)
	thread := newThread(t, h, api.AccessFull)
	submit(t, h, thread, api.MessageDispatch{Text: "Run sleep 60.", Mode: api.StartNow})
	for deadline := time.Now().Add(turnWait); ; time.Sleep(5 * time.Millisecond) {
		snap, err := h.Read(ctx, api.Query{Thread: thread})
		if err != nil {
			t.Fatal(err)
		}
		if started(snap.Events) && pid("tool") > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the tool call never started")
		}
	}
	submit(t, h, thread, api.ThreadDelete{})
	if !exits(pid("tool"), 6*time.Second) {
		t.Error("the deleted thread's tool call is still running")
	}
}

func started(events []api.Event) bool {
	for _, e := range events {
		if w, ok := must(e.Decode()).(api.WorkItem); ok && w.Status == api.WorkStarted {
			return true
		}
	}
	return false
}

func TestASendGivesUpAtItsDeadline(t *testing.T) {
	replay(t, "deaf", "")
	pid := processes(t)
	ag := open(t)
	sendCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	err := within(t, 2*time.Second, "a send claude never reads", func() error {
		return ag.Do(sendCtx, provider.Start{Text: strings.Repeat("x", 1<<20)})
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Do returned %v, want its deadline", err)
	}
	closeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	within(t, time.Second, "Close", func() error { return ag.Close(closeCtx) })
	if !exits(pid("claude"), 3*time.Second) {
		t.Error("claude still runs after its Close ran out of time")
	}
}

func TestCloseReapsClaudeWhileAnotherProcessHoldsItsOutput(t *testing.T) {
	replay(t, "holder", "")
	pid := processes(t)
	ag := open(t)
	// Under -race, the fake takes a second to exit.
	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := within(t, 6*time.Second, "Close", func() error { return ag.Close(closeCtx) }); err != nil {
		t.Errorf("Close returned %v, want claude ended once its input did", err)
	}
	if !exits(pid("claude"), 0) {
		t.Error("Close returned with claude not reaped")
	}
}

// until reads ag's events up to the first that holds for want, which it
// returns.
func until(t *testing.T, ag provider.Agent, want func(provider.Event) bool) provider.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, turnWait)
	defer cancel()
	for {
		e, err := ag.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if want(e) {
			return e
		}
	}
}

func turnEnded(e provider.Event) bool {
	_, ok := e.(provider.TurnEnded)
	return ok
}

// Claude Code ends a stopped turn at the interrupt's result, and 18 ms
// later takes a steer still queued as a turn of its own, writing it to the
// session the next turn resumes. Nothing of that turn may run: the user
// stopped it.
func TestNothingRunsAfterAStoppedTurnEnds(t *testing.T) {
	replay(t, "steer-stop", "")
	ws := t.TempDir()
	ag := openIn(t, ws)
	if err := ag.Do(ctx, provider.Start{Text: "Run ./busy.sh with the Bash tool."}); err != nil {
		t.Fatal(err)
	}
	until(t, ag, func(e provider.Event) bool { w, ok := e.(provider.Work); return ok && w.Status == api.WorkStarted })
	if err := ag.Do(ctx, provider.Steer{Text: "After that, run touch steer-ran."}); err != nil {
		t.Fatal(err)
	}
	if err := ag.Do(ctx, provider.Interrupt{}); err != nil {
		t.Fatal(err)
	}
	if e := until(t, ag, turnEnded).(provider.TurnEnded); e.Outcome != api.OutcomeInterrupted {
		t.Errorf("the turn ended %+v, want interrupted", e)
	}
	if err := ag.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "steer-taken")); err == nil {
		t.Error("claude took the steer after the stopped turn ended")
	}
}

// A stop that reaches Claude Code after it finished the turn leaves the
// turn as Claude Code ended it.
func TestAStopAfterTheTurnCompletedLeavesItCompleted(t *testing.T) {
	replay(t, "stop-after-result", "")
	ag := open(t)
	defer ag.Close(ctx)
	if err := ag.Do(ctx, provider.Start{Text: "Reply done."}); err != nil {
		t.Fatal(err)
	}
	if err := ag.Do(ctx, provider.Interrupt{}); err != nil {
		t.Fatal(err)
	}
	if e := until(t, ag, turnEnded).(provider.TurnEnded); e.Outcome != api.OutcomeCompleted {
		t.Errorf("the turn ended %+v, want completed: Claude Code finished it before it read the stop", e)
	}
}
