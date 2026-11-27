package main

import (
	"encoding/json"
	"slices"
	"time"

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
// empty, whether the user closed it, and its rows in order: while open
// and, for a copy whose items name the section, while it closes, till the
// curtain has covered them.
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
	Full     bool   `json:"full,omitempty"`
}

// sidebarPage is how many threads a section, or a thread's children,
// shows till the user asks for more: past it, a row says how many more
// there are and shows the next as many.
const sidebarPage = 100

// sidebarSpare is the room a copy's list keeps, past its last thread, for
// the rows that say how many more there are: one for each section, and
// each thread's children, it is in the middle of.
const sidebarSpare = 2 * (maxDepth + 2)

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
	// However many pages the user asked for, the list holds no more items
	// than a list can: each section's header and the row under it, the
	// threads, and the rows that say how many more there are.
	left := widget.MaxListItems - 2*len(cats)
	more := func(n int, parent string, depth int) sidebarRow {
		left--
		return sidebarRow{More: n, Parent: parent, Depth: depth, Full: left < sidebarSpare}
	}
	for _, cat := range cats {
		items := a.itemsIn(cat)
		a.sectionCounts[cat] = len(items)
		if len(items) == 0 && cat != catInbox {
			continue
		}
		s := categories[cat]
		sec := sidebarSection{ID: sectionNames[cat], Title: s.title, Empty: s.empty, Closed: cat != catInbox && a.sectionClosed[cat], Rows: []sidebarRow{}}
		if !sec.Closed || a.lingers(cat) {
			var kids func(t thread, depth int)
			kids = func(t thread, depth int) {
				if !a.childrenOpen[t.id()] || depth > maxDepth {
					return
				}
				ks := a.shownKids(t)
				var kept []int
				for i, k := range ks {
					if a.sidebarKept[k.id()] {
						kept = append(kept, i)
					}
				}
				if n := a.paged(t.id().String(), len(ks), kept, &left, func(i int) api.ID { return ks[i].id() }, func(i int) {
					sec.Rows = append(sec.Rows, a.sidebarRow(ks[i], depth))
					kids(ks[i], depth+1)
				}); n > 0 {
					sec.Rows = append(sec.Rows, more(n, t.id().String(), depth))
				}
			}
			var kept []int
			for id := range a.sidebarKept {
				if i, ok := a.sorted.index[cat][id]; ok {
					kept = append(kept, i)
				}
			}
			slices.Sort(kept)
			if n := a.paged(sec.ID, len(items), kept, &left, func(i int) api.ID { return items[i].t.id() }, func(i int) {
				sec.Rows = append(sec.Rows, a.sidebarRow(items[i].t, 0))
				kids(items[i].t, 1)
			}); n > 0 {
				sec.Rows = append(sec.Rows, more(n, "", 0))
			}
		}
		out = append(out, sec)
	}
	return out
}

// paged adds those of n threads the pages of key show, in order: as many
// pages as the user asked for, then of those kept, at their places in
// order, those past the pages, a page of them at most, and the shown
// thread and its parents always, while the list has room left. It returns
// how many more there are.
func (a *app) paged(key string, n int, kept []int, left *int, id func(int) api.ID, add func(int)) (more int) {
	shown, added, past := min(n, sidebarPage*max(a.sidebarPages[key], 1)), 0, 0
	for added < shown && *left > sidebarSpare {
		*left--
		add(added)
		added++
	}
	paged := added
	for _, i := range kept {
		if i < paged {
			continue
		}
		if *left > sidebarSpare && (i < shown || past < sidebarPage) || *left > sidebarSpare/2 && a.onSelectedPath(id(i)) {
			if i >= shown {
				past++
			}
			*left--
			add(i)
			added++
		}
	}
	return n - added
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
	var reveal func(*widget.Node, widget.ListItem) bool
	if !a.revealing.IsZero() && a.revealing == a.selected {
		reveal = func(_ *widget.Node, it widget.ListItem) bool {
			id, err := api.ParseID(it.Thread)
			return err == nil && id == a.revealing
		}
	}
	return componentCopy{
		key:     part,
		box:     box,
		native:  func(*widget.Node) { native() },
		current: true,
		context: func(plugins.ThreadRecord) any { return body },
		listRow: func(c *ui.Context, list *widget.Node, i int, e ui.Element, _ bool) bool {
			return a.sidebarListRow(c, list.Items[i].Thread, list.Items[i].Section, was, e)
		},
		reveal: reveal,
		fold: func(_ *widget.Node, section string) (float32, bool) {
			cat, ok := catNamed(section)
			f := a.sectionFold[cat]
			if !ok || f == nil {
				return 1, false
			}
			f.saw = true
			return f.p, f.p < 1
		},
	}
}

// sectionFold is how far open a section of the bottom part shows, as it
// opens and shuts: p, 0 shut to 1 open, on its way from from to to since
// at. live is whether the section had threads the frame before; saw
// whether a copy's list named it this frame (render.Style.Fold), and
// named whether one did the frame before. span is the most of its rows
// Glitched's own curtain went over in this fold (foldSpan).
type sectionFold struct {
	from, to, p      float32
	at               time.Time
	live, saw, named bool
	span             float32
}

// foldSections moves each section of the bottom part on to open or shut,
// as the user left it, over the chevron's turn: from where it is, so that
// a click mid-way turns it round. It is there at once with less motion
// asked for, as the section shows, at launch or holding threads anew, and
// should the clock go back.
func (a *app) foldSections(c *ui.Context) {
	reduce := c.Preferences().ReduceMotion
	for _, cat := range []category{catWorking, catSnoozed, catSettled} {
		f := a.sectionFold[cat]
		if f == nil {
			f = &sectionFold{}
			a.sectionFold[cat] = f
		}
		f.named, f.saw = f.saw, false
		to := float32(1)
		if a.sectionClosed[cat] {
			to = 0
		}
		has := len(a.itemsIn(cat)) > 0
		if !f.live || !has || reduce || a.now.Before(f.at) {
			f.from, f.to, f.p, f.at, f.live = to, to, to, a.now, has
			continue
		}
		if to != f.to {
			f.from, f.to, f.at = f.p, to, a.now
		}
		k := min(float32(a.now.Sub(f.at))/float32(turn.Duration), 1)
		f.p = f.from + (f.to-f.from)*easeOut(k)
		if k >= 1 {
			f.p = f.to
		}
		if f.p != f.to {
			c.AnimationFrame()
		}
	}
}

// sectionP is how far open the section cat shows, 0 shut to 1 open: the
// inbox, always open, is 1.
func (a *app) sectionP(cat category) float32 {
	if f := a.sectionFold[cat]; f != nil {
		return f.p
	}
	if cat != catInbox && a.sectionClosed[cat] {
		return 0
	}
	return 1
}

// lingers is whether the section cat, shut, still shows some of its rows,
// as a copy's list folded it the frame before: a copy that names no
// section shows them no more from the click on.
func (a *app) lingers(cat category) bool {
	f := a.sectionFold[cat]
	return f != nil && f.p > 0 && f.named
}

// folding is whether a section of the bottom part opens or shuts now.
func (a *app) folding() bool {
	for _, f := range a.sectionFold {
		if f.p != f.to {
			return true
		}
	}
	return false
}

// foldingNamed is whether the section a list item names opens or shuts
// now.
func (a *app) foldingNamed(section string) bool {
	cat, ok := catNamed(section)
	f := a.sectionFold[cat]
	return ok && f != nil && f.p < 1
}

// catNamed is the section a copy's context names id (sectionNames).
func catNamed(id string) (category, bool) {
	for cat, s := range sectionNames {
		if s == id && id != "" {
			return cat, true
		}
	}
	return 0, false
}

// foldOut is fold without its way in: a row that scrolls into a list
// was there all along.
var foldOut = func() ui.ElementTransition { t := fold; t.Enter = nil; return t }()

// foldStill is fold's colors alone, for a row of a section opening or
// shutting: the section's curtain sizes it, in layout, and a row's own
// size moving after it, or folding in as it is uncovered, would run apart
// from the curtain.
var foldStill = ui.ElementTransition{Colors: fold.Colors, Duration: fold.Duration, Ease: fold.Ease}

// sidebarListRow does to a thread's row in sidebar.body what the sidebar
// does to its own: it folds in, as its thread joins the part (not among
// was, the part's threads the frame before), and out,
// slides in as it arrives from the hero, plays the jokes, and shows the
// thread on a click, its composer taking the focus. It stays as though
// pointed at while its snooze popover is open. While the section its item
// names opens or shuts, the section's curtain alone moves the row.
func (a *app) sidebarListRow(c *ui.Context, thread, section string, was map[api.ID]bool, e ui.Element) bool {
	id, err := api.ParseID(thread)
	if err != nil {
		return false
	}
	t, ok := a.find(id)
	if !ok {
		return false
	}
	a.revealRow(id)
	folding := a.foldingNamed(section)
	switch {
	case folding:
		e.Transition(foldStill)
	case !was[id]:
		e.Transition(fold)
	default:
		e.Transition(foldOut)
	}
	if a.arriving(t) && !folding {
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
