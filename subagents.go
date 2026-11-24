package main

import (
	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plugins"
)

// workStatuses are a step's statuses as thread.panel.subagent's context
// names them.
var workStatuses = map[api.WorkStatus]string{api.WorkStarted: "started", api.WorkCompleted: "completed", api.WorkFailed: "failed"}

// outcomes are how a subagent ended, as thread.panel.subagent's context
// names them.
var outcomes = map[api.Outcome]string{api.OutcomeCompleted: "completed", api.OutcomeInterrupted: "stopped", api.OutcomeFailed: "failed", api.OutcomeAbandoned: "abandoned"}

// subagentPanel is the context of thread.panel.subagent: the step that
// started a native subagent, and what the subagent said and did under it,
// in order, and how it ended.
type subagentPanel struct {
	Thread plugins.ThreadRecord `json:"thread"`
	// Part is the copy's: the header or the body.
	Part  string         `json:"part"`
	Title string         `json:"title"`
	Parts []subagentPart `json:"parts"`
	Steps int            `json:"steps"`
	// Ended is whether the subagent ended, and Outcome how: completed,
	// stopped, failed or abandoned. Tokens are what it spent, as its provider said.
	Ended   bool   `json:"ended"`
	Outcome string `json:"outcome,omitempty"`
	Tokens  int64  `json:"tokens"`
}

// subagentPart is one of the subagent's runs of text, or one of its steps.
type subagentPart struct {
	Text string        `json:"text,omitempty"`
	Step *subagentStep `json:"step,omitempty"`
}

type subagentStep struct {
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Exit   *int   `json:"exit,omitempty"`
	Output string `json:"output,omitempty"`
}

// subagentShown is the step whose subagent the panel beside t's chat
// shows, while no other panel is open there.
func (a *app) subagentShown(t thread) (client.WorkStep, bool) {
	item := a.subagentStep[t.id()]
	if item == "" || a.showDiff || a.showPreview || a.showPluginTab {
		return client.WorkStep{}, false
	}
	for _, e := range a.rowsOf(t).es {
		for _, w := range e.work {
			if w.Item == item && w.Subagent != nil {
				return w, true
			}
		}
	}
	return client.WorkStep{}, false
}

// openSubagent opens the panel of the subagent step w started, in place
// of any other beside the chat.
func (a *app) openSubagent(t thread, w client.WorkStep) {
	a.subagentStep[t.id()] = w.Item
	a.showDiff, a.showPreview, a.showPluginTab, a.panelsJump = false, false, false, false
}

// subagentPanel is the panel of a native subagent's transcript, in two
// copies of thread.panel.subagent, as the diff panel is: the header's in
// the window's title bar, the body's below it.
func (a *app) subagentPanel(c *ui.Context, t thread, w client.WorkStep) {
	s := w.Subagent
	panel := subagentPanel{Title: w.Title, Steps: s.Steps(), Ended: s.Ended, Tokens: s.Tokens, Parts: []subagentPart{}}
	if s.Ended {
		panel.Outcome = outcomes[s.Outcome]
	}
	for _, p := range s.Parts {
		if p.Step == nil {
			panel.Parts = append(panel.Parts, subagentPart{Text: p.Text})
			continue
		}
		panel.Parts = append(panel.Parts, subagentPart{Step: &subagentStep{Kind: p.Step.Kind, Title: p.Step.Title, Status: workStatuses[p.Step.Status], Exit: p.Step.Exit, Output: p.Step.Output}})
	}
	copyOf := func(part string) func(plugins.ThreadRecord) any {
		return func(r plugins.ThreadRecord) any {
			p := panel
			p.Thread, p.Part = r, part
			return p
		}
	}
	key := t.id().String() + "/" + w.Item
	ui.Column(c).Width(a.diffWidth()).FillHeight().Children(func() {
		bar := c.TitleBar()
		head := ui.Row(c).Height(max(bar.Height, headerHeight)).Padding(0, 8+bar.Right, 0, 14).Gap(8).AlignItems(ui.Center).DragWindow()
		// The subagent's transcript grows as it works: its panel scrolls.
		body := ui.Scroll(c).Grow(1).Background(palette.sidebar).BorderWidth(1, 0, 0, 1).BorderColor(palette.border)
		a.pluginComponent(c, "thread.panel.subagent", &t,
			componentCopy{key: key + "/header", box: func() ui.Element { return head }, context: copyOf("header")},
			componentCopy{key: key + "/body", box: func() ui.Element { return body }, context: copyOf("body")})
	})
}
