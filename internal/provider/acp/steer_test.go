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

// upTo is the run's events up to and with the first that ok takes.
func upTo(t *testing.T, g provider.Agent, ok func(provider.Event) bool) []provider.Event {
	t.Helper()
	var out []provider.Event
	for {
		e := next(t, g)
		out = append(out, e)
		if ok(e) {
			return out
		}
	}
}

func saying(text string) func(provider.Event) bool {
	return func(e provider.Event) bool { d, ok := e.(provider.TextDelta); return ok && d.Text == text }
}

// quiet fails the test if the run has another event soon.
func quiet(t *testing.T, g provider.Agent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if e, err := g.Next(ctx); err == nil {
		t.Fatalf("an event after the turn ended: %#v", e)
	}
}

// untilLogged waits until the fake logged method.
func untilLogged(t *testing.T, log, method string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if entries, err := acptest.Read(log); err == nil && slices.ContainsFunc(entries, func(e acptest.Entry) bool { return e.Method == method }) {
			return
		}
	}
	t.Fatalf("the agent never logged %s", method)
}

func prompted(t *testing.T, log string) (texts []string, sessions []string) {
	t.Helper()
	for _, e := range find(t, wire(t, log), "session/prompt") {
		var p struct {
			SessionId string
			Prompt    []struct{ Text string }
		}
		json.Unmarshal(e.Params, &p)
		texts = append(texts, p.Prompt[0].Text)
		sessions = append(sessions, p.SessionId)
	}
	return texts, sessions
}

// A message sent mid-turn cancels the turn's prompt and, once the agent
// ends it, goes as the next prompt on the same session. The run streams
// both and ends once, done, with both prompts' tokens; the agent is kept
// for the thread's next run.
func TestASteerCancelsThePromptAndSendsTheMessage(t *testing.T) {
	a, log := fake(t, "steer")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Work. [hold]"})
	events := upTo(t, g, saying("Working."))
	do(t, g, provider.Steer{Text: "Use tabs."})
	events = append(events, turn(t, g)...)
	quiet(t, g)
	closeRun(t, g)
	if got := text(events); got != "Working.Steered: Use tabs." {
		t.Fatalf("text %q", got)
	}
	if e := ended(t, events); e.Outcome != api.OutcomeCompleted || e.Error != "" || e.Tokens != 110 {
		t.Fatalf("ended %+v; want completed with 110 tokens", e)
	}
	if n := len(slices.DeleteFunc(slices.Clone(events), func(e provider.Event) bool { _, ok := e.(provider.TurnEnded); return !ok })); n != 1 {
		t.Fatalf("%d turn ends", n)
	}
	w := slices.DeleteFunc(methods(wire(t, log)), func(m string) bool { return m != "session/prompt" && m != "session/cancel" && m != "cancelled" })
	// The SDK cancels the prompt before the fake's own cancel handler logs.
	if len(w) != 4 || w[0] != "session/prompt" || w[3] != "session/prompt" || !slices.Contains(w[1:3], "session/cancel") || !slices.Contains(w[1:3], "cancelled") {
		t.Fatalf("wire %v; want the prompt cancelled before the next", w)
	}
	texts, sessions := prompted(t, log)
	if len(texts) != 2 || texts[1] != "Use tabs." || sessions[0] != sessions[1] {
		t.Fatalf("prompts %q on %q; want the message alone on the turn's session", texts, sessions)
	}

	g = open(t, a, s)
	do(t, g, provider.Start{Text: "Again."})
	if got := text(turn(t, g)); got != "Steered: Again." {
		t.Fatalf("the next run's text %q", got)
	}
	closeRun(t, g)
	if n := len(find(t, wire(t, log), "initialize")); n != 1 {
		t.Fatalf("%d starts; want the steered run's agent kept", n)
	}
}

// A steer while the agent waits on the user withdraws the card, answers
// the agent cancelled, and sends the message.
func TestASteerWithdrawsOpenRequests(t *testing.T) {
	for _, tc := range []struct{ tag, method string }{{"ask", "permission answered"}, {"question", "elicitation/create answer"}} {
		t.Run(tc.tag, func(t *testing.T) {
			a, log := fake(t, "steer")
			g := open(t, a, spec(t))
			do(t, g, provider.Start{Text: "Edit. [" + tc.tag + "] [hold]"})
			events := upTo(t, g, func(e provider.Event) bool { _, ok := e.(provider.RequestOpened); return ok })
			id := events[len(events)-1].(provider.RequestOpened).ID
			do(t, g, provider.Steer{Text: "Skip it."})
			events = turn(t, g)
			closeRun(t, g)
			if !slices.ContainsFunc(events, func(e provider.Event) bool { w, ok := e.(provider.RequestWithdrawn); return ok && w.ID == id }) {
				t.Fatalf("the card was not withdrawn: %#v", events)
			}
			if e := ended(t, events); e.Outcome != api.OutcomeCompleted || !strings.HasSuffix(text(events), "Steered: Skip it.") {
				t.Fatalf("ended %+v with text %q", e, text(events))
			}
			if err := g.Do(context.Background(), provider.Respond{Request: id, Answer: json.RawMessage(`{"allow":true}`)}); !errors.Is(err, provider.ErrWithdrawn) {
				t.Fatalf("respond after the steer: %v", err)
			}
			answers := find(t, wire(t, log), tc.method)
			if len(answers) != 1 || !strings.Contains(string(answers[0].Params), "cancel") {
				t.Fatalf("the agent heard %v; want its request cancelled", answers)
			}
		})
	}
}

// Each steer of a run cancels the prompt before it, the last answers.
func TestRepeatedSteers(t *testing.T) {
	a, log := fake(t, "steer")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Work. [hold]"})
	upTo(t, g, saying("Working."))
	do(t, g, provider.Steer{Text: "Then this. [hold]"})
	upTo(t, g, saying("Working."))
	do(t, g, provider.Steer{Text: "Done now."})
	events := turn(t, g)
	quiet(t, g)
	closeRun(t, g)
	if e := ended(t, events); e.Outcome != api.OutcomeCompleted || e.Tokens != 120 || text(events) != "Steered: Done now." {
		t.Fatalf("ended %+v with text %q", e, text(events))
	}
	texts, _ := prompted(t, log)
	if len(texts) != 3 || len(find(t, wire(t, log), "session/cancel")) != 2 {
		t.Fatalf("prompts %q, wire %v", texts, methods(wire(t, log)))
	}
}

// A step the cancelled prompt left open ends before the message's answer:
// failed where the agent cancelled, done where it finished the turn first.
func TestASteerClosesThePromptsSteps(t *testing.T) {
	for _, tc := range []struct {
		tags string
		want api.WorkStatus
	}{{"[tool] [hold]", api.WorkFailed}, {"[tool] [hold] [finish]", api.WorkCompleted}} {
		t.Run(tc.tags, func(t *testing.T) {
			a, _ := fake(t, "steer")
			g := open(t, a, spec(t))
			do(t, g, provider.Start{Text: "Build. " + tc.tags})
			upTo(t, g, saying("Working."))
			do(t, g, provider.Steer{Text: "Stop building."})
			events := turn(t, g)
			closeRun(t, g)
			closed := slices.IndexFunc(events, func(e provider.Event) bool {
				w, ok := e.(provider.Work)
				return ok && w.Item == "run-1" && w.Status == tc.want
			})
			answer := slices.IndexFunc(events, saying("Steered: Stop building."))
			if closed < 0 || answer < 0 || closed > answer {
				t.Fatalf("events %#v; want run-1 %v before the answer", events, tc.want)
			}
			if e := ended(t, events); e.Outcome != api.OutcomeCompleted {
				t.Fatalf("ended %+v", e)
			}
		})
	}
}

// A stop while a steer waits for the agent to cancel ends the turn
// stopped, sends no message, and stops the agent at the run's end.
func TestAStopDuringASteer(t *testing.T) {
	a, log := fake(t, "steer")
	gate := filepath.Join(t.TempDir(), "gate")
	t.Setenv(acptest.Gate, gate)
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Work. [hold] [gate]"})
	upTo(t, g, saying("Working."))
	steered := make(chan error, 1)
	go func() { steered <- g.Do(context.Background(), provider.Steer{Text: "Never sent."}) }()
	untilLogged(t, log, "cancelled")
	do(t, g, provider.Interrupt{})
	os.WriteFile(gate, nil, 0o600)
	if err := <-steered; err == nil {
		t.Fatal("a steer the stop overtook returned nil")
	}
	if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeInterrupted {
		t.Fatalf("ended %+v", e)
	}
	quiet(t, g)
	closeRun(t, g)
	if texts, _ := prompted(t, log); len(texts) != 1 {
		t.Fatalf("prompts %q; want the message never sent", texts)
	}
	if w := methods(wire(t, log)); !slices.Contains(w, "stdin closed") {
		t.Fatalf("wire %v; want the agent stopped", w)
	}
}

// A steer whose call ends while the agent cancels sends no message; the
// turn still ends once the agent ends it.
func TestASteerItsCallingContextEnds(t *testing.T) {
	a, log := fake(t, "steer")
	gate := filepath.Join(t.TempDir(), "gate")
	t.Setenv(acptest.Gate, gate)
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "Work. [hold] [gate]"})
	upTo(t, g, saying("Working."))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := g.Do(ctx, provider.Steer{Text: "Never sent."}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("steer: %v; want its deadline", err)
	}
	os.WriteFile(gate, nil, 0o600)
	if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeInterrupted {
		t.Fatalf("ended %+v", e)
	}
	closeRun(t, g)
	if texts, _ := prompted(t, log); len(texts) != 1 {
		t.Fatalf("prompts %q; want the message never sent", texts)
	}
}

// A steer with no turn to take it, or one the agent fails to cancel, says
// why; it is no unsupported action, and the run still ends.
func TestASteerThatCantBeSent(t *testing.T) {
	t.Run("before the start", func(t *testing.T) {
		a, _ := fake(t, "steer")
		g := open(t, a, spec(t))
		defer closeRun(t, g)
		if err := g.Do(context.Background(), provider.Steer{Text: "Early."}); err == nil || errors.Is(err, provider.ErrUnsupported) {
			t.Fatalf("steer: %v", err)
		}
	})
	t.Run("after the end", func(t *testing.T) {
		a, log := fake(t, "steer")
		g := open(t, a, spec(t))
		defer closeRun(t, g)
		do(t, g, provider.Start{Text: "Hi."})
		turn(t, g)
		if err := g.Do(context.Background(), provider.Steer{Text: "Late."}); err == nil || errors.Is(err, provider.ErrUnsupported) {
			t.Fatalf("steer: %v", err)
		}
		if texts, _ := prompted(t, log); len(texts) != 1 {
			t.Fatalf("prompts %q", texts)
		}
	})
	t.Run("after a stop", func(t *testing.T) {
		a, log := fake(t, "steer")
		g := open(t, a, spec(t))
		defer closeRun(t, g)
		do(t, g, provider.Start{Text: "Work. [hold]"})
		upTo(t, g, saying("Working."))
		do(t, g, provider.Interrupt{})
		if err := g.Do(context.Background(), provider.Steer{Text: "Late."}); err == nil || errors.Is(err, provider.ErrUnsupported) {
			t.Fatalf("steer: %v", err)
		}
		if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeInterrupted {
			t.Fatalf("ended %+v", e)
		}
		if texts, _ := prompted(t, log); len(texts) != 1 {
			t.Fatalf("prompts %q", texts)
		}
	})
	for _, tag := range []string{"crash", "hang"} {
		t.Run(tag, func(t *testing.T) {
			grace := interruptGrace
			interruptGrace = 300 * time.Millisecond
			t.Cleanup(func() { interruptGrace = grace })
			a, log := fake(t, "steer")
			g := open(t, a, spec(t))
			do(t, g, provider.Start{Text: "Work. [hold] [" + tag + "]"})
			upTo(t, g, saying("Working."))
			if err := g.Do(context.Background(), provider.Steer{Text: "Never sent."}); err == nil || errors.Is(err, provider.ErrUnsupported) {
				t.Fatalf("steer: %v", err)
			}
			if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeFailed {
				t.Fatalf("ended %+v", e)
			}
			closeRun(t, g)
			if texts, _ := prompted(t, log); len(texts) != 1 {
				t.Fatalf("prompts %q", texts)
			}
		})
	}
}

// A stop whose steer's call ends first, while the agent ends the prompt
// cancelled before the stop's own cancel, still stops the agent: a turn
// that did not end done never parks it for the thread's next run.
func TestAStopAfterItsSteerGaveUpStopsTheAgent(t *testing.T) {
	a, log := fake(t, "steer")
	gate := filepath.Join(t.TempDir(), "gate")
	t.Setenv(acptest.Gate, gate)
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Work. [hold] [gate]"})
	upTo(t, g, saying("Working."))
	ctx, cancel := context.WithCancel(context.Background())
	steered := make(chan error, 1)
	go func() { steered <- g.Do(ctx, provider.Steer{Text: "Never sent."}) }()
	untilLogged(t, log, "cancelled")
	cancel()
	if err := <-steered; !errors.Is(err, context.Canceled) {
		t.Fatalf("steer: %v; want its call's end", err)
	}
	os.WriteFile(gate, nil, 0o600)
	if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeInterrupted {
		t.Fatalf("ended %+v", e)
	}
	do(t, g, provider.Interrupt{})
	closeRun(t, g)

	g = open(t, a, s)
	do(t, g, provider.Start{Text: "Again."})
	turn(t, g)
	closeRun(t, g)
	if n := len(find(t, wire(t, log), "initialize")); n != 2 {
		t.Fatalf("%d starts; want the stopped run's agent stopped", n)
	}
}

// A steer whose call ended before it began leaves the turn as it was: a
// card stays open for the user's answer, and an earlier steer's cancel
// keeps its grace, so an agent that never ends the prompt is stopped.
func TestASteerAlreadyGivenUpChangesNothing(t *testing.T) {
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	t.Run("an open card", func(t *testing.T) {
		a, log := fake(t, "steer")
		g := open(t, a, spec(t))
		do(t, g, provider.Start{Text: "Edit. [ask] [hold]"})
		events := upTo(t, g, func(e provider.Event) bool { _, ok := e.(provider.RequestOpened); return ok })
		id := events[len(events)-1].(provider.RequestOpened).ID
		if err := g.Do(gone, provider.Steer{Text: "Skip it."}); !errors.Is(err, context.Canceled) {
			t.Fatalf("steer: %v; want its call's end", err)
		}
		do(t, g, provider.Respond{Request: id, Answer: json.RawMessage(`{"allow":true}`)})
		untilLogged(t, log, "permission answered")
		do(t, g, provider.Interrupt{})
		events = turn(t, g)
		closeRun(t, g)
		if slices.ContainsFunc(events, func(e provider.Event) bool { _, ok := e.(provider.RequestWithdrawn); return ok }) {
			t.Fatalf("a card was withdrawn: %#v", events)
		}
		if answers := find(t, wire(t, log), "permission answered"); len(answers) != 1 || !strings.Contains(string(answers[0].Params), "allow-once") {
			t.Fatalf("the agent heard %v; want the user's allow", answers)
		}
		if texts, _ := prompted(t, log); len(texts) != 1 {
			t.Fatalf("prompts %q", texts)
		}
	})
	t.Run("an earlier cancel's grace", func(t *testing.T) {
		grace := interruptGrace
		interruptGrace = 500 * time.Millisecond
		t.Cleanup(func() { interruptGrace = grace })
		a, log := fake(t, "steer")
		g := open(t, a, spec(t))
		do(t, g, provider.Start{Text: "Work. [hold] [hang]"})
		upTo(t, g, saying("Working."))
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		if err := g.Do(ctx, provider.Steer{Text: "First."}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("steer: %v; want its deadline", err)
		}
		if err := g.Do(gone, provider.Steer{Text: "Second."}); !errors.Is(err, context.Canceled) {
			t.Fatalf("steer: %v; want its call's end", err)
		}
		if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeFailed {
			t.Fatalf("ended %+v; want the hung agent stopped", e)
		}
		closeRun(t, g)
		if texts, _ := prompted(t, log); len(texts) != 1 {
			t.Fatalf("prompts %q", texts)
		}
	})
}

// A prompt the agent ends for a reason that fails a turn fails the steered
// one too, and the message never goes.
func TestASteerOverAFailedPrompt(t *testing.T) {
	for _, reason := range []string{"refusal", "max_tokens", "max_turn_requests", "weird"} {
		t.Run(reason, func(t *testing.T) {
			a, log := fake(t, "steer")
			g := open(t, a, spec(t))
			do(t, g, provider.Start{Text: "Work. [hold] [stop=" + reason + "]"})
			upTo(t, g, saying("Working."))
			if err := g.Do(context.Background(), provider.Steer{Text: "Never sent."}); err == nil {
				t.Fatal("steer: nil; want the prompt's failure")
			}
			events := turn(t, g)
			closeRun(t, g)
			if e := ended(t, events); e.Outcome != api.OutcomeFailed || e.Error == "" {
				t.Fatalf("ended %+v; want failed", e)
			}
			if slices.ContainsFunc(events, func(e provider.Event) bool { _, ok := e.(provider.Steered); return ok }) {
				t.Fatalf("the steer was marked: %#v", events)
			}
			if texts, _ := prompted(t, log); len(texts) != 1 {
				t.Fatalf("prompts %q; want the message never sent", texts)
			}
		})
	}
}

// A /compact the agent finished silently as the steer cancelled it says
// the conversation compacted before the message goes; one it failed, or
// answered, fails the turn and sends no message.
func TestASteerOverACompaction(t *testing.T) {
	t.Run("done", func(t *testing.T) {
		a, log := fake(t, "steer-compact")
		g := open(t, a, spec(t))
		do(t, g, provider.Start{Text: "/compact"})
		untilLogged(t, log, "session/prompt")
		do(t, g, provider.Steer{Text: "Go on."})
		events := turn(t, g)
		closeRun(t, g)
		compacted := slices.IndexFunc(events, func(e provider.Event) bool { c, ok := e.(provider.Context); return ok && c.Compacted })
		mark := slices.IndexFunc(events, func(e provider.Event) bool { _, ok := e.(provider.Steered); return ok })
		answer := slices.IndexFunc(events, saying("Steered: Go on."))
		if compacted < 0 || mark < compacted || answer < mark {
			t.Fatalf("events %#v; want the compaction, then the steer, then its answer", events)
		}
		if e := ended(t, events); e.Outcome != api.OutcomeCompleted {
			t.Fatalf("ended %+v", e)
		}
		if texts, _ := prompted(t, log); len(texts) != 2 || texts[0] != "/compact" {
			t.Fatalf("prompts %q", texts)
		}
	})
	for _, scenario := range []string{"steer-compact-failed", "steer-compact-replies"} {
		t.Run(scenario, func(t *testing.T) {
			a, log := fake(t, scenario)
			g := open(t, a, spec(t))
			do(t, g, provider.Start{Text: "/compact"})
			untilLogged(t, log, "session/prompt")
			if err := g.Do(context.Background(), provider.Steer{Text: "Never sent."}); err == nil || !strings.Contains(err.Error(), "compact") {
				t.Fatalf("steer: %v; want the compaction's failure", err)
			}
			events := turn(t, g)
			closeRun(t, g)
			if e := ended(t, events); e.Outcome != api.OutcomeFailed {
				t.Fatalf("ended %+v; want failed", e)
			}
			if slices.ContainsFunc(events, func(e provider.Event) bool { c, ok := e.(provider.Context); return ok && c.Compacted }) {
				t.Fatalf("a failed compaction reported done: %#v", events)
			}
			if texts, _ := prompted(t, log); len(texts) != 1 {
				t.Fatalf("prompts %q; want the message never sent", texts)
			}
		})
	}
}

// A refusal ends its turn failed without discarding the warm agent process.
func TestARefusalKeepsTheAgentParked(t *testing.T) {
	a, log := fake(t, "steer")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Nope. [stop=refusal]"})
	if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeFailed {
		t.Fatalf("ended %+v; want failed", e)
	}
	closeRun(t, g)

	g = open(t, a, s)
	do(t, g, provider.Start{Text: "Next."})
	if e := ended(t, turn(t, g)); e.Outcome != api.OutcomeCompleted {
		t.Fatalf("next run ended %+v; want completed", e)
	}
	closeRun(t, g)
	if n := len(find(t, wire(t, log), "initialize")); n != 1 {
		t.Fatalf("%d starts; want the agent kept after refusal", n)
	}
}
