package api

// ThreadRole identifies specialized coordinator behavior for a top-level thread.
type ThreadRole string

const (
	RoleDefault ThreadRole = ""
	RoleManager ThreadRole = "manager"
)

// SweepSchedule configures periodic sweeps on a Manager thread.
// IntervalSeconds == 0 disables sweeps.
type SweepSchedule struct {
	IntervalSeconds int64  `json:"intervalSeconds,omitempty"`
	Prompt          string `json:"prompt,omitempty"`
}

const DefaultSweepPrompt = "Scheduled sweep: review your active and recently finished workers, check NOTES.md and WORKERS.md for pending items, take any needed follow-up actions, or call dismiss_wake if everything is on track and needs no user attention."

// WakeDismiss marks the thread's live run as silent so a Manager wake
// (from a worker report or scheduled sweep) that needs no user attention
// does not bump UpdatedAt, show as unread, or pull the thread into Inbox.
type WakeDismiss struct {
	Run    ID     `json:"run,omitzero"`
	Reason string `json:"reason,omitempty"`
}

// WakeDismissed records that Run was dismissed as a silent wake.
type WakeDismissed struct {
	Run    ID     `json:"run"`
	Reason string `json:"reason,omitempty"`
}

func (WakeDismissed) eventType() string { return "wake.dismissed" }

// SweepStarted records when a scheduled Manager sweep was admitted.
type SweepStarted struct {
	Run ID `json:"run"`
}

func (SweepStarted) eventType() string { return "sweep.started" }
