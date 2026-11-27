//go:build unix

package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

// stopSteered plays tape: a turn busy in a tool call takes len(steers)
// steers, each carrying its message, and is stopped. It returns how the
// turn ended and the uuids that went to Claude Code.
func stopSteered(t *testing.T, tape string, steers ...api.ID) (provider.TurnEnded, []string) {
	t.Helper()
	replay(t, tape, "")
	var sent prompts
	ag := openTapped(t, sent.tap)
	if err := ag.Do(ctx, provider.Start{Text: "Run ./busy.sh with the Bash tool."}); err != nil {
		t.Fatal(err)
	}
	until(t, ag, func(e provider.Event) bool { w, ok := e.(provider.Work); return ok && w.Status == api.WorkStarted })
	for _, m := range steers {
		if err := ag.Do(ctx, provider.Steer{Text: "After that, run touch steer-ran.", Message: m}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ag.Do(ctx, provider.Interrupt{}); err != nil {
		t.Fatal(err)
	}
	e := until(t, ag, turnEnded).(provider.TurnEnded)
	if e.Outcome != api.OutcomeInterrupted {
		t.Errorf("the turn ended %+v, want interrupted", e)
	}
	ag.Close(ctx)
	sent.mu.Lock()
	defer sent.mu.Unlock()
	return e, slices.Clone(sent.sent)
}

func openTapped(t *testing.T, tap provider.Tap) provider.Agent {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ad := &claude.Adapter{Path: must(os.Executable())}
	t.Cleanup(func() { ad.Close() })
	ag, err := ad.Open(ctx, provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Workspace: t.TempDir(), Tap: tap})
	if err != nil {
		t.Fatal(err)
	}
	return ag
}

func checkUntaken(t *testing.T, e provider.TurnEnded, want ...api.ID) {
	t.Helper()
	if !slices.Equal(e.Untaken, want) {
		t.Errorf("the stopped turn left untaken %v, want %v", e.Untaken, want)
	}
}

// A steer Claude Code only queued when the stop came is one the agent
// never took, whichever way the stop ended the turn.
func TestAStopNamesTheSteersClaudeNeverTook(t *testing.T) {
	t.Run("cancelled by the interrupt", func(t *testing.T) {
		steer := api.NewID()
		e, sent := stopSteered(t, "steer-stop", steer)
		checkUntaken(t, e, steer)
		// The steer goes to Claude Code as its message's uuid.
		if want := claude.SessionOf(steer); len(sent) != 2 || sent[1] != want {
			t.Errorf("sent user messages %v, want the steer as %s", sent, want)
		}
	})
	t.Run("stopped in place of an interrupt", func(t *testing.T) {
		steer := api.NewID()
		e, _ := stopSteered(t, "steer-stop-uncancellable-lifecycle", steer)
		checkUntaken(t, e, steer)
	})
	t.Run("deaf to the interrupt", func(t *testing.T) {
		claude.SetInterruptGrace(t, 100*time.Millisecond)
		steer := api.NewID()
		e, _ := stopSteered(t, "steer-deaf", steer)
		checkUntaken(t, e, steer)
	})
	t.Run("one of two", func(t *testing.T) {
		first, second := api.NewID(), api.NewID()
		e, _ := stopSteered(t, "steer-two-stop", first, second)
		checkUntaken(t, e, second)
	})
}

// A steer Claude Code took, or may have, is not named: Claude Code
// reports a cancellation for a steer it started too.
func TestAStopNamesNoSteerClaudeTook(t *testing.T) {
	t.Run("started, listed and then cancelled", func(t *testing.T) {
		e, _ := stopSteered(t, "steer-taken-stop", api.NewID())
		checkUntaken(t, e)
	})
	t.Run("listed by a status", func(t *testing.T) {
		e, _ := stopSteered(t, "steer-status-stop", api.NewID())
		checkUntaken(t, e)
	})
	t.Run("started, then deaf", func(t *testing.T) {
		claude.SetInterruptGrace(t, 100*time.Millisecond)
		e, _ := stopSteered(t, "steer-deaf-started", api.NewID())
		checkUntaken(t, e)
	})
	// A Claude Code that does not say what it took tells nothing.
	t.Run("without msg_lifecycle_v1", func(t *testing.T) {
		e, _ := stopSteered(t, "steer-stop-uncancellable", api.NewID())
		checkUntaken(t, e)
	})
}

// Only a stopped turn names untaken steers.
func TestATurnThatWasNotStoppedNamesNoSteer(t *testing.T) {
	for _, tape := range []string{"steer", "steer-late", "steer-unlisted"} {
		t.Run(tape, func(t *testing.T) {
			replay(t, tape, "")
			ag := open(t)
			defer ag.Close(ctx)
			if err := ag.Do(ctx, provider.Start{Text: "Run ./slow.sh with the Bash tool. Then tell me what it printed."}); err != nil {
				t.Fatal(err)
			}
			until(t, ag, func(e provider.Event) bool { w, ok := e.(provider.Work); return ok && w.Status == api.WorkStarted })
			if err := ag.Do(ctx, provider.Steer{Text: "Also, after that, say the word BANANA.", Message: api.NewID()}); err != nil {
				t.Fatal(err)
			}
			e := until(t, ag, turnEnded).(provider.TurnEnded)
			if e.Outcome != api.OutcomeCompleted || e.Untaken != nil {
				t.Errorf("the turn ended %+v, want completed with no untaken steer", e)
			}
		})
	}
}

// stopEarly plays tape: a turn takes a steer, carrying steer, and is
// stopped once claude has said the steer is in state, before its system
// init.
func stopEarly(t *testing.T, tape string, steer api.ID, state string) provider.TurnEnded {
	t.Helper()
	replay(t, tape, "")
	said := make(chan struct{})
	var once sync.Once
	ag := openTapped(t, func(_ provider.Protocol, pipe provider.Pipe, line []byte, _ int) {
		var m struct{ Type, State string }
		json.Unmarshal(line, &m)
		if pipe == provider.Stdout && m.Type == "command_lifecycle" && m.State == state {
			once.Do(func() { close(said) })
		}
	})
	defer ag.Close(ctx)
	if err := ag.Do(ctx, provider.Start{Text: "Run ./busy.sh with the Bash tool."}); err != nil {
		t.Fatal(err)
	}
	if err := ag.Do(ctx, provider.Steer{Text: "After that, run touch steer-ran.", Message: steer}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-said:
	case <-time.After(turnWait):
		t.Fatalf("claude never said the steer was %s", state)
	}
	if err := ag.Do(ctx, provider.Interrupt{}); err != nil {
		t.Fatal(err)
	}
	e := until(t, ag, turnEnded).(provider.TurnEnded)
	if e.Outcome != api.OutcomeInterrupted {
		t.Errorf("the turn ended %+v, want interrupted", e)
	}
	return e
}

// Claude Code 2.1.331 names msg_lifecycle_v1 only in its system init,
// which comes seconds after the lifecycle lines it says that with.
func TestAStopBeforeInitNamesTheSteersClaudeNeverTook(t *testing.T) {
	t.Run("queued, stopped in place of an interrupt", func(t *testing.T) {
		steer := api.NewID()
		checkUntaken(t, stopEarly(t, "steer-stop-before-init", steer, "queued"), steer)
	})
	t.Run("queued, interrupted", func(t *testing.T) {
		steer := api.NewID()
		checkUntaken(t, stopEarly(t, "steer-stop-before-init-cancellable", steer, "queued"), steer)
	})
	t.Run("started", func(t *testing.T) {
		checkUntaken(t, stopEarly(t, "steer-taken-before-init", api.NewID(), "started"))
	})
}

// drained has a goroutine read ag's events, as a run does, and gives the
// turn's end.
func drained(ag provider.Agent) <-chan provider.TurnEnded {
	end := make(chan provider.TurnEnded, 1)
	go func() {
		for {
			e, err := ag.Next(ctx)
			if err != nil {
				return
			}
			if e, ok := e.(provider.TurnEnded); ok {
				end <- e
			}
		}
	}()
	return end
}

// A steer bigger than the pipe to Claude Code goes out while Claude Code
// streams, which it does instead of reading the steer until its output
// is read.
func TestABigSteerWritesWhileClaudeStreams(t *testing.T) {
	replay(t, "steer-flood", "")
	ag := open(t)
	defer ag.Close(ctx)
	if err := ag.Do(ctx, provider.Start{Text: "Run ./busy.sh with the Bash tool."}); err != nil {
		t.Fatal(err)
	}
	until(t, ag, func(e provider.Event) bool { w, ok := e.(provider.Work); return ok && w.Status == api.WorkStarted })
	end := drained(ag)
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ag.Do(c, provider.Steer{Text: strings.Repeat("a", 1<<20), Message: api.NewID()}); err != nil {
		t.Fatalf("the steer went out with %v", err)
	}
	select {
	case e := <-end:
		if e.Outcome != api.OutcomeCompleted {
			t.Errorf("the turn ended %+v, want completed", e)
		}
	case <-time.After(turnWait):
		t.Fatal("the turn did not end")
	}
}

// A steer Claude Code starts while the adapter is still writing it is
// one it took.
func TestASteerClaudeStartsWhileItIsWrittenIsTaken(t *testing.T) {
	steer := api.NewID()
	flood := strings.Repeat(`{"recv":{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"ping"}}}`+"\n", 3000)
	tape := filepath.Join(t.TempDir(), "steer-started-mid-write.jsonl")
	os.WriteFile(tape, []byte(`{"sent":{"request":{"subtype":"initialize"},"type":"control_request"}}
{"recv":{"type":"control_response","response":{"subtype":"success","request_id":"glitched-init","response":{}}}}
{"sent":{"type":"user","uuid":"uuid-of-the-first-message"}}
{"recv":{"type":"system","subtype":"init","capabilities":["interrupt_receipt_v1","interrupt_cancel_queued_v1","msg_lifecycle_v1"]}}
{"recv":{"type":"assistant","parent_tool_use_id":null,"message":{"content":[{"type":"tool_use","id":"bash","name":"Bash","input":{"command":"./busy.sh"}}]}}}
{"sleep":"300ms"}
{"recv":{"type":"command_lifecycle","command_uuid":"`+claude.SessionOf(steer)+`","state":"started"}}
`+flood+`{"sent":{"type":"user","uuid":"uuid-of-the-steer"}}
{"sent":{"request":{"subtype":"interrupt"},"type":"control_request"}}
{"recv":{"type":"control_response","response":{"subtype":"success","request_id":"glitched-interrupt","response":{"still_queued":[],"cancelled":[]}}}}
{"recv":{"type":"command_lifecycle","command_uuid":"uuid-of-the-steer","state":"cancelled"}}
{"recv":{"type":"result","subtype":"error_during_execution","is_error":true,"usage":{"input_tokens":3,"output_tokens":4}}}
{"eof":true}
`), 0o644)
	replay(t, "steer-flood", "")
	t.Setenv(envFake, tape)
	ag := open(t)
	defer ag.Close(ctx)
	if err := ag.Do(ctx, provider.Start{Text: "Run ./busy.sh with the Bash tool."}); err != nil {
		t.Fatal(err)
	}
	until(t, ag, func(e provider.Event) bool { w, ok := e.(provider.Work); return ok && w.Status == api.WorkStarted })
	end := drained(ag)
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ag.Do(c, provider.Steer{Text: strings.Repeat("a", 1<<20), Message: steer}); err != nil {
		t.Fatalf("the steer went out with %v", err)
	}
	if err := ag.Do(ctx, provider.Interrupt{}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-end:
		checkUntaken(t, e)
	case <-time.After(turnWait):
		t.Fatal("the turn did not end")
	}
}

// A steer whose write failed is one the run knows failed, not one claude
// never took: only the steer after it, which claude queued, is named.
func TestAStopNamesNoSteerThatFailedToSend(t *testing.T) {
	replay(t, "steer-stop", "")
	var sent prompts
	ag := openTapped(t, sent.tap)
	defer ag.Close(ctx)
	if err := ag.Do(ctx, provider.Start{Text: "Run ./busy.sh with the Bash tool."}); err != nil {
		t.Fatal(err)
	}
	until(t, ag, func(e provider.Event) bool { w, ok := e.(provider.Work); return ok && w.Status == api.WorkStarted })
	failed, queued := api.NewID(), api.NewID()
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if err := ag.Do(gone, provider.Steer{Text: "First, run touch failed-ran.", Message: failed}); err == nil {
		t.Fatal("a steer sent with an ended context went out")
	}
	if err := ag.Do(ctx, provider.Steer{Text: "After that, run touch steer-ran.", Message: queued}); err != nil {
		t.Fatal(err)
	}
	if err := ag.Do(ctx, provider.Interrupt{}); err != nil {
		t.Fatal(err)
	}
	e := until(t, ag, turnEnded).(provider.TurnEnded)
	if e.Outcome != api.OutcomeInterrupted {
		t.Errorf("the turn ended %+v, want interrupted", e)
	}
	checkUntaken(t, e, queued)
	sent.mu.Lock()
	defer sent.mu.Unlock()
	if want := claude.SessionOf(queued); len(sent.sent) != 2 || sent.sent[1] != want {
		t.Errorf("sent user messages %v, want only the queued steer as %s", sent.sent, want)
	}
}
