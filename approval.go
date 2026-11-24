package main

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	// Always is that the agent offers to allow such calls from now on.
	Always bool `json:"always"`
	// Session is that the agent offers to allow such calls until its
	// session ends.
	Session bool `json:"session"`
}

func askOf(body json.RawMessage) (ask, bool) {
	var b ask
	return b, json.Unmarshal(body, &b) == nil && (b.Operation != "" || b.Command != "")
}

// agentName is the thread's agent as its cards name it: its provider's
// family, else the provider's own name for it, capitalised.
func agentName(t thread) string {
	p := t.View.Model.Provider
	if i := slices.IndexFunc(families, func(f family) bool { return f.provider == p }); i >= 0 {
		return families[i].name
	}
	if p == "" {
		return "The agent"
	}
	r, n := utf8.DecodeRuneInString(p)
	return string(unicode.ToUpper(r)) + p[n:]
}

// accessLabel is an access level as the composer's picker names it.
func accessLabel(a api.Access) string {
	for label, v := range accessOf {
		if v == a {
			return label
		}
	}
	return ""
}

// askedAt is the access level r is asked at, which its card names: the
// one its turn was admitted at, where that is the thread's live turn, or
// what the turn's provider confirmed it runs instead. The thread's own,
// which a configure since may have changed, is its next turn's; it names a
// request with no live turn of its own.
func askedAt(t thread, r *client.RequestEntry) string {
	if turn := t.View.Turn; turn != nil && turn.Run == r.Run {
		if g := t.View.Confirmed.Access; g != nil && g.Run == turn.Run && g.Short() {
			return shortAccessName(*g)
		}
		return accessLabel(turn.Access)
	}
	return accessLabel(t.View.Access)
}

// answerOf reads an answer sent to the agent: {"allow":true} or not.
func answerOf(raw json.RawMessage) client.Answer {
	var b struct{ Allow, Always, Session bool }
	switch {
	case json.Unmarshal(raw, &b) != nil || !b.Allow:
		return client.Deny
	case b.Always:
		return client.AllowAlways
	case b.Session:
		return client.AllowSession
	}
	return client.AllowOnce
}

var answerTitles = map[client.Answer]string{client.Deny: "Denied", client.AllowOnce: "Allowed", client.AllowAlways: "Always allowed", client.AllowSession: "Allowed for this session"}

// requestCard is the agent's request, in the timeline: what it wants to
// do first, then where and why, with the answers under it while it waits.
// Answered, it keeps the answer; voided, it says so.
func (a *app) requestCard(c *ui.Context, t thread, e entry) {
	r := e.request
	switch r.Kind {
	case api.KindQuestion:
		a.questionCard(c, t, e)
		return
	case api.KindCredentials:
		a.credentialsCard(c, t, e)
		return
	}
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
	// Each text the card shows, with where the last frame drew its lines.
	var read []shownText
	// agent draws a text from the agent, and boxed one that runs, in a
	// box; each returns where the last frame drew its first line.
	agent := func(key, s string, size float32, col ui.Color) ui.Rect {
		lines := a.verbatim(c, key, s, size, col)
		read = append(read, shownText{key, s, lines})
		if len(lines) == 0 {
			return ui.Rect{}
		}
		return lines[0].at
	}
	// flag says in a line of the card's own what the agent's texts don't.
	flag := func(key, s string, col ui.Color) {
		row := ui.Row(c).Gap(6).AlignItems(ui.Center)
		row.Children(func() {
			ui.Icon(c, icShieldAlert).FontSize(13).TextColor(col)
			ui.Text(c, s).FontSize(12.5).FontWeight(500).TextColor(col)
		})
		read = append(read, shownText{key, s, []textLine{{row.Bounds(), 0, len([]rune(s))}}})
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
				a.pendingMark(c)
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
			if rd := a.reads[r.Request]; waiting && rd != nil && rd.below > 0 {
				markBelow(c, rd.below)
			}
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
		// lead is the first line of what runs: an answer needs it drawn.
		lead := func(first ui.Rect) { seen.lead = first }
		texts.Padding(textsPad, 14, 12).Gap(8).Children(func() {
			switch {
			case !known:
				ui.Text(c, agentName(t)+" asks:").FontSize(12.5).TextColor(palette.muted)
				body, more := a.cut(r.Request, "body", shown(string(r.Body), false), cutChars)
				lead(boxed("body", body, 12))
				more(c)
			case b.Command != "":
				ui.Text(c, agentName(t)+" wants to run a command").FontSize(12.5).TextColor(palette.muted)
				command, more := a.cut(r.Request, "command", shown(b.Command, true), cutChars)
				lead(boxed("command", command, 12.5))
				more(c)
				if b.Unsandboxed {
					flag("unsandboxed", "Runs outside the sandbox", palette.amber)
				}
				if b.Cwd != "" {
					cwd, more := a.cut(r.Request, "cwd", shown(b.Cwd, false), cutPathChars)
					beside("in", "cwd", cwd, 12)
					more(c)
				}
			default:
				operation, moreOperation := a.cut(r.Request, "operation", shown(b.Operation, false), cutNameChars)
				label := beside(agentName(t)+" wants to", "operation", operation, 12.5)
				moreOperation(c)
				path, morePath := a.cut(r.Request, "path", shown(b.Path, false), cutPathChars)
				switch {
				case b.Args != "":
					args, more := a.cut(r.Request, "args", shown(b.Args, true), cutChars)
					lead(boxed("args", args, 12))
					more(c)
					if b.Path != "" {
						beside("on", "path", path, 12)
						morePath(c)
					}
				case b.Path != "":
					lead(agent("path", path, 12, palette.text))
					morePath(c)
				default:
					lead(label)
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
				flag("escapes", escapes, palette.amber)
			}
		})
		if waiting {
			a.read(t, r.Request, seen, read)
			a.drawn[r.Request] = seen
		} else {
			delete(a.reads, r.Request)
		}
		if waiting || a.zapping(r) {
			a.requestAnswers(c, t, r)
		}
	})
}

// escapes is the flag on a request with text its card, and its
// notification, show as escapes.
const escapes = `Shown with escapes: \u{…} is text outside printable ASCII, \\u{ a literal \u{`

// asciiNotice says a card shows some of the agent's text as escapes.
func asciiNotice(c *ui.Context) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icShieldAlert).FontSize(13).TextColor(palette.amber)
		ui.Text(c, escapes).FontSize(12.5).FontWeight(500).TextColor(palette.amber)
	})
}

// cutLines and cutChars bound each text from the agent a card shows until
// asked for all of it. The timeline keeps to its end, so a taller card
// would push its top, and what an allow approves first, out of view above
// Allow once. A path in the card's first sentence gets cutPathChars, and a
// tool's name cutNameChars, so the command or input under it stays in
// view in the smallest window.
const cutLines, cutChars, cutPathChars, cutNameChars = 8, 600, 100, 60

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

// markBelow says, in a waiting card's header, how many lines of its texts
// their box hides below: in the card's own words and font, where no text
// from the agent shows, so that none passes for it.
func markBelow(c *ui.Context, below int) {
	ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icArrowDown).FontSize(12).TextColor(palette.accent)
		ui.Text(c, plural(below, "more line")+" below").FontSize(11.5).FontWeight(500).TextColor(palette.accent)
	})
}

// sight is where the last frame drew a waiting card: its header, the card,
// the first line of what runs (requestCard's lead) or of the questions,
// and its texts' box, which clips that once scrolled, with where they are
// scrolled. A question card's texts are the card, which scrolls nothing.
type sight struct {
	header, card, lead, texts ui.Rect
	scroll                    *ui.ScrollState
}

// shownText is a text a waiting card shows, by its key, and where the last
// frame drew its lines.
type shownText struct {
	key, text string
	lines     []textLine
}

// reading is what the window showed of a waiting request's card: each of
// its texts, with which of their runes a frame drew in full and in view
// since its texts' box took its size and the card said what it says; when
// a frame first drew the card's start in view (seen), zero until one did;
// when a frame last drew it moved, grown, scrolled or saying something
// else since the frame before; and what the last frame drew. next and
// last are where the last frame drew the first and the last line no frame
// drew so, and below is how many lines it drew below the texts' box.
type reading struct {
	texts       map[string]*readText
	born, moved time.Time
	was         cardState
	next, last  ui.Rect
	below       int
}

// readText is a text of a card, and which of its runes, and of the place
// after the last, a frame drew in full; left is how many no frame did.
type readText struct {
	text  string
	drawn []bool
	left  int
}

// cardState is a card's place, what it says, and what covers it, as a
// frame drew it.
type cardState struct {
	header, card, lead, texts ui.Rect
	y                         float32
	says                      string
	over                      []ui.Rect
}

// unread is whether the card holds a line no frame drew in full in view.
func (rd *reading) unread() bool {
	for _, t := range rd.texts {
		if t.left > 0 {
			return true
		}
	}
	return false
}

// read keeps what the last frame showed of the waiting request's card,
// drawn where s says with texts.
func (a *app) read(t thread, request api.ID, s sight, texts []shownText) {
	rd := a.reads[request]
	if rd == nil {
		rd = &reading{texts: map[string]*readText{}}
		a.reads[request] = rd
	}
	full := a.inFull(t)
	if rd.born.IsZero() && a.shows(s, full) {
		rd.born, rd.moved = a.now, a.now
	}
	var says strings.Builder
	for _, tx := range texts {
		says.WriteString(tx.key + "\x00" + tx.text + "\x00")
	}
	anew := says.String() != rd.was.says
	if anew || abs32(s.texts.W-rd.was.texts.W) >= 0.5 || abs32(s.texts.H-rd.was.texts.H) >= 0.5 {
		// A card that says something else, or a box of another size that
		// shows its texts otherwise, is read anew.
		rd.texts = map[string]*readText{}
	}
	view := meet(s.texts, full)
	if anew {
		// The last frame drew what the card said before, where it was then.
		view = ui.Rect{}
	}
	shown := map[string]*readText{}
	rd.next, rd.last, rd.below = ui.Rect{}, ui.Rect{}, 0
	for _, tx := range texts {
		r := rd.texts[tx.key]
		if r == nil {
			n := utf8.RuneCountInString(tx.text) + 1
			r = &readText{text: tx.text, drawn: make([]bool, n), left: n}
		}
		shown[tx.key] = r
		for _, l := range tx.lines {
			if l.at.Y+l.at.H > s.texts.Y+s.texts.H+0.5 {
				rd.below++
			}
			if within(l.at, view) && !a.hidden(l.at) {
				for i := l.from; i <= l.to && i < len(r.drawn); i++ {
					if !r.drawn[i] {
						r.drawn[i] = true
						r.left--
					}
				}
			}
			if slices.Contains(r.drawn[l.from:min(l.to+1, len(r.drawn))], false) {
				if rd.next.H == 0 {
					rd.next = l.at
				}
				rd.last = l.at
			}
		}
	}
	rd.texts = shown
	var y float32
	if s.scroll != nil {
		y = s.scroll.Y
	}
	now := cardState{s.header, s.card, s.lead, s.texts, y, says.String(), a.coversOf(s.card)}
	if !now.like(rd.was) && a.laidAt.After(rd.moved) {
		rd.moved = a.laidAt
	}
	rd.was = now
}

// like is whether a card in state s looks as it did in state w, a frame
// before: the same texts, under the same covers, and every place within
// half a DIP of where it was, as a card does that stopped, or creeps to
// where it stops.
func (s cardState) like(w cardState) bool {
	near := func(a, b ui.Rect) bool {
		return abs32(a.X-b.X) < 0.5 && abs32(a.Y-b.Y) < 0.5 && abs32(a.W-b.W) < 0.5 && abs32(a.H-b.H) < 0.5
	}
	return s.says == w.says && abs32(s.y-w.y) < 0.5 && near(s.header, w.header) && near(s.card, w.card) &&
		near(s.lead, w.lead) && near(s.texts, w.texts) && slices.EqualFunc(s.over, w.over, near)
}

// meet is the part of r inside v.
func meet(r, v ui.Rect) ui.Rect {
	x, y := max(r.X, v.X), max(r.Y, v.Y)
	w, h := min(r.X+r.W, v.X+v.W)-x, min(r.Y+r.H, v.Y+v.H)-y
	if w <= 0 || h <= 0 {
		return ui.Rect{}
	}
	return ui.Rect{X: x, Y: y, W: w, H: h}
}

// dwell is how long a waiting card stands still, its place, size, scroll,
// texts and what covers it as they were, before a press answers it: a
// press as a card lands, moves or comes out from under a cover answers
// what the user had no time to read.
const dwell = 300 * time.Millisecond

// press is a press of an answer to the waiting request that began at
// began, by key or on a button. It brings a card whose start no frame
// drew in view into view. It answers only a card that stood still since
// dwell before the press began, and a key only the keyed card, which
// showed its start and its keys before ⌘ went down, so that a key held,
// repeating, answers no card that came after the one it answered, nor
// one keyed after it. Deny on its button answers then, as the
// user looks at it; else it answers a card that drew every line it holds,
// and whose start the window draws. A card with a line no frame drew
// scrolls that line into view; one whose start is out of view brings it
// into view.
func (a *app) press(c *ui.Context, t thread, es []entry, r *client.RequestEntry, answer client.Answer, began time.Time, key bool) {
	rd := a.reads[r.Request]
	switch {
	case rd == nil:
	case rd.born.IsZero():
		a.reveal(t, es, r.Request)
	case began.Before(rd.moved.Add(dwell)):
	case key && (!a.chord.After(rd.born) || !a.keyedShown()):
	case !key && answer == client.Deny:
		a.answer(c, t, r, answer)
	case rd.unread():
		a.readOn(t, es, r.Request)
	case !a.seen(t, r.Request):
		a.reveal(t, es, r.Request)
	default:
		a.answer(c, t, r, answer)
	}
}

// readOn scrolls the waiting request's texts as little as shows the lines
// no frame drew in full, from the first, or brings the card into view
// when its texts are not.
func (a *app) readOn(t thread, es []entry, request api.ID) {
	s, rd := a.drawn[request], a.reads[request]
	if s.scroll == nil || rd.next.H == 0 || !within(s.texts, a.inFull(t)) {
		a.reveal(t, es, request)
		return
	}
	top := rd.next.Y - s.texts.Y - textsPad
	if top > 0 {
		top = min(top, rd.last.Y+rd.last.H-(s.texts.Y+s.texts.H)+textsPad)
	}
	s.scroll.Y = max(0, s.scroll.Y+top)
}

// textsPad is the room around a card's texts in their box.
const textsPad = 12

// inFull is the part of the chat's pane the timeline shows in full, as the
// frame on screen drew it: above the edge it fades out under, and above
// the composer, which a draft grows over it. Before the composer is laid
// out, or while a cover at a place the app does not know shows, it is
// none of it. Other covers hide what they lie over in it (hidden).
func (a *app) inFull(t thread) ui.Rect {
	v := a.paneAt
	if a.composerAt.H <= 0 || a.blind {
		return ui.Rect{}
	}
	v.H = min(v.H-composerRoom-a.agentsRoom(t), a.composerAt.Y-v.Y)
	return v
}

// seen is whether the last frame drew the card of the waiting request in
// full where it starts, uncovered: its header, and the first line of what
// runs, unscrolled out of its box. An answer to an unseen card would
// approve what the user never read.
func (a *app) seen(t thread, request api.ID) bool {
	s, ok := a.drawn[request]
	return ok && a.shows(s, a.inFull(t))
}

// shows is whether a card drawn where s says shows its start in v, and no
// cover lies over it.
func (a *app) shows(s sight, v ui.Rect) bool {
	return s.start(v) && !a.hidden(s.header) && !a.hidden(s.lead)
}

// start is whether a card drawn where s says shows its start in v: its
// header, and the first line of what runs, unscrolled out of its box.
func (s sight) start(v ui.Rect) bool {
	return within(s.header, v) && within(s.lead, v) && within(s.lead, s.texts)
}

// reveal brings the start of the waiting request's card into view: its
// texts back to their start, and the card to the top of the timeline when
// the last frame did not draw it in full.
func (a *app) reveal(t thread, es []entry, request api.ID) {
	s, ok := a.drawn[request]
	if ok && s.scroll != nil {
		s.scroll.Y = 0
	}
	if ok && within(s.card, a.inFull(t)) && !a.hidden(s.card) {
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

// requestAnswers are a waiting request's answers, Allow once and Deny,
// with Always allow and Allow for this session where the agent offers to
// keep the grant; or the answer on its way, or why it wasn't sent. Just
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
	allow, always, session, deny, row, labels := a.answerButtons(c, t, r)
	a.answerAt = answerRow{r.Request, row.Bounds(), allow.Bounds(), always.Bounds(), session.Bounds(), deny.Bounds(), labels}
	began := a.pressBegan(r.Request, allow.Pressed() || always.Valid() && always.Pressed() || session.Valid() && session.Pressed() || deny.Pressed())
	if allow.Clicked() {
		a.press(c, t, a.rowsOf(t).es, r, client.AllowOnce, began, false)
	}
	if always.Valid() && always.Clicked() {
		a.press(c, t, a.rowsOf(t).es, r, client.AllowAlways, began, false)
	}
	if session.Valid() && session.Clicked() {
		a.press(c, t, a.rowsOf(t).es, r, client.AllowSession, began, false)
	}
	if deny.Clicked() {
		a.press(c, t, a.rowsOf(t).es, r, client.Deny, began, false)
	}
}

// pressBegan is when the pointer began to press the request's answers,
// one of which pressed says it presses now, as the frames saw it: now for
// a press that began and ended between two frames, and for a key's.
func (a *app) pressBegan(request api.ID, pressed bool) time.Time {
	at := a.pressing
	switch {
	case pressed && at.request != request:
		a.pressing = pressAt{request, a.now}
		return a.now
	case pressed:
		return at.at
	case at.request == request:
		a.pressing = pressAt{}
		return at.at
	}
	return a.now
}

// pressAt is when the pointer began to press a request's answers.
type pressAt struct {
	request api.ID
	at      time.Time
}

// answerButtons lays out the row of a request's answers, and returns its
// buttons, the row, and its labels as they were laid out in the last
// frame, for a zap to paint as it breaks the row up.
func (a *app) answerButtons(c *ui.Context, t thread, r *client.RequestEntry) (allow, always, session, deny, row ui.Element, labels []zapLabel) {
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
		b, _ := askOf(r.Body)
		if b.Always {
			always = ghost(c, "always", false).TextColor(palette.accent).Padding(5, 10).
				Description(agentName(t) + " allows such calls from now on, by its own rules.").Children(func() {
				text("Always allow", 12.5, 500, palette.accent)
			})
		}
		if b.Session {
			session = ghost(c, "session", false).TextColor(palette.accent).Padding(5, 10).
				Description(agentName(t) + " allows such calls until its session ends, by its own rules.").Children(func() {
				text("Allow for this session", 12.5, 500, palette.accent)
			})
		}
		deny = ghost(c, "deny", false).TextColor(palette.red).Padding(5, 10).Children(func() {
			text("Deny", 12.5, 0, palette.red)
			if a.keyed == r.Request {
				chip("⇧⌘D", palette.muted, palette.hover, palette.border).MinWidth(18).TextAlign(ui.Center)
			}
		})
		ui.Spacer(c)
		ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icHand).FontSize(12).TextColor(palette.faint)
			text(askedAt(t, r), 11.5, 0, palette.faint)
		})
	})
	return allow, always, session, deny, row, labels
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
func waitingRequest(t thread, rs rows) *client.RequestEntry {
	if len(rs.asks) == 0 {
		return nil
	}
	r := rs.es[rs.asks[len(rs.asks)-1]].request
	if _, sending := responding(t, r.Request); sending {
		return nil
	}
	return r
}

// keyedRequest is the waiting request ⌘⏎ and ⇧⌘D answer, with no answer
// from here on its way: the last one the window draws in full, and else
// the last one, which they bring into view.
func (a *app) keyedRequest(t thread, rs rows) *client.RequestEntry {
	var last *client.RequestEntry
	for _, i := range slices.Backward(rs.asks) {
		r := rs.es[i].request
		if _, sending := responding(t, r.Request); sending {
			continue
		}
		if a.seen(t, r.Request) {
			return r
		}
		if last == nil {
			last = r
		}
	}
	return last
}

// approvalKeys answer a waiting request from the keyboard while the
// composer holds nothing: ⌘⏎ allows it once, as it would send a draft,
// and ⇧⌘D denies it, as press says, and its answers show the keys. With a
// draft, ⌘⏎ sends it, which steers the agent. They keep when ⌘ went down,
// and when a frame first showed the keyed card's keys. Under an overlay of
// the app they wait, taken from the composer, whose ⌘⏎ would stop the
// turn. Questions take keys of in, the chat's pane, as questionKeys says.
func (a *app) approvalKeys(c *ui.Context, in ui.Element, t thread, rs rows) {
	switch {
	case c.Modifiers()&ui.Cmd == 0:
		a.chord = time.Time{}
	case a.chord.IsZero():
		a.chord = a.now
	}
	r := a.keyedRequest(t, rs)
	var keyed api.ID
	if r != nil {
		keyed = r.Request
	}
	switch {
	case keyed != a.keyed:
		a.keyed, a.keyedSince = keyed, time.Time{}
	case a.keyedSince.IsZero():
		// This pass's timeline drew the card with its keys.
		a.keyedSince = a.now
	}
	if r == nil || a.drafting() {
		return
	}
	switch r.Kind {
	case api.KindQuestion:
		a.questionKeys(c, in, t, r)
		return
	case api.KindCredentials:
		a.credentialsKeys(c, in, t, r)
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
	if !a.covered {
		a.press(c, t, rs.es, r, answer, a.now, true)
	}
}

// keyedShown is whether ⌘ went down after a frame showed the keyed card
// with its keys: a chord held from before acts on no card keyed since.
func (a *app) keyedShown() bool {
	return !a.keyedSince.IsZero() && a.chord.After(a.keyedSince)
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
