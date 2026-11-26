package runtime

import (
	"errors"
	"log/slog"
	"slices"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

func movesSweeps(threads []api.Thread) bool {
	return slices.ContainsFunc(threads, func(t api.Thread) bool { return t.Role == api.RoleManager })
}

func (h *Host) pokeSweeps() {
	select {
	case h.sweeps <- struct{}{}:
	default:
	}
}

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
		for _, t := range managers {
			at, ok := engine.NextSweepAt(t, now)
			if !ok {
				continue
			}
			if at.After(now) {
				if next.IsZero() || at.Before(next) {
					next = at
				}
				continue
			}
			if err := h.observe(h.ctx, t.ID, engine.SweepDue{Run: api.NewID()}, 0); err != nil {
				if h.ctx.Err() == nil && !errors.Is(err, engine.ErrRefused) {
					slog.Warn("runtime: failed to trigger manager sweep", "thread", t.ID, "err", err)
				}
			}
		}
		if !next.IsZero() {
			timer.Reset(next.Sub(now))
		}
	}
}
