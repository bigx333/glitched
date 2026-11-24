package main

import (
	"cmp"
	"encoding/json"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// Pointing at a message shows what can be done with it, in a toolbar
// that floats under it: copying it, putting it back in the composer to
// edit and send again, having the agent answer the message again,
// forking the thread from the turn a reply ended, or a plugin's command. The toolbar takes no room, so pointing at a message
// moves nothing.

// messageActions is the toolbar under the message es[i], at the edge the
// message is on, while the message is pointed at or the toolbar itself
// is. It touches the message, so the pointer can reach it. What is in it
// is a copy of message.toolbar, which the built-in Message toolbar draws
// from its natives: each action, as Glitched draws it.
func (a *app) messageActions(c *ui.Context, t thread, es []entry, i int, pointed bool) {
	e := es[i]
	items := a.plugins.MenuItems("message.actions")
	m := messageToolbar{Key: e.key, Role: "assistant", Items: len(items), At: e.at, Took: e.ran.Milliseconds()}
	if e.kind == entryUser {
		m.Role = "user"
	}
	a.pluginComponent(c, "message.toolbar", &t, componentCopy{
		key: e.key,
		box: func() ui.Element {
			bar := ui.Row(c).Key("actions").Absolute().Bottom(-22).MinHeight(22).AlignItems(ui.End).Padding(2, 0, 0)
			if e.kind == entryUser {
				bar.Right(0)
			} else {
				bar.Left(0)
			}
			if !pointed && !bar.Hovered() {
				bar.Invisible()
			}
			return bar
		},
		context: func(plugins.ThreadRecord) any { return m },
		action: func(n *widget.Node) {
			switch n.Name {
			case "message.copy":
				if action(c, "copy", icCopy, "Copy", false).Clicked() {
					c.WriteClipboard(e.text)
					c.AddToast(ui.Toast{Title: "Copied"})
				}
			case "message.edit":
				if action(c, "edit", icPencil, "Edit & resend", false).Clicked() {
					a.draft, a.code, a.composerOpened = e.text, codeBlock{}, true
				}
			case "message.retry":
				asked, ok := askedBefore(es, i)
				if action(c, "retry", icRotate, "Retry", !ok || t.View.Live()).Clicked() {
					a.backend.Send(t.env, t.id(), asked, api.SteerActive)
					a.listFor(t.id()).ScrollToEnd()
				}
			case "message.fork":
				if action(c, "fork", icBranch, "Fork from here", false).Clicked() {
					a.fork(t, e.turn, t.View.Model)
				}
			case "message.time":
				short, full := a.messageTime(c, e)
				a.tip(c, ui.Box(c).Label(full).Padding(2, 6).Children(func() {
					ui.Text(c, short).FontSize(11).TextColor(palette.faint).AccessHidden(true)
				}), full)
			case "message.item":
				it := items[itemIndex(n)]
				icon := appIconNamed(it.Icon)
				if icon == nil {
					icon = icPlug
				}
				if action(c, "plugin:"+it.Plugin+"/"+it.Command, icon, it.Title, false).Clicked() {
					message := map[string]string{"role": m.Role}
					if a.plugins.Allowed(it.Plugin, "messages:read") {
						message["text"] = e.text
					}
					a.runMenuItem(it, map[string]any{"thread": a.plugins.Record(it.Plugin, a.threadRecord(t)), "message": message})
				}
			}
		},
		absent: func(n *widget.Node) bool {
			switch n.Name {
			case "message.edit":
				return e.kind != entryUser
			case "message.retry":
				return !e.retry
			case "message.fork":
				return e.turn.IsZero() || t.Transcript == nil || t.Transcript.Dropped(e.turn)
			case "message.item":
				at := itemIndex(n)
				return at < 0 || at >= len(items)
			case "message.time":
				return e.at.IsZero()
			}
			return false
		},
		builtin: func() *widget.Node { return messageToolbarRow(m) },
	})
}

// messageToolbar is the context of message.toolbar: which message a copy
// is under, whose it is, and how many commands plugins put in the
// toolbar, which message.item draws by index. Never what it says.
type messageToolbar struct {
	Key   string    `json:"key"`
	Role  string    `json:"role"`
	Items int       `json:"items,omitempty"`
	At    time.Time `json:"at,omitzero"`
	Took  int64     `json:"took,omitempty"` // ms
}

// messageToolbarRow is the built-in Message toolbar's drawing of the
// toolbar m, as its main.js draws it. Glitched draws the same while no
// plugin has drawn it. The time is on the outer side, so the actions sit
// where they would without it.
func messageToolbarRow(m messageToolbar) *widget.Node {
	stamp := &widget.Node{Type: "native", ID: "time", Name: "message.time"}
	row := &widget.Node{Type: "row", Gap: 2}
	if m.Role == "user" {
		row.Children = append(row.Children, stamp)
	}
	row.Children = append(row.Children,
		&widget.Node{Type: "native", ID: "copy", Name: "message.copy"},
		&widget.Node{Type: "native", ID: "edit", Name: "message.edit"},
		&widget.Node{Type: "native", ID: "retry", Name: "message.retry"},
	)
	for i := range m.Items {
		index, _ := json.Marshal(i)
		row.Children = append(row.Children, &widget.Node{Type: "native", ID: "item-" + string(index), Name: "message.item", Props: map[string]json.RawMessage{"index": index}})
	}
	row.Children = append(row.Children, &widget.Node{Type: "native", ID: "fork", Name: "message.fork"})
	if m.Role != "user" {
		row.Children = append(row.Children, stamp)
	}
	return row
}

// A row action's padding, the gap between its icon and its label, and the
// size of both.
const (
	actionPadY = 2
	actionPadX = 6
	actionGap  = 4
	actionSize = 11
)

func action(c *ui.Context, key any, icon *ui.SVG, label string, disabled bool) ui.Element {
	return ghost(c, key, false).Padding(actionPadY, actionPadX).Gap(actionGap).Disabled(disabled).Children(func() {
		ui.Icon(c, icon).FontSize(actionSize)
		ui.Text(c, label).FontSize(actionSize)
	})
}

// messageTime is when e was sent, or its words came, as its toolbar says
// it, in the user's formats: the time today, the day too before today,
// the year too in another year; and, under a turn's last reply, how long
// the turn ran. full writes the day out, for the tooltip and assistive
// technology. A toolbar is built each frame, so each is written once.
func (a *app) messageTime(c *ui.Context, e entry) (short, full string) {
	lang := cmp.Or(c.Locale(), "en")
	r := c.Region()
	if r.HourCycle != 12 && r.HourCycle != 24 {
		r = i18n.RegionFor(lang)
		r.Locale = ""
	}
	zone, now := cmp.Or(a.zone, time.Local), a.now
	if a.clock != nil {
		now = a.clock()
	}
	at, now := e.at.In(zone), now.In(zone)
	skeleton := "jm"
	switch {
	case at.Year() != now.Year():
		skeleton = "yMMMdjm"
	case at.YearDay() != now.YearDay():
		skeleton = "MMMdjm"
	}
	k := stampKey{at: e.at.UnixNano(), ran: e.ran, zone: zone, skeleton: skeleton, lang: lang, region: r}
	if s, ok := a.stamps[k]; ok {
		return s[0], s[1]
	}
	locale := r.FormatLocale(lang)
	short = i18n.FormatDate(locale, at, i18n.DateOptions{Skeleton: skeleton, HourCycle: r.HourCycle})
	full = i18n.FormatDate(locale, at, i18n.DateOptions{Date: i18n.FullStyle, Time: i18n.ShortStyle, HourCycle: r.HourCycle})
	if e.ran >= time.Second {
		took := e.ran.Round(time.Second)
		short += " · " + took.String()
		full += ", took " + took.String()
	}
	if a.stamps == nil || len(a.stamps) >= 4096 {
		a.stamps = map[stampKey][2]string{}
	}
	a.stamps[k] = [2]string{short, full}
	return short, full
}

// stampKey is what a message's time is written from.
type stampKey struct {
	at       int64
	ran      time.Duration
	zone     *time.Location
	skeleton string
	lang     string
	region   i18n.Region
}

// itemIndex is the index of the message.item n, -1 without a whole
// number.
func itemIndex(n *widget.Node) int {
	at := -1
	if json.Unmarshal(n.Props["index"], &at) != nil {
		return -1
	}
	return at
}

// askedBefore is the message the reply es[i] answers: the last one sent
// before it.
func askedBefore(es []entry, i int) (string, bool) {
	for ; i >= 0; i-- {
		if es[i].kind == entryUser && es[i].failed.Err == nil {
			return es[i].text, true
		}
	}
	return "", false
}

// pluginMenuItems end a menu with the commands plugins put in it, after a
// line; args are what the plugin's command is passed.
func (a *app) pluginMenuItems(m *ui.Menu, at string, args func(plugin string) map[string]any) {
	items := a.plugins.MenuItems(at)
	if len(items) == 0 {
		return
	}
	m.Separator()
	for _, it := range items {
		if m.Item(it.Title).Chosen() {
			a.runMenuItem(it, args(it.Plugin))
		}
	}
}

// runMenuItem runs a plugin's command chosen in a menu of the app's, and
// says so when it fails.
func (a *app) runMenuItem(it plugins.MenuItem, args map[string]any) {
	a.plugins.RunCommand(it.Plugin, it.Command, args, func(err error) {
		if err != nil {
			a.pluginNotice(ui.Toast{Title: a.plugins.Name(it.Plugin) + ": " + it.Title + " failed", Description: err.Error()})
		}
	})
}
