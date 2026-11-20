package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	plugin "github.com/yxxbc/mygo-plugin"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/plugins"
	"glitched/internal/plugins/bundled"
	"glitched/internal/plugins/render"
	"glitched/internal/plugins/widget"
	"glitched/internal/transport"
)

// pluginKey names a page a plugin contributes.
type pluginKey struct{ plugin, page string }

func (k pluginKey) path() string {
	return "/plugins/" + url.PathEscape(k.plugin) + "/" + url.PathEscape(k.page)
}

// settingsPath is the page's route in Settings, where the plugin lists it.
func (k pluginKey) settingsPath() string {
	return "/settings/plugin/" + url.PathEscape(k.plugin) + "/" + url.PathEscape(k.page)
}

// pluginPage is what a plugin's stock page shows: what the plugin answered
// last, and what went wrong last, reading it or acting on it.
type pluginPage struct {
	page *plugin.Page
	err  error
	// read counts the reads asked for, so that only the last one's answer
	// shows.
	read    int
	reading bool
}

var pluginIcons = map[string]*ui.SVG{
	"server":   icServer,
	"sparkles": icSparkles,
	"code":     icCode,
	"globe":    icGlobe,
	"terminal": icTerminal,
	"file":     icFile,
	"wrap":     icWrap,
	"copy":     icCopy,
	"note":     icFile,
	"inbox":    icInbox,
	"clock":    icClock,
	"list":     icListChecks,
	"settings": icSettings,
	"zap":      icZap,
	"folder":   icFolder,
	"message":  icMessage,
}

// openPlugins loads the plugins installed in the state directory, whose
// pages and effects run through the app.
func (a *app) openPlugins(dir string) {
	// A plugin's links open web pages, never the app's own routes.
	a.pluginMD.OnLink = func(u string) {
		if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
			mygo.Shell.OpenExternal(u)
		}
	}
	a.plugins = plugins.Open(dir, plugins.Options{
		AppVersion: mygo.App.Version(),
		OpenURL:    mygo.Shell.OpenExternal,
		Post:       a.post,
		Do:         a.pluginEffect,
		// The built-in plugins: Header actions draws the thread header's
		// buttons till one of the user's replaces it.
		Bundled:     bundled.FS,
		ReleaseKeys: []string{bundled.ReleaseKey},
		SignOwn:     true,
		// Plugins with timers wait for Start, once the window runs and the
		// language is known.
		WaitToStart: true,
		Reloaded: func(string) {
			clear(a.pluginAssets)
			forgetPluginTerminals()
		},
		ServiceCall: func(ctx context.Context, id, method string, args json.RawMessage) (any, error) {
			call, _ := json.Marshal(plugins.ServiceCall{Plugin: id, Method: method, Args: args})
			out, err := a.serviceClient(dir).PluginCall(ctx, call)
			if err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

// pluginsChanged forgets what the plugins' pages showed, as after a
// reload, which closed them.
func (a *app) pluginsChanged(err error) {
	forgetPluginTerminals()
	clear(a.pluginPages)
	clear(a.pluginControls)
	clear(a.pluginAssets)
	if err != nil {
		a.pluginNotice(ui.Toast{Title: "Couldn't save the plugin settings", Description: err.Error()})
	}
}

// pluginNotice shows a toast on the next frame, as only a frame can.
func (a *app) pluginNotice(t ui.Toast) {
	a.pluginLater = append(a.pluginLater, func(c *ui.Context) { c.AddToast(t) })
}

// pluginNav lists the plugins' pages, over Settings. Choosing a stock page
// reads it again.
func (a *app) pluginNav(c *ui.Context) {
	for _, e := range a.plugins.Entries() {
		if e.Settings {
			continue
		}
		k := pluginKey{e.Plugin, e.Page}
		icon := appIconNamed(e.Icon)
		if icon == nil {
			icon = icPlug
		}
		if ghost(c, "plugin:"+k.path(), a.router.Path() == k.path()).Padding(7, 8).Children(func() {
			ui.Icon(c, icon).FontSize(15)
			ui.Text(c, e.Title).Grow(1).SingleLine()
		}).Clicked() {
			if !a.plugins.HasView(k.plugin) {
				a.readPluginPage(k)
			}
			if a.router.Path() != k.path() {
				a.router.Push(k.path())
			}
		}
	}
}

// readPluginPage asks the plugin for its stock page, off the UI thread, as
// it runs the plugin's code.
func (a *app) readPluginPage(k pluginKey) {
	st := a.pluginPages[k]
	if st == nil {
		st = &pluginPage{}
		a.pluginPages[k] = st
	}
	st.read++
	read := st.read
	st.reading = true
	go func() {
		page, err := a.plugins.Page(k.plugin, k.page)
		a.post(func() {
			if read != st.read {
				return
			}
			st.reading = false
			st.page, st.err = page, err
		})
	}()
}

// actOnPluginPage does what an item's action asks, off the UI thread. A
// command may change what the page shows, so the page is read again after
// one.
func (a *app) actOnPluginPage(k pluginKey, act plugin.Action) {
	go func() {
		err := a.plugins.Act(k.plugin, act)
		a.post(func() {
			st := a.pluginPages[k]
			if st == nil {
				return // the plugins reloaded meanwhile
			}
			if err != nil {
				st.err = err
				return
			}
			if act.Kind == "command" {
				a.readPluginPage(k)
			}
		})
	}()
}

func (a *app) pluginsPage(c *ui.Context, r *ui.Route) {
	k := pluginKey{r.Param("plugin"), r.Param("page")}
	title, subtitle, busy, body := a.pluginPageParts(k)
	r.Title(title)
	bar := c.TitleBar()
	left := 24 + bar.Left*(1-a.sidebarOpen)
	ui.Column(c).Fill().Children(func() {
		head := ui.Row(c).Height(max(bar.Height, headerHeight)).Shrink(0).Padding(0, 12+bar.Right, 0, left).Gap(8).AlignItems(ui.Center)
		head.DragWindow().Children(func() {
			if !a.showSidebar {
				if a.iconButton(c, "show-sidebar", icPanelLeft, "Show sidebar  ⌘B", false).Clicked() {
					a.toggleSidebar(false)
				}
			}
			ui.Text(c, a.plugins.Name(k.plugin)).TextColor(palette.muted)
			ui.Text(c, "/").TextColor(palette.faint)
			ui.Text(c, title).FontWeight(600)
			if busy {
				spin := ui.Icon(c, icLoader).Key("busy").FontSize(12).TextColor(palette.muted)
				spin.Rotate(spin.Loop("spin", 800*time.Millisecond, ui.Linear) * 360)
			}
		})
		main := ui.Column(c).Children(func() {
			a.page(c, title, subtitle, body)
		})
		a.contentCard(main.Grow(1))
	})
}

// pluginPageParts are what a plugin's page shows: its title and subtitle,
// whether it is busy, and its body. A stock page is read again when no
// read of it is under way.
func (a *app) pluginPageParts(k pluginKey) (title, subtitle string, busy bool, body func(c *ui.Context)) {
	title = k.page
	for _, e := range a.plugins.Entries() {
		if e.Plugin == k.plugin && e.Page == k.page {
			title = e.Title
		}
	}
	if a.plugins.HasView(k.plugin) {
		s, err := a.plugins.Session(k.plugin, k.page)
		switch {
		case err != nil:
			body = func(c *ui.Context) { ui.Text(c, err.Error()).Selectable().TextColor(palette.red) }
		default:
			if s.View != nil {
				title = cmpOr(s.View.Title, title)
				subtitle = s.View.Subtitle
			}
			busy = s.Busy
			body = func(c *ui.Context) { a.pluginView(c, k, s) }
		}
	} else {
		if a.pluginPages[k] == nil {
			a.readPluginPage(k)
		}
		st := a.pluginPages[k]
		if st.page != nil {
			title = cmpOr(st.page.Title, title)
			subtitle = st.page.Subtitle
		}
		body = func(c *ui.Context) { a.pluginBody(c, k, st) }
	}
	return title, subtitle, busy, body
}

func cmpOr(s, or string) string {
	if s != "" {
		return s
	}
	return or
}

// pluginView draws a native view page: what went wrong, with a way to
// start over, then the plugin's widgets.
func (a *app) pluginView(c *ui.Context, k pluginKey, s *plugins.Session) {
	cs := a.pluginControls[k]
	if cs == nil {
		cs = &render.Controls{}
		a.pluginControls[k] = cs
	}
	cs.Sync(s.View, s.Rev, s.Processed)
	if s.Err != nil {
		ui.Row(c).Key("error").Gap(12).Padding(12, 14).Radius(10).Border(1, palette.red.Alpha(0.4)).Background(palette.red.Alpha(0.08)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, s.Err.Error()).Selectable().TextColor(palette.red).Grow(1).Shrink(1)
			ui.Button(c.Key("retry"), "Start over").OnClick(func() {
				delete(a.pluginControls, k)
				s.Retry()
			})
		})
	}
	if s.View == nil {
		if s.Err == nil {
			ui.Text(c, "Loading…").Key("loading").TextColor(palette.muted)
		}
		return
	}
	st := a.pluginStyle(k.plugin)
	st.Scope = k.plugin + "/" + k.page
	st.Rows = func(list string, it widget.ListItem) *widget.Node { return s.Row(plugins.RowAt{List: list}, it) }
	render.View(c, s.View, cs, st, &a.pluginMD, s.Dispatch)
}

// pluginBody draws a stock page.
func (a *app) pluginBody(c *ui.Context, k pluginKey, st *pluginPage) {
	if st.err != nil {
		ui.Text(c, st.err.Error()).Key("error").Selectable().TextColor(palette.red)
	}
	p := st.page
	if p == nil {
		if st.reading {
			ui.Text(c, "Loading…").Key("loading").TextColor(palette.muted)
		}
		return
	}
	if p.Markdown != "" {
		ui.Column(c).Key("markdown").LineHeight(1.55).Children(func() {
			markdown.View(c, p.Markdown, &a.pluginMD)
		})
	}
	for i, s := range p.Sections {
		ui.Column(c).Key(i).Children(func() {
			card(c, strings.ToUpper(s.Title), func() {
				if len(s.Items) == 0 {
					ui.Text(c, "Nothing here.").Key("none").TextColor(palette.muted).Padding(14, 16)
				}
				for _, item := range s.Items {
					a.pluginItem(c, k, item)
				}
			})
		})
	}
	if p.Markdown == "" && len(p.Sections) == 0 {
		ui.Text(c, "Nothing here.").Key("empty").TextColor(palette.muted)
	}
}

func (a *app) pluginItem(c *ui.Context, k pluginKey, item plugin.Item) {
	ui.Row(c).Key(item.ID).Padding(14, 16).Gap(16).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			ui.Text(c, item.Title).FontWeight(500)
			if item.Subtitle != "" {
				ui.Text(c, item.Subtitle).FontSize(12).TextColor(palette.muted)
			}
		})
		for _, act := range item.Actions {
			ui.Button(c.Key("action:"+act.ID), act.Title).OnClick(func() { a.actOnPluginPage(k, act) })
		}
	})
}

// pluginCommands are the plugins' commands, as the palette offers them.
func (a *app) pluginCommands() []command {
	var out []command
	for _, pc := range a.plugins.Commands() {
		out = append(out, command{id: "plugin:" + pc.Plugin + ":" + pc.ID, section: "Actions", label: pc.PluginName + ": " + pc.Title, icon: "plug-circle", run: func() {
			a.plugins.RunCommand(pc.Plugin, pc.ID, nil, func(err error) {
				if err != nil {
					a.pluginNotice(ui.Toast{Title: pc.PluginName + ": " + pc.Title + " failed", Description: err.Error()})
				}
			})
		}})
	}
	return out
}

var statusNames = map[status]string{statusIdle: "idle", statusWorking: "working", statusCompleted: "completed", statusApproval: "waiting"}

// commandRecord is one of the palette's actions as glitched.commands.list
// gives it.
type commandRecord struct {
	ID      string `json:"id"`
	Section string `json:"section"`
	Label   string `json:"label"`
	Hint    string `json:"hint,omitempty"`
	Icon    string `json:"icon"`
	Also    string `json:"also,omitempty"`
}

// runCommand is glitched.commands.run: what choosing the action of that
// id in the palette does, closing it, and only while the palette could be
// opened to choose it. An action that deletes asks first, as there.
func (a *app) runCommand(id string) error {
	if a.removeOpen || a.deleteOpen {
		return errors.New("a confirm waits for the user")
	}
	for _, cmd := range a.actions() {
		if cmd.id == id {
			a.cmdkOpen = false
			cmd.run()
			return nil
		}
	}
	return fmt.Errorf("no command %q: glitched.commands.list names them", id)
}

// pluginEffect does what the plugin id asked for, which the host already
// checked it may. It runs on the UI thread.
func (a *app) pluginEffect(id string, e plugins.Effect) (any, error) {
	name := a.plugins.Name(id)
	switch e.Type {
	case "toast":
		var args struct{ Title, Description string }
		if err := json.Unmarshal(e.Args, &args); err != nil || args.Title == "" {
			return nil, errors.New("toast needs a title")
		}
		// The plugin's name goes with it: a plugin can't speak as the app.
		a.pluginNotice(ui.Toast{Title: args.Title, Description: strings.TrimPrefix(args.Description+" · "+name, " · "), Type: "info"})
		return nil, nil
	case "clipboard.write":
		var args struct{ Text string }
		if err := json.Unmarshal(e.Args, &args); err != nil {
			return nil, err
		}
		a.pluginLater = append(a.pluginLater, func(c *ui.Context) { c.WriteClipboard(args.Text) })
		return nil, nil
	case "notify":
		var args struct{ Title, Body, Thread string }
		if err := json.Unmarshal(e.Args, &args); err != nil || args.Title == "" {
			return nil, errors.New("notify needs a title")
		}
		if a.notify == nil {
			return nil, errors.New("notifications are off")
		}
		// A notification about a thread opens it when clicked, as the
		// app's own do; naming one needs threads:read.
		nid := "plugin:" + id
		if args.Thread != "" {
			if !a.plugins.Allowed(id, "threads:read") {
				return nil, fmt.Errorf("%w: naming a thread needs threads:read", plugins.ErrDenied)
			}
			t, err := a.pluginFind(e.Args)
			if err != nil {
				return nil, err
			}
			nid = "thread:" + t.id().String()
		}
		a.notify(mygo.NotificationOptions{ID: nid, Title: args.Title, Body: args.Body, Subtitle: name, Group: "plugins"})
		return nil, nil
	case "panels.toggle":
		return a.panelEffect(e.Args)
	case "panels.state":
		return a.panels(), nil
	case "open.in":
		return a.openInEffect(e.Args)
	case "app.checkForUpdates":
		return a.checkForUpdatesEffect()
	case "app.settings.get":
		return a.appSettings(), nil
	case "app.settings.set":
		return a.setAppSetting(e.Args)
	case "diff.set", "diff.setReviewed", "diff.openFile":
		return a.diffEffect(e.Type, e.Args)
	case "tab.open":
		var args struct{ Tab string }
		if err := json.Unmarshal(e.Args, &args); err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(a.plugins.Ext(id).Tabs, func(t plugins.Tab) bool { return t.ID == args.Tab }) {
			return nil, fmt.Errorf("the plugin has no tab %q", args.Tab)
		}
		a.openPluginTab(pluginTab{id, args.Tab}, true)
		if a.router.Path() != "/" {
			a.router.Push("/")
		}
		return nil, nil
	case "page.open":
		var args struct{ Page string }
		if err := json.Unmarshal(e.Args, &args); err != nil {
			return nil, err
		}
		for _, en := range a.plugins.Entries() {
			if en.Plugin == id && en.Page == args.Page {
				k := pluginKey{id, args.Page}
				path := k.path()
				if en.Settings {
					path = k.settingsPath()
				}
				a.router.Push(path)
				return nil, nil
			}
		}
		return nil, fmt.Errorf("the plugin has no page %q", args.Page)
	case "composer.draft":
		var args struct{ Text, Workspace, Thread string }
		if err := json.Unmarshal(e.Args, &args); err != nil {
			return nil, err
		}
		// In a thread's composer: the thread is shown, with the draft.
		if args.Thread != "" {
			if !a.plugins.Allowed(id, "threads:read") {
				return nil, fmt.Errorf("%w: drafting in a thread needs threads:read", plugins.ErrDenied)
			}
			t, err := a.pluginFind(e.Args)
			if err != nil {
				return nil, err
			}
			a.select_(t)
			a.draft, a.code = args.Text, codeBlock{}
			a.focusPane(0, -1)
			return nil, nil
		}
		ws, err := pluginWorkspace(args.Workspace)
		if err != nil {
			return nil, err
		}
		a.startIn(draft{nil, ws})
		a.draft, a.code = args.Text, codeBlock{}
		a.focusPane(0, -1)
		return nil, nil
	case "threads.list":
		var ts []thread
		for _, env := range a.backend.Envs() {
			for _, th := range env.Threads {
				ts = append(ts, thread{env, th})
			}
		}
		slices.SortFunc(ts, newestFirst)
		out := []plugins.ThreadRecord{}
		for _, t := range ts {
			out = append(out, a.threadRecord(t))
		}
		return out, nil
	case "threads.open":
		t, err := a.pluginFind(e.Args)
		if err != nil {
			return nil, err
		}
		a.select_(t)
		return nil, nil
	case "threads.rename":
		var args struct{ Title string }
		json.Unmarshal(e.Args, &args)
		s := strings.TrimSpace(args.Title)
		if s == "" || len(s) > 200 {
			return nil, errors.New("threads.rename needs a title of up to 200 bytes")
		}
		t, err := a.pluginFind(e.Args)
		if err != nil {
			return nil, err
		}
		a.backend.Rename(t.env, t.id(), s)
		return nil, nil
	case "projects.list":
		return a.projectRecords(), nil
	case "projects.filter":
		return a.filterEffect(e.Args)
	case "projects.add":
		a.addProject()
		return nil, nil
	case "threads.setDisposition":
		return a.setDispositionEffect(e.Args)
	case "models.list":
		return modelRecords(), nil
	case "threads.configure":
		return a.configureEffect(e.Args)
	case "threads.setAccess":
		return a.setAccessEffect(e.Args)
	case "composer.submit":
		return a.submitEffect(e.Args)
	case "threads.markRead":
		t, err := a.pluginFind(e.Args)
		if err != nil {
			return nil, err
		}
		a.backend.MarkRead(t.Thread)
		return nil, nil
	case "threads.interrupt":
		t, err := a.pluginFind(e.Args)
		if err != nil {
			return nil, err
		}
		if t.View.Live() {
			a.backend.Interrupt(t.env, t.id())
		}
		return nil, nil
	case "threads.delete":
		// Glitched asks the user first, whatever the settings say: a
		// plugin never deletes unasked.
		t, err := a.pluginFind(e.Args)
		if err != nil {
			return nil, err
		}
		if t.View.Live() {
			return nil, errors.New("the thread's agent runs: stop it first")
		}
		// A confirm stays about the thread it asked about till it closes.
		if a.deleteOpen || a.deleting.Thread != nil {
			return nil, errors.New("another delete waits for the user")
		}
		a.deleting, a.deleteOpen = t, true
		return nil, nil
	case "commands.list":
		out := []commandRecord{}
		for _, cmd := range a.actions() {
			out = append(out, commandRecord{cmd.id, cmd.section, cmd.label, cmd.hint, cmd.icon, cmd.also})
		}
		return out, nil
	case "commands.run":
		var args struct{ ID string }
		json.Unmarshal(e.Args, &args)
		return nil, a.runCommand(args.ID)
	case "projects.remove":
		var args struct{ Workspace, Machine string }
		json.Unmarshal(e.Args, &args)
		var found []*project
		for _, p := range a.projects {
			if p.key.workspace == args.Workspace && (args.Machine == "" || p.env.Name == args.Machine) {
				found = append(found, p)
			}
		}
		switch {
		case len(found) == 0:
			return nil, fmt.Errorf("no project %q", args.Workspace)
		case len(found) > 1:
			return nil, fmt.Errorf("%q is a project on more than one machine: name the machine", args.Workspace)
		case a.projectLive(found[0]):
			return nil, errors.New("an agent runs in the project: stop it first")
		case a.removeOpen:
			return nil, errors.New("another removal waits for the user")
		}
		a.askRemove(found[0])
		return nil, nil
	case "threads.send":
		var args struct{ Text string }
		if err := json.Unmarshal(e.Args, &args); err != nil || strings.TrimSpace(args.Text) == "" {
			return nil, errors.New("threads.send needs text")
		}
		t, err := a.pluginFind(e.Args)
		if err != nil {
			return nil, err
		}
		cmd := a.backend.Send(t.env, t.id(), args.Text, api.SteerActive)
		if cmd.IsZero() {
			return nil, errors.New("the thread's engine isn't reached")
		}
		return map[string]string{"message": cmd.String()}, nil
	case "threads.create":
		var args struct{ Prompt, Title, Workspace string }
		if err := json.Unmarshal(e.Args, &args); err != nil || strings.TrimSpace(args.Prompt) == "" {
			return nil, errors.New("threads.create needs a prompt")
		}
		ws, err := pluginWorkspace(args.Workspace)
		if err != nil {
			return nil, err
		}
		envs := a.backend.Envs()
		if len(envs) == 0 {
			return nil, errors.New("no engine is reached")
		}
		title := cmpOr(args.Title, args.Prompt)
		if r := []rune(title); len(r) > 48 {
			title = string(r[:48]) + "…"
		}
		tid := a.backend.Create(envs[0], ws, title, a.modelRef(), accessOf[a.access])
		if tid.IsZero() {
			return nil, errors.New("this machine's engine isn't reached")
		}
		a.backend.Send(envs[0], tid, args.Prompt, api.SteerActive)
		return map[string]string{"thread": tid.String()}, nil
	}
	return nil, fmt.Errorf("unknown effect %q", e.Type)
}

// pluginFind is the thread an effect names in its thread field.
func (a *app) pluginFind(args json.RawMessage) (thread, error) {
	var ref struct{ Thread string }
	if err := json.Unmarshal(args, &ref); err != nil {
		return thread{}, err
	}
	id, err := api.ParseID(ref.Thread)
	if err != nil {
		return thread{}, fmt.Errorf("no thread %q", ref.Thread)
	}
	t, ok := a.find(id)
	if !ok {
		return thread{}, fmt.Errorf("no thread %q", ref.Thread)
	}
	return t, nil
}

// pluginWorkspace is the folder an effect names, which must be one on
// this machine; the home folder when it names none.
func pluginWorkspace(ws string) (string, error) {
	if ws == "" {
		return homeDir(), nil
	}
	ws = expandHome(ws)
	if !filepath.IsAbs(ws) {
		return "", fmt.Errorf("workspace %q is not an absolute path", ws)
	}
	if fi, err := os.Stat(ws); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("workspace %q is not a folder", ws)
	}
	return filepath.Clean(ws), nil
}

// serviceClient reaches the plugins' services where this machine's engine
// runs them, on its unix socket.
func (a *app) serviceClient(dir string) *transport.EndpointClient {
	a.serviceMu.Lock()
	defer a.serviceMu.Unlock()
	if a.services == nil {
		a.services = transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	}
	return a.services
}
