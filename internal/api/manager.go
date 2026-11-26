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

const DefaultSweepPrompt = "Scheduled sweep: check your active and recently finished workers. Verify each finished worker's branch, diff and checks against its brief. Steer or cancel stalled or off-scope work with task_send or task_cancel, and answer routine worker questions with task_answer. Update WORKERS.md and NOTES.md. Then call dismiss_wake unless the user must act: a decision, secret or legitimate approval only the user can give, an unresolved blocker or failure, or a verified deliverable ready for review."

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

// SweepRun starts a sweep on an idle Manager thread now, whether or not
// its sweeps are scheduled. A non-empty Prompt takes the place of the
// configured one for this run.
type SweepRun struct {
	Prompt string `json:"prompt,omitempty"`
}

// SweepStarted records when a Manager sweep, scheduled or run by hand,
// was admitted.
type SweepStarted struct {
	Run ID `json:"run"`
}

func (SweepStarted) eventType() string { return "sweep.started" }
