package main

import (
	"cmp"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// pending is how a composer control's value stands for its thread against
// what the thread's provider runs, and what its tooltip says of it: ""
// while the value is in effect.
type pending struct {
	standing api.Standing
	note     string
}

func (p pending) on() bool { return p.standing != api.InEffect }

// short is whether the provider runs other than the value, or never took it.
func (p pending) short() bool { return p.standing == api.Short || p.standing == api.Unapplied }

// providerName is the provider's family's name, as the picker shows it.
func providerName(provider string) string {
	if f := familyOf(provider); f.name != "" {
		return f.name
	}
	return provider
}

// accessLevelName is level's name in the picker.
func accessLevelName(level api.Access) string {
	for name, v := range accessOf {
		if v == level {
			return name
		}
	}
	return accessAsk
}

// chosenView is t's view with the access, model and effort its composer
// shows: the engine's view lags a configure it hasn't taken yet. A
// composer that shows what the view has leaves it as it is, though it
// names it otherwise, as a model by its name for one by its id.
func (a *app) chosenView(t thread) api.Thread {
	v := t.View
	if l, ok := accessOf[a.access]; ok {
		v.Access = l
	}
	if a.cliDecides(t) {
		return v
	}
	if keyOfThread(v.Model.Provider, v.Model.Model) != a.model {
		v.Model.Model = a.model.ID
	}
	m, _ := a.modelOf(a.model)
	if e := effortFor(m, a.effort); e != effortFor(m, v.Model.Effort) {
		v.Model.Effort = e
	}
	return v
}

// accessPending is how the thread's access stands, as the composer shows
// it. A draft's is in effect: nothing runs it yet.
func (a *app) accessPending(t thread) pending {
	if t.id().IsZero() {
		return pending{}
	}
	v := a.chosenView(t)
	p := pending{standing: v.AccessStanding()}
	name := providerName(v.Model.Provider)
	switch p.standing {
	case api.NextTurn:
		p.note = "Applies on the next turn"
	case api.Held:
		p.note = "This turn is still running at " + accessLevelName(v.Turn.Access)
	case api.Applying:
		p.note = "Waiting for " + name + " to confirm it"
	case api.Unapplied:
		p.note = "Not applied: the last turn ended before " + name + " confirmed it"
	case api.Short:
		g := v.Confirmed.Access
		p.note = name + " runs " + shortAccessName(*g)
		if g.Mode != "" {
			p.note += " (" + g.Mode + ")"
		}
		if g.Why != "" {
			p.note += ": " + g.Why
		}
	}
	return p
}

// shortAccessName is the name of what a provider that runs other than the
// access asked runs: another level by its name, and Approve for me fallen
// back from the provider's own auto mode as what it then does.
func shortAccessName(g api.ConfirmedAccess) string {
	if g.Access == api.AccessAutoEdits && g.Asked == api.AccessAutoEdits && g.Why != "" {
		return "Approve edits only"
	}
	return accessLevelName(g.Access)
}

// accessShown is the level the composer's access control shows for t,
// for its icon and color, and what it says of it: the chosen level, or
// what the provider really runs when it runs other than it.
func (a *app) accessShown(t thread) (level, label string) {
	if a.accessPending(t).standing != api.Short {
		return a.access, a.access
	}
	g := *t.View.Confirmed.Access
	return accessLevelName(g.Access), shortAccessName(g)
}

// modelPending is how the thread's model and effort stand, the model's
// first: the button shows both.
func (a *app) modelPending(t thread) pending {
	if t.id().IsZero() {
		return pending{}
	}
	v := a.chosenView(t)
	name := providerName(v.Model.Provider)
	modelName := func(m api.ModelRef) string {
		info, _ := a.modelOf(keyOfThread(m.Provider, m.Model))
		if info.name == "" {
			return m.Model
		}
		return info.name
	}
	effortName := func(m api.ModelRef, effort string) string {
		info, _ := a.modelOf(keyOfThread(m.Provider, m.Model))
		for _, e := range effortsOf(info) {
			if e.id == effort {
				return e.label
			}
		}
		return effort
	}
	p := pending{standing: v.ModelStanding()}
	what := func(m api.ModelRef) string { return modelName(m) }
	if !p.on() {
		p.standing = v.EffortStanding()
		what = func(m api.ModelRef) string { return modelName(m) + " at " + effortName(m, m.Effort) }
	}
	switch p.standing {
	case api.NextTurn:
		p.note = "Applies on the next turn"
	case api.Held:
		p.note = "This turn is still running " + what(v.Turn.Model)
	case api.Applying:
		p.note = "Waiting for " + name + " to confirm it"
	case api.Unapplied:
		p.note = "Not applied: the last turn ended before " + name + " confirmed it"
	case api.Short:
		g := v.Confirmed.Model
		ran := api.ModelRef{Provider: v.Model.Provider, Model: g.Model, Effort: g.Effort}
		p.note = name + " runs " + what(ran)
		if g.Why != "" {
			p.note += ": " + g.Why
		}
	}
	return p
}

// tipOf is the tooltip of a control whose value stands as p: why it is
// pending or short, else the control's own.
func tipOf(p pending, own string) string {
	if p.on() {
		return p.note
	}
	return own
}

// unconfirmedMark marks the control it is drawn in as p has it:
// a value pending, or one its provider runs short of. Glitched draws it
// in the control's own box, so a plugin that draws the control's content
// can neither hide it nor show it of its own. The mark is a dot on the
// control's corner, which takes no room: muted while pending, breathing
// while the provider applies it, amber where the provider runs something
// else.
func (a *app) unconfirmedMark(c *ui.Context, p pending) {
	if !p.on() {
		return
	}
	color := palette.muted
	if p.short() {
		color = palette.amber
	}
	dot := ui.Box(c).Key("pending-dot").Absolute().Top(2).Right(2).Size(6, 6).Radius(3).PassThrough().Background(color)
	if p.standing == api.Applying {
		dot.Opacity(0.4 + 0.6*dot.Loop("applying", 1200*time.Millisecond, pingPong))
	}
}

// runningModel is the model's and effort's names the model button shows
// for t: the chosen ones, or what its provider really runs where it runs
// another.
func (a *app) runningModel(t thread, name, effort string) (string, string) {
	v := a.chosenView(t)
	if t.id().IsZero() || v.Confirmed.Model == nil {
		return name, effort
	}
	g := v.Confirmed.Model
	info, _ := a.modelOf(keyOfThread(v.Model.Provider, g.Model))
	if v.ModelStanding() == api.Short {
		name = cmp.Or(info.name, g.Model)
	}
	if v.EffortStanding() == api.Short {
		effort = g.Effort
		for _, e := range effortsOf(info) {
			if e.id == g.Effort {
				effort = e.label
			}
		}
	}
	return name, effort
}
