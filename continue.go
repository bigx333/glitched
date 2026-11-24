package main

import (
	"errors"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
)

// continuable is whether t's run was left uncertain, as an engine restart
// leaves it, with no message sent from here since: Continue, or a message
// of the user's, replaces that run. A second message would replace the run
// the first started.
func continuable(t thread) bool {
	return t.View.Run == api.RunUncertain && !slices.ContainsFunc(t.Pending, func(p client.Pending) bool {
		_, message := p.Body.(api.MessageDispatch)
		return message && p.Err == nil
	})
}

// continueRun has the agent of t's uncertain run pick up where it left off,
// in a run that replaces it: the engine refuses it once the thread moved
// on from the view this read, as when another client continued it first.
func (a *app) continueRun(t thread) api.ID {
	return a.backend.Replace(t.env, t.id(), api.ContinueText)
}

// continueButton continues a run left uncertain, beside the Stop that ends
// it, while the composer is empty: with a draft, Send replaces the run.
func (a *app) continueButton(c *ui.Context, t thread) {
	b := a.tip(c, ui.ButtonBase(c.Key("continue")).Label("Continue"),
		"Continue where the agent left off; it checks first that its last step didn't already take effect").
		Height(30).Padding(0, 12).Radius(15).Center().Cursor(ui.CursorPointer).Border(1, palette.border).Margin(0, 4, 0, 0)
	if b.Hovered() {
		b.Background(palette.hover)
	}
	if b.Children(func() { ui.Text(c, "Continue").FontSize(12.5) }).Clicked() {
		a.continueRun(t)
	}
}

// continuesAtOnce is how many of a Continue all's threads run at once on
// one engine, as a restart that cut off many agents would otherwise start
// them together.
const continuesAtOnce = 2

// continuing is a thread Continue all continues, once its turn comes, on
// the engine's log it was asked on: sent is the Continue's command, and
// the run it starts; replaces, the uncertain run it replaces.
type continuing struct {
	env            *client.Env
	thread, log    api.ID
	sent, replaces api.ID
}

// lostIn is the threads of the user's own a Continue all in cat's head
// would continue, which only the inbox has, on engines online: children
// are their parents' to pick up again.
func (a *app) lostIn(cat category) []thread {
	if cat != catInbox {
		return nil
	}
	var ts []thread
	for _, it := range a.itemsIn(catInbox) {
		if _, online := it.t.env.Status.(client.Online); online && it.t.View.Parent.IsZero() && continuable(it.t) && !a.continuingAlready(it.t) {
			ts = append(ts, it.t)
		}
	}
	return ts
}

func (a *app) continuingAlready(t thread) bool {
	return slices.ContainsFunc(a.continues, func(j continuing) bool { return j.env == t.env && j.thread == t.id() })
}

// continueAll continues ts, continuesAtOnce at a time, in order.
func (a *app) continueAll(ts []thread) {
	for _, t := range ts {
		if !a.continuingAlready(t) {
			a.continues = append(a.continues, continuing{env: t.env, thread: t.id(), log: t.env.Log()})
		}
	}
	a.paceContinues()
}

// paceContinues continues the next of a Continue all's threads on each
// engine while fewer than continuesAtOnce of those it continued there
// are live. One gone, or no longer uncertain before its turn, as the user
// continued or stopped it meanwhile, is dropped, as is one asked on a log
// the engine has since replaced: what it knew of that log's runs is moot.
// Its place is the engine's, whichever route reaches it: one on a route
// found to duplicate another moves to that one.
func (a *app) paceContinues() {
	envs := a.backend.Envs()
	for i := range a.continues {
		j := &a.continues[i]
		if _, dup := j.env.Status.(client.Duplicate); !dup {
			continue
		}
		if k := slices.IndexFunc(envs, func(e *client.Env) bool {
			_, dup := e.Status.(client.Duplicate)
			return e != j.env && e.ID == j.env.ID && !dup && e.Log() == j.log
		}); k >= 0 {
			j.env = envs[k]
		}
	}
	busy := map[api.ID]int{}
	a.continues = slices.DeleteFunc(a.continues, func(j continuing) bool {
		if !slices.Contains(envs, j.env) {
			return true
		}
		// One whose engine shows through another route, or none yet, waits
		// for that route's snapshot: its place is the engine's still.
		log := j.env.Log()
		if _, dup := j.env.Status.(client.Duplicate); dup || log.IsZero() {
			if !j.sent.IsZero() {
				busy[j.env.ID]++
			}
			return false
		}
		th := j.env.Threads[j.thread]
		if th == nil || log != j.log {
			return true
		}
		t := thread{j.env, th}
		if j.sent.IsZero() {
			return !continuable(t)
		}
		if j.live(t) {
			busy[j.env.ID]++
			return false
		}
		return true
	})
	for i := range a.continues {
		// One on an engine offline waits for it to come back.
		_, online := a.continues[i].env.Status.(client.Online)
		if j := &a.continues[i]; online && j.sent.IsZero() && busy[j.env.ID] < continuesAtOnce {
			t := thread{j.env, j.env.Threads[j.thread]}
			j.sent, j.replaces = a.continueRun(t), t.View.Turn.Run
			busy[j.env.ID]++
		}
	}
}

// live is whether the run j's Continue started is live, waiting on the
// user's answer too, as an answer resumes it without asking for a place;
// or whether its Continue may be: the thread still shows the uncertain run
// it replaces, and the engine hasn't refused the Continue: one on its way,
// or one that failed short of the engine's answer, as a reply lost on the
// way back, may have started it. A Continue whose run shows started it,
// whatever became of its reply.
func (j continuing) live(t thread) bool {
	v := t.View
	switch {
	case v.Turn != nil && v.Turn.Run == j.sent:
		return v.Run != api.RunUncertain
	case v.Run != api.RunUncertain || v.Turn == nil || v.Turn.Run != j.replaces:
		return false
	}
	return !slices.ContainsFunc(t.Pending, func(p client.Pending) bool {
		_, refused := errors.AsType[*api.RejectedError](p.Err)
		return p.Command == j.sent && refused
	})
}

// limited is whether a usage limit ended t's last turn, and nothing moved
// the thread on since: no run is live and no message was sent from here.
func limited(t thread) bool {
	l := t.View.LastTurn
	return l != nil && l.Limit != nil && !t.View.Live() && !slices.ContainsFunc(t.Pending, func(p client.Pending) bool {
		_, message := p.Body.(api.MessageDispatch)
		return message && p.Err == nil
	})
}

// resumesAt is when the engine continues t, a thread limited, because its
// usage limit resets, and whether it will: only this machine's engine says
// whether it was asked to, once its status is read.
func (a *app) resumesAt(t thread, now time.Time) (time.Time, bool) {
	if t.env.Kind != client.Local {
		return time.Time{}, false
	}
	m := a.backend.Machine()
	if m.Read.IsZero() {
		a.backend.Inspect()
	}
	if !m.Config.ContinueAfterLimit {
		return time.Time{}, false
	}
	return t.View.LimitResume(now)
}

// limitControl is, beside Send, when the engine continues a thread a usage
// limit stopped, with the Cancel that stops it; or, where it won't, the
// Continue that sends the agent on now.
func (a *app) limitControl(c *ui.Context, t thread, now time.Time) {
	at, ok := a.resumesAt(t, now)
	if !ok {
		tip := "The usage limit stopped the agent; continue where it left off"
		if r := t.View.LastTurn.Limit.Resets; !r.IsZero() {
			tip = "The usage limit stopped the agent until " + snoozeLabel(r.Local(), now) + "; continue where it left off"
		}
		b := a.tip(c, ui.ButtonBase(c.Key("limit-continue")).Label("Continue"), tip).
			Height(30).Padding(0, 12).Radius(15).Center().Cursor(ui.CursorPointer).Border(1, palette.border).Margin(0, 4, 0, 0)
		if b.Hovered() {
			b.Background(palette.hover)
		}
		if b.Children(func() { ui.Text(c, "Continue").FontSize(12.5) }).Clicked() {
			a.backend.Send(t.env, t.id(), api.LimitContinueText, api.StartNow)
		}
		return
	}
	label := "Continues at " + snoozeLabel(at.Local(), now)
	if !at.After(now) {
		label = "Continuing"
	}
	ui.Row(c).Key("limit-resume").Height(30).Padding(0, 4, 0, 12).Gap(8).Radius(15).AlignItems(ui.Center).Border(1, palette.border).Margin(0, 4, 0, 0).Shrink(0).Children(func() {
		ui.Icon(c, icClock).FontSize(12).TextColor(palette.muted)
		ui.Text(c, label).FontSize(12).TextColor(palette.muted)
		x := a.tip(c, ui.ButtonBase(c.Key("limit-cancel")).Label("Cancel"), "Don't continue once the usage limit resets").
			Height(22).Padding(0, 8).Radius(11).Center().Cursor(ui.CursorPointer)
		if x.Hovered() {
			x.Background(palette.hover)
		}
		if x.Children(func() { ui.Text(c, "Cancel").FontSize(12) }).Clicked() {
			a.backend.CancelResume(t.env, t.id())
		}
	})
}
