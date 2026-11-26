package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// listFor is the thread's list of entries, kept per thread so that each
// keeps where it was scrolled to.
func (a *app) listFor(id api.ID) *ui.ListState {
	l := a.lists[id]
	if l == nil {
		l = &ui.ListState{}
		a.lists[id] = l
	}
	return l
}

// timeline lists the thread's entries, as MyGo's markdown-chat does: the
// list builds only those in view, starts at the end and follows it as a
// reply streams in, gliding, until the user scrolls up; scrolling back to
// the end follows it again. Each entry is in the middle of a row as wide
// as the pane, so that the wheel scrolls it wherever the pointer is. It
// fades in as the composer docks from the hero; into a hero, the thread
// left fades out, sliding up, out of the pointer's way.
func (a *app) timeline(c *ui.Context, t thread, rs rows) (laid bool) {
	tr := transcriptK(a.dockK)
	from, leaving := a.departing(t)
	if leaving {
		t, rs = from, a.rowsOf(from)
	}
	es := rs.es
	a.drew, a.drawn = a.drawn, map[api.ID]sight{}
	a.laidAt, a.builtAt = a.builtAt, a.now
	l := a.listFor(t.id())
	// In Zen mode, an item is a row or a line folding rows.
	v := a.viewFor(t.id(), rs)
	a.keepPlace(t.id(), l, v)
	l.Key = func(j int) any { return v.key(j) }
	l.FollowEnd = true
	l.FollowAnimation = &ui.Smooth
	// The agents strip sits over the composer: the end clears it too.
	room := composerRoom + a.agentsRoom(t) + a.queueRoom(t)
	at := v.thinkingPos(thinkingAt(t, rs))
	list := ui.List(c.Key(t.id()), l, v.len(), func(j int) {
		ui.Row(c).Justify(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).MaxWidth(contentWidth).Gap(22).Children(func() {
				if f := v.fold(j); f != nil {
					a.zenLine(c, t, f)
				} else {
					a.entry(c, t, rs, v.row(j))
				}
				if j == at {
					a.thinkingRow(c, t)
				}
			})
		})
	})
	if leaving {
		list.Translate(0, -32*(1-tr)).PassThrough()
	}
	list.Fill().Padding(20, columnRight, room, columnLeft).Gap(22).Justify(ui.End).
		ScrollbarInsets(0, 0, room-40, 0).Opacity(tr).Children(func() {
		// The hero has its own greeting, over its composer.
		if len(es) == 0 && remote(t) != nil {
			ui.Row(c).Justify(ui.Center).Children(func() { a.remoteNotice(c, t) })
		} else if len(es) == 0 && !hero(t) {
			ui.Row(c).Justify(ui.Center).Children(func() {
				// What it says is a plugin's: the built-in Empty thread, or
				// one of the user's that replaces it.
				box := func() ui.Element { return ui.Column(c).Key("empty").Center().Padding(140, 0, 0) }
				a.pluginComponent(c, "thread.empty", &t, componentCopy{key: t.id().String(), box: box, context: func(r plugins.ThreadRecord) any { return threadEmpty{r} }})
			})
		}
	})
	a.turnMarkers(c, t.id(), rs, l, v)
	return !leaving && list.Bounds().H > 0
}

// thinkingAt is the entry under which the transcript says the agent is at
// work, from the moment its run takes a message until the turn ends: the
// transcript's last, above the messages still queued or on their way, or
// the run's own message while the transcript has yet to list it. It is -1
// while the run waits on the user, as the transcript may show before the
// run's state does, or its engine can't be reached to say when it ends.
func thinkingAt(t thread, rs rows) int {
	if _, ok := thinkingLabels[t.View.Run]; !ok || len(rs.asks) > 0 || t.View.Turn != nil && t.View.Turn.Silent || t.env != nil && t.env.Status != client.Status(client.Online{}) {
		return -1
	}
	if t.View.Turn != nil {
		if i := slices.IndexFunc(rs.es[rs.fixed:], func(e entry) bool { return e.key == "m/"+t.View.Turn.Run.String() }); i >= 0 {
			return rs.fixed + i
		}
	}
	return rs.fixed - 1
}

var thinkingLabels = map[api.RunState]string{api.RunStarting: "Thinking…", api.RunRunning: "Thinking…", api.RunStopping: "Stopping…"}

// thinkingRow says the agent is at work: the steps card's working mark and
// shimmering label, without the card, at the card header's inset.
func (a *app) thinkingRow(c *ui.Context, t thread) {
	ui.Row(c).Key("thinking").Gap(6).AlignItems(ui.Center).Padding(0, 13).Transition(riseIn).Children(func() {
		a.statusMark(c, statusWorking)
		shimmerText(c, thinkingLabels[t.View.Run], 12, palette.muted, palette.text)
	})
}

// compactDivider draws a quiet centered divider for an automatic idle
// compaction run.
func (a *app) compactDivider(c *ui.Context, e entry) {
	ui.Row(c).Key(e.key).Gap(10).AlignItems(ui.Center).Padding(4, 12).Transition(riseIn).Children(func() {
		ui.Box(c).Height(1).Grow(1).Background(palette.border)
		if e.working {
			a.statusMark(c, statusWorking)
			shimmerText(c, e.text, 11.5, palette.muted, palette.text)
		} else {
			ui.Text(c, e.text).FontSize(11.5).TextColor(palette.faint).SingleLine()
		}
		ui.Box(c).Height(1).Grow(1).Background(palette.border)
	})
}

// threadEmpty is the context of thread.empty.
type threadEmpty struct {
	Thread plugins.ThreadRecord `json:"thread"`
}

// jumpToLatest floats a pill over the composer while the user has
// scrolled up from the end, which takes them back. It fades and rises in
// as the composer folds, and goes as it opens, riding the composer's top
// edge as it moves.
func (a *app) jumpToLatest(c *ui.Context, t thread) {
	j := a.jumpK
	if j == 0 {
		return
	}
	l := a.listFor(t.id())
	// Over the composer as it is in this frame, which is laid out after.
	bottom := composerBarBottom + a.composerHeight(c, t) + 26
	if hero(t) {
		return // nothing to jump to
	}
	// The open Agents panel floats over where it is, as it fades in with
	// the composer: it gives way to the panel as that comes, taking no
	// presses once mostly gone, and none of the pointer once gone, where
	// it shows past a panel lower than it.
	o := j
	if len(a.shown(t)) > 0 {
		o *= 1 - a.agentsK*fadeIn(a.composerK, 0.45, 0.55)
	}
	ui.Row(c).Absolute().Left(columnLeft).Right(columnRight).Bottom(bottom).Justify(ui.Center).PassThrough().Children(func() {
		b := faded(a.glassButton(c, "jump").Padding(5, 12).Translate(0, 8*(1-j)), o)
		a.cover(coverKey{kind: "jump"}, b)
		if o <= 0.001 {
			b.Invisible()
		}
		if b.Children(func() {
			ui.Icon(c, icArrowDown).FontSize(13)
			ui.Text(c, "Jump to latest").FontSize(12)
		}).Clicked() {
			l.ScrollToEnd()
		}
	})
}

// userMarkdown is a message the user typed as Markdown that shows only
// its code as Markdown would, its fenced code blocks and code spans, and
// the rest as typed: every mark escaped, and every line, blank line and
// indent kept, but for one blank line each side of a code block, which
// the block's spacing shows. A fence that opens nothing, unclosed with
// no code after it, is text, and one left unclosed after code is closed,
// which leaves its code to copy. It is "" for a message without code, which
// shows as plain text: one Markdown span per escaped mark would draw a
// long paste far slower.
func userMarkdown(text string) string {
	text = strings.TrimLeft(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	var blocks, prose []string
	code := false
	flush := func(beforeCode bool) {
		if len(blocks) > 0 && len(prose) > 0 && strings.TrimSpace(prose[0]) == "" {
			prose = prose[1:]
		}
		if beforeCode && len(prose) > 0 && strings.TrimSpace(prose[len(prose)-1]) == "" {
			prose = prose[:len(prose)-1]
		}
		if len(prose) > 0 {
			md, spans := typedProse(strings.Join(prose, "\n"))
			blocks, code = append(blocks, md), code || spans
		}
		prose = nil
	}
	for i := 0; i < len(lines); i++ {
		fence, labeled := openingFence(lines[i])
		if fence == "" {
			prose = append(prose, lines[i])
			continue
		}
		end := i + 1
		for end < len(lines) && !closesFence(lines[end], fence) {
			end++
		}
		if !labeled {
			prose = append(prose, lines[i:min(end+1, len(lines))]...)
			i = end
			continue
		}
		if end == len(lines) && strings.TrimSpace(strings.Join(lines[i+1:], "")) == "" {
			prose = append(prose, lines[i:]...)
			break
		}
		flush(true)
		block := lines[i:min(end+1, len(lines))]
		if end == len(lines) {
			block = append(slices.Clip(block), fence)
		}
		blocks, code = append(blocks, strings.Join(block, "\n")), true
		i = end
	}
	flush(false)
	if !code {
		return ""
	}
	return strings.Join(blocks, "\n")
}

// typedProse is text as one paragraph of Markdown that shows it as typed
// but for its code spans: each line a hard break, its indent entities
// Markdown keeps, and its marks escaped. A last line left blank holds a
// space, as a hard break ending a paragraph shows as a backslash. spans
// reports a code span.
func typedProse(text string) (md string, spans bool) {
	var b strings.Builder
	lineStart := true
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == '\n':
			b.WriteString("\\\n")
			lineStart = true
			i++
			continue
		case lineStart && (c == ' ' || c == '\t'):
			fmt.Fprintf(&b, "&#%d;", c)
			i++
			continue
		case c == '`':
			if end := codeSpanEnd(text, i); end > 0 {
				b.WriteString(text[i:end])
				i, lineStart, spans = end, false, true
				continue
			}
			for i < len(text) && text[i] == '`' {
				b.WriteString("\\`")
				i++
			}
			lineStart = false
			continue
		case c < 0x80 && strings.IndexByte(`!"#$%&'()*+,-./:;<=>?@[\]^_{|}~`, c) >= 0:
			b.WriteByte('\\')
		}
		b.WriteByte(c)
		lineStart = false
		i++
	}
	if strings.HasSuffix(text, "\n") {
		b.WriteString("&#32;")
	}
	return b.String(), spans
}

// maxBackticks is the longest run of backticks that opens a code span in
// MyGo's parser, which reads a longer run as text.
const maxBackticks = 1000

// codeSpanEnd is the end of the code span whose opening backticks start
// at i in text, after the run of as many backticks that closes it, or 0
// when none does on the same line. A span may not cross lines: Markdown
// reads a line's blocks before its spans, so the lines after a span's
// first would read as Markdown, unescaped.
func codeSpanEnd(text string, i int) int {
	n := 0
	for i+n < len(text) && text[i+n] == '`' {
		n++
	}
	if n > maxBackticks {
		return 0
	}
	for j := i + n; j < len(text); {
		if text[j] == '\n' {
			return 0
		}
		if text[j] != '`' {
			j++
			continue
		}
		m := 0
		for j+m < len(text) && text[j+m] == '`' {
			m++
		}
		if m == n {
			return j + m
		}
		j += m
	}
	return 0
}

// openingFence is the run of backticks or tildes, three or more, that
// opens a fenced code block on line, or "" when line opens none. labeled
// reports whether no more than a language's name follows it: a block
// whose fence holds more shows as typed, up to the fence that closes it.
func openingFence(line string) (fence string, labeled bool) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 || t == "" || t[0] != '`' && t[0] != '~' {
		return "", false
	}
	m := t[:len(t)-len(strings.TrimLeft(t, t[:1]))]
	info := strings.TrimSpace(t[len(m):])
	if len(m) < 3 || m[0] == '`' && strings.Contains(info, "`") {
		return "", false
	}
	return m, isLanguage(info)
}

// isLanguage reports whether info, a fence's info string, is empty or a
// language's name alone, which the block shows as its label: the rest of
// what a user types after a fence would show nowhere.
func isLanguage(info string) bool {
	return !strings.ContainsFunc(info, func(r rune) bool {
		return !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune("+#._-", r))
	})
}

// closesFence reports whether line closes the fenced code block fence
// opened.
func closesFence(line, fence string) bool {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 || !strings.HasPrefix(t, fence) {
		return false
	}
	return strings.Trim(strings.TrimLeft(t, fence[:1]), " \t") == ""
}

// userMarkdown is userMarkdown(text) for the message keyed key, made
// again only when its text changes.
func (a *app) userMarkdown(key, text string) string {
	if m, ok := a.userMDs[key]; ok && m.text == text {
		return m.md
	}
	if a.userMDs == nil || len(a.userMDs) >= userMDsKept {
		a.userMDs = map[string]userMD{}
	}
	m := userMD{text, userMarkdown(text)}
	a.userMDs[key] = m
	return m.md
}

// userMDsKept is how many messages' Markdown userMarkdown keeps.
const userMDsKept = 512

// userMD is a message's text and the Markdown it shows as.
type userMD struct{ text, md string }

// entry draws the timeline row rs.es[i]. A message, a run's work log or an
// error is a copy of timeline.message, which a plugin draws around
// transcript.body: the row's own content, as Glitched draws it. Glitched
// draws a message's actions over every copy, shown while it is pointed
// at. Approval, question and report cards are Glitched's alone.
func (a *app) entry(c *ui.Context, t thread, rs rows, i int) {
	e := rs.es[i]
	switch e.kind {
	case entryRequest:
		a.requestCard(c, t, e)
		return
	case entryReport:
		a.reportCard(c, t, e.key, e)
		return
	case entryCompact:
		a.compactDivider(c, e)
		return
	case entryFork:
		a.forkDivider(c, t, rs.es[i])
		return
	}
	m := timelineMessage{Key: e.key, Kind: entryKindNames[e.kind], Streaming: e.streaming, Note: e.note, Failed: e.failed.Err != nil, At: e.at, Working: e.working, Paused: e.paused, Took: e.took, Work: timelineWork(e.work), Plan: e.plan}
	acted := false
	a.pluginComponent(c, "timeline.message", &t, componentCopy{
		key: e.key,
		box: func() ui.Element {
			return ui.Column(c).Key(e.key).Transition(riseIn)
		},
		context: func(plugins.ThreadRecord) any { return m },
		native:  func(body *widget.Node) { acted = a.transcriptBody(c, t, rs, i, body) },
		retry: func() {
			if e.failed.Err != nil {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(0, 0, 0, 8).Children(func() { a.failedActions(c, t, e.failed) })
			}
		},
		over: func(box ui.Element) {
			if !acted && e.kind == entryUser && e.failed.Err == nil && !t.id().IsZero() {
				a.messageActions(c, t, rs.es, i, box.Hovered())
			}
		},
		builtin: func() *widget.Node { return timelineRow(m) },
	})
}

var (
	timelineBody  = &widget.Node{Type: "native", ID: "body", Name: "transcript.body", Props: bare}
	timelineRetry = &widget.Node{Type: "native", ID: "retry", Name: "transcript.retry"}
	bare          = map[string]json.RawMessage{"bare": json.RawMessage("true")}
)

// timelineRow is the built-in Timeline rows plugin's drawing of the row m,
// as its main.ts draws it. Glitched draws the same while no plugin has
// drawn the row, so that the body keeps its state, as a selection, when
// the plugin's drawing comes.
func timelineRow(m timelineMessage) *widget.Node {
	switch m.Kind {
	case "user":
		words := &widget.Node{Type: "native", ID: "body", Name: "transcript.body", Props: bare, Padding: widget.Spacing{10, 14}}
		bubble := &widget.Node{Type: "column", Corners: []float32{16, 16, 4, 16}, Background: "$surface", Border: "$border", Children: []*widget.Node{words}}
		col := &widget.Node{Type: "column", Align: "end", Gap: 6, MaxWidthPercent: 80, Children: []*widget.Node{bubble}}
		if m.Note != "" {
			note := &widget.Node{Type: "text", Text: m.Note, Size: "small", Color: "$text.faint"}
			if m.Failed {
				note.Color = "$danger"
			}
			col.Children = append(col.Children, &widget.Node{Type: "row", Children: []*widget.Node{note, timelineRetry}})
		}
		return &widget.Node{Type: "row", Children: []*widget.Node{{Type: "spacer"}, col}}
	case "failed", "error":
		row := &widget.Node{Type: "row", Align: "center", Padding: widget.Spacing{6, 6, 6, 14}, Radius: 12, Border: "$danger", BorderOpacity: new(0.35),
			Children: []*widget.Node{{Type: "native", ID: "body", Name: "transcript.body", Props: bare, Grow: true}}}
		if m.Kind == "failed" {
			row.Children = append(row.Children, timelineRetry)
		}
		return row
	}
	return timelineBody
}

// timelineMessage is the context of timeline.message: which row a copy is
// and what it is doing. Its text is transcript.body's alone, so a reply
// streaming in changes no copy's context.
type timelineMessage struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Streaming bool   `json:"streaming,omitempty"`
	Note      string `json:"note,omitempty"`
	Failed    bool   `json:"failed,omitempty"`
	// At is when a message was sent or a reply's words came.
	At time.Time `json:"at,omitzero"`
	// Working, Paused, Took, Work and Plan structure work and plan rows.
	Working bool               `json:"working,omitempty"`
	Paused  string             `json:"paused,omitempty"`
	Took    string             `json:"took,omitempty"`
	Work    []timelineWorkStep `json:"work,omitempty"`
	Plan    []api.PlanStep     `json:"plan,omitempty"`
}

// timelineWorkStep is a step of a work row, structured for a plugin's own
// work log. Its output is included only while the row shows it: a failed
// command's.
type timelineWorkStep struct {
	Item     string    `json:"item"`
	Kind     string    `json:"kind"`
	Title    string    `json:"title"`
	Status   string    `json:"status"`
	Path     string    `json:"path,omitempty"`
	Exit     *int      `json:"exit,omitempty"`
	Output   string    `json:"output,omitempty"`
	Started  time.Time `json:"started,omitzero"`
	Ended    time.Time `json:"ended,omitzero"`
	Subagent bool      `json:"subagent,omitempty"`
	Steps    int       `json:"steps,omitempty"`
}

func timelineWork(ws []client.WorkStep) []timelineWorkStep {
	out := make([]timelineWorkStep, 0, len(ws))
	for _, w := range ws {
		steps := 0
		if w.Subagent != nil {
			steps = w.Subagent.Steps()
		}
		out = append(out, timelineWorkStep{
			Item: w.Item, Kind: w.Kind, Title: w.Title, Status: string(w.Status), Path: w.Path,
			Exit: w.Exit, Output: w.Output, Started: w.Started, Ended: w.Ended,
			Subagent: w.Subagent != nil, Steps: steps,
		})
	}
	return out
}

var entryKindNames = map[entryKind]string{entryUser: "user", entryAssistant: "assistant", entryWork: "work", entryPlan: "plan", entryFailed: "failed", entryError: "error"}

// transcriptBody draws the timeline row rs.es[i], which body,
// transcript.body, places. Bare, it draws only what the row says, with
// nothing around it: a message's words, inset by body's padding so that
// it selects with them, a run's work log, an error's words; the copy
// draws the rest, and transcript.retry a failed command's Retry and
// Dismiss. Otherwise it draws the row as Glitched did before the built-in
// drew its chrome: a message in its bubble with its note, an error in its
// outline with Retry and Dismiss. It draws a message's actions, shown
// while it is pointed at, under it, and says whether it did: a bare
// message of yours has them under its copy instead.
func (a *app) transcriptBody(c *ui.Context, t thread, rs rows, i int, body *widget.Node) bool {
	e := rs.es[i]
	var bare bool
	if body != nil {
		json.Unmarshal(body.Props["bare"], &bare)
	}
	inset := func(el ui.Element) ui.Element {
		if bare && len(body.Padding) > 0 {
			t, r, b, l := body.Padding.Sides()
			el.Padding(t, r, b, l)
		}
		return el
	}
	switch e.kind {
	case entryUser:
		words := func() ui.Element {
			if md := a.userMarkdown(e.key, e.text); md != "" {
				return inset(markdown.View(c, md, &a.userMD))
			}
			return inset(ui.Text(c, e.text).Selectable().LineHeight(1.5))
		}
		if bare {
			words()
			return false
		}
		r := ui.Row(c).Key(e.key)
		r.Children(func() {
			ui.Spacer(c)
			ui.Column(c).AlignItems(ui.End).Gap(6).MaxWidthPercent(80).Children(func() {
				words().Padding(10, 14).Radius(16, 16, 4, 16).Background(palette.surface).Border(1, palette.border)
				if note := e.note; note != "" {
					e.note = note
					a.messageNote(c, t, e)
				}
			})
			if e.failed.Err == nil && !t.id().IsZero() {
				a.messageActions(c, t, rs.es, i, r.Hovered())
			}
		})
	case entryAssistant:
		col := inset(ui.Column(c).Key(e.key).LineHeight(1.55))
		col.Children(func() {
			var md ui.Element
			if e.streaming {
				md = markdown.Stream(c, e.text, &a.md)
			} else {
				md = markdown.View(c, e.text, &a.md)
			}
			a.followSelection(c, t, e.key, e.text, col, md)
			a.messageActions(c, t, rs.es, i, col.Hovered() && !e.streaming)
		})
	case entryWork:
		a.workLog(c, t, e.key, e, rs.files)
	case entryPlan:
		a.planCard(c, t, e)
	case entryFailed, entryError:
		if bare {
			inset(ui.Text(c, e.text).FontSize(12).TextColor(palette.red))
			return false
		}
		ui.Row(c).Key(e.key).Gap(8).AlignItems(ui.Center).Padding(6, 6, 6, 14).Radius(12).Border(1, palette.red.Alpha(0.35)).Children(func() {
			ui.Text(c, e.text).FontSize(12).TextColor(palette.red).Grow(1).Shrink(1)
			if e.kind == entryFailed {
				a.failedActions(c, t, e.failed)
			}
		})
	}
	return e.kind == entryUser || e.kind == entryAssistant
}

// messageNote is under a message that hasn't simply been sent: queued,
// cancelled, or not sent.
func (a *app) messageNote(c *ui.Context, t thread, e entry) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		col := palette.faint
		if e.failed.Err != nil {
			col = palette.red
		}
		ui.Text(c, e.note).FontSize(11.5).TextColor(col)
		if e.failed.Err != nil {
			a.failedActions(c, t, e.failed)
		}
	})
}

// failedActions are a failed command's buttons: Retry, unless sending it
// again can only fail the same way, and Dismiss. Retry waits while what
// refused the command holds, as a live run does a delete.
func (a *app) failedActions(c *ui.Context, t thread, p client.Pending) {
	_, del := p.Body.(api.ThreadDelete)
	if !p.Terminal() && ghost(c, "retry", false).Padding(2, 8).Disabled(del && t.View.Live()).Children(func() {
		ui.Icon(c, icRotate).FontSize(12)
		ui.Text(c, "Retry").FontSize(11.5)
	}).Clicked() {
		a.backend.Retry(t.env, t.id(), p.Command)
	}
	if ghost(c, "dismiss", false).Padding(2, 8).Children(func() {
		ui.Icon(c, icX).FontSize(12)
		ui.Text(c, "Dismiss").FontSize(11.5)
	}).Clicked() {
		a.backend.Dismiss(t.env, t.id(), p.Command)
	}
}

func (a *app) workLog(c *ui.Context, t thread, key string, e entry, files []fileChange) {
	working := e.working
	open := !a.collapsed[key]
	ui.Column(c).Key(key).Radius(12).Border(1, palette.border).Background(palette.bg.Alpha(0.6)).Clip().
		Children(func() {
			ui.Row(c).Key("bar").AlignItems(ui.Center).Children(func() {
				if ghost(c, "head", false).Radius(0).Padding(8, 12).Grow(1).Children(func() {
					switch {
					case e.paused != "":
						ui.Icon(c, icClock).FontSize(12).TextColor(palette.amber)
						ui.Text(c, e.paused).FontSize(12).Grow(1)
					case working:
						a.statusMark(c, statusWorking)
						shimmerText(c, e.took, 12, palette.muted, palette.text)
					default:
						ui.Text(c, e.took).FontSize(12).Grow(1)
					}
					ui.Text(c, plural(len(e.work), "step")).FontSize(11).TextColor(palette.faint)
					chevron(c, open)
				}).Clicked() {
					a.collapsed[key] = open
				}
			})
			if !open {
				return
			}
			ui.Column(c).Key("steps").Padding(6, 8, 10).Gap(2).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).
				ClipY().Transition(fold).Children(func() {
				for _, w := range e.work {
					// Each step the agent takes unfolds below the last.
					a.step(c, t, files, e.run, w)
				}
			})
		})
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
		r = ghost(c, w.Item, false).Padding(2, 4).Radius(5).Gap(8).ClipY().Transition(fold)
	} else {
		r = ui.Row(c).Key(w.Item).Padding(2, 4).Radius(5).Gap(8).AlignItems(ui.Center).ClipY().Transition(fold)
	}
	if r.Children(func() {
		ui.Icon(c, icon).FontSize(12).TextColor(palette.faint)
		// An edit's path reads from the workspace and gives way at its
		// start, so the file's name shows.
		if verb, ok := strings.CutSuffix(w.Title, " "+w.Path); ok && w.Path != "" {
			path := shownPath(t.View.Workspace, w.Path)
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
	if w.Output != "" && (w.Status == api.WorkFailed || w.Exit != nil && *w.Exit != 0) {
		ui.Text(c, w.Output).Key(w.Item+"/output").Selectable().FontSize(11.5).Font("monospace").LineHeight(1.5).TextColor(palette.muted).
			Margin(2, 0, 4, 24).Padding(6, 10).Radius(6).Background(palette.bg).Border(1, palette.border)
	}
}

// tookFor is how long a step took, to a tenth of a second.
func tookFor(d time.Duration) string {
	return max(d, 0).Round(100 * time.Millisecond).String()
}

// plural counts things: "1 step", "2 steps".
func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func counts(c *ui.Context, add, del int) {
	if add > 0 {
		ui.Textf(c, "+%d", add).FontSize(11.5).Font("monospace").TextColor(palette.green)
	}
	if del > 0 {
		ui.Textf(c, "−%d", del).FontSize(11.5).Font("monospace").TextColor(palette.red)
	}
}
