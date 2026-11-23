package api

// Confirmed is what a thread's provider last confirmed its agent runs at:
// its access, and its model and effort, each as of the run it confirmed
// them in. A part nil is one no run of the thread has confirmed.
type Confirmed struct {
	Access *ConfirmedAccess `json:"access,omitempty"`
	Model  *ConfirmedModel  `json:"model,omitempty"`
}

// ConfirmedAccess is the access a run's agent runs at, as its provider
// confirmed it. Asked is the run's own, the thread's as the run was
// admitted. Access is the level the provider runs it at, and Mode the
// provider's own name for it. Why says, in the provider's words, why it
// runs short of Asked: a level below it, or Asked's level in a mode that
// lets the agent do less, as when it falls back from its auto mode. It is
// "" when the provider runs what was asked.
type ConfirmedAccess struct {
	Run    ID     `json:"run"`
	Asked  Access `json:"asked"`
	Access Access `json:"access"`
	Mode   string `json:"mode,omitempty"`
	Why    string `json:"why,omitempty"`
}

// Short is whether the provider runs other than the run asked: a level
// below it or above it, or Asked's level in a mode that does less.
func (c ConfirmedAccess) Short() bool { return c.Why != "" || c.Access != c.Asked }

// ConfirmedModel is the model and effort a run's agent runs, as its
// provider confirmed them. Asked is the run's own. Model and Effort are
// what the provider runs, by the ids a ModelRef names them by; an Effort
// "" is one the provider didn't say. Why says why they aren't Asked's,
// "" when the provider runs what was asked.
type ConfirmedModel struct {
	Run    ID       `json:"run"`
	Asked  ModelRef `json:"asked"`
	Model  string   `json:"model"`
	Effort string   `json:"effort,omitempty"`
	Why    string   `json:"why,omitempty"`
}

// Standing is how what the user chose for a thread stands against what
// its provider runs.
type Standing uint8

const (
	// InEffect: the provider runs what was chosen, or nothing says
	// otherwise, as for a thread that never ran.
	InEffect Standing = iota
	// NextTurn: the thread's next turn takes it.
	NextTurn
	// Held: a turn still runs at what it was admitted at, Turn's.
	Held
	// Applying: the live turn asked for it, and its provider hasn't
	// confirmed it yet.
	Applying
	// Short: the provider runs something short of it, and says why.
	Short
	// Unapplied: the last turn asked for it and ended before its provider
	// confirmed it.
	Unapplied
)

// confirmation is one part of Confirmed as standing reads it: the run it
// came from, what that run asked, and whether the provider ran short.
type confirmation[T comparable] struct {
	run   ID
	asked T
	short bool
}

// standing is how chosen stands, given what the live run asked, if one is
// live, what the last run to end asked, if one has, and whether it failed,
// and the provider's last confirmation, if any. A run unconfirmed leaves
// the last confirmation standing while it asked the same, so that a turn
// of an unchanged thread shows no change; one that failed with nothing
// confirmed before it took nothing.
func standing[T comparable](chosen T, live, last *T, liveRun, lastRun ID, lastFailed bool, c *confirmation[T]) Standing {
	if live != nil && *live != chosen {
		return Held
	}
	run, asked := liveRun, live
	if live == nil {
		run, asked = lastRun, last
	}
	if asked == nil {
		return InEffect
	}
	if c != nil && c.run == run {
		switch {
		case c.asked != chosen:
			return NextTurn
		case c.short:
			return Short
		}
		return InEffect
	}
	if live == nil && *asked != chosen {
		return NextTurn
	}
	// The latest run asked for what was chosen, and its provider hasn't
	// confirmed it: what was confirmed before stands if it was the same.
	before := c
	if before == nil && live != nil && last != nil {
		before = &confirmation[T]{asked: *last}
	}
	switch {
	case before == nil && live == nil && lastFailed:
		return Unapplied
	case before == nil:
		return InEffect
	case before.asked == chosen && before.short:
		return Short
	case before.asked == chosen:
		return InEffect
	case live != nil:
		return Applying
	}
	return Unapplied
}

// AccessStanding is how the thread's Access stands against the access its
// provider runs.
func (t Thread) AccessStanding() Standing {
	var live, last *Access
	var liveRun, lastRun ID
	if t.Turn != nil {
		live, liveRun = &t.Turn.Access, t.Turn.Run
	}
	if t.LastTurn != nil && !t.LastTurn.Run.IsZero() {
		last, lastRun = &t.LastTurn.Access, t.LastTurn.Run
	}
	var c *confirmation[Access]
	if g := t.Confirmed.Access; g != nil {
		c = &confirmation[Access]{run: g.Run, asked: g.Asked, short: g.Short()}
	}
	return standing(t.Access, live, last, liveRun, lastRun, t.lastFailed(), c)
}

// ModelStanding is how the thread's model stands against the model its
// provider runs, and EffortStanding its effort.
func (t Thread) ModelStanding() Standing {
	return t.modelPart(func(m ModelRef) string { return m.Model }, func(c ConfirmedModel) bool {
		return c.Asked.Model != "" && c.Model != c.Asked.Model
	})
}

func (t Thread) EffortStanding() Standing {
	return t.modelPart(func(m ModelRef) string { return m.Effort }, func(c ConfirmedModel) bool {
		return c.Asked.Effort != "" && c.Effort != "" && c.Effort != c.Asked.Effort
	})
}

func (t Thread) modelPart(of func(ModelRef) string, short func(ConfirmedModel) bool) Standing {
	var live, last *string
	var liveRun, lastRun ID
	if t.Turn != nil {
		live, liveRun = new(of(t.Turn.Model)), t.Turn.Run
	}
	if t.LastTurn != nil && !t.LastTurn.Run.IsZero() {
		last, lastRun = new(of(t.LastTurn.Model)), t.LastTurn.Run
	}
	var c *confirmation[string]
	if g := t.Confirmed.Model; g != nil {
		c = &confirmation[string]{run: g.Run, asked: of(g.Asked), short: short(*g)}
	}
	return standing(of(t.Model), live, last, liveRun, lastRun, t.lastFailed(), c)
}

func (t Thread) lastFailed() bool { return t.LastTurn != nil && t.LastTurn.Outcome == OutcomeFailed }
