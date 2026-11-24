package runtime

import (
	"log/slog"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// lostRun is a run whose agent boot found gone, as it was before: the
// state it was lost in says whether it waited on the user or was stopping.
type lostRun struct {
	thread, run api.ID
	was         engine.RunState
}

const (
	// continuesAtOnce is how many continued runs work at once, as T3 Code's
	// fix for a boot that restarted 54 runs together caps them.
	continuesAtOnce = 2
	// chainCap continues in a row within chainWindow, each lost in turn, end
	// the chain: a run that brings the engine down is continued no more.
	chainCap    = 3
	chainWindow = 5 * time.Minute
)

// continueLost continues the runs boot lost that may be, as Continue does,
// at most continuesAtOnce live at once, in the order boot found them.
func (h *Host) continueLost() {
	defer h.wg.Done()
	defer close(h.lostDone)
	var live []api.ID // continued threads whose run may still be live, by thread
	for _, l := range h.lost {
		for len(live) >= continuesAtOnce {
			select {
			case <-h.ctx.Done():
				return
			case <-h.continued:
			}
			live = h.stillLive(live)
		}
		t, err := h.store.Thread(h.ctx, l.thread)
		if err != nil || !mayContinue(t, l, h.clock()) {
			continue
		}
		_, err = h.Submit(h.ctx, api.Command{ID: api.NewID(), Target: l.thread, Body: api.MessageDispatch{Text: api.ContinueText, Mode: api.RestartActive, Replaces: l.run}})
		if err != nil {
			if h.ctx.Err() == nil {
				slog.Warn("runtime: couldn't continue a run the restart lost", "thread", l.thread, "err", err)
			}
			continue
		}
		live = append(live, l.thread)
	}
}

// stillLive is the threads of ts whose live run works or waits, the run a
// continue started.
func (h *Host) stillLive(ts []api.ID) []api.ID {
	var out []api.ID
	for _, id := range ts {
		t, err := h.store.Thread(h.ctx, id)
		if r := t.Live(); err == nil && r != nil && r.State != engine.RunUncertain {
			out = append(out, id)
		}
	}
	return out
}

// mayContinue is whether l, lost at boot, is continued unasked: a thread of
// the user's, its run still the uncertain one, lost while it worked rather
// than while it waited on the user's answer or stopped at their word; not
// settled or snoozed; and not the end of a chain of continues each lost in
// turn.
func mayContinue(t engine.Thread, l lostRun, now time.Time) bool {
	r := t.Live()
	switch {
	case !t.Parent.IsZero(), r == nil, r.ID != l.run, r.State != engine.RunUncertain:
		return false
	case l.was != engine.RunRunning && l.was != engine.RunStarting:
		return false
	case t.Disposition.State == api.Settled, t.Disposition.State == api.Snoozed && now.Before(t.Disposition.Until):
		return false
	}
	return chain(t, now) < chainCap
}

// chain is how many of t's last runs in a row were continues, the live one
// too, those that ended within chainWindow of now.
func chain(t engine.Thread, now time.Time) int {
	n := 0
	for i := len(t.Runs) - 1; i >= 0; i-- {
		r := t.Runs[i]
		m, _ := t.Message(r.ID)
		if m.Text != api.ContinueText || m.Mode != api.RestartActive || r.State == engine.RunFinished && now.Sub(r.Ended) > chainWindow {
			break
		}
		n++
	}
	return n
}
