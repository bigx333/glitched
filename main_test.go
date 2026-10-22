package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// The prototype builds without a window, and its toggles respond.
func TestView(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	if !tt.HasText("Sort sidebar threads by recent activity") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if err := tt.Click("View diff"); err != nil {
		t.Fatal(err)
	}
	if !a.showDiff || !tt.HasText("Changes") {
		t.Errorf("diff panel not shown")
	}
	if err := tt.Click("Fix terminal resize flicker on Windows"); err != nil {
		t.Fatal(err)
	}
	if a.selected != "t2" {
		t.Errorf("selected %q", a.selected)
	}
}

func TestSendPlaysCannedTurn(t *testing.T) {
	a := newApp()
	a.selected = "t3"
	tt := ui.NewTester(a.view, 1320, 860)
	_, th := a.current()
	a.draft = "Fix the flicker"
	tt.Frame()
	if err := tt.Click("Send"); err != nil {
		t.Fatal(err)
	}
	if len(th.entries) != 3 || th.status != statusCompleted {
		t.Fatalf("entries %d, status %d", len(th.entries), th.status)
	}
}

func TestSettingsPages(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	for _, s := range settingsSections {
		if err := tt.Click(s.label); err != nil {
			t.Fatal(err)
		}
		if a.router.Path() != "/settings/"+s.path {
			t.Errorf("path %q after clicking %q", a.router.Path(), s.label)
		}
	}
	if err := tt.Click("Appearance"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second) // let the page slide in
	if err := tt.Click("Light"); err != nil {
		t.Fatal(err)
	}
	if a.settings.theme != 1 {
		t.Errorf("theme %d", a.settings.theme)
	}
	if err := tt.Click("Back to app"); err != nil {
		t.Fatal(err)
	}
	if a.router.Path() != "/" {
		t.Errorf("path %q", a.router.Path())
	}
}

func TestCommandPalette(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	if err := tt.Click("Search"); err != nil {
		t.Fatal(err)
	}
	tt.Type("websocket")
	if !tt.HasText("Investigate websocket reconnect loop") {
		t.Fatalf("texts %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	if a.cmdkOpen || a.selected != "t4" {
		t.Errorf("open %v, selected %q", a.cmdkOpen, a.selected)
	}
}

func TestModelPicker(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	if err := tt.Click("Model"); err != nil {
		t.Fatal(err)
	}
	if !a.pickerOpen || a.pickerFamily != "Claude" {
		t.Fatalf("picker should open on Claude's models: %v %q", a.pickerOpen, a.pickerFamily)
	}
	// The families stay put whichever family's models show.
	claude, _ := tt.Find("Claude")
	// Pointing at a family opens its models.
	r, _ := tt.Find("Codex")
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	if moved, _ := tt.Find("Claude"); moved != claude {
		t.Errorf("Claude moved from %v to %v as Codex opened", claude, moved)
	}
	if err := tt.Click("GPT-5.5 Codex"); err != nil {
		t.Fatal(err)
	}
	if a.model != "GPT-5.5 Codex" {
		t.Errorf("model %q", a.model)
	}
	if err := tt.Click("Fast mode"); err != nil {
		t.Fatal(err)
	}
	if !a.fast {
		t.Error("fast mode not on")
	}
	tt.Advance(time.Second)
}

// The sidebar's shortcuts show only while ⌘ is held.
// The sidebar's commands show their keycaps, so the shortcuts are found.
func TestKeycaps(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	for _, s := range tt.Texts() {
		if s == "⌘" {
			return
		}
	}
	t.Error("no keycaps in the sidebar")
}

// The sidebar's right edge drags it wider or narrower, within limits,
// and Archived opens the archived threads.
func TestSidebarEdge(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	r, ok := tt.Find("Resize sidebar")
	if !ok {
		t.Fatal("no edge to drag")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x+60, y)
	tt.Release(x+60, y)
	if a.sidebarWidth != 332 {
		t.Errorf("sidebar %v wide after dragging its edge 60 right, want 332", a.sidebarWidth)
	}
	r, _ = tt.Find("Resize sidebar")
	x = r.X + r.W/2
	tt.Press(x, y)
	tt.Move(x-500, y)
	tt.Release(x-500, y)
	if a.sidebarWidth != minSidebarWidth {
		t.Errorf("sidebar %v wide after dragging far left, want %v", a.sidebarWidth, minSidebarWidth)
	}
	if err := tt.Click("Archived"); err != nil {
		t.Fatal(err)
	}
	if a.router.Path() != "/settings/archived" {
		t.Errorf("Archived opened %q", a.router.Path())
	}
}

// The effort's thumb follows the pointer while dragged, locking onto a
// level only near its point, and settles on the level it last locked
// onto when let go.
func TestEffortDrag(t *testing.T) {
	a := newApp()
	a.effort = "Low"
	tt := ui.NewTester(a.view, 1320, 860)
	if err := tt.Click("Model"); err != nil {
		t.Fatal(err)
	}
	faster, _ := tt.Find("Faster")
	smarter, _ := tt.Find("Smarter")
	left, right := faster.X, smarter.X+smarter.W
	y := faster.Y - 24 // the track's middle, above the labels
	at := func(level float32) float32 { return left + stopInset + (right-left-2*stopInset)*level/3 }

	tt.Press(at(0), y)
	tt.Move(at(0.5), y)
	tt.Move(at(0.8), y)
	if a.effort != "Low" {
		t.Errorf("effort %q at 0.8 levels, short of Medium's point, want Low", a.effort)
	}
	tt.Move(at(1.05), y)
	if a.effort != "Medium" {
		t.Errorf("effort %q at Medium's point, want Medium", a.effort)
	}
	// Past half way to High, but not at its point: still Medium, and
	// let go, it goes back to Medium.
	tt.Move(at(1.7), y)
	tt.Advance(time.Second)
	if a.effort != "Medium" {
		t.Errorf("effort %q at 1.7 levels, want Medium", a.effort)
	}
	tt.Release(at(1.7), y)
	tt.Advance(time.Second)
	if a.effort != "Medium" {
		t.Errorf("effort %q after letting go at 1.7 levels, want Medium", a.effort)
	}
}

// A click on the effort's track, away from a point, picks the nearest level.
func TestEffortClick(t *testing.T) {
	a := newApp()
	a.effort = "Low"
	tt := ui.NewTester(a.view, 1320, 860)
	if err := tt.Click("Model"); err != nil {
		t.Fatal(err)
	}
	faster, _ := tt.Find("Faster")
	smarter, _ := tt.Find("Smarter")
	left, right := faster.X, smarter.X+smarter.W
	x := left + stopInset + (right-left-2*stopInset)*2.3/3
	tt.ClickAtWith(0, x, faster.Y-24)
	tt.Advance(time.Second)
	if a.effort != "High" {
		t.Errorf("effort %q after clicking at 2.3 levels, want High", a.effort)
	}
}

// Full access is granted only by holding it; the other levels by a click.
func TestAccessLevels(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	if err := tt.Click("Access"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(accessAsk); err != nil {
		t.Fatal(err)
	}
	if a.access != accessAsk {
		t.Fatalf("access %q", a.access)
	}
	if err := tt.Click("Access"); err != nil {
		t.Fatal(err)
	}
	r, _ := tt.Find(accessFull)
	x, y := r.X+r.W/2, r.Y+r.H/2
	// A tap is not enough.
	tt.Press(x, y)
	tt.Advance(200 * time.Millisecond)
	tt.Release(x, y)
	tt.Advance(time.Second)
	if a.access != accessAsk {
		t.Fatalf("access %q after a tap on Full access", a.access)
	}
	tt.Move(x, y)
	tt.Press(x, y)
	tt.Advance(1200 * time.Millisecond)
	if a.access != accessFull {
		t.Fatalf("access %q after holding Full access", a.access)
	}
	tt.Release(x, y)
}

// Switching the access level moves nothing in the composer: its effects
// only paint.
func TestAccessEffectsKeepLayout(t *testing.T) {
	for _, level := range []string{accessAsk, accessAuto} {
		a := newApp()
		a.access = accessFull
		tt := ui.NewTester(a.view, 1320, 860)
		before, _ := tt.Find("High") // the effort, inside the composer
		a.access, a.accessAt = level, tt.Now()
		tt.Frame()
		tt.Advance(200 * time.Millisecond)
		if during, _ := tt.Find("High"); during != before {
			t.Errorf("%s: the composer's row moved from %v to %v during its effect", level, before, during)
		}
	}
}

// Threads move between the inbox's sections as they're settled, sent and
// finished; the strip filters them to a project.
func TestInbox(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	t2 := a.projects[0].threads[1] // pending approval, in the inbox
	if categoryOf(t2) != catInbox || categoryOf(a.projects[0].threads[0]) != catWorking {
		t.Fatal("mock threads in the wrong sections")
	}
	// Settle it from its row's actions.
	r, _ := tt.Find(t2.title)
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	if err := tt.Click("Settle"); err != nil {
		t.Fatal(err)
	}
	if categoryOf(t2) != catSettled {
		t.Errorf("settled thread in section %d", categoryOf(t2))
	}
	// It stays in the inbox a moment, sighing in its burst of confetti,
	// then folds away into Settled.
	if a.shownIn(t2) != catInbox {
		t.Error("settled thread left the inbox before its joke played")
	}
	tt.Advance(1500 * time.Millisecond)
	// Settled is closed by default; opening it shows the thread. (The
	// toast shows its title too, at the window's other side.)
	inSidebar := func(title string) bool {
		r, ok := tt.Find(title)
		return ok && r.X < a.sidebarWidth
	}
	if inSidebar(t2.title) {
		t.Error("closed Settled section shows its threads")
	}
	if err := tt.Click("Settled"); err != nil {
		t.Fatal(err)
	}
	if !inSidebar(t2.title) {
		t.Error("opened Settled section hides the settled thread")
	}
	// Sending in a settled thread brings it through Working back to the
	// inbox, unread (the test's agent finishes at once).
	a.select_(t2.id)
	a.draft = "and again"
	a.send(t2)
	if categoryOf(t2) != catInbox {
		t.Errorf("thread in section %d after its turn, want the inbox", categoryOf(t2))
	}
	// The strip filters to a project.
	if err := tt.Click("agent-phone"); err != nil {
		t.Fatal(err)
	}
	for _, cat := range []category{catInbox, catWorking, catSnoozed, catSettled} {
		for _, it := range a.itemsIn(cat) {
			if it.p.name != "agent-phone" {
				t.Errorf("filter to agent-phone shows %q of %s", it.t.title, it.p.name)
			}
		}
	}
	if items := a.itemsIn(catSnoozed); len(items) != 1 || items[0].t.title != "SIP re-INVITE handling" {
		t.Errorf("filter to agent-phone: snoozed %v", items)
	}
}

// Pointing at a thread swaps its time for its actions without moving the
// rows around it.
func TestInboxHoverKeepsLayout(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	first := a.projects[0].threads[1].title // the inbox's first row
	next := a.projects[0].threads[2].title
	before, _ := tt.Find(next)
	r, _ := tt.Find(first)
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(time.Second)
	if after, _ := tt.Find(next); after.Y != before.Y {
		t.Errorf("the next row moved from y %v to %v as the one above was pointed at", before.Y, after.Y)
	}
}

// Each of the inbox's jokes plays through without a hitch.
func TestInboxGags(t *testing.T) {
	for _, kind := range []gagKind{gagSnooze, gagSettle, gagUndo} {
		a := newApp()
		tt := ui.NewTester(a.view, 1320, 860)
		th := a.projects[0].threads[1]
		a.rowFx[th.id] = rowGagFx{kind: kind, at: tt.Now(), from: catInbox, hold: 500 * time.Millisecond, life: time.Second}
		for range 40 {
			tt.Advance(80 * time.Millisecond)
		}
		if _, ok := a.rowFx[th.id]; ok {
			t.Errorf("joke %d still playing after %v", kind, 40*80*time.Millisecond)
		}
	}
}

// Snoozing from a row's popover: the pointer leaves the row for the
// popover, and the choice still lands.
func TestSnooze(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	th := a.projects[0].threads[1]
	r, _ := tt.Find(th.title)
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	if err := tt.Click("Snooze"); err != nil {
		t.Fatal(err)
	}
	if a.snoozeFor != th.id || !tt.HasText("sleep on it") {
		t.Fatalf("snooze popover not open: %q", a.snoozeFor)
	}
	// Off the row, onto the popover's choice.
	tm, _ := tt.Find("Tomorrow")
	tt.Move(tm.X+tm.W/2, tm.Y+tm.H/2)
	if err := tt.Click("Tomorrow"); err != nil {
		t.Fatal(err)
	}
	if th.snoozed != "Tomorrow" {
		t.Fatalf("snoozed %q, want Tomorrow", th.snoozed)
	}
	tt.Advance(2 * time.Second)
	if a.shownIn(th) != catSnoozed {
		t.Errorf("snoozed thread shown in section %d", a.shownIn(th))
	}
}

// Snoozing and settling play their jokes before the row moves, and none
// after; sending back plays after it lands.
func TestGagTiming(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	th := a.projects[0].threads[1]

	th.snoozed = "Tomorrow"
	a.rowFx[th.id] = newGag(gagSnooze, tt.Now(), catInbox)
	tt.Advance(snoozeLife / 2)
	if a.shownIn(th) != catInbox {
		t.Error("snoozed row moved before its joke ended")
	}
	tt.Advance(snoozeLife)
	if a.shownIn(th) != catSnoozed {
		t.Error("snoozed row did not move once its joke ended")
	}
	if _, ok := a.rowFx[th.id]; ok {
		t.Error("snooze's joke still playing after the row moved")
	}

	th.snoozed = ""
	a.rowFx[th.id] = newGag(gagUndo, tt.Now(), catSnoozed)
	tt.Frame()
	if a.shownIn(th) != catInbox {
		t.Error("sent back row waited to move")
	}
	if _, ok := a.rowFx[th.id]; !ok {
		t.Error("jk didn't play after the row landed")
	}
}

// The diff panel shows each file's real diff, unified by default, and
// accepting a change counts it out.
func TestDiffPanel(t *testing.T) {
	a := newApp()
	a.showDiff = true
	tt := ui.NewTester(a.view, 1320, 860)
	d := a.diffFor("apps/web/src/components/Sidebar.tsx")
	if d.Layout != editor.DiffUnified || !tt.HasText("Unified") {
		t.Fatalf("layout %v", d.Layout)
	}
	n := len(d.Hunks())
	if n == 0 {
		t.Fatal("no changes in the mock diff")
	}
	d.Accept(0)
	tt.Frame()
	if len(d.Hunks()) != n-1 {
		t.Errorf("%d changes after accepting one of %d", len(d.Hunks()), n)
	}
	if !d.Wrap {
		t.Error("long lines don't wrap by default")
	}
	// Split needs the room: at 1320 the panel would be 608 wide.
	if err := tt.Click("Split"); err != nil {
		t.Fatal(err)
	}
	if a.diffLayout != editor.DiffUnified {
		t.Error("Split switched the layout without the room for it")
	}
	tt.SetSize(1600, 900)
	tt.Frame()
	if err := tt.Click("Split"); err != nil {
		t.Fatal(err)
	}
	if a.diffLayout != editor.DiffSplit {
		t.Error("Split did not switch the layout")
	}
	tt.SetSize(1320, 860)
	tt.Frame()
	if a.diffLayout != editor.DiffUnified {
		t.Error("the layout stayed split as the room went")
	}
}

// A file row's actions mark it reviewed, revert it and copy its path;
// the hunk bar steps and keeps from the keyboard.
func TestDiffFileActions(t *testing.T) {
	a := newApp()
	a.showDiff = true
	tt := ui.NewTester(a.view, 1320, 860)
	path := "apps/web/src/components/ChevronIcon.tsx"
	r, ok := tt.Find("Diff of " + path)
	if !ok {
		t.Fatal("no file row")
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(time.Second)
	if err := tt.Click("Mark reviewed"); err != nil {
		t.Fatal(err)
	}
	if !a.reviewed[path] {
		t.Error("file not marked reviewed")
	}
	if err := tt.Click("Copy path"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != path {
		t.Errorf("clipboard %q", tt.Clipboard())
	}
	if err := tt.Click("Revert file"); err != nil {
		t.Fatal(err)
	}
	if n := len(a.diffFor(path).Hunks()); n != 0 {
		t.Errorf("%d changes left after reverting the file", n)
	}

	d := a.diffFor("apps/web/src/components/Sidebar.tsx")
	n := len(d.Hunks())
	if n < 2 {
		t.Fatalf("%d changes in the mock diff, want at least 2", n)
	}
	tt.Key(ui.Cmd, ui.KeyBracketRight)
	if i, _ := d.CurrentHunk(); i != 1 {
		t.Errorf("⌘] went to change %d, want 1", i)
	}
	tt.Key(ui.Cmd, ui.KeyBracketLeft)
	if i, _ := d.CurrentHunk(); i != 0 {
		t.Errorf("⌘[ went to change %d, want 0", i)
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyEnter)
	if len(d.Hunks()) != n-1 {
		t.Errorf("%d changes after ⇧⌘↩, want %d", len(d.Hunks()), n-1)
	}
}

// The timeline starts at its end; scrolled up, a pill takes it back.
func TestTimelineFollowsEnd(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	_, th := a.current()
	l := a.listFor(th)
	tt.Advance(time.Second)
	if !l.AtEnd() || tt.HasText("Jump to latest") {
		t.Fatal("timeline should open at its end, without the pill")
	}
	l.ScrollTo(0, ui.Start)
	tt.Advance(time.Second)
	if l.AtEnd() || !tt.HasText("Jump to latest") {
		t.Fatal("scrolled to the top, the pill should show")
	}
	if err := tt.Click("Jump to latest"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(2 * time.Second)
	if !l.AtEnd() {
		t.Error("Jump to latest did not scroll to the end")
	}
}

// Files dropped or picked become chips over the draft, and go with the
// message sent; a long paste becomes one too.
func TestAttachments(t *testing.T) {
	a := newApp()
	a.selected = "t3"
	tt := ui.NewTester(a.view, 1320, 860)
	a.attachFiles([]string{"go.mod", "resources/icon.png"})
	tt.Frame()
	if !tt.HasText("go.mod") || !tt.HasText("icon.png") {
		t.Fatalf("chips missing: %q", tt.Texts())
	}
	if a.attachments[1].thumb == nil {
		t.Error("no thumbnail for an image")
	}
	if !a.onPaste(strings.Repeat("x\n", longPaste)) || len(a.attachments) != 3 {
		t.Error("a long paste should become an attachment")
	}
	if a.onPaste("short") {
		t.Error("a short paste should go in the box")
	}
	if err := tt.Click("Remove go.mod"); err != nil {
		t.Fatal(err)
	}
	if len(a.attachments) != 2 {
		t.Fatalf("%d attachments after removing one", len(a.attachments))
	}
	_, th := a.current()
	a.send(th)
	if len(a.attachments) != 0 || len(th.entries[len(th.entries)-3].attachments) != 2 {
		t.Error("sending should take the attachments with the message")
	}
}

func TestFirstLine(t *testing.T) {
	for in, want := range map[string]string{
		"Done. The **collapsed** projects are kept.\n\n- more": "Done. The collapsed projects are kept.",
		"## Comparing\n\nbody":                                 "Comparing",
		"> **Tip:** start small":                               "Tip: start small",
	} {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// The palette's results scroll to the choice the arrows move to, though
// the list builds only the rows in view.
func TestPaletteScrollsToChoice(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	a.openCommandPalette()
	tt.Frame()
	cmds := a.commands()
	last := cmds[len(cmds)-1].label
	tt.Key(0, ui.KeyUp) // wraps to the last
	tt.Advance(time.Second)
	if a.cmdkIndex != len(cmds)-1 {
		t.Fatalf("choice %d, want the last, %d", a.cmdkIndex, len(cmds)-1)
	}
	r, ok := tt.Find(last)
	if !ok || r.Y > 96+60+380 {
		t.Errorf("the last result %q is not in view (%v, %v)", last, r, ok)
	}
	tt.Key(0, ui.KeyEnter)
	if a.cmdkOpen {
		t.Error("Enter should open the choice and close the palette")
	}
}

// @ suggests files and / commands; the keys move and take the choice.
func TestAutocomplete(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	a.draft = "look at @Side"
	tt.Frame()
	if len(a.suggestions) == 0 || !strings.Contains(a.suggestions[0].insert, "Sidebar.tsx") {
		t.Fatalf("suggestions for @Side: %+v", a.suggestions)
	}
	tt.Key(0, ui.KeyEnter)
	if a.draft != "look at @apps/web/src/components/Sidebar.tsx " {
		t.Errorf("draft %q after taking the suggestion", a.draft)
	}
	a.draft = "/re"
	tt.Frame()
	if len(a.suggestions) != 1 || a.suggestions[0].label != "/review" {
		t.Fatalf("suggestions for /re: %+v", a.suggestions)
	}
	tt.Key(0, ui.KeyEscape)
	if len(a.suggestions) != 0 {
		t.Error("Escape should dismiss the suggestions")
	}
	a.draft = "not a /command"
	tt.Frame()
	if len(a.suggestions) != 0 {
		t.Error("/ mid-line should suggest nothing")
	}
}

// Quick ask starts a thread in the chosen project with what was typed,
// and opens it.
func TestQuickAsk(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	a.quick.project, a.quick.query = 2, "Why does the SIP stack drop calls?"
	a.startQuickThread()
	tt.Frame()
	p, th := a.current()
	if p.name != "agent-phone" || th.title != "Why does the SIP stack drop calls?" {
		t.Fatalf("opened %q in %s", th.title, p.name)
	}
	if len(th.entries) == 0 || th.entries[0].text != "Why does the SIP stack drop calls?" {
		t.Error("the question wasn't sent")
	}
}

// The preview pane and the diff share the side of the chat: one at a time.
func TestPreviewToggle(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	if err := tt.Click("Toggle preview  ⇧⌘P"); err != nil {
		t.Fatal(err)
	}
	if !a.showPreview || !tt.HasText("The preview shows here, in the app's window.") {
		t.Fatal("preview pane not shown")
	}
	if err := tt.Click("Toggle diff  ⌘D"); err != nil {
		t.Fatal(err)
	}
	if !a.showDiff || a.showPreview {
		t.Errorf("diff %v, preview %v: the diff should take the preview's place", a.showDiff, a.showPreview)
	}
}

// A tick for each message sent; clicking one scrolls to it.
func TestTurnMarkers(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	tt.Advance(time.Second)
	_, th := a.current()
	first := "Go to: " + preview(th.entries[0])
	if err := tt.Click(first); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if f, _ := a.listFor(th).Visible(); f > 0 {
		t.Errorf("first row in view %d after clicking the first marker, want 0", f)
	}
	if !tt.HasText(th.entries[0].text) {
		t.Error("the first message isn't in view")
	}
}

// The sidebar shows the projects chosen, several at once; choosing every
// one is showing all.
func TestProjectFilter(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	shown := func() map[string]bool {
		m := map[string]bool{}
		for _, cat := range []category{catInbox, catWorking, catSnoozed, catSettled} {
			for _, it := range a.itemsIn(cat) {
				m[it.p.name] = true
			}
		}
		return m
	}
	for _, name := range []string{"t3code", "agent-phone"} {
		if err := tt.Click(name); err != nil {
			t.Fatal(err)
		}
	}
	if s := shown(); !s["t3code"] || !s["agent-phone"] || s["mygo"] {
		t.Errorf("filtered to t3code and agent-phone, the sidebar shows %v", s)
	}
	if err := tt.Click("mygo"); err != nil {
		t.Fatal(err)
	}
	if len(a.projectFilter) != 0 {
		t.Errorf("every project chosen should be no filter, got %v", a.projectFilter)
	}
	if err := tt.Click("agent-phone"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("All"); err != nil {
		t.Fatal(err)
	}
	if len(a.projectFilter) != 0 {
		t.Error("All should clear the filter")
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"voip.othos.co.za":       "https://voip.othos.co.za",
		"localhost:5173":         "http://localhost:5173",
		"127.0.0.1:3000/app":     "http://127.0.0.1:3000/app",
		"192.168.1.20:8080":      "http://192.168.1.20:8080",
		"http://example.com":     "http://example.com",
		" example.com/path?q=1 ": "https://example.com/path?q=1",
	} {
		if got := normalizeURL(in); got != want {
			t.Errorf("normalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// Scrolled up from the end, the composer is compact, its controls folded
// away; a draft, a click or the end of the thread brings it back.
func TestCompactComposer(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	_, th := a.current()
	l := a.listFor(th)
	tt.Advance(time.Second)
	if !tt.HasText("Approve for me") {
		t.Fatal("the composer should be full size at the end")
	}
	l.ScrollTo(0, ui.Start)
	tt.Advance(time.Second)
	if tt.HasText("Approve for me") {
		t.Error("scrolled up, the composer should fold its controls away")
	}
	// Changes from outside the view, as typing's, which a frame shows.
	a.draft = "a follow-up"
	tt.Frame()
	tt.Advance(time.Second)
	if !tt.HasText("Approve for me") {
		t.Error("with a draft, the composer should be full size")
	}
	a.draft = ""
	tt.Frame()
	tt.Advance(time.Second)
	l.ScrollToEnd()
	tt.Frame()
	tt.Advance(2 * time.Second)
	if !tt.HasText("Approve for me") {
		t.Error("back at the end, the composer should be full size")
	}
}

// Moving the effort changes the flow's speed smoothly: the pulses go on
// from where they were, never jumping, however far the level moves.
func TestFlowKeepsGoing(t *testing.T) {
	var f flowState
	at := time.Unix(1000, 0)
	f.step(at, 0)
	prev := f.dist
	for i := range 120 {
		at = at.Add(16 * time.Millisecond)
		level := 0
		if i >= 30 {
			level = 3 // moved to Max mid-flow
		}
		f.step(at, level)
		moved := f.dist - prev
		if moved < 0 || moved > flowSpeed(3)*0.016*1.01 {
			t.Fatalf("frame %d: the pulses moved %.2f DIPs, not a frame's way at up to Max's speed", i, moved)
		}
		prev = f.dist
	}
	if f.speed < 200 || f.level < 2.9 {
		t.Errorf("speed %.0f, level %.2f: should have eased to Max's", f.speed, f.level)
	}
}

// An approval is answered from its card or the keyboard; allowed, the
// agent goes on, denied, it replies and stops. With a draft, ⌘⏎ sends.
func TestApproval(t *testing.T) {
	a := newApp()
	a.selected = "t2"
	tt := ui.NewTester(a.view, 1320, 860)
	_, th := a.current()
	ap := th.pending()
	if ap == nil || !tt.HasText("Approval needed") || !tt.HasText("Paused for approval") {
		t.Fatalf("no pending approval shown; texts %q", tt.Texts())
	}
	if err := tt.Click("Allow once"); err != nil {
		t.Fatal(err)
	}
	if ap.decision != allowed || th.status != statusCompleted || !tt.HasText("Allowed") {
		t.Fatalf("decision %d, status %d", ap.decision, th.status)
	}
	if last := th.entries[len(th.entries)-1]; last.kind != entryAssistant || last.text != approved.reply {
		t.Errorf("the agent didn't go on: last entry %+v", last)
	}

	a = newApp()
	a.selected = "t2"
	tt = ui.NewTester(a.view, 1320, 860)
	_, th = a.current()
	ap = th.pending()
	a.draft = "use the other runner"
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyEnter)
	if ap.decision != undecided {
		t.Fatalf("⌘⏎ with a draft decided %d, want it to send", ap.decision)
	}
	if th.entries[len(th.entries)-1].text != scripts[(replyCount-1)%len(scripts)].reply {
		t.Errorf("⌘⏎ with a draft didn't send")
	}

	a = newApp()
	a.selected = "t2"
	tt = ui.NewTester(a.view, 1320, 860)
	_, th = a.current()
	ap = th.pending()
	tt.Key(ui.Cmd|ui.Shift, ui.KeyD)
	if ap.decision != denied || !tt.HasText("Denied") || th.status != statusCompleted {
		t.Fatalf("⇧⌘D decided %d, status %d", ap.decision, th.status)
	}
	if last := th.entries[len(th.entries)-1]; last.text != refused {
		t.Errorf("no reply to the denial: %+v", last)
	}
}

// Pointing at a message shows its actions, which take no room. Copy
// copies it; Edit & resend puts it back in the composer, the thread cut
// to before it; Retry has the agent answer again; Fork here opens a
// thread of the messages so far.
func TestMessageActions(t *testing.T) {
	a := newApp()
	a.selected = "t3"
	tt := ui.NewTester(a.view, 1320, 860)
	_, th := a.current()
	replyCount = 0 // the reply without a code block, which has a Copy of its own
	// Longer than a title, so the sidebar doesn't show the same text.
	const msg = "Fix the flicker when the window is resized on Windows 11, please."
	a.draft = msg
	tt.Frame()
	if err := tt.Click("Send"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if tt.HasText("Edit & resend") {
		t.Error("actions show without the pointer")
	}
	r, _ := tt.Find(msg)
	below, _ := tt.Find("Worked for 3s")
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(time.Second)
	if after, _ := tt.Find("Worked for 3s"); after.Y != below.Y {
		t.Errorf("the work log moved from y %v to %v as the message was pointed at", below.Y, after.Y)
	}
	if err := tt.Click("Copy"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != msg {
		t.Errorf("clipboard %q", tt.Clipboard())
	}
	if err := tt.Click("Edit & resend"); err != nil {
		t.Fatal(err)
	}
	if a.draft != msg || len(th.entries) != 0 {
		t.Fatalf("draft %q, entries %d", a.draft, len(th.entries))
	}

	replyCount = 0
	a.draft = msg
	tt.Frame()
	if err := tt.Click("Send"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	hoverReply := func() {
		r, ok := tt.Find("Done. The collapsed projects are kept in localStorage under t3code:sidebar:collapsed, as a list of project ids.")
		if !ok {
			t.Fatalf("no reply; texts %q", tt.Texts())
		}
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Advance(time.Second)
	}
	hoverReply()
	replyCount = 0
	if err := tt.Click("Retry"); err != nil {
		t.Fatal(err)
	}
	if len(th.entries) != 3 || th.entries[0].kind != entryUser || th.status != statusCompleted {
		t.Fatalf("after retry: %d entries, status %d", len(th.entries), th.status)
	}
	hoverReply()
	if err := tt.Click("Fork here"); err != nil {
		t.Fatal(err)
	}
	_, f := a.current()
	if f == th || len(f.entries) != 3 || f.title != th.title+" (fork)" {
		t.Fatalf("fork: %q with %d entries", f.title, len(f.entries))
	}
}

// A command's step shows how it went, a failed one what it printed, and
// an edit's step opens the file in the diff.
func TestWorkLogSteps(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	if !tt.HasText("✗ 1") || !tt.HasText("✓ 0") || !tt.HasText("Test Files  1 failed (1)") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if err := tt.Click("Editing"); err != nil {
		t.Fatal(err)
	}
	if !a.showDiff || a.diffFile != 0 {
		t.Errorf("diff %v at file %d after clicking the edit step", a.showDiff, a.diffFile)
	}
}

// A thread row's context menu renames the thread in place, snoozes and
// settles it, copies its branch, archives it, and deletes it, asking
// first while the setting says to.
func TestThreadMenu(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	t4 := a.projects[0].threads[3]
	if err := tt.RightClick(t4.title); err != nil {
		t.Fatal(err)
	}
	if m := tt.Menu(); len(m) == 0 || m[0] != "Rename" {
		t.Fatalf("menu %q", m)
	}
	if err := tt.ChooseMenuItem("Copy branch name"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "main" {
		t.Errorf("clipboard %q", tt.Clipboard())
	}
	if err := tt.RightClick(t4.title); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Rename"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("Websocket loop")
	tt.Key(0, ui.KeyEnter)
	if t4.title != "Websocket loop" || a.renaming != "" {
		t.Fatalf("title %q, renaming %q", t4.title, a.renaming)
	}
	if err := tt.RightClick(t4.title); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Snooze", "Tomorrow"); err != nil {
		t.Fatal(err)
	}
	if t4.snoozed != "Tomorrow" {
		t.Errorf("snoozed %q", t4.snoozed)
	}
	// The toast naming the thread goes; Snoozed is closed, and its thread
	// shows once it's opened.
	tt.Advance(10 * time.Second)
	if err := tt.Click("Snoozed"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second) // its rows unfold
	if err := tt.RightClick(t4.title); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Archive"); err != nil {
		t.Fatal(err)
	}
	if indexOf(a.projects[0].threads, t4) >= 0 || indexOf(a.archived, t4) != 0 {
		t.Error("archived thread not moved to the archive")
	}

	// Deleting asks, and deleting the open thread opens another. The
	// open thread is in Working, closed by default.
	t1 := a.projects[0].threads[0]
	if err := tt.Click("Working"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second) // its rows unfold
	if err := tt.RightClick(t1.title); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Delete “" + t1.title + "”?") {
		t.Fatalf("no alert; texts %q", tt.Texts())
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatal(err)
	}
	if indexOf(a.projects[0].threads, t1) >= 0 || a.selected == t1.id {
		t.Errorf("deleted thread still there or selected")
	}
	if _, cur := a.current(); cur == t1 {
		t.Error("the deleted thread is still current")
	}
	a.settings.confirmDelete = false
	t5 := a.projects[0].threads[len(a.projects[0].threads)-1]
	if err := tt.Click("Settled"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second) // its rows unfold
	if err := tt.RightClick(t5.title); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatal(err)
	}
	if indexOf(a.projects[0].threads, t5) >= 0 || tt.HasText("Delete “"+t5.title+"”?") {
		t.Error("deleting without the setting should not ask")
	}
}

// The palette lists threads by recency, opens one with its diff on ⌘↩,
// keeps to actions after >, and starts a thread in a chosen project.
func TestPaletteCommands(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	a.openCommandPalette()
	tt.Frame()
	var threads []string
	for _, cmd := range a.commands() {
		if cmd.t != nil {
			threads = append(threads, cmd.t.age)
		}
	}
	if len(threads) < 3 || threads[0] != "now" || threads[1] != "12m" || threads[2] != "2h" {
		t.Errorf("threads by age %q", threads)
	}
	tt.Type("websocket")
	tt.Key(ui.Cmd, ui.KeyEnter)
	if a.cmdkOpen || a.selected != "t4" || !a.showDiff {
		t.Errorf("⌘↩: open %v, selected %q, diff %v", a.cmdkOpen, a.selected, a.showDiff)
	}

	a.openCommandPalette()
	tt.Frame()
	tt.Type(">")
	for _, cmd := range a.commands() {
		if cmd.section != "Actions" {
			t.Fatalf("%q listed after >", cmd.label)
		}
	}
	tt.Type("new thread in mygo")
	tt.Key(0, ui.KeyEnter)
	p, th := a.current()
	if p.name != "mygo" || th.title != "New thread" {
		t.Errorf("new thread in %q titled %q", p.name, th.title)
	}
}
