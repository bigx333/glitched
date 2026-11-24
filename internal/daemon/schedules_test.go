package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/bundled"
	"glitched/internal/plugins/pack"
	"glitched/internal/provider"
	"glitched/internal/provider/codex/codextest"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// tick is how often the Schedules service wakes in these tests: its minute
// scaled to 300 ms.
const tick = 0.005

// installSchedules installs the built-in Schedules in dir as the user's own
// copy, signed with the device's key, as the release key signs it only
// once sign-bundled runs, with the grants a built-in starts with.
func installSchedules(t *testing.T, dir string) {
	t.Helper()
	sub, err := fs.Sub(bundled.FS, "schedules")
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(plugins.Dir(dir), "schedules")
	if err := os.CopyFS(dst, sub); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dst, pack.SignatureFile))
	key, err := pack.LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(dst, key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugins.json"), []byte(`{"grants": {"schedules": ["service", "threads:read", "threads:write", "threads:access"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// schedules calls the Schedules service's method over the engine's socket.
func schedules(t *testing.T, ep *transport.EndpointClient, method string, args any, out any) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"plugin": "schedules", "method": method, "args": args})
	b, err := ep.PluginCall(context.Background(), raw)
	if err != nil {
		t.Fatalf("schedules.%s: %v", method, err)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("schedules.%s: %s: %v", method, b, err)
		}
	}
}

type savedSchedule struct {
	ID, Missed, Anchor, NextDue, LastThread string
}

type scheduleRun struct{ Due, Key, Thread, State, Reason string }

// saveSchedule saves a schedule of the prompt in ws with fields over it.
func saveSchedule(t *testing.T, ep *transport.EndpointClient, ws string, fields map[string]any) savedSchedule {
	t.Helper()
	s := map[string]any{"title": "Nightly", "prompt": "Check the build.", "workspace": ws, "provider": "codex", "model": "gpt-6-luna", "effort": "low"}
	for k, v := range fields {
		s[k] = v
	}
	var out savedSchedule
	schedules(t, ep, "save", map[string]any{"schedule": s}, &out)
	return out
}

// runsOnce are id's runs once one is no longer starting, waited for.
func runsOnce(t *testing.T, ep *transport.EndpointClient, id string, done func([]scheduleRun) bool) []scheduleRun {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var runs []scheduleRun
		schedules(t, ep, "runs", map[string]any{"id": id}, &runs)
		if done(runs) {
			return runs
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting on the runs of %s: %+v", id, runs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// settled is whether no run is starting, and there is one.
func settled(runs []scheduleRun) bool {
	if len(runs) == 0 {
		return false
	}
	for _, r := range runs {
		if r.State == "starting" {
			return false
		}
	}
	return true
}

// pluginDB is the Schedules service's own glitched.db in dir, which a test
// writes as the engine was down: for a crash, or the minutes it missed.
func pluginDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(plugins.DataDir(dir), "schedules", "plugin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func write(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// list is the schedules' first page, which here is all of them.
func list(t *testing.T, ep *transport.EndpointClient) []savedSchedule {
	t.Helper()
	var page struct{ Schedules []savedSchedule }
	schedules(t, ep, "list", nil, &page)
	return page.Schedules
}

// unsettled makes the service's record of a started run fail, as a crash
// right after its create would leave it: the run stays starting.
const unsettled = "CREATE TRIGGER unsettled BEFORE UPDATE OF state ON runs WHEN NEW.state = 'started' BEGIN SELECT RAISE(ABORT, 'crash'); END"

// at is t as the service writes times.
func at(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func threads(t *testing.T, ep *transport.EndpointClient) []api.Thread {
	t.Helper()
	snap, err := ep.Read(context.Background(), api.Query{})
	if err != nil {
		t.Fatal(err)
	}
	return snap.Threads
}

// An interval schedule that missed three occurrences while the engine was
// down runs once when it is back, by default: for the latest of them, and
// next at the one after now.
func TestAnOverdueScheduleRunsOnceForItsMissedOccurrences(t *testing.T) {
	fakeCodex(t)
	dir, ws := stateDir(t), t.TempDir()
	installSchedules(t, dir)
	_, ep, stop := codexAt(t, dir, tick)
	s := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 5})
	if s.Missed != "once" || s.NextDue == "" {
		t.Fatalf("saved %+v, want a missed run to run once, and a next run", s)
	}
	stop()

	anchor := time.Now().Add(-17 * time.Minute).Truncate(time.Second)
	write(t, pluginDB(t, dir), "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", at(anchor), at(anchor.Add(5*time.Minute)), s.ID)
	_, ep, _ = codexAt(t, dir, tick)
	runs := runsOnce(t, ep, s.ID, settled)
	if len(runs) != 1 || runs[0].State != "started" || runs[0].Due != at(anchor.Add(15*time.Minute)) {
		t.Fatalf("runs %+v, want one started for %s, the latest missed", runs, at(anchor.Add(15*time.Minute)))
	}
	if th := threads(t, ep); len(th) != 1 || th[0].ID.String() != runs[0].Thread {
		t.Fatalf("threads %+v, want the run's %s alone", th, runs[0].Thread)
	}
	_, queued, _, _ := tally(endedLog(t, ep, api.ID(mustID(t, runs[0].Thread))))
	if len(queued) != 1 || queued[0].Text != "Check the build." {
		t.Fatalf("the run's messages %+v", queued)
	}
	if l := list(t, ep); len(l) != 1 || l[0].NextDue != at(anchor.Add(20*time.Minute)) || l[0].LastThread != runs[0].Thread {
		t.Fatalf("the schedule %+v, want next at %s and its last thread the run's", l, at(anchor.Add(20*time.Minute)))
	}
}

func mustID(t *testing.T, s string) api.ID {
	t.Helper()
	id, err := api.ParseID(s)
	if err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return id
}

// A schedule that skips missed runs records one it missed by more than ten
// minutes, and starts no thread.
func TestASkippingScheduleRecordsTheRunItMissed(t *testing.T) {
	fakeCodex(t)
	dir, ws := stateDir(t), t.TempDir()
	installSchedules(t, dir)
	_, ep, stop := codexAt(t, dir, tick)
	s := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 60, "missed": "skip"})
	stop()

	anchor := time.Now().Add(-30 * time.Minute).Truncate(time.Second)
	write(t, pluginDB(t, dir), "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", at(anchor), at(anchor), s.ID)
	_, ep, _ = codexAt(t, dir, tick)
	runs := runsOnce(t, ep, s.ID, settled)
	if len(runs) != 1 || runs[0].State != "missed" || runs[0].Due != at(anchor) || runs[0].Thread != "" {
		t.Fatalf("runs %+v, want one missed for %s", runs, at(anchor))
	}
	if th := threads(t, ep); len(th) != 0 {
		t.Fatalf("threads %+v, want none", th)
	}
	if l := list(t, ep); l[0].NextDue != at(anchor.Add(time.Hour)) {
		t.Fatalf("next run at %s, want %s", l[0].NextDue, at(anchor.Add(time.Hour)))
	}
}

// A schedule whose last thread still waits on the user skips its next run,
// and says why.
func TestAScheduleWhoseLastRunGoesOnSkipsTheNext(t *testing.T) {
	fakeCodex(t)
	t.Setenv(codextest.Scenario, "command") // the turn asks to run a command, and waits for the answer
	dir, ws := stateDir(t), t.TempDir()
	installSchedules(t, dir)
	_, ep, stop := codexAt(t, dir, tick)
	s := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 60})
	stop()

	first := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	db := pluginDB(t, dir)
	write(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", at(first), at(first), s.ID)
	_, ep, _ = codexAt(t, dir, tick)
	runs := runsOnce(t, ep, s.ID, settled)
	if len(runs) != 1 || runs[0].State != "started" {
		t.Fatalf("runs %+v, want the first started", runs)
	}
	deadline := time.Now().Add(30 * time.Second)
	for th := threads(t, ep); len(th) != 1 || len(th[0].Requests) == 0; th = threads(t, ep) {
		if time.Now().After(deadline) {
			t.Fatalf("threads %+v, want the run's waiting on its approval", th)
		}
		time.Sleep(20 * time.Millisecond)
	}
	runsOnce(t, ep, s.ID, func([]scheduleRun) bool {
		l := list(t, ep)
		return l[0].LastThread == runs[0].Thread && l[0].NextDue > at(time.Now())
	})

	next := first.Add(time.Minute)
	write(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", at(next), at(next), s.ID)
	runs = runsOnce(t, ep, s.ID, func(rs []scheduleRun) bool { return len(rs) == 2 && settled(rs) })
	if runs[0].Due != at(next) || runs[0].State != "skipped" || runs[0].Reason != "the previous run is still going" {
		t.Fatalf("runs %+v, want the second skipped as the first still goes", runs)
	}
	if th := threads(t, ep); len(th) != 1 {
		t.Fatalf("threads %+v, want the first run's alone", th)
	}
}

// A crash between a run's intent and its create, or between the create
// and the run's record, comes back to one thread with one message and one
// turn: the engine after it sends the run again, under its key.
func TestACrashMidRunStartsOneThread(t *testing.T) {
	fakeCodex(t)
	dir, ws := stateDir(t), t.TempDir()
	installSchedules(t, dir)
	_, ep, stop := codexAt(t, dir, tick)
	s := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 60})
	db := pluginDB(t, dir)
	write(t, db, "CREATE TRIGGER unsent BEFORE UPDATE OF sent ON runs BEGIN SELECT RAISE(ABORT, 'crash'); END")
	anchor := time.Now().Add(-5 * time.Minute).Truncate(time.Second)
	write(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", at(anchor), at(anchor), s.ID)
	runsOnce(t, ep, s.ID, func(rs []scheduleRun) bool { return len(rs) == 1 })
	if th := threads(t, ep); len(th) != 0 {
		t.Fatalf("threads %+v, want none before the create", th)
	}
	stop()

	write(t, db, "DROP TRIGGER unsent")
	write(t, db, unsettled)
	_, ep, stop = codexAt(t, dir, tick)
	for deadline := time.Now().Add(30 * time.Second); len(threads(t, ep)) == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the run sent again made no thread")
		}
	}
	first := threads(t, ep)[0].ID
	endedLog(t, ep, first)
	stop()

	write(t, db, "DROP TRIGGER unsettled")
	_, ep, _ = codexAt(t, dir, tick)
	runs := runsOnce(t, ep, s.ID, settled)
	if len(runs) != 1 || runs[0].State != "started" || runs[0].Due != at(anchor) || runs[0].Thread != first.String() {
		t.Fatalf("runs %+v, want the one run started as %s", runs, first)
	}
	if th := threads(t, ep); len(th) != 1 || th[0].ID != first {
		t.Fatalf("threads %+v, want %s alone", th, first)
	}
	if _, queued, started, _ := tally(threadLog(t, ep, first)); len(queued) != 1 || started != 1 {
		t.Fatalf("the thread has %d messages and %d turns, want one of each", len(queued), started)
	}
}

// A schedule that skips missed runs counts how late it is from the run it
// missed first: a five-minute one down for twelve misses its latest, two
// minutes old, too.
func TestASkippingScheduleCountsFromTheFirstRunItMissed(t *testing.T) {
	fakeCodex(t)
	dir, ws := stateDir(t), t.TempDir()
	installSchedules(t, dir)
	_, ep, stop := codexAt(t, dir, tick)
	s := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 5, "missed": "skip"})
	stop()

	anchor := time.Now().Add(-12 * time.Minute).Truncate(time.Second)
	write(t, pluginDB(t, dir), "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", at(anchor), at(anchor), s.ID)
	_, ep, _ = codexAt(t, dir, tick)
	runs := runsOnce(t, ep, s.ID, settled)
	if len(runs) != 1 || runs[0].State != "missed" || runs[0].Due != at(anchor.Add(10*time.Minute)) {
		t.Fatalf("runs %+v, want the latest, at %s, missed", runs, at(anchor.Add(10*time.Minute)))
	}
	if th := threads(t, ep); len(th) != 0 {
		t.Fatalf("threads %+v, want none", th)
	}
}

// Editing a schedule's title or prompt keeps the run it has due, which the
// next tick starts.
func TestEditingASchedulesTextKeepsItsDueRun(t *testing.T) {
	fakeCodex(t)
	dir, ws := stateDir(t), t.TempDir()
	installSchedules(t, dir)
	_, ep, stop := codexAt(t, dir) // a minute's timer, which doesn't wake during the edit
	s := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 60})
	due := time.Now().Add(-time.Minute).Truncate(time.Second)
	write(t, pluginDB(t, dir), "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", at(due), at(due), s.ID)
	edited := saveSchedule(t, ep, ws, map[string]any{"id": s.ID, "kind": "interval", "every": 60, "title": "Hourly"})
	if edited.NextDue != at(due) {
		t.Fatalf("after the edit next run at %s, want the due %s", edited.NextDue, at(due))
	}
	w := saveSchedule(t, ep, ws, map[string]any{"kind": "weekly", "at": "09:00", "weekdays": []int{2, 3}})
	write(t, pluginDB(t, dir), "UPDATE schedules SET next_due = ? WHERE id = ?", at(due), w.ID)
	if w = saveSchedule(t, ep, ws, map[string]any{"id": w.ID, "kind": "weekly", "at": "09:00", "weekdays": []int{3, 2, 3}}); w.NextDue != at(due) {
		t.Fatalf("the same weekdays in another order moved the next run to %s, from the due %s", w.NextDue, at(due))
	}
	stop()

	_, ep, _ = codexAt(t, dir, tick)
	if runs := runsOnce(t, ep, s.ID, settled); len(runs) != 1 || runs[0].State != "started" || runs[0].Due != at(due) {
		t.Fatalf("runs %+v, want the due run started", runs)
	}
}

// A Run now a crash left starting finishes on the next tick, with the
// thread its create made, though its schedule is off and so never due.
func TestARunNowLeftStartingFinishesOnTheNextTick(t *testing.T) {
	fakeCodex(t)
	dir, ws := stateDir(t), t.TempDir()
	installSchedules(t, dir)
	_, ep, _ := codexAt(t, dir, tick)
	s := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 60, "enabled": false})
	db := pluginDB(t, dir)
	write(t, db, unsettled)
	raw, _ := json.Marshal(map[string]any{"plugin": "schedules", "method": "runNow", "args": map[string]any{"id": s.ID, "op": api.NewID().String()}})
	if _, err := ep.PluginCall(context.Background(), raw); err == nil {
		t.Fatal("Run now answered though its record failed")
	}
	th := threads(t, ep)
	if len(th) != 1 {
		t.Fatalf("threads %+v, want Run now's", th)
	}
	write(t, db, "DROP TRIGGER unsettled")
	runs := runsOnce(t, ep, s.ID, settled)
	if len(runs) != 1 || runs[0].State != "started" || runs[0].Thread != th[0].ID.String() {
		t.Fatalf("runs %+v, want Run now's started as %s", runs, th[0].ID)
	}
	if th := threads(t, ep); len(th) != 1 {
		t.Fatalf("threads %+v, want Run now's alone", th)
	}
}

// A run left starting keeps the thread its create made though its schedule
// is edited before the run is recorded: it is sent again as it was first,
// and counts as a run still going till then.
func TestAnEditBeforeARunIsRecordedKeepsItsThread(t *testing.T) {
	fakeCodex(t)
	t.Setenv(codextest.Scenario, "command") // the turn waits on an approval
	dir, ws := stateDir(t), t.TempDir()
	installSchedules(t, dir)
	_, ep, _ := codexAt(t, dir, tick)
	s := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 60})
	db := pluginDB(t, dir)
	write(t, db, unsettled)
	raw, _ := json.Marshal(map[string]any{"plugin": "schedules", "method": "runNow", "args": map[string]any{"id": s.ID, "op": api.NewID().String()}})
	if _, err := ep.PluginCall(context.Background(), raw); err == nil {
		t.Fatal("Run now answered though its record failed")
	}
	th := threads(t, ep)
	if len(th) != 1 || th[0].Access != api.AccessAsk {
		t.Fatalf("threads %+v, want Run now's, asking for approval", th)
	}
	first := th[0].ID
	saveSchedule(t, ep, ws, map[string]any{"id": s.ID, "kind": "interval", "every": 60, "access": "Full access", "prompt": "Check it all."})

	due := time.Now().Add(-time.Minute).Truncate(time.Second)
	write(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", at(due), at(due), s.ID)
	runs := runsOnce(t, ep, s.ID, func(rs []scheduleRun) bool { return len(rs) == 2 && rs[1].State != "starting" })
	if runs[1].Due != at(due) || runs[1].State != "skipped" || runs[0].State != "starting" {
		t.Fatalf("runs %+v, want the due run skipped while Run now's still starts", runs)
	}
	write(t, db, "DROP TRIGGER unsettled")
	runs = runsOnce(t, ep, s.ID, settled)
	if runs[0].State != "started" || runs[0].Thread != first.String() {
		t.Fatalf("runs %+v, want Run now's started as %s", runs, first)
	}
	if th := threads(t, ep); len(th) != 1 || th[0].ID != first || th[0].Access != api.AccessAsk {
		t.Fatalf("threads %+v, want %s alone, asking for approval", th, first)
	}

	schedules(t, ep, "delete", map[string]any{"id": s.ID}, nil)
	if runs := runsOnce(t, ep, s.ID, func([]scheduleRun) bool { return true }); len(runs) != 0 {
		t.Fatalf("a deleted schedule kept its runs %+v", runs)
	}
}

// A Run now that ends first doesn't hide an earlier run that still waits
// on you: an automatic run checks every thread of its schedule that may
// still go.
func TestARunNowHidesNoRunStillWaiting(t *testing.T) {
	fakeCodex(t)
	t.Setenv(codextest.Scenario, "command") // the first turn waits on an approval
	dir, ws := stateDir(t), t.TempDir()
	installSchedules(t, dir)
	_, ep, _ := codexAt(t, dir, tick)
	s := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 5})
	db := pluginDB(t, dir)
	first := at(time.Now().Add(-2 * time.Minute))
	write(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", first, first, s.ID)
	waiting := mustID(t, runsOnce(t, ep, s.ID, settled)[0].Thread)
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if th := threads(t, ep); len(th) == 1 && len(th[0].Requests) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first run never waited on its approval")
		}
	}
	t.Setenv(codextest.Scenario, "memory") // Run now's turn just ends
	var manual struct{ Thread string }
	schedules(t, ep, "runNow", map[string]any{"id": s.ID, "op": api.NewID().String()}, &manual)
	endedLog(t, ep, mustID(t, manual.Thread))

	next := at(time.Now().Add(-time.Minute))
	write(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", next, next, s.ID)
	runs := runsOnce(t, ep, s.ID, func(rs []scheduleRun) bool { return len(rs) == 3 && settled(rs) })
	if runs[1].Due != next || runs[1].State != "skipped" {
		t.Fatalf("runs %+v, want the next skipped while the first waits", runs)
	}
	if th := threads(t, ep); len(th) != 2 {
		t.Fatalf("threads %+v, want the first, %s, and Run now's", th, waiting)
	}
}

// A schedule whose last thread can't be checked records its run failed,
// saying why, and the schedules due after it still run.
func TestAScheduleThatCantBeCheckedHoldsUpNoOther(t *testing.T) {
	fakeCodex(t)
	dir, ws := stateDir(t), t.TempDir()
	installSchedules(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "plugins.json"), []byte(`{"grants": {"schedules": ["service", "threads:write", "threads:access"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, ep, stop := codexAt(t, dir, tick)
	a := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 60, "title": "A"})
	b := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 60, "title": "B"})
	stop()

	db := pluginDB(t, dir)
	first, second := time.Now().Add(-2*time.Minute).Truncate(time.Second), time.Now().Add(-time.Minute).Truncate(time.Second)
	write(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", at(first), at(first), a.ID)
	write(t, db, "INSERT INTO runs (schedule, key, due, state, thread, open, at) VALUES (?, 'manual:gone', ?, 'started', 'gone', 1, ?)", a.ID, at(first.Add(-time.Hour)), at(first))
	write(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", at(second), at(second), b.ID)
	_, ep, _ = codexAt(t, dir, tick)
	if runs := runsOnce(t, ep, a.ID, func(rs []scheduleRun) bool { return len(rs) == 2 }); runs[0].State != "failed" || !strings.Contains(runs[0].Reason, "couldn't be checked") {
		t.Fatalf("A's runs %+v, want one failed as its last thread couldn't be checked", runs)
	}
	if runs := runsOnce(t, ep, b.ID, settled); len(runs) != 1 || runs[0].State != "started" {
		t.Fatalf("B's runs %+v, want one started", runs)
	}
}

// coldSchedules is an engine on dir whose Schedules has schedule s, saved
// by fields, and has not started since, with the Schedules database
// locked: as the service starts, its database waits, as it does on a
// slow disk. unlock lets it go on.
func coldSchedules(t *testing.T, fields map[string]any) (dir string, ep *transport.EndpointClient, s savedSchedule, db *sql.DB, unlock func()) {
	t.Helper()
	fakeCodex(t)
	dir = stateDir(t)
	installSchedules(t, dir)
	_, ep, stop := codexAt(t, dir)
	s = saveSchedule(t, ep, t.TempDir(), fields)
	stop()
	_, ep, _ = codexAt(t, dir)
	db = pluginDB(t, dir)
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	return dir, ep, s, db, func() {
		if _, err := c.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
	}
}

// A delete whose caller timed out may still land, as any call that timed
// out may: the service had it, and does it once its database lets it.
// Trying it again answers as the first did, and a list shows it's done.
func TestADeleteThatTimedOutMayStillLand(t *testing.T) {
	fakeCodex(t)
	dir := stateDir(t)
	installSchedules(t, dir)
	_, ep, _ := codexAt(t, dir)
	s := saveSchedule(t, ep, t.TempDir(), map[string]any{"kind": "interval", "every": 60})
	db := pluginDB(t, dir)
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"plugin": "schedules", "method": "delete", "args": map[string]any{"id": s.ID}})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := ep.PluginCall(ctx, raw); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("delete answered %v, want its deadline", err)
	}
	if _, err := c.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); len(list(t, ep)) != 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the delete that timed out never landed")
		}
	}
	schedules(t, ep, "delete", map[string]any{"id": s.ID}, nil)
}

// A Run now tried again under the op of its first try, after that one
// timed out and after one that answered, starts one thread.
func TestARunNowTriedAgainStartsOneThread(t *testing.T) {
	_, ep, s, _, unlock := coldSchedules(t, map[string]any{"kind": "interval", "every": 60, "enabled": false})
	op := api.NewID().String()
	raw, _ := json.Marshal(map[string]any{"plugin": "schedules", "method": "runNow", "args": map[string]any{"id": s.ID, "op": op}})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := ep.PluginCall(ctx, raw); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run now answered %v, want its deadline", err)
	}
	unlock()
	var first, again struct{ Thread string }
	schedules(t, ep, "runNow", map[string]any{"id": s.ID, "op": op}, &first)
	schedules(t, ep, "runNow", map[string]any{"id": s.ID, "op": op}, &again)
	runs := runsOnce(t, ep, s.ID, settled)
	if n := len(threads(t, ep)); n != 1 || len(runs) != 1 || first.Thread == "" || again.Thread != first.Thread {
		t.Fatalf("one Run now tried three times made %d threads, answered %q then %q, and the runs %+v", n, first.Thread, again.Thread, runs)
	}
}

// A run whose create the engine took, but whose record failed to write,
// keeps its thread though the engine, restarted, no longer runs the
// provider it named.
func TestARunKeepsItsThreadPastItsProvider(t *testing.T) {
	fakeCodex(t)
	dir := stateDir(t)
	installSchedules(t, dir)
	_, ep, stop := codexAt(t, dir)
	s := saveSchedule(t, ep, t.TempDir(), map[string]any{"kind": "interval", "every": 5, "enabled": false})
	db := pluginDB(t, dir)
	write(t, db, unsettled)
	raw, _ := json.Marshal(map[string]any{"plugin": "schedules", "method": "runNow", "args": map[string]any{"id": s.ID, "op": api.NewID().String()}})
	if _, err := ep.PluginCall(context.Background(), raw); err == nil {
		t.Fatal("Run now answered though its record failed")
	}
	made := threads(t, ep)
	stop()
	write(t, db, "DROP TRIGGER unsettled")
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, PluginTimerScale: tick, Providers: provider.Mux{"fake": fake.New(nil)}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ep = transport.NewEndpointClient(Socket(dir), api.ID{})
	defer ep.Close()
	runs := runsOnce(t, ep, s.ID, settled)
	if len(made) != 1 || len(runs) != 1 || runs[0].State != "started" || runs[0].Thread != made[0].ID.String() {
		t.Fatalf("the engine made %+v; the run is %+v", made, runs)
	}
}
