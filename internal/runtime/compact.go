package runtime

import (
	"errors"
	"log/slog"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

var errCompactNotDue = errors.New("the compaction is no longer due")

// stillCompacting is whether b, a compaction's start, may still go out now.
func (h *Host) stillCompacting(t engine.Thread, b engine.StartTurn) bool {
	return h.idleCompact.Load() && engine.CompactDue(t, b.Run, h.clock())
}

// maxCompactions is how many idle compactions run at once: each resumes
// a CLI, and many due together, as at boot or after the Mac wakes, would
// start them all.
const maxCompactions = 2

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

// compactLoop owns every idle compaction: a timer per eligible thread, the
// threads whose timer fired, and the compactions running. It re-arms
// eligible threads on boot, reacts to commits without blocking OnCommit,
// and starts at most maxCompactions at once, the one whose window closes
// first first; one whose window closed while it waited is skipped.
func (h *Host) compactLoop() {
	defer h.wg.Done()
	timers := map[api.ID]*compactTimer{}
	due := map[api.ID]compactDue{}
	running := map[api.ID]api.ID{}
	stopOne := func(id api.ID) {
		if ct, ok := timers[id]; ok {
			ct.timer.Stop()
			delete(timers, id)
		}
		delete(due, id)
	}
	stopAll := func() {
		for id := range timers {
			stopOne(id)
		}
		clear(due)
	}
	defer stopAll()

	syncThread := func(id api.ID, force bool) {
		t, err := h.store.Thread(h.ctx, id)
		if err != nil {
			if h.ctx.Err() == nil {
				slog.Warn("runtime: idle compact load thread", "thread", id, "err", err)
			}
			return
		}
		if run, ok := running[id]; ok {
			if r := t.Live(); r != nil && r.ID == run {
				return
			}
			delete(running, id)
		}
		if !h.idleCompact.Load() {
			stopOne(id)
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
		if d, ok := due[id]; !force && ok && d.afterRun == afterRun {
			return
		}
		stopOne(id)
		d := compactDue{thread: id, afterRun: afterRun}
		// One due now waits with the others due, so the nearest goes first.
		if !now.Before(start) {
			due[id] = d
			return
		}
		timers[id] = &compactTimer{
			afterRun: afterRun,
			start:    start,
			timer: time.AfterFunc(max(0, start.Sub(now)), func() {
				select {
				case h.compactDue <- d:
				case <-h.ctx.Done():
				}
			}),
		}
	}

	dispatch := func() {
		for len(running) < maxCompactions && len(due) > 0 && h.idleCompact.Load() {
			var next compactDue
			var nextEnd time.Time
			now := h.clock()
			for id, d := range due {
				t, err := h.store.Thread(h.ctx, id)
				if err != nil {
					return
				}
				afterRun, _, end, ok := engine.IdleCompactWindow(t, now)
				if !ok || afterRun != d.afterRun {
					delete(due, id)
					syncThread(id, true)
					continue
				}
				if nextEnd.IsZero() || end.Before(nextEnd) {
					next, nextEnd = d, end
				}
			}
			if nextEnd.IsZero() {
				return
			}
			delete(due, next.thread)
			run := api.NewID()
			_, err := h.store.Observe(h.ctx, next.thread, engine.IdleCompact{Run: run, AfterRun: next.afterRun}, h.clock(), 0)
			switch {
			case err == nil:
				running[next.thread] = run
			case errors.Is(err, engine.ErrRefused):
				// Not yet due by the clock now, as after it stepped back:
				// the timer goes again.
				syncThread(next.thread, true)
			case h.ctx.Err() == nil:
				slog.Warn("runtime: idle compact observe", "thread", next.thread, "err", err)
			}
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
	dispatch()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-h.compactRescan:
			rescan()
		case id := <-h.compactCheck:
			syncThread(id, false)
		case d := <-h.compactDue:
			if cur := timers[d.thread]; cur != nil && cur.afterRun == d.afterRun {
				delete(timers, d.thread)
				due[d.thread] = d
			}
		}
		dispatch()
	}
}
