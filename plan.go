package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// planStatuses are a plan step's statuses as thread.header.plan's context
// names them.
var planStatuses = map[api.PlanStatus]string{api.PlanPending: "pending", api.PlanActive: "active", api.PlanDone: "done"}

// headerPlan is the context of thread.header.plan: the plan the thread's
// agent works to, and whether the header is narrow.
type headerPlan struct {
	Thread plugins.ThreadRecord `json:"thread"`
	Narrow bool                 `json:"narrow"`
	Steps  []headerPlanStep     `json:"steps"`
}

type headerPlanStep struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

// planPill is how far the thread's agent is through its plan, which it
// has, narrow its smallest drawing. It is a copy of thread.header.plan,
// which the built-in Checklist draws. Unless shown, it is drawn out of the row,
// to be measured there. It is the copy's box.
func (a *app) planPill(c *ui.Context, t thread, narrow, shown bool) ui.Element {
	p := headerPlan{Narrow: narrow}
	for _, s := range t.Transcript.Plan.Steps {
		p.Steps = append(p.Steps, headerPlanStep{Text: s.Text, Status: planStatuses[s.Status]})
	}
	var box ui.Element
	a.pluginComponent(c, "thread.header.plan", &t, componentCopy{
		key: t.id().String(),
		box: func() ui.Element {
			box = ui.Row(c).Key("component:thread.header.plan").Shrink(0).AlignItems(ui.Center)
			if !shown {
				box = box.Absolute().Invisible()
			}
			return box
		},
		context: func(r plugins.ThreadRecord) any {
			p.Thread = r
			return p
		},
		builtin: func() *widget.Node { return planBadge(p) },
	})
	return box
}

// planMarks mark a step's status in the plan's tooltip.
var planMarks = map[string]string{"done": "✓", "active": "→", "pending": "○"}

// planBadge is the built-in Checklist's drawing of the plan p, as its main.ts
// draws it: a ring of the steps done, how many of how many, and the step
// the agent is at, or the next; narrow, the count alone. Its tooltip lists
// every step. A plan of no steps it doesn't draw.
func planBadge(p headerPlan) *widget.Node {
	if len(p.Steps) == 0 {
		return nil
	}
	done, at := 0, -1
	for i, s := range p.Steps {
		switch {
		case s.Status == "done":
			done++
		case s.Status == "active" && (at < 0 || p.Steps[at].Status != "active"), s.Status == "pending" && at < 0:
			at = i
		}
	}
	total := len(p.Steps)
	text := fmt.Sprintf("%d of %d", done, total)
	switch {
	case p.Narrow:
		text = fmt.Sprintf("%d/%d", done, total)
	case at >= 0:
		text += " · " + clipStep(p.Steps[at].Text)
	}
	lines := make([]string, total)
	for i, s := range p.Steps {
		lines[i] = cmpOr(planMarks[s.Status], planMarks["pending"]) + " " + s.Text
	}
	share := num(float64(int(float64(done)/float64(total)*1e4+0.5)) / 1e4)
	return &widget.Node{Type: "badge", Style: "pill", Text: text, Value: json.RawMessage(share),
		Tooltip: fmt.Sprintf("Checklist, %d of %d done\n", done, total) + strings.Join(lines, "\n")}
}

// clipStep is a step's text on one line, its runs of spaces and line
// breaks one space, cut to 40 characters with an ellipsis.
func clipStep(s string) string {
	chars := []rune(strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }), " "))
	if len(chars) > 40 {
		return string(chars[:39]) + "…"
	}
	return string(chars)
}

// planCard is a run's plan in the timeline, a step a line as the agent
// last said it: done steps faint and struck through, the one at work
// marked, spinning while the run is. A plan with every step done starts
// folded to its head; a click on the head folds or unfolds it, with the
// head where it was.
func (a *app) planCard(c *ui.Context, t thread, e entry) {
	done := 0
	for _, s := range e.plan {
		if s.Status == api.PlanDone {
			done++
		}
	}
	allDone := len(e.plan) > 0 && done == len(e.plan)
	m := a.memoOf(t.id())
	open := m.disclosureOpen(e.key, !allDone)
	live := t.View.Turn != nil && t.View.Turn.Run == e.run && t.View.Working()
	ui.Column(c).Key(e.key).Radius(12).Border(1, palette.border).Background(palette.bg.Alpha(0.6)).Clip().Children(func() {
		ui.Row(c).Key("bar").Children(func() {
			if ghost(c, "head", false).Radius(0).Padding(8, 12).Gap(8).Grow(1).Children(func() {
				ui.Icon(c, icListChecks).FontSize(12).TextColor(palette.muted)
				ui.Text(c, "Checklist").FontSize(12).Grow(1)
				ui.Text(c, fmt.Sprintf("%d of %d done", done, len(e.plan))).FontSize(11).TextColor(palette.faint)
				chevron(c, open)
			}).Clicked() {
				m.toggleDisclosure(e.key, !allDone)
				a.listFor(t.id()).HoldPlace()
			}
		})
		if !open {
			return
		}
		ui.Column(c).Key("steps").Padding(6, 12, 10).Gap(2).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Children(func() {
			for i, s := range e.plan {
				ui.Row(c).Key(i).Padding(2, 0).Gap(8).AlignItems(ui.Center).Children(func() {
					switch {
					case s.Status == api.PlanDone:
						ui.Icon(c, icCheckCirc).FontSize(12).TextColor(palette.green)
					case s.Status == api.PlanActive && live:
						a.statusMark(c, statusWorking)
					case s.Status == api.PlanActive:
						ui.Icon(c, icArrowRight).FontSize(12).TextColor(palette.accent)
					default:
						ui.Icon(c, icCircle).FontSize(12).TextColor(palette.faint)
					}
					text := ui.Text(c, s.Text).FontSize(12).Shrink(1).TextColor(palette.text)
					if s.Status == api.PlanDone {
						text.TextColor(palette.faint).Strikethrough()
					}
				})
			}
		})
	})
}
