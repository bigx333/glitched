package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// With an empty state directory, the app embeds this machine's engine and
// shows no threads, ready for the first.
func TestStartsEmptyOnALocalEngine(t *testing.T) {
	h := start(t, tempDir(t))
	if envs := h.a.backend.Envs(); len(envs) != 1 || envs[0].Kind != client.Local || len(envs[0].Threads) != 0 {
		t.Fatalf("envs %+v, want this machine's alone, with no threads", envs)
	}
	h.tt.Advance(time.Second)
	for _, s := range []string{"What should we build?", "Inbox zero. Nothing needs you.", "New thread"} {
		if !h.tt.HasText(s) {
			t.Errorf("no %q in %q", s, h.tt.Texts())
		}
	}
}

// The sidebar lists the engine's threads; a click shows one.
func TestView(t *testing.T) {
	dir := tempDir(t)
	inbox(t, dir)
	h := start(t, dir)
	fix := h.thread("Fix terminal resize flicker on Windows")
	h.tt.Advance(time.Second)
	h.click(fix.View.Title)
	if h.a.selected != fix.id() {
		t.Fatalf("selected %s, want %s", h.a.selected, fix.id())
	}
	h.until("its transcript", func() bool { return h.tt.HasText("Why does it flicker?") && h.tt.HasText("Read main.go") })
	if h.a.backend.Unread(fix.Thread) {
		t.Error("the thread shown is still unread")
	}
	h.click("Toggle diff  ⌘D")
	if !h.a.showDiff || !h.tt.HasText("No changes in this thread yet.") {
		t.Errorf("diff panel not shown: %q", h.tt.Texts())
	}
}

// Sending in the draft creates the thread on this machine, in its
// workspace, and its turn streams in until it asks for an approval.
func TestSendStartsAThread(t *testing.T) {
	h := start(t, tempDir(t))
	h.a.draft = "Fix the flicker"
	h.tt.Frame()
	h.click("Send")
	th := h.a.current()
	if th.id().IsZero() || th.env != h.local() || th.View.Title != "Fix the flicker" || th.View.Workspace != homeDir() {
		t.Fatalf("after Send the chat shows %+v on %v, want the new thread on this machine", th.View, th.env)
	}
	if h.a.draft != "" || !h.tt.HasText("Fix the flicker") {
		t.Errorf("draft %q, window %q: the message should have left the composer for the timeline", h.a.draft, h.tt.Texts())
	}
	h.until("the turn to ask", func() bool {
		return h.tt.HasText("Looking at the code.") && h.tt.HasText("May I edit it?") && h.tt.HasText("Read main.go") && h.a.statusOf(h.a.current()) == statusApproval
	})
}

// The turn's approval shows as a card; Allow lets the turn finish.
func TestApprovalCardAllows(t *testing.T) {
	h := start(t, tempDir(t))
	h.a.draft = "Fix the flicker"
	h.tt.Frame()
	h.click("Send")
	h.until("the approval card", func() bool { return h.tt.HasText("Allow edit main.go?") })
	h.tt.Advance(time.Second) // risen in
	if _, ok := h.tt.Find("Deny"); !ok {
		t.Fatalf("no Deny on the card: %q", h.tt.Texts())
	}
	h.click("Allow")
	h.until("the turn to finish", func() bool {
		v := h.a.current().View
		return v.LastTurn != nil && !v.Working() && h.tt.HasText("Done.") && h.tt.HasText("Allowed") && h.tt.HasText("Edit main.go")
	})
	if o := h.a.current().View.LastTurn.Outcome; o != api.OutcomeCompleted {
		t.Errorf("the turn ended %v, want completed", o)
	}
	if _, ok := h.tt.Find("Allow"); ok {
		t.Error("the answered card still offers Allow")
	}
}

// Stop interrupts the turn, which ends as interrupted.
func TestStopInterrupts(t *testing.T) {
	h := start(t, tempDir(t))
	h.a.draft = "Fix the flicker"
	h.tt.Frame()
	h.click("Send")
	h.until("the turn to wait on its approval", func() bool { return h.a.current().View.Working() && h.tt.HasText("May I edit it?") })
	h.click("Stop")
	h.until("the turn to end", func() bool {
		v := h.a.current().View
		return !v.Working() && v.LastTurn != nil
	})
	if o := h.a.current().View.LastTurn.Outcome; o != api.OutcomeInterrupted {
		t.Errorf("the turn ended %v, want interrupted", o)
	}
	if _, ok := h.tt.Find("Send"); !ok {
		t.Error("the stop button didn't go back to Send")
	}
}

// A server added in Settings shows its threads beside this machine's,
// each with its machine; one that refuses the token says so.
func TestServers(t *testing.T) {
	rdir := tempDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	d := engine(t, daemon.Config{Dir: rdir, Listen: []string{"127.0.0.1:0"}}, fake.New(fake.Turn(false)))
	seed(t, d, "/srv/api", "Rotate the API keys", "Which keys are old?")
	addr := d.Addrs()[0].String()

	h := start(t, tempDir(t))
	h.click("Settings")
	h.click("Servers")
	h.a.server = client.Server{Label: "devbox", Addr: addr, Token: token}
	h.tt.Frame()
	h.click("Add server")
	h.until("devbox online", func() bool { return h.tt.HasText("Online") })
	h.a.server = client.Server{Label: "stranger", Addr: addr, Token: "wrong"}
	h.tt.Frame()
	h.click("Add server")
	h.until("the stranger refused", func() bool { return h.tt.HasText("Refused") })

	h.click("Back to app")
	th := h.thread("Rotate the API keys")
	h.tt.Advance(time.Second)
	if th.local() || th.env.Name != "devbox" {
		t.Fatalf("the server's thread is on %v, want devbox", th.env)
	}
	r, ok := h.tt.Find("devbox")
	if !ok || r.X > h.a.sidebarWidth {
		t.Errorf("no devbox badge in the sidebar: %q", h.tt.Texts())
	}

	// A new thread can start on the server, in its home directory.
	h.click("New thread")
	h.click("Workspace")
	if err := h.tt.ChooseMenuItem("~ on devbox"); err != nil {
		t.Fatalf("%v; the menu has %q", err, h.tt.Menu())
	}
	h.a.draft = "Check the certificates"
	h.tt.Frame()
	h.click("Send")
	h.until("the thread on devbox", func() bool {
		th := h.a.current()
		return !th.id().IsZero() && th.env.Name == "devbox" && th.View.Workspace == "~" && th.View.Revision > 0
	})

	h.click(th.View.Title)
	h.until("its transcript, from the server", func() bool { return h.tt.HasText("Which keys are old?") && h.tt.HasText("Read main.go") })
	for _, s := range []string{"Open in", "Commit & push", "Toggle terminal  ⌘J", "Toggle diff  ⌘D", "Attach files"} {
		if _, ok := h.tt.Find(s); ok {
			t.Errorf("a server's thread offers %q, which works on this machine's files", s)
		}
	}
	h.a.draft = "Rotate them"
	h.tt.Frame()
	h.click("Send")
	h.until("the server to run the turn", func() bool {
		snap, err := d.Endpoint().Read(context.Background(), api.Query{Thread: th.id()})
		return err == nil && snap.Threads[0].Queued == 0 && snap.Threads[0].Run == api.RunIdle && h.tt.HasText("Rotate them") &&
			strings.Count(strings.Join(h.tt.Texts(), "\n"), "Done.") == 2
	})

	h.click("Settings")
	h.click("Servers")
	h.click("Remove") // devbox's, the first
	h.tt.Advance(time.Second)
	if s := h.a.backend.Servers(); len(s) != 1 || s[0].Label != "stranger" || h.tt.HasText("devbox") {
		t.Errorf("after removing devbox the servers are %+v and the page shows %q", s, h.tt.Texts())
	}
	h.click("Back to app")
	if th := h.a.current(); !th.id().IsZero() || h.tt.HasText("Rotate the API keys") {
		t.Errorf("devbox's thread still shows after removing it: %q", h.tt.Texts())
	}
}

func TestSettingsPages(t *testing.T) {
	a := offline(t)
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
	dir := tempDir(t)
	inbox(t, dir)
	h := start(t, dir)
	loop := h.thread("Investigate websocket reconnect loop")
	h.click("Search")
	h.tt.Type("websocket")
	if !h.tt.HasText(loop.View.Title) {
		t.Fatalf("texts %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyEnter)
	if h.a.cmdkOpen || h.a.selected != loop.id() {
		t.Errorf("open %v, selected %s", h.a.cmdkOpen, h.a.selected)
	}
}

func TestModelPicker(t *testing.T) {
	a := offline(t)
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
	a := offline(t)
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
	a := offline(t)
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
	a := offline(t)
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
	a := offline(t)
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
		a := offline(t)
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
	dir := tempDir(t)
	inbox(t, dir)
	h := start(t, dir)
	fix := h.thread("Fix terminal resize flicker on Windows")
	h.tt.Advance(time.Second)
	if c := h.a.categoryOf(fix); c != catInbox {
		t.Fatalf("a finished thread in section %d, want the inbox", c)
	}
	// Settle it from its row's actions.
	h.hover(fix.View.Title)
	h.click("Settle")
	if c := h.a.categoryOf(fix); c != catSettled {
		t.Errorf("settled thread in section %d", c)
	}
	if h.a.backend.Unread(fix.Thread) {
		t.Error("a settled thread is still unread")
	}
	// It stays in the inbox a moment, sighing in its burst of confetti,
	// then folds away into Settled.
	if h.a.shownIn(fix) != catInbox {
		t.Error("settled thread left the inbox before its joke played")
	}
	h.until("the engine to settle it", func() bool { return fix.View.Disposition.State == api.Settled && len(fix.Pending) == 0 })
	h.tt.Advance(1500 * time.Millisecond)
	// Settled is closed by default; opening it shows the thread. (The
	// toast shows its title too, at the window's other side.)
	inSidebar := func(title string) bool {
		r, ok := h.tt.Find(title)
		return ok && r.X < h.a.sidebarWidth
	}
	if inSidebar(fix.View.Title) {
		t.Error("closed Settled section shows its threads")
	}
	h.click("Settled")
	if !inSidebar(fix.View.Title) {
		t.Error("opened Settled section hides the settled thread")
	}
	// Sending in a settled thread brings it through Working back to the
	// inbox.
	h.a.select_(fix)
	h.a.draft = "and again"
	h.a.send(fix)
	h.until("its turn to end in the inbox", func() bool {
		v := fix.View
		return v.Disposition.State == api.Open && !v.Working() && v.Queued == 0 && h.a.categoryOf(fix) == catInbox &&
			h.tt.HasText("and again") && strings.Count(strings.Join(h.tt.Texts(), "\n"), "Looking at the code.") == 2
	})
	// The strip filters to a project.
	h.click("agent-phone")
	for _, cat := range []category{catInbox, catWorking, catSnoozed, catSettled} {
		for _, it := range h.a.itemsIn(cat) {
			if it.p.name() != "agent-phone" {
				t.Errorf("filter to agent-phone shows %q of %s", it.t.View.Title, it.p.name())
			}
		}
	}
	if items := h.a.itemsIn(catInbox); len(items) != 1 || items[0].t.View.Title != "SIP re-INVITE handling" {
		t.Errorf("filter to agent-phone: inbox %v", items)
	}
}

// Pointing at a thread swaps its time for its actions without moving the
// rows around it.
func TestInboxHoverKeepsLayout(t *testing.T) {
	dir := tempDir(t)
	inbox(t, dir)
	h := start(t, dir)
	h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	items := h.a.itemsIn(catInbox)
	first, next := items[0].t.View.Title, items[1].t.View.Title
	before, _ := h.tt.Find(next)
	h.hover(first)
	h.tt.Advance(time.Second)
	if after, _ := h.tt.Find(next); after.Y != before.Y {
		t.Errorf("the next row moved from y %v to %v as the one above was pointed at", before.Y, after.Y)
	}
}

// Each of the inbox's jokes plays through without a hitch.
func TestInboxGags(t *testing.T) {
	dir := tempDir(t)
	inbox(t, dir)
	h := start(t, dir)
	th := h.thread("SIP re-INVITE handling")
	for _, kind := range []gagKind{gagSnooze, gagSettle, gagUndo} {
		h.a.rowFx[th.id()] = rowGagFx{kind: kind, at: h.tt.Now(), from: catInbox, hold: 500 * time.Millisecond, life: time.Second}
		for range 40 {
			h.tt.Advance(80 * time.Millisecond)
		}
		if _, ok := h.a.rowFx[th.id()]; ok {
			t.Errorf("joke %d still playing after %v", kind, 40*80*time.Millisecond)
		}
	}
}

// Snoozing from a row's popover: the pointer leaves the row for the
// popover, and the choice still lands; Back to inbox brings it back.
func TestSnooze(t *testing.T) {
	dir := tempDir(t)
	inbox(t, dir)
	h := start(t, dir)
	th := h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	h.hover(th.View.Title)
	h.click("Snooze")
	if h.a.snoozeFor != th.id() || !h.tt.HasText("sleep on it") {
		t.Fatalf("snooze popover not open: %s", h.a.snoozeFor)
	}
	// Off the row, onto the popover's choice.
	h.hover("Tomorrow")
	h.click("Tomorrow")
	want := morning(h.tt.Now(), 1)
	h.until("the engine to snooze it", func() bool {
		d := th.View.Disposition
		return d.State == api.Snoozed && d.Until.Equal(want) && len(th.Pending) == 0
	})
	h.tt.Advance(2 * time.Second)
	if h.a.shownIn(th) != catSnoozed {
		t.Errorf("snoozed thread shown in section %d", h.a.shownIn(th))
	}
	h.click("Snoozed")
	h.tt.Advance(time.Second)
	h.hover(th.View.Title)
	h.click("Back to inbox")
	h.until("the engine to bring it back", func() bool { return th.View.Disposition.State == api.Open && len(th.Pending) == 0 })
	if h.a.categoryOf(th) != catInbox {
		t.Errorf("thread back in section %d, want the inbox", h.a.categoryOf(th))
	}
}

// Snoozing and settling play their jokes before the row moves, and none
// after; sending back plays after it lands.
func TestGagTiming(t *testing.T) {
	dir := tempDir(t)
	inbox(t, dir)
	h := start(t, dir)
	th := h.thread("SIP re-INVITE handling")
	a, tt := h.a, h.tt

	a.backend.SetDisposition(th.env, th.id(), api.Disposition{State: api.Snoozed, Until: morning(tt.Now(), 1)})
	a.rowFx[th.id()] = newGag(gagSnooze, tt.Now(), catInbox)
	tt.Advance(snoozeLife / 2)
	if a.shownIn(th) != catInbox {
		t.Error("snoozed row moved before its joke ended")
	}
	tt.Advance(snoozeLife)
	if a.shownIn(th) != catSnoozed {
		t.Error("snoozed row did not move once its joke ended")
	}
	if _, ok := a.rowFx[th.id()]; ok {
		t.Error("snooze's joke still playing after the row moved")
	}

	a.backend.SetDisposition(th.env, th.id(), api.Disposition{})
	a.rowFx[th.id()] = newGag(gagUndo, tt.Now(), catSnoozed)
	tt.Frame()
	if a.shownIn(th) != catInbox {
		t.Error("sent back row waited to move")
	}
	if _, ok := a.rowFx[th.id()]; !ok {
		t.Error("jk didn't play after the row landed")
	}
}

// The diff panel shows each file's real diff, unified by default, and
// accepting a change counts it out.
func TestDiffPanel(t *testing.T) {
	a := offline(t)
	a.showDiff = true
	tt := ui.NewTester(a.view, 1320, 860)
	d := a.diffFor("apps/web/src/components/Sidebar.tsx")
	if a.diffLayout != editor.DiffUnified || !tt.HasText("Unified") {
		t.Fatalf("layout %v", a.diffLayout)
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
	if err := tt.Click("Split"); err != nil {
		t.Fatal(err)
	}
	if a.diffLayout != editor.DiffSplit {
		t.Error("Split did not switch the layout")
	}
}

// long is a thread of several turns, shown, its transcript in.
func long(t *testing.T) (*harness, thread) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "Sort sidebar threads by recent activity",
		"Where does the sidebar get its threads from?", "And where is a thread's updatedAt set?",
		"Can the project groups collapse today?", "Sort them by most recent activity.", "Persist the collapsed state.")
	h := start(t, dir)
	th := h.thread("Sort sidebar threads by recent activity")
	h.a.select_(th)
	h.until("its transcript", func() bool { return len(entriesOf(th)) == 5*4 })
	h.tt.Advance(time.Second)
	return h, th
}

// The timeline starts at its end; scrolled up, a pill takes it back.
func TestTimelineFollowsEnd(t *testing.T) {
	h, th := long(t)
	tt := h.tt
	l := h.a.listFor(th.id())
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
// message sent, as their paths; a long paste becomes one too.
func TestAttachments(t *testing.T) {
	h := start(t, tempDir(t))
	a, tt := h.a, h.tt
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
	a.draft = "look"
	a.send(a.current())
	es := entriesOf(a.current())
	if len(a.attachments) != 0 || len(es) != 1 || es[0].text != "look\n\n@resources/icon.png\n\n"+strings.Repeat("x\n", longPaste-1)+"x" {
		t.Errorf("sent %q: the message should take the attachments, the file by its path", texts(es))
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
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	for i := range 10 {
		seed(t, d, "/work/t3code", "Thread "+string(rune('A'+i)))
	}
	h := start(t, dir)
	h.thread("Thread J")
	a, tt := h.a, h.tt
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

// @ suggests the workspace's files and / commands; the keys move and take
// the choice.
func TestAutocomplete(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "apps/web/src/components"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "apps/web/src/components/Sidebar.tsx"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	a := offline(t)
	a.fresh.workspace = ws
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

// Quick ask starts a thread on this machine in the chosen project with
// what was typed, and opens it.
func TestQuickAsk(t *testing.T) {
	dir := tempDir(t)
	inbox(t, dir)
	h := start(t, dir)
	h.thread("SIP re-INVITE handling")
	a := h.a
	for i, p := range a.localProjects() {
		if p.name() == "agent-phone" {
			a.quick.project = i
		}
	}
	a.quick.query = "Why does the SIP stack drop calls?"
	a.startQuickThread()
	th := a.current()
	if th.env != h.local() || th.View.Workspace != "/work/agent-phone" || th.View.Title != "Why does the SIP stack drop calls?" {
		t.Fatalf("opened %q in %s on %v", th.View.Title, th.View.Workspace, th.env)
	}
	h.until("the question in the thread", func() bool { return h.tt.HasText("Why does the SIP stack drop calls?") && h.tt.HasText("Read main.go") })
}

// The preview pane and the diff share the side of the chat: one at a time.
func TestPreviewToggle(t *testing.T) {
	a := offline(t)
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
	h, th := long(t)
	es := entriesOf(th)
	first := "Go to: " + preview(es[0])
	if err := h.tt.Click(first); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	if f, _ := h.a.listFor(th.id()).Visible(); f > 0 {
		t.Errorf("first row in view %d after clicking the first marker, want 0", f)
	}
	if !h.tt.HasText(es[0].text) {
		t.Error("the first message isn't in view")
	}
}

// The sidebar shows the projects chosen, several at once; choosing every
// one is showing all.
func TestProjectFilter(t *testing.T) {
	dir := tempDir(t)
	inbox(t, dir)
	h := start(t, dir)
	h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	a := h.a
	shown := func() map[string]bool {
		m := map[string]bool{}
		for _, cat := range []category{catInbox, catWorking, catSnoozed, catSettled} {
			for _, it := range a.itemsIn(cat) {
				m[it.p.name()] = true
			}
		}
		return m
	}
	for _, name := range []string{"t3code", "agent-phone"} {
		h.click(name)
	}
	if s := shown(); !s["t3code"] || !s["agent-phone"] || s["mygo"] {
		t.Errorf("filtered to t3code and agent-phone, the sidebar shows %v", s)
	}
	h.click("mygo")
	if len(a.projectFilter) != 0 {
		t.Errorf("every project chosen should be no filter, got %v", a.projectFilter)
	}
	h.click("agent-phone")
	h.click("All")
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
	h, th := long(t)
	tt := h.tt
	l := h.a.listFor(th.id())
	if !tt.HasText("Approve for me") {
		t.Fatal("the composer should be full size at the end")
	}
	l.ScrollTo(0, ui.Start)
	tt.Advance(time.Second)
	if tt.HasText("Approve for me") {
		t.Error("scrolled up, the composer should fold its controls away")
	}
	// Changes from outside the view, as typing's, which a frame shows.
	h.a.draft = "a follow-up"
	tt.Frame()
	tt.Advance(time.Second)
	if !tt.HasText("Approve for me") {
		t.Error("with a draft, the composer should be full size")
	}
	h.a.draft = ""
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
