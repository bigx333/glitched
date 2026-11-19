package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/plugins/widget"
	"glitched/internal/provider/fake"
)

// The model and access chosen for a new thread are the default again
// once the app starts anew on the same state directory.
func TestTheNewThreadDefaultOutlivesARestart(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir, nil, func(func()) {})
	tt := ui.NewTester(a.view, 1320, 860)
	for _, click := range []string{"Model", "Sonnet 5.5", "Fast mode", "Access", accessAsk} {
		if err := tt.Click(click); err != nil {
			t.Fatal(err)
		}
	}
	a.effort = "Low"
	tt.Frame()
	again := newApp(dir, nil, func(func()) {})
	if got, want := again.choice(), (choice{Model: "Claude Sonnet 5.5", Effort: "Low", Fast: true, Access: accessAsk}); got != want {
		t.Fatalf("after a restart the composer has %+v, want %+v", got, want)
	}
}

// A composer.json naming what the pickers no longer offer keeps the
// app's own default for each such name.
func TestAStaleComposerFileKeepsTheDefaults(t *testing.T) {
	dir := t.TempDir()
	want := offline(t).choice()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model":"Claude Opus 3","effort":"Huge","access":"Root"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := newApp(dir, nil, func(func()) {}).choice(); got != want {
		t.Fatalf("from a stale file the composer has %+v, want %+v", got, want)
	}
}

// Two windows on one state directory each choose a default. What one chose
// isn't undone by the other choosing something else, and a window that
// chose shows what a restart would: the other's choice with its own.
func TestTwoWindowsOnOneStateDirectoryKeepEachOthersDefault(t *testing.T) {
	dir := t.TempDir()
	one, two := newApp(dir, nil, func(func()) {}), newApp(dir, nil, func(func()) {})
	first, second := ui.NewTester(one.view, 1320, 860), ui.NewTester(two.view, 1320, 860)
	click := func(tt *ui.Tester, texts ...string) {
		t.Helper()
		for _, text := range texts {
			if err := tt.Click(text); err != nil {
				t.Fatal(err)
			}
		}
	}
	restarted := func() choice { return newApp(dir, nil, func(func()) {}).choice() }

	click(first, "Access", accessAsk)
	click(second, "Model", "Sonnet 5.5")
	want := choice{Model: "Claude Sonnet 5.5", Effort: "High", Access: accessAsk}
	if got := restarted(); got != want {
		t.Errorf("after a restart the default is %+v, want %+v: one window's access and the other's model", got, want)
	}
	if got := two.choice(); got != want {
		t.Errorf("the window that chose the model shows %+v, want %+v as a restart would", got, want)
	}

	click(first, "Model", "Fast mode")
	want.Fast = true
	if got := restarted(); got != want {
		t.Errorf("after a restart the default is %+v, want %+v", got, want)
	}
	if got := one.choice(); got != want {
		t.Errorf("the window that chose fast mode shows %+v, want %+v as a restart would", got, want)
	}
}

// A default that couldn't be saved is saved with the next one chosen, and
// not tried again each frame until then.
func TestADefaultThatCouldNotBeSavedIsSavedWithTheNext(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir, nil, func(func()) {})
	tt := ui.NewTester(a.view, 1320, 860)
	// Nothing can be written over a directory.
	path := filepath.Join(dir, "composer.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, click := range []string{"Access", accessAsk} {
		if err := tt.Click(click); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the save that failed was tried again with nothing chosen since: %v", err)
	}
	for _, click := range []string{"Model", "Fast mode"} {
		if err := tt.Click(click); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := newApp(dir, nil, func(func()) {}).choice(), a.choice(); got != want || !want.Fast || want.Access != accessAsk {
		t.Errorf("after a restart the default is %+v, want %+v", got, want)
	}
}

// A composer.json that doesn't parse holds no default to keep: the next
// one chosen replaces it.
func TestAComposerFileThatDoesNotParseIsReplaced(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model": `), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newApp(dir, nil, func(func()) {})
	tt := ui.NewTester(a.view, 1320, 860)
	for _, click := range []string{"Access", accessAsk} {
		if err := tt.Click(click); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := newApp(dir, nil, func(func()) {}).choice(), a.choice(); got != want || want.Access != accessAsk {
		t.Errorf("after a restart the default is %+v, want %+v", got, want)
	}
}

// In a thread, the pickers show and change that thread's own model and
// access, within its provider, and leave the default for a new thread as
// it was.
func TestAThreadsPickersAreItsOwn(t *testing.T) {
	h := start(t, tempDir(t))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	id := h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAutoEdits)
	h.thread("Mine")
	h.a.selected = id
	h.a.model, h.a.access = "Claude Haiku 5.5", accessFull
	draft := h.a.choice()
	h.tt.Frame()
	if _, ok := h.tt.Find("Claude Opus 5.5"); !ok {
		t.Fatalf("the composer doesn't show the thread's model: %q", h.tt.Texts())
	}
	h.click("Access")
	h.click(accessAsk)
	h.click("Model")
	if _, ok := h.tt.Find("Codex"); ok {
		t.Error("the picker offers another provider's models in a Claude thread")
	}
	h.click("Sonnet 5.5")
	h.click("Fast mode")
	want := api.ModelRef{Provider: "claude", Model: "Claude Sonnet 5.5", Effort: "high", Fast: true}
	h.until("the thread's new model and access", func() bool {
		v := h.thread("Mine").View
		return v.Model == want && v.Access == api.AccessAsk
	})
	if h.a.choice() != draft {
		t.Errorf("the default for a new thread is %+v after changing a thread's, want %+v", h.a.choice(), draft)
	}
	if _, err := os.Stat(h.a.composerPath()); !os.IsNotExist(err) {
		t.Errorf("a thread's change saved the default for a new one: %v", err)
	}
}

// A thread of a provider the picker doesn't list is offered no models to
// switch to: it keeps its provider.
func TestAThreadOfAnUnlistedProviderIsOfferedNoModels(t *testing.T) {
	h := start(t, tempDir(t))
	id := h.a.backend.Create(h.local(), t.TempDir(), "Fake", api.ModelRef{Provider: "fake", Model: "fake"}, api.AccessAsk)
	h.thread("Fake")
	h.a.selected = id
	h.tt.Frame()
	h.click("Model")
	for _, s := range []string{"Claude", "Codex", "Sonnet 5.5", "GPT-5.5"} {
		if r, ok := h.tt.Find(s); ok && r.W > 0 {
			if err := h.tt.Click(s); err == nil {
				t.Errorf("the picker offers %q on a fake thread", s)
			}
		}
	}
	h.tt.Frame()
	if m := h.thread("Fake").View.Model; m.Model != "fake" {
		t.Errorf("the thread's model is %+v", m)
	}
}

// The effort chosen in a thread is that thread's, as the engine names it.
func TestAThreadsEffortIsItsOwn(t *testing.T) {
	h := start(t, tempDir(t))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	id := h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAsk)
	h.thread("Mine")
	h.a.selected = id
	h.tt.Frame()
	h.click("Model")
	faster, _ := h.tt.Find("Faster")
	h.tt.ClickAtWith(0, faster.X+stopInset, faster.Y-24) // Low's point
	h.tt.Advance(time.Second)
	h.until("the thread's new effort", func() bool { return h.thread("Mine").View.Model.Effort == "low" })
	if h.a.effort != "High" {
		t.Errorf("the default effort is %q after changing a thread's, want High", h.a.effort)
	}
}

// effortAt is where the open picker's effort slider has its levels: the
// window's x of the point level levels along it, from Low at 0, and its y.
func effortAt(t *testing.T, tt *ui.Tester) (at func(level float32) float32, y float32) {
	t.Helper()
	faster, ok := tt.Find("Faster")
	smarter, there := tt.Find("Smarter")
	if !ok || !there {
		t.Fatalf("no effort slider in %q", tt.Texts())
	}
	left, right := faster.X, smarter.X+smarter.W
	return func(level float32) float32 { return left + stopInset + (right-left-2*stopInset)*level/3 }, faster.Y - 24
}

// The effort's thumb sends a level's configure once, when it locks onto
// the level, though the configure fails: held there it asks no more of
// the engine, and let go there it asks nothing either, as a click on a
// level asks once. Dragged onto the level again, it asks again.
func TestTheEffortThumbSendsALevelOnceEachTimeItsChosen(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: api.NewID(),
		Body: api.ThreadCreate{Workspace: "/work/t3code", Title: "Mine", Model: opus}}); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	th := h.thread("Mine")
	h.a.select_(th)
	h.tt.Advance(time.Second)
	d.Close()
	h.until("the engine gone", func() bool { return h.local().Status != client.Status(client.Online{}) })
	h.click("Model")
	h.tt.Advance(time.Second)
	at, y := effortAt(t, h.tt)
	// The failed configure's row: with no engine, each one sent fails at
	// once and takes the place of the one before it.
	sent := func() api.ID {
		t.Helper()
		if len(th.Pending) != 1 || th.Pending[0].Err == nil {
			t.Fatalf("the thread's pending commands are %+v, want its one failed configure", th.Pending)
		}
		return th.Pending[0].Command
	}

	h.tt.Press(at(2), y) // High, the thread's own
	if len(th.Pending) != 0 {
		t.Fatalf("pressing the thumb where it is sent %+v", th.Pending)
	}
	h.tt.Move(at(1.5), y)
	h.tt.Move(at(0), y)
	low := sent()
	for range 5 {
		h.tt.Frame()
	}
	h.tt.Move(at(0.05), y)
	if sent() != low {
		t.Error("the thumb held on Low sent its configure again")
	}
	h.tt.Release(at(0.05), y)
	h.tt.Advance(time.Second)
	if sent() != low {
		t.Error("letting the thumb go on Low sent its configure again")
	}

	h.tt.Press(at(2), y)
	h.tt.Move(at(0), y)
	h.tt.Release(at(0), y)
	h.tt.Advance(time.Second)
	again := sent()
	if again == low {
		t.Error("the thumb dragged onto Low again, after its configure failed, sent nothing")
	}

	h.tt.Press(at(1), y)
	medium := sent()
	if medium == again {
		t.Fatal("a press on Medium's point sent nothing")
	}
	h.tt.Release(at(1), y)
	h.tt.Advance(time.Second)
	if sent() != medium {
		t.Error("a click on Medium's point sent its configure twice")
	}
}

// A child's wider access the engine refuses fails, with its row. Picked
// again once its parent is as wide, it's sent again and taken.
func TestAnAccessRefusedIsSentAgainWhenPickedAgain(t *testing.T) {
	h := tree3(t)
	pick := func(title, level string) {
		t.Helper()
		h.a.select_(h.thread(title))
		h.tt.Advance(time.Second)
		h.click("Access")
		h.click(level)
		h.tt.Advance(time.Second)
	}
	child := h.thread("Write the tests")
	pick("Write the tests", accessAuto)
	h.until("the refusal", func() bool { return len(child.Pending) == 1 && child.Pending[0].Err != nil })
	refused := child.Pending[0].Command

	pick("Ship the sidebar sort", accessAuto)
	h.until("the parent at Approve for me", func() bool { return h.thread("Ship the sidebar sort").View.Access == api.AccessAutoEdits })

	pick("Write the tests", accessAuto)
	if len(child.Pending) == 1 && child.Pending[0].Command == refused {
		t.Fatal("picking the level again sent nothing: the refused configure's row is all there is")
	}
	h.until("the child at Approve for me", func() bool { return child.View.Access == api.AccessAutoEdits && len(child.Pending) == 0 })
}

// A picker that closes under the held thumb, as Escape and ⌘, close it,
// lets the thumb go: the default it chose outlives a restart, and so does
// the next one chosen.
func TestADefaultChosenByAThumbItsPickerClosedOnIsKept(t *testing.T) {
	for name, key := range map[string]struct {
		mods ui.Modifiers
		key  ui.Key
	}{
		"Escape":   {0, ui.KeyEscape},
		"Settings": {ui.Cmd, ui.KeyComma},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			a := newApp(dir, nil, func(func()) {})
			tt := ui.NewTester(a.view, 1320, 860)
			if err := tt.Click("Model"); err != nil {
				t.Fatal(err)
			}
			at, y := effortAt(t, tt)
			tt.Press(at(2), y)
			tt.Move(at(0), y)
			tt.Key(key.mods, key.key)
			tt.Frame()
			if _, ok := tt.Find("Faster"); ok {
				t.Fatalf("the picker is still open: %q", tt.Texts())
			}
			tt.Release(at(0), y)
			tt.Advance(time.Second)
			if got := newApp(dir, nil, func(func()) {}).effort; a.effort != "Low" || got != "Low" {
				t.Errorf("the thumb chose %q; after a restart the default is %q, want Low", a.effort, got)
			}
			a.router.Push("/")
			tt.Advance(time.Second)
			for _, click := range []string{"Access", accessAsk} {
				if err := tt.Click(click); err != nil {
					t.Fatal(err)
				}
			}
			tt.Advance(time.Second)
			if got, want := newApp(dir, nil, func(func()) {}).choice(), a.choice(); got != want || want.Access != accessAsk {
				t.Errorf("after a restart the default is %+v, want %+v as chosen since", got, want)
			}
		})
	}
}

// A press on the effort's track that its picker closes under chooses
// nothing when it's let go: not for the draft, and not, as a click where
// it was pressed, for the thread whose picker opens next.
func TestAPressItsPickerClosedOnChoosesNoEffort(t *testing.T) {
	h := start(t, tempDir(t))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAsk)
	th := h.thread("Mine")
	h.until("the thread's view", func() bool { return th.View.Revision > 0 && len(th.Pending) == 0 })
	h.tt.Frame()
	h.click("Model")
	at, y := effortAt(t, h.tt)
	h.tt.Press(at(0.3), y) // between Low and Medium, in the draft
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	h.tt.Release(at(0.3), y)
	h.tt.Advance(time.Second)
	if h.a.effort != "High" {
		t.Errorf("the draft's effort is %q after a press its picker closed on, want High", h.a.effort)
	}
	h.a.select_(th)
	h.tt.Advance(time.Second)
	h.click("Model") // only opens the thread's picker
	h.tt.Advance(time.Second)
	if len(th.Pending) != 0 {
		t.Fatalf("opening the thread's picker sent %+v", th.Pending)
	}
	h.settle(func() bool { return false })
	if got := th.View.Model.Effort; got != "high" {
		t.Errorf("opening the thread's picker changed its effort to %q, want high", got)
	}
}

// A thumb held in one composer chooses nothing in another. Dragged in a
// thread, with ⌘N bringing the draft under it, it leaves the default for
// a new thread as it was, and the picker closes with the thread it was of.
func TestAThumbHeldInAThreadChoosesNothingInTheDraft(t *testing.T) {
	h := start(t, tempDir(t))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAsk)
	th := h.thread("Mine")
	h.until("the thread's view", func() bool { return th.View.Revision > 0 && len(th.Pending) == 0 })
	h.a.select_(th)
	h.tt.Advance(time.Second)
	h.click("Model")
	h.tt.Advance(time.Second)
	at, y := effortAt(t, h.tt)
	h.tt.Press(at(2), y) // High, the thread's own
	h.tt.Move(at(1), y)
	h.until("the thread at Medium", func() bool { return th.View.Model.Effort == "medium" && len(th.Pending) == 0 })
	h.tt.Key(ui.Cmd, ui.KeyN)
	h.tt.Frame()
	h.tt.Move(at(0), y)
	h.tt.Frame()
	h.tt.Release(at(0), y)
	h.tt.Advance(time.Second)
	if !h.a.current().id().IsZero() {
		t.Fatalf("⌘N left the thread %q in the composer", h.a.current().View.Title)
	}
	if _, open := h.tt.Find("Faster"); open {
		t.Errorf("the thread's picker is open over the draft: %q", h.tt.Texts())
	}
	if got := newApp(h.a.dir, nil, func(func()) {}).effort; h.a.effort != "High" || got != "High" {
		t.Errorf("a thumb held in a thread left the default's effort at %q, and %q after a restart; want High, as it was", h.a.effort, got)
	}
	if got := th.View.Model.Effort; got != "medium" || len(th.Pending) != 0 {
		t.Errorf("the thread's effort is %q with %+v pending, want the medium its thumb chose", got, th.Pending)
	}
}

// Nor does a thumb held in one thread choose for the thread that comes
// under it, as one jumped to from the keyboard: that one is sent nothing.
func TestAThumbHeldInAThreadChoosesNothingInTheNextThread(t *testing.T) {
	h := start(t, tempDir(t))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAsk)
	h.a.backend.Create(h.local(), t.TempDir(), "Other", opus, api.AccessAsk)
	mine, other := h.thread("Mine"), h.thread("Other")
	h.until("the threads' views", func() bool {
		return mine.View.Revision > 0 && other.View.Revision > 0 && len(mine.Pending)+len(other.Pending) == 0
	})
	h.a.select_(mine)
	h.tt.Advance(time.Second)
	h.click("Model")
	h.tt.Advance(time.Second)
	at, y := effortAt(t, h.tt)
	h.tt.Press(at(2), y) // High, the thread's own
	h.tt.Frame()
	h.a.select_(other)
	h.tt.Frame()
	h.tt.Move(at(0), y)
	h.tt.Frame()
	h.tt.Release(at(0), y)
	h.tt.Advance(time.Second)
	if got := threadChoice(other).Effort; got != "High" || len(mine.Pending)+len(other.Pending) != 0 {
		t.Errorf("a thumb held in one thread left the thread come under it at effort %q, with %d sent to it and %d to its own; want High and nothing sent",
			got, len(other.Pending), len(mine.Pending))
	}
	if _, open := h.tt.Find("Faster"); open {
		t.Errorf("the first thread's picker is open over the next: %q", h.tt.Texts())
	}
}

// A hold on Full access begun in one thread grants nothing to the thread
// that comes under it.
func TestAHoldOnFullAccessGrantsNothingToTheNextThread(t *testing.T) {
	h := start(t, tempDir(t))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAsk)
	h.a.backend.Create(h.local(), t.TempDir(), "Other", opus, api.AccessAsk)
	mine, other := h.thread("Mine"), h.thread("Other")
	h.until("the threads' views", func() bool {
		return mine.View.Revision > 0 && other.View.Revision > 0 && len(mine.Pending)+len(other.Pending) == 0
	})
	h.a.select_(mine)
	h.tt.Advance(time.Second)
	h.click("Access")
	h.tt.Advance(time.Second)
	r, ok := h.tt.Find(accessFull)
	if !ok {
		t.Fatalf("no Full access row: %q", h.tt.Texts())
	}
	h.tt.Press(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Advance(holdTime / 2)
	h.a.select_(other)
	h.tt.Advance(2 * holdTime)
	h.tt.Release(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Advance(time.Second)
	if got := threadChoice(other).Access; got != accessAsk || len(mine.Pending)+len(other.Pending) != 0 {
		t.Errorf("a hold begun in one thread left the thread come under it at %q, with %d sent to it and %d to its own; want %q and nothing sent",
			got, len(other.Pending), len(mine.Pending), accessAsk)
	}
	if _, open := h.tt.Find(accessAuto); open {
		t.Errorf("the first thread's picker is open over the next: %q", h.tt.Texts())
	}
}

// A thread's pickers send only what they changed. With another client's
// narrowing not yet shown here, an effort chosen in the thread shows at
// once and leaves the access the engine has: it doesn't put back the one
// this window still shows.
func TestAThreadsPickerSendsOnlyWhatItChanged(t *testing.T) {
	ctx := context.Background()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	id := api.NewID()
	if _, err := d.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: id,
		Body: api.ThreadCreate{Workspace: "/work/t3code", Title: "Mine", Model: opus, Access: api.AccessFull}}); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	th := h.thread("Mine")
	h.a.select_(th)
	h.tt.Advance(time.Second)
	// Another client narrows the thread. Its view waits among what the
	// manager posted, which nothing runs until the effort is chosen.
	if _, err := d.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: id,
		Body: api.ThreadConfigure{Access: new(api.AccessAsk)}}); err != nil {
		t.Fatal(err)
	}
	h.click("Model")
	h.tt.Advance(time.Second)
	at, y := effortAt(t, h.tt)
	h.tt.Press(at(0), y)
	h.tt.Release(at(0), y)
	h.tt.Advance(time.Second)
	if th.View.Access != api.AccessFull {
		t.Fatalf("the other client's narrowing shows already: the thread's view is at access %v", th.View.Access)
	}
	if got, want := threadChoice(th), (choice{Model: "Claude Opus 5.5", Effort: "Low", Access: accessFull}); got != want {
		t.Errorf("with its effort on its way the thread shows %+v, want %+v", got, want)
	}
	h.until("the thread's new effort", func() bool { return th.View.Model.Effort == "low" && len(th.Pending) == 0 })
	snap, err := d.Endpoint().Read(ctx, api.Query{Thread: id})
	if err != nil {
		t.Fatal(err)
	}
	if v := snap.Threads[0]; v.Access != api.AccessAsk || v.Model.Effort != "low" {
		t.Errorf("the engine has effort %q at access %v, want the effort chosen here at the other client's Ask", v.Model.Effort, v.Access)
	}
}

// A choice that failed has a row of its own, which says what it was: with
// the engine gone, an effort and then an access chosen leave a row each,
// as neither's Retry undoes the other.
func TestAFailedChoicesRowSaysWhichItWas(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: api.NewID(),
		Body: api.ThreadCreate{Workspace: "/work/t3code", Title: "Mine", Model: opus}}); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	th := h.thread("Mine")
	h.a.select_(th)
	h.tt.Advance(time.Second)
	d.Close()
	h.until("the engine gone", func() bool { return h.local().Status != client.Status(client.Online{}) })
	h.click("Model")
	h.tt.Advance(time.Second)
	at, y := effortAt(t, h.tt)
	h.tt.Press(at(0), y)
	h.tt.Release(at(0), y)
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Advance(time.Second)
	h.click("Access")
	h.click(accessAuto)
	h.tt.Advance(time.Second)
	if len(th.Pending) != 2 || th.Pending[0].Err == nil || th.Pending[1].Err == nil {
		t.Fatalf("the thread's pending commands are %+v, want its failed effort and its failed access", th.Pending)
	}
	for _, row := range []string{"Couldn't change the thread's effort: ", "Couldn't change the thread's access: "} {
		if !h.tt.HasText(row) {
			t.Errorf("no row says %q: the window shows %q", row, h.tt.Texts())
		}
	}
	for what, c := range map[string]api.ThreadConfigure{
		"model":                       {Model: new("Claude Sonnet 5.5")},
		"fast mode":                   {Fast: new(true)},
		"model and fast mode":         {Model: new("Claude Sonnet 5.5"), Fast: new(false)},
		"model, effort and fast mode": {Model: new("Claude Sonnet 5.5"), Effort: new("low"), Fast: new(false)},
	} {
		if got, want := failure(client.Pending{Body: c, Err: errors.New("boom")}), "Couldn't change the thread's "+what+": boom"; got != want {
			t.Errorf("a failed configure's row says %q, want %q", got, want)
		}
	}
}

// A default the draft's thumb chose is kept though the draft goes before
// the thumb is let go, as a thread or another page comes under it: the
// default is saved once the hold ends, whatever the app shows then.
func TestADefaultChosenByAThumbTheDraftLostIsKept(t *testing.T) {
	for name, away := range map[string]func(h *harness, th thread){
		"Thread": func(h *harness, th thread) { h.a.select_(th) },
		"Page":   func(h *harness, th thread) { h.a.router.Push("/settings/appearance") },
	} {
		t.Run(name, func(t *testing.T) {
			h := start(t, tempDir(t))
			opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
			h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAsk)
			th := h.thread("Mine")
			h.until("the thread's view", func() bool { return th.View.Revision > 0 && len(th.Pending) == 0 })
			h.tt.Frame()
			h.click("Model")
			at, y := effortAt(t, h.tt)
			h.tt.Press(at(2), y)
			h.tt.Move(at(0), y)
			h.tt.Frame()
			away(h, th)
			h.tt.Frame()
			h.tt.Release(at(0), y)
			h.tt.Advance(time.Second)
			h.a.router.Push("/")
			h.a.select_(th)
			h.tt.Advance(time.Second)
			if got := newApp(h.a.dir, nil, func(func()) {}).effort; got != "Low" {
				t.Errorf("after a restart the default's effort is %q, want the Low its thumb chose", got)
			}
			if got := th.View.Model.Effort; got != "high" || len(th.Pending) != 0 {
				t.Errorf("the thread's effort is %q with %+v pending, want high and nothing sent", got, th.Pending)
			}
		})
	}
}

const configurerModule = `export async function command({ id, args }) {
	switch (id) {
	case "list": {
		const l = await glitched.models.list()
		await glitched.toast("models " + l.models.map((m) => m.provider + ":" + m.name).join(",") + " efforts " + l.efforts.join(","))
		return
	}
	case "configure":
		await glitched.threads.configure(args.thread, args.options)
		return
	}
}`

// A plugin lists the picker's models and sets a thread's model, effort and
// fast mode as the picker does, under threads:write, but never its access:
// a configure that names it, or anything else, is refused whole. The
// Providers page is Glitched's own: no plugin replaces or places it.
func TestAPluginConfiguresAThreadsModelButNeverItsAccess(t *testing.T) {
	h := start(t, tempDir(t))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	id := h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAutoEdits)
	h.thread("Mine")
	writeOwnPlugin(t, h.a, "configurer", `{"id": "configurer", "name": "Configurer", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["threads:write"], "contributes": {"commands": [{"id": "list", "title": "l"}, {"id": "configure", "title": "c"}]}}`, configurerModule)
	settlePlugins(t, h.a, h.tt, h.p)
	run := func(cmd string, options map[string]any) error {
		t.Helper()
		var failed error
		ran := false
		h.a.plugins.RunCommand("configurer", cmd, map[string]any{"thread": id.String(), "options": options}, func(err error) { failed, ran = err, true })
		h.until(cmd+" to run", func() bool { return ran })
		settlePlugins(t, h.a, h.tt, h.p)
		return failed
	}
	if err := run("list", nil); err != nil {
		t.Fatal(err)
	}
	if !h.tt.HasText("models Claude:Claude Opus 5.5,Claude:Claude Sonnet 5.5") || !h.tt.HasText("efforts low,medium,high,max") {
		t.Errorf("models.list answered %q", h.tt.Texts())
	}
	sonnet := map[string]any{"model": "Claude Sonnet 5.5", "effort": "Low", "fast": true}
	if err := run("configure", sonnet); err == nil || !strings.Contains(err.Error(), "threads:write") {
		t.Fatalf("configured without threads:write: %v", err)
	}
	if err := h.a.plugins.SetGranted("configurer", "threads:write", true); err != nil {
		t.Fatal(err)
	}
	settlePlugins(t, h.a, h.tt, h.p)
	for _, options := range []map[string]any{
		{"access": "full"},
		{"model": "Claude Sonnet 5.5", "access": "full"},
		{"Access": "full"},
		{"fast": true, "provider": "codex"},
		{"model": "GPT-5.5"},
		{"effort": "extreme"},
		{},
	} {
		if err := run("configure", options); err == nil {
			t.Errorf("configure %v was taken", options)
		}
	}
	if v := h.thread("Mine"); v.View.Model != opus || v.View.Access != api.AccessAutoEdits || len(v.Pending) != 0 {
		t.Fatalf("a refused configure changed the thread: %+v %v, pending %v", v.View.Model, v.View.Access, v.Pending)
	}
	if err := run("configure", sonnet); err != nil {
		t.Fatal(err)
	}
	want := api.ModelRef{Provider: "claude", Model: "Claude Sonnet 5.5", Effort: "low", Fast: true}
	h.until("the thread's new model", func() bool {
		v := h.thread("Mine").View
		return v.Model == want && v.Access == api.AccessAutoEdits
	})

	writeOwnPlugin(t, h.a, "providers", replacing("providers", "settings.providers"), `export function place({ items }) { return items.map(() => ({ type: "text", text: "Mine" })) }`)
	h.a.plugins.SetGranted("providers", "ui.replace", true)
	h.a.plugins.Reload()
	if err := installed(h.a, "providers").Err; err == nil || !strings.Contains(err.Error(), "settings.providers, which is off limits to plugins") {
		t.Errorf("a plugin replacing the Providers page loaded: %v", err)
	}
	if _, err := widget.Decode([]byte(`{"body": {"type": "native", "name": "settings.providers"}}`)); err == nil || !strings.Contains(err.Error(), "off limits") {
		t.Errorf("a view may place the Providers page: %v", err)
	}
	h.a.router.Push("/settings/providers")
	h.tt.Frame()
	if !h.tt.HasText("Claude Code") || h.tt.HasText("Mine") {
		t.Errorf("the Providers page isn't Glitched's: %q", h.tt.Texts())
	}
}
