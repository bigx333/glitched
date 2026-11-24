package runtime

import (
	"log/slog"
	"maps"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// WithLimitResume has the engine continue a thread whose last turn a usage
// limit ended once the limit resets, as api.Thread.LimitResume says.
func WithLimitResume(on bool) Option {
	return func(h *Host) { h.limitResume.Store(on) }
}

// SetLimitResume applies WithLimitResume's switch at once: once it
// returns off, the engine starts no continue of its own.
func (h *Host) SetLimitResume(on bool) {
	h.limitMu.Lock()
	was := h.limitResume.Swap(on)
	h.limitMu.Unlock()
	if was != on {
		h.pokeLimits()
	}
}

// limitOf is the usage limit that failed e's turn, in UTC as the store
// keeps times: nil on a turn that didn't fail, as one the user stopped.
func limitOf(e provider.TurnEnded) *api.UsageLimit {
	if e.Outcome != api.OutcomeFailed || e.Limit == nil {
		return nil
	}
	return &api.UsageLimit{Resets: e.Limit.Resets.UTC().Round(0)}
}

func (h *Host) pokeLimits() {
	select {
	case h.limits <- struct{}{}:
	default:
	}
}

// resumeLimits continues each thread whose usage limit has reset, at most
// continuesAtOnce of those it continued at work at once, and waits for the
// next reset, or a commit that may change what is due.
func (h *Host) resumeLimits() {
	defer h.wg.Done()
	resumed := map[api.ID]api.ID{} // the threads it continued, to the run each started
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-h.limits:
		case <-timer.C:
		}
		if !h.limitResume.Load() {
			continue
		}
		snap, err := h.store.Read(h.ctx, api.Query{})
		if err != nil {
			if h.ctx.Err() == nil {
				slog.Warn("runtime: couldn't read the threads a usage limit holds", "err", err)
			}
			continue
		}
		// A continue holds its place while its run is live; a thread gone,
		// as one deleted, holds none.
		live := map[api.ID]api.ID{}
		for _, v := range snap.Threads {
			if v.Turn != nil {
				live[v.ID] = v.Turn.Run
			}
		}
		maps.DeleteFunc(resumed, func(thread, run api.ID) bool { return live[thread] != run })
		now := h.clock()
		var next time.Time
		for _, v := range snap.Threads {
			at, ok := v.LimitResume(now)
			switch {
			case !ok:
			case at.After(now):
				if next.IsZero() || at.Before(next) {
					next = at
				}
			case len(resumed) < continuesAtOnce:
				if cmd, ok := h.continueLimited(v); ok {
					resumed[v.ID] = cmd
				}
			}
		}
		if !next.IsZero() {
			timer.Reset(next.Sub(now))
		}
	}
}

// continueLimited continues v, whose usage limit reset, unless the switch
// is off by now, and returns its command, which is its run's ID too.
func (h *Host) continueLimited(v api.Thread) (api.ID, bool) {
	h.limitMu.Lock()
	defer h.limitMu.Unlock()
	if !h.limitResume.Load() {
		return api.ID{}, false
	}
	cmd := api.NewID()
	if _, err := h.Submit(h.ctx, api.Command{ID: cmd, Target: v.ID, Revision: v.Revision, Body: api.MessageDispatch{Text: api.LimitContinueText, Mode: api.StartNow, Auto: true}}); err != nil {
		if h.ctx.Err() == nil {
			slog.Info("runtime: didn't continue a thread whose usage limit reset", "thread", v.ID, "err", err)
		}
		return api.ID{}, false
	}
	return cmd, true
}
