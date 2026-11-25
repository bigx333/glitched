package engine

import (
	"cmp"
	"strings"
	"time"

	"glitched/internal/api"
)

const (
	// MinIdleCompactTokens is the context an Antigravity thread holds
	// before idle compaction is worth it. A Claude thread's own threshold
	// comes from its CLI's settings, and its provider reports a cache
	// expiry only for a thread above it (see provider.Context).
	MinIdleCompactTokens = 200_000
	// ClaudeCacheTTL is the only prompt cache Claude threads compact
	// before: Claude Code's own idle compaction refuses a 5-minute cache.
	ClaudeCacheTTL = time.Hour
	// AntigravityCacheTTL is how long Antigravity keeps a session's
	// cache after its last turn ends.
	AntigravityCacheTTL = 15 * time.Minute
	// CompactLapse is how late a compaction may start after it fell due.
	// A later one, as after the Mac slept, could find the cache gone and
	// pay to read the whole context cold, so it is skipped.
	CompactLapse = time.Minute
)

// IsAntigravityGeminiModel reports whether model names a Gemini model on
// Antigravity. Non-Gemini models on Antigravity (such as Claude) are
// excluded from idle compaction.
func IsAntigravityGeminiModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gemini")
}

// IdleCompactWindow reports when t should compact while idle, after its
// last run, or ok=false when it is ineligible. The window is [start, end):
// it opens at 90% of the cache's lifetime, as Claude Code's own idle
// compaction does, and closes CompactLapse later, before the cache lapses.
func IdleCompactWindow(t Thread, now time.Time) (afterRun api.ID, start, end time.Time, ok bool) {
	if t.Live() != nil || len(t.Runs) == 0 || t.waiting() {
		return api.ID{}, time.Time{}, time.Time{}, false
	}
	last := t.Runs[len(t.Runs)-1]
	start, end, ok = compactWindow(t, last, now)
	return last.ID, start, end, ok
}

// CompactDue is whether the silent run that is t's live one may still
// send its /compact at now: it is still starting, nothing the user did
// since its admission changed the thread, and the window after the run
// before it is still open.
func CompactDue(t Thread, run api.ID, now time.Time) bool {
	r := t.Live()
	i := t.runAt(run)
	if r == nil || r.ID != run || !r.Silent || r.State != RunStarting || i < 1 {
		return false
	}
	if r.Model != t.Model || r.Access != t.Access || t.waiting() {
		return false
	}
	start, end, ok := compactWindow(t, t.Runs[i-1], now)
	return ok && !now.Before(start) && now.Before(end)
}

// waiting is whether a message of the user's waits its turn or a request
// waits on them: either outranks a compaction.
func (t Thread) waiting() bool {
	for _, m := range t.Queue {
		if m.State == QueueReady || m.State == QueueHeld {
			return true
		}
	}
	for _, r := range t.Requests {
		if r.State == api.RequestPending {
			return true
		}
	}
	return false
}

// compactWindow is the window after last, a finished run of t's.
func compactWindow(t Thread, last Run, now time.Time) (start, end time.Time, ok bool) {
	if !t.Exists() || t.Deleted || t.settled() || t.child() || t.Context.Stale {
		return time.Time{}, time.Time{}, false
	}
	if last.State != RunFinished || last.Outcome != api.OutcomeCompleted || last.Ended.IsZero() || last.Silent {
		return time.Time{}, time.Time{}, false
	}
	if m, found := t.Message(last.ID); !found || strings.EqualFold(strings.TrimSpace(m.Text), "/compact") {
		return time.Time{}, time.Time{}, false
	}
	var expires time.Time
	var ttl time.Duration
	switch t.Model.Provider {
	case "claude":
		if last.Model.Provider != "claude" || t.Context.CacheExpires.IsZero() {
			return time.Time{}, time.Time{}, false
		}
		expires, ttl = t.Context.CacheExpires, ClaudeCacheTTL
	case "antigravity":
		if last.Model.Provider != "antigravity" || t.Session == "" || t.Context.Used < MinIdleCompactTokens {
			return time.Time{}, time.Time{}, false
		}
		var confirmed string
		if c := t.Confirmed.Model; c != nil {
			confirmed = c.Model
		}
		if !IsAntigravityGeminiModel(cmp.Or(t.Model.Model, confirmed, t.Context.Model)) {
			return time.Time{}, time.Time{}, false
		}
		if m := cmp.Or(last.Model.Model, confirmed); m != "" && !IsAntigravityGeminiModel(m) {
			return time.Time{}, time.Time{}, false
		}
		expires, ttl = last.Ended.Add(AntigravityCacheTTL), AntigravityCacheTTL
	default:
		return time.Time{}, time.Time{}, false
	}
	start = expires.Add(-ttl / 10)
	end = start.Add(CompactLapse)
	if t.Disposition.State == api.Snoozed && t.Disposition.Until.After(start) {
		start = t.Disposition.Until
	}
	if !start.Before(end) || !now.Before(end) {
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}
