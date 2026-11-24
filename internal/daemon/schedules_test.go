package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"glitched/internal/provider/codex/codextest"
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
	var list []savedSchedule
	schedules(t, ep, "list", nil, &list)
	if len(list) != 1 || list[0].NextDue != at(anchor.Add(20*time.Minute)) || list[0].LastThread != runs[0].Thread {
		t.Fatalf("the schedule %+v, want next at %s and its last thread the run's", list, at(anchor.Add(20*time.Minute)))
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
	var list []savedSchedule
	schedules(t, ep, "list", nil, &list)
	if list[0].NextDue != at(anchor.Add(time.Hour)) {
		t.Fatalf("next run at %s, want %s", list[0].NextDue, at(anchor.Add(time.Hour)))
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
		var list []savedSchedule
		schedules(t, ep, "list", nil, &list)
		return list[0].LastThread == runs[0].Thread && list[0].NextDue > at(time.Now())
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

// A crash between a run's starting row and its create, or between the
// create and the row's end, comes back to one thread with one message and
// one turn: the engine after it starts the run again under its key.
func TestACrashMidRunStartsOneThread(t *testing.T) {
	fakeCodex(t)
	dir, ws := stateDir(t), t.TempDir()
	installSchedules(t, dir)
	_, ep, stop := codexAt(t, dir, tick)
	s := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 60})
	stop()

	anchor := time.Now().Add(-5 * time.Minute).Truncate(time.Second)
	db := pluginDB(t, dir)
	write(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", at(anchor), at(anchor), s.ID)
	write(t, db, "INSERT INTO runs (schedule, due, key, state, at) VALUES (?, ?, ?, 'starting', ?)", s.ID, at(anchor), s.ID+"@"+at(anchor), at(anchor))
	_, ep, stop = codexAt(t, dir, tick)
	runs := runsOnce(t, ep, s.ID, settled)
	if len(runs) != 1 || runs[0].State != "started" || runs[0].Due != at(anchor) {
		t.Fatalf("runs %+v, want the run left starting started", runs)
	}
	first := mustID(t, runs[0].Thread)
	endedLog(t, ep, first)
	stop()

	write(t, db, "UPDATE runs SET state = 'starting', thread = NULL WHERE schedule = ?", s.ID)
	write(t, db, "UPDATE schedules SET next_due = ? WHERE id = ?", at(anchor), s.ID)
	_, ep, _ = codexAt(t, dir, tick)
	runs = runsOnce(t, ep, s.ID, settled)
	if len(runs) != 1 || runs[0].State != "started" || runs[0].Thread != first.String() {
		t.Fatalf("runs %+v, want the one run started again as %s", runs, first)
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

// A Run now a crash left starting finishes on the next tick, under its
// own key, though its schedule is off and so never due.
func TestARunNowLeftStartingFinishesOnTheNextTick(t *testing.T) {
	fakeCodex(t)
	dir, ws := stateDir(t), t.TempDir()
	installSchedules(t, dir)
	_, ep, stop := codexAt(t, dir, tick)
	s := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 60, "enabled": false})
	stop()

	due := time.Now().Add(-time.Minute).Truncate(time.Second)
	write(t, pluginDB(t, dir), "INSERT INTO runs (schedule, due, key, state, at) VALUES (?, ?, 'manual:01M85G4HXV540JRFTZA18F6P9N', 'starting', ?)", s.ID, at(due), at(due))
	_, ep, _ = codexAt(t, dir, tick)
	runs := runsOnce(t, ep, s.ID, settled)
	if len(runs) != 1 || runs[0].State != "started" || runs[0].Thread == "" {
		t.Fatalf("runs %+v, want the Run now started", runs)
	}
	if th := threads(t, ep); len(th) != 1 || th[0].ID.String() != runs[0].Thread {
		t.Fatalf("threads %+v, want the run's %s alone", th, runs[0].Thread)
	}
}

// A run whose thread started but whose record fails to write stays
// starting, and counts as a run still going, till a tick finishes it with
// that same thread.
func TestARunWhoseRecordFailsFinishesWithItsThread(t *testing.T) {
	fakeCodex(t)
	t.Setenv(codextest.Scenario, "command") // the turn waits on an approval
	dir, ws := stateDir(t), t.TempDir()
	installSchedules(t, dir)
	_, ep, _ := codexAt(t, dir, tick)
	s := saveSchedule(t, ep, ws, map[string]any{"kind": "interval", "every": 60})
	db := pluginDB(t, dir)
	write(t, db, "CREATE TRIGGER no_last_thread BEFORE UPDATE OF last_thread ON schedules BEGIN SELECT RAISE(ABORT, 'disk full'); END")
	raw, _ := json.Marshal(map[string]any{"plugin": "schedules", "method": "runNow", "args": map[string]any{"id": s.ID}})
	if _, err := ep.PluginCall(context.Background(), raw); err == nil {
		t.Fatal("Run now answered though its record failed")
	}
	if th := threads(t, ep); len(th) != 1 {
		t.Fatalf("threads %+v, want Run now's", th)
	}

	due := time.Now().Add(-time.Minute).Truncate(time.Second)
	write(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", at(due), at(due), s.ID)
	runs := runsOnce(t, ep, s.ID, func(rs []scheduleRun) bool { return len(rs) == 2 && rs[1].State != "starting" })
	if runs[1].Due != at(due) || runs[1].State != "skipped" || runs[0].State != "starting" {
		t.Fatalf("runs %+v, want the due run skipped while Run now's still starts", runs)
	}
	write(t, db, "DROP TRIGGER no_last_thread")
	runs = runsOnce(t, ep, s.ID, settled)
	th := threads(t, ep)
	if len(th) != 1 || runs[0].State != "started" || runs[0].Thread != th[0].ID.String() {
		t.Fatalf("runs %+v and threads %+v, want Run now's started with its thread, alone", runs, th)
	}

	schedules(t, ep, "delete", map[string]any{"id": s.ID}, nil)
	if runs := runsOnce(t, ep, s.ID, func([]scheduleRun) bool { return true }); len(runs) != 0 {
		t.Fatalf("a deleted schedule kept its runs %+v", runs)
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
	write(t, db, "UPDATE schedules SET anchor = ?, next_due = ?, last_thread = 'gone' WHERE id = ?", at(first), at(first), a.ID)
	write(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", at(second), at(second), b.ID)
	_, ep, _ = codexAt(t, dir, tick)
	if runs := runsOnce(t, ep, a.ID, settled); len(runs) != 1 || runs[0].State != "failed" || !strings.Contains(runs[0].Reason, "couldn't be checked") {
		t.Fatalf("A's runs %+v, want one failed as its last thread couldn't be checked", runs)
	}
	if runs := runsOnce(t, ep, b.ID, settled); len(runs) != 1 || runs[0].State != "started" {
		t.Fatalf("B's runs %+v, want one started", runs)
	}
}
