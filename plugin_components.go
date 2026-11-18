package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
)

// pluginComponent draws the component name, for the copy key, with the
// first plugin that replaces it and has a good drawing: the user's before
// the built-in. Each is given the copy, so that one failing falls back to
// the next with no gap. context is what a plugin is given, from the
// thread's record as that plugin may see it.
func (a *app) pluginComponent(c *ui.Context, name, key string, t thread, context func(plugins.ThreadRecord) any) {
	if a.plugins == nil {
		return
	}
	f := a.frameOf(name)
	full := a.threadRecord(t)
	drawn := false
	for _, pl := range a.plugins.Replacers(name) {
		ctx := context(a.plugins.Record(pl.Plugin, full))
		version, _ := json.Marshal(ctx)
		fk := [2]string{pl.Plugin, pl.Place.ID}
		f.items[fk] = append(f.items[fk], plugins.PlaceItem{Key: key, Version: string(version), Context: ctx})
		app := a.plugins.App(pl.Plugin)
		if drawn || app == nil || app.Err != nil {
			continue
		}
		node := app.PlaceView(pl.Place.ID, key)
		if node == nil {
			continue
		}
		drawn = true
		a.drawPlace(c, ui.Row(c).Key("component:"+name).Shrink(0).AlignItems(ui.Center), pl, key, ctx, app, node)
	}
}

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
	for _, e := range a.backend.Envs() {
		for _, th := range e.Threads {
			t := thread{e, th}
			if t.id().String() != in.Thread {
				continue
			}
			if !t.local() || t.View.Workspace == "" {
				return nil, errors.New("the thread's workspace isn't on this machine")
			}
			return nil, openInApp(in.App, expandHome(t.View.Workspace))
		}
	}
	return nil, fmt.Errorf("no thread %q", in.Thread)
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
