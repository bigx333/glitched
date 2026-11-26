package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
)

// Managers are persistent coordinator threads. They never go to the inbox's
// sections: they have their own, over it, and are never snoozed or settled.
// Each has a header popover for its sweeps, an overview panel beside the
// chat with what needs the user, its workers, the workspace's shared docs
// and its sweeps, and a welcome of its own while it has no messages.

// managerUI is what the app keeps of its Managers' UI.
type managerUI struct {
	// closed is whether the sidebar's Managers section is collapsed.
	closed bool
	// showPanel is whether the overview panel shows beside a Manager's
	// chat, while no other panel does.
	showPanel bool
	// sweepPopover is the Manager whose header sweep popover is open.
	sweepPopover api.ID
	// promptDraft is the standing sweep prompt being edited, of the
	// Manager promptFor.
	promptFor   api.ID
	promptDraft string
	dialog      managerDialog
	// docs are the shared context docs of each local workspace root.
	docs map[string]*workspaceDocs
}

// managerDialog is the New manager dialog: the Manager's name, the project
// it coordinates (scratch for none, which makes it a folder of its own),
// its sweeps and its standing sweep prompt. auto is the name the dialog
// filled in, which follows the project until the user types another.
type managerDialog struct {
	open         bool
	name, auto   string
	project      projectKey
	env          *client.Env
	sweepSeconds int64
	prompt       string
	scratch      bool
}

// workspaceDocs are a workspace root's shared context docs, as read off
// the UI thread: gen tells the read that's current from those it replaced.
type workspaceDocs struct {
	loading  bool
	loadedAt time.Time
	gen      uint64
	files    []managerDoc
	expanded map[string]bool
	err      string
}

// managerDoc is one shared context doc: its size on disk, and as much of
// its text as a Manager is given.
type managerDoc struct {
	name      string
	exists    bool
	size      int64
	truncated bool
	omitted   bool
	text      string
}

// managerDocNames are the docs the engine injects into a Manager and its
// workers (internal/runtime/manager_docs.go), managerDocLimit how much of
// each it reads, and managerDocTotalLimit the combined cap across all files.
var managerDocNames = []string{"AGENTS.md", "WORKERS.md", "NOTES.md"}

const (
	managerDocLimit      = 16 << 10
	managerDocTotalLimit = 32 << 10
)

// managerStarters are what Create writes into a missing doc.
var managerStarters = map[string]string{
	"WORKERS.md": "# Workers\n\nWho works on what. The manager keeps this current.\n\n| Worker | Task | Branch | Status |\n| --- | --- | --- | --- |\n",
	"NOTES.md":   "# Notes\n\nStanding decisions, open questions and follow-ups for this workspace.\n\n## Open items\n\n- \n",
}

// sweepChoices are the sweep cadences offered, with their short labels.
type sweepChoice struct {
	secs  int64
	label string
}

var sweepChoices = []sweepChoice{{0, "Off"}, {900, "15m"}, {1800, "30m"}, {3600, "1h"}, {14400, "4h"}}

// name is the choice's name to assistive tools, and the menu's label.
func (s sweepChoice) name() string {
	if s.secs == 0 {
		return "Sweeps off"
	}
	return "Sweep every " + s.label
}

func isManager(t thread) bool { return t.Thread != nil && t.View.Role == api.RoleManager }

// managerItems are the Managers of the projects filtered to, every
// machine's together, the most recently updated first.
func (a *app) managerItems() []inboxItem {
	a.sortThreads()
	return a.sorted.managers
}

// sweepWithInterval is cur at secs, its prompt kept.
func sweepWithInterval(cur *api.SweepSchedule, secs int64) *api.SweepSchedule {
	next := &api.SweepSchedule{IntervalSeconds: secs}
	if cur != nil {
		next.Prompt = cur.Prompt
	}
	return next
}

func sweepSeconds(s *api.SweepSchedule) int64 {
	if s == nil {
		return 0
	}
	return max(s.IntervalSeconds, 0)
}

// effectiveSweep is t's sweep schedule, folded with any pending configure
// that has not failed, so back-to-back cadence and prompt edits compose
// instead of overwriting one another.
func effectiveSweep(t thread) *api.SweepSchedule {
	s := t.View.Sweep
	for _, p := range t.Pending {
		if p.Err != nil {
			continue
		}
		if c, ok := p.Body.(api.ThreadConfigure); ok && c.Sweep != nil {
			s = c.Sweep
		}
	}
	return s
}

// sweepBlocked is whether RunSweep would be refused right now: the Manager
// is running or holding queued work waiting to resume.
func sweepBlocked(t thread) bool {
	return t.View.Live() || t.View.Resumable || t.View.Queued > 0
}

// threadNeedsYou reports whether t needs the user's attention, why, and
// whether the reason is a failure. Remote placeholders expose RunWaiting
// even when their local Requests slice is empty.
func threadNeedsYou(t thread) (why string, isFail bool, ok bool) {
	switch {
	case len(t.View.Requests) > 0 || t.View.Run == api.RunWaiting:
		return "Waits on you", false, true
	case t.View.Run == api.RunUncertain:
		return "Interrupted", false, true
	case failedLast(t):
		return "Failed", true, true
	}
	return "", false, false
}

// managerAttention is how many of the Manager t and its workers wait on
// the user or failed, and how many workers work.
func (a *app) managerAttention(t thread) (needs, active int) {
	tree := a.shown(t)
	if _, _, ok := threadNeedsYou(t); ok {
		needs++
	}
	for _, n := range tree {
		if _, _, ok := threadNeedsYou(n.t); ok {
			needs++
		}
	}
	return needs, working(tree)
}

// --- Sidebar

// managersSection is the sidebar's Managers: a header that adds one and,
// with some, collapses them, over their rows, each with its workers.
func (a *app) managersSection(c *ui.Context) {
	items := a.managerItems()
	open := len(items) > 0 && !a.mgr.closed
	box := ui.Column(c).Key("managers").Padding(0, 8, 4).Gap(2).Shrink(0)
	if len(items) > 0 {
		box.BorderWidth(0, 0, 1, 0).BorderColor(palette.border)
	}
	box.Children(func() {
		ui.Row(c).Key("managers-head").AlignItems(ui.Center).Gap(2).Children(func() {
			var head ui.Element
			if len(items) > 0 {
				head = ghost(c, "managers-toggle", false).Label("Managers")
			} else {
				head = ui.Row(c).Key("managers-title").AlignItems(ui.Center)
			}
			head.Height(30).Padding(0, 8).Gap(8).Grow(1).Shrink(1).Children(func() {
				ui.Icon(c, icManager).FontSize(14).TextColor(palette.accent)
				ui.Text(c, "Managers").FontSize(12.5).FontWeight(600).TextColor(palette.text)
				if len(items) > 0 {
					a.managersCount(c, items)
					ui.Spacer(c)
					chevronAt(c, 90*float32(b2i(open))).TextColor(palette.faint)
				}
			})
			if len(items) > 0 && head.Clicked() {
				a.mgr.closed = !a.mgr.closed
			}
			if lost := a.lostManagers(); len(lost) > 0 {
				b := a.tip(c, ghost(c, "continue-all-managers", false).Padding(2, 8).Label("Continue all"),
					"Continue "+plural(len(lost), "manager")+" whose agent was cut off, two at a time")
				if b.Children(func() { ui.Text(c, "Continue all").FontSize(11.5) }).Clicked() {
					a.continueAll(lost)
					c.AddToast(ui.Toast{Title: "Continuing", Description: plural(len(lost), "manager"), Type: "info"})
				}
			}
			if a.iconButton(c, "new-manager", icPlus, "New manager", false).Padding(5).Clicked() {
				env, key := a.defaultManagerProject()
				a.openManagerDialog(env, key)
			}
		})
		if !open {
			return
		}
		rows := func() {
			for _, it := range items {
				a.managerRow(c, it)
				a.childRows(c, it.t, 1)
			}
		}
		// Many Managers scroll, rather than starve the inbox under them.
		n := len(items)
		for _, it := range items {
			if a.childrenOpen[it.t.id()] {
				n += len(a.shown(it.t))
			}
		}
		if float32(n)*(rowHeight+2) <= managerRowsHeight {
			ui.Column(c).Key("manager-rows").Gap(2).Children(rows)
			return
		}
		ui.Scroll(c).Key("manager-rows").Height(managerRowsHeight).Shrink(0).Gap(2).Children(rows)
	})
}

// managerRowsHeight is the most of the sidebar the Managers' rows take
// before they scroll.
const managerRowsHeight = 220

// managersCount is the Managers' count: amber while one, or a worker under
// one, waits on the user or failed, the accent while one is unread.
func (a *app) managersCount(c *ui.Context, items []inboxItem) {
	needs, unread := false, false
	for _, it := range items {
		n, _ := a.managerAttention(it.t)
		needs = needs || n > 0
		unread = unread || a.backend.Unread(it.t.Thread)
	}
	n := ui.Textf(c, "%d", len(items)).FontSize(11).FontWeight(600).Padding(1, 7).Radius(999)
	switch {
	case needs:
		n.Background(palette.amber.Alpha(0.2)).TextColor(palette.amber)
	case unread:
		n.Background(palette.accent.Alpha(0.2)).TextColor(palette.accent)
	default:
		n.Background(palette.hover).TextColor(palette.muted)
	}
}

// managerRow is a Manager in the sidebar: its project's tile, its title,
// its workers' toggle, and what it and they need; pointed at, its menu.
func (a *app) managerRow(c *ui.Context, it inboxItem) {
	t := it.t
	selected := t.id() == a.selected && a.router.Path() == "/"
	r := row(c, t.id(), selected).Height(rowHeight).Padding(0, 6, 0, 8).Gap(9)
	if a.revealRow(t.id()) {
		r.ScrollIntoView()
	}
	hovered := r.Hovered()
	r.ContextMenu(func(m *ui.Menu) { a.managerMenu(c, m, t) })
	if r.Children(func() {
		a.threadTile(c, t, it.p)
		if a.renaming == t.id() {
			a.renameInput(c, t)
			return
		}
		name := ui.Text(c, title(t)).FontSize(13).SingleLine().Grow(1).Shrink(1)
		if a.backend.Unread(t.Thread) {
			name.FontWeight(600).TextColor(palette.text)
		}
		a.pluginSlot(c, "thread.row", t.id().String(), &t, false, true)
		if len(a.backend.Envs()) > 1 {
			a.machineBadge(c, t.env)
		}
		tree := a.shown(t)
		if len(tree) > 0 {
			defer a.childToggle(c, t)
		}
		if hovered {
			a.iconButton(c, "manager-menu", icDots, "Manager actions", false).Padding(3).
				Menu(func(m *ui.Menu) { a.managerMenu(c, m, t) })
			return
		}
		a.managerMark(c, t, tree, selected)
	}).Clicked() {
		a.choose(t)
	}
}

// managerMark is what a Manager's row shows at its end while it isn't
// pointed at: a failure, what waits on the user, how many workers work,
// that it is unread, or how long ago it changed.
func (a *app) managerMark(c *ui.Context, t thread, tree []node, selected bool) {
	dim := palette.faint
	if selected {
		dim = palette.muted
	}
	_, _, selfNeeds := threadNeedsYou(t)
	anyNeeds := selfNeeds || slices.ContainsFunc(tree, func(n node) bool {
		_, _, ok := threadNeedsYou(n.t)
		return ok
	})
	switch {
	case failed(t):
		a.tip(c, ui.Box(c).Size(7, 7).Radius(4).Background(palette.red).Label("A command failed"), "A command failed")
	case anyNeeds:
		ui.Text(c, "Needs you").FontSize(10.5).FontWeight(600).TextColor(palette.amber).
			Background(palette.amber.Alpha(0.16)).Padding(1, 5).Radius(999).Shrink(0)
	case working(tree) > 0:
		ui.Textf(c, "%d active", working(tree)).FontSize(11).TextColor(palette.blue).Shrink(0)
	case a.backend.Unread(t.Thread):
		ui.Box(c).Size(7, 7).Radius(4).Background(palette.accent)
	default:
		ui.Text(c, ago(t.View.UpdatedAt, a.now)).FontSize(11).TextColor(dim)
	}
}

// managerMenu is a Manager's context menu. A Manager is never snoozed or
// settled, nor forked: it coordinates, and lives on.
func (a *app) managerMenu(c *ui.Context, m *ui.Menu, t thread) {
	if m.Item("Rename").Chosen() {
		a.renaming, a.renameDraft, a.renameFocused = t.id(), title(t), false
	}
	overview := "Open overview"
	if a.managerPanelShown(t) && t.id() == a.selected {
		overview = "Close overview"
	}
	if m.Item(overview).Chosen() {
		if overview == "Close overview" {
			a.mgr.showPanel = false
		} else {
			a.choose(t)
			a.openOverview(t)
		}
	}
	m.Separator()
	if m.Item("Run sweep now").Disabled(sweepBlocked(t)).Chosen() {
		a.backend.RunSweep(t.env, t.id(), "")
	}
	m.Submenu("Sweep schedule", func(m *ui.Menu) {
		cur := effectiveSweep(t)
		at := sweepSeconds(cur)
		for _, s := range sweepChoices {
			if m.Item(s.name()).Radio(at == s.secs).Chosen() {
				a.backend.SetSweep(t.env, t.id(), sweepWithInterval(cur, s.secs))
			}
		}
	})
	if tree := a.shown(t); working(tree) > 0 {
		if m.Item("Stop all workers").Chosen() {
			n := a.stopAll(tree)
			c.AddToast(ui.Toast{Title: "Stopped", Description: plural(n, "worker"), Type: "info"})
		}
	}
	if t.local() {
		if path := a.providerLog(t); path != "" {
			m.Separator()
			if m.Item("Reveal provider log").Chosen() {
				revealFile(path)
			}
		}
	}
	a.pluginMenuItems(m, "thread.menu", func(plugin string) map[string]any {
		return map[string]any{"thread": a.plugins.Record(plugin, a.threadRecord(t))}
	})
	m.Separator()
	if m.Item("Delete…").Disabled(t.View.Live()).Chosen() {
		a.askDelete(t)
	}
}

// --- Creating

// managerName is the name a new Manager of the project p takes, nil for
// none.
func managerName(p *project) string {
	if p == nil || p.noProject {
		return "Manager"
	}
	return p.name() + " Manager"
}

// defaultManagerProject picks the single project filtered in the sidebar,
// or else the shown thread's project.
func (a *app) defaultManagerProject() (*client.Env, projectKey) {
	if len(a.projectFilter) == 1 {
		for k := range a.projectFilter {
			for _, p := range a.projects {
				if p.key == k {
					return p.env, p.key
				}
			}
		}
	}
	t := a.current()
	return t.env, a.projectOf(t).key
}

// openManagerDialog opens New manager, on the project of key on env, or
// with no project when that is none or none was given.
func (a *app) openManagerDialog(env *client.Env, key projectKey) {
	d := managerDialog{open: true, sweepSeconds: defaultSweepSeconds}
	var p *project
	for _, x := range a.projects {
		if x.key == key && x.env == env && !x.noProject {
			p = x
		}
	}
	switch local := a.localEnv(); {
	case p != nil:
		d.env, d.project = p.env, p.key
	case local != nil:
		d.env, d.project, d.scratch = local, keyOf(nil, a.scratchRoot()), true
	default:
		for _, x := range a.projects {
			if !x.noProject {
				p, d.env, d.project = x, x.env, x.key
				break
			}
		}
	}
	d.name = managerName(p)
	d.auto = d.name
	a.mgr.dialog = d
}

// pickManagerProject sets the dialog's project to p, nil for none, and its
// name with it while the user hasn't typed one.
func (a *app) pickManagerProject(p *project) {
	d := &a.mgr.dialog
	if p == nil {
		d.env, d.project, d.scratch = a.localEnv(), keyOf(nil, a.scratchRoot()), true
	} else {
		d.env, d.project, d.scratch = p.env, p.key, false
	}
	if name := managerName(p); d.name == d.auto {
		d.name, d.auto = name, name
	}
}

// createManager makes a Manager of the project root on env, titled title,
// in a folder of its own when scratch, and shows it: it returns its ID,
// zero when none was made.
func (a *app) createManager(env *client.Env, root string, scratch bool, title string, sweep *api.SweepSchedule) api.ID {
	if scratch {
		dir, err := a.scratchFolder("manager")
		if err != nil {
			a.pluginNotice(ui.Toast{Title: "Couldn't make the Manager's folder", Description: err.Error()})
			return api.ID{}
		}
		root = dir
	}
	id := a.backend.CreateManager(env, root, title, a.newThreadRef(thread{env, &client.Thread{}}), accessOf[a.access], sweep)
	if id.IsZero() {
		return id
	}
	a.selected = id
	a.backend.OpenThread(env, id)
	if a.router.Path() != "/" {
		a.router.Push("/")
	}
	a.focusBack()
	return id
}

// submitManagerDialog makes the Manager the dialog describes and opens its
// overview.
func (a *app) submitManagerDialog() {
	d := &a.mgr.dialog
	if d.env == nil {
		return
	}
	name := cmp.Or(strings.TrimSpace(d.name), "Manager")
	sweep := &api.SweepSchedule{IntervalSeconds: d.sweepSeconds, Prompt: strings.TrimSpace(d.prompt)}
	id := a.createManager(d.env, d.project.workspace, d.scratch, name, sweep)
	if id.IsZero() {
		return
	}
	d.open = false
	if t, ok := a.find(id); ok {
		a.openOverview(t)
	}
}

// managerDialog is New manager, while it's open.
func (a *app) managerDialog(c *ui.Context) {
	d := &a.mgr.dialog
	if !d.open {
		return
	}
	a.dialog(c, &d.open, func(backdrop, panel ui.Element) {
		th := c.Theme()
		width, _ := c.Size()
		room := width - th.Space(10)
		backdrop.Background(ui.RGBA(0, 0, 0, 0.4))
		panel.Width(min(460, room)).MaxWidth(room).Padding(20).Gap(14).Radius(14).Label("New manager")
		a.frosted(c, panel).Shadow(0, 10, 30, 0, ui.RGBA(0, 0, 0, 0.3))
		wrapped := func(s string) ui.Element { return ui.Text(c, s).Width(0).MinWidthPercent(100) }
		field := func(s string) { ui.Text(c, s).FontSize(11).FontWeight(600).LetterSpacing(0.4).TextColor(palette.faint) }
		ui.Column(c).Gap(4).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icManager).FontSize(18).TextColor(palette.accent)
				ui.Text(c, "New manager").Bold().FontSize(th.FontSize + 2).TextColor(palette.text)
			})
			wrapped("Coordinates parallel workers, shares workspace docs, and runs periodic sweeps.").FontSize(12.5).TextColor(palette.muted)
		})
		ui.Column(c).Gap(6).Children(func() {
			field("NAME")
			in := ui.TextInput(c.Key("manager-name"), &d.name).AutoFocus().Placeholder("e.g. Release Captain").
				Label("Manager name").FontSize(13).Padding(6, 9).Radius(7)
			if in.Submitted() {
				a.submitManagerDialog()
			}
		})
		ui.Column(c).Gap(6).Children(func() {
			field("PROJECT")
			chips := func() {
				ui.Row(c).Key("manager-projects").Wrap().Gap(6).Children(func() {
					for _, p := range a.projects {
						if p.noProject {
							continue
						}
						on := !d.scratch && d.env == p.env && d.project == p.key
						if managerChip(c, "manager-project:"+p.key.String(), a.projectLabel(p), on).Label("Project " + a.projectLabel(p)).Clicked() {
							a.pickManagerProject(p)
						}
					}
					if a.localEnv() != nil && managerChip(c, "manager-project:none", "No project", d.scratch).Clicked() {
						a.pickManagerProject(nil)
					}
				})
			}
			named := 0
			for _, p := range a.projects {
				if !p.noProject {
					named++
				}
			}
			if named > 6 {
				ui.Scroll(c).Key("manager-projects-scroll").Height(110).Shrink(0).Children(chips)
			} else {
				chips()
			}
		})
		ui.Column(c).Gap(6).Children(func() {
			field("SWEEPS")
			ui.Row(c).Key("manager-sweeps").Gap(6).Children(func() {
				for _, s := range sweepChoices {
					if managerChip(c, "manager-sweep:"+s.label, s.label, d.sweepSeconds == s.secs).Label(s.name()).Clicked() {
						d.sweepSeconds = s.secs
					}
				}
			})
		})
		ui.Column(c).Gap(6).Children(func() {
			field("STANDING INSTRUCTIONS")
			ui.TextArea(c.Key("manager-prompt"), &d.prompt).Label("Standing sweep prompt").
				Placeholder("Optional standing brief for sweeps. By default a sweep verifies finished workers, steers stalled ones, updates WORKERS.md and NOTES.md, and stays quiet unless you must act.").
				FontSize(12.5).Padding(6, 9).Radius(7).MinHeight(64).MaxHeight(140)
		})
		ui.Row(c).Gap(8).Justify(ui.End).Margin(4, 0, 0, 0).Children(func() {
			ui.Button(c.Key("manager-cancel"), "Cancel").OnClick(func() { d.open = false })
			ui.PrimaryButton(c.Key("manager-create"), "Create manager").Disabled(d.env == nil).OnClick(a.submitManagerDialog)
		})
	})
}

// managerChip is a choice among others: a pill, filled while on.
func managerChip(c *ui.Context, key, label string, on bool) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Padding(3, 10).Radius(999).Border(1, palette.border).Cursor(ui.CursorPointer).
		TextColor(palette.muted).Transition(hoverFade)
	switch {
	case on:
		b.Background(palette.accent.Alpha(0.16)).Border(1, palette.accent.Alpha(0.45)).TextColor(palette.accent)
	case b.Hovered():
		b.Background(palette.hover).TextColor(palette.text)
	}
	return b.Children(func() { ui.Text(c, label).FontSize(12).NoWrap() })
}

// --- Sweeps

// sweepTiming says when the Manager t last swept and sweeps next, as of
// now, as the engine schedules it: an interval after its last sweep, or
// what it last did.
func sweepTiming(t thread, now time.Time) string {
	last := t.View.LastSweep
	secs := sweepSeconds(effectiveSweep(t))
	var parts []string
	if !last.IsZero() {
		parts = append(parts, "Last sweep "+agoPhrase(last, now))
	}
	switch {
	case secs == 0 && last.IsZero():
		return "Sweeps off"
	case secs == 0:
		parts = append(parts, "Sweeps paused")
	case t.View.Live():
		parts = append(parts, "Next after this run")
	case t.View.Resumable || t.View.Queued > 0:
		parts = append(parts, "Paused while queued work waits")
	default:
		if resets, ok := t.View.LimitResume(now); ok && resets.After(now) {
			parts = append(parts, "Waiting on usage limit ("+span(resets.Sub(now))+")")
			break
		}
		base := last
		if t.View.UpdatedAt.After(base) {
			base = t.View.UpdatedAt
		}
		if t.View.LastTurn != nil && t.View.LastTurn.Ended.After(base) {
			base = t.View.LastTurn.Ended
		}
		next := base.Add(time.Duration(secs) * time.Second)
		if resets, ok := t.View.LimitResume(now); ok {
			if after := resets.Add(api.LimitOverdue + time.Nanosecond); after.After(next) {
				next = after
			}
		}
		if next.After(now) {
			parts = append(parts, "Next in "+span(next.Sub(now)))
		} else {
			parts = append(parts, "Next sweep due")
		}
	}
	return strings.Join(parts, " · ")
}

// agoPhrase is how long before now t was, in words.
func agoPhrase(t, now time.Time) string {
	if s := ago(t, now); s != "now" {
		return s + " ago"
	}
	return "just now"
}

// span is d, short: minutes under an hour, else hours.
func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()+0.5))
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// syncSweepDraft starts the sweep prompt's edit over from t's, when it was
// another Manager's or force says so.
func (a *app) syncSweepDraft(t thread, force bool) {
	if a.mgr.promptFor == t.id() && !force {
		return
	}
	a.mgr.promptFor, a.mgr.promptDraft = t.id(), ""
	if s := effectiveSweep(t); s != nil {
		a.mgr.promptDraft = s.Prompt
	}
}

// toggleSweepPopover opens the sweep popover of t, or closes it.
func (a *app) toggleSweepPopover(t thread) {
	if a.mgr.sweepPopover == t.id() {
		a.mgr.sweepPopover = api.ID{}
		return
	}
	a.mgr.sweepPopover = t.id()
	a.syncSweepDraft(t, false)
}

// sweepPopover is the header's sweep popover of t, under anchor while
// open: its cadence, its standing prompt and Run sweep now.
func (a *app) sweepPopover(c *ui.Context, t thread, anchor ui.Element) {
	open := a.mgr.sweepPopover == t.id() && !t.id().IsZero()
	if !open {
		return
	}
	a.overlay(c, open, func() {
		box := a.cover(coverKey{kind: "sweep"}, ui.Column(c).Key("sweep-popover").AttachTo(anchor, ui.AnchorBottomStart, ui.AnchorTopStart))
		box.Margin(6, 0, 0, 0).Width(340).Padding(14).Gap(12).Radius(12).Label("Sweep schedule")
		a.frosted(c, box).Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.35))
		box.Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, "Sweep schedule").FontSize(13).FontWeight(600).TextColor(palette.text)
				ui.Text(c, sweepTiming(t, a.now)).FontSize(11.5).TextColor(palette.muted)
			})
			a.sweepControls(c, t, false)
		})
		if box.OverlayShortcut(0, ui.KeyEscape) || box.PressedOutside() && !anchor.Hovered() {
			a.mgr.sweepPopover = api.ID{}
		}
	})
}

// sweepControls are a Manager's cadence, its standing prompt, and Run
// sweep now, which in the popover closes it.
func (a *app) sweepControls(c *ui.Context, t thread, inPanel bool) {
	a.syncSweepDraft(t, false)
	cur := effectiveSweep(t)
	at := sweepSeconds(cur)
	ui.Row(c).Key("sweep-cadence").Gap(6).Children(func() {
		for _, s := range sweepChoices {
			if managerChip(c, "cadence:"+s.label, s.label, at == s.secs).Label(s.name()).Clicked() && at != s.secs {
				a.backend.SetSweep(t.env, t.id(), sweepWithInterval(cur, s.secs))
			}
		}
	})
	saved := ""
	if cur != nil {
		saved = cur.Prompt
	}
	ui.TextArea(c.Key("sweep-prompt"), &a.mgr.promptDraft).Label("Standing sweep prompt").
		Placeholder("Default: verify finished workers, steer stalled ones, update WORKERS.md and NOTES.md, and stay quiet unless you must act.").
		FontSize(12).Padding(6, 9).Radius(7).MinHeight(56).MaxHeight(140)
	ui.Row(c).Key("sweep-actions").Gap(6).AlignItems(ui.Center).Children(func() {
		draft := strings.TrimSpace(a.mgr.promptDraft)
		if ui.Button(c.Key("sweep-save"), "Save prompt").Disabled(draft == saved).Clicked() {
			a.backend.SetSweep(t.env, t.id(), &api.SweepSchedule{IntervalSeconds: at, Prompt: draft})
			a.mgr.promptDraft = draft
		}
		if saved != "" && ui.Button(c.Key("sweep-reset"), "Reset").Clicked() {
			a.backend.SetSweep(t.env, t.id(), &api.SweepSchedule{IntervalSeconds: at})
			a.mgr.promptDraft = ""
		}
		ui.Spacer(c)
		if ui.PrimaryButton(c.Key("sweep-run"), "Run sweep now").Disabled(sweepBlocked(t)).Clicked() {
			a.backend.RunSweep(t.env, t.id(), "")
			if !inPanel {
				a.mgr.sweepPopover = api.ID{}
			}
		}
	})
}

// --- The overview panel

// managerPanelShown is whether the overview panel shows beside t's chat:
// t is a Manager, the user opened it, and no other panel shows there.
func (a *app) managerPanelShown(t thread) bool {
	return a.mgr.showPanel && isManager(t) && !a.showDiff && !a.showPreview && !a.showPluginTab
}

// openOverview opens the overview panel beside the Manager t's chat, in
// place of any other there, its docs read anew.
func (a *app) openOverview(t thread) {
	a.mgr.showPanel = true
	a.mgr.sweepPopover = api.ID{}
	a.showDiff, a.showPreview, a.showPluginTab, a.panelsJump = false, false, false, false
	delete(a.subagentStep, t.id())
	a.syncSweepDraft(t, false)
	if t.local() {
		a.ensureManagerDocs(t.project(), true)
	}
}

// toggleOverview opens t's overview panel, or closes it.
func (a *app) toggleOverview(t thread) {
	if a.managerPanelShown(t) {
		a.mgr.showPanel = false
		return
	}
	a.openOverview(t)
}

// yieldOverview closes the overview as another panel opens beside the chat.
func (a *app) yieldOverview() {
	if a.showDiff || a.showPreview || a.showPluginTab {
		a.mgr.showPanel = false
	}
}

// managerPanel is the overview of the Manager t: what needs the user, its
// workers, the shared context docs of its workspace, and its sweeps.
func (a *app) managerPanel(c *ui.Context, t thread) {
	tree := a.shown(t)
	root := t.project()
	ui.Column(c).Width(a.diffWidth()).FillHeight().Children(func() {
		bar := c.TitleBar()
		ui.Row(c).Key("overview-head").Height(max(bar.Height, headerHeight)).Padding(0, 8+bar.Right, 0, 14).Gap(8).
			AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Icon(c, icManager).FontSize(15).TextColor(palette.accent)
			ui.Text(c, "Overview").FontSize(13).FontWeight(600).TextColor(palette.text)
			ui.Spacer(c)
			if t.local() && a.iconButton(c, "refresh-docs", icRotate, "Refresh workspace docs", false).Clicked() {
				a.ensureManagerDocs(root, true)
			}
			if a.iconButton(c, "close-overview", icX, "Close overview", false).Clicked() {
				a.mgr.showPanel = false
			}
		})
		ui.Scroll(c).Key("overview:"+t.id().String()).Grow(1).Background(palette.sidebar).BorderWidth(1, 0, 0, 1).
			BorderColor(palette.border).Padding(12, 14).Gap(16).Children(func() {
			a.needsYouCard(c, t, tree)
			a.workersCard(c, t, tree)
			a.docsCard(c, t)
			overviewSection(c, "SWEEPS & BRIEF", "", func() {
				ui.Text(c, sweepTiming(t, a.now)).FontSize(12).TextColor(palette.muted)
				a.sweepControls(c, t, true)
			})
		})
	})
}

// overviewSection is a section of the overview: its heading, what's at
// the heading's end, and its body.
func overviewSection(c *ui.Context, heading, note string, body func()) {
	ui.Column(c).Key(heading).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, heading).FontSize(11).FontWeight(600).LetterSpacing(0.6).TextColor(palette.faint)
			if note != "" {
				ui.Text(c, note).FontSize(11).TextColor(palette.faint)
			}
		})
		body()
	})
}

// needsYouCard lists the Manager and the workers that wait on the user or
// failed; a click shows one. It shows only while there are some.
func (a *app) needsYouCard(c *ui.Context, t thread, tree []node) {
	type need struct {
		t      thread
		why    string
		isFail bool
	}
	var needs []need
	if why, isFail, ok := threadNeedsYou(t); ok {
		needs = append(needs, need{t, why, isFail})
	}
	for _, n := range tree {
		if why, isFail, ok := threadNeedsYou(n.t); ok {
			needs = append(needs, need{n.t, why, isFail})
		}
	}
	if len(needs) == 0 {
		return
	}
	ui.Column(c).Key("needs-you").Padding(10, 10, 8).Gap(4).Radius(10).Border(1, palette.amber.Alpha(0.35)).
		Background(palette.amber.Alpha(0.06)).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Padding(0, 2, 2).Children(func() {
			ui.Icon(c, icHand).FontSize(13).TextColor(palette.amber)
			ui.Text(c, "Needs you").FontSize(12).FontWeight(600).TextColor(palette.amber)
		})
		for _, n := range needs {
			col := palette.amber
			if n.isFail {
				col = palette.red
			}
			if row(c, "need:"+n.t.id().String(), false).Height(28).Padding(0, 6).Children(func() {
				ui.Box(c).Size(7, 7).Radius(4).Background(col).Shrink(0)
				ui.Text(c, title(n.t)).FontSize(12.5).TextColor(palette.text).SingleLine().Grow(1).Shrink(1)
				ui.Text(c, n.why).FontSize(11).TextColor(col).Shrink(0)
			}).Clicked() {
				a.choose(n.t)
			}
		}
	})
}

// workersCard lists the Manager's workers, those that need the user
// first, then those working, those failed, and the latest; a click shows
// one, a right click opens its menu.
func (a *app) workersCard(c *ui.Context, t thread, tree []node) {
	ws := slices.Clone(tree)
	rank := func(k thread) int {
		switch {
		case len(k.View.Requests) > 0 || k.View.Run == api.RunWaiting || k.View.Run == api.RunUncertain:
			return 0
		case k.View.Live():
			return 1
		case failedLast(k):
			return 2
		}
		return 3
	}
	slices.SortStableFunc(ws, func(p, q node) int {
		return cmp.Or(cmp.Compare(rank(p.t), rank(q.t)), q.t.View.UpdatedAt.Compare(p.t.View.UpdatedAt))
	})
	n := working(tree)
	note := "0"
	if len(tree) > 0 {
		note = fmt.Sprintf("%d active · %d total", n, len(tree))
	}
	overviewSection(c, "WORKERS", note, func() {
		if n > 0 {
			if ghost(c, "stop-workers", false).Padding(3, 8).Label("Stop all workers").Children(func() {
				ui.Box(c).Size(8, 8).Radius(2).Background(palette.text)
				ui.Text(c, "Stop all").FontSize(11.5)
			}).Clicked() {
				stopped := a.stopAll(tree)
				c.AddToast(ui.Toast{Title: "Stopped", Description: plural(stopped, "worker"), Type: "info"})
			}
		}
		if len(ws) == 0 {
			ui.Text(c, "No workers yet. Ask the manager to delegate tasks.").FontSize(12).TextColor(palette.faint).Padding(2, 2)
			return
		}
		ui.Column(c).Key("workers").Gap(1).Children(func() {
			for _, w := range ws {
				k := w.t
				r := row(c, "worker:"+k.id().String(), k.id() == a.selected).Height(30).Padding(0, 6).Gap(8).Label(title(k))
				r.ContextMenu(func(m *ui.Menu) { a.threadMenu(c, m, k, a.categoryOf(k)) })
				if r.Children(func() {
					a.statusDot(c, a.statusOf(k), failedLast(k))
					ui.Text(c, title(k)).FontSize(12.5).TextColor(palette.text).SingleLine().Grow(1).Shrink(1)
					if b := k.View.Branch; b != "" {
						ui.Text(c, b).FontSize(10.5).TextColor(palette.muted).SingleLine().Shrink(2).MaxWidth(110).
							Padding(1, 6).Radius(999).Border(1, palette.border)
					}
					ui.Text(c, ago(k.View.UpdatedAt, a.now)).FontSize(11).TextColor(palette.faint).Shrink(0)
				}).Clicked() {
					a.choose(k)
				}
			}
		})
	})
}

// docsCard is the shared context docs of the Manager's workspace root:
// each one's size, its text on a click, and Open, or Create for a missing
// WORKERS.md or NOTES.md.
func (a *app) docsCard(c *ui.Context, t thread) {
	overviewSection(c, "SHARED CONTEXT", "", func() {
		ui.Text(c, "Injected into this manager and its workers from the workspace root.").FontSize(12).
			TextColor(palette.muted).Width(0).MinWidthPercent(100)
		if !t.local() {
			name := "its machine"
			if t.env != nil && t.env.Name != "" {
				name = t.env.Name
			}
			ui.Text(c, "Shared context files live on "+name+".").FontSize(12).TextColor(palette.faint)
			return
		}
		root := t.project()
		d := a.ensureManagerDocs(root, false)
		if d.err != "" {
			ui.Text(c, d.err).FontSize(12).TextColor(palette.red).Width(0).MinWidthPercent(100)
		}
		if len(d.files) == 0 {
			ui.Text(c, "Reading…").FontSize(12).TextColor(palette.faint)
			return
		}
		ui.Column(c).Key("docs").Radius(10).Border(1, palette.border).Background(palette.surface).Clip().
			Dividers(1, palette.border).Children(func() {
			for _, f := range d.files {
				a.docRow(c, root, d, f)
			}
		})
	})
}

func (a *app) docRow(c *ui.Context, root string, d *workspaceDocs, f managerDoc) {
	path := filepath.Join(root, f.name)
	ui.Column(c).Key("doc:" + f.name).Children(func() {
		open := f.exists && d.expanded[f.name]
		ui.Row(c).Padding(0, 6, 0, 0).Gap(4).AlignItems(ui.Center).Children(func() {
			head := ui.ButtonBase(c.Key("doc-head")).Grow(1).Shrink(1).Height(34).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(f.name)
			if f.exists && head.Hovered() {
				head.Background(palette.hover)
			}
			if head.Children(func() {
				col := palette.faint
				if f.exists {
					col = palette.accent
				}
				ui.Icon(c, icFile).FontSize(13).TextColor(col)
				name := ui.Text(c, f.name).FontSize(12.5).SingleLine().Shrink(1)
				if f.exists {
					name.TextColor(palette.text)
				} else {
					name.TextColor(palette.muted)
				}
				ui.Spacer(c)
				status := "Not created"
				switch {
				case f.omitted:
					status = "Over 32 KB cap"
				case f.truncated:
					status = "Truncated"
				case f.exists:
					status = fileSize(f.size)
				}
				ui.Text(c, status).FontSize(11).TextColor(palette.faint).Shrink(0)
				if f.exists {
					chevron(c, open).FontSize(10).TextColor(palette.faint)
				}
			}).Clicked() && f.exists {
				if d.expanded == nil {
					d.expanded = map[string]bool{}
				}
				d.expanded[f.name] = !open
			}
			switch {
			case f.exists:
				if ghost(c, "doc-open", false).Padding(3, 8).Label("Open " + f.name).Children(func() {
					ui.Text(c, "Open").FontSize(11.5)
				}).Clicked() {
					if ed := editorCLI(); ed != "" {
						a.openInEditor(c, ed, path)
					} else {
						revealFile(path)
					}
				}
			case managerStarters[f.name] != "":
				if ghost(c, "doc-create", false).Padding(3, 8).Label("Create " + f.name).Children(func() {
					ui.Text(c, "Create").FontSize(11.5).TextColor(palette.accent)
				}).Clicked() {
					a.createManagerDoc(root, f.name)
				}
			}
		})
		if open {
			ui.Scroll(c).Key("doc-text").Height(min(180, 24+float32(strings.Count(f.text, "\n")+1)*16)).Shrink(0).
				Margin(0, 10, 10).Padding(8, 10).Radius(6).Background(palette.bg.Alpha(0.6)).Children(func() {
				ui.Text(c, f.text).Selectable().Font("monospace").FontSize(11).TextColor(palette.text).Width(0).MinWidthPercent(100)
			})
		}
	})
}

// fileSize is n bytes as the panel shows a size.
func fileSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}

// ensureManagerDocs is what the app has read of root's docs: on the first
// ask, or when force says so, it reads them again off the UI thread.
func (a *app) ensureManagerDocs(root string, force bool) *workspaceDocs {
	if a.mgr.docs == nil {
		a.mgr.docs = map[string]*workspaceDocs{}
	}
	d := a.mgr.docs[root]
	if d == nil {
		d = &workspaceDocs{}
		a.mgr.docs[root] = d
		force = true
	}
	if !force || root == "" {
		return d
	}
	d.gen++
	d.loading = true
	gen := d.gen
	go func() {
		files, err := readManagerDocs(root)
		a.post(func() {
			cur := a.mgr.docs[root]
			if cur == nil || cur.gen != gen {
				return
			}
			cur.loading, cur.loadedAt, cur.files, cur.err = false, time.Now(), files, ""
			if err != nil {
				cur.err = err.Error()
			}
		})
	}()
	return d
}

// readManagerDocs reads root's docs, each as far as a Manager is given it,
// tracking the engine's 32 KiB combined injection cap across files.
func readManagerDocs(root string) ([]managerDoc, error) {
	var out []managerDoc
	var errs []error
	remaining := managerDocTotalLimit
	for _, name := range managerDocNames {
		f := managerDoc{name: name}
		fi, err := os.Stat(filepath.Join(root, name))
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			errs = append(errs, err)
		case fi.Mode().IsRegular():
			f.exists, f.size = true, fi.Size()
			file, err := os.Open(filepath.Join(root, name))
			if err != nil {
				errs = append(errs, err)
				break
			}
			limit := managerDocLimit
			if remaining > 0 {
				limit = min(managerDocLimit, remaining)
			} else {
				f.omitted = true
			}
			data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
			file.Close()
			if err != nil {
				errs = append(errs, err)
			}
			if len(data) > limit {
				data = data[:limit]
				if !f.omitted {
					f.truncated = true
				}
			}
			f.text = string(data)
			if !f.omitted && strings.TrimSpace(f.text) != "" {
				remaining -= len(data)
			}
		}
		out = append(out, f)
	}
	return out, errors.Join(errs...)
}

// createManagerDoc writes name's starter into root, off the UI thread,
// never over a file there, and reads the docs again.
func (a *app) createManagerDoc(root, name string) {
	body := managerStarters[name]
	go func() {
		err := writeNew(filepath.Join(root, name), body)
		a.post(func() {
			if err != nil && !errors.Is(err, fs.ErrExist) {
				d := a.ensureManagerDocs(root, false)
				d.err = "Couldn't create " + name + ": " + err.Error()
				return
			}
			a.ensureManagerDocs(root, true)
		})
	}()
}

func writeNew(path, body string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(body)
	return errors.Join(err, f.Close())
}

// --- The empty Manager

// managerEmpty is what a Manager with no messages yet shows: what it is,
// its sweeps and docs, and the way to its overview.
func (a *app) managerEmpty(c *ui.Context, t thread) {
	ui.Column(c).Key("manager-empty").MaxWidth(480).Padding(100, 24, 0).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icManager).FontSize(28).TextColor(palette.accent)
		ui.Text(c, title(t)).FontSize(18).FontWeight(600).TextColor(palette.text).TextAlign(ui.Center)
		about := "Manager without a project"
		if p := a.projectOf(t); !p.noProject {
			about = "Manager for " + p.name()
		}
		ui.Text(c, about).FontSize(13).TextColor(palette.muted)
		ui.Row(c).Key("manager-chips").Wrap().Gap(6).Justify(ui.Center).Children(func() {
			sw := effectiveSweep(t)
			emptyChip(c, sweepLabel(sw), sweepSeconds(sw) > 0)
			var have map[string]bool
			if t.local() {
				have = map[string]bool{}
				for _, f := range a.ensureManagerDocs(t.project(), false).files {
					have[f.name] = f.exists
				}
			}
			for _, name := range managerDocNames {
				emptyChip(c, name, have[name])
			}
		})
		ui.Text(c, "Give this manager goals or standing instructions. It delegates parallel workers, shares workspace docs, and stays quiet on routine sweeps.").
			FontSize(13).TextColor(palette.muted).TextAlign(ui.Center).Width(0).MinWidthPercent(100)
		if !a.managerPanelShown(t) {
			if ui.Button(c.Key("manager-open-overview"), "Open overview").Clicked() {
				a.openOverview(t)
			}
		}
	})
}

// emptyChip is a fact of the empty Manager: in the accent while on.
func emptyChip(c *ui.Context, label string, on bool) {
	e := ui.Text(c, label).Key("chip:"+label).FontSize(11).Padding(2, 8).Radius(999).Border(1, palette.border).TextColor(palette.faint)
	if on {
		e.TextColor(palette.accent).Background(palette.accent.Alpha(0.12)).Border(1, palette.accent.Alpha(0.3))
	}
}
