package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
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

// shown is a string from the agent as the card shows it. Each control or
// format character, and each line or paragraph separator, is an escape
// such as \u{202E}: drawn as it is, it can reorder a line or end it, so
// the card would show one thing while an allow approves another. A line
// break stays one in a field of lines.
func shown(s string, lines bool) string {
	var b strings.Builder
	for _, r := range s {
		if (r != '\n' || !lines) && (unicode.In(r, unicode.Cc, unicode.Cf) || r == '\u2028' || r == '\u2029') {
			fmt.Fprintf(&b, `\u{%X}`, r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
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
// do, where, and why, with the answers under it while it waits. Answered,
// it keeps the answer; voided, it says so.
func (a *app) requestCard(c *ui.Context, t thread, e entry) {
	r := e.request
	b, known := askOf(r.Body)
	waiting := r.State == api.RequestPending
	ui.Column(c).Key(e.key).Radius(12).Border(1, palette.border).Background(palette.surface).Clip().
		Transition(riseIn).Children(func() {
		ui.Row(c).Padding(9, 14).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border).Children(func() {
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
		ui.Column(c).Padding(12, 14, 12).Gap(8).Children(func() {
			switch {
			case !known:
				ui.Text(c, agentName(t)+" asks:").FontSize(12.5).TextColor(palette.muted)
				ui.Text(c, shown(string(r.Body), false)).Selectable().FontSize(12).Font("monospace")
			case b.Command != "":
				ui.Row(c).Gap(5).AlignItems(ui.Center).Wrap().Children(func() {
					ui.Text(c, agentName(t)+" wants to run a command").FontSize(12.5).TextColor(palette.muted)
					if b.Cwd != "" {
						ui.Text(c, "in").FontSize(12.5).TextColor(palette.muted)
						ui.Text(c, shown(b.Cwd, false)).FontSize(12).Font("monospace")
					}
				})
				ui.Text(c, shown(b.Command, true)).Selectable().FontSize(12.5).Font("monospace").Padding(8, 10).Radius(6).
					Background(palette.bg).Border(1, palette.border)
				if b.Unsandboxed {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, icShieldAlert).FontSize(13).TextColor(palette.amber)
						ui.Text(c, "Runs outside the sandbox").FontSize(12.5).FontWeight(500).TextColor(palette.amber)
					})
				}
			default:
				ui.Row(c).Gap(5).AlignItems(ui.Center).Wrap().Children(func() {
					ui.Text(c, agentName(t)+" wants to "+shown(b.Operation, false)).FontSize(12.5).TextColor(palette.muted)
					if b.Path != "" {
						ui.Text(c, shown(b.Path, false)).FontSize(12).Font("monospace")
					}
				})
				if b.Args != "" {
					a.requestArgs(c, r.Request, b.Args)
				}
			}
			if b.Reason != "" {
				ui.Text(c, shown(b.Reason, true)).FontSize(12.5).TextColor(palette.muted).LineHeight(1.45)
			}
			if r.Reason != "" && !waiting {
				ui.Text(c, shown(r.Reason, true)).FontSize(12).TextColor(palette.faint)
			}
		})
		if waiting || a.zapping(r) {
			a.requestAnswers(c, t, r)
		}
	})
}

// argsLines and argsChars bound the arguments a card shows until asked
// for all of them.
const argsLines, argsChars = 8, 600

// requestArgs are the arguments an allow approves, cut to their first
// lines with a toggle to show all and how much more that is.
func (a *app) requestArgs(c *ui.Context, request api.ID, args string) {
	args = shown(args, true)
	open := a.argsOpen[request]
	text, cut := args, false
	if !open {
		text, cut = clipped(args, argsLines, argsChars)
	}
	ui.Text(c, text).Selectable().FontSize(12).Font("monospace").LineHeight(1.45).Padding(8, 10).Radius(6).
		Background(palette.bg).Border(1, palette.border)
	if !cut && !open {
		return
	}
	label, icon := "Show less", icChevronUp
	if !open {
		label, icon = "Show all · "+more(args, text), icChevronD
	}
	if ghost(c, "args", false).Padding(2, 6).Children(func() {
		ui.Icon(c, icon).FontSize(12)
		ui.Text(c, label).FontSize(12)
	}).Clicked() {
		a.argsOpen[request] = !open
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
		a.answer(c, t, r, client.AllowOnce)
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
			chip("⌘⏎", ui.Hex("#ffffff").Alpha(0.9), ui.Hex("#ffffff").Alpha(0.2), ui.Color{})
		})
		deny = ghost(c, "deny", false).TextColor(palette.red).Padding(5, 10).Children(func() {
			text("Deny", 12.5, 0, palette.red)
			chip("⇧⌘D", palette.muted, palette.hover, palette.border).MinWidth(18).TextAlign(ui.Center)
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

// approvalKeys answer the waiting request from the keyboard while the
// composer holds nothing: ⌘⏎ allows it once, as it would send a draft,
// and ⇧⌘D denies it. With a draft, ⌘⏎ sends it, which steers the agent.
func (a *app) approvalKeys(c *ui.Context, t thread, es []entry) {
	r := waitingRequest(t, es)
	if r == nil || strings.TrimSpace(a.draft) != "" || len(a.attachments) > 0 {
		return
	}
	switch {
	case c.Shortcut(ui.Cmd, ui.KeyEnter):
		a.answer(c, t, r, client.AllowOnce)
	case c.Shortcut(ui.Cmd|ui.Shift, ui.KeyD):
		a.answer(c, t, r, client.Deny)
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
