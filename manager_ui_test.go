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
	if !canSteer(dismissed) {
		t.Fatal("canSteer returned false for a dismissed Manager wake")
	}
	compacting := api.Thread{
		Run:  api.RunRunning,
		Turn: &api.Turn{Run: api.NewID(), Silent: true},
	}
	if canSteer(compacting) {
		t.Fatal("canSteer returned true for a silent compaction run")
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
