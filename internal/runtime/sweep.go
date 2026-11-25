package runtime

import (
	"log/slog"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

func (h *Host) pokeSweeps() {
	select {
	case h.sweeps <- struct{}{}:
	default:
	}
}

func sweepDueAt(v api.Thread, now time.Time) (time.Time, bool) {
	if v.Role != api.RoleManager || v.Sweep == nil || v.Sweep.IntervalSeconds <= 0 ||
		v.Deleted || v.Remote != nil || v.Origin != nil {
		return time.Time{}, false
	}
	if v.Disposition.State == api.Settled {
		return time.Time{}, false
	}
	if v.Disposition.State == api.Snoozed && now.Before(v.Disposition.Until) {
		return time.Time{}, false
	}
	if v.Turn != nil || v.Run != api.RunIdle || v.Resumable || v.Queued > 0 {
		return time.Time{}, false
	}
	base := v.LastSweep
	if v.UpdatedAt.After(base) {
		base = v.UpdatedAt
	}
	if v.LastTurn != nil && v.LastTurn.Ended.After(base) {
		base = v.LastTurn.Ended
	}
	return base.Add(time.Duration(v.Sweep.IntervalSeconds) * time.Second), true
}

// runSweeps triggers periodic scheduled sweeps on Manager threads when due.
func (h *Host) runSweeps() {
	defer h.wg.Done()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-h.sweeps:
		case <-timer.C:
		}
		snap, err := h.store.Read(h.ctx, api.Query{})
		if err != nil {
			if h.ctx.Err() == nil {
				slog.Warn("runtime: couldn't read threads for manager sweeps", "err", err)
			}
			continue
		}
		now := h.clock()
		var next time.Time
		for _, v := range snap.Threads {
			at, ok := sweepDueAt(v, now)
			if !ok {
				continue
			}
			if at.After(now) {
				if next.IsZero() || at.Before(next) {
					next = at
				}
				continue
			}
			if err := h.observe(h.ctx, v.ID, engine.SweepDue{Run: api.NewID()}, 0); err != nil {
				if h.ctx.Err() == nil && !isRefused(err) {
					slog.Warn("runtime: failed to trigger manager sweep", "thread", v.ID, "err", err)
				}
			}
		}
		if !next.IsZero() {
			timer.Reset(next.Sub(now))
		}
	}
}

func isRefused(err error) bool {
	return err != nil && (err == engine.ErrRefused || len(err.Error()) > 0 && containsRefused(err))
}

func containsRefused(err error) bool {
	for err != nil {
		if err == engine.ErrRefused {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return false
}
