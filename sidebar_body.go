package main

import (
	"encoding/json"
	"slices"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// sidebarBody is what a copy of sidebar.body is about: the inbox, which
// grows, or the sections under it, Working, Snoozed and Settled, which take
// at most maxHeight before they scroll. Selected is the thread shown;
// machines whether there are several, whose badges rows then show; slots
// whether plugins place widgets in each row (thread.row). EmptyHeight is
// how tall Glitched's own sidebar draws what an empty section says.
type sidebarBody struct {
	Part        string           `json:"part"`
	MaxHeight   float32          `json:"maxHeight,omitempty"`
	EmptyHeight float32          `json:"emptyHeight"`
	Page        int              `json:"page"`
	Selected    string           `json:"selected"`
	Machines    bool             `json:"machines,omitempty"`
	Slots       bool             `json:"slots,omitempty"`
	Sections    []sidebarSection `json:"sections"`
}

// sidebarSection is a section the copy shows: its id, what it says while
// empty, whether the user closed it, and, while open, its rows in order.
type sidebarSection struct {
	ID     string       `json:"id"`
	Title  string       `json:"title"`
	Empty  string       `json:"empty,omitempty"`
	Closed bool         `json:"closed,omitempty"`
	Rows   []sidebarRow `json:"rows"`
}

// sidebarRow is a thread's row: its title, its name to assistive tools;
// or, with more, the row that shows the next page of the section's
// threads, or of parent's children, more being how many aren't shown;
// depth is how deep a child is under the
// row it shows under, 0 for a row of the section's own; status what it
// does (statusNames); kids whether it has children to show; renaming
// whether the user renames it.
type sidebarRow struct {
	Thread   string `json:"thread"`
	Title    string `json:"title"`
	Depth    int    `json:"depth,omitempty"`
	Status   string `json:"status"`
	Kids     bool   `json:"kids,omitempty"`
	Renaming bool   `json:"renaming,omitempty"`
	More     int    `json:"more,omitempty"`
	Parent   string `json:"parent,omitempty"`
}

// sidebarPage is how many threads a section, or a thread's children,
// shows till the user asks for more: past it, a row says how many more
// there are and shows the next as many.
const sidebarPage = 100

func (a *app) sidebarRow(t thread, depth int) sidebarRow {
	return sidebarRow{Thread: t.id().String(), Title: title(t), Depth: depth, Status: statusNames[a.statusOf(t)], Kids: len(a.shown(t)) > 0, Renaming: a.renaming == t.id()}
}

// sidebarBodyOf is the context of the copy part, of the sections cats.
func (a *app) sidebarBodyOf(part string, maxHeight float32, cats ...category) sidebarBody {
	b := sidebarBody{Part: part, MaxHeight: maxHeight, Page: sidebarPage, Machines: len(a.backend.Envs()) > 1, Sections: a.sidebarSections(cats...)}
	if a.plugins != nil {
		b.Slots = len(a.plugins.PlacesAt("thread.row")) > 0
	}
	if a.router.Path() == "/" && !a.selected.IsZero() {
		b.Selected = a.selected.String()
	}
	return b
}

// sidebarSections are the sections of cats the sidebar shows, with their
// rows: Working, Snoozed and Settled only while they have threads.
func (a *app) sidebarSections(cats ...category) []sidebarSection {
	if t, ok := a.find(a.selected); ok {
		a.sidebarKept[t.id()] = true
		for _, p := range a.ancestors(t) {
			a.sidebarKept[p.id()] = true
		}
	}
	var out []sidebarSection
	for _, cat := range cats {
		items := a.itemsIn(cat)
		a.sectionCounts[cat] = len(items)
		if len(items) == 0 && cat != catInbox {
			continue
		}
		s := categories[cat]
		sec := sidebarSection{ID: sectionNames[cat], Title: s.title, Empty: s.empty, Closed: cat != catInbox && a.sectionClosed[cat], Rows: []sidebarRow{}}
		if !sec.Closed {
			var kids func(t thread, depth int)
			kids = func(t thread, depth int) {
				if !a.childrenOpen[t.id()] || depth > maxDepth {
					return
				}
				ks := a.kids[t.id()]
				more := a.paged(t.id().String(), len(ks), func(i int) api.ID { return ks[i].id() }, func(i int) {
					sec.Rows = append(sec.Rows, a.sidebarRow(ks[i], depth))
					kids(ks[i], depth+1)
				})
				if more > 0 {
					sec.Rows = append(sec.Rows, sidebarRow{More: more, Parent: t.id().String(), Depth: depth})
				}
			}
			more := a.paged(sec.ID, len(items), func(i int) api.ID { return items[i].t.id() }, func(i int) {
				sec.Rows = append(sec.Rows, a.sidebarRow(items[i].t, 0))
				kids(items[i].t, 1)
			})
			if more > 0 {
				sec.Rows = append(sec.Rows, sidebarRow{More: more})
			}
		}
		out = append(out, sec)
	}
	return out
}

// paged adds those of n threads the pages of key show, in order: as many
// pages as the user asked for, then the threads kept wherever they sort,
// a page of them at most, and the shown thread and its parents always.
// It returns how many more there are.
func (a *app) paged(key string, n int, id func(int) api.ID, add func(int)) (more int) {
	shown, kept := sidebarPage*max(a.sidebarPages[key], 1), 0
	for i := range n {
		switch k := a.sidebarKept[id(i)]; {
		case i < shown, k && kept < sidebarPage, k && a.onSelectedPath(id(i)):
			if i >= shown {
				kept++
			}
			add(i)
		default:
			more++
		}
	}
	return more
}

// onSelectedPath is whether the thread is the one shown or a parent of it.
func (a *app) onSelectedPath(id api.ID) bool {
	t, ok := a.find(a.selected)
	return ok && (t.id() == id || slices.ContainsFunc(a.ancestors(t), func(p thread) bool { return p.id() == id }))
}

// showMore shows the next page of the section, or the thread's children,
// key names.
func (a *app) showMore(key string) {
	a.sidebarPages[key] = max(a.sidebarPages[key], 1) + 1
}

// sectionNative draws sidebar.section, the header of the section its
// props name.
func (a *app) sectionNative(c *ui.Context, n *widget.Node) {
	var id string
	json.Unmarshal(n.Props["section"], &id)
	for cat, s := range sectionNames {
		if s == id {
			a.sectionHead(c, cat, a.sectionCounts[cat])
			return
		}
	}
}

// sidebarCopy is the copy part of sidebar.body, of the sections cats, in
// box; native draws Glitched's own in its place.
func (a *app) sidebarCopy(c *ui.Context, part string, maxHeight float32, box func() ui.Element, native func(), cats ...category) componentCopy {
	body := a.sidebarBodyOf(part, maxHeight, cats...)
	body.EmptyHeight = 12 + lineHeight(c, 12, 0)
	was := a.sidebarRows[part]
	now := map[api.ID]bool{}
	for _, s := range body.Sections {
		for _, r := range s.Rows {
			if id, err := api.ParseID(r.Thread); err == nil {
				now[id] = true
			}
		}
	}
	a.sidebarRows[part] = now
	return componentCopy{
		key:     part,
		box:     box,
		native:  func(*widget.Node) { native() },
		current: true,
		context: func(plugins.ThreadRecord) any { return body },
		listRow: func(c *ui.Context, list *widget.Node, i int, e ui.Element) bool {
			return a.sidebarListRow(c, list.Items[i].Thread, was, e)
		},
	}
}

// foldOut is fold without its way in: a row that scrolls into a list
// was there all along.
var foldOut = func() ui.ElementTransition { t := fold; t.Enter = nil; return t }()

// sidebarListRow does to a thread's row in sidebar.body what the sidebar
// does to its own: it folds in, as its thread joins the part (not among
// was, the part's threads the frame before), and out,
// slides in as it arrives from the hero, plays the jokes, and shows the
// thread on a click, its composer taking the focus. It stays as though
// pointed at while its snooze popover is open.
func (a *app) sidebarListRow(c *ui.Context, thread string, was map[api.ID]bool, e ui.Element) bool {
	id, err := api.ParseID(thread)
	if err != nil {
		return false
	}
	t, ok := a.find(id)
	if !ok {
		return false
	}
	if !was[id] {
		e.Transition(fold)
	} else {
		e.Transition(foldOut)
	}
	if a.arriving(t) {
		e.Transition(arrive)
	}
	if fx, playing := a.rowJoke(c, id); playing {
		rowGag(e, fx)
	}
	if e.Clicked() {
		a.choose(t)
	}
	return a.snoozeFor == id
}
