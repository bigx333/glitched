package runtime

import (
	"log/slog"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

const (
	// continuesAtOnce is how many continued runs work at once, as T3 Code's
	// fix for a boot that restarted 54 runs together caps them.
	continuesAtOnce = 2
	// chainCap continues in a row, each asked for within chainWindow of
	// now, end the chain: a run that brings the engine down is continued no
	// more.
	chainCap    = 3
	chainWindow = 5 * time.Minute
)

// continueLost continues, as Continue does, each run the log says is to be
// continued unasked, at most continuesAtOnce of them live at once, those
// the last boots continued among them.
func (h *Host) continueLost() {
	defer h.wg.Done()
	defer close(h.lostDone)
	threads, err := h.store.LiveThreads(h.ctx)
	if err != nil {
		slog.Warn("runtime: couldn't list the runs to continue", "err", err)
		return
	}
	live := map[api.ID]api.ID{} // the continued runs that may still work, by run, to their thread
	var due []api.ID
	for _, id := range threads {
		t, log, err := h.history(id)
		if err != nil {
			continue
		}
		switch r := t.Live(); {
		case r == nil:
		case r.State != engine.RunUncertain:
			if autoContinued(log, r.ID) {
				live[r.ID] = id
			}
		case dueContinue(log, r.ID):
			due = append(due, id)
		}
	}
	for _, id := range due {
		for h.prune(live) >= continuesAtOnce {
			select {
			case h.full <- struct{}{}:
			default:
			}
			select {
			case <-h.ctx.Done():
				return
			case <-h.continued:
			}
		}
		t, log, err := h.history(id)
		if err != nil {
			continue
		}
		r := t.Live()
		if r == nil || r.State != engine.RunUncertain || !dueContinue(log, r.ID) || chain(log, r.ID, h.clock()) >= chainCap {
			continue
		}
		cmd := api.NewID()
		if _, err := h.Submit(h.ctx, api.Command{ID: cmd, Target: id, Body: api.MessageDispatch{Text: api.ContinueText, Mode: api.RestartActive, Replaces: r.ID, Auto: true}}); err != nil {
			if h.ctx.Err() == nil {
				slog.Info("runtime: didn't continue a run a restart cut off", "thread", id, "err", err)
			}
			continue
		}
		live[cmd] = id
	}
}

// history is thread as it is and the log it folds from.
func (h *Host) history(thread api.ID) (engine.Thread, []engine.Event, error) {
	t, err := h.store.Thread(h.ctx, thread)
	if err != nil {
		return t, nil, err
	}
	evs, _, err := h.store.Since(h.ctx, thread, 0, 0)
	if err != nil {
		return t, nil, err
	}
	log := make([]engine.Event, 0, len(evs))
	for _, e := range evs {
		if b, err := e.Decode(); err == nil {
			log = append(log, engine.Event{Thread: thread, At: e.At, Body: b})
		}
	}
	return t, log, nil
}

// prune drops from live the runs that no longer work or wait, and says how
// many are left.
func (h *Host) prune(live map[api.ID]api.ID) int {
	for run, id := range live {
		t, err := h.store.Thread(h.ctx, id)
		if r := t.Live(); err == nil && (r == nil || r.ID != run || r.State == engine.RunUncertain) {
			delete(live, run)
		}
	}
	return len(live)
}

// dueContinue is whether the last time run became uncertain, the engine was
// to continue it unasked.
func dueContinue(log []engine.Event, run api.ID) bool {
	for i := len(log) - 1; i >= 0; i-- {
		if u, ok := log[i].Body.(api.TurnUncertain); ok && u.Run == run {
			return u.Continue
		}
	}
	return false
}

// autoContinued is whether run is one the engine continued unasked.
func autoContinued(log []engine.Event, run api.ID) bool {
	q, _, ok := queued(log, run)
	return ok && q.Auto
}

// chain is how many continues in a row led to run, run's own too, each
// asked for within chainWindow of now.
func chain(log []engine.Event, run api.ID, now time.Time) int {
	n := 0
	for {
		q, at, ok := queued(log, run)
		if !ok || q.Replaces.IsZero() || now.Sub(at) > chainWindow {
			return n
		}
		n++
		run = q.Replaces
	}
}

// queued is the message that started run, as it was queued, and when.
func queued(log []engine.Event, run api.ID) (api.MessageQueued, time.Time, bool) {
	for _, e := range log {
		if q, ok := e.Body.(api.MessageQueued); ok && q.Message == run {
			return q, e.At, true
		}
	}
	return api.MessageQueued{}, time.Time{}, false
}
