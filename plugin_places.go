package main

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"weak"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
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
	d := disposition(t)
	pd := &plugins.Disposition{State: dispositionNames[d.State]}
	if d.State == api.Snoozed {
		pd.Until = d.Until.UTC().Format(time.RFC3339)
	}
	r := plugins.ThreadRecord{
		ID:          t.id().String(),
		Title:       t.View.Title,
		Workspace:   t.View.Workspace,
		Status:      statusNames[a.statusOf(t)],
		Machine:     machine,
		UpdatedAt:   t.View.UpdatedAt.UTC().Format(time.RFC3339),
		Section:     sectionNames[a.categoryOf(t)],
		Disposition: pd,
		Unread:      a.backend.Unread(t.Thread),
		Requests:    len(t.View.Requests),
		Tokens:      t.View.Tokens,
	}
	if p, ok := a.parentOf(t); ok {
		r.Parent = p.id().String()
	}
	for _, x := range a.ancestors(t) {
		r.Ancestors = append(r.Ancestors, x.id().String())
	}
	for _, k := range a.kids[t.id()] {
		r.Children = append(r.Children, k.id().String())
	}
	return r
}

// sectionNames and dispositionNames are the sidebar's sections and the
// inbox states as plugins name them.
var (
	sectionNames     = map[category]string{catInbox: "inbox", catWorking: "working", catSnoozed: "snoozed", catSettled: "settled"}
	dispositionNames = map[api.DispositionState]string{api.Open: "inbox", api.Snoozed: "snoozed", api.Settled: "settled"}
)

func recordVersion(r plugins.ThreadRecord) string {
	d := ""
	if r.Disposition != nil {
		d = r.Disposition.State + " " + r.Disposition.Until
	}
	return strings.Join([]string{r.Title, r.Workspace, r.Status, r.Machine, r.UpdatedAt, r.Section, d, strconv.FormatBool(r.Unread),
		r.Parent, strings.Join(r.Ancestors, ","), strings.Join(r.Children, ","), strconv.Itoa(r.Requests), strconv.FormatInt(r.Tokens, 10)}, "\x00")
}

// placeKey names one copy of a plugin's place.
type placeKey struct{ plugin, place, key string }

// placeFrame is what this frame drew of the plugins' places: the copies of
// each, and the slots drawn, so that a copy no longer drawn is let go.
type placeFrame struct {
	items map[[2]string][]plugins.PlaceItem
	slots map[string]bool
	keys  map[placeKey]bool
	// waited are the components that waited for a first drawing.
	waited map[string]bool
	// drawn is who drew the copies of each component drawn.
	drawn map[string]*componentDrawn
	// rowsBy is when the frame stops waiting for lists' rows: one wait
	// for all its lists, however many.
	rowsBy time.Time
}

// componentDrawn is who drew each copy of a component in a frame, in
// order: thread is the thread they were about, nil for a component of the
// app's own.
type componentDrawn struct {
	thread *api.ID
	copies []copyDrawn
	// frame is the frame that drew them, and replacers the plugins that
	// replaced the component then, best first, as they stood.
	frame     int
	replacers []replacerDrawn
}

// replacerDrawn is a plugin that replaced a component as a frame drew it:
// its session then, and whether its last drawing had failed.
type replacerDrawn struct {
	id      string
	session *plugins.Session
	failed  bool
}

// scopeOf is 1 for a thread's component, 0 for the app's own.
func scopeOf(d *componentDrawn) int {
	if d.thread != nil {
		return 1
	}
	return 0
}

// copyDrawn is who drew one copy: the plugin, or Glitched when glitched,
// or nothing; and the plugins that answered null for it.
type copyDrawn struct {
	plugin   string
	glitched bool
	nulls    []string
}

// frameOf is this frame's record of what the plugins drew, with the slot
// or component at marked drawn.
func (a *app) frameOf(at string) *placeFrame {
	f := &a.placeFrame
	if f.items == nil {
		f.items, f.slots, f.keys, f.waited, f.drawn = map[[2]string][]plugins.PlaceItem{}, map[string]bool{}, map[placeKey]bool{}, map[string]bool{}, map[string]*componentDrawn{}
	}
	f.slots[at] = true
	return f
}

// pluginSlot draws what every running plugin puts at the slot at, for the
// copy key: a thread's id, or "" for a slot drawn once. t is the thread it
// is about, nil for none.
func (a *app) pluginSlot(c *ui.Context, at, key string, t *thread) {
	if a.plugins == nil {
		return
	}
	f := a.frameOf(at)
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
		a.drawPlace(c, ui.Row(c).Key("plugin:"+pl.Plugin+":"+pl.Place.ID).Shrink(1).AlignItems(ui.Center).ClipX(), pl, key, ctx, app, node, nil, nil, "", nil)
	}
}

// sessionControls are the controls of what a plugin's app session draws,
// and that session, held weakly: one closed is no longer kept.
type sessionControls struct {
	cs *render.Controls
	s  weak.Pointer[plugins.Session]
}

// controlsOf are the controls at k of what the session s draws. A plugin
// loaded again counts its views and the events it took from the start, so
// its controls start again with it, else a view of the same count would
// leave them as they were: an input or a list it now draws would never
// show, and a field would keep the last load's value.
func controlsOf[K comparable](m map[K]*sessionControls, k K, s *plugins.Session) *render.Controls {
	sc := m[k]
	switch {
	case sc == nil:
		sc = &sessionControls{&render.Controls{}, weak.Make(s)}
		m[k] = sc
	case sc.s != weak.Make(s):
		sc.cs.Restart()
		sc.s = weak.Make(s)
	}
	return sc.cs
}

// drawPlace draws in box what the plugin drew for one copy of its place,
// and hands what the user does there to its app session.
//
// native draws the native widgets of a component's copy, nil elsewhere,
// listRow does to its lists' rows what the app does to its own, and
// after what Glitched draws over the drawing in box, as a message's
// actions, nil for nothing.
func (a *app) drawPlace(c *ui.Context, box ui.Element, pl plugins.PlaceAt, key string, ctx any, app *plugins.Session, node *widget.Node, native func(c *ui.Context, n *widget.Node, row string, fire func(any)), listRow func(c *ui.Context, list *widget.Node, i int, e ui.Element) bool, reveal string, after func()) {
	pk := placeKey{pl.Plugin, pl.Place.ID, key}
	a.placeFrame.keys[pk] = true
	cs := controlsOf(a.placeControls, pk, app)
	v := &widget.View{Body: node}
	cs.Sync(v, app.Rev, app.Processed)
	ref := &plugins.PlaceRef{ID: pl.Place.ID, At: pl.Place.At, Key: key, Context: ctx}
	box.Children(func() {
		st := a.pluginStyle(pl.Plugin)
		st.Scope = pl.Plugin + "/" + pl.Place.ID + "/" + key
		st.Native = native
		st.ListRow = listRow
		st.Reveal = reveal
		st.Rows = func(list string, it widget.ListItem) *widget.Node {
			return app.Row(plugins.RowAt{List: list, Place: ref}, it)
		}
		st.WaitRows = func(list string, items []widget.ListItem) {
			if a.placeFrame.rowsBy.IsZero() {
				a.placeFrame.rowsBy = time.Now().Add(firstDrawWait)
			}
			app.WaitRows(plugins.RowAt{List: list, Place: ref}, items, a.placeFrame.rowsBy)
		}
		render.View(c, v, cs, st, &a.pluginMD, func(ev plugins.UIEvent, values map[string]any) int {
			ev.Place = ref
			return app.Dispatch(ev, values)
		})
		if after != nil {
			after()
		}
	})
}

// syncPlaces tells each app session which copies of its places this frame
// drew, once the frame is built.
func (a *app) syncPlaces() {
	f := a.placeFrame
	a.placeFrame = placeFrame{}
	if a.plugins == nil {
		return
	}
	// A record of a session since stopped holds it no longer.
	maps.DeleteFunc(a.partsDrawn, func(_ string, d *componentDrawn) bool {
		return slices.ContainsFunc(d.replacers, func(r replacerDrawn) bool { return a.plugins.Live(r.id) != r.session })
	})
	if f.slots == nil {
		return
	}
	for at := range f.slots {
		// A built-in a copy shadows draws only a component's copies, as a
		// fallback, so only Replacers names it.
		for _, pl := range slices.Concat(a.plugins.PlacesAt(at), a.plugins.Replacers(at)) {
			if app := a.plugins.App(pl.Plugin); app != nil {
				app.SetItems(pl.Place.ID, f.items[[2]string{pl.Plugin, pl.Place.ID}])
			}
		}
	}
	for k := range a.placeControls {
		if !f.keys[k] {
			a.placeControls[k].cs.Release()
			delete(a.placeControls, k)
		}
	}
	if a.partsDrawn == nil {
		a.partsDrawn = map[string]*componentDrawn{}
	}
	a.frames++
	for _, d := range f.drawn {
		d.frame = a.frames
		a.scopeFrames[scopeOf(d)] = a.frames
	}
	maps.Copy(a.partsDrawn, f.drawn)
}

// emitThreadChanges tells the plugins that hear thread.changed of each
// thread whose record changed since the last time, those that hear
// thread.message of each new message in the thread shown, and those that
// hear thread.files.changed of each edit to the thread shown's files.
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
			from, filed := a.pluginSections[r.ID]
			a.pluginSections[r.ID] = r.Section
			if filed && from != r.Section {
				a.sidebarKept[t.id()] = true
				ev := map[string]string{"thread": r.ID, "from": from, "to": r.Section}
				a.plugins.Emit("thread.moved", func(string) any { return ev })
			}
		}
	}
	for id := range a.pluginRecords {
		if !seen[id] {
			delete(a.pluginRecords, id)
			delete(a.pluginSections, id)
			delete(a.pluginMessages, id)
			delete(a.pluginFiles, id)
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
	// The files, unless they're the very ones told of, which rowsOf
	// keeps while none changes.
	files := a.rowsOf(t).files
	if was := a.pluginFiles[id]; was == nil || len(was.of) != len(files) || len(files) > 0 && &was.of[0] != &files[0] {
		var edited []string
		stamps := make(map[string]time.Time, len(files))
		for _, f := range files {
			stamps[f.path] = f.stamp
			if was != nil {
				if at, ok := was.stamps[f.path]; !ok || !at.Equal(f.stamp) {
					edited = append(edited, f.path)
				}
			}
		}
		if len(edited) > 0 {
			ev := map[string]any{"thread": id, "files": edited}
			a.plugins.Emit("thread.files.changed", func(string) any { return ev })
		}
		a.pluginFiles[id] = &toldFiles{stamps: stamps, of: files}
	}
	told, known := a.pluginMessages[id]
	first := !known
	if first {
		told = &toldMessages{keys: map[string]bool{}}
		a.pluginMessages[id] = told
	}
	rs := a.rowsOf(t)
	if told.epoch != rs.epoch {
		told.epoch, told.through = rs.epoch, 0
	}
	for i := told.through; i < len(rs.es); i++ {
		e := rs.es[i]
		if told.through == i && i < rs.fixed && !e.streaming {
			told.through++
		}
		if (e.kind != entryUser && e.kind != entryAssistant) || e.streaming || told.keys[e.key] {
			continue
		}
		told.keys[e.key] = true
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

// toldFiles are the changed files of a thread the plugins were told of,
// with each one's last edit, and the files rowsOf gave for them.
type toldFiles struct {
	stamps map[string]time.Time
	of     []fileChange
}

// toldMessages are the messages of a thread the plugins were told of, by
// key, and the rows of the thread's rows (of epoch) through which every
// message is told and none will change.
type toldMessages struct {
	keys    map[string]bool
	through int
	epoch   uint64
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
			a.plugins.RunCommand(cmd.Plugin, cmd.Command, nil, func(err error) {
				if err != nil {
					a.pluginNotice(ui.Toast{Title: fmt.Sprintf("%s: %s failed", cmd.PluginName, cmd.Command), Description: err.Error()})
				}
			})
		}
	}
}
