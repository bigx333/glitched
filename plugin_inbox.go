package main

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plugins"
)

// rowActions is the context of thread.row.actions: the thread, the
// section its row shows in, and the times the app offers to snooze it
// until.
type rowActions struct {
	Thread  plugins.ThreadRecord `json:"thread"`
	Section string               `json:"section"`
	Snooze  []snoozeChoice       `json:"snooze"`
}

type snoozeChoice struct {
	When  string `json:"when"`
	Joke  string `json:"joke"`
	Emoji string `json:"emoji"`
	Until string `json:"until"`
}

// rowActions are a pointed-at thread's actions, a plugin's: the built-in
// snoozes and settles it, or sends it back to the inbox. They stay while
// its popover is open, which takes the pointer away from the row.
func (a *app) rowActions(c *ui.Context, t thread, cat category) {
	var snooze []snoozeChoice
	for _, s := range snoozeTimes {
		snooze = append(snooze, snoozeChoice{s.when, s.joke, s.emoji, s.until(a.now).UTC().Format(time.RFC3339)})
	}
	box := func() ui.Element { return ui.Row(c).Key("component:thread.row.actions").Gap(0) }
	a.popoverOpen = false
	a.pluginComponent(c, "thread.row.actions", &t, componentCopy{key: t.id().String(), box: box, context: func(r plugins.ThreadRecord) any {
		// A plugin that can't read threads isn't told where it is.
		section := sectionNames[cat]
		if r.Section == "" {
			section = ""
		}
		return rowActions{r, section, snooze}
	}})
	switch {
	case a.popoverOpen:
		a.snoozeFor = t.id()
	case a.snoozeFor == t.id():
		a.snoozeFor = api.ID{}
	}
}

// projectRecord is a project as plugins see it: Key names it to
// projects.filter and in a contextMenu, and Icon and Color are its mark.
type projectRecord struct {
	Key       string `json:"key"`
	Workspace string `json:"workspace"`
	Machine   string `json:"machine,omitempty"`
	Name      string `json:"name"`
	Icon      string `json:"icon"`
	Color     string `json:"color"`
	// Filtered is whether the sidebar is filtered to it.
	Filtered bool `json:"filtered,omitempty"`
	// Local is whether its workspace is on this machine.
	Local bool `json:"local,omitempty"`
}

// projectStrip is the context of sidebar.projects.
type projectStrip struct {
	Projects []projectRecord `json:"projects"`
	// Filtered is whether the sidebar shows only some projects; Picking
	// whether Add project's folder picker is open; Machines whether there
	// is more than one machine, whose names then tell projects apart.
	Filtered bool `json:"filtered"`
	Picking  bool `json:"picking"`
	Machines bool `json:"machines"`
}

func (k projectKey) String() string { return k.server + ":" + k.workspace }

// projectRecords are the sidebar's projects, in its order.
func (a *app) projectRecords() []projectRecord {
	a.syncMarks()
	out := []projectRecord{}
	for _, p := range a.projects {
		icon, color := p.markNames()
		machine := ""
		if p.env != nil {
			machine = p.env.Name
		}
		local := p.env == nil || p.env.Kind == client.Local
		out = append(out, projectRecord{p.key.String(), p.key.workspace, machine, p.name(), icon, color, a.projectFilter[p.key], local})
	}
	return out
}

// filterEffect is projects.filter: the sidebar shows only the projects
// named, or every one when none, or every one, is.
func (a *app) filterEffect(raw json.RawMessage) (any, error) {
	var args struct{ Keys []string }
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	keep := map[projectKey]bool{}
	for _, k := range args.Keys {
		i := slices.IndexFunc(a.projects, func(p *project) bool { return p.key.String() == k })
		if i < 0 {
			return nil, errors.New("projects.filter: no project " + k)
		}
		keep[a.projects[i].key] = true
	}
	clear(a.projectFilter)
	if len(keep) < len(a.projects) {
		maps.Copy(a.projectFilter, keep)
	}
	a.filterEdits++
	return nil, nil
}

// contextMenu gives e, in a plugin's view, the menu of Glitched's that
// name names: a thread's or a project's, which only a plugin that reads
// threads may open.
func (a *app) contextMenu(id string) func(c *ui.Context, e ui.Element, name string) {
	return func(c *ui.Context, e ui.Element, name string) {
		if !a.plugins.Allowed(id, "threads:read") {
			return
		}
		kind, ref, _ := strings.Cut(name, ":")
		switch kind {
		case "project":
			if i := slices.IndexFunc(a.projects, func(p *project) bool { return p.key.String() == ref }); i >= 0 {
				p := a.projects[i]
				e.ContextMenu(func(m *ui.Menu) { a.projectMenu(m, p) })
			}
		case "thread":
			id, err := api.ParseID(ref)
			if err != nil {
				return
			}
			if t, ok := a.find(id); ok {
				e.ContextMenu(func(m *ui.Menu) { a.threadMenu(c, m, t, a.categoryOf(t)) })
			}
		}
	}
}

// jokes are the row animations a plugin may play as it moves a thread,
// each with the app's toast, if it has one, and each only as the thread
// goes where its joke says.
var jokes = map[string]struct {
	kind  gagKind
	state string
}{"snooze": {gagSnooze, "snoozed"}, "settle": {gagSettle, "settled"}, "undo": {gagUndo, "inbox"}}

// setDispositionEffect is threads.setDisposition: the thread goes to the
// inbox, is snoozed till a time to come, or is settled, and its row plays
// the joke the plugin names, as the app's own actions do.
func (a *app) setDispositionEffect(raw json.RawMessage) (any, error) {
	var args struct{ State, Until, Animation string }
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	joke, joking := jokes[args.Animation]
	switch {
	case args.Animation != "" && !joking:
		return nil, errors.New("threads.setDisposition: animation is snooze, settle or undo")
	case joking && joke.state != args.State:
		return nil, errors.New("threads.setDisposition: " + args.Animation + " plays only as a thread is " + joke.state)
	}
	kind := joke.kind
	t, err := a.pluginFind(raw)
	if err != nil {
		return nil, err
	}
	var d api.Disposition
	switch args.State {
	case "inbox":
	case "settled":
		d.State = api.Settled
	case "snoozed":
		until, err := time.Parse(time.RFC3339, args.Until)
		if err != nil || !until.After(a.now) {
			return nil, errors.New("threads.setDisposition: a snooze needs an until to come, in RFC 3339")
		}
		d = api.Disposition{State: api.Snoozed, Until: until}
	default:
		return nil, errors.New("threads.setDisposition: state is inbox, snoozed or settled")
	}
	from := a.categoryOf(t)
	a.backend.SetDisposition(t.env, t.id(), d)
	if !joking {
		return nil, nil
	}
	a.pluginLater = append(a.pluginLater, func(c *ui.Context) {
		a.gag(c, t.id(), kind, from)
		switch kind {
		case gagSnooze:
			c.AddToast(ui.Toast{Title: "Sleeping until " + lower(snoozeWhen(d.Until, a.now)) + " 💤", Description: t.View.Title, Type: "info"})
		case gagSettle:
			c.AddToast(ui.Toast{Title: "Settled. Phew.", Description: title(t)})
		}
	})
	return nil, nil
}

// snoozeWhen names a snooze's time as the app's choices do, when it is
// one of them, and as the sidebar shows it otherwise.
func snoozeWhen(until, now time.Time) string {
	if i := slices.IndexFunc(snoozeTimes, func(s struct {
		when, joke, emoji string
		until             func(now time.Time) time.Time
	}) bool {
		return s.until(now).Equal(until)
	}); i >= 0 {
		return snoozeTimes[i].when
	}
	return snoozeLabel(until, now)
}
