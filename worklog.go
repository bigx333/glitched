package main

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
)

// A workMode is the layout shape of a run's work log.
type workMode uint8

const (
	// workInlineSingle is settled work of one step: the step, with no card
	// around it.
	workInlineSingle workMode = iota
	// workCollapsed is one summary or live status line, with failed and
	// subagent steps surfaced underneath.
	workCollapsed
	// workExpanded is the bordered card listing every step.
	workExpanded
)

// A workActivity is what a work log is doing right now.
type workActivity uint8

const (
	workIdle workActivity = iota
	workRunning
	workThinking
	workStopping
	workPaused
)

// workState derives the live activity of the work row rs.es[i].
func workState(t thread, rs rows, i int, zen bool) workActivity {
	e := rs.es[i]
	if e.paused != "" {
		return workPaused
	}
	if !zen && i == thinkingAt(t, rs) {
		switch {
		case t.View.Run == api.RunStopping:
			return workStopping
		case e.working:
			return workRunning
		default:
			return workThinking
		}
	}
	if e.working {
		return workRunning
	}
	return workIdle
}

// classifyWork picks the layout shape of a work log from its activity and
// disclosure state. A settled single-step log always renders inline so it
// never shows a toggle that removes itself on click.
func classifyWork(e entry, act workActivity, open bool) workMode {
	if act == workIdle && len(e.work) == 1 {
		return workInlineSingle
	}
	if open {
		return workExpanded
	}
	return workCollapsed
}

// workScrollHeight is the most a work log's open steps take before they
// scroll.
const workScrollHeight = 280

// workLog is a run's work. Folded, as it starts outside Zen mode, it is a
// single line: the active step, thinking between steps, or a summary once
// settled, with failed and subagent steps surfaced underneath. In an open
// Zen line multi-step logs start open so the user does not click twice.
// Open, it is a ledger of its steps, which tabs filter by kind.
func (a *app) workLog(c *ui.Context, t thread, rs rows, i int, key string, e entry, files []fileChange) {
	m := a.memoOf(t.id())
	act := workState(t, rs, i, a.settings.zen)
	auto := a.settings.zen
	open := m.disclosureOpen(key, auto)
	mode := classifyWork(e, act, open)
	lg := workLedgerStats(e.work)
	if mode == workInlineSingle {
		ws := m.workScrollOf(key)
		ui.Column(c).Key(key).Padding(0, 1).Children(func() { a.step(c, t, files, e.run, e.work[0], lg.maxDur, ws) })
		return
	}
	toggle := func() {
		wasOpen := m.disclosureOpen(key, auto)
		m.toggleDisclosure(key, auto)
		if wasOpen {
			m.dropWorkScroll(key)
		}
		a.holdUnlessAtEnd(t)
	}
	if mode == workExpanded {
		ws := m.workScrollOf(key)
		live := act == workRunning || act == workThinking || act == workStopping
		if live && (!ws.opened || ws.scroll.Y >= ws.scroll.MaxY-2) {
			ws.scroll.Y = math.MaxFloat32
		}
		ws.opened = true
		filtering := len(lg.tabs) > 1
		if !filtering || !slices.ContainsFunc(lg.tabs, func(tb workTab) bool { return tb.filter == ws.filter }) {
			ws.filter = workFilterAll
		}
		ui.Column(c).Key(key).Radius(10).Border(1, palette.border).Background(palette.bg.Alpha(0.75)).Clip().Children(func() {
			ui.Row(c).Key("bar").Children(func() {
				if ghost(c, "head", false).Radius(0).Padding(8, 10).Grow(1).Children(func() { a.workHead(c, t, e, act, true) }).Clicked() {
					toggle()
				}
			})
			if filtering {
				ui.Row(c).Key("filters").Role(ui.RoleTabList).FocusGroup(ui.Horizontal).Label("Filter steps").
					Wrap().Padding(5, 10).Gap(4).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).
					Background(palette.hover.Alpha(0.5)).Children(func() {
					for _, tb := range lg.tabs {
						on := ws.filter == tb.filter
						text := palette.muted
						switch {
						case tb.filter == workFilterFailed:
							text = palette.red
						case on:
							text = palette.text
						}
						if ghost(c, tb.filter, on).Role(ui.RoleTab).AccessSelected(on).Padding(3, 7).Radius(5).Shrink(0).Children(func() {
							ui.Text(c, tb.text()).Font("monospace").FontSize(10.5).FontFeatures("tnum").TextColor(text).SingleLine().Shrink(0)
						}).Clicked() {
							ws.filter = tb.filter
							ws.scroll.Y = 0
							a.holdUnlessAtEnd(t)
						}
					}
				})
			}
			ui.Scroll(c).Key("steps-scroll").TrackScroll(&ws.scroll).Focusable().Label("Work steps").
				MinHeight(0).MaxHeight(workScrollHeight).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Children(func() {
				ui.Column(c).Key("steps").Children(func() {
					shown := slices.DeleteFunc(slices.Clone(e.work), func(w client.WorkStep) bool { return !ws.filter.matches(w) })
					for n, w := range shown {
						r := ui.Column(c).Key("row/" + w.Item)
						if n < len(shown)-1 {
							r.BorderWidth(0, 0, 1, 0).BorderColor(palette.border.Alpha(0.55))
						}
						r.Children(func() { a.step(c, t, files, e.run, w, lg.maxDur, ws) })
					}
				})
			})
		})
		return
	}
	kept := func(w client.WorkStep) bool { return stepFailed(w) || w.Subagent != nil }
	// A transparent border matching the expanded card keeps the header Y
	// fixed when opening and closing.
	ui.Column(c).Key(key).Gap(2).Border(1, ui.Transparent).Children(func() {
		ui.Row(c).Key("bar").Children(func() {
			if ghost(c, "head", false).Radius(6).Padding(8, 10).Grow(1).Children(func() { a.workHead(c, t, e, act, false) }).Clicked() {
				toggle()
			}
		})
		if !slices.ContainsFunc(e.work, kept) {
			return
		}
		ws := m.workScrollOf(key)
		ui.Column(c).Key("kept").Gap(2).Children(func() {
			for _, w := range e.work {
				if kept(w) {
					a.step(c, t, files, e.run, w, lg.maxDur, ws)
				}
			}
		})
	})
}

// workHead draws the clickable header of a work log.
func (a *app) workHead(c *ui.Context, t thread, e entry, act workActivity, open bool) {
	steps := len(e.work) > 1 || open
	switch act {
	case workPaused:
		ui.Icon(c, icClock).FontSize(12).TextColor(palette.amber)
		ui.Text(c, e.paused).FontSize(12).SingleLine().Shrink(1)
		ui.Spacer(c)
	case workStopping:
		a.statusMark(c, statusWorking)
		shimmerText(c, "Stopping…", 12, palette.muted, palette.text)
	case workRunning:
		a.statusMark(c, statusWorking)
		shimmerText(c, liveStep(t.View.Workspace, e.work), 12, palette.muted, palette.text)
	case workThinking:
		a.statusMark(c, statusWorking)
		shimmerText(c, thinkingWorkLabel(t.View.Workspace, e.work), 12, palette.muted, palette.text)
	default:
		ui.Text(c, workSummary(e.work)).FontSize(12).SingleLine().Shrink(1)
		ui.Spacer(c)
		if len(e.work) > 1 {
			ui.Row(c).Key("spark").Width(44).Height(4).Radius(999).Clip().Shrink(0).Children(func() {
				for _, s := range workSpark(e.work) {
					ui.Row(c).Grow(float32(s.n)).Height(4).Background(workKinds[s.kind].color(palette))
				}
			})
		}
		if n := failedSteps(e.work); n > 0 {
			ui.Text(c, fmt.Sprintf("%d failed", n)).FontSize(11).LineHeight(1).TextColor(palette.red).Background(palette.red.Alpha(0.14)).
				Radius(999).Padding(0.5, 6).SingleLine().Shrink(0)
		}
		if !e.started.IsZero() && !e.ended.IsZero() {
			ui.Text(c, e.ended.Sub(e.started).Round(time.Second).String()).Font("monospace").FontFeatures("tnum").FontSize(11).
				TextColor(palette.faint).SingleLine().Shrink(0)
			if steps {
				ui.Text(c, "·").FontSize(11).TextColor(palette.faint).SingleLine().Shrink(0)
			}
		}
	}
	if steps {
		ui.Text(c, plural(len(e.work), "step")).FontSize(11).TextColor(palette.faint).SingleLine().Shrink(0)
	}
	chevron(c, open)
}

// thinkingWorkLabel names the live row between sequential steps so the
// user sees both that the agent is thinking and the last step completed.
func thinkingWorkLabel(workspace string, ws []client.WorkStep) string {
	if len(ws) == 0 {
		return "Thinking…"
	}
	last := liveStep(workspace, ws)
	if last == "" || last == "Working…" {
		return "Thinking…"
	}
	return "Thinking… · " + last
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
// what it counts, and whether it counts each file path once.
type workPhrase struct {
	verb, noun string
	files      bool
}

var (
	readFilePhrase = workPhrase{"read", "file", true}
	editPhrase     = workPhrase{"made", "edit", false}
	commandPhrase  = workPhrase{"ran", "command", false}
	searchPhrase   = workPhrase{"searched", "time", false}
	subagentPhrase = workPhrase{"started", "subagent", false}
	toolPhrase     = workPhrase{"used", "tool", false}
)

// phraseOf classifies a step truthfully from provider fields: file reads
// carry Path while pathless reads (such as WebFetch) count as tools, and
// edits count operations because multi-file patches carry only one Path.
func phraseOf(w client.WorkStep) workPhrase {
	if w.Subagent != nil {
		return subagentPhrase
	}
	switch w.Kind {
	case "read":
		if w.Path != "" {
			return readFilePhrase
		}
		return toolPhrase
	case "edit":
		return editPhrase
	case "command":
		return commandPhrase
	case "search":
		return searchPhrase
	}
	return toolPhrase
}

// workSummary says what the steps did, a phrase a kind of step, in the
// order each kind first came: "Ran 2 commands · made 1 edit".
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

// A workFilter is a kind of step, as the ledger tags and filters it, or
// all steps, or the failed ones.
type workFilter uint8

const (
	workFilterAll workFilter = iota
	workFilterCommand
	workFilterRead
	workFilterEdit
	workFilterSearch
	workFilterSubagent
	workFilterTool
	workFilterFailed
)

// A workKind is how the ledger shows a kind of step: its tag, its filter
// tab's label and its color.
type workKind struct {
	tag, label string
	color      func(colors) ui.Color
}

var workKinds = [...]workKind{
	workFilterAll:      {"", "All", func(p colors) ui.Color { return p.muted }},
	workFilterCommand:  {"CMD", "Cmd", func(p colors) ui.Color { return p.blue }},
	workFilterRead:     {"READ", "Read", func(p colors) ui.Color { return p.muted }},
	workFilterEdit:     {"EDIT", "Edit", func(p colors) ui.Color { return p.accent }},
	workFilterSearch:   {"FIND", "Search", func(p colors) ui.Color { return p.muted }},
	workFilterSubagent: {"AGENT", "Agent", func(p colors) ui.Color { return p.amber }},
	workFilterTool:     {"TOOL", "Tool", func(p colors) ui.Color { return p.muted }},
	workFilterFailed:   {"FAIL", "Failed", func(p colors) ui.Color { return p.red }},
}

// stepKindOf is the kind of a step, by the same truth as phraseOf.
func stepKindOf(w client.WorkStep) workFilter {
	switch phraseOf(w) {
	case subagentPhrase:
		return workFilterSubagent
	case readFilePhrase:
		return workFilterRead
	case editPhrase:
		return workFilterEdit
	case commandPhrase:
		return workFilterCommand
	case searchPhrase:
		return workFilterSearch
	}
	return workFilterTool
}

// matches is whether the filter shows the step.
func (f workFilter) matches(w client.WorkStep) bool {
	switch f {
	case workFilterAll:
		return true
	case workFilterFailed:
		return stepFailed(w)
	}
	return stepKindOf(w) == f
}

// A workTab is a filter tab of the ledger, with the steps it shows.
type workTab struct {
	filter workFilter
	n      int
}

func (tb workTab) text() string { return fmt.Sprintf("%s %d", workKinds[tb.filter].label, tb.n) }

// A workLedger is what a work log's ledger takes from all its steps: the
// longest step, which duration bars measure against, and its filter tabs.
type workLedger struct {
	maxDur time.Duration
	tabs   []workTab
}

// minLedgerDur is the least a duration bar measures against, so a log of
// quick commands does not draw them all full.
const minLedgerDur = 1500 * time.Millisecond

// workLedgerStats is the ledger of the steps: tabs for all, each strict
// subset kind in the order it first came, and the failed when some (not
// all) steps failed.
func workLedgerStats(ws []client.WorkStep) workLedger {
	lg := workLedger{maxDur: minLedgerDur, tabs: []workTab{{workFilterAll, len(ws)}}}
	failed := 0
	for _, w := range ws {
		if d, ok := stepDur(w); ok {
			lg.maxDur = max(lg.maxDur, d)
		}
		if stepFailed(w) {
			failed++
		}
		k := stepKindOf(w)
		if i := slices.IndexFunc(lg.tabs, func(tb workTab) bool { return tb.filter == k }); i >= 0 {
			lg.tabs[i].n++
		} else {
			lg.tabs = append(lg.tabs, workTab{k, 1})
		}
	}
	if failed > 0 {
		lg.tabs = append(lg.tabs, workTab{workFilterFailed, failed})
	}
	// A filter tab matching every step duplicates All and adds no choice.
	lg.tabs = append(lg.tabs[:1], slices.DeleteFunc(lg.tabs[1:], func(tb workTab) bool { return tb.n == len(ws) })...)
	return lg
}

// stepDur is how long a finished step took, when it says.
func stepDur(w client.WorkStep) (time.Duration, bool) {
	if w.Status == api.WorkStarted || w.Started.IsZero() || w.Ended.IsZero() {
		return 0, false
	}
	d := w.Ended.Sub(w.Started)
	return d, d > 0
}

// A sparkRun is a run of steps of one kind, or failed, in a work log's
// breakdown bar.
type sparkRun struct {
	kind workFilter
	n    int
}

// workSparkMax is the most runs the breakdown bar draws one by one; past
// it, it draws one run a kind.
const workSparkMax = 24

// workSpark is the breakdown bar of the steps: their runs of one kind in
// order or, with too many runs, the kinds in the order each first came.
func workSpark(ws []client.WorkStep) []sparkRun {
	var runs []sparkRun
	for _, w := range ws {
		k := stepKindOf(w)
		if stepFailed(w) {
			k = workFilterFailed
		}
		if n := len(runs); n > 0 && runs[n-1].kind == k {
			runs[n-1].n++
		} else {
			runs = append(runs, sparkRun{k, 1})
		}
	}
	if len(runs) <= workSparkMax {
		return runs
	}
	var kinds []sparkRun
	for _, r := range runs {
		if i := slices.IndexFunc(kinds, func(k sparkRun) bool { return k.kind == r.kind }); i >= 0 {
			kinds[i].n += r.n
		} else {
			kinds = append(kinds, r)
		}
	}
	return kinds
}

// step is one of the agent's steps, as a ledger row: a tag of its kind,
// what it worked on, and how it went. A finished command shows how long
// it took against the log's longest step, maxDur. With ws, clicking a
// command or a failed step with output toggles its drawer open or closed;
// edits in the diff and subagents still open their panels on row click,
// with a sibling error toggle when they fail with output.
func (a *app) step(c *ui.Context, t thread, files []fileChange, run api.ID, w client.WorkStep, maxDur time.Duration, ws *workScroll) {
	kind := stepKindOf(w)
	failed := stepFailed(w)
	file := -1
	if w.Kind == "edit" && w.Path != "" {
		file = slices.IndexFunc(files, func(f fileChange) bool { return f.path == w.Path })
	}
	sub := w.Subagent != nil
	navigable := file >= 0 || sub
	inspectable := !navigable && ws != nil && (kind == workFilterCommand || (failed && w.Output != ""))
	errToggle := navigable && failed && w.Output != "" && ws != nil
	inspected := (inspectable || errToggle) && ws.inspect[w.Item]
	drawRow := func() {
		var r ui.Element
		if navigable || inspectable {
			r = ghost(c, w.Item, false)
			if inspectable {
				r.Label(stepButtonLabel(w, failed)).Expanded(inspected) // the button's name and drawer state
			}
		} else {
			r = ui.Row(c).Key(w.Item).AlignItems(ui.Center)
		}
		if errToggle {
			r.Grow(1).MinWidth(0)
		}
		r.Padding(4, 10).Gap(8).Radius(5)
		if failed && !errToggle && !r.Hovered() {
			r.Background(palette.red.Alpha(0.06))
		}
		if r.Children(func() {
			stepTag(c, w, kind, failed)
			stepPayload(c, t.View.Workspace, w, kind)
			ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
				if sub {
					ui.Text(c, plural(w.Subagent.Steps(), "step")).FontSize(11).FontFeatures("tnum").TextColor(palette.faint).SingleLine().Shrink(0)
				}
				switch {
				case w.Status == api.WorkStarted:
					a.statusMark(c, statusWorking)
				case file >= 0:
					add, del := a.diffStats(t, files[file])
					counts(c, add, del)
					if r.Hovered() {
						ui.Text(c, "Open in diff").FontSize(11).TextColor(palette.muted).SingleLine().Shrink(0)
						ui.Icon(c, icExternal).FontSize(11).TextColor(palette.muted)
					}
				case sub || kind == workFilterCommand:
					d, timed := stepDur(w)
					if timed {
						durationBar(c, d, maxDur, failed)
					}
					if timed || w.Exit != nil {
						ui.Text(c, tookFor(d)).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(palette.faint).
							MinWidth(30).TextAlign(ui.End).SingleLine().Shrink(0)
					}
					if sub && r.Hovered() {
						ui.Icon(c, icExternal).FontSize(11).TextColor(palette.muted)
					}
				}
				if inspectable && failed && w.Output != "" {
					chevron(c.Key("chevron"), inspected).FontSize(10).TextColor(palette.faint)
				}
			})
		}).Clicked() {
			switch {
			case file >= 0:
				a.showDiff, a.showPreview, a.panelsJump = true, false, false
				a.diffFile[t.id()] = w.Path
			case sub:
				a.openSubagent(t, run, w)
			case inspectable:
				ws.toggleInspect(w.Item)
				a.holdUnlessAtEnd(t)
			}
		}
	}
	if errToggle {
		ui.Row(c).Key(w.Item + "/wrap").AlignItems(ui.Center).Radius(5).Background(palette.red.Alpha(0.06)).Children(func() {
			drawRow()
			if ghost(c, w.Item+"/error-toggle", false).Label(errorToggleLabel(w, inspected)).Expanded(inspected).
				Padding(5, 7).Radius(5).Justify(ui.Center).Shrink(0).Children(func() {
				chevron(c.Key("chevron"), inspected).FontSize(10).TextColor(palette.faint)
			}).Clicked() {
				ws.toggleInspect(w.Item)
				a.holdUnlessAtEnd(t)
			}
		})
	} else {
		drawRow()
	}
	if !inspected {
		return
	}
	border := palette.border
	if failed {
		border = palette.red.Alpha(0.35)
	}
	showCommand := kind == workFilterCommand
	ui.Column(c).Key(w.Item+"/drawer").Margin(0, 10, 6, 64).Padding(7, 10).Gap(6).Radius(6).Background(palette.bg).Border(1, border).Children(func() {
		if showCommand {
			ui.Text(c, w.Title).Key(w.Item + "/command").Selectable().FontSize(11.5).Font("monospace").LineHeight(1.5).TextColor(palette.text)
		}
		if w.Output != "" {
			out := ui.Text(c, w.Output).Key(w.Item + "/output").Selectable().FontSize(11.5).Font("monospace").LineHeight(1.5).TextColor(palette.muted)
			if showCommand {
				out.BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Padding(6, 0, 0)
			}
		}
	})
}

// stepButtonLabel is the accessible name of an inspectable step button:
// its title, followed by its failure tag when it failed so screen readers
// hear both the step and its failure status while prefix lookups keep matching.
func stepButtonLabel(w client.WorkStep, failed bool) string {
	if !failed {
		return w.Title
	}
	tag := "ERR"
	if w.Exit != nil {
		tag = fmt.Sprintf("ERR %d", *w.Exit)
	}
	return fmt.Sprintf("%s · %s", w.Title, tag)
}

// errorToggleLabel is the accessible name of the sibling error drawer
// toggle on a failed edit or subagent row.
func errorToggleLabel(w client.WorkStep, open bool) string {
	verb := "Show error"
	if open {
		verb = "Hide error"
	}
	if w.Title == "" {
		return verb
	}
	return verb + ": " + w.Title
}

// stepTag is a ledger row's tag: its kind, or how it failed.
func stepTag(c *ui.Context, w client.WorkStep, kind workFilter, failed bool) {
	tag, color := workKinds[kind].tag, workKinds[kind].color(palette)
	if failed {
		tag, color = workKinds[workFilterFailed].tag, palette.red
		if w.Exit != nil {
			tag = fmt.Sprintf("ERR %d", *w.Exit)
		}
	}
	bg := color.Alpha(0.12)
	if failed {
		bg = palette.red.Alpha(0.16)
	}
	ui.Row(c).Width(46).Shrink(0).Radius(4).Padding(2, 0).Justify(ui.Center).AlignItems(ui.Center).Background(bg).Children(func() {
		ui.Text(c, tag).Font("monospace").FontSize(9.5).FontWeight(700).TextColor(color).SingleLine()
	})
}

// stepPayload is what a ledger row's step worked on: a file's directory
// giving way to its name, a command's program before its arguments, or
// the step's title. Each is named in full for tests and screen readers.
func stepPayload(c *ui.Context, workspace string, w client.WorkStep, kind workFilter) {
	mono := func(s string, color ui.Color) ui.Element {
		return ui.Text(c, s).Font("monospace").FontSize(11.5).TextColor(color).SingleLine()
	}
	if verb, path, ok := stepName(workspace, w); ok {
		cut := strings.LastIndexByte(path, '/') + 1
		ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Center).Clip().Label(verb + " " + path).Children(func() {
			if cut > 0 {
				mono(path[:cut], palette.faint).TruncateStart().Shrink(1)
			}
			mono(path[cut:], palette.text).Shrink(0)
		})
		return
	}
	if kind == workFilterCommand {
		bin, args := splitCommand(w.Title)
		ui.Row(c).Grow(1).MinWidth(0).Gap(7).AlignItems(ui.Center).Clip().Label(w.Title).Children(func() {
			if args == "" || len(bin) > 24 {
				mono(bin, palette.text).Shrink(1)
			} else {
				mono(bin, palette.text).Shrink(0)
			}
			if args != "" {
				mono(args, palette.muted).Shrink(1)
			}
		})
		return
	}
	ui.Text(c, w.Title).FontSize(12).TextColor(palette.text).SingleLine().Shrink(1).Grow(1).MinWidth(0)
}

var flatBreaks = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// splitCommand splits a command's title at its first separator, flattening
// line breaks for one-line display while preserving spaces inside arguments.
func splitCommand(title string) (bin, args string) {
	s := flatBreaks.Replace(strings.TrimLeft(title, " \t\r\n"))
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimLeft(s[i+1:], " \t")
}

// durationBar is how long a step took against the log's longest, maxDur:
// red when it failed and amber when it is among the slowest.
func durationBar(c *ui.Context, d, maxDur time.Duration, failed bool) {
	color := palette.blue.Alpha(0.75)
	switch {
	case failed:
		color = palette.red
	case slowStep(d, maxDur):
		color = palette.amber
	}
	ui.Row(c).Width(durationBarWidth).Height(3).Radius(999).Background(palette.text.Alpha(0.08)).Clip().Shrink(0).Children(func() {
		ui.Row(c).Width(durationFill(d, maxDur)).Height(3).Radius(999).Background(color)
	})
}

const durationBarWidth = 28

// durationFill is how much of a duration bar a step of d fills, against
// the log's longest step, maxDur: at least a sliver.
func durationFill(d, maxDur time.Duration) float32 {
	return min(max(durationBarWidth*float32(d)/float32(max(maxDur, 1)), 3), durationBarWidth)
}

// slowStep is whether a step of d is among the log's slowest: 2s at least
// and 65% of its longest step.
func slowStep(d, maxDur time.Duration) bool {
	return d >= 2*time.Second && d*100 >= maxDur*65
}

// holdUnlessAtEnd keeps the timeline's place as a work log, its filter or
// a step's drawer opens or closes, so that the head clicked stays under
// the pointer while the user reads further up. At the end, the list
// follows its end instead, keeping the end, and with it the full
// composer, in view.
func (a *app) holdUnlessAtEnd(t thread) {
	if l := a.listFor(t.id()); !l.AtEnd() {
		l.HoldPlace()
	}
}
