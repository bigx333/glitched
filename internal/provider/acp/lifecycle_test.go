package acp

import (
	"context"
	"glitched/internal/api"
	"glitched/internal/provider"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAnInitializeErrorSaysWhy(t *testing.T) {
	a := wireFake(t, "init-error")
	_, err := a.Open(t.Context(), spec(t))
	if err == nil || !strings.Contains(err.Error(), "Could not initialize local profile") {
		t.Fatalf("initialize sent explicit RPC reason; Open returned %v", err)
	}
}

func TestRequestsGlitchedCantAllowOnceAreDeclined(t *testing.T) {
	for _, mode := range []string{"deny-fallback", "foreign-permission"} {
		t.Run(mode, func(t *testing.T) {
			a := wireFake(t, mode)
			g := open(t, a, spec(t))
			defer closeRun(t, g)
			do(t, g, provider.Start{Text: "check"})
			events := turn(t, g)
			if got := text(events); !strings.Contains(got, `"outcome":"cancelled"`) {
				t.Fatalf("got %s", got)
			} else {
				t.Logf("wire reply %s", got)
			}
		})
	}
}

func TestAnotherProtocolVersionIsRefused(t *testing.T) {
	a := wireFake(t, "version")
	g, err := a.Open(t.Context(), spec(t))
	if err == nil {
		defer closeRun(t, g)
		t.Fatal("Open returned an agent for wire version 999; client only offered 1")
	}
}

func TestATurnWithNoStopReasonFails(t *testing.T) {
	a := wireFake(t, "unknown-end")
	g := open(t, a, spec(t))
	defer closeRun(t, g)
	do(t, g, provider.Start{Text: "test"})
	e := ended(t, turn(t, g))
	if e.Outcome == api.OutcomeCompleted {
		t.Fatalf("agent returned {}; turn outcome=%v", e.Outcome)
	}
}

func TestACrashedAgentsToolIsStoppedOnClose(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip()
	}
	out := filepath.Join(t.TempDir(), "pid")
	t.Setenv("ACP_WIRE_FAKE_OUT", out)
	a := wireFake(t, "crash-child")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "test"})
	end := ended(t, turn(t, g))
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	closeRun(t, g)
	if alive(pid) {
		t.Fatalf("turn outcome=%v; Close succeeded; child %d still alive", end.Outcome, pid)
	}
}

// Start returns only once the prompt is written: an agent that can't
// take it fails the start, which never claims a send.
func TestAStartTheAgentNeverGotFails(t *testing.T) {
	a, log := fake(t, "hi")
	g := open(t, a, spec(t))
	defer closeRun(t, g)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := g.(*Agent).p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.Do(context.Background(), provider.Start{Text: "never sent"}); err == nil {
		t.Fatal("Start returned nil for a prompt the agent never got")
	}
	if n := len(find(t, wire(t, log), "session/prompt")); n != 0 {
		t.Fatalf("the agent got %d prompts; want none", n)
	}
}

// The agent holds the turn until it is cancelled: one that ended the turn
// first has nothing left to cancel, and gets no cancel.
func TestAStopRightAfterStartFollowsThePrompt(t *testing.T) {
	out := filepath.Join(t.TempDir(), "wire")
	t.Setenv(wireEnv, out)
	a := wireFake(t, "hold")
	g := open(t, a, spec(t))
	defer closeRun(t, g)
	do(t, g, provider.Start{Text: "test"})
	do(t, g, provider.Interrupt{})
	if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeInterrupted {
		t.Fatalf("the stopped turn ended %v, want interrupted", e.Outcome)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	p := strings.Index(s, `"session/prompt"`)
	c := strings.Index(s, `"session/cancel"`)
	if p < 0 || c < 0 || c < p {
		t.Fatalf("wire order (byte offsets): prompt=%d cancel=%d", p, c)
	}
}

// A stop that comes once the agent answered the prompt finds the turn
// ended: there is nothing to cancel, and the turn stays completed.
func TestAStopAfterTheAgentEndedItsTurnSendsNoCancel(t *testing.T) {
	out := filepath.Join(t.TempDir(), "wire")
	t.Setenv(wireEnv, out)
	a := wireFake(t, "plain")
	g := open(t, a, spec(t))
	defer closeRun(t, g)
	do(t, g, provider.Start{Text: "test"})
	<-g.(*Agent).done
	do(t, g, provider.Interrupt{})
	if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeCompleted {
		t.Fatalf("the turn the agent finished ended %v, want completed", e.Outcome)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"session/cancel"`) {
		t.Fatal("a cancel went to an agent whose turn had ended")
	}
}

func TestAnAgentThatSpeaksBetweenTurnsIsNotReused(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ACP_WIRE_FAKE_OUT", dir)
	a := wireFake(t, "unowned")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "first"})
	turn(t, g)
	first := g.(*Agent).p.cmd.Process.Pid
	if err := os.WriteFile(filepath.Join(dir, "between"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	closeRun(t, g)
	g = open(t, a, s)
	second := g.(*Agent).p.cmd.Process.Pid
	defer closeRun(t, g)
	if err := os.WriteFile(filepath.Join(dir, "before"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	ev, err := g.Next(ctx)
	if err == nil {
		t.Fatalf("first pid=%d second pid=%d; second Next before Start returned %#v", first, second, ev)
	}
}

// What a woken agent says before its run's prompt is not the run's: it
// is dropped, and the agent takes no run after this one.
func TestAnAgentThatSpeaksBeforeItsPromptIsNotReused(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ACP_WIRE_FAKE_OUT", dir)
	a := wireFake(t, "unowned")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "first"})
	turn(t, g)
	first := g.(*Agent).p.cmd.Process.Pid
	closeRun(t, g)
	g = open(t, a, s)
	if pid := g.(*Agent).p.cmd.Process.Pid; pid != first {
		t.Fatalf("a quiet agent was not reused: %d, then %d", first, pid)
	}
	for _, phase := range []string{"between", "before"} {
		if err := os.WriteFile(filepath.Join(dir, phase), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if ev, err := g.Next(ctx); err == nil {
		t.Fatalf("the run heard %#v before its prompt", ev)
	}
	closeRun(t, g)
	g = open(t, a, s)
	defer closeRun(t, g)
	if pid := g.(*Agent).p.cmd.Process.Pid; pid == first {
		t.Fatalf("the agent that spoke before its prompt was reused: %d", pid)
	}
}
