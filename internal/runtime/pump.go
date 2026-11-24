package runtime

import (
	"strings"
	"time"
	"unicode/utf8"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/secrets"
)

// deltaWindow batches text deltas into one event. Any other event flushes
// the batch first, so requests and turn ends never overtake text.
const deltaWindow = 50 * time.Millisecond

// pump commits one agent's events until its turn ends, the log moves past
// the run, or the host closes. Each event and each text batch is kept until
// its commit lands; the reader holds at most one more. The agent is closed
// when its turn is over for this host, and only then does its thread take
// effects again; a closing host leaves the agent running.
func (h *Host) pump(thread api.ID, lr *liveRun) {
	defer h.wg.Done()
	run, ag := lr.run, lr.agent
	events := make(chan provider.Event)
	var readErr error
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer close(events)
		for {
			ev, err := ag.Next(lr.ctx)
			if err != nil {
				readErr = err
				return
			}
			events <- ev
		}
	}()

	commit := func(o engine.Observation) bool { return h.record(thread, o, 0) }
	redact := func(s string) string { return secrets.Redact(s, h.credentials(thread)) }
	// Text is shown redacted of the thread's credentials. A timed flush
	// keeps back an end that could be the start of one; any other shows
	// it all.
	var text strings.Builder
	var flush <-chan time.Time
	flushSome := func(final bool) {
		shown, kept := secrets.RedactStream(text.String(), h.credentials(thread), final)
		if shown != "" {
			commit(engine.Delta{Run: run, Text: shown})
		}
		text.Reset()
		text.WriteString(kept)
		flush = nil
	}
	flushText := func() { flushSome(true) }
	// A native subagent's text is a stream of its own, redacted as the
	// run's is, apart from it: neither's events end the other's text.
	nestedText := map[string]*strings.Builder{}
	var nestedOrder []string // the subagents that spoke, in the order they first did
	flushNested := func(parent string, final bool) {
		b := nestedText[parent]
		if b == nil {
			return
		}
		shown, kept := secrets.RedactStream(b.String(), h.credentials(thread), final)
		if shown != "" {
			commit(engine.Subagent{Run: run, Event: api.SubagentDelta{Run: run, Parent: parent, Text: shown}})
		}
		b.Reset()
		b.WriteString(kept)
	}
	flushAllNested := func() {
		for _, parent := range nestedOrder {
			flushNested(parent, true)
		}
	}
	ended := false
	for done := false; !done; {
		select {
		case <-flush:
			flushSome(false)
		case ev, ok := <-events:
			if !ok {
				done = true
				break
			}
			switch ev := ev.(type) {
			case provider.TextDelta:
				text.WriteString(ev.Text)
				if flush == nil {
					flush = time.After(deltaWindow)
				}
			case provider.Work:
				flushText()
				// Only a failed step's output is shown, and a passing
				// command's would fill a catch-up's budget for nothing.
				var output string
				if ev.Status == api.WorkFailed || ev.Exit != nil && *ev.Exit != 0 {
					output = tail(redact(ev.Output), api.OutputLimit)
				}
				commit(engine.Work{Item: api.WorkItem{Run: run, Item: ev.Item, Kind: ev.Kind, Title: redact(ev.Title), Status: ev.Status,
					Path: redact(ev.Path), Exit: ev.Exit, Output: output}})
			case provider.Nested:
				flushSome(false)
				if d, ok := ev.Event.(provider.TextDelta); ok {
					if nestedText[ev.Parent] == nil {
						nestedText[ev.Parent] = &strings.Builder{}
						nestedOrder = append(nestedOrder, ev.Parent)
					}
					nestedText[ev.Parent].WriteString(d.Text)
					flushNested(ev.Parent, false)
					break
				}
				_, end := ev.Event.(provider.TurnEnded)
				flushNested(ev.Parent, end)
				if b := h.nested(run, ev, redact); b != nil {
					commit(engine.Subagent{Run: run, Event: b})
				}
			case provider.Plan:
				// What could be the start of a credential stays held, as a
				// timed flush keeps it: the plan reveals nothing that ends it.
				flushSome(false)
				steps := make([]api.PlanStep, len(ev.Steps))
				for i, s := range ev.Steps {
					steps[i] = api.PlanStep{Text: redact(s.Text), Status: s.Status}
				}
				commit(engine.Plan{Plan: api.PlanUpdated{Run: run, Steps: steps}})
			case provider.Granted:
				commit(engine.Granted{Run: run, Access: ev.Access, Mode: ev.Mode, Why: redact(ev.Why)})
			case provider.Ran:
				commit(engine.Ran{Run: run, Model: redact(ev.Model), Effort: ev.Effort, Why: redact(ev.Why)})
			case provider.Context:
				commit(engine.Context{
					Run: run, Used: ev.Used, Window: ev.Window, Model: redact(ev.Model), Compacted: ev.Compacted,
					CacheExpires: ev.CacheExpires, PreTokens: ev.PreTokens, PostTokens: ev.PostTokens,
				})
			case provider.RequestOpened:
				flushText()
				commit(engine.Surfaced{Run: run, Request: api.NewID(), Dedup: ev.ID, Kind: ev.Kind, Body: secrets.RedactJSON(ev.Body, h.credentials(thread))})
			case provider.RequestWithdrawn:
				flushText()
				commit(engine.Withdrawn{Run: run, Dedup: ev.ID, Reason: redact(ev.Reason)})
			case provider.TurnEnded:
				flushAllNested()
				flushText()
				// An agent can end its turn before the call that answered
				// it returns: what that call did is logged first, so a
				// crash never leaves an ended turn with an answer the
				// agent took logged as on its way.
				h.mu.Lock()
				settled := lr.settled
				h.mu.Unlock()
				if settled != nil {
					select {
					case <-settled:
					case <-lr.ctx.Done():
					}
				}
				if ended = commit(engine.Ended{Run: run, Outcome: ev.Outcome, Error: redact(ev.Error), Tokens: ev.Tokens}); ended {
					h.forget(run)
				}
			}
		}
	}
	flushAllNested()
	flushText()
	switch {
	case ended:
	case h.ctx.Err() != nil:
		return
	case lr.ctx.Err() == nil:
		if !commit(engine.Ended{Run: run, Outcome: api.OutcomeFailed, Error: redact(readErr.Error())}) {
			return
		}
	}
	h.forget(run)
	if h.closeAgent(ag) {
		h.closedAgent(lr)
		h.closed(thread)
	}
}

// nested is a native subagent's step or end as the log keeps it, redacted
// as the run's own are; nil for one no event shows. Its text is streamed
// apart (see pump).
func (h *Host) nested(run api.ID, n provider.Nested, redact func(string) string) api.EventBody {
	switch ev := n.Event.(type) {
	case provider.Work:
		var output string
		if ev.Status == api.WorkFailed || ev.Exit != nil && *ev.Exit != 0 {
			output = tail(redact(ev.Output), api.OutputLimit)
		}
		return api.SubagentWork{Parent: n.Parent, WorkItem: api.WorkItem{Run: run, Item: ev.Item, Kind: ev.Kind, Title: redact(ev.Title), Status: ev.Status,
			Path: redact(ev.Path), Exit: ev.Exit, Output: output}}
	case provider.TurnEnded:
		return api.SubagentEnded{Run: run, Parent: n.Parent, Outcome: ev.Outcome, Tokens: ev.Tokens}
	}
	return nil
}

// tail is the last n bytes of s, from the start of a UTF-8 character.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}
