package api

import "time"

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

// WakeKind is why the engine, not the user, started a run: a child's
// report or a Manager's scheduled sweep.
type WakeKind uint8

const (
	NotWake WakeKind = iota
	WakeReport
	WakeSweep
)

// WakeDismiss quiets a Manager's live wake, one a worker's report or a
// scheduled sweep started, that needs no user attention: the thread
// keeps the place and unread mark it had before the wake.
type WakeDismiss struct {
	Run    ID     `json:"run,omitzero"`
	Reason string `json:"reason,omitempty"`
}

// WakeDismissed records that Run was dismissed. UpdatedAt is the thread's
// before the wake, which it goes back to. With Reports, the reports the
// wake took are dismissed with it, and the thread's Reported goes back
// to Reported, its last alert only the user can act on.
type WakeDismissed struct {
	Run       ID        `json:"run"`
	Reason    string    `json:"reason,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	Reports   bool      `json:"reports,omitempty"`
	Reported  time.Time `json:"reported,omitzero"`
}

func (WakeDismissed) eventType() string { return "wake.dismissed" }

// WakeRaised is a wake the user is to see after all: it asked them
// something, or they sent it a message. It is no longer dismissed, and
// no longer a wake its Manager may dismiss.
type WakeRaised struct {
	Run ID `json:"run"`
}

func (WakeRaised) eventType() string { return "wake.raised" }

// SweepStarted records when a scheduled Manager sweep was admitted.
type SweepStarted struct {
	Run ID `json:"run"`
}

func (SweepStarted) eventType() string { return "sweep.started" }
