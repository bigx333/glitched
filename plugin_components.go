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

	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// pluginComponent draws the component name, for the copy key, in the box
// it makes, with the first plugin that replaces it and has a good
// drawing: the user's before the built-in. Each is given the copy, so
// that one failing falls back to the next with no gap. context is what a
// plugin is given, from the thread's record as that plugin may see it.
// The native widgets it places draw about t. t is nil for a component of
// the app's own, as a settings page, whose record is zero.
func (a *app) pluginComponent(c *ui.Context, box func() ui.Element, name, key string, t *thread, context func(plugins.ThreadRecord) any) {
	if a.plugins == nil {
		return
	}
	f := a.frameOf(name)
	var full plugins.ThreadRecord
	if t != nil {
		full = a.threadRecord(*t)
	}
	type drawing struct {
		pl   plugins.PlaceAt
		ctx  any
		app  *plugins.Session
		node *widget.Node
	}
	var good, failed *drawing
	var undrawn []drawing
	for _, pl := range a.plugins.Replacers(name) {
		ctx := context(a.plugins.Record(pl.Plugin, full))
		version, _ := json.Marshal(ctx)
		fk := [2]string{pl.Plugin, pl.Place.ID}
		f.items[fk] = append(f.items[fk], plugins.PlaceItem{Key: key, Version: string(version), Context: ctx})
		app := a.plugins.App(pl.Plugin)
		if good != nil || app == nil {
			continue
		}
		node := app.PlaceView(pl.Place.ID, key)
		switch {
		case node == nil:
			undrawn = append(undrawn, drawing{pl, ctx, app, nil})
		case app.Err == nil:
			good = &drawing{pl, ctx, app, node}
		case failed == nil:
			failed = &drawing{pl, ctx, app, node}
		}
	}
	// A copy none has drawn yet, as a panel opening, is drawn before it
	// shows, the user's first: it never opens empty.
	if good == nil && failed == nil {
		deadline := time.Now().Add(firstDrawWait)
		for _, u := range undrawn {
			if u.node = u.app.DrawFirst(u.pl.Place.ID, f.items[[2]string{u.pl.Plugin, u.pl.Place.ID}], key, deadline); u.node != nil && u.app.Err == nil {
				good = &u
				break
			}
		}
	}
	// When every one failed, the first one's last good drawing stays: a
	// row that can still be clicked beats none.
	d := cmp.Or(good, failed)
	if d == nil {
		return
	}
	a.drawPlace(c, box(), d.pl, key, d.ctx, d.app, d.node, func(c *ui.Context, n *widget.Node, fire func(any)) { a.drawNative(c, name, t, n, fire) })
}

// drawNative draws the native widget n in a copy of the component about
// t: only in the components it names.
func (a *app) drawNative(c *ui.Context, component string, t *thread, n *widget.Node, fire func(any)) {
	in := widget.Natives[n.Name].In
	if !slices.Contains(in, component) || widget.Natives[n.Name].Thread && t == nil {
		ui.Text(c, n.Name+" draws only in "+strings.Join(in, ", ")+".").TextColor(palette.muted).Margin(ui.Auto)
		return
	}
	switch n.Name {
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
	files := changedFiles(entriesOf(t))
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
