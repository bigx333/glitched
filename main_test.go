package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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
func TestShortcutHints(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	has := func() bool {
		for _, s := range tt.Texts() {
			if s == "⌘" {
				return true
			}
		}
		return false
	}
	if has() {
		t.Error("keycaps show without ⌘ held")
	}
	tt.HoldModifiers(ui.Cmd)
	if !has() {
		t.Error("keycaps hidden while ⌘ is held")
	}
	tt.HoldModifiers(0)
	if has() {
		t.Error("keycaps still show after ⌘ is let go")
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
	if !tt.HasText("SIP re-INVITE handling") {
		t.Error("filter to agent-phone hides its thread")
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
