package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// pending is the approval the thread waits on, if any.
func (t *thread) pending() *approval {
	if t.status != statusApproval {
		return nil
	}
	for i := len(t.entries) - 1; i >= 0; i-- {
		if ap := t.entries[i].approval; ap != nil && ap.decision == undecided {
			return ap
		}
	}
	return nil
}

// pausedFor reports whether the entry at i is a work log the agent left
// for an approval it is still waiting on.
func (t *thread) pausedFor(i int) bool {
	return i+1 < len(t.entries) && t.entries[i+1].approval != nil && t.entries[i+1].approval.decision == undecided
}

// approvalCard is the agent's request, in the timeline: what it wants to
// run, where, and why, with the decision under it. Decided, it keeps the
// answer.
func (a *app) approvalCard(c *ui.Context, key string, t *thread, ap *approval) {
	ui.Column(c).Key(key).Radius(12).Border(1, palette.border).Background(palette.surface).Clip().
		Transition(riseIn).Children(func() {
		ui.Row(c).Padding(9, 14).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border).Children(func() {
			switch ap.decision {
			case undecided:
				statusMark(c, statusApproval)
				ui.Text(c, "Approval needed").FontSize(13).FontWeight(600)
			case allowed:
				ui.Icon(c, icCheck).FontSize(13).TextColor(palette.green)
				ui.Text(c, "Allowed").FontSize(13).FontWeight(600)
			case denied:
				ui.Icon(c, icX).FontSize(13).TextColor(palette.red)
				ui.Text(c, "Denied").FontSize(13).FontWeight(600)
			}
			ui.Spacer(c)
			if ap.decision == undecided {
				ui.Textf(c, "waiting %s", ap.waiting).FontSize(11.5).TextColor(palette.faint)
			}
		})
		ui.Column(c).Padding(12, 14, 12).Gap(8).Children(func() {
			ui.Row(c).Gap(5).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Text(c, "Claude wants to run a command in").FontSize(12.5).TextColor(palette.muted)
				ui.Text(c, ap.dir).FontSize(12).Font("monospace")
			})
			ui.Text(c, ap.command).Selectable().FontSize(12.5).Font("monospace").Padding(8, 10).Radius(6).
				Background(palette.bg).Border(1, palette.border)
			ui.Text(c, ap.reason).FontSize(12.5).TextColor(palette.muted).LineHeight(1.45)
		})
		if ap.decision != undecided {
			return
		}
		ui.Row(c).Key("decide").Padding(0, 14, 12).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
			if filled(c, "allow", palette.accent, ui.Hex("#ffffff")).Children(func() {
				ui.Text(c, "Allow once").FontSize(12.5).FontWeight(500)
				// A keycap on the button's own color.
				ui.Text(c, "⌘⏎").FontSize(10.5).TextColor(ui.Hex("#ffffff").Alpha(0.9)).Padding(1, 4).Radius(4).
					Background(ui.Hex("#ffffff").Alpha(0.2))
			}).Clicked() {
				a.decide(t, allowed)
			}
			if ghost(c, "always", false).Border(1, palette.border).Padding(5, 10).Children(func() {
				ui.Text(c, "Always allow").FontSize(12.5)
				ui.Text(c, ap.rule).FontSize(12).Font("monospace").TextColor(palette.text)
			}).Clicked() {
				c.AddToast(ui.Toast{Title: "Always allowed", Description: ap.rule + " runs without asking in this project"})
				a.decide(t, allowed)
			}
			if ghost(c, "deny", false).TextColor(palette.red).Padding(5, 10).Children(func() {
				ui.Text(c, "Deny").FontSize(12.5)
				kbd(c, "⇧⌘D")
			}).Clicked() {
				a.decide(t, denied)
			}
			ui.Spacer(c)
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icHand).FontSize(12).TextColor(palette.faint)
				ui.Text(c, a.access).FontSize(11.5).TextColor(palette.faint)
			})
		})
	})
}

// filled is a button with a face of its own color.
func filled(c *ui.Context, key any, bg, fg ui.Color) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Gap(6).AlignItems(ui.Center).Padding(5, 10).Radius(6).
		Background(bg).TextColor(fg).Cursor(ui.CursorPointer).Transition(hoverFade)
	if b.Hovered() {
		b.Background(bg.Alpha(0.85))
	}
	return b
}

// approvalKeys answer the pending approval from the keyboard while the
// composer holds nothing: ⌘⏎ allows it, as it would send a draft, and ⇧⌘D
// denies it.
func (a *app) approvalKeys(c *ui.Context, t *thread) {
	if t.pending() == nil || strings.TrimSpace(a.draft) != "" || len(a.attachments) > 0 {
		return
	}
	switch {
	case c.Shortcut(ui.Cmd, ui.KeyEnter):
		a.decide(t, allowed)
	case c.Shortcut(ui.Cmd|ui.Shift, ui.KeyD):
		a.decide(t, denied)
	}
}

// decide answers the thread's pending approval. Allowed, the agent goes
// on with its turn; denied, it replies and the turn ends.
func (a *app) decide(t *thread, d decision) {
	ap := t.pending()
	if ap == nil {
		return
	}
	ap.decision = d
	if d == denied {
		t.entries = append(t.entries, entry{kind: entryAssistant, text: refused})
		t.finish()
		return
	}
	t.status, t.age = statusWorking, "now"
	t.stop.Store(false)
	a.listFor(t).ScrollToEnd()
	a.play(t, approved)
}

// changed is every file the agent changed in the thread, in order.
func (t *thread) changed() []fileChange {
	var files []fileChange
	for _, e := range t.entries {
		files = append(files, e.files...)
	}
	return files
}
