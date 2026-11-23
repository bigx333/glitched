package engine

import (
	"cmp"
	"strings"
	"time"

	"glitched/internal/api"
)

const (
	// MinIdleCompactTokens is the minimum context occupancy required before
	// idle compaction runs, matching Claude Code's idleCompaction default.
	MinIdleCompactTokens = 200_000
	// ClaudeCompactLead is how far ahead of Claude's reported prompt cache
	// expiry idle compaction begins, leaving room to resume a parked or
	// exited process and run /compact while the cache is still warm.
	ClaudeCompactLead = 2 * time.Minute
	// AntigravityIdleDelay is how long an idle Antigravity Gemini thread
	// waits after its last turn before compacting.
	AntigravityIdleDelay = 15 * time.Minute
	// AntigravityGrace bounds how far past AntigravityIdleDelay a pending
	// compaction may still fire (e.g. after sleep/restart) before skipping.
	AntigravityGrace = 2 * time.Minute
)

// IsAntigravityGeminiModel reports whether model names a Gemini model on
// Antigravity. Non-Gemini models on Antigravity (such as Claude) are
// excluded from idle compaction.
func IsAntigravityGeminiModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gemini")
}

// IdleCompactWindow reports when t should automatically compact while idle,
// or ok=false when it is ineligible. The window is [start, end): compacting
// before start is premature, and compacting at or after end is skipped so a
// cold cache is not re-read for a thread the user may never reopen.
func IdleCompactWindow(t Thread, now time.Time) (afterRun api.ID, start, end time.Time, ok bool) {
	if !t.Exists() || t.Deleted || t.settled() || !t.Parent.IsZero() {
		return api.ID{}, time.Time{}, time.Time{}, false
	}
	if t.Disposition.State == api.Snoozed && now.Before(t.Disposition.Until) {
		return api.ID{}, time.Time{}, time.Time{}, false
	}
	if t.Live() != nil {
		return api.ID{}, time.Time{}, time.Time{}, false
	}
	for _, m := range t.Queue {
		if m.State == QueueReady || m.State == QueueHeld {
			return api.ID{}, time.Time{}, time.Time{}, false
		}
	}
	for _, r := range t.Requests {
		if r.State == api.RequestPending {
			return api.ID{}, time.Time{}, time.Time{}, false
		}
	}
	if len(t.Runs) == 0 {
		return api.ID{}, time.Time{}, time.Time{}, false
	}
	last := t.Runs[len(t.Runs)-1]
	if last.State != RunFinished || last.Outcome != api.OutcomeCompleted || last.Ended.IsZero() || last.Silent {
		return api.ID{}, time.Time{}, time.Time{}, false
	}
	if m, found := t.Message(last.ID); !found || strings.EqualFold(strings.TrimSpace(m.Text), "/compact") {
		return api.ID{}, time.Time{}, time.Time{}, false
	}
	if t.Context.Stale || t.Context.Used < MinIdleCompactTokens || t.Session == "" {
		return api.ID{}, time.Time{}, time.Time{}, false
	}
	switch t.Model.Provider {
	case "claude":
		if last.Model.Provider != "claude" || t.Context.CacheExpires.IsZero() {
			return api.ID{}, time.Time{}, time.Time{}, false
		}
		end = t.Context.CacheExpires
		start = end.Add(-ClaudeCompactLead)
	case "antigravity":
		if last.Model.Provider != "antigravity" {
			return api.ID{}, time.Time{}, time.Time{}, false
		}
		model := cmp.Or(t.Model.Model, t.Context.Model)
		if !IsAntigravityGeminiModel(model) {
			return api.ID{}, time.Time{}, time.Time{}, false
		}
		if last.Model.Model != "" && !IsAntigravityGeminiModel(last.Model.Model) {
			return api.ID{}, time.Time{}, time.Time{}, false
		}
		start = last.Ended.Add(AntigravityIdleDelay)
		end = start.Add(AntigravityGrace)
	default:
		return api.ID{}, time.Time{}, time.Time{}, false
	}
	if !now.Before(end) {
		return api.ID{}, time.Time{}, time.Time{}, false
	}
	return last.ID, start, end, true
}
