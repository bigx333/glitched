package main

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plugins/widget"
)

// foldFrame is one refresh of the Tester's window.
const foldFrame = time.Second / 60

// foldScene is scene "sidebar", Snoozed and Settled open with open, with
// many threads more settled, its bottom part drawn by the built-in.
func foldScene(t *testing.T, open bool, many int) *harness {
	t.Helper()
	o := ""
	if open {
		o = "1"
	}
	t.Setenv("OPEN", o)
	t.Setenv("MANY", strconv.Itoa(many))
	t.Setenv("MANYIN", "")
	t.Setenv("SIDEBARDOWN", "")
	h := sceneHarness(t, "sidebar")
	bottomDrawnBy(t, h, "sidebar")
	return h
}

// bottomDrawnBy fails t unless the plugin drew the sidebar's bottom part
// in the last frame: Glitched's own drawn in its place, as when the
// built-in's signature is refused, looks much the same.
func bottomDrawnBy(t *testing.T, h *harness, plugin string) {
	t.Helper()
	d := h.a.partsDrawn["sidebar.body"]
	if d == nil || len(d.copies) != 2 || d.copies[1].plugin != plugin || h.a.plugins.Failed(plugin) != nil {
		var by []copyDrawn
		if d != nil {
			by = d.copies
		}
		t.Fatalf("the sidebar's bottom part isn't drawn by %s: copies %+v, failed %v", plugin, by, h.a.plugins.Failed(plugin))
	}
}

// bottomItems are the items of the list the plugin drew the bottom part
// with, as it last drew it.
func bottomItems(h *harness, plugin, part string) []widget.ListItem {
	app := h.a.plugins.App(plugin)
	if app == nil || app.PlaceView("body", part) == nil {
		return nil
	}
	var items []widget.ListItem
	app.PlaceView("body", part).Walk(func(n *widget.Node) {
		if n.Type == "list" {
			items = n.Items
		}
	})
	return items
}

// yOf is the top of what shows text, NaN where nothing does.
func yOf(h *harness, text string) float32 {
	r, ok := h.tt.Find(text)
	if !ok {
		return float32(math.NaN())
	}
	return r.Y
}

// rowH is how tall the sidebar shows the row of the thread titled title,
// 0 where it shows none.
func rowH(h *harness, title string) float32 {
	r, ok := revealRowOf(h, title)
	if !ok {
		return 0
	}
	return r.H
}

// bottomList is the bottom part's list, as assistive tools see it.
func bottomList(t *testing.T, h *harness) ui.Rect {
	t.Helper()
	l, ok := h.tt.AccessByRole(ui.RoleList, "Working, Snoozed and Settled")
	if !ok {
		t.Fatal("no list of Working, Snoozed and Settled")
	}
	return l.Bounds
}

// chevronAxis is which way the chevron at the end of the header titled
// title lies, in degrees, 0 to 180, in the window's axes: the long axis of
// its ink, from the ink's second moments, each pixel weighted by how far it
// is from the header's background, in a box at the header as it is in the
// frame, so that the header moving doesn't count, only the chevron turning.
func chevronAxis(h *harness, title string) (float64, bool) {
	r, found := h.tt.Find(title)
	if !found {
		return 0, false
	}
	img := h.tt.Image()
	s := float64(img.Bounds().Dx()) / 1320
	cy := float64(r.Y + r.H/2)
	x0, x1 := float64(h.a.sidebarWidth)-40, float64(h.a.sidebarWidth)-4
	y0, y1 := cy-10, cy+10
	lum := func(px, py int) float64 {
		c := img.RGBAAt(px, py)
		return 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
	}
	bg := lum(int(x0*s), int(cy*s))
	type inked struct{ x, y, w float64 }
	var ink []inked
	var sx, sy, sw float64
	for py := int(y0 * s); py < int(y1*s); py++ {
		for px := int(x0 * s); px < int(x1*s); px++ {
			if w := math.Abs(lum(px, py) - bg); w > 6 {
				x, y := float64(px)+0.5, float64(py)+0.5
				ink = append(ink, inked{x, y, w})
				sx, sy, sw = sx+w*x, sy+w*y, sw+w
			}
		}
	}
	if sw == 0 {
		return 0, false
	}
	mx, my := sx/sw, sy/sw
	var xx, yy, xy float64
	for _, p := range ink {
		dx, dy := p.x-mx, p.y-my
		xx, yy, xy = xx+p.w*dx*dx, yy+p.w*dy*dy, xy+p.w*dx*dy
	}
	deg := 0.5 * math.Atan2(2*xy, xx-yy) * 180 / math.Pi
	return math.Mod(deg+180, 180), true
}

// chevronTurn measures a chevron's turn from its axis shut.
type chevronTurn struct{ shut float64 }

// chevronTurnOf is the measure of a chevron whose axis is shut at rest
// shut and open at rest open, which must be a quarter turn apart: t
// fails where they aren't.
func chevronTurnOf(t testing.TB, shut, open float64) chevronTurn {
	t.Helper()
	if d := math.Mod(math.Abs(open-shut), 180); math.Abs(d-90) > 5 {
		t.Fatalf("the chevron's axis is %.1f° shut and %.1f° open: %.1f° apart, not a quarter turn", shut, open, d)
	}
	return chevronTurn{shut}
}

// at is how far the chevron whose axis is axis turned, -30° to 150°: it
// turns clockwise on the screen as it opens, as the axis's angle grows,
// y going down.
func (c chevronTurn) at(axis float64) float64 {
	deg := math.Mod(axis-c.shut+360, 180)
	if deg > 150 {
		deg -= 180
	}
	return deg
}

// foldShot is what a frame of a fold shows: the section's progress, its
// header's top and the next one's, its first row's height, the bottom
// list's, and the chevron's turn, NaN where it couldn't be measured.
type foldShot struct {
	p               float32
	head, next, row float32
	text            float32 // what shows of the row's title, after layout
	list            float32
	turn            float64
	axis            float64 // the chevron's (chevronAxis), NaN where none
	others          map[string]float32
	at              time.Time // the Tester's clock
}

func (s foldShot) String() string {
	return fmt.Sprintf("p=%.3f head=%.1f next=%.1f row=%.1f list=%.1f turn=%.1f°", s.p, s.head, s.next, s.row, s.list, s.turn)
}

// foldWatch records the frames of a section folding: header is its title,
// next the header under it ("" for none), row its first row's title.
type foldWatch struct {
	t            *testing.T
	h            *harness
	cat          category
	header, next string
	row          string
	turn         chevronTurn
	turned       bool
	others       []string
	shots        []foldShot
}

func (w *foldWatch) shot() foldShot {
	h := w.h
	s := foldShot{p: h.a.sectionP(w.cat), head: yOf(h, w.header), row: rowH(h, w.row), list: bottomList(w.t, h).H, turn: math.NaN(), at: h.tt.Now()}
	if w.next != "" {
		s.next = yOf(h, w.next)
	}
	if r, ok := h.tt.Find(w.row); ok && r.X < h.a.sidebarWidth {
		s.text = r.H
	}
	s.axis = math.NaN()
	if axis, ok := chevronAxis(h, w.header); ok {
		s.axis = axis
		if w.turned {
			s.turn = w.turn.at(axis)
		}
	}
	if len(w.others) > 0 {
		s.others = map[string]float32{}
		for _, o := range w.others {
			s.others[o] = yOf(h, o)
		}
	}
	return s
}

// calibrate measures the chevron of the header shut and open, which the
// section is at rest at both.
func (w *foldWatch) calibrate(shut, open float64) {
	w.turn = chevronTurnOf(w.t, shut, open)
	w.turned = true
}

// ink is which way the chevron at the end of the header titled title
// lies (chevronAxis).
func ink(t *testing.T, h *harness, title string) float64 {
	t.Helper()
	axis, ok := chevronAxis(h, title)
	if !ok {
		t.Fatalf("no chevron at the end of %s", title)
	}
	return axis
}

// click clicks the header, the plugins drawing what it changed, in the
// same instant: the frame the fold starts in.
func (w *foldWatch) click() foldShot {
	w.t.Helper()
	if err := w.h.tt.Click(w.header); err != nil {
		w.t.Fatal(err)
	}
	settlePlugins(w.t, w.h.a, w.h.tt, w.h.p)
	s := w.shot()
	w.shots = append(w.shots, s)
	return s
}

// frames records n frames more.
func (w *foldWatch) frames(n int) []foldShot {
	var out []foldShot
	for range n {
		w.h.tt.Advance(foldFrame)
		s := w.shot()
		out = append(out, s)
		w.shots = append(w.shots, s)
	}
	return out
}

func (w *foldWatch) log() {
	for i, s := range w.shots {
		w.t.Logf("frame %2d (%5.1f ms): %v", i, float64(i)*1000/60, s)
	}
}

// curtainRow is how tall a section of one 32 pt row shows its row at p:
// the curtain uncovers p of the row and its 2 pt gap.
func curtainRow(p float32) float32 { return max(p*34-2, 0) }

// moves is whether the curtain of a one-row section shows the row more
// or less from frame a to frame b.
func moves(a, b foldShot) bool { return math.Abs(float64(curtainRow(a.p)-curtainRow(b.p))) > 0.01 }

func closeTo(a, b, by float32) bool { return float32(math.Abs(float64(a-b))) <= by }

// checkTurn fails t where the chevron isn't turned 90°·p, within the
// measure's few degrees.
func checkTurn(t *testing.T, i int, s foldShot) {
	t.Helper()
	if math.IsNaN(s.turn) || math.Abs(s.turn-90*float64(s.p)) > 10 {
		t.Errorf("frame %d: the chevron is turned %.1f°, the section %.3f open (want %.1f°)", i, s.turn, s.p, 90*s.p)
	}
}

// checkTurns fails t where a chevron turns back from the way its section
// goes, frame to frame, by more than the measure's degree.
func checkTurns(t *testing.T, ps []float32, turns []float64) {
	t.Helper()
	for i := 1; i < len(turns); i++ {
		d := turns[i] - turns[i-1]
		if ps[i] > ps[i-1] && d < -1 || ps[i] < ps[i-1] && d > 1 || ps[i] == ps[i-1] && math.Abs(d) > 1 {
			t.Errorf("frame %d: the chevron turned %.1f° to %.1f° as the section went %.3f to %.3f", i, turns[i-1], turns[i], ps[i-1], ps[i])
		}
	}
}

// checkCurve fails t where the section isn't as far open as the
// chevron's curve has it at the frame s, the fold started at start,
// opening with open.
func checkCurve(t *testing.T, i int, s foldShot, start time.Time, open bool) {
	t.Helper()
	k := min(float32(s.at.Sub(start))/float32(turn.Duration), 1)
	want := easeOut(k)
	if !open {
		want = 1 - want
	}
	if math.Abs(float64(s.p-want)) > 1e-3 {
		t.Errorf("frame %d, %v in: the section is %.4f open, its curve %.4f", i, s.at.Sub(start), s.p, want)
	}
}

// titleIn is where the row's title sits in it, and how tall it is, with
// the section at rest open.
func titleIn(t *testing.T, h *harness, title string) (off, tall float32) {
	t.Helper()
	row, ok := revealRowOf(h, title)
	text, found := h.tt.Find(title)
	if !ok || !found {
		t.Fatalf("no row of %s at rest", title)
	}
	return text.Y - row.Y, text.H
}

// checkTitle fails t where what shows of the row's title isn't what the
// curtain uncovered of it, as a row whose own size moves after layout
// shows more or less.
func checkTitle(t *testing.T, i int, s foldShot, off, tall float32) {
	t.Helper()
	if want := min(max(s.row-off, 0), tall); !closeTo(s.text, want, 0.75) {
		t.Errorf("frame %d: %.2f of the title shows, the curtain uncovered %.2f of it (%v)", i, s.text, want, s)
	}
}

// Snoozed opens as a curtain, over the chevron's 200 ms on its curve: the
// bottom box grows up, so its header rises, frame by frame, as its row
// shows more of itself from the top and the chevron turns, all with the
// same progress; Settled, under it, stays. At 200 ms it is as a section
// opened at once is, and stays so.
func TestSidebarFoldOpensSnoozedAsACurtain(t *testing.T) {
	h := foldScene(t, false, 0)
	for _, it := range bottomItems(h, "sidebar", "bottom") {
		if it.Section != "" && (strings.HasPrefix(it.Key, "h:") || strings.HasPrefix(it.Key, "e:")) {
			t.Errorf("the built-in's header %q names a section, %q: it would fold", it.Key, it.Section)
		}
	}
	w := &foldWatch{t: t, h: h, cat: catSnoozed, header: "Snoozed", next: "Settled", row: "Snoozed until tomorrow"}
	shut := ink(t, h, "Snoozed")
	before := w.shot()
	if before.p != 0 || before.row != 0 {
		t.Fatalf("Snoozed shut: %v", before)
	}
	s0 := w.click()
	// The thread's row names its section; its header doesn't.
	var named bool
	for _, it := range bottomItems(h, "sidebar", "bottom") {
		switch {
		case strings.HasPrefix(it.Key, "h:") || strings.HasPrefix(it.Key, "e:"):
			if it.Section != "" {
				t.Errorf("the built-in's header %q names section %q", it.Key, it.Section)
			}
		case it.Thread != "" || strings.HasPrefix(it.Key, "m:") || strings.HasPrefix(it.Key, "f:"):
			named = named || it.Section == "snoozed"
		}
	}
	if !named {
		t.Errorf("the built-in's Snoozed row names no section: %+v", bottomItems(h, "sidebar", "bottom"))
	}
	for _, it := range bottomItems(h, "sidebar", "inbox") {
		if it.Section != "" {
			t.Errorf("the inbox's item %q names section %q: the inbox never folds", it.Key, it.Section)
		}
	}
	if s0.p != 0 || s0.head != before.head || s0.next != before.next || s0.row != 0 {
		t.Errorf("the click's frame moved: %v, was %v", s0, before)
	}
	got := w.frames(13)
	for i, s := range got {
		checkCurve(t, i+1, s, s0.at, true)
	}
	for i, s := range got[:11] {
		prev := w.shots[i]
		if !(s.p > prev.p) || moves(prev, s) && (!(s.head < prev.head) || !(s.row > prev.row) || !(s.list > prev.list)) {
			t.Errorf("frame %d: %v after %v; want the header rising, the row and the list growing", i+1, s, prev)
		}
		if !closeTo(s.next, before.next, 0.01) {
			t.Errorf("frame %d: Settled moved, %v → %v", i+1, before.next, s.next)
		}
		if want := curtainRow(s.p); !closeTo(s.row, want, 0.75) {
			t.Errorf("frame %d: the row shows %.2f at p %.3f, want %.2f", i+1, s.row, s.p, want)
		}
		if !closeTo(before.head-s.head, s.row, 0.75) {
			t.Errorf("frame %d: the header rose %.2f, the row shows %.2f: out of step", i+1, before.head-s.head, s.row)
		}
	}
	if s := got[12]; s.p != 1 || s.row != rowHeight {
		t.Errorf("at 216 ms: %v, want it open", s)
	}
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	rest := w.shot()
	off, tall := titleIn(t, h, "Snoozed until tomorrow")
	for i, s := range got[:12] {
		checkTitle(t, i+1, s, off, tall)
	}
	if end := got[11]; !closeTo(end.head, rest.head, 0.5) || !closeTo(end.row, rest.row, 0.5) || !closeTo(end.next, rest.next, 0.01) || !closeTo(end.list, rest.list, 0.5) {
		t.Errorf("at 200 ms %v, at rest %v: it jumps at the end", end, rest)
	}
	open := ink(t, h, "Snoozed")
	w.calibrate(shut, open)
	// The chevron, measured off the frames again.
	h2 := foldScene(t, false, 0)
	w2 := &foldWatch{t: t, h: h2, cat: catSnoozed, header: "Snoozed", next: "Settled", row: "Snoozed until tomorrow"}
	w2.calibrate(shut, open)
	w2.click()
	var ps []float32
	var turns []float64
	for i, s := range w2.frames(13) {
		checkTurn(t, i+1, s)
		ps, turns = append(ps, s.p), append(turns, s.turn)
	}
	checkTurns(t, ps, turns)
	w2.log()
	// As one left open.
	fresh := foldScene(t, false, 0)
	fresh.a.sectionClosed[catSnoozed] = false
	fresh.tt.Frame()
	settlePlugins(t, fresh.a, fresh.tt, fresh.p)
	fresh.tt.Advance(time.Second)
	settlePlugins(t, fresh.a, fresh.tt, fresh.p)
	fw := &foldWatch{t: t, h: fresh, cat: catSnoozed, header: "Snoozed", next: "Settled", row: "Snoozed until tomorrow"}
	if f := fw.shot(); f.head != rest.head || f.row != rest.row || f.list != rest.list || f.next != rest.next {
		t.Errorf("opened, it shows %v; left open, %v", rest, f)
	}
	if t.Failed() {
		w.log()
	}
}

// Snoozed shuts as the curtain goes back down: the header falls, the row
// shows less of itself, and stays among the built-in's items till nothing
// of it shows, and then goes, with no jump.
func TestSidebarFoldShutsSnoozedAsACurtain(t *testing.T) {
	h := foldScene(t, true, 0)
	w := &foldWatch{t: t, h: h, cat: catSnoozed, header: "Snoozed", next: "Settled", row: "Snoozed until tomorrow"}
	before := w.shot()
	if before.p != 1 || before.row != rowHeight {
		t.Fatalf("Snoozed open: %v", before)
	}
	off, tall := titleIn(t, h, "Snoozed until tomorrow")
	listed := func() bool {
		for _, it := range bottomItems(h, "sidebar", "bottom") {
			if it.Section == "snoozed" && it.Thread != "" {
				return true
			}
		}
		return false
	}
	s0 := w.click()
	if s0.p != 1 || s0.head != before.head || s0.row != before.row {
		t.Errorf("the click's frame moved: %v, was %v", s0, before)
	}
	for i := range 13 {
		s := w.frames(1)[0]
		prev := w.shots[i]
		checkCurve(t, i+1, s, s0.at, false)
		if s.p > 0 {
			if !(s.p < prev.p) || moves(prev, s) && (!(s.head > prev.head) || !(s.row < prev.row) || !(s.list < prev.list)) ||
				!moves(prev, s) && (s.head != prev.head || s.row != prev.row || s.list != prev.list) {
				t.Errorf("frame %d: %v after %v; want the header falling, the row and the list shrinking", i+1, s, prev)
			}
			if !listed() {
				t.Errorf("frame %d: the row left the built-in's items at p %.3f", i+1, s.p)
			}
			if want := curtainRow(s.p); !closeTo(s.row, want, 0.75) {
				t.Errorf("frame %d: the row shows %.2f at p %.3f, want %.2f", i+1, s.row, s.p, want)
			}
			checkTitle(t, i+1, s, off, tall)
		} else if listed() {
			t.Errorf("frame %d: shut, the row is still among the built-in's items", i+1)
		}
		if !closeTo(s.next, before.next, 0.01) {
			t.Errorf("frame %d: Settled moved, %v → %v", i+1, before.next, s.next)
		}
	}
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	rest := w.shot()
	if end := w.shots[12]; !closeTo(end.head, rest.head, 0.5) || end.row != 0 || rest.row != 0 || !closeTo(end.list, rest.list, 0.5) {
		t.Errorf("at 200 ms %v, at rest %v: it jumps at the end", end, rest)
	}
	if !closeTo(rest.head-before.head, rowHeight, 0.5) {
		t.Errorf("shut, the header fell %.2f, want %d", rest.head-before.head, rowHeight)
	}
	if t.Failed() {
		w.log()
	}
}

// Settled with 40 threads more opens to the bottom box's most, half the
// sidebar, growing all the way, and shuts back the same way; no row
// jumps between its last frame folding and its rest.
func TestSidebarFoldOpensAndShutsALongSection(t *testing.T) {
	h := foldScene(t, false, 40)
	w := &foldWatch{t: t, h: h, cat: catSettled, header: "Settled", row: "Settled long ago", others: []string{"Snoozed"}}
	before := w.shot()
	w.click()
	w.frames(14)
	for i := 1; i < len(w.shots); i++ {
		s, prev := w.shots[i], w.shots[i-1]
		if s.list < prev.list || s.head > prev.head || s.others["Snoozed"] > prev.others["Snoozed"] {
			t.Errorf("opening, frame %d: %v after %v", i, s, prev)
		}
		if i <= 5 && !(s.list > prev.list && s.p > prev.p) {
			t.Errorf("opening, frame %d: %v after %v; want the box growing", i, s, prev)
		}
	}
	cap := h.a.sidebarTall / 2
	last := w.shots[len(w.shots)-1]
	if !closeTo(last.list, cap, 0.5) {
		t.Errorf("open, the list is %.1f tall, want the box's most, %.1f", last.list, cap)
	}
	rows := func() map[string]float32 {
		out := map[string]float32{}
		l := bottomList(t, h)
		for _, x := range h.tt.AccessAll() {
			if x.Role == ui.RoleButton && x.Bounds.X < h.a.sidebarWidth && x.Bounds.Y >= l.Y && x.Bounds.Y < l.Y+l.H && x.Bounds.H > 0 {
				out[x.Name] = x.Bounds.Y
			}
		}
		return out
	}
	settled := rows()
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	for name, y := range rows() {
		if was, ok := settled[name]; ok && !closeTo(was, y, 0.5) {
			t.Errorf("opened, the row %q went from %.1f to %.1f at rest", name, was, y)
		}
	}
	if s := w.shot(); !closeTo(s.list, last.list, 0.5) || !closeTo(s.head, last.head, 0.5) {
		t.Errorf("opened, %v; at rest %v", last, s)
	}
	if t.Failed() {
		w.log()
	}
	// And shut.
	w.shots = nil
	w.click()
	w.frames(14)
	for i := 1; i < len(w.shots); i++ {
		s, prev := w.shots[i], w.shots[i-1]
		if s.list > prev.list || s.others["Snoozed"] < prev.others["Snoozed"] {
			t.Errorf("shutting, frame %d: %v after %v", i, s, prev)
		}
		if i <= 5 && !(s.list < prev.list && s.p < prev.p) {
			t.Errorf("shutting, frame %d: %v after %v; want the box shrinking", i, s, prev)
		}
	}
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	if s, end := w.shot(), w.shots[len(w.shots)-1]; !closeTo(s.list, end.list, 0.5) || !closeTo(s.head, end.head, 0.5) || !closeTo(s.list, before.list, 0.5) || !closeTo(s.head, before.head, 0.5) {
		t.Errorf("shut, %v; at rest %v; before it opened %v", end, s, before)
	}
	if t.Failed() {
		w.log()
	}
}

// With Snoozed open over Settled, Settled opening and shutting moves
// Snoozed's header and row with it, as far as Settled shows.
func TestSidebarFoldMovesTheSectionsAboveWithIt(t *testing.T) {
	h := foldScene(t, true, 0)
	w := &foldWatch{t: t, h: h, cat: catSettled, header: "Settled", row: "Settled long ago", others: []string{"Snoozed", "Snoozed until tomorrow"}}
	open := w.shot()
	w.click()
	w.frames(13)
	h.tt.Advance(time.Second)
	shut := w.shot()
	if !closeTo(shut.others["Snoozed"]-open.others["Snoozed"], rowHeight, 0.5) {
		t.Fatalf("Settled shut moved Snoozed %v → %v", open.others["Snoozed"], shut.others["Snoozed"])
	}
	check := func(what string) {
		if p := w.shots[3].p; !(p > 0 && p < 1) {
			t.Errorf("%s, Settled is %.3f open at 50 ms", what, p)
		}
		for i, s := range w.shots {
			for _, o := range w.others {
				// Snoozed falls as far as Settled's row shows less.
				want := open.others[o] + (rowHeight - curtainRow(s.p))
				if !closeTo(s.others[o], want, 0.75) {
					t.Errorf("%s, frame %d: %s at %.2f, Settled %.3f open: want %.2f", what, i, o, s.others[o], s.p, want)
				}
			}
		}
	}
	check("shutting")
	w.shots = nil
	w.click()
	w.frames(13)
	check("opening")
	if t.Failed() {
		w.log()
	}
}

// Clicked again mid-way, a section turns round from where it is, with
// no jump: its header moves no more in the frame it does than in a frame
// before.
func TestSidebarFoldTurnsRoundMidWay(t *testing.T) {
	h := foldScene(t, false, 0)
	w := &foldWatch{t: t, h: h, cat: catSnoozed, header: "Snoozed", next: "Settled", row: "Snoozed until tomorrow"}
	shut := w.shot()
	shutInk := ink(t, h, "Snoozed")
	w.click()
	w.frames(6) // 100 ms
	mid := w.shots[6]
	again := w.click()
	if again.p != mid.p || again.head != mid.head || again.row != mid.row {
		t.Errorf("clicked again at 100 ms: %v, was %v", again, mid)
	}
	// The most a frame moves it: the first of a fold over all of it, the
	// row and its gap.
	most := (rowHeight + 2) * w.shots[1].p
	w.frames(14)
	for i := 8; i < len(w.shots); i++ {
		s, prev := w.shots[i], w.shots[i-1]
		if s.head-prev.head > most+0.5 {
			t.Errorf("frame %d: the header jumped %.2f, more than the most a frame moves it, %.2f", i, s.head-prev.head, most)
		}
		if s.p > 0 && (!(s.p < prev.p) || moves(prev, s) && !(s.head > prev.head)) {
			t.Errorf("frame %d: %v after %v; want it shutting", i, s, prev)
		}
		if want := curtainRow(s.p); !closeTo(s.row, want, 0.75) {
			t.Errorf("frame %d: the row shows %.2f at p %.3f, want %.2f", i, s.row, s.p, want)
		}
	}
	if end := w.shots[len(w.shots)-1]; end.p != 0 || end.head != shut.head || end.row != 0 {
		t.Errorf("turned round, it ends %v; shut it was %v", end, shut)
	}
	// The chevron turned round with it, measured off the frames once it
	// is seen open.
	h.tt.Click("Snoozed")
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	w.calibrate(shutInk, ink(t, h, "Snoozed"))
	var ps []float32
	var turns []float64
	for i := range w.shots {
		s := &w.shots[i]
		s.turn = w.turn.at(s.axis)
		checkTurn(t, i, *s)
		ps, turns = append(ps, s.p), append(turns, s.turn)
	}
	checkTurns(t, ps, turns)
	if t.Failed() {
		w.log()
	}
}

// With less motion asked for, a section opens and shuts in the click's
// frame, and stays as it is then.
func TestSidebarFoldSnapsWithLessMotion(t *testing.T) {
	h := foldScene(t, false, 0)
	h.tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	h.tt.Advance(time.Second)
	w := &foldWatch{t: t, h: h, cat: catSnoozed, header: "Snoozed", next: "Settled", row: "Snoozed until tomorrow"}
	shut := w.shot()
	opened := w.click()
	if opened.p != 1 || opened.row != rowHeight || !closeTo(shut.head-opened.head, rowHeight, 0.5) {
		t.Errorf("opened with less motion: %v, shut %v", opened, shut)
	}
	h.tt.Advance(time.Second)
	if s := w.shot(); s.head != opened.head || s.row != opened.row || s.list != opened.list {
		t.Errorf("opened with less motion, it moved on: %v, then %v", opened, s)
	}
	shutAgain := w.click()
	if shutAgain.p != 0 || shutAgain.row != 0 || shutAgain.head != shut.head {
		t.Errorf("shut with less motion: %v, shut before %v", shutAgain, shut)
	}
}

// startOpen is start with Settled open before the window's first frame,
// as a section is at launch: with its threads in by then, loaded is set.
func startOpen(t *testing.T, dir string, loaded bool) *harness {
	t.Helper()
	p := &posts{wake: make(chan struct{}, 1)}
	a := testApp(t, dir, nil, p.post)
	a.sectionClosed[catSettled] = false
	if err := a.backend.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.backend.Close() })
	deadline := time.Now().Add(30 * time.Second)
	for loaded && len(a.itemsIn(catSettled)) < 3 {
		if !p.run() {
			select {
			case <-p.wake:
			case <-time.After(10 * time.Millisecond):
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the threads never came in")
		}
	}
	h := &harness{t, a, ui.NewTester(a.view, 1320, 860), p}
	h.until("this machine's engine", func() bool { return a.backend.Envs()[0].Status == client.Status(client.Online{}) })
	return h
}

// A section open as the app starts shows open from the frame its threads
// come in, whether they're in by the window's first frame or come after:
// nothing opens.
func TestSidebarFoldShowsASectionOpenAtLaunchAtOnce(t *testing.T) {
	for _, loaded := range []bool{true, false} {
		t.Run(fmt.Sprintf("threads in by the first frame %v", loaded), func(t *testing.T) {
			h := startOpen(t, pagedDir(t, pagedBatch{name: "Settled", n: 3, settled: true}), loaded)
			if loaded && h.a.sectionP(catSettled) != 1 {
				t.Errorf("Settled open at launch is %.3f open in the first frames", h.a.sectionP(catSettled))
			}
			h.until("the threads", func() bool { return len(h.a.itemsIn(catSettled)) == 3 })
			settlePlugins(t, h.a, h.tt, h.p)
			bottomDrawnBy(t, h, "sidebar")
			if p := h.a.sectionP(catSettled); p != 1 {
				t.Errorf("Settled open at launch is %.3f open", p)
			}
			first := rowH(h, "Settled 000")
			y := yOf(h, "Settled")
			h.tt.Advance(time.Second)
			settlePlugins(t, h.a, h.tt, h.p)
			if first != rowHeight || yOf(h, "Settled") != y || rowH(h, "Settled 000") != rowHeight {
				t.Errorf("Settled open at launch: its row %.1f tall, header at %.1f; a second on, %.1f and %.1f", first, y, rowH(h, "Settled 000"), yOf(h, "Settled"))
			}
		})
	}
}

// yourCopy is the built-in sidebar's module as a copy of yours, its
// place's answer passed through edit.
func yourCopy(t *testing.T, edit string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("internal", "plugins", "bundled", "sidebar", "main.ts"))
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	if !strings.Contains(js, "export function place(") {
		t.Fatal("the built-in has no place")
	}
	js = strings.Replace(js, "export function place(", "function builtinPlace(", 1)
	return js + "\nexport function place(arg) {\n" + edit + "\n}\n"
}

// installYourCopy installs yourCopy, through edit, as your copy of the
// sidebar, mine, and waits for it to draw the bottom part.
func installYourCopy(t *testing.T, h *harness, edit string) {
	t.Helper()
	writeOwnPlugin(t, h.a, "mine", strings.Replace(sidebarManifest, `"index.js"`, `"index.ts"`, 1), yourCopy(t, edit))
	h.a.plugins.SetGranted("mine", "ui.replace", true)
	h.a.plugins.SetGranted("mine", "threads:read", true)
	for range 40 {
		settlePlugins(t, h.a, h.tt, h.p)
		if d := h.a.partsDrawn["sidebar.body"]; d != nil && len(d.copies) == 2 && d.copies[1].plugin == "mine" {
			break
		}
		h.tt.Advance(50 * time.Millisecond)
	}
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	bottomDrawnBy(t, h, "mine")
}

// A copy of yours that names no item's section shuts a section in the
// click's frame, as before sections folded: its rows don't linger, whole,
// for the chevron's turn. The chevron still turns.
func TestSidebarFoldLeavesYourCopyWithoutSectionsAsItWas(t *testing.T) {
	h := foldScene(t, true, 0)
	installYourCopy(t, h, `
  const out = builtinPlace(arg);
  for (const v of out) if (v && v.items) v.items = v.items.map(({ section, ...it }) => it);
  return out;`)
	for _, it := range bottomItems(h, "mine", "bottom") {
		if it.Section != "" {
			t.Fatalf("your copy's item %q names section %q", it.Key, it.Section)
		}
	}
	w := &foldWatch{t: t, h: h, cat: catSnoozed, header: "Snoozed", next: "Settled", row: "Snoozed until tomorrow"}
	open := w.shot()
	if open.row != rowHeight {
		t.Fatalf("your copy shows Snoozed open as %v", open)
	}
	s0 := w.click()
	got := w.frames(3)
	if s0.row != 0 || !closeTo(s0.head-open.head, rowHeight, 0.5) {
		t.Errorf("shut in your copy, the click's frame shows %v (open %v): want it shut", s0, open)
	}
	for i, s := range got {
		if s.row != 0 || s.head != s0.head {
			t.Errorf("frame %d: %v; want it shut as at the click", i+1, s)
		}
	}
	if p := got[0].p; !(p > 0 && p < 1) {
		t.Errorf("the chevron doesn't turn: %.3f open a frame on", p)
	}
	if t.Failed() {
		w.log()
	}
}

// A thread joining an open section, which doesn't fold, still folds in
// as it did: its row grows over frames.
func TestSidebarFoldLeavesARowJoiningAnOpenSectionFoldingIn(t *testing.T) {
	h := foldScene(t, true, 0)
	x := h.thread("SIP re-INVITE handling")
	h.a.backend.SetDisposition(x.env, x.id(), api.Disposition{State: api.Snoozed, Until: morning(h.a.now, 1)})
	h.until("it in Snoozed", func() bool { return h.a.categoryOf(h.thread("SIP re-INVITE handling")) == catSnoozed })
	settlePlugins(t, h.a, h.tt, h.p)
	// What shows of its title: the row's folding in, after layout,
	// clips it.
	shows := func() float32 {
		r, ok := h.tt.Find("SIP re-INVITE handling")
		if !ok || r.X >= h.a.sidebarWidth {
			return 0
		}
		return r.H
	}
	var hs []float32
	for range 14 {
		hs = append(hs, shows())
		h.tt.Advance(foldFrame)
	}
	hs = append(hs, shows())
	full := hs[len(hs)-1]
	if full < 10 || rowH(h, "SIP re-INVITE handling") != rowHeight {
		t.Fatalf("the row joined Snoozed %.1f tall, its title %.1f", rowH(h, "SIP re-INVITE handling"), full)
	}
	grew := false
	for i := 1; i < len(hs); i++ {
		if hs[i] < hs[i-1] {
			t.Errorf("the row shrank as it joined: %v", hs)
		}
		grew = grew || hs[i] > hs[i-1] && hs[i-1] > 0
	}
	if !grew || hs[1] >= full {
		t.Errorf("the row joining an open section doesn't fold in: heights %v", hs)
	}
}

// A thread sent from the hero still slides into the sidebar: into the
// inbox part, which never folds. It guards that a row arrives as before
// while no section folds.
func TestSidebarFoldLeavesTheHerosRowArrivingInTheInbox(t *testing.T) {
	h := start(t, inboxDir(t))
	h.a.select_(h.thread("Investigate websocket reconnect loop"))
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	h.click("New thread")
	h.tt.Advance(time.Second)
	h.tt.Type("Build the hero")
	h.click("Send")
	var xs []float32
	for range 30 {
		h.tt.Advance(foldFrame)
		if r, ok := h.tt.Find("Build the hero"); ok && r.X < h.a.sidebarWidth {
			xs = append(xs, r.X)
		}
	}
	if len(xs) < 2 || !(xs[0] < xs[len(xs)-1]) {
		t.Errorf("the hero's row doesn't slide in: its x frame by frame %v", xs)
	}
}

// At rest, nothing folding, the sidebar draws as it did before sections
// folded, pixel for pixel: FOLD_BEFORE=<png> is the window of scene
// sidebar with OPEN=1 as it was, the left 330 DIPs of which it compares.
func TestSidebarFoldDrawsTheSidebarAtRestAsBefore(t *testing.T) {
	before := os.Getenv("FOLD_BEFORE")
	if before == "" {
		t.Skip()
	}
	f, err := os.Open(before)
	if err != nil {
		t.Fatal(err)
	}
	want, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	h := foldScene(t, true, 0)
	// As TestShot draws it.
	h.a.settings.theme, h.a.settings.tintPanels = darkTheme.name, false
	h.tt.Frame()
	h.tt.Advance(time.Second)
	got := h.tt.Image()
	w := 330 * got.Bounds().Dx() / 1320
	diff, first := 0, image.Point{-1, -1}
	box := image.Rectangle{Min: image.Point{1 << 30, 1 << 30}}
	for y := 0; y < got.Bounds().Dy(); y++ {
		for x := 0; x < w; x++ {
			r1, g1, b1, _ := got.At(x, y).RGBA()
			r2, g2, b2, _ := want.At(x, y).RGBA()
			if r1>>8 != r2>>8 || g1>>8 != g2>>8 || b1>>8 != b2>>8 {
				diff++
				if first.X < 0 {
					first = image.Point{x, y}
				}
				box.Min.X, box.Min.Y = min(box.Min.X, x), min(box.Min.Y, y)
				box.Max.X, box.Max.Y = max(box.Max.X, x+1), max(box.Max.Y, y+1)
			}
		}
	}
	if diff > 0 {
		if out := os.Getenv("FOLD_DIR"); out != "" {
			o, _ := os.Create(filepath.Join(out, "rest-open-now.png"))
			png.Encode(o, got)
			o.Close()
		}
		t.Errorf("at rest the sidebar draws %d pixels otherwise than before, first at %v, within %v", diff, first, box)
	}
}

// A plugin slow to draw: the frames don't wait for it, so the rows it
// draws open come late. Its frames, logged, say by how much the chevron
// runs ahead of the rows.
func TestSidebarFoldWithASlowPlugin(t *testing.T) {
	h := foldScene(t, false, 0)
	installYourCopy(t, h, `
  const until = Date.now() + 120;
  while (Date.now() < until) {}
  return builtinPlace(arg);`)
	waitingFirstDraws(t, time.Millisecond)
	w := &foldWatch{t: t, h: h, cat: catSnoozed, header: "Snoozed", next: "Settled", row: "Snoozed until tomorrow"}
	before := w.shot()
	if err := h.tt.Click("Snoozed"); err != nil {
		t.Fatal(err)
	}
	w.shots = append(w.shots, w.shot())
	for range 24 {
		time.Sleep(16 * time.Millisecond) // the plugin's time, as the window's
		h.p.run()
		w.frames(1)
	}
	firstTurn, firstRow, fullTurn, fullRow := -1, -1, -1, -1
	for i, s := range w.shots {
		if firstTurn < 0 && s.p > 0 {
			firstTurn = i
		}
		if firstRow < 0 && (s.row > 0 || s.head < before.head) {
			firstRow = i
		}
		if fullTurn < 0 && s.p == 1 {
			fullTurn = i
		}
		if fullRow < 0 && s.row == rowHeight {
			fullRow = i
		}
	}
	w.log()
	t.Logf("the chevron starts turning at frame %d and ends at %d; the rows start showing at frame %d and end at %d", firstTurn, fullTurn, firstRow, fullRow)
	if fullRow < 0 {
		t.Errorf("the slow plugin's rows never showed open")
	}
}
