package runtime

import (
	"log/slog"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

type compactDue struct {
	thread   api.ID
	afterRun api.ID
}

type compactTimer struct {
	afterRun api.ID
	start    time.Time
	timer    *time.Timer
}

// SetIdleCompaction toggles automatic idle compaction at runtime without
// restarting the host. Turning it off cancels all pending timers; turning
// it on scans eligible idle threads immediately.
func (h *Host) SetIdleCompaction(enabled bool) {
	if h.idleCompact.Swap(enabled) == enabled {
		return
	}
	h.requestCompactRescan()
}

func (h *Host) requestCompactRescan() {
	select {
	case h.compactRescan <- struct{}{}:
	default:
	}
}

func (h *Host) noteCompact(thread api.ID) {
	select {
	case h.compactCheck <- thread:
	default:
		// Channel full: fall back to a full candidate scan so no thread is lost.
		h.requestCompactRescan()
	}
}

// compactLoop owns all per-thread idle compaction timers. It re-arms
// eligible threads on boot, reacts to commits without blocking OnCommit,
// and stops every timer cleanly when the host closes.
func (h *Host) compactLoop() {
	defer h.wg.Done()
	timers := map[api.ID]*compactTimer{}
	stopOne := func(id api.ID) {
		if ct, ok := timers[id]; ok {
			ct.timer.Stop()
			delete(timers, id)
		}
	}
	stopAll := func() {
		for id, ct := range timers {
			ct.timer.Stop()
			delete(timers, id)
		}
	}
	defer stopAll()

	syncThread := func(id api.ID, force bool) {
		if !h.idleCompact.Load() {
			stopOne(id)
			return
		}
		t, err := h.store.Thread(h.ctx, id)
		if err != nil {
			if h.ctx.Err() == nil {
				slog.Warn("runtime: idle compact load thread", "thread", id, "err", err)
			}
			return
		}
		now := h.clock()
		afterRun, start, _, ok := engine.IdleCompactWindow(t, now)
		if !ok {
			stopOne(id)
			return
		}
		if cur := timers[id]; !force && cur != nil && cur.afterRun == afterRun && cur.start.Equal(start) {
			return
		}
		stopOne(id)
		delay := max(time.Duration(0), start.Sub(now))
		due := compactDue{thread: id, afterRun: afterRun}
		timers[id] = &compactTimer{
			afterRun: afterRun,
			start:    start,
			timer: time.AfterFunc(delay, func() {
				select {
				case h.compactDue <- due:
				case <-h.ctx.Done():
				}
			}),
		}
	}

	rescan := func() {
		if !h.idleCompact.Load() {
			stopAll()
			return
		}
		ids, err := h.store.IdleCompactCandidates(h.ctx, engine.MinIdleCompactTokens)
		if err != nil {
			if h.ctx.Err() == nil {
				slog.Warn("runtime: idle compact scan", "err", err)
			}
			return
		}
		keep := make(map[api.ID]bool, len(ids))
		for _, id := range ids {
			keep[id] = true
			syncThread(id, true)
		}
		for id := range timers {
			if !keep[id] {
				stopOne(id)
			}
		}
	}

	rescan()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-h.compactRescan:
			rescan()
		case id := <-h.compactCheck:
			syncThread(id, false)
		case due := <-h.compactDue:
			cur := timers[due.thread]
			if cur == nil || cur.afterRun != due.afterRun {
				continue
			}
			delete(timers, due.thread)
			if !h.idleCompact.Load() {
				continue
			}
			if err := h.observe(h.ctx, due.thread, engine.IdleCompact{Run: api.NewID(), AfterRun: due.afterRun}, 0); err != nil && h.ctx.Err() == nil {
				slog.Warn("runtime: idle compact observe", "thread", due.thread, "err", err)
			}
		}
	}
}
