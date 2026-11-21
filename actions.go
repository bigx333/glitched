package main

import (
	"encoding/json"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// Pointing at a message shows what can be done with it, in a toolbar
// that floats under it: copying it, putting it back in the composer to
// edit and send again, having the agent answer the message again, or a
// plugin's command. The toolbar takes no room, so pointing at a message
// moves nothing.

// messageActions is the toolbar under the message es[i], at the edge the
// message is on, while the message is pointed at or the toolbar itself
// is. It touches the message, so the pointer can reach it. What is in it
// is a copy of message.toolbar, which the built-in Message toolbar draws
// from its natives: each action, as Glitched draws it.
func (a *app) messageActions(c *ui.Context, t thread, es []entry, i int, pointed bool) {
	e := es[i]
	items := a.plugins.MenuItems("message.actions")
	m := messageToolbar{Key: e.key, Role: "assistant", Items: len(items)}
	if e.kind == entryUser {
		m.Role = "user"
	}
	a.pluginComponent(c, "message.toolbar", &t, componentCopy{
		key: e.key,
		box: func() ui.Element {
			bar := ui.Row(c).Key("actions").Absolute().Bottom(-22).Padding(2, 0, 0)
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
				if e.kind == entryUser && action(c, "edit", icPencil, "Edit & resend", false).Clicked() {
					a.draft, a.code, a.composerOpened = e.text, codeBlock{}, true
				}
			case "message.retry":
				if e.kind != entryAssistant {
					return
				}
				asked, ok := askedBefore(es, i)
				if action(c, "retry", icRotate, "Retry", !ok || t.View.Live()).Clicked() {
					a.backend.Send(t.env, t.id(), asked, api.SteerActive)
					a.listFor(t.id()).ScrollToEnd()
				}
			case "message.item":
				var at int
				if json.Unmarshal(n.Props["index"], &at) != nil || at < 0 || at >= len(items) {
					return
				}
				it := items[at]
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
		builtin: func() *widget.Node { return messageToolbarRow(m) },
	})
}

// messageToolbar is the context of message.toolbar: which message a copy
// is under, whose it is, and how many commands plugins put in the
// toolbar, which message.item draws by index. Never what it says.
type messageToolbar struct {
	Key   string `json:"key"`
	Role  string `json:"role"`
	Items int    `json:"items,omitempty"`
}

// messageToolbarRow is the built-in Message toolbar's drawing of the
// toolbar m, as its main.js draws it. Glitched draws the same while no
// plugin has drawn it.
func messageToolbarRow(m messageToolbar) *widget.Node {
	row := &widget.Node{Type: "row", Gap: 2, Children: []*widget.Node{{Type: "native", ID: "copy", Name: "message.copy"}}}
	if m.Role == "user" {
		row.Children = append(row.Children, &widget.Node{Type: "native", ID: "edit", Name: "message.edit"})
	} else {
		row.Children = append(row.Children, &widget.Node{Type: "native", ID: "retry", Name: "message.retry"})
	}
	for i := range m.Items {
		index, _ := json.Marshal(i)
		row.Children = append(row.Children, &widget.Node{Type: "native", ID: "item-" + string(index), Name: "message.item", Props: map[string]json.RawMessage{"index": index}})
	}
	return row
}

func action(c *ui.Context, key any, icon *ui.SVG, label string, disabled bool) ui.Element {
	return ghost(c, key, false).Padding(2, 6).Gap(4).Disabled(disabled).Children(func() {
		ui.Icon(c, icon).FontSize(11)
		ui.Text(c, label).FontSize(11)
	})
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
