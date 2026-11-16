package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plain"
)

// questionForm is what the user picked on a question card that waits:
// per question, the labels picked and the Other text.
type questionForm struct {
	picked [][]string
	other  []string
	fields []ui.Handle // the Other inputs, whose focus keeps Escape theirs
	at     int         // the question ⌘1 to ⌘4 pick in
}

// form is the picks on the card of r, kept by request until it's answered.
func (a *app) form(r *client.RequestEntry, q api.Questions) *questionForm {
	f := a.questions[r.Request]
	if f == nil {
		n := len(q.Questions)
		f = &questionForm{picked: make([][]string, n), other: make([]string, n), fields: make([]ui.Handle, n)}
		a.questions[r.Request] = f
	}
	return f
}

// pick picks the option of question i labelled l: in a Multi question it
// toggles; in another it is the one answer, and the keys move to the next
// question.
func (f *questionForm) pick(q api.Questions, i int, l string) {
	f.at = i
	if q.Questions[i].Multi {
		if j := slices.Index(f.picked[i], l); j >= 0 {
			f.picked[i] = slices.Delete(f.picked[i], j, j+1)
		} else {
			f.picked[i] = append(f.picked[i], l)
		}
		return
	}
	f.picked[i], f.other[i] = []string{l}, ""
	f.at = min(i+1, len(q.Questions)-1)
}

// answers is the form as the engine takes it: one Answer per question.
func (f *questionForm) answers() api.Answers {
	var as api.Answers
	for i := range f.picked {
		as.Answers = append(as.Answers, api.Answer{Picked: f.picked[i], Other: strings.TrimSpace(f.other[i])})
	}
	return as
}

// done is whether each question has a pick or Other text.
func (f *questionForm) done() bool {
	for i := range f.picked {
		if len(f.picked[i]) == 0 && strings.TrimSpace(f.other[i]) == "" {
			return false
		}
	}
	return true
}

// lone is whether q is one question of one answer, which a pick answers.
func lone(q api.Questions) bool { return len(q.Questions) == 1 && !q.Questions[0].Multi }

// questionCard is a question request in the timeline: its questions, with
// their options and an Other field, while it waits; what was answered once
// it is; or that it is no longer asked. A body no card can show is shown as
// it came, with Dismiss only, as the engine takes nothing else for it.
// The agent's text shows as printable ASCII, as an approval's does. Its
// keys show while they are the card's, as keyedRequest says.
func (a *app) questionCard(c *ui.Context, t thread, e entry) {
	r := e.request
	q, _ := api.ParseQuestions(r.Body)
	waiting := r.State == api.RequestPending
	sent, sending := responding(t, r.Request)
	escaped := false
	shown := func(s string, lines bool) string {
		s, e := plain.Text(s, lines)
		escaped = escaped || e
		return s
	}
	keyed := a.keyed == r.Request
	card := ui.Column(c).Key(e.key).Radius(12).Border(1, palette.border).Background(palette.surface).Clip().
		Transition(riseIn)
	card.Children(func() {
		header := ui.Row(c).Padding(9, 14).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border)
		header.Children(func() {
			answer, _ := api.ParseAnswers(r.Answer)
			switch {
			case waiting || r.State == api.RequestAnswered:
				a.pendingMark(c)
				ui.Text(c, agentName(t)+" asks").FontSize(13).FontWeight(600)
			case r.State == api.RequestDelivered && len(answer.Answers) == 0:
				ui.Icon(c, icX).FontSize(13).TextColor(palette.muted)
				ui.Text(c, "Dismissed").FontSize(13).FontWeight(600).TextColor(palette.muted)
			case r.State == api.RequestDelivered:
				ui.Icon(c, icCheck).FontSize(13).TextColor(palette.green)
				ui.Text(c, "Answered").FontSize(13).FontWeight(600)
			default:
				ui.Icon(c, icArrowRight).FontSize(13).TextColor(palette.muted)
				ui.Text(c, "No longer asked").FontSize(13).FontWeight(600).TextColor(palette.muted)
			}
			ui.Spacer(c)
			if waiting && !r.Opened.IsZero() {
				ui.Text(c, "waiting "+ago(r.Opened, a.now)).FontSize(11.5).TextColor(palette.faint)
			}
		})
		var lead ui.Rect
		switch {
		case len(q.Questions) == 0:
			ui.Column(c).Padding(12, 14, 12).Gap(8).Children(func() {
				lead = ui.Text(c, agentName(t)+" asks:").FontSize(12.5).TextColor(palette.muted).Bounds()
				ui.Text(c, shown(string(r.Body), false)).Selectable().FontSize(12).Font("monospace")
			})
		case waiting:
			lead = a.questionForm(c, t, r, q, keyed, !sending, shown)
		default:
			questionSummary(c, r, q, shown)
		}
		if waiting {
			a.drawn[r.Request] = sight{header: header.Bounds(), card: card.Bounds(), lead: lead, texts: card.Bounds()}
			a.bindWaiting(r.Request, card)
		}
		if escaped {
			ui.Box(c).Key("ascii").Padding(0, 14, 12).Children(func() { asciiNotice(c) })
		}
		if !waiting {
			return
		}
		ui.Row(c).Key("decide").Padding(0, 14, 12).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
			switch {
			case sending && sent.Err != nil:
				ui.Text(c, "Not sent: "+sent.Err.Error()).FontSize(12).TextColor(palette.red).Grow(1).Shrink(1)
				a.failedActions(c, t, sent)
			case sending:
				ui.Text(c, "Sending…").FontSize(12).TextColor(palette.muted)
			default:
				a.questionButtons(c, t, r, q, keyed)
			}
		})
	})
}

// questionForm lays out the questions of a waiting card, which take the
// user's picks while open, the agent's text as shown shows it, with ⌘1 to
// ⌘4 on the options the keys pick while keyed. It returns where the last
// frame drew the first question's line.
func (a *app) questionForm(c *ui.Context, t thread, r *client.RequestEntry, q api.Questions, keyed, open bool, shown func(string, bool) string) (lead ui.Rect) {
	f := a.form(r, q)
	ui.Column(c).Padding(12, 14, 12).Gap(14).Children(func() {
		for i, x := range q.Questions {
			ui.Column(c).Key(i).Gap(4).Children(func() {
				line := ui.Row(c).Gap(7).AlignItems(ui.Center).Wrap().Padding(0, 0, 3)
				if i == 0 {
					lead = line.Bounds()
				}
				line.Children(func() {
					if x.Header != "" {
						ui.Text(c, shown(x.Header, false)).FontSize(11).FontWeight(600).TextColor(palette.accent).
							Padding(1, 6).Radius(4).Background(palette.accent.Alpha(0.14))
					}
					ui.Text(c, shown(x.Text, true)).FontSize(13).Shrink(1)
					if x.Multi {
						ui.Text(c, "pick any").FontSize(11.5).TextColor(palette.faint)
					}
				})
				for j, o := range x.Options {
					on := slices.Contains(f.picked[i], o.Label)
					b := ghost(c, j, on).Padding(5, 8).Disabled(!open).Children(func() {
						ui.Text(c, optionMark(x.Multi, on)).FontSize(13).TextColor(palette.accent).Width(14)
						ui.Text(c, shown(o.Label, false)).FontSize(12.5).TextColor(palette.text)
						if o.About != "" {
							ui.Text(c, shown(o.About, true)).FontSize(11.5).TextColor(palette.faint).Shrink(1)
						}
						ui.Spacer(c)
						if keyed && i == f.at && j < len(pickKeys) {
							kbd(c, fmt.Sprintf("⌘%d", j+1))
						}
					})
					if b.Clicked() {
						a.pickOption(t, r, q, f, i, o.Label)
					}
				}
				ui.Row(c).Key("other").Gap(6).AlignItems(ui.Center).Padding(0, 8).Children(func() {
					on := strings.TrimSpace(f.other[i]) != ""
					ui.Text(c, optionMark(x.Multi, on)).FontSize(13).TextColor(palette.accent).Width(14)
					before := f.other[i]
					ui.TextInput(c.Key("other"), &f.other[i]).Bind(&f.fields[i]).Placeholder("Other… type your own answer").
						Label("Other answer to " + shown(cmp.Or(x.Header, x.Text), false)).
						FontSize(12.5).Grow(1).Disabled(!open)
					if f.other[i] != before {
						f.at = i
						if !x.Multi && strings.TrimSpace(f.other[i]) != "" {
							f.picked[i] = nil // Other is the one answer
						}
					}
				})
			})
		}
	})
	return lead
}

func optionMark(multi, on bool) string {
	switch {
	case multi && on:
		return "☑"
	case multi:
		return "☐"
	case on:
		return "◉"
	}
	return "○"
}

// questionButtons are a waiting card's Answer, once each question has an
// answer, and Dismiss; Dismiss only, for questions the card can't show.
// Their keys show while keyed.
func (a *app) questionButtons(c *ui.Context, t thread, r *client.RequestEntry, q api.Questions, keyed bool) {
	f := a.form(r, q)
	var answer ui.Element
	if len(q.Questions) > 0 {
		answer = filled(c, "answer", palette.accent, ui.Hex("#ffffff")).Disabled(!f.done()).Children(func() {
			ui.Text(c, "Answer").FontSize(12.5).FontWeight(500)
			if keyed {
				ui.Text(c, "⌘⏎").FontSize(10.5).Padding(1, 4).Radius(4).Background(ui.Hex("#ffffff").Alpha(0.2))
			}
		})
		if !f.done() {
			answer.Opacity(0.5)
		}
	}
	dismiss := ghost(c, "dismiss", false).Padding(5, 10).Children(func() {
		ui.Text(c, "Dismiss").FontSize(12.5)
		if keyed {
			kbd(c, "⇧⌘D")
		}
	})
	ui.Spacer(c)
	ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icHand).FontSize(12).TextColor(palette.faint)
		ui.Text(c, accessLabel(t.View.Access)).FontSize(11.5).TextColor(palette.faint)
	})
	if answer.Clicked() && f.done() {
		a.answerQuestions(t, r, f.answers())
	}
	if dismiss.Clicked() {
		a.answerQuestions(t, r, api.Answers{})
	}
}

// questionSummary is what an answered card says: each question's header,
// or its text, and the answer it got; or, voided, why it's no longer asked.
// The agent's text, and the answer, show as shown shows them.
func questionSummary(c *ui.Context, r *client.RequestEntry, q api.Questions, shown func(string, bool) string) {
	answer, _ := api.ParseAnswers(r.Answer)
	ui.Column(c).Padding(10, 14, 12).Gap(6).Children(func() {
		for i, x := range q.Questions {
			ui.Row(c).Key(i).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
				if r.State != api.RequestAnswered || x.Header == "" {
					ui.Text(c, shown(x.Text, true)).FontSize(12.5).TextColor(palette.muted).Shrink(1)
				}
				if i >= len(answer.Answers) {
					return
				}
				if x.Header != "" {
					ui.Text(c, shown(x.Header, false)).FontSize(11).FontWeight(600).TextColor(palette.accent).
						Padding(1, 6).Radius(4).Background(palette.accent.Alpha(0.14))
				}
				ui.Text(c, shown(answerText(answer.Answers[i]), false)).Selectable().FontSize(12.5).FontWeight(600)
			})
		}
		switch {
		case r.State == api.RequestAnswered:
			ui.Text(c, "Sending…").FontSize(12).TextColor(palette.muted)
		case r.State == api.RequestInvalidated || r.State == api.RequestExpired:
			why := r.Reason
			if why == "" {
				why = "The turn stopped."
			}
			ui.Text(c, shown(why, true)).FontSize(12).TextColor(palette.faint)
		}
	})
}

// answerText is an answer as one line: the labels picked, then the
// user's own words.
func answerText(x api.Answer) string {
	parts := slices.Clone(x.Picked)
	if x.Other != "" {
		parts = append(parts, `"`+x.Other+`"`)
	}
	return strings.Join(parts, ", ")
}

// pickOption picks an option, which answers a lone question at once.
func (a *app) pickOption(t thread, r *client.RequestEntry, q api.Questions, f *questionForm, i int, l string) {
	f.pick(q, i, l)
	if lone(q) {
		a.answerQuestions(t, r, f.answers())
	}
}

// answerQuestions sends the answers to r; none dismisses it.
func (a *app) answerQuestions(t thread, r *client.RequestEntry, as api.Answers) {
	if !a.backend.AnswerQuestions(t.env, t.id(), r.Request, as).IsZero() {
		delete(a.questions, r.Request)
	}
}

var pickKeys = []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4}

// questionKeys answer the waiting questions from the keyboard while the
// composer holds nothing, as approvalKeys do an approval: ⌘1 to ⌘4 pick
// in the question the keys are at, ⌘⏎ answers once each question has an
// answer, and ⇧⌘D or Escape dismisses. All but ⌘⏎ are in's keys the
// focused element leaves: a shell in the terminal drawer keeps Escape, and
// Control's chords, which ⌘ is on Linux and Windows. Escape in an Other
// field is the field's. Under an overlay of the app, they all wait, and
// Escape is the overlay's. ⌘'s act only once ⌘ went down after a frame
// showed the card with its keys, as approvalKeys' do.
func (a *app) questionKeys(c *ui.Context, in ui.Element, t thread, r *client.RequestEntry) {
	// Taken even while a question has no answer, from the shell, and under
	// an overlay: the composer's would stop the turn.
	enter := c.Shortcut(ui.Cmd, ui.KeyEnter)
	if a.covered {
		return
	}
	shown := a.keyedShown()
	q, _ := api.ParseQuestions(r.Body) // none, for a body the card can't show
	f := a.form(r, q)
	typing := slices.ContainsFunc(f.fields, func(h ui.Handle) bool { return h.Focused(c) })
	dismiss := in.OverlayShortcut(ui.Cmd|ui.Shift, ui.KeyD) && shown || !typing && in.OverlayShortcut(0, ui.KeyEscape)
	for j, k := range pickKeys {
		if in.OverlayShortcut(ui.Cmd, k) && shown && f.at < len(q.Questions) && j < len(q.Questions[f.at].Options) {
			a.pickOption(t, r, q, f, f.at, q.Questions[f.at].Options[j].Label)
			return
		}
	}
	switch {
	case enter && shown && len(q.Questions) > 0 && f.done():
		a.answerQuestions(t, r, f.answers())
	case dismiss:
		a.answerQuestions(t, r, api.Answers{})
	}
}
