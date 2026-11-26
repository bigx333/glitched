package runtime

import (
	"errors"
	"log/slog"
	"slices"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// movesSweeps is whether a commit of threads can move when a sweep is
// due: only a Manager's change can.
func movesSweeps(threads []api.Thread) bool {
	return slices.ContainsFunc(threads, func(t api.Thread) bool { return t.Role == api.RoleManager })
}

func (h *Host) pokeSweeps() {
	select {
	case h.sweeps <- struct{}{}:
	default:
	}
}

// sweepDueAt is when v's next scheduled sweep is due, or ok false when it
// has none: not a scheduled Manager, settled, or busy. A snoozed one's
// waits for the snooze to run out, and one waiting to continue a turn a
// usage limit ended waits for that continue, or for it to be overdue.
func sweepDueAt(v api.Thread, now time.Time) (time.Time, bool) {
	if v.Role != api.RoleManager || v.Sweep == nil || v.Sweep.IntervalSeconds <= 0 ||
		v.Deleted || v.Remote != nil || v.Origin != nil || v.Disposition.State == api.Settled {
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
	due := base.Add(time.Duration(v.Sweep.IntervalSeconds) * time.Second)
	if v.Disposition.State == api.Snoozed && v.Disposition.Until.After(due) {
		due = v.Disposition.Until
	}
	if resets, ok := v.LimitResume(now); ok {
		// LimitResume holds through LimitOverdue past the reset.
		if after := resets.Add(api.LimitOverdue + time.Nanosecond); after.After(due) {
			due = after
		}
	}
	return due, true
}

// runSweeps triggers periodic scheduled sweeps on Manager threads when due.
// It reads only the scheduled Managers, when one changes or its timer
// fires.
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
		managers, err := h.store.SweepManagers(h.ctx)
		if err != nil {
			if h.ctx.Err() == nil {
				slog.Warn("runtime: couldn't read threads for manager sweeps", "err", err)
			}
			continue
		}
		now := h.clock()
		var next time.Time
		for _, v := range managers {
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
				if h.ctx.Err() == nil && !errors.Is(err, engine.ErrRefused) {
					slog.Warn("runtime: failed to trigger manager sweep", "thread", v.ID, "err", err)
				}
			}
		}
		if !next.IsZero() {
			timer.Reset(next.Sub(now))
		}
	}
}
