package runtime

import (
	"log/slog"
	"strings"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
)

// deltaWindow batches text deltas into one event. Any other event flushes
// the batch first, so requests and turn ends never overtake text.
const deltaWindow = 50 * time.Millisecond

// pump commits one agent's events until its turn ends, the log moves past
// the run, or the host closes. Unless the host is closing, it then closes
// the agent.
func (h *Host) pump(thread api.ID, lr *liveRun) {
	defer h.wg.Done()
	run, ag := lr.run, lr.agent
	events, failed := make(chan provider.Event), make(chan error, 1)
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		for {
			ev, err := ag.Next(lr.ctx)
			if err != nil {
				failed <- err
				return
			}
			select {
			case events <- ev:
			case <-lr.ctx.Done():
				return
			}
		}
	}()

	commit := func(o engine.Observation) {
		if err := h.observe(thread, o, 0); err != nil && h.ctx.Err() == nil {
			slog.Error("runtime: agent event", "run", run, "err", err)
		}
	}
	var text strings.Builder
	var flush <-chan time.Time
	flushText := func() {
		if text.Len() > 0 {
			commit(engine.Delta{Run: run, Text: text.String()})
			text.Reset()
		}
		flush = nil
	}
	end := func(o engine.Observation) {
		flushText()
		if o != nil {
			commit(o)
		}
		h.forget(lr)
		closeAgent(ag)
	}

	for {
		select {
		case <-lr.ctx.Done():
			if h.ctx.Err() == nil {
				end(nil)
			}
			return
		case <-flush:
			flushText()
		case err := <-failed:
			if lr.ctx.Err() == nil {
				end(engine.Ended{Run: run, Outcome: api.OutcomeFailed, Error: err.Error()})
			} else if h.ctx.Err() == nil {
				end(nil)
			}
			return
		case ev := <-events:
			switch ev := ev.(type) {
			case provider.TextDelta:
				text.WriteString(ev.Text)
				if flush == nil {
					flush = time.After(deltaWindow)
				}
			case provider.Work:
				flushText()
				commit(engine.Work{Item: api.WorkItem{Run: run, Item: ev.Item, Kind: ev.Kind, Title: ev.Title, Status: ev.Status}})
			case provider.RequestOpened:
				flushText()
				commit(engine.Surfaced{Run: run, Request: api.NewID(), Dedup: ev.ID, Kind: ev.Kind, Body: ev.Body})
			case provider.TurnEnded:
				end(engine.Ended{Run: run, Outcome: ev.Outcome, Error: ev.Error})
				return
			}
		}
	}
}
