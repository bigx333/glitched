package main

import (
	"encoding/json"
	"slices"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
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
	// Earlier counts the subagent's parts before the first of Parts,
	// which the panel leaves out.
	Earlier int `json:"earlier"`
	// Ended is whether the subagent ended, and Outcome how: completed,
	// stopped, failed or abandoned, or cut, when its run ended before
	// saying. Tokens are what it spent, as its provider said.
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

// subagentSel is the step whose native subagent a thread's panel shows:
// its run's, as a step's item is only its run's.
type subagentSel struct {
	run  api.ID
	item string
}

// subagentShown is the step whose subagent the panel beside t's chat
// shows, while no other panel is open there.
func (a *app) subagentShown(t thread) (subagentSel, client.WorkStep, bool) {
	sel, ok := a.subagentStep[t.id()]
	if !ok || a.showDiff || a.showPreview || a.showPluginTab {
		return subagentSel{}, client.WorkStep{}, false
	}
	for _, e := range a.rowsOf(t).es {
		if e.kind != entryWork || e.run != sel.run {
			continue
		}
		for _, w := range e.work {
			if w.Item == sel.item && w.Subagent != nil {
				return sel, w, true
			}
		}
	}
	return subagentSel{}, client.WorkStep{}, false
}

// openSubagent opens the panel of the subagent run's step w started, in
// place of any other beside the chat.
func (a *app) openSubagent(t thread, run api.ID, w client.WorkStep) {
	a.subagentStep[t.id()] = subagentSel{run, w.Item}
	a.showDiff, a.showPreview, a.showPluginTab, a.panelsJump = false, false, false, false
}

// Limits on what the panel's context carries, so that a copy drawn from
// it, the built-in's or a replacement's, stays within a component's
// widgets and a view's bytes: the latest parts, a step's title and the
// end of a failed step's output cut short, and the latest text.
const (
	subagentParts  = 250
	subagentTitle  = 300
	subagentOutput = 1000
	subagentText   = 192 << 10
	subagentBytes  = 640 << 10
)

// subagentMade is the context last made for a subagent's panel, as its
// subagent was at rev, under a step of that title.
type subagentMade struct {
	of    *client.Subagent
	rev   int
	title string
	panel subagentPanel
}

// closeWidth is an iconButton's: its icon, 15 points, and its padding of
// 6 on either side.
const closeWidth = 15 + 2*6

// subagentContext is the context of t's panel of the subagent step w
// started, but for its thread and part.
func subagentContext(w client.WorkStep) subagentPanel {
	s := w.Subagent
	panel := subagentPanel{Title: cutEnd(w.Title, subagentTitle), Steps: s.Steps(), Ended: s.Ended || s.Cut, Tokens: s.Tokens, Parts: []subagentPart{}}
	switch {
	case s.Ended:
		panel.Outcome = outcomes[s.Outcome]
	case s.Cut:
		panel.Outcome = "cut"
	}
	// From the latest part back, each as much as the budgets left take:
	// text by its bytes, everything as JSON escapes it, as a view
	// carries it. A text cut to fit keeps its end; past one that can't
	// keep a character, or a step that doesn't fit, the rest are left out.
	parts := s.Parts[max(len(s.Parts)-subagentParts, 0):]
	panel.Earlier = len(s.Parts) - len(parts)
	text, room := subagentText, subagentBytes
	for i := len(parts) - 1; i >= 0; i-- {
		p := parts[i]
		var part subagentPart
		if p.Step != nil {
			part.Step = &subagentStep{Kind: p.Step.Kind, Title: cutEnd(p.Step.Title, subagentTitle), Status: workStatuses[p.Step.Status], Exit: p.Step.Exit, Output: cutStart(p.Step.Output, subagentOutput)}
		} else {
			part.Text = cutStart(p.Text, text)
		}
		b, _ := json.Marshal(part)
		if len(b) > room && p.Step == nil {
			// Escaped, a byte may take six: what is left of the room
			// holds that many of the text's bytes, whatever they are.
			part.Text = cutStart(p.Text, min(text, (room-len(`{"text":""}`))/6))
			b, _ = json.Marshal(part)
		}
		if len(b) > room || p.Step == nil && part.Text == "" {
			panel.Earlier += i + 1
			break
		}
		room -= len(b) + 1
		text -= len(part.Text)
		panel.Parts = append(panel.Parts, part)
	}
	slices.Reverse(panel.Parts)
	return panel
}

// cutEnd is s cut to at most n bytes, at a character, with an ellipsis
// where it was cut.
func cutEnd(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := n - len("…")
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + "…"
}

// cutStart is the end of s, at most n bytes of it, at a character, with
// an ellipsis where it was cut: "" when that leaves no room for one.
func cutStart(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= len("…") {
		return ""
	}
	i := len(s) - n + len("…")
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return "…" + s[i:]
}

// subagentPanel is the panel of a native subagent's transcript, in two
// copies of thread.panel.subagent, as the diff panel is: the header's in
// the window's title bar, the body's below it. Glitched keeps the
// header's Close, which closes it whatever draws the panel, or fails to.
func (a *app) subagentPanel(c *ui.Context, t thread, sel subagentSel, w client.WorkStep) {
	// Measuring what fits is work: once for each change of the subagent.
	if m := &a.subagentMade; m.of != w.Subagent || m.rev != w.Subagent.Rev || m.title != w.Title {
		*m = subagentMade{w.Subagent, w.Subagent.Rev, w.Title, subagentContext(w)}
	}
	panel := a.subagentMade.panel
	copyOf := func(part string) func(plugins.ThreadRecord) any {
		return func(r plugins.ThreadRecord) any {
			p := panel
			p.Thread, p.Part = r, part
			return p
		}
	}
	key := t.id().String() + "/" + sel.run.String() + "/" + sel.item
	ui.Column(c).Width(a.diffWidth()).FillHeight().Children(func() {
		bar := c.TitleBar()
		// Close sits over the header's right end, clear of what the plugin
		// draws, so that a header too wide for a narrow panel never pushes
		// it out.
		headH, right := max(bar.Height, headerHeight), 8+bar.Right
		head := ui.Row(c).Height(headH).Padding(0, right+closeWidth+8, 0, 14).Gap(8).AlignItems(ui.Center).ClipX().DragWindow()
		// The subagent's transcript grows as it works: its panel scrolls,
		// from the top for each subagent shown.
		body := ui.Scroll(c).Key(key).Grow(1).Background(palette.sidebar).BorderWidth(1, 0, 0, 1).BorderColor(palette.border)
		a.pluginComponent(c, "thread.panel.subagent", &t,
			componentCopy{key: key + "/header", box: func() ui.Element { return head }, context: copyOf("header"),
				native: func(*widget.Node) {
					ui.Text(c, panel.Title).FontSize(13).SingleLine().Shrink(1).Grow(1).TextColor(palette.text)
				},
				over: func(ui.Element) {
					ui.Row(c).Absolute().Top(0).Right(right).Height(headH).AlignItems(ui.Center).Children(func() {
						if a.iconButton(c, "close-subagent", icX, "Close subagent", false).Clicked() {
							delete(a.subagentStep, t.id())
						}
					})
				}},
			componentCopy{key: key + "/body", box: func() ui.Element { return body }, context: copyOf("body"),
				native: func(*widget.Node) {
					ui.Text(c, "No plugin draws this panel. Turn on Subagent panel in Settings › Plugins.").TextColor(palette.muted).Margin(16)
				}})
	})
}
