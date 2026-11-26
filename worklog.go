package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
)

// A workMode is how a run's work log shows.
type workMode uint8

const (
	// workInlineSingle is settled work of one step: the step, with no card
	// around it.
	workInlineSingle workMode = iota
	// workLiveCollapsed is work under way, folded: one line of the step at
	// work.
	workLiveCollapsed
	// workSettledCollapsed is settled work, folded: one line of what it did.
	workSettledCollapsed
	// workExpanded is the card of every step.
	workExpanded
)

// classifyWork is how the work log of e shows, open or not.
func classifyWork(e entry, open bool) workMode {
	switch {
	case open:
		return workExpanded
	case e.working:
		return workLiveCollapsed
	case e.paused == "" && len(e.work) == 1:
		return workInlineSingle
	}
	return workSettledCollapsed
}

// workScrollHeight is the most a work log's open steps take before they
// scroll.
const workScrollHeight = 280

// workLog is a run's work. Folded, as it starts outside Zen mode, it is a
// line: the step at work while the work goes on, what it did once done,
// and under the line the steps that failed or ran a subagent, which never
// fold away. In an open Zen line it starts open: the user opened the line
// to see the work. A click on its head holds the timeline where it is, so
// the head stays under the pointer.
func (a *app) workLog(c *ui.Context, t thread, key string, e entry, files []fileChange) {
	m := a.memoOf(t.id())
	auto := a.settings.zen
	open := m.disclosureOpen(key, auto)
	mode := classifyWork(e, open)
	if mode == workInlineSingle {
		ui.Column(c).Key(key).Padding(0, 8).Children(func() { a.step(c, t, files, e.run, e.work[0]) })
		return
	}
	toggle := func() {
		m.toggleDisclosure(key, auto)
		a.listFor(t.id()).HoldPlace()
	}
	if mode == workExpanded {
		ui.Column(c).Key(key).Radius(12).Border(1, palette.border).Background(palette.bg.Alpha(0.6)).Clip().Children(func() {
			ui.Row(c).Key("bar").Children(func() {
				if ghost(c, "head", false).Radius(0).Padding(8, 12).Grow(1).Children(func() { a.workHead(c, t, e, true) }).Clicked() {
					toggle()
				}
			})
			ui.Scroll(c).Key("steps-scroll").MinHeight(0).MaxHeight(workScrollHeight).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Children(func() {
				ui.Column(c).Key("steps").Padding(6, 8, 10).Gap(2).Children(func() {
					for _, w := range e.work {
						a.step(c, t, files, e.run, w)
					}
				})
			})
		})
		return
	}
	kept := func(w client.WorkStep) bool { return stepFailed(w) || w.Subagent != nil }
	// A border as the card's, unseen, keeps the head where it is as it opens.
	ui.Column(c).Key(key).Gap(2).Border(1, ui.Transparent).Children(func() {
		ui.Row(c).Key("bar").Children(func() {
			if ghost(c, "head", false).Radius(6).Padding(8, 12).Grow(1).Children(func() { a.workHead(c, t, e, false) }).Clicked() {
				toggle()
			}
		})
		if !slices.ContainsFunc(e.work, kept) {
			return
		}
		ui.Column(c).Key("kept").Padding(0, 8).Gap(2).Children(func() {
			for _, w := range e.work {
				if kept(w) {
					a.step(c, t, files, e.run, w)
				}
			}
		})
	})
}

// workHead is the head of a work log: what the run waits on, the step at
// work, shimmering, or what the work did, how many steps failed and how
// long it took; how many steps it took, and the chevron.
func (a *app) workHead(c *ui.Context, t thread, e entry, open bool) {
	switch {
	case e.paused != "":
		ui.Icon(c, icClock).FontSize(12).TextColor(palette.amber)
		ui.Text(c, e.paused).FontSize(12).SingleLine().Shrink(1)
		ui.Spacer(c)
	case e.working:
		a.statusMark(c, statusWorking)
		ui.Row(c).Shrink(1).MinWidth(0).Children(func() {
			shimmerText(c, liveStep(t.View.Workspace, e.work), 12, palette.muted, palette.text)
		})
		ui.Spacer(c)
	default:
		ui.Text(c, workSummary(e.work)).FontSize(12).SingleLine().Shrink(1)
		ui.Spacer(c)
		if n := failedSteps(e.work); n > 0 {
			ui.Text(c, fmt.Sprintf("%d failed", n)).FontSize(11).TextColor(palette.red).Shrink(0)
		}
		if !e.started.IsZero() && !e.ended.IsZero() {
			ui.Text(c, e.ended.Sub(e.started).Round(time.Second).String()).FontSize(11).TextColor(palette.faint).Shrink(0)
		}
	}
	if len(e.work) > 1 || open {
		ui.Text(c, plural(len(e.work), "step")).FontSize(11).TextColor(palette.faint).Shrink(0)
	}
	chevron(c, open)
}

// liveStep names the step at work: the last started, or the last.
func liveStep(workspace string, ws []client.WorkStep) string {
	at := len(ws) - 1
	for i := len(ws) - 1; i >= 0; i-- {
		if ws[i].Status == api.WorkStarted {
			at = i
			break
		}
	}
	if at < 0 {
		return "Working…"
	}
	if verb, path, ok := stepName(workspace, ws[at]); ok {
		return verb + " " + path
	}
	return cmpOr(ws[at].Title, "Working…")
}

// stepName is a step's title as its verb and its path, from the
// workspace, when the title ends in the step's path.
func stepName(workspace string, w client.WorkStep) (verb, path string, ok bool) {
	verb, ok = strings.CutSuffix(w.Title, " "+w.Path)
	if !ok || w.Path == "" {
		return "", "", false
	}
	return verb, shownPath(workspace, w.Path), true
}

// stepFailed is whether a step failed: said so, or exited other than 0.
func stepFailed(w client.WorkStep) bool {
	return w.Status == api.WorkFailed || w.Exit != nil && *w.Exit != 0
}

// A workPhrase is what a work summary says of a kind of step: its verb,
// what it counts, and whether it counts each file once.
type workPhrase struct {
	verb, noun string
	files      bool
}

var (
	workPhrases = map[string]workPhrase{
		"read":    {"read", "file", true},
		"edit":    {"edited", "file", true},
		"command": {"ran", "command", false},
		"search":  {"searched", "time", false},
	}
	subagentPhrase = workPhrase{"started", "subagent", false}
	toolPhrase     = workPhrase{"used", "tool", false}
)

func phraseOf(w client.WorkStep) workPhrase {
	if w.Subagent != nil {
		return subagentPhrase
	}
	if p, ok := workPhrases[w.Kind]; ok {
		return p
	}
	return toolPhrase
}

// workSummary says what the steps did, a phrase a kind of step, in the
// order each kind first came: "Ran 2 commands · edited 1 file".
func workSummary(ws []client.WorkStep) string {
	type count struct {
		p     workPhrase
		n     int
		paths map[string]bool
	}
	var cs []*count
	for _, w := range ws {
		p := phraseOf(w)
		i := slices.IndexFunc(cs, func(c *count) bool { return c.p == p })
		if i < 0 {
			i = len(cs)
			cs = append(cs, &count{p: p, paths: map[string]bool{}})
		}
		c := cs[i]
		if p.files && w.Path != "" {
			if c.paths[w.Path] {
				continue
			}
			c.paths[w.Path] = true
		}
		c.n++
	}
	if len(cs) == 0 {
		return "Worked"
	}
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.p.verb + " " + plural(c.n, c.p.noun)
	}
	s := strings.Join(parts, " · ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// step is one of the agent's steps. A command that finished shows its
// exit code and how long it took, and what it printed when it failed. An
// edit of a file the diff lists opens it there.
func (a *app) step(c *ui.Context, t thread, files []fileChange, run api.ID, w client.WorkStep) {
	icon := icFile
	switch w.Kind {
	case "command":
		icon = icTerminal
	case "edit":
		icon = icPencil
	case "search":
		icon = icSearch
	}
	file := -1
	if w.Kind == "edit" && w.Path != "" {
		file = slices.IndexFunc(files, func(f fileChange) bool { return f.path == w.Path })
	}
	sub := w.Subagent != nil
	if sub {
		icon = icChild
	}
	var r ui.Element
	if file >= 0 || sub {
		r = ghost(c, w.Item, false).Padding(2, 4).Radius(5).Gap(8)
	} else {
		r = ui.Row(c).Key(w.Item).Padding(2, 4).Radius(5).Gap(8).AlignItems(ui.Center)
	}
	if r.Children(func() {
		ui.Icon(c, icon).FontSize(12).TextColor(palette.faint)
		// An edit's path reads from the workspace and gives way at its
		// start, so the file's name shows.
		if verb, path, ok := stepName(t.View.Workspace, w); ok {
			name := ui.Row(c).Gap(4).Shrink(1)
			if file >= 0 {
				name.Label(verb + " " + path) // the button's name
			}
			name.Children(func() {
				ui.Text(c, verb).FontSize(12).SingleLine().Shrink(0).TextColor(palette.text)
				ui.Text(c, path).FontSize(12).SingleLine().TruncateStart().Shrink(1).TextColor(palette.text)
			})
		} else {
			ui.Text(c, w.Title).FontSize(12).SingleLine().Shrink(1).TextColor(palette.text)
		}
		if file >= 0 {
			add, del := a.diffStats(t, files[file])
			counts(c, add, del)
		}
		if sub {
			ui.Text(c, plural(w.Subagent.Steps(), "step")).FontSize(11.5).SingleLine().Shrink(0).TextColor(palette.faint)
		}
		ui.Spacer(c)
		switch {
		case w.Status == api.WorkStarted:
			a.statusMark(c, statusWorking)
		case w.Exit != nil:
			mark, color := "✓", palette.green
			if *w.Exit != 0 {
				mark, color = "✗", palette.red
			}
			ui.Textf(c, "%s %d", mark, *w.Exit).FontSize(11.5).Font("monospace").TextColor(color)
			ui.Text(c, "· "+tookFor(w.Ended.Sub(w.Started))).FontSize(11.5).Font("monospace").TextColor(palette.faint)
		case w.Status == api.WorkFailed:
			a.tip(c, ui.Icon(c, icX).FontSize(12).TextColor(palette.red), "Failed")
		case file >= 0 && r.Hovered():
			ui.Text(c, "Open in diff").FontSize(11.5).TextColor(palette.muted)
			ui.Icon(c, icExternal).FontSize(11).TextColor(palette.muted)
		case sub && r.Hovered():
			ui.Text(c, "Open").FontSize(11.5).TextColor(palette.muted)
			ui.Icon(c, icExternal).FontSize(11).TextColor(palette.muted)
		}
	}).Clicked() {
		switch {
		case file >= 0:
			a.showDiff, a.showPreview, a.panelsJump = true, false, false
			a.diffFile[t.id()] = w.Path
		case sub:
			a.openSubagent(t, run, w)
		}
	}
	if w.Output != "" && stepFailed(w) {
		ui.Text(c, w.Output).Key(w.Item+"/output").Selectable().FontSize(11.5).Font("monospace").LineHeight(1.5).TextColor(palette.muted).
			Margin(2, 0, 4, 24).Padding(6, 10).Radius(6).Background(palette.bg).Border(1, palette.border)
	}
}
