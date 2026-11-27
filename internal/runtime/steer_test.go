package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/runtime"
	"glitched/internal/secrets"
	"glitched/internal/store"
)

// steerer's agents hold their turn until steered. A steer sends before,
// provider.Steered where marks says so, after (when set) or the steer's
// text reply, and the turn's end (or EOF when eof is set), all before its
// call returns, which takes hold. With stops, the turn ends only at a
// stop, interrupted, naming the steers untaken when untaken is set, which
// get no reply; messages, where set, gets each steer's Message.
type steerer struct {
	marks    bool
	eof      bool
	stops    bool
	untaken  bool
	messages chan api.ID
	before   []provider.Event
	after    []provider.Event
	hold     time.Duration
	err      error
	steered  chan struct{}
}

func (s *steerer) Describe(context.Context) (provider.Info, error) {
	return provider.Info{Name: "fake", Version: "1"}, nil
}

func (s *steerer) Open(context.Context, provider.SessionSpec) (provider.Agent, error) {
	g := &steerAgent{s: s, events: make(chan provider.Event, 16)}
	if s.marks {
		return markingAgent{g}, nil
	}
	return g, nil
}

func (s *steerer) Reconcile(context.Context, provider.RuntimeIdentity) (provider.Recovery, error) {
	return provider.Lost{Reason: "no running agent for this run"}, nil
}

type steerAgent struct {
	s      *steerer
	events chan provider.Event
	mu     sync.Mutex
	sent   []api.ID
}

func (g *steerAgent) Do(ctx context.Context, act provider.Action) error {
	if _, ok := act.(provider.Interrupt); ok && g.s.stops {
		g.mu.Lock()
		var untaken []api.ID
		if g.s.untaken {
			untaken = g.sent
		}
		g.mu.Unlock()
		g.events <- provider.TurnEnded{Outcome: api.OutcomeInterrupted, Untaken: untaken}
		return nil
	}
	a, ok := act.(provider.Steer)
	if !ok {
		return nil
	}
	if g.s.err != nil {
		return g.s.err
	}
	if g.s.messages != nil {
		g.s.messages <- a.Message
	}
	for _, e := range g.s.before {
		g.events <- e
	}
	if g.s.marks {
		g.events <- provider.Steered{}
	}
	switch {
	case len(g.s.after) > 0:
		for _, e := range g.s.after {
			g.events <- e
		}
	case !g.s.untaken:
		g.events <- provider.TextDelta{Text: "Steered: " + a.Text}
	}
	switch {
	case g.s.eof:
		close(g.events)
	case g.s.stops:
		g.mu.Lock()
		g.sent = append(g.sent, a.Message)
		g.mu.Unlock()
	default:
		g.events <- provider.TurnEnded{Outcome: api.OutcomeCompleted}
	}
	if g.s.steered != nil {
		close(g.s.steered)
	}
	select {
	case <-time.After(g.s.hold):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *steerAgent) Next(ctx context.Context) (provider.Event, error) {
	select {
	case e, ok := <-g.events:
		if !ok {
			return nil, io.EOF
		}
		return e, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *steerAgent) Close(context.Context) error { return nil }

type markingAgent struct{ *steerAgent }

func (markingAgent) MarksSteer() bool { return true }

// steered runs a turn the user steers with "hurry" until it is over, and
// returns its node, thread and steered message.
func steered(t *testing.T, ad *steerer) (node, api.ID, api.ID) {
	t.Helper()
	n := start(t, dbPath(t), ad)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "the turn at work", func(th api.Thread) bool { return th.Run == api.RunRunning })
	steer := submit(t, n.h, thread, dispatch("hurry", api.SteerActive)).Command
	until(t, n.h, thread, "the turn's end", idle)
	return n, thread, steer
}

// An agent that ends its turn (or hits EOF) before the steer's call
// returns ends it with the message taken: its dispatch is logged ahead of
// the end, which would otherwise cancel it, and ahead of the steer's reply.
func TestATurnThatEndsAsItIsSteeredKeepsTheMessage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		marks bool
		eof   bool
	}{
		{"marked", true, false},
		{"unmarked", false, false},
		{"marked-eof", true, true},
		{"unmarked-eof", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, thread, steer := steered(t, &steerer{marks: tc.marks, eof: tc.eof, hold: 10 * time.Millisecond})
			stored, err := n.st.Thread(ctx, thread)
			if err != nil {
				t.Fatal(err)
			}
			if m, _ := stored.Message(steer); m.State != engine.QueueDispatched {
				t.Fatalf("the steered message is %v; want dispatched", m.State)
			}
			evs := events(t, n.h, thread)
			dispatched := slices.IndexFunc(evs, func(e api.Event) bool { return e.Type == "message.dispatched" })
			ended := slices.IndexFunc(evs, func(e api.Event) bool { return e.Type == "turn.ended" })
			reply := slices.IndexFunc(evs, func(e api.Event) bool {
				if e.Type != "assistant.delta" {
					return false
				}
				d, _ := e.Decode()
				return d.(api.AssistantDelta).Text == "Steered: hurry"
			})
			if dispatched < 0 || ended < dispatched {
				t.Fatalf("message.dispatched at %d, turn.ended at %d; want the dispatch first", dispatched, ended)
			}
			if reply < 0 || reply < dispatched {
				t.Fatalf("assistant.delta at %d, message.dispatched at %d; want the reply after the message", reply, dispatched)
			}
			wantOutcome := api.OutcomeCompleted
			if tc.eof {
				wantOutcome = api.OutcomeFailed
			}
			if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != wantOutcome {
				t.Fatalf("run ended %v, want %v", got, wantOutcome)
			}
			assertReplay(t, n.st)
		})
	}
}

// A steer the agent's adapter says it never took, as the stop came
// first, ends cancelled with the stopped turn, and the agent is told which
// message each steer carries. A fork of the turn hands over the prompt,
// not the steer the agent never read.
func TestASteerTheAgentNeverTookIsCancelled(t *testing.T) {
	for _, untaken := range []bool{true, false} {
		t.Run(map[bool]string{true: "untaken", false: "taken"}[untaken], func(t *testing.T) {
			ad := &steerer{stops: true, untaken: untaken, messages: make(chan api.ID, 1)}
			other := newForkAdapter(provider.ConversationFork{}, nil, false)
			n := serve(t, dbPath(t), provider.Mux{"fake": ad, "other": other})
			thread := newThread(t, n.h)
			submit(t, n.h, thread, dispatch("go", api.StartNow))
			until(t, n.h, thread, "the turn at work", func(th api.Thread) bool { return th.Run == api.RunRunning })
			steer := submit(t, n.h, thread, dispatch("hurry", api.SteerActive)).Command
			if got := <-ad.messages; got != steer {
				t.Fatalf("the agent was steered with message %v, want %v", got, steer)
			}
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(2 * time.Millisecond) {
				stored, err := n.st.Thread(ctx, thread)
				if err != nil {
					t.Fatal(err)
				}
				if m, _ := stored.Message(steer); m.State == engine.QueueDispatched {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("timed out waiting for the steer's dispatch")
				}
			}
			submit(t, n.h, thread, api.TurnInterrupt{})
			until(t, n.h, thread, "the stopped turn's end", idle)
			if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeInterrupted {
				t.Fatalf("run ended %v, want interrupted", got)
			}
			stored, err := n.st.Thread(ctx, thread)
			if err != nil {
				t.Fatal(err)
			}
			want := engine.QueueDispatched
			if untaken {
				want = engine.QueueCancelled
			}
			if m, _ := stored.Message(steer); m.State != want {
				t.Fatalf("the steer is %v; want %v", m.State, want)
			}

			fork := api.NewID()
			submit(t, n.h, fork, api.ThreadCreate{Workspace: "/src/app", Title: "fork", Model: api.ModelRef{Provider: "other", Model: "other"}, Text: "go on",
				Fork: &api.Fork{Thread: thread, Run: runsOf(t, n.h, thread)[0].Run}})
			until(t, n.h, fork, "the fork's first turn", func(th api.Thread) bool { return idle(th) && len(runsOf(t, n.h, fork)) == 1 })
			_, _, sent := other.seen()
			if len(sent) != 1 || strings.Contains(sent[0], "<user>\ngo\n</user>") != untaken || strings.Contains(sent[0], "<user>\ngo\n\nhurry\n</user>") == untaken {
				t.Fatalf("the fork was handed %q; want the prompt, and the steer only if the agent took it", sent)
			}
			assertReplay(t, n.st)
		})
	}
}

// Text the agent sent just before a steer shows before the message, and
// its answer to the steer after it, never run together, however soon
// after the text the steer comes.
func TestTextBeforeASteerShowsBeforeTheMessage(t *testing.T) {
	n, thread, _ := steered(t, &steerer{marks: true, before: []provider.Event{provider.TextDelta{Text: "Working."}}})
	var got []string
	for _, e := range events(t, n.h, thread) {
		switch e.Type {
		case "message.dispatched":
			got = append(got, "dispatched")
		case "assistant.delta":
			d, err := e.Decode()
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, d.(api.AssistantDelta).Text)
		}
	}
	if want := []string{"Working.", "dispatched", "Steered: hurry"}; !slices.Equal(got, want) {
		t.Fatalf("the log has %q; want %q", got, want)
	}
	assertReplay(t, n.st)
}

// For a marking agent, neither a delay past the text flush window nor a
// work item in the steered prompt can appear above the user's message.
func TestMarkedSteerHoldsWorkAndDelayedTextBehindTheMessage(t *testing.T) {
	n, thread, _ := steered(t, &steerer{
		marks:  true,
		hold:   100 * time.Millisecond,
		before: []provider.Event{provider.TextDelta{Text: "Working."}},
		after: []provider.Event{
			provider.Work{Item: "w1", Kind: "command", Title: "go test", Status: api.WorkCompleted},
			provider.TextDelta{Text: "Steered: hurry"},
		},
	})
	var got []string
	for _, e := range events(t, n.h, thread) {
		switch e.Type {
		case "message.dispatched":
			got = append(got, "dispatched")
		case "work.item":
			got = append(got, "work")
		case "assistant.delta":
			d, err := e.Decode()
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, d.(api.AssistantDelta).Text)
		}
	}
	if want := []string{"Working.", "dispatched", "work", "Steered: hurry"}; !slices.Equal(got, want) {
		t.Fatalf("the log has %q; want %q", got, want)
	}
	assertReplay(t, n.st)
}

// Closing the host while an accepted steer's receipt is retrying does not
// let turn.ended overtake it and cancel the message.
func TestCloseWhileSteerReceiptRetriesKeepsTheMessage(t *testing.T) {
	for _, marks := range []bool{true, false} {
		name := "unmarked"
		if marks {
			name = "marked"
		}
		t.Run(name, func(t *testing.T) {
			path := dbPath(t)
			ad := &steerer{marks: marks, steered: make(chan struct{})}
			n := start(t, path, ad)
			thread := newThread(t, n.h)
			submit(t, n.h, thread, dispatch("go", api.StartNow))
			until(t, n.h, thread, "the turn at work", func(th api.Thread) bool { return th.Run == api.RunRunning })

			heal := failEvents(t, sideDB(t, path), "message.dispatched")
			steer := submit(t, n.h, thread, dispatch("hurry", api.SteerActive)).Command
			<-ad.steered
			time.Sleep(30 * time.Millisecond)

			closed := make(chan error, 1)
			go func() { closed <- n.h.Close() }()
			time.Sleep(40 * time.Millisecond)
			heal()
			if err := <-closed; err != nil {
				t.Fatal(err)
			}

			stored, err := n.st.Thread(ctx, thread)
			if err != nil {
				t.Fatal(err)
			}
			if m, _ := stored.Message(steer); m.State != engine.QueueDispatched {
				t.Fatalf("the steered message is %v; want dispatched", m.State)
			}
			assertReplay(t, n.st)
		})
	}
}

// Withdrawing an open request mid-prompt keeps any potential credential
// prefix buffered instead of finalizing redaction early.
func TestRequestWithdrawnKeepsSplitCredentialsRedacted(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	vault := secrets.New(t.TempDir())
	ad := &steerer{
		marks: true,
		before: []provider.Event{
			provider.RequestOpened{ID: "r1", Kind: api.KindApproval, Body: json.RawMessage(`{"title":"run"}`)},
			provider.TextDelta{Text: "token=" + probe[:8]},
			provider.RequestWithdrawn{ID: "r1", Reason: "steer " + probe},
			provider.TextDelta{Text: probe[8:] + " ok"},
		},
	}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithVault(vault))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Kill()
	thread := newThread(t, h)
	if err := vault.Put(thread, map[string]string{"GLX_PROBE": probe}); err != nil {
		t.Fatal(err)
	}
	submit(t, h, thread, dispatch("go", api.StartNow))
	until(t, h, thread, "the turn at work", func(th api.Thread) bool { return th.Run == api.RunRunning })
	submit(t, h, thread, dispatch("hurry", api.SteerActive))
	until(t, h, thread, "the turn", idle)

	evs := events(t, h, thread)
	for _, e := range evs {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), probe) {
			t.Errorf("%s holds the credential", e.Type)
		}
	}
	if got := transcript(t, evs); got != "token=[GLX_PROBE] okSteered: hurry" {
		t.Fatalf("transcript = %q; want %q", got, "token=[GLX_PROBE] okSteered: hurry")
	}
}

// A failed steer redacts thread credentials from the provider error before
// recording it in the thread log.
func TestFailedSteerRedactsCredentials(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	vault := secrets.New(t.TempDir())
	ad := &steerer{marks: true, err: errors.New("rpc failed: " + probe)}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithVault(vault))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Kill()
	thread := newThread(t, h)
	if err := vault.Put(thread, map[string]string{"GLX_PROBE": probe}); err != nil {
		t.Fatal(err)
	}
	submit(t, h, thread, dispatch("go", api.StartNow))
	until(t, h, thread, "the turn at work", func(th api.Thread) bool { return th.Run == api.RunRunning })
	steer := submit(t, h, thread, dispatch("hurry", api.SteerActive)).Command
	until(t, h, thread, "the steer cancelled", func(api.Thread) bool {
		return slices.ContainsFunc(events(t, h, thread), func(e api.Event) bool {
			if e.Type != "message.cancelled" {
				return false
			}
			b, err := e.Decode()
			return err == nil && b.(api.MessageCancelled).Message == steer
		})
	})
	var reason string
	for _, e := range events(t, h, thread) {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), probe) {
			t.Errorf("%s holds the credential", e.Type)
		}
		if e.Type == "message.cancelled" {
			d, _ := e.Decode()
			if mc := d.(api.MessageCancelled); mc.Message == steer {
				reason = mc.Reason
			}
		}
	}
	if !strings.Contains(reason, "[GLX_PROBE]") {
		t.Fatalf("cancel reason = %q; want [GLX_PROBE]", reason)
	}
}

// Closing the host while the pump is still committing pre-steer text does
// not let message.dispatched overtake the earlier text.
func TestCloseWhilePreSteerTextCommitsKeepsOrder(t *testing.T) {
	path := dbPath(t)
	ad := &steerer{
		marks:   true,
		before:  []provider.Event{provider.TextDelta{Text: "Working."}},
		steered: make(chan struct{}),
	}
	n := start(t, path, ad)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "the turn at work", func(th api.Thread) bool { return th.Run == api.RunRunning })

	heal := failEvents(t, sideDB(t, path), "assistant.delta")
	steer := submit(t, n.h, thread, dispatch("hurry", api.SteerActive)).Command
	<-ad.steered
	time.Sleep(30 * time.Millisecond)

	closed := make(chan error, 1)
	go func() { closed <- n.h.Close() }()
	time.Sleep(40 * time.Millisecond)
	heal()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}

	stored, err := n.st.Thread(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := stored.Message(steer); m.State != engine.QueueDispatched {
		t.Fatalf("the steered message is %v; want dispatched", m.State)
	}
	snap, err := n.st.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	evs := snap.Events
	dispatched := slices.IndexFunc(evs, func(e api.Event) bool { return e.Type == "message.dispatched" })
	before := slices.IndexFunc(evs, func(e api.Event) bool {
		if e.Type != "assistant.delta" {
			return false
		}
		d, _ := e.Decode()
		return d.(api.AssistantDelta).Text == "Working."
	})
	if before < 0 || dispatched < 0 || before > dispatched {
		t.Fatalf("Working. at %d, message.dispatched at %d; want Working. first", before, dispatched)
	}
	assertReplay(t, n.st)
}
