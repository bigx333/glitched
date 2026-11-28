package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

func TestManagerUIHeaderSweepAndWakeDismiss(t *testing.T) {
	dir := tempDir(t)
	installOwn(t, dir, "managers")
	h := startWith(t, dir, func(c *daemon.Config) {
		c.Providers = daemon.FakeProviders(fake.New(fake.Turn(true)))
	})
	h.grantManagers()

	ws := gitWorkspace(t)
	mgrID := h.a.backend.CreateManager(h.local(), ws, "Workspace Coordinator", api.ModelRef{Provider: "claude", Model: "claude-opus-5-5"}, api.AccessAutoEdits, &api.SweepSchedule{IntervalSeconds: 1800})
	h.until("manager created", func() bool {
		th := h.local().Threads[mgrID]
		return th != nil && th.View.Role == api.RoleManager && th.View.Revision > 0
	})

	h.a.selected = mgrID
	h.a.backend.OpenThread(h.local(), mgrID)
	h.until("the Manager's pills", func() bool {
		h.tt.Frame()
		_, m := h.inHeader("Manager")
		_, s := h.inHeader("Sweep 30m")
		return m && s
	})

	h.clickHeaderPlugin("Sweep 30m")
	h.until("the sweep popover", func() bool { h.tt.Frame(); return h.tt.HasText("Sweep schedule") })
	h.clickPlugin("Sweep every 1h")
	h.until("sweep updated to 1h", func() bool {
		th := h.local().Threads[mgrID]
		return th != nil && th.View.Sweep != nil && th.View.Sweep.IntervalSeconds == 3600
	})
	h.until("Sweep 1h pill", func() bool { h.tt.Frame(); _, ok := h.inHeader("Sweep 1h"); return ok })

	runID := h.a.backend.Send(h.local(), mgrID, "sweep turn", api.StartNow)
	h.until("manager turn waiting", func() bool {
		th := h.local().Threads[mgrID]
		return th != nil && th.View.Turn != nil && th.View.Run == api.RunWaiting
	})
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer ep.Close()
	_, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: mgrID, Body: api.WakeDismiss{Run: runID, Reason: "quiet"}})
	if !errors.As(err, new(*api.RejectedError)) {
		t.Fatalf("WakeDismiss of the user's turn = %v, want RejectedError", err)
	}
}

func TestCmdKNewManagerTakesTheProjectAndTheNewThreadsModel(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	repo, worktree := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := d.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{
		Workspace: worktree, Project: repo, Branch: "feature", Title: "In a worktree", Model: fakeModel,
	}}); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	h.a.select_(h.thread("In a worktree"))
	h.a.show(choice{Model: modelKey{Provider: fakeModel.Provider}, Follow: true})
	mgr := h.newManager()
	if mgr.View.Workspace != repo {
		t.Errorf("the Manager runs in %q, want the project %q", mgr.View.Workspace, repo)
	}
	if mgr.View.Model.Model != "" || mgr.View.Model.Provider != fakeModel.Provider {
		t.Errorf("the Manager's model is %+v, want the program's own", mgr.View.Model)
	}
}

func TestCmdKNewManagerWithNoProjectHasItsOwnFolder(t *testing.T) {
	h := start(t, tempDir(t))
	h.a.startIn(draft{env: h.local(), workspace: h.a.scratchRoot()})
	before, _ := os.ReadDir(h.a.scratchRoot())
	h.a.actions()
	if listed, _ := os.ReadDir(h.a.scratchRoot()); len(listed) != len(before) {
		t.Fatalf("listing the actions made folders: %v, was %v", listed, before)
	}
	mgr := h.newManager()
	ws := mgr.View.Workspace
	if filepath.Dir(ws) != h.a.scratchRoot() {
		t.Fatalf("the Manager runs in %q, want a folder of its own under %q", ws, h.a.scratchRoot())
	}
	if fi, err := os.Stat(ws); err != nil || !fi.IsDir() {
		t.Fatalf("the Manager's folder %q: %v", ws, err)
	}
}

func (h *harness) newManager() *client.Thread {
	h.t.Helper()
	before := h.a.selected
	i := slices.IndexFunc(h.a.actions(), func(c command) bool { return c.id == "manager.new" })
	if i < 0 {
		h.t.Fatal("Cmd+K lists no New manager thread")
	}
	h.a.actions()[i].run()
	id := h.a.selected
	if id == before || id.IsZero() {
		h.t.Fatalf("New manager thread showed %s, not a new thread", id)
	}
	h.until("the Manager", func() bool {
		th := h.local().Threads[id]
		return th != nil && th.View.Role == api.RoleManager && th.View.Revision > 0
	})
	return h.local().Threads[id]
}

func TestManagerFailedSweepSaysSo(t *testing.T) {
	p := client.Pending{Body: api.ThreadConfigure{Sweep: &api.SweepSchedule{IntervalSeconds: 3600}}, Err: errors.New("boom")}
	if got, want := failure(p), "Couldn't change the thread's sweep schedule: boom"; got != want {
		t.Fatalf("the row says %q, want %q", got, want)
	}
}

func TestDismissedWakeKeepsQueuedSendNowEnabled(t *testing.T) {
	dismissed := api.Thread{
		Run:  api.RunRunning,
		Turn: &api.Turn{Run: api.NewID(), Silent: true, Dismissed: true},
	}
	if !canSteer(dismissed) || !dismissed.Stoppable() || !activeTurn(dismissed) {
		t.Fatalf("dismissed Manager wake: canSteer=%v Stoppable=%v activeTurn=%v, want true/true/true", canSteer(dismissed), dismissed.Stoppable(), activeTurn(dismissed))
	}
	a := &app{}
	if !a.queues(thread{Thread: &client.Thread{View: dismissed}}) {
		t.Fatal("queues returned false for a quiet Manager wake")
	}
	waitingDismissed := dismissed
	waitingDismissed.Run = api.RunWaiting
	if a.queues(thread{Thread: &client.Thread{View: waitingDismissed}}) {
		t.Fatal("queues returned true while waiting on a request")
	}
	compacting := api.Thread{
		Run:  api.RunRunning,
		Turn: &api.Turn{Run: api.NewID(), Silent: true},
	}
	if canSteer(compacting) || compacting.Stoppable() || activeTurn(compacting) || a.queues(thread{Thread: &client.Thread{View: compacting}}) {
		t.Fatalf("silent compaction run: canSteer=%v Stoppable=%v activeTurn=%v, want all false", canSteer(compacting), compacting.Stoppable(), activeTurn(compacting))
	}
}

func TestManagerSweepCadenceKeepsItsPrompt(t *testing.T) {
	const prompt = "Check CI and report only red builds."
	at := &api.SweepSchedule{IntervalSeconds: 1800, Prompt: prompt}
	for _, s := range sweepChoices {
		next := sweepWithInterval(at, s.secs)
		if next.IntervalSeconds != s.secs || next.Prompt != prompt {
			t.Fatalf("from %+v to %s the schedule is %+v", at, s.label, next)
		}
		at = next
	}
	if s := sweepWithInterval(nil, 900); s.IntervalSeconds != 900 || s.Prompt != "" {
		t.Fatalf("with no schedule, 15m is %+v", s)
	}
}

// managerFixture is an engine with a plain thread and a Manager, under
// which the workers titled workers run, in the workspace ws, which docs
// fill once they have: the docs go into the Manager's prompt, which the
// fake spawner reads.
func (h *harness) grantManagers() {
	h.t.Helper()
	for _, c := range []string{"threads:read", "threads:write"} {
		if err := h.a.plugins.SetGranted("managers", c, true); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) clickHeaderPlugin(text string) {
	h.t.Helper()
	var r ui.Rect
	h.until(text+" in header", func() bool {
		h.tt.Frame()
		var ok bool
		r, ok = h.inHeader(text)
		return ok
	})
	h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	settlePlugins(h.t, h.a, h.tt, h.p)
}

func managerFixture(t *testing.T, ws string, docs map[string]string, workers ...string) (*harness, api.ID) {
	t.Helper()
	dir := tempDir(t)
	installOwn(t, dir, "managers")
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, ws, "A plain thread")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := api.NewID()
	if _, err := d.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: api.ThreadCreate{
		Workspace: ws, Title: "The coordinator", Model: fakeModel, Role: api.RoleManager,
		Sweep: &api.SweepSchedule{IntervalSeconds: 1800},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, w := range workers {
		seedChild(t, d, id, w, "do it")
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := start(t, dir)
	h.grantManagers()
	h.thread("The coordinator")
	h.thread("A plain thread")
	for _, w := range workers {
		h.thread(w)
	}
	return h, id
}

// A Manager lists in the sidebar's Managers, over the inbox, and in none
// of the inbox's sections, whichever sidebar draws them.
func TestManagersHaveASectionOfTheirOwn(t *testing.T) {
	h, id := managerFixture(t, "/work/mygo", nil)
	check := func(where string) {
		t.Helper()
		settlePlugins(t, h.a, h.tt, h.p)
		h.tt.Advance(time.Second)
		for _, cat := range []category{catInbox, catWorking, catSnoozed, catSettled} {
			if slices.ContainsFunc(h.a.itemsIn(cat), func(it inboxItem) bool { return it.t.id() == id }) {
				t.Fatalf("%s: the Manager is in %s", where, categories[cat].title)
			}
		}
		if ms := h.a.managerItems(); len(ms) != 1 || ms[0].t.id() != id {
			t.Fatalf("%s: the Managers are %v", where, ms)
		}
		if n := slices.Index(h.tt.Texts(), "The coordinator"); n < 0 || slices.Index(h.tt.Texts()[n+1:], "The coordinator") >= 0 {
			t.Fatalf("%s: the sidebar shows the Manager other than once: %q", where, h.tt.Texts())
		}
		head, _ := h.tt.Find("Managers")
		row, _ := h.tt.Find("The coordinator")
		inbox, _ := h.tt.Find("Inbox")
		plain, _ := h.tt.Find("A plain thread")
		if !(head.Y < row.Y && row.Y < inbox.Y && inbox.Y < plain.Y) {
			t.Fatalf("%s: Managers %+v, its row %+v, Inbox %+v, the plain thread %+v", where, head, row, inbox, plain)
		}
	}
	check("Glitched's own sidebar")
	if err := h.a.plugins.SetEnabled("sidebar", false); err != nil {
		t.Fatal(err)
	}
	h.a.pluginsChanged(nil)
	check("the fallback")
}

// A Manager is never snoozed or settled: its row offers neither, its menu
// neither, and a plugin can't.
func TestManagerIsNeverSnoozedOrSettled(t *testing.T) {
	h, id := managerFixture(t, "/work/mygo", nil)
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	h.hover("The coordinator")
	h.tt.Frame()
	if h.tt.HasText("Snooze") || h.tt.HasText("Settle") || !h.tt.HasText("Manager actions") {
		t.Fatalf("pointed at, the Manager's row shows %q", h.tt.Texts())
	}
	h.rightClick("The coordinator")
	menu := h.tt.Menu()
	for _, want := range []string{"Rename", "Run sweep now", "Sweep schedule", "Delete…"} {
		if !slices.Contains(menu, want) {
			t.Errorf("the Manager's menu %q has no %q", menu, want)
		}
	}
	for _, not := range []string{"Snooze", "Mark settled", "Back to inbox", "Fork", "Continue on"} {
		if slices.Contains(menu, not) {
			t.Errorf("the Manager's menu %q offers %q", menu, not)
		}
	}
	h.tt.CloseMenu()
	for _, args := range []map[string]string{
		{"state": "snoozed", "until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
		{"state": "settled", "animation": "settle"},
	} {
		args["thread"] = id.String()
		raw, _ := json.Marshal(args)
		if _, err := h.a.setDispositionEffect(raw); err == nil {
			t.Errorf("a plugin set the Manager %s", args["state"])
		}
	}
	if d := h.local().Threads[id].View.Disposition; d.State != api.Open {
		t.Fatalf("the Manager is %+v", d)
	}
}

// New manager makes a Manager of the name, project, cadence and brief the
// user gave, shows it, and opens its overview.
func TestManagerDialogMakesAManagerAndOpensItsOverview(t *testing.T) {
	ws := t.TempDir()
	h, _ := managerFixture(t, ws, nil)
	settlePlugins(t, h.a, h.tt, h.p)
	h.click("New manager")
	h.until("the dialog", func() bool { h.tt.Frame(); return h.tt.HasText("Create manager") })
	h.click("Project " + filepath.Base(ws))
	h.tt.Frame()
	if d := h.a.mgr.dialog; d.name != filepath.Base(ws)+" Manager" {
		t.Errorf("on the project, the dialog names the Manager %q", d.name)
	}
	h.click("Manager name")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("Release Captain")
	h.click("Sweep every 1h")
	h.click("Standing sweep prompt")
	const brief = "Watch the release branch"
	h.tt.Type(brief)
	h.tt.Frame()
	h.click("Create manager")
	var mgr thread
	h.until("the new Manager", func() bool {
		for _, th := range h.local().Threads {
			if th.View.Title == "Release Captain" && th.View.Role == api.RoleManager && th.View.Revision > 0 {
				mgr = thread{h.local(), th}
				return true
			}
		}
		return false
	})
	if s := mgr.View.Sweep; s == nil || s.IntervalSeconds != 3600 || s.Prompt != brief {
		t.Errorf("the new Manager sweeps %+v", s)
	}
	if mgr.View.Workspace != ws {
		t.Errorf("the new Manager runs in %q, want the project %q", mgr.View.Workspace, ws)
	}
	h.until("its overview", func() bool { h.tt.Frame(); return h.tt.HasText("WORKERS") && h.tt.HasText("SHARED CONTEXT") })
	if h.a.selected != mgr.id() || !h.a.managerPanelShown(h.a.current()) || h.a.mgr.dialog.open {
		t.Fatalf("after Create the app shows %s, the overview %v, the dialog %v", h.a.selected, h.a.managerPanelShown(h.a.current()), h.a.mgr.dialog.open)
	}
	if !h.tt.HasText("Manager for " + filepath.Base(ws)) {
		t.Errorf("the new Manager doesn't welcome: %q", h.tt.Texts())
	}
	if h.tt.HasText("Send a message to start the conversation.") {
		t.Errorf("the new Manager shows a plain thread's greeting")
	}
}

// The header's sweep pill opens a popover whose cadences keep the
// Manager's prompt, through Off too, and whose Run sweep now sweeps.
func TestManagerSweepPopoverKeepsThePromptAndSweeps(t *testing.T) {
	dir := tempDir(t)
	installOwn(t, dir, "managers")
	h := start(t, dir)
	h.grantManagers()
	const prompt = "Only check release blockers"
	id := h.a.backend.CreateManager(h.local(), t.TempDir(), "Manager", fakeModel, api.AccessAsk, &api.SweepSchedule{IntervalSeconds: 14400, Prompt: prompt})
	h.until("the Manager", func() bool { th := h.local().Threads[id]; return th != nil && th.View.Revision > 0 })
	h.a.select_(thread{h.local(), h.local().Threads[id]})
	sweep := func() *api.SweepSchedule { return h.local().Threads[id].View.Sweep }
	h.clickHeaderPlugin("Sweep 4h")
	h.until("the popover", func() bool { h.tt.Frame(); return h.tt.HasText("Sweep schedule") })
	for _, step := range []struct {
		click  string
		secs   int64
		header string
	}{{"Sweep every 1h", 3600, "Sweep 1h"}, {"Sweeps off", 0, "Sweep off"}, {"Sweep every 15m", 900, "Sweep 15m"}} {
		h.clickPlugin(step.click)
		h.until(step.header, func() bool {
			h.tt.Frame()
			s := sweep()
			_, in := h.inHeader(step.header)
			return s != nil && s.IntervalSeconds == step.secs && in
		})
		if s := sweep(); s.Prompt != prompt {
			t.Fatalf("after %s the prompt is %q, want %q", step.click, s.Prompt, prompt)
		}
	}
	h.clickPlugin("Run sweep now")
	h.until("a sweep", func() bool { return !h.local().Threads[id].View.LastSweep.IsZero() })
	h.until("popover closed", func() bool { h.tt.Frame(); return !h.tt.HasText("Sweep schedule") })
}

func TestManagerSweepPromptEndingInCompactShowsHeaderPills(t *testing.T) {
	dir := tempDir(t)
	installOwn(t, dir, "managers")
	h := start(t, dir)
	h.grantManagers()
	id := h.a.backend.CreateManager(h.local(), t.TempDir(), "Compact Prompt Coordinator", fakeModel, api.AccessAsk, &api.SweepSchedule{IntervalSeconds: 1800, Prompt: "Keep output compact"})
	h.until("the Manager", func() bool { th := h.local().Threads[id]; return th != nil && th.View.Revision > 0 })
	h.a.select_(thread{h.local(), h.local().Threads[id]})
	h.until("the Manager's pills", func() bool {
		h.tt.Frame()
		_, m := h.inHeader("Manager")
		_, s := h.inHeader("Sweep 30m")
		_, o := h.inHeader("Overview")
		return m && s && o
	})
}

// The overview lists what needs the user, the workers and the shared
// docs, read off the UI thread, and makes a missing WORKERS.md.
func TestManagerOverviewShowsWorkersDocsAndWhatNeedsYou(t *testing.T) {
	const agents = "# Agents\nBe brief.\n"
	ws := t.TempDir()
	h, id := managerFixture(t, ws, map[string]string{"AGENTS.md": agents}, "Worker one", "Worker two")
	mgr := thread{h.local(), h.local().Threads[id]}
	h.a.select_(mgr)
	h.until("the overview's button", func() bool { h.tt.Frame(); return h.tt.HasText("Overview") })
	h.clickPlugin("Overview")
	h.until("the overview", func() bool {
		h.tt.Frame()
		return h.tt.HasText("WORKERS") && h.tt.HasText("AGENTS.md") && h.tt.HasText("Worker two")
	})
	otherID := h.a.backend.CreateManager(h.local(), ws, "Other Manager", fakeModel, api.AccessAsk, &api.SweepSchedule{IntervalSeconds: 1800})
	h.until("other manager", func() bool { th := h.local().Threads[otherID]; return th != nil && th.View.Revision > 0 })
	if got := h.a.threadRecord(mgr).Manager; got == nil || !got.OverviewOpen {
		t.Fatalf("selected Manager record OverviewOpen = %+v, want true", got)
	}
	if got := h.a.threadRecord(thread{h.local(), h.local().Threads[otherID]}).Manager; got == nil || got.OverviewOpen {
		t.Fatalf("unselected Manager record OverviewOpen = %+v, want false", got)
	}
	h.until("the docs", func() bool {
		h.tt.Frame()
		return h.tt.HasText("Not created") && h.tt.HasText(fileSize(int64(len(agents))))
	})
	if h.tt.HasText("Needs you") {
		t.Fatalf("nothing waits, yet the overview says %q", h.tt.Texts())
	}

	h.click("Create WORKERS.md")
	h.until("WORKERS.md", func() bool {
		_, err := os.Stat(filepath.Join(ws, "WORKERS.md"))
		h.tt.Frame()
		return err == nil && !slices.Contains(h.tt.Texts(), "Create WORKERS.md")
	})

	// A worker that failed needs the user; a click shows it.
	k := h.thread("Worker one")
	k.View.LastTurn = &api.TurnResult{Outcome: api.OutcomeFailed, Ended: time.Now()}
	h.until("Needs you", func() bool { h.tt.Frame(); return h.tt.HasText("Needs you") && h.tt.HasText("Failed") })
	h.click("Failed")
	h.until("the worker", func() bool { h.tt.Frame(); return h.a.selected == k.id() })
}

func TestManagerSweepEditsComposeAndPreserveDraft(t *testing.T) {
	ws := t.TempDir()
	h, id := managerFixture(t, ws, nil)
	mgr := thread{h.local(), h.local().Threads[id]}
	h.a.select_(mgr)
	h.a.openOverview(mgr)
	h.tt.Frame()

	// Simulate an in-flight cadence change (30m -> 1h) before the view updates.
	mgr.Thread.Pending = append(mgr.Thread.Pending, client.Pending{
		Command: api.NewID(),
		Body:    api.ThreadConfigure{Sweep: &api.SweepSchedule{IntervalSeconds: 3600}},
	})
	if got := sweepSeconds(effectiveSweep(mgr)); got != 3600 {
		t.Fatalf("effectiveSweep = %d, want 3600", got)
	}

	// Unsaved draft in Overview survives opening the header sweep popover.
	const draft = "Check nightly failures first"
	h.a.mgr.promptFor = id
	h.a.mgr.promptDraft = draft
	h.clickHeaderPlugin("Sweep 1h")
	h.until("sweep popover open", func() bool { h.tt.Frame(); return h.tt.HasText("Sweep schedule") })
	if h.a.mgr.promptDraft != draft {
		t.Fatalf("opening sweep popover wiped draft: got %q, want %q", h.a.mgr.promptDraft, draft)
	}

	// Saving the prompt in Overview composes with the pending 1h cadence rather than reverting to 30m.
	h.clickHeaderPlugin("Sweep 1h")
	h.until("sweep popover closed", func() bool { h.tt.Frame(); return !h.tt.HasText("Sweep schedule") })
	h.click("Save prompt")
	h.until("composed sweep saved", func() bool {
		s := h.local().Threads[id].View.Sweep
		return s != nil && s.IntervalSeconds == 3600 && s.Prompt == draft
	})

	// Clear the synthetic pending entry and verify an untouched Overview draft refreshes when saved elsewhere.
	mgr.Thread.Pending = nil
	h.tt.Frame()
	const headerPrompt = "Saved from header popover"
	h.a.backend.SetSweep(h.local(), id, &api.SweepSchedule{IntervalSeconds: 3600, Prompt: headerPrompt})
	h.until("header prompt persisted", func() bool {
		s := h.local().Threads[id].View.Sweep
		return s != nil && s.Prompt == headerPrompt
	})
	h.tt.Frame()
	if h.a.mgr.promptDraft != headerPrompt {
		t.Fatalf("untouched Overview draft = %q, want %q", h.a.mgr.promptDraft, headerPrompt)
	}
}

func TestManagerNeedsYouIncludesFailedManagerAndRemoteWaitingWorker(t *testing.T) {
	ws := t.TempDir()
	h, id := managerFixture(t, ws, nil, "Remote worker")
	mgr := thread{h.local(), h.local().Threads[id]}
	worker := h.thread("Remote worker")

	mgr.View.LastTurn = &api.TurnResult{Outcome: api.OutcomeFailed, Ended: time.Now()}
	worker.View.Run = api.RunWaiting
	worker.View.Requests = nil
	worker.View.Remote = &api.Placement{Engine: api.NewID(), Link: api.NewID()}

	h.a.select_(mgr)
	h.a.openOverview(mgr)
	h.tt.Frame()

	needs, active := h.a.managerAttention(mgr)
	if needs != 2 || active != 1 {
		t.Fatalf("managerAttention = (%d, %d), want (2, 1)", needs, active)
	}
	if !h.tt.HasText("Needs you") || !h.tt.HasText("Failed") || !h.tt.HasText("Waits on you") {
		t.Fatalf("overview missing failed manager or remote waiting worker: %q", h.tt.Texts())
	}
}

func TestManagerDialogKeepsActionsOnscreenWithManyProjects(t *testing.T) {
	h := start(t, tempDir(t))
	for i := range 48 {
		ws := fmt.Sprintf("/work/project-%02d", i)
		h.a.projects = append(h.a.projects, &project{env: h.local(), key: keyOf(h.local(), ws)})
	}
	h.tt.SetSize(1000, 700)
	h.a.openManagerDialog(h.local(), projectKey{})
	h.tt.Frame()
	r, ok := h.tt.Find("Create manager")
	if !ok {
		t.Fatalf("Create manager button missing: %q", h.tt.Texts())
	}
	if r.Y < 0 || r.Y+r.H > 700 {
		t.Fatalf("Create manager button pushed offscreen: rect=%+v winH=700", r)
	}
}

func TestManagersSectionOffersContinueAllForInterruptedManager(t *testing.T) {
	h, id := managerFixture(t, "/work/mygo", nil)
	settlePlugins(t, h.a, h.tt, h.p)
	mgr := h.local().Threads[id]
	mgr.View.Run = api.RunUncertain
	mgr.View.Turn = &api.Turn{Run: api.NewID(), Model: fakeModel}
	h.tt.Frame()

	if !h.tt.HasText("Continue all") {
		t.Fatalf("Managers header missing Continue all for interrupted Manager: %q", h.tt.Texts())
	}
	h.click("Continue all")
	h.tt.Frame()
	if !h.a.continuingAlready(thread{h.local(), mgr}) {
		t.Fatalf("clicking Continue all did not enqueue interrupted Manager")
	}
}

func TestManagerSweepTimingAndDocsTotalCap(t *testing.T) {
	now := time.Unix(20_000, 0).UTC()
	resumable := thread{Thread: &client.Thread{View: api.Thread{
		Role:      api.RoleManager,
		Sweep:     &api.SweepSchedule{IntervalSeconds: 900},
		Resumable: true,
	}}}
	if got := sweepTiming(resumable, now); !strings.Contains(got, "Paused while queued work waits") {
		t.Fatalf("sweepTiming(resumable) = %q, want paused message", got)
	}
	if !sweepBlocked(resumable) {
		t.Fatalf("sweepBlocked(resumable) = false, want true")
	}

	limited := thread{Thread: &client.Thread{View: api.Thread{
		Role:  api.RoleManager,
		Sweep: &api.SweepSchedule{IntervalSeconds: 900},
		LastTurn: &api.TurnResult{
			Outcome: api.OutcomeFailed,
			Ended:   now.Add(-time.Minute),
			Limit:   &api.UsageLimit{Resets: now.Add(20 * time.Minute)},
		},
	}}}
	if got := sweepTiming(limited, now); !strings.Contains(got, "Waiting on usage limit") {
		t.Fatalf("sweepTiming(limited) = %q, want usage limit message", got)
	}

	dir := t.TempDir()
	chunk := strings.Repeat("a", managerDocLimit)
	for _, name := range managerDocNames {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(chunk), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	docs, err := readManagerDocs(dir)
	if err != nil {
		t.Fatalf("readManagerDocs: %v", err)
	}
	if len(docs) != 3 || docs[0].omitted || docs[1].omitted || !docs[2].omitted {
		t.Fatalf("docs omitted flags = [%v, %v, %v], want [false, false, true]", docs[0].omitted, docs[1].omitted, docs[2].omitted)
	}
}

// The Manager's row and the Managers' count say Needs you for what the
// Manager asks or failed, and for a worker the user took over; what its
// other workers ask or fail is the Manager's to vet, which its overview
// still shows.
func TestTheManagersRowNeedsYouOnlyForWhatReachesTheUser(t *testing.T) {
	h, id := managerFixture(t, t.TempDir(), nil, "Worker one")
	mgr := thread{h.local(), h.local().Threads[id]}
	worker := h.thread("Worker one")
	row := func() bool {
		h.tt.Advance(time.Second)
		return managerNeedsUser(mgr, h.a.below(mgr)) && h.tt.HasText("Needs you")
	}
	if row() {
		t.Fatalf("nothing waits, yet the row says %q", h.tt.Texts())
	}
	worker.View.LastTurn = &api.TurnResult{Outcome: api.OutcomeFailed, Ended: time.Now()}
	worker.View.Requests = []api.Request{{ID: api.NewID(), Kind: api.KindApproval}}
	if row() {
		t.Fatalf("a worker's unvetted ask and failure show on the Manager's row: %q", h.tt.Texts())
	}
	if needs, _ := h.a.managerAttention(mgr); needs != 1 {
		t.Fatalf("the overview counts %d needing the user, want the worker", needs)
	}
	worker.View.TakenOver = true
	if !row() {
		t.Fatalf("a worker the user took over asks, yet the row says %q", h.tt.Texts())
	}
	// A taken-over worker that failed and is now idle is hidden from the
	// sidebar's child list, yet still surfaces Needs you on the Manager row.
	worker.View.Requests = nil
	if len(h.a.shown(mgr)) != 0 {
		t.Fatalf("idle failed worker still in shown(mgr): %v", h.a.shown(mgr))
	}
	if !row() {
		t.Fatalf("a taken-over failed idle worker lost Needs you on the Manager's row: %q", h.tt.Texts())
	}
	worker.View.TakenOver = false
	mgr.View.Requests = []api.Request{{ID: api.NewID(), Kind: api.KindQuestion}}
	if !row() {
		t.Fatalf("the Manager asks, yet the row says %q", h.tt.Texts())
	}
}

func TestWorkerActiveClassification(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		view api.Thread
		want bool
	}{
		{"starting", api.Thread{Run: api.RunStarting}, true},
		{"running", api.Thread{Run: api.RunRunning}, true},
		{"waiting", api.Thread{Run: api.RunWaiting}, true},
		{"stopping", api.Thread{Run: api.RunStopping}, true},
		{"uncertain", api.Thread{Run: api.RunUncertain}, true},
		{"idle with pending request", api.Thread{Run: api.RunIdle, Requests: []api.Request{{ID: api.NewID(), Kind: api.KindApproval}}}, true},
		{"idle with queued ready", api.Thread{Run: api.RunIdle, Queued: 1, Resumable: false}, true},
		{"idle with queued paused", api.Thread{Run: api.RunIdle, Queued: 1, Resumable: true}, false},
		{"idle completed", api.Thread{Run: api.RunIdle, LastTurn: &api.TurnResult{Outcome: api.OutcomeCompleted, Ended: now}}, false},
		{"idle stopped", api.Thread{Run: api.RunIdle, LastTurn: &api.TurnResult{Outcome: api.OutcomeInterrupted, Ended: now}}, false},
		{"idle failed", api.Thread{Run: api.RunIdle, LastTurn: &api.TurnResult{Outcome: api.OutcomeFailed, Ended: now}}, false},
		{"idle abandoned", api.Thread{Run: api.RunIdle, LastTurn: &api.TurnResult{Outcome: api.OutcomeAbandoned, Ended: now}}, false},
		{"idle fresh", api.Thread{Run: api.RunIdle}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := thread{Thread: &client.Thread{View: tc.view}}
			if got := workerActive(k); got != tc.want {
				t.Errorf("workerActive(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestManagerHidesDoneWorkersFromSidebarAndPillButKeepsOverview(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	ws := t.TempDir()
	plainID := seed(t, d, ws, "Plain parent")
	seedChild(t, d, plainID, "Plain done child", "do it")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mgrID := api.NewID()
	if _, err := d.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: mgrID, Body: api.ThreadCreate{
		Workspace: ws, Title: "The coordinator", Model: fakeModel, Role: api.RoleManager,
		Sweep: &api.SweepSchedule{IntervalSeconds: 1800},
	}}); err != nil {
		t.Fatal(err)
	}
	seedChild(t, d, mgrID, "Completed worker", "do it")
	seedChild(t, d, mgrID, "Stopped worker", "do it")
	seedChild(t, d, mgrID, "Failed worker", "do it")

	h := start(t, dir)
	mgr := h.thread("The coordinator")
	plain := h.thread("Plain parent")
	h.thread("Plain done child")
	wDone := h.thread("Completed worker")
	wStop := h.thread("Stopped worker")
	wFail := h.thread("Failed worker")

	now := time.Now()
	wDone.View.Run, wDone.View.LastTurn = api.RunIdle, &api.TurnResult{Outcome: api.OutcomeCompleted, Ended: now}
	wStop.View.Run, wStop.View.LastTurn = api.RunIdle, &api.TurnResult{Outcome: api.OutcomeInterrupted, Ended: now}
	wStop.View.Queued, wStop.View.Resumable = 1, true // paused queued work counts as done
	wFail.View.Run, wFail.View.LastTurn = api.RunIdle, &api.TurnResult{Outcome: api.OutcomeFailed, Ended: now}

	h.a.select_(mgr)
	h.a.childrenOpen[mgr.id()] = true
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)

	// 1. All done workers are hidden from sidebar and Workers pill.
	if got := h.a.shown(mgr); len(got) != 0 {
		t.Fatalf("shown(mgr) with only done workers = %d, want 0", len(got))
	}
	if got := h.a.below(mgr); len(got) != 3 {
		t.Fatalf("below(mgr) = %d, want 3", len(got))
	}
	if h.a.agentsRoom(mgr) != 0 || h.a.hasAbove(mgr) {
		t.Fatalf("done-only Manager still reserves composer strip: agentsRoom=%v hasAbove=%v", h.a.agentsRoom(mgr), h.a.hasAbove(mgr))
	}
	if _, ok := h.tt.Find("Workers"); ok {
		t.Fatalf("Workers pill shown when all workers are done: %q", h.tt.Texts())
	}
	for _, name := range []string{"Completed worker", "Stopped worker", "Failed worker"} {
		if r, ok := h.tt.Find(name); ok && r.X < h.a.sidebarWidth {
			t.Fatalf("done worker %q still rendered in sidebar at %+v", name, r)
		}
	}
	if r, ok := h.tt.Find("3 child threads"); ok && r.X < h.a.sidebarWidth {
		t.Fatalf("Manager row still shows child toggle for done workers at %+v", r)
	}

	// 2. Overview still lists all done workers, Needs you still flags failed,
	//    and clicking a done worker opens it with breadcrumb back to Manager.
	if needs, active := h.a.managerAttention(mgr); needs != 1 || active != 0 {
		t.Fatalf("managerAttention = (%d, %d), want (1, 0)", needs, active)
	}
	h.a.openOverview(mgr)
	h.tt.Advance(time.Second)
	if !h.tt.HasText("0 active · 3 total") || !h.tt.HasText("Needs you") {
		t.Fatalf("overview missing done-worker summary or Needs you: %q", h.tt.Texts())
	}
	for _, name := range []string{"Completed worker", "Stopped worker", "Failed worker"} {
		if !h.tt.HasText(name) {
			t.Fatalf("overview missing done worker %q: %q", name, h.tt.Texts())
		}
	}
	h.click("Completed worker")
	h.tt.Advance(time.Second)
	if h.a.selected != wDone.id() {
		t.Fatalf("clicking done worker in Overview selected %s, want %s", h.a.selected, wDone.id())
	}
	if _, ok := h.tt.Find("Back to The coordinator"); !ok {
		t.Fatalf("done worker thread missing breadcrumb back to Manager: %q", h.tt.Texts())
	}

	// 3. Active workers (running, waiting, queued) appear in sidebar and
	//    Workers pill/panel, while done workers stay excluded.
	h.a.select_(mgr)
	h.a.mgr.showPanel = false
	wDone.View.Run = api.RunRunning
	wStop.View.Run, wStop.View.Queued, wStop.View.Resumable = api.RunIdle, 1, false // queued to run
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)

	if got := h.a.shown(mgr); len(got) != 2 {
		t.Fatalf("shown(mgr) with 2 active + 1 failed = %d, want 2", len(got))
	}
	if r, ok := h.tt.Find("2 child threads"); !ok || r.X >= h.a.sidebarWidth {
		t.Fatalf("Manager row missing 2-active child toggle in sidebar: %q", h.tt.Texts())
	}
	for _, name := range []string{"Completed worker", "Stopped worker"} {
		if r, ok := h.tt.Find(name); !ok || r.X >= h.a.sidebarWidth {
			t.Fatalf("active worker %q missing from expanded Manager sidebar: %q", name, h.tt.Texts())
		}
	}
	if r, ok := h.tt.Find("Failed worker"); ok && r.X < h.a.sidebarWidth {
		t.Fatalf("done failed worker %q leaked into expanded Manager sidebar", "Failed worker")
	}
	if h.a.agentsRoom(mgr) == 0 || !h.a.hasAbove(mgr) {
		t.Fatalf("active Manager workers missing composer strip room")
	}

	// Open the Workers panel (exercising agents.body built-in plugin too).
	h.click("Workers")
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	if !h.a.agentsOpen[mgr.id()] || h.a.agentsK != 1 {
		t.Fatalf("Workers panel did not open: open=%v k=%v", h.a.agentsOpen[mgr.id()], h.a.agentsK)
	}
	h.a.agentsAll[mgr.id()] = true
	// Collapse sidebar children and check only the Workers panel's rows
	// (everything after "Close agents" in the overlay, not timeline report cards).
	h.a.childrenOpen[mgr.id()] = false
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	panelRows := func() []string {
		texts := h.tt.Texts()
		i := slices.Index(texts, "Close agents")
		if i < 0 {
			return nil
		}
		return texts[i+1:]
	}
	rows := panelRows()
	if !slices.Contains(rows, "Completed worker") || !slices.Contains(rows, "Stopped worker") {
		t.Fatalf("Workers panel missing active workers: %q", rows)
	}
	if slices.Contains(rows, "Failed worker") {
		t.Fatalf("Workers panel listed done worker: %q", rows)
	}
	if app := h.a.plugins.App("agents"); app == nil || app.PlaceView("body", mgr.id().String()) == nil {
		t.Fatalf("built-in agents plugin did not draw Workers panel body")
	} else if items := app.PlaceView("body", mgr.id().String()).Items; len(items) != 2 {
		t.Fatalf("agents.body plugin items = %d, want 2 active workers", len(items))
	}

	// 4. Last active workers finish while Workers panel is open: panel closes,
	//    open/all/K state resets, pill disappears, and a later worker starts collapsed.
	wDone.View.Run = api.RunIdle
	wStop.View.Queued = 0
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if h.a.agentsOpen[mgr.id()] || h.a.agentsAll[mgr.id()] || h.a.agentsK != 0 {
		t.Fatalf("finishing last worker left panel state dirty: open=%v all=%v k=%v",
			h.a.agentsOpen[mgr.id()], h.a.agentsAll[mgr.id()], h.a.agentsK)
	}
	if h.a.agentsRoom(mgr) != 0 || h.a.hasAbove(mgr) {
		t.Fatalf("composer strip room remained after last worker finished: room=%v hasAbove=%v", h.a.agentsRoom(mgr), h.a.hasAbove(mgr))
	}
	if _, ok := h.tt.Find("Workers"); ok {
		t.Fatalf("Workers pill remained after last worker finished: %q", h.tt.Texts())
	}

	// Later, a new worker becomes active: pill appears collapsed (not popped open).
	wDone.View.Run = api.RunWaiting
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if h.a.agentsOpen[mgr.id()] || h.a.agentsK != 0 {
		t.Fatalf("new worker unexpectedly popped Workers panel open: open=%v k=%v", h.a.agentsOpen[mgr.id()], h.a.agentsK)
	}
	if _, ok := h.tt.Find("Workers"); !ok {
		t.Fatalf("Workers pill did not return for newly active worker: %q", h.tt.Texts())
	}

	// 5. Plain (non-Manager) thread children still appear when done.
	h.a.select_(plain)
	h.a.childrenOpen[plain.id()] = true
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	if got := h.a.shown(plain); len(got) != 1 {
		t.Fatalf("shown(plain) = %d, want 1 done child", len(got))
	}
	if _, ok := h.tt.Find("Agents"); !ok {
		t.Fatalf("plain thread lost Agents pill for done child: %q", h.tt.Texts())
	}
	if r, ok := h.tt.Find("Plain done child"); !ok || r.X >= h.a.sidebarWidth {
		t.Fatalf("plain thread lost done child in sidebar: %q", h.tt.Texts())
	}
}

func TestManagerKeepsDoneAncestorVisibleForActiveGrandchild(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	ws := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mgrID := api.NewID()
	if _, err := d.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: mgrID, Body: api.ThreadCreate{
		Workspace: ws, Title: "The coordinator", Model: fakeModel, Role: api.RoleManager,
		Sweep: &api.SweepSchedule{IntervalSeconds: 1800},
	}}); err != nil {
		t.Fatal(err)
	}
	w1ID := seedChild(t, d, mgrID, "Parent worker", "do it")
	seedChild(t, d, w1ID, "Active grandchild", "do it")
	seedChild(t, d, w1ID, "Done grandchild", "do it")
	w2ID := seedChild(t, d, mgrID, "Done branch worker", "do it")
	seedChild(t, d, w2ID, "Done branch leaf", "do it")

	h := start(t, dir)
	mgr := h.thread("The coordinator")
	w1 := h.thread("Parent worker")
	gActive := h.thread("Active grandchild")
	h.thread("Done grandchild")
	h.thread("Done branch worker")
	h.thread("Done branch leaf")

	gActive.View.Run = api.RunRunning
	h.a.select_(mgr)
	h.a.childrenOpen[mgr.id()] = true
	h.a.childrenOpen[w1.id()] = true
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)

	shownMgr := h.a.shown(mgr)
	if len(shownMgr) != 2 || shownMgr[0].t.id() != w1.id() || shownMgr[0].depth != 1 || shownMgr[1].t.id() != gActive.id() || shownMgr[1].depth != 2 {
		t.Fatalf("shown(mgr) = %+v, want [Parent worker(1), Active grandchild(2)]", shownMgr)
	}
	if shownW1 := h.a.shown(w1); len(shownW1) != 1 || shownW1[0].t.id() != gActive.id() {
		t.Fatalf("shown(w1) = %+v, want [Active grandchild]", shownW1)
	}
	if r, ok := h.tt.Find("Parent worker"); !ok || r.X >= h.a.sidebarWidth {
		t.Fatalf("done ancestor worker hidden from sidebar: %q", h.tt.Texts())
	}
	if r, ok := h.tt.Find("Active grandchild"); !ok || r.X >= h.a.sidebarWidth {
		t.Fatalf("active grandchild hidden from sidebar: %q", h.tt.Texts())
	}
	for _, hidden := range []string{"Done grandchild", "Done branch worker", "Done branch leaf"} {
		if r, ok := h.tt.Find(hidden); ok && r.X < h.a.sidebarWidth {
			t.Fatalf("done worker %q leaked into sidebar", hidden)
		}
	}

	// Workers panel lists the ancestor chain and active grandchild only.
	h.click("Workers")
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	h.a.childrenOpen[mgr.id()] = false
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	texts := h.tt.Texts()
	ci := slices.Index(texts, "Close agents")
	if ci < 0 {
		t.Fatalf("Workers panel not open: %q", texts)
	}
	panelRows := texts[ci+1:]
	for _, want := range []string{"Parent worker", "Active grandchild"} {
		if !slices.Contains(panelRows, want) {
			t.Fatalf("Workers panel missing %q: %q", want, panelRows)
		}
	}
	for _, hidden := range []string{"Done grandchild", "Done branch worker", "Done branch leaf"} {
		if slices.Contains(panelRows, hidden) {
			t.Fatalf("Workers panel listed done worker %q: %q", hidden, panelRows)
		}
	}

	// Once the grandchild finishes, the whole chain disappears from sidebar and pill,
	// while Overview still shows all 5 workers.
	gActive.View.Run = api.RunIdle
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if len(h.a.shown(mgr)) != 0 || len(h.a.shown(w1)) != 0 {
		t.Fatalf("finished chain still shown: mgr=%d w1=%d", len(h.a.shown(mgr)), len(h.a.shown(w1)))
	}
	if _, ok := h.tt.Find("Workers"); ok {
		t.Fatalf("Workers pill remained after grandchild finished")
	}
	h.a.openOverview(mgr)
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if !h.tt.HasText("0 active · 5 total") {
		t.Fatalf("overview lost workers after grandchild finished: %q", h.tt.Texts())
	}
}
