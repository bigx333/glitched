package runtime

import (
	"log/slog"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// WithLimitResume has the engine continue a thread whose last turn a usage
// limit ended once the limit resets, as api.Thread.LimitResume says.
func WithLimitResume(on bool) Option {
	return func(h *Host) { h.limitResume.Store(on) }
}

// SetLimitResume applies WithLimitResume's switch at once.
func (h *Host) SetLimitResume(on bool) {
	if h.limitResume.Swap(on) != on {
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
		now := h.clock()
		for _, v := range snap.Threads {
			if run, ok := resumed[v.ID]; ok && (v.Turn == nil || v.Turn.Run != run) {
				delete(resumed, v.ID)
			}
		}
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
				cmd := api.NewID()
				if _, err := h.Submit(h.ctx, api.Command{ID: cmd, Target: v.ID, Revision: v.Revision, Body: api.MessageDispatch{Text: api.LimitContinueText, Mode: api.StartNow, Auto: true}}); err != nil {
					if h.ctx.Err() == nil {
						slog.Info("runtime: didn't continue a thread whose usage limit reset", "thread", v.ID, "err", err)
					}
					continue
				}
				resumed[v.ID] = cmd
			}
		}
		if !next.IsZero() {
			timer.Reset(next.Sub(now))
		}
	}
}
