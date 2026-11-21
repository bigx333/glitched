package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/render"
	"glitched/internal/plugins/widget"
)

// A componentCopy is one copy of a component that a frame draws: its
// key, the box it makes to draw in, its context from the thread's record
// as each plugin may see it, and its native body, which Glitched draws
// while no plugin has drawn the copy yet. Nil draws nothing then. The
// body is given the native node that places it, nil where Glitched draws
// the body alone.
// builtin is the built-in's drawing of the copy, which Glitched draws
// around the body while no plugin has drawn it, or while the built-in's
// drawing is of the copy as it was. retry is the copy's transcript.retry,
// and over what Glitched draws over every drawing of it, in its box, as
// a message's actions.
type componentCopy struct {
	key     string
	box     func() ui.Element
	context func(plugins.ThreadRecord) any
	native  func(body *widget.Node)
	retry   func()
	over    func(box ui.Element)
	builtin func() *widget.Node
	// listRow does to the rows of the copy's lists what the app does to
	// its own (render.Style.ListRow), builtin when the built-in plugin
	// draws the copy; nil does nothing.
	listRow func(c *ui.Context, list *widget.Node, i int, e ui.Element, builtin bool) bool
	// current waits for the plugin to draw each new context of the copy,
	// as for its first drawing, so that what it shows keeps up with the
	// app's own state, as the sidebar's threads moving between sections.
	current bool
}

// fallback draws the copy's native body of the component name, in the
// built-in's drawing where the copy names one, so that the body keeps its
// state, as a selection, when that drawing takes over.
func (a *app) fallback(c *ui.Context, name string, t *thread, cp componentCopy) {
	if cp.native == nil {
		return
	}
	box := cp.box()
	box.Children(func() {
		if cp.builtin == nil {
			cp.native(nil)
		} else {
			st := a.pluginStyle("")
			st.Native = func(c *ui.Context, n *widget.Node, row string, fire func(any)) {
				a.drawNative(c, name, t, row, n, fire, cp)
			}
			render.View(c, &widget.View{Body: cp.builtin()}, &render.Controls{}, st, &a.pluginMD, nil)
		}
		if cp.over != nil {
			cp.over(box)
		}
	})
}

// pluginComponent draws copies of the component name, each with the first
// plugin that replaces it and has a good drawing of it: the user's before
// the built-in. Each is given every copy, so that one failing falls back
// to the next with no gap. The native widgets they place draw about t,
// which is nil for a component of the app's own, as a settings page,
// whose record is zero. It reports whether a plugin drew every copy:
// where Glitched draws the built-in itself (plugins.Native), it draws it
// for a copy no replacement draws well.
//
// Copies none has drawn yet, as a panel opening, are drawn before they
// show, in one wait a frame for the component: a copy a later call of
// the same frame brings shows its native body until its drawing comes.
func (a *app) pluginComponent(c *ui.Context, name string, t *thread, copies ...componentCopy) bool {
	if a.plugins == nil {
		for _, cp := range copies {
			a.fallback(c, name, t, cp)
		}
		return false
	}
	f := a.frameOf(name)
	var full plugins.ThreadRecord
	if t != nil {
		full = a.threadRecord(*t)
	}
	type drawing struct {
		pl      plugins.PlaceAt
		ctx     any
		version string
		app     *plugins.Session
		node    *widget.Node
	}
	replacers := a.plugins.Replacers(name)
	ctxs := make([][]any, len(copies))
	versions := make([][]string, len(copies))
	for i, cp := range copies {
		for _, pl := range replacers {
			ctx := cp.context(a.plugins.Record(pl.Plugin, full))
			ctxs[i] = append(ctxs[i], ctx)
			version, _ := json.Marshal(ctx)
			versions[i] = append(versions[i], string(version))
			fk := [2]string{pl.Plugin, pl.Place.ID}
			f.items[fk] = append(f.items[fk], plugins.PlaceItem{Key: cp.key, Version: string(version), Context: ctx})
		}
	}
	// pick chooses each copy's drawing from what the plugins have drawn.
	good := make([]*drawing, len(copies))
	failed := make([]*drawing, len(copies))
	pick := func() {
		for i, cp := range copies {
			good[i], failed[i] = nil, nil
			for j, pl := range replacers {
				app := a.plugins.App(pl.Plugin)
				if app == nil {
					continue
				}
				node := app.PlaceView(pl.Place.ID, cp.key)
				switch {
				case node == nil:
				case app.Err == nil:
					good[i] = &drawing{pl, ctxs[i][j], versions[i][j], app, node}
				case failed[i] == nil:
					failed[i] = &drawing{pl, ctxs[i][j], versions[i][j], app, node}
				}
				if good[i] != nil {
					break
				}
			}
		}
	}
	pick()
	undrawn := func(i int) bool { return good[i] == nil && failed[i] == nil }
	waits := false
	for i, cp := range copies {
		waits = waits || undrawn(i) || cp.current
	}
	if waits && !f.waited[name] {
		f.waited[name] = true
		deadline := time.Now().Add(firstDrawWait)
		for _, pl := range replacers {
			app := a.plugins.App(pl.Plugin)
			if app == nil {
				continue
			}
			var keys []string
			for i, cp := range copies {
				// A current copy waits on the plugin drawing it, not
				// on one after it.
				if undrawn(i) || cp.current && (good[i] == nil || good[i].pl.Plugin == pl.Plugin) {
					keys = append(keys, cp.key)
				}
			}
			if len(keys) == 0 {
				break
			}
			app.DrawFirst(pl.Place.ID, f.items[[2]string{pl.Plugin, pl.Place.ID}], keys, deadline)
			pick() // the wait may have changed any copy's drawing, not only the new ones'
		}
	}
	drawn := f.drawn[name]
	if drawn == nil {
		drawn = &componentDrawn{}
		if t != nil {
			id := t.id()
			drawn.thread = &id
		}
		for _, pl := range replacers {
			drawn.replacers = append(drawn.replacers, replacerDrawn{pl.Plugin, a.plugins.Live(pl.Plugin), a.plugins.Failed(pl.Plugin) != nil})
		}
		f.drawn[name] = drawn
	}
	drewAll := true
	for i, cp := range copies {
		var by copyDrawn
		for _, pl := range replacers {
			if app := a.plugins.Live(pl.Plugin); app != nil && app.AnsweredNull(pl.Place.ID, cp.key) {
				by.nulls = append(by.nulls, pl.Plugin)
			}
		}
		// When every one failed, the first one's last good drawing stays:
		// a row that can still be clicked beats none. Where Glitched draws
		// the built-in itself, it does instead, as it does for a current
		// copy, whose last good drawing shows what no longer is.
		d := cmp.Or(good[i], failed[i])
		if d == nil || good[i] == nil && (plugins.ComponentNamed(name).Native || cp.current) {
			drewAll = false
			by.glitched = cp.native != nil
			drawn.copies = append(drawn.copies, by)
			a.fallback(c, name, t, cp)
			continue
		}
		by.plugin = d.pl.Plugin
		drawn.copies = append(drawn.copies, by)
		// The built-in's drawing of the copy as it was, as a message's
		// note before it failed, is Glitched's of it as it is.
		if cp.builtin != nil && d.app.Builtin && d.app.Answered(d.pl.Place.ID, cp.key) != d.version {
			a.fallback(c, name, t, cp)
			continue
		}
		box := cp.box()
		a.drawPlace(c, box, d.pl, cp.key, d.ctx, d.app, d.node, func(c *ui.Context, n *widget.Node, row string, fire func(any)) {
			a.drawNative(c, name, t, row, n, fire, cp)
		}, func(c *ui.Context, list *widget.Node, i int, e ui.Element) bool {
			return cp.listRow != nil && cp.listRow(c, list, i, e, d.app.Builtin)
		}, func() {
			if cp.over != nil {
				cp.over(box)
			}
		})
	}
	return drewAll
}

// drawNative draws the native widget n in a copy of the component about
// t, or, in a list's row, about the thread its item names, row: only in
// the components it names. cp is the copy it is in, whose native body
// transcript.body draws and whose retry transcript.retry does.
func (a *app) drawNative(c *ui.Context, component string, t *thread, row string, n *widget.Node, fire func(any), cp componentCopy) {
	nat := widget.Natives[n.Name]
	if nat.Row {
		t = nil
		if id, err := api.ParseID(row); err == nil {
			if rt, ok := a.find(id); ok {
				t = &rt
			}
		}
	}
	if !slices.Contains(nat.In, component) || (nat.Thread || nat.Row) && t == nil {
		ui.Text(c, n.Name+" draws only in "+strings.Join(nat.In, ", ")+".").TextColor(palette.muted).Margin(ui.Auto)
		return
	}
	switch n.Name {
	case "thread.row.actions":
		a.rowActions(c, *t, a.shownIn(*t))
	case "thread.rename":
		if a.renaming == t.id() {
			a.renameInput(c, *t)
		}
	case "thread.tile":
		a.threadTile(c, *t, a.projectOf(*t))
	case "thread.status":
		var dot bool
		var size float32
		json.Unmarshal(n.Props["dot"], &dot)
		json.Unmarshal(n.Props["size"], &size)
		mark := func() {
			if dot {
				a.statusDot(c, a.statusOf(*t), failedLast(*t))
			} else {
				a.statusMark(c, a.statusOf(*t))
			}
		}
		if size > 0 {
			ui.Box(c).Size(size, size).Center().Shrink(0).Children(mark)
		} else {
			mark()
		}
	case "thread.age":
		if component == "agents.body" {
			ui.Text(c, ago(t.View.UpdatedAt, a.now)).FontSize(11).TextColor(palette.faint).Shrink(0)
			break
		}
		ui.Text(c, ago(t.View.UpdatedAt, a.now)).FontSize(11.5).TextColor(palette.faint).MinWidth(28).TextAlign(ui.End)
	case "thread.actions":
		a.iconButton(c, "menu", icDots, "Thread actions", false).Padding(3).
			Menu(func(m *ui.Menu) { a.threadMenu(c, m, *t, a.categoryOf(*t)) })
	case "thread.children":
		if len(a.shown(*t)) > 0 {
			a.childToggle(c, *t)
		}
	case "thread.title":
		var child bool
		json.Unmarshal(n.Props["child"], &child)
		if component == "agents.body" {
			ui.Text(c, title(*t)).FontSize(12.5).TextColor(palette.text).SingleLine().Grow(1).Shrink(1)
			break
		}
		if !child {
			a.rowTitle(c, *t)
			break
		}
		name := ui.Text(c, title(*t)).FontSize(12.5).SingleLine().Grow(1).Shrink(1)
		if a.backend.Unread(t.Thread) {
			name.FontWeight(600).TextColor(palette.text)
		}
	case "thread.mark":
		var child bool
		json.Unmarshal(n.Props["child"], &child)
		switch {
		case !child:
			a.rowMark(c, *t, a.shownIn(*t), t.id() == a.selected && a.router.Path() == "/")
		case len(a.shown(*t)) == 0:
			ui.Text(c, ago(t.View.UpdatedAt, a.now)).FontSize(11).TextColor(palette.faint).Shrink(0)
		}
	case "thread.slot":
		a.pluginSlot(c, "thread.row", t.id().String(), t)
	case "thread.machine":
		if len(a.backend.Envs()) > 1 {
			a.machineBadge(c, t.env)
		}
	case "sidebar.section":
		a.sectionNative(c, n)
	case "composer.input":
		a.composerIn(c)
	case "transcript.body":
		cp.native(n)
	case "transcript.retry":
		cp.retry()
	case "diff.body":
		var path string
		json.Unmarshal(n.Props["path"], &path)
		a.diffBody(c, *t, path, fire)
	case "theme.card":
		var name string
		json.Unmarshal(n.Props["theme"], &name)
		if !slices.Contains(themeNames(), name) {
			ui.Text(c, "No theme "+name+".").TextColor(palette.muted)
			return
		}
		ui.Row(c).Children(func() { a.themeCard(c, name, func() { fire(name) }) })
	}
}

// firstDrawWait bounds how long the UI waits for a component's first
// drawing: past it, the copy shows empty till the plugin answers.
const firstDrawWait = 300 * time.Millisecond

// headerActions is the context of thread.header.actions.
type headerActions struct {
	Thread       plugins.ThreadRecord `json:"thread"`
	Local        bool                 `json:"local"`
	Narrow       bool                 `json:"narrow"`
	Panels       panelState           `json:"panels"`
	ChangedFiles int                  `json:"changedFiles"`
}

type panelState struct {
	Terminal bool `json:"terminal"`
	Diff     bool `json:"diff"`
	Preview  bool `json:"preview"`
}

func (a *app) panels() panelState {
	return panelState{Terminal: a.showTerminal, Diff: a.showDiff, Preview: a.showPreview}
}

// togglePanel opens or closes a panel beside the chat, as its header
// button and shortcut do.
func (a *app) togglePanel(panel string) error {
	switch panel {
	case "terminal":
		a.showTerminal = !a.showTerminal
	case "diff":
		a.showDiff = !a.showDiff
		if a.showDiff {
			a.showPreview = false
		}
	case "preview":
		a.togglePreview()
	default:
		return fmt.Errorf("no panel %q: terminal, diff or preview", panel)
	}
	a.panelsJump = false
	return nil
}

// panelEffect is panels.toggle: open, when given, says which way.
func (a *app) panelEffect(args json.RawMessage) (any, error) {
	var in struct {
		Panel string
		Open  *bool
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	now := map[string]bool{"terminal": a.showTerminal, "diff": a.showDiff, "preview": a.showPreview}
	if on, ok := now[in.Panel]; ok && in.Open != nil && *in.Open == on {
		return a.panels(), nil
	}
	if err := a.togglePanel(in.Panel); err != nil {
		return nil, err
	}
	return a.panels(), nil
}

// openInApps are the apps a plugin's Open in opens a workspace in, by the
// name it gives: on macOS the app's name for open -a, elsewhere its
// command; finder is the system's file manager.
var openInApps = map[string]struct{ mac, cmd string }{
	"cursor": {"Cursor", "cursor"},
	"vscode": {"Visual Studio Code", "code"},
	"zed":    {"Zed", "zed"},
	"finder": {"", ""},
}

// openInEffect is open.in: the thread's workspace, on this machine, in one
// of editors. The plugin names the thread, never a path.
func (a *app) openInEffect(args json.RawMessage) (any, error) {
	var in struct{ App, Thread string }
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if _, ok := openInApps[in.App]; !ok {
		return nil, fmt.Errorf("no app %q to open in: cursor, vscode, zed or finder", in.App)
	}
	t, err := a.threadNamed(in.Thread)
	if err != nil {
		return nil, err
	}
	if !t.local() || t.View.Workspace == "" {
		return nil, errors.New("the thread's workspace isn't on this machine")
	}
	return nil, openInApp(in.App, expandHome(t.View.Workspace))
}

// threadNamed is the thread a plugin names by its id.
func (a *app) threadNamed(id string) (thread, error) {
	for _, e := range a.backend.Envs() {
		for _, th := range e.Threads {
			if t := (thread{e, th}); t.id().String() == id {
				return t, nil
			}
		}
	}
	return thread{}, fmt.Errorf("no thread %q", id)
}

// diffEffect is diff.set, diff.setReviewed and diff.openFile. Each names
// the thread and, for a file, one the thread changed: a plugin reaches
// no other file.
func (a *app) diffEffect(typ string, args json.RawMessage) (any, error) {
	var in struct {
		Thread, Path, Shown, Layout string
		Wrap, Reviewed              *bool
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	t, err := a.threadNamed(in.Thread)
	if err != nil {
		return nil, err
	}
	files := a.rowsOf(t).files
	changed := func(path string) (fileChange, error) {
		if i := slices.IndexFunc(files, func(f fileChange) bool { return f.path == path }); i >= 0 {
			return files[i], nil
		}
		return fileChange{}, fmt.Errorf("the thread changed no file %q", path)
	}
	switch typ {
	case "diff.set":
		layouts := map[string]editor.DiffLayout{"unified": editor.DiffUnified, "split": editor.DiffSplit}
		layout, ok := layouts[in.Layout]
		if !ok && in.Layout != "" {
			return nil, fmt.Errorf("no layout %q: unified or split", in.Layout)
		}
		if in.Shown != "" {
			if _, err := changed(in.Shown); err != nil {
				return nil, err
			}
			a.diffFile[t.id()] = in.Shown
		}
		if ok {
			a.diffLayout = layout
		}
		if in.Wrap != nil {
			a.diffWrap = *in.Wrap
		}
		return nil, nil
	case "diff.setReviewed":
		f, err := changed(in.Path)
		if err != nil {
			return nil, err
		}
		d := a.diffFor(t, f)
		if d == nil {
			return nil, fmt.Errorf("the diff of %s isn't read yet", in.Path)
		}
		d.reviewed = in.Reviewed == nil || *in.Reviewed
		return nil, nil
	}
	f, err := changed(in.Path)
	if err != nil {
		return nil, err
	}
	ed := editorCLI()
	switch {
	case !t.local():
		return nil, errors.New("the thread's files aren't on this machine")
	case ed == "":
		return nil, errors.New("no editor to open the file in")
	}
	path := workingFile(t.View.Workspace, f.path)
	a.pluginLater = append(a.pluginLater, func(c *ui.Context) { a.openInEditor(c, ed, path) })
	return nil, nil
}

// openInApp opens the folder in the app, and doesn't wait for it.
var openInApp = func(app, folder string) error {
	e := openInApps[app]
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "darwin" && e.mac != "":
		cmd = exec.Command("open", "-a", e.mac, folder)
	case runtime.GOOS == "darwin":
		cmd = exec.Command("open", folder)
	case runtime.GOOS == "windows" && e.cmd == "":
		cmd = exec.Command("explorer", folder)
	case e.cmd == "":
		cmd = exec.Command("xdg-open", folder)
	default:
		cmd = exec.Command(e.cmd, folder)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
