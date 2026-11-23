package main

import (
	"slices"

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
// in a run that replaces it.
func (a *app) continueRun(t thread) api.ID {
	return a.backend.Send(t.env, t.id(), api.ContinueText, api.RestartActive)
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

// continuesAtOnce is how many of a Continue all's threads run at once, as
// a restart that cut off many agents would otherwise start them together.
const continuesAtOnce = 2

// continuing is a thread Continue all continues, once its turn comes:
// sent is the Continue's command, and the run it starts.
type continuing struct {
	env    *client.Env
	thread api.ID
	sent   api.ID
}

// lostIn is the threads of the user's own a Continue all in cat's head
// would continue, which only the inbox has: children are their parents'
// to pick up again.
func (a *app) lostIn(cat category) []thread {
	if cat != catInbox {
		return nil
	}
	var ts []thread
	for _, it := range a.itemsIn(catInbox) {
		if it.t.View.Parent.IsZero() && continuable(it.t) && !a.continuingAlready(it.t) {
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
			a.continues = append(a.continues, continuing{env: t.env, thread: t.id()})
		}
	}
	a.paceContinues()
}

// paceContinues continues the next of a Continue all's threads while fewer
// than continuesAtOnce of those it continued are at work. One gone, or no
// longer uncertain before its turn, as the user continued or stopped it
// meanwhile, is dropped.
func (a *app) paceContinues() {
	busy := 0
	a.continues = slices.DeleteFunc(a.continues, func(j continuing) bool {
		th := j.env.Threads[j.thread]
		if th == nil || !slices.Contains(a.backend.Envs(), j.env) {
			return true
		}
		t := thread{j.env, th}
		if j.sent.IsZero() {
			return !continuable(t)
		}
		if j.works(t) {
			busy++
			return false
		}
		return true
	})
	for i := range a.continues {
		if busy >= continuesAtOnce {
			return
		}
		if j := &a.continues[i]; j.sent.IsZero() {
			j.sent = a.continueRun(thread{j.env, j.env.Threads[j.thread]})
			busy++
		}
	}
}

// works is whether the run j's Continue started still works, or its
// Continue is on its way: the uncertain run it replaces is still t's. A
// run waiting on the user's answer works no more than one that ended.
func (j continuing) works(t thread) bool {
	if slices.ContainsFunc(t.Pending, func(p client.Pending) bool { return p.Command == j.sent && p.Err != nil }) {
		return false
	}
	v := t.View
	if v.Turn != nil && v.Turn.Run == j.sent {
		return v.Run == api.RunStarting || v.Run == api.RunRunning
	}
	return v.Run == api.RunUncertain
}
