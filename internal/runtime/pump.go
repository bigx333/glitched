package runtime

import (
	"strings"
	"time"
	"unicode/utf8"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
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
	var text strings.Builder
	var flush <-chan time.Time
	flushText := func() {
		if text.Len() > 0 {
			commit(engine.Delta{Run: run, Text: text.String()})
			text.Reset()
		}
		flush = nil
	}
	ended := false
	for done := false; !done; {
		select {
		case <-flush:
			flushText()
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
					output = tail(ev.Output, api.OutputLimit)
				}
				commit(engine.Work{Item: api.WorkItem{Run: run, Item: ev.Item, Kind: ev.Kind, Title: ev.Title, Status: ev.Status,
					Path: ev.Path, Exit: ev.Exit, Output: output}})
			case provider.RequestOpened:
				flushText()
				commit(engine.Surfaced{Run: run, Request: api.NewID(), Dedup: ev.ID, Kind: ev.Kind, Body: ev.Body})
			case provider.RequestWithdrawn:
				flushText()
				commit(engine.Withdrawn{Run: run, Dedup: ev.ID, Reason: ev.Reason})
			case provider.TurnEnded:
				flushText()
				if ended = commit(engine.Ended{Run: run, Outcome: ev.Outcome, Error: ev.Error, Tokens: ev.Tokens}); ended {
					h.forget(run)
				}
			}
		}
	}
	flushText()
	switch {
	case ended:
	case h.ctx.Err() != nil:
		return
	case lr.ctx.Err() == nil:
		if !commit(engine.Ended{Run: run, Outcome: api.OutcomeFailed, Error: readErr.Error()}) {
			return
		}
	}
	h.forget(run)
	if h.closeAgent(ag) {
		h.closed(thread)
	}
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
