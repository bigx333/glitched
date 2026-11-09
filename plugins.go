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
	"glitched/internal/plugins/render"
	"glitched/internal/transport"
)

// pluginKey names a page a plugin contributes.
type pluginKey struct{ plugin, page string }

func (k pluginKey) path() string {
	return "/plugins/" + url.PathEscape(k.plugin) + "/" + url.PathEscape(k.page)
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
	"sparkles": icSparkles,
	"code":     icCode,
	"globe":    icGlobe,
	"terminal": icTerminal,
	"file":     icFile,
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
		Language:   mygo.App.Locale,
		OpenURL:    mygo.Shell.OpenExternal,
		Post:       a.post,
		Do:         a.pluginEffect,
		Reloaded:   func(string) { clear(a.pluginAssets) },
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
	title := k.page
	for _, e := range a.plugins.Entries() {
		if e.Plugin == k.plugin && e.Page == k.page {
			title = e.Title
		}
	}
	var subtitle string
	var body func(c *ui.Context)
	busy := false
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
	render.View(c, s.View, cs, a.pluginStyle(k.plugin), &a.pluginMD, s.Dispatch)
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
		out = append(out, command{section: "Actions", label: pc.PluginName + ": " + pc.Title, icon: icPlug, run: func() {
			a.plugins.RunCommand(pc.Plugin, pc.ID, func(err error) {
				if err != nil {
					a.pluginNotice(ui.Toast{Title: pc.PluginName + ": " + pc.Title + " failed", Description: err.Error()})
				}
			})
		}})
	}
	return out
}

// pluginThread is a thread, as a plugin sees it.
type pluginThread struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Workspace string    `json:"workspace"`
	Status    string    `json:"status"`
	Machine   string    `json:"machine"`
	UpdatedAt time.Time `json:"updatedAt"`
}

var statusNames = map[status]string{statusIdle: "idle", statusWorking: "working", statusCompleted: "completed", statusApproval: "waiting"}

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
		var args struct{ Title, Body string }
		if err := json.Unmarshal(e.Args, &args); err != nil || args.Title == "" {
			return nil, errors.New("notify needs a title")
		}
		if a.notify == nil {
			return nil, errors.New("notifications are off")
		}
		a.notify(mygo.NotificationOptions{ID: "plugin:" + id, Title: args.Title, Body: args.Body, Subtitle: name, Group: "plugins"})
		return nil, nil
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
				a.router.Push(pluginKey{id, args.Page}.path())
				return nil, nil
			}
		}
		return nil, fmt.Errorf("the plugin has no page %q", args.Page)
	case "composer.draft":
		var args struct{ Text, Workspace string }
		if err := json.Unmarshal(e.Args, &args); err != nil {
			return nil, err
		}
		ws, err := pluginWorkspace(args.Workspace)
		if err != nil {
			return nil, err
		}
		a.startIn(draft{nil, ws})
		a.draft = args.Text
		a.draftArea.Focus()
		return nil, nil
	case "threads.list":
		out := []pluginThread{}
		for _, env := range a.backend.Envs() {
			for _, th := range env.Threads {
				t := thread{env, th}
				out = append(out, pluginThread{t.id().String(), t.View.Title, t.View.Workspace, statusNames[a.statusOf(t)], env.Name, t.View.UpdatedAt})
			}
		}
		slices.SortFunc(out, func(x, y pluginThread) int { return y.UpdatedAt.Compare(x.UpdatedAt) })
		return out, nil
	case "threads.open":
		t, err := a.pluginFind(e.Args)
		if err != nil {
			return nil, err
		}
		a.select_(t)
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
