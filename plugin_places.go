package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
	"glitched/internal/plugins/render"
	"glitched/internal/plugins/widget"
)

// threadRecord is the thread as a plugin granted threads:read sees it;
// Host.Record takes away what a plugin wasn't granted.
func (a *app) threadRecord(t thread) plugins.ThreadRecord {
	machine := ""
	if t.env != nil {
		machine = t.env.Name
	}
	return plugins.ThreadRecord{
		ID:        t.id().String(),
		Title:     t.View.Title,
		Workspace: t.View.Workspace,
		Status:    statusNames[a.statusOf(t)],
		Machine:   machine,
		UpdatedAt: t.View.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func recordVersion(r plugins.ThreadRecord) string {
	return strings.Join([]string{r.Title, r.Workspace, r.Status, r.Machine, r.UpdatedAt}, "\x00")
}

// placeKey names one copy of a plugin's place.
type placeKey struct{ plugin, place, key string }

// placeFrame is what this frame drew of the plugins' places: the copies of
// each, and the slots drawn, so that a copy no longer drawn is let go.
type placeFrame struct {
	items map[[2]string][]plugins.PlaceItem
	slots map[string]bool
	keys  map[placeKey]bool
}

// pluginSlot draws what every running plugin puts at the slot at, for the
// copy key: a thread's id, or "" for a slot drawn once. t is the thread it
// is about, nil for none.
func (a *app) pluginSlot(c *ui.Context, at, key string, t *thread) {
	if a.plugins == nil {
		return
	}
	f := &a.placeFrame
	if f.items == nil {
		f.items, f.slots, f.keys = map[[2]string][]plugins.PlaceItem{}, map[string]bool{}, map[placeKey]bool{}
	}
	f.slots[at] = true
	var full plugins.ThreadRecord
	if t != nil {
		full = a.threadRecord(*t)
	}
	for _, pl := range a.plugins.PlacesAt(at) {
		var ctx any
		version := ""
		if t != nil {
			r := a.plugins.Record(pl.Plugin, full)
			ctx, version = r, recordVersion(r)
		}
		fk := [2]string{pl.Plugin, pl.Place.ID}
		f.items[fk] = append(f.items[fk], plugins.PlaceItem{Key: key, Version: version, Context: ctx})
		app := a.plugins.App(pl.Plugin)
		if app == nil {
			continue
		}
		node := app.PlaceView(pl.Place.ID, key)
		if node == nil {
			continue
		}
		pk := placeKey{pl.Plugin, pl.Place.ID, key}
		f.keys[pk] = true
		cs := a.placeControls[pk]
		if cs == nil {
			cs = &render.Controls{}
			a.placeControls[pk] = cs
		}
		v := &widget.View{Body: node}
		cs.Sync(v, app.Rev, app.Processed)
		ref := &plugins.PlaceRef{ID: pl.Place.ID, At: at, Key: key, Context: ctx}
		ui.Row(c).Key("plugin:" + pl.Plugin + ":" + pl.Place.ID).Shrink(1).AlignItems(ui.Center).ClipX().Children(func() {
			st := a.pluginStyle(pl.Plugin)
			st.Scope = pl.Plugin + "/" + pl.Place.ID + "/" + key
			render.View(c, v, cs, st, &a.pluginMD, func(ev plugins.UIEvent, values map[string]any) int {
				ev.Place = ref
				return app.Dispatch(ev, values)
			})
		})
	}
}

// syncPlaces tells each app session which copies of its places this frame
// drew, once the frame is built.
func (a *app) syncPlaces() {
	f := a.placeFrame
	a.placeFrame = placeFrame{}
	if a.plugins == nil || f.slots == nil {
		return
	}
	for at := range f.slots {
		for _, pl := range a.plugins.PlacesAt(at) {
			if app := a.plugins.App(pl.Plugin); app != nil {
				app.SetItems(pl.Place.ID, f.items[[2]string{pl.Plugin, pl.Place.ID}])
			}
		}
	}
	for k := range a.placeControls {
		if !f.keys[k] {
			delete(a.placeControls, k)
		}
	}
}

// emitThreadChanges tells the plugins that hear thread.changed of each
// thread whose record changed since the last time, and those that hear
// thread.message of each new message in the thread shown.
func (a *app) emitThreadChanges() {
	if a.plugins == nil {
		return
	}
	seen := map[string]bool{}
	for _, e := range a.backend.Envs() {
		for _, th := range e.Threads {
			t := thread{e, th}
			r := a.threadRecord(t)
			v := recordVersion(r)
			seen[r.ID] = true
			old, known := a.pluginRecords[r.ID]
			a.pluginRecords[r.ID] = v
			if known && old != v {
				a.plugins.Emit("thread.changed", func(p string) any { return a.plugins.Record(p, r) })
			}
		}
	}
	for id := range a.pluginRecords {
		if !seen[id] {
			delete(a.pluginRecords, id)
			a.plugins.Emit("thread.changed", func(string) any { return map[string]any{"id": id, "deleted": true} })
		}
	}
	t, ok := a.find(a.selected)
	if !ok || t.Transcript == nil {
		return
	}
	// Messages by their keys, which a message keeps as more come: the
	// first look at a thread tells nothing, then each new finished one is
	// told once.
	id := t.id().String()
	first := a.pluginMessagesOf != id
	if first {
		a.pluginMessagesOf, a.pluginMessages = id, map[string]bool{}
	}
	for _, e := range entriesOf(t) {
		if (e.kind != entryUser && e.kind != entryAssistant) || e.streaming || a.pluginMessages[e.key] {
			continue
		}
		a.pluginMessages[e.key] = true
		if first {
			continue
		}
		role := "user"
		if e.kind == entryAssistant {
			role = "assistant"
		}
		msg := map[string]any{"thread": id, "role": role, "text": e.text}
		a.plugins.Emit("thread.message", func(string) any { return msg })
	}
}

// pluginTab is a tab of a plugin's in the thread panel.
type pluginTab struct{ plugin, tab string }

// pluginTabs are the tabs the running plugins contribute.
func (a *app) pluginTabs() []pluginTabEntry {
	if a.plugins == nil {
		return nil
	}
	var out []pluginTabEntry
	for _, in := range a.plugins.Installed() {
		if !in.Enabled || in.Err != nil {
			continue
		}
		for _, t := range a.plugins.Ext(in.ID).Tabs {
			out = append(out, pluginTabEntry{pluginTab{in.ID, t.ID}, t, in.Name})
		}
	}
	return out
}

type pluginTabEntry struct {
	key    pluginTab
	tab    plugins.Tab
	plugin string
}

// tabButtons are the header's buttons for the plugins' panel tabs.
func (a *app) tabButtons(c *ui.Context) {
	for _, e := range a.pluginTabs() {
		on := a.showPluginTab && !a.showDiff && !a.showPreview && a.pluginTabShown == e.key
		icon := appIconNamed(e.tab.Icon)
		if icon == nil {
			icon = icPlug
		}
		if a.iconButton(c, "tab:"+e.key.plugin+":"+e.key.tab, icon, e.tab.Title+" · "+e.plugin, on).Clicked() {
			a.openPluginTab(e.key, !on)
		}
	}
}

func (a *app) openPluginTab(k pluginTab, open bool) {
	a.showPluginTab, a.pluginTabShown = open, k
	if open {
		a.showDiff, a.showPreview = false, false // one pane beside the chat at a time
	}
}

// pluginTabPanel is the panel of the tab shown, about the thread t.
func (a *app) pluginTabPanel(c *ui.Context, t thread) {
	k := a.pluginTabShown
	ui.Column(c).Fill().Background(palette.bg).BorderWidth(0, 0, 0, 1).BorderColor(palette.border).Children(func() {
		ui.Row(c).Height(headerHeight).Shrink(0).Padding(0, 12).Gap(4).AlignItems(ui.Center).Children(func() {
			for _, e := range a.pluginTabs() {
				if row(c, "tab:"+e.key.plugin+":"+e.key.tab, e.key == k).Padding(5, 10).Children(func() {
					ui.Text(c, e.tab.Title).SingleLine()
				}).Clicked() {
					a.openPluginTab(e.key, true)
				}
			}
			ui.Spacer(c)
			if a.iconButton(c, "close-tab", icX, "Close", false).Clicked() {
				a.showPluginTab = false
			}
		})
		ui.Scroll(c).Grow(1).Padding(16).Children(func() {
			if t.id().IsZero() {
				ui.Text(c, "Open a thread to see this tab.").TextColor(palette.muted)
				return
			}
			r := a.plugins.Record(k.plugin, a.threadRecord(t))
			s, err := a.plugins.TabSession(k.plugin, k.tab, t.id().String(), r)
			if err != nil {
				ui.Text(c, err.Error()).Selectable().TextColor(palette.red)
				return
			}
			pk := pluginKey{k.plugin, "tab:" + k.tab + "@" + t.id().String()}
			a.pluginView(c, pk, s)
		})
	})
}

// shortcutKeys maps a shortcut's key to MyGo's.
func shortcutKey(k plugins.Key) (ui.Modifiers, ui.Key, bool) {
	var mods ui.Modifiers
	if k.Cmd {
		mods |= ui.Cmd
	}
	if k.Ctrl {
		mods |= ui.Ctrl
	}
	if k.Alt {
		mods |= ui.Alt
	}
	if k.Shift {
		mods |= ui.Shift
	}
	n := k.Name
	switch {
	case len(n) == 1 && n[0] >= 'a' && n[0] <= 'z':
		return mods, ui.KeyA + ui.Key(n[0]-'a'), true
	case len(n) == 1 && n[0] >= '0' && n[0] <= '9':
		return mods, ui.Key0 + ui.Key(n[0]-'0'), true
	case strings.HasPrefix(n, "f"):
		i, err := strconv.Atoi(n[1:])
		if err == nil && i >= 1 && i <= 12 {
			return mods, ui.KeyF1 + ui.Key(i-1), true
		}
	}
	named := map[string]ui.Key{"enter": ui.KeyEnter, "space": ui.KeySpace, "tab": ui.KeyTab, "up": ui.KeyUp, "down": ui.KeyDown, "left": ui.KeyLeft, "right": ui.KeyRight, "comma": ui.KeyComma, "period": ui.KeyPeriod, "slash": ui.KeySlash}
	key, ok := named[n]
	return mods, key, ok
}

// appShortcuts are the app's own: a plugin's shortcut on one of them
// stays the app's.
var appShortcuts = []string{"cmd+k", "cmd+n", "cmd+b", "cmd+d", "cmd+j", "cmd+comma", "cmd+shift+p", "cmd+shift+l", "cmd+t", "cmd+w", "cmd+q", "cmd+enter"}

// pluginShortcuts runs the plugins' commands their shortcuts name. A
// shortcut the app, or an earlier plugin, already has stays theirs.
func (a *app) pluginShortcuts(c *ui.Context) {
	if a.plugins == nil {
		return
	}
	taken := slices.Clone(appShortcuts)
	for _, cmd := range a.plugins.Shortcuts() {
		norm := cmd.Key.String()
		if slices.Contains(taken, norm) {
			continue
		}
		taken = append(taken, norm)
		mods, key, ok := shortcutKey(cmd.Key)
		if ok && c.Shortcut(mods, key) {
			a.plugins.RunCommand(cmd.Plugin, cmd.Command, func(err error) {
				if err != nil {
					a.pluginNotice(ui.Toast{Title: fmt.Sprintf("%s: %s failed", cmd.PluginName, cmd.Command), Description: err.Error()})
				}
			})
		}
	}
}
