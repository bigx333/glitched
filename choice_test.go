package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
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
