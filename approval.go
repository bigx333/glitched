package main

import (
	"encoding/json"
	"strings"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plain"
)

// ask is what a request's body asks, in the words the providers use:
// {"operation":"edit","path":"main.go"}, or a command to run where, and
// why. Args are what an allow approves of a tool the provider has no
// words for, and Unsandboxed a command that runs outside the sandbox.
type ask struct {
	Operation   string `json:"operation"`
	Path        string `json:"path"`
	Command     string `json:"command"`
	Cwd         string `json:"cwd"`
	Reason      string `json:"reason"`
	Args        string `json:"args"`
	Unsandboxed bool   `json:"unsandboxed"`
}

func askOf(body json.RawMessage) (ask, bool) {
	var b ask
	return b, json.Unmarshal(body, &b) == nil && (b.Operation != "" || b.Command != "")
}

// agentNames are the providers' agents as the card names them.
var agentNames = map[string]string{"claude": "Claude", "codex": "Codex"}

func agentName(t thread) string {
	if n, ok := agentNames[t.View.Model.Provider]; ok {
		return n
	}
	return "The agent"
}

// accessLabel is the thread's access level as the composer's picker
// names it.
func accessLabel(a api.Access) string {
	for label, v := range accessOf {
		if v == a {
			return label
		}
	}
	return ""
}

// answerOf reads an answer sent to the agent: {"allow":true} or not.
func answerOf(raw json.RawMessage) client.Answer {
	var b struct{ Allow bool }
	if json.Unmarshal(raw, &b) != nil || !b.Allow {
		return client.Deny
	}
	return client.AllowOnce
}

var answerTitles = map[client.Answer]string{client.Deny: "Denied", client.AllowOnce: "Allowed"}

// requestCard is the agent's request, in the timeline: what it wants to
// do first, then where and why, with the answers under it while it waits.
// Answered, it keeps the answer; voided, it says so.
func (a *app) requestCard(c *ui.Context, t thread, e entry) {
	r := e.request
	b, known := askOf(r.Body)
	waiting := r.State == api.RequestPending
	// Each string from the agent as the card shows it, and whether any of
	// them has text the card shows as escapes.
	escaped := false
	shown := func(s string, lines bool) string {
		s, e := plain.Text(s, lines)
		escaped = escaped || e
		return s
	}
	// agent draws a text from the agent, and boxed one that runs, in a
	// box; each returns where the last frame drew its first line.
	agent := func(key, s string, size float32, col ui.Color) (first ui.Rect) {
		box, line := a.verbatim(c, key, s, size, col)
		first = box.Bounds()
		first.H = min(first.H, line)
		return first
	}
	boxed := func(key, s string, size float32) (first ui.Rect) {
		ui.Box(c).Padding(8, 10).Radius(6).Background(palette.bg).Border(1, palette.border).Children(func() {
			first = agent(key, s, size, palette.text)
		})
		return first
	}
	// beside is a label with a text from the agent after it, which wraps
	// under itself; it returns where the last frame drew the two.
	beside := func(label, key, s string, size float32) (at ui.Rect) {
		row := ui.Row(c).Gap(5).AlignItems(ui.Start)
		row.Children(func() {
			ui.Text(c, label).FontSize(12.5).TextColor(palette.muted)
			ui.Box(c).Grow(1).Shrink(1).MinWidth(0).Children(func() { agent(key, s, size, palette.text) })
		})
		return row.Bounds()
	}
	card := ui.Column(c).Key(e.key).Radius(12).Border(1, palette.border).Background(palette.surface).Clip().
		Transition(riseIn)
	card.Children(func() {
		header := ui.Row(c).Padding(9, 14).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border)
		header.Children(func() {
			switch r.State {
			case api.RequestPending:
				pendingMark(c)
				ui.Text(c, "Approval needed").FontSize(13).FontWeight(600)
			case api.RequestInvalidated, api.RequestExpired:
				ui.Icon(c, icArrowRight).FontSize(13).TextColor(palette.muted)
				ui.Text(c, "No longer asked").FontSize(13).FontWeight(600).TextColor(palette.muted)
			default:
				answer := answerOf(r.Answer)
				if answer == client.Deny {
					ui.Icon(c, icX).FontSize(13).TextColor(palette.red)
				} else {
					ui.Icon(c, icCheck).FontSize(13).TextColor(palette.green)
				}
				ui.Text(c, answerTitles[answer]).FontSize(13).FontWeight(600)
			}
			ui.Spacer(c)
			if waiting && !r.Opened.IsZero() {
				ui.Text(c, "waiting "+ago(r.Opened, a.now)).FontSize(11.5).TextColor(palette.faint)
			}
		})
		texts := ui.Column(c)
		seen := sight{header: header.Bounds(), card: card.Bounds()}
		if waiting {
			texts, seen.scroll = a.inView(c, t, r.Request)
			seen.texts = texts.Bounds()
		}
		// lead is what the card shows first, from its label to the first
		// line of what runs: an allow needs it drawn.
		lead := func(label, first ui.Rect) { seen.lead = union(label, first) }
		texts.Padding(12, 14, 12).Gap(8).Children(func() {
			switch {
			case !known:
				label := ui.Text(c, agentName(t)+" asks:").FontSize(12.5).TextColor(palette.muted)
				body, more := a.cut(r.Request, "body", shown(string(r.Body), false), cutChars)
				lead(label.Bounds(), boxed("body", body, 12))
				more(c)
			case b.Command != "":
				label := ui.Text(c, agentName(t)+" wants to run a command").FontSize(12.5).TextColor(palette.muted)
				command, more := a.cut(r.Request, "command", shown(b.Command, true), cutChars)
				lead(label.Bounds(), boxed("command", command, 12.5))
				more(c)
				if b.Unsandboxed {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, icShieldAlert).FontSize(13).TextColor(palette.amber)
						ui.Text(c, "Runs outside the sandbox").FontSize(12.5).FontWeight(500).TextColor(palette.amber)
					})
				}
				if b.Cwd != "" {
					cwd, more := a.cut(r.Request, "cwd", shown(b.Cwd, false), cutPathChars)
					beside("in", "cwd", cwd, 12)
					more(c)
				}
			default:
				label := beside(agentName(t)+" wants to", "operation", shown(b.Operation, false), 12.5)
				path, morePath := a.cut(r.Request, "path", shown(b.Path, false), cutPathChars)
				switch {
				case b.Args != "":
					args, more := a.cut(r.Request, "args", shown(b.Args, true), cutChars)
					lead(label, boxed("args", args, 12))
					more(c)
					if b.Path != "" {
						beside("on", "path", path, 12)
						morePath(c)
					}
				case b.Path != "":
					lead(label, agent("path", path, 12, palette.text))
					morePath(c)
				default:
					lead(label, label)
				}
			}
			if b.Reason != "" {
				reason, more := a.cut(r.Request, "reason", shown(b.Reason, true), cutChars)
				agent("reason", reason, 12, palette.muted)
				more(c)
			}
			if r.Reason != "" && !waiting {
				agent("why", shown(r.Reason, true), 12, palette.faint)
			}
			if escaped {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icShieldAlert).FontSize(13).TextColor(palette.amber)
					ui.Text(c, escapes).FontSize(12.5).FontWeight(500).TextColor(palette.amber)
				})
			}
		})
		if waiting {
			a.drawn[r.Request] = seen
		}
		if waiting || a.zapping(r) {
			a.requestAnswers(c, t, r)
		}
	})
}

// escapes is the card's flag on a request with text it shows as escapes.
const escapes = `Shown with escapes: \u{…} is text outside printable ASCII, \\u{ a literal \u{`

// cutLines and cutChars bound each text from the agent a card shows until
// asked for all of it. The timeline keeps to its end, so a taller card
// would push its top, and what an allow approves first, out of view above
// Allow once. A path in the card's first sentence gets cutPathChars, so
// the command under it stays in view.
const cutLines, cutChars, cutPathChars = 8, 600, 100

// cardRoom is what a waiting card's texts leave of the timeline's pane
// besides the composer's room: the timeline's top padding, and the card's
// header and answers. cardTextsMin is the least the texts get.
const cardRoom, cardTextsMin = 100, 64

// inView holds a waiting card's texts, which scroll in it once they are
// taller than the timeline leaves them, so that the card's top and its
// answers fit in view together. They open at their start, and keep where
// the user scrolled them in the card's sight while the timeline shows it.
func (a *app) inView(c *ui.Context, t thread, request api.ID) (ui.Element, *ui.ScrollState) {
	st := a.drew[request].scroll
	if st == nil {
		st = &ui.ScrollState{}
	}
	texts := ui.Scroll(c).Key("texts").TrackScroll(st)
	if a.paneAt.H > 0 {
		texts.MaxHeight(max(cardTextsMin, a.paneAt.H-composerRoom-a.agentsRoom(t)-cardRoom))
	}
	return texts, st
}

// sight is where the last frame drew a waiting card: its header, the card,
// what it shows first (requestCard's lead), and its texts' box, which
// clips that once scrolled, with where they are scrolled.
type sight struct {
	header, card, lead, texts ui.Rect
	scroll                    *ui.ScrollState
}

// inFull is the part of the chat's pane the timeline shows in full: above
// the composer and the edge it fades out under.
func (a *app) inFull(t thread) ui.Rect {
	v := a.paneAt
	v.H -= composerRoom + a.agentsRoom(t)
	return v
}

// seen is whether the last frame drew the card of the waiting request in
// full where it starts: its header, and its texts from their label to the
// first line of what runs, unscrolled out of their box. An answer to an
// unseen card would approve what the user never read.
func (a *app) seen(t thread, request api.ID) bool {
	s, ok := a.drawn[request]
	v := a.inFull(t)
	return ok && within(s.header, v) && within(s.lead, v) && within(s.lead, s.texts)
}

// reveal brings the start of the waiting request's card into view: its
// texts back to their start, and the card to the top of the timeline when
// the last frame did not draw it in full.
func (a *app) reveal(t thread, es []entry, request api.ID) {
	s, ok := a.drawn[request]
	if ok && s.scroll != nil {
		s.scroll.Y = 0
	}
	if ok && within(s.card, a.inFull(t)) {
		return
	}
	for i, e := range es {
		if e.request != nil && e.request.Request == request {
			a.listFor(t.id()).ScrollTo(i, ui.Start)
		}
	}
}

// within is whether r, drawn, lies inside v.
func within(r, v ui.Rect) bool {
	const slack = 0.5
	return r.W > 0 && r.H > 0 && r.X >= v.X-slack && r.Y >= v.Y-slack &&
		r.X+r.W <= v.X+v.W+slack && r.Y+r.H <= v.Y+v.H+slack
}

// union is the least rect that holds r and q.
func union(r, q ui.Rect) ui.Rect {
	if r.W <= 0 || r.H <= 0 || q.W <= 0 || q.H <= 0 {
		return ui.Rect{}
	}
	x, y := min(r.X, q.X), min(r.Y, q.Y)
	return ui.Rect{X: x, Y: y, W: max(r.X+r.W, q.X+q.W) - x, H: max(r.Y+r.H, q.Y+q.H) - y}
}

// cardText is a text on a request's card, by the field it shows.
type cardText struct {
	request api.ID
	field   string
}

// cut is a text on a request's card as the card shows it: its first lines
// and chars characters unless asked for all of it, and the toggle between
// the two, which says how much more all is and draws nothing when there
// is no more.
func (a *app) cut(request api.ID, field, text string, chars int) (string, func(*ui.Context)) {
	key := cardText{request, field}
	open := a.shownAll[key]
	shown, cut := text, false
	if !open {
		shown, cut = clipped(text, cutLines, chars)
	}
	return shown, func(c *ui.Context) {
		if !cut && !open {
			return
		}
		label, icon := "Show less", icChevronUp
		if !open {
			label, icon = "Show all · "+more(text, shown), icChevronD
		}
		if ghost(c, field, false).Padding(2, 6).Children(func() {
			ui.Icon(c, icon).FontSize(12)
			ui.Text(c, label).FontSize(12)
		}).Clicked() {
			a.shownAll[key] = !open
		}
	}
}

// clipped is s cut to its first lines lines and chars characters, and
// whether that cut anything.
func clipped(s string, lines, chars int) (string, bool) {
	out := s
	if ls := strings.SplitN(s, "\n", lines+1); len(ls) > lines {
		out = strings.Join(ls[:lines], "\n")
	}
	if r := []rune(out); len(r) > chars {
		out = string(r[:chars])
	}
	if out == s {
		return s, false
	}
	return out + "…", true
}

// more is how much of all a cut of it leaves out: the lines it hides, or
// the characters where it cuts into the last line it shows.
func more(all, cut string) string {
	if n := strings.Count(all, "\n") - strings.Count(cut, "\n"); n > 0 {
		return plural(n, "more line")
	}
	return plural(len([]rune(all))-len([]rune(cut))+1, "more character")
}

// requestAnswers are a waiting request's answers, Allow once and Deny; or
// the answer on its way, or why it wasn't sent. No provider yet lets a
// rule through without asking again, so there is no Always allow. Just
// answered, they break up (zap.go).
func (a *app) requestAnswers(c *ui.Context, t thread, r *client.RequestEntry) {
	sent, sending := responding(t, r.Request)
	if a.zapping(r) {
		if !sending || sent.Err == nil {
			a.zapRow(c)
			return
		}
		a.zap = approvalZap{} // not sent: the row says why
	}
	switch {
	case sending && sent.Err != nil:
		ui.Row(c).Key("decide").Padding(0, 14, 12).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Text(c, "Not sent: "+sent.Err.Error()).FontSize(12).TextColor(palette.red).Grow(1).Shrink(1)
			a.failedActions(c, t, sent)
		})
		return
	case sending:
		ui.Row(c).Key("decide").Padding(0, 14, 12).Children(func() {
			ui.Text(c, "Sending…").FontSize(12).TextColor(palette.muted)
		})
		return
	}
	allow, deny, row, labels := a.answerButtons(c, t, r)
	a.answerAt = answerRow{r.Request, row.Bounds(), allow.Bounds(), deny.Bounds(), labels}
	if allow.Clicked() {
		if a.seen(t, r.Request) {
			a.answer(c, t, r, client.AllowOnce)
		} else {
			a.reveal(t, entriesOf(t), r.Request)
		}
	}
	if deny.Clicked() {
		a.answer(c, t, r, client.Deny)
	}
}

// answerButtons lays out the row of a request's answers, and returns its
// buttons, the row, and its labels as they were laid out in the last
// frame, for a zap to paint as it breaks the row up.
func (a *app) answerButtons(c *ui.Context, t thread, r *client.RequestEntry) (allow, deny, row ui.Element, labels []zapLabel) {
	// Each label where the last frame laid it out, read as it's built.
	text := func(s string, size float32, weight int, col ui.Color) ui.Element {
		e := ui.Text(c, s).FontSize(size).TextColor(col)
		if weight != 0 {
			e.FontWeight(weight)
		}
		labels = append(labels, zapLabel{r: e.Bounds(), span: ui.Span{Text: s, Size: size, Weight: weight, Color: col}})
		return e
	}
	// A key's chip, as kbd draws one.
	chip := func(s string, col, bg, border ui.Color) ui.Element {
		e := text(s, 10.5, 0, col).Padding(1, 4).Radius(4).Background(bg)
		if border != (ui.Color{}) {
			e.Border(1, border)
		}
		l := &labels[len(labels)-1]
		l.pad, l.bg, l.border = 4, bg, border
		return e
	}
	row = ui.Row(c).Key("decide").Padding(0, 14, 12).Gap(8).AlignItems(ui.Center).Wrap()
	row.Children(func() {
		allow = filled(c, "allow", palette.accent, ui.Hex("#ffffff")).Children(func() {
			text("Allow once", 12.5, 500, ui.Hex("#ffffff"))
			if a.keyed == r.Request {
				chip("⌘⏎", ui.Hex("#ffffff").Alpha(0.9), ui.Hex("#ffffff").Alpha(0.2), ui.Color{})
			}
		})
		deny = ghost(c, "deny", false).TextColor(palette.red).Padding(5, 10).Children(func() {
			text("Deny", 12.5, 0, palette.red)
			if a.keyed == r.Request {
				chip("⇧⌘D", palette.muted, palette.hover, palette.border).MinWidth(18).TextAlign(ui.Center)
			}
		})
		ui.Spacer(c)
		ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icHand).FontSize(12).TextColor(palette.faint)
			text(accessLabel(t.View.Access), 11.5, 0, palette.faint)
		})
	})
	return allow, deny, row, labels
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

// waitingRequest is the request the thread waits on that no answer from
// here is on its way to: the last one the timeline shows waiting.
func waitingRequest(t thread, es []entry) *client.RequestEntry {
	for i := len(es) - 1; i >= 0; i-- {
		if r := es[i].request; r != nil && r.State == api.RequestPending {
			if _, sending := responding(t, r.Request); sending {
				return nil
			}
			return r
		}
	}
	return nil
}

// keyedRequest is the waiting request ⌘⏎ and ⇧⌘D answer, with no answer
// from here on its way: the last one the window draws in full, and else
// the last one, which they bring into view; and whether the window draws
// it.
func (a *app) keyedRequest(t thread, es []entry) (*client.RequestEntry, bool) {
	var last *client.RequestEntry
	for i := len(es) - 1; i >= 0; i-- {
		r := es[i].request
		if r == nil || r.State != api.RequestPending {
			continue
		}
		if _, sending := responding(t, r.Request); sending {
			continue
		}
		if a.seen(t, r.Request) {
			return r, true
		}
		if last == nil {
			last = r
		}
	}
	return last, false
}

// approvalKeys answer a waiting request from the keyboard while the
// composer holds nothing: ⌘⏎ allows it once, as it would send a draft,
// and ⇧⌘D denies it. Each answers only a card the window draws in full,
// and its answers show the keys; else they bring the card into view. With
// a draft, ⌘⏎ sends it, which steers the agent.
func (a *app) approvalKeys(c *ui.Context, t thread, es []entry) {
	r, seen := a.keyedRequest(t, es)
	a.keyed = api.ID{}
	if r != nil {
		a.keyed = r.Request
	}
	if r == nil || strings.TrimSpace(a.draft) != "" || len(a.attachments) > 0 {
		return
	}
	answer := client.AllowOnce
	switch {
	case c.Shortcut(ui.Cmd, ui.KeyEnter):
	case c.Shortcut(ui.Cmd|ui.Shift, ui.KeyD):
		answer = client.Deny
	default:
		return
	}
	if seen {
		a.answer(c, t, r, answer)
	} else {
		a.reveal(t, es, r.Request)
	}
}

// responding is the answer to the request sent from here and not landed,
// if there is one.
func responding(t thread, request api.ID) (client.Pending, bool) {
	for _, p := range t.Pending {
		if r, ok := p.Body.(api.RequestRespond); ok && r.Request == request {
			return p, true
		}
	}
	return client.Pending{}, false
}
