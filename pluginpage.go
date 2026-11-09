package main

import (
	"net/url"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	plugin "github.com/yxxbc/mygo-plugin"
)

// pluginKey names a page a plugin contributes.
type pluginKey struct{ plugin, page string }

func (k pluginKey) path() string {
	return "/plugins/" + url.PathEscape(k.plugin) + "/" + url.PathEscape(k.page)
}

// pluginPage is what a plugin's page shows: what the plugin answered last,
// and what went wrong last, reading it or acting on it.
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
}

// pluginNav lists the plugins' pages, over Settings. Choosing one reads it
// again.
func (a *app) pluginNav(c *ui.Context) {
	for _, e := range a.plugins.Entries() {
		k := pluginKey{e.Plugin, e.Page}
		icon := pluginIcons[e.Icon]
		if icon == nil {
			icon = icPlug
		}
		if ghost(c, "plugin:"+k.path(), a.router.Path() == k.path()).Padding(7, 8).Children(func() {
			ui.Icon(c, icon).FontSize(15)
			ui.Text(c, e.Title).Grow(1).SingleLine()
		}).Clicked() {
			a.readPluginPage(k)
			if a.router.Path() != k.path() {
				a.router.Push(k.path())
			}
		}
	}
}

// readPluginPage asks the plugin for its page, off the UI thread, as it
// runs the plugin's code.
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
			if err != nil {
				a.pluginPages[k].err = err
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
	if a.pluginPages[k] == nil {
		a.readPluginPage(k)
	}
	st := a.pluginPages[k]
	title, subtitle := k.page, ""
	for _, e := range a.plugins.Entries() {
		if e.Plugin == k.plugin && e.Page == k.page {
			title = e.Title
		}
	}
	if st.page != nil {
		if st.page.Title != "" {
			title = st.page.Title
		}
		subtitle = st.page.Subtitle
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
		})
		body := ui.Column(c).Children(func() {
			a.page(c, title, subtitle, func(c *ui.Context) { a.pluginBody(c, k, st) })
		})
		a.contentCard(body.Grow(1))
	})
}

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
			markdown.View(c, p.Markdown, &a.md)
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
