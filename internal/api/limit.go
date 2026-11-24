package api

import "time"

// LimitOverdue is how long after its usage limit reset a thread is still
// continued: one past it is left for the user, as T3 Code leaves one.
const LimitOverdue = 24 * time.Hour

// LimitResume is when the engine, where the user asked it to, continues the
// thread because the usage limit that ended its last turn resets, and
// whether it does. It does for a thread of the user's own that waits on
// nothing else, isn't settled or snoozed, and whose provider said when the
// limit resets, after the turn ended and no more than LimitOverdue before
// now. A Continue or a message of the user's, or a Stop of the run it
// started, moves the thread on, and a cancel of the user's ends it.
func (t Thread) LimitResume(now time.Time) (time.Time, bool) {
	l := t.LastTurn
	switch {
	case l == nil || l.Limit == nil || l.ResumeCancelled:
	case !t.Parent.IsZero() || t.Live() || t.Queued > 0 || len(t.Requests) > 0:
	case t.Disposition.State != Open:
	case !l.Limit.Resets.After(l.Ended) || now.Sub(l.Limit.Resets) > LimitOverdue:
	default:
		return l.Limit.Resets, true
	}
	return time.Time{}, false
}
