package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	plugin "github.com/yxxbc/mygo-plugin"

	"glitched/internal/plugins/pack"
)

// schedulesRuntime runs the built-in Schedules' service as its module, on
// its own database, with threads it makes up: each create answers
// thread-N, and threads.list answers them all completed. clock, when set,
// is JavaScript that defines clock(), the time the service reads for each
// Date.now(). It answers the runtime, the database as a test writes it,
// and the keys each create was sent, in order.
func schedulesRuntime(t *testing.T, clock string) (*plugin.Plugin, *sql.DB, func() []string) {
	t.Helper()
	state := t.TempDir()
	folder := filepath.Join(state, "code")
	if err := os.CopyFS(folder, os.DirFS(filepath.Join("bundled", "schedules"))); err != nil {
		t.Fatal(err)
	}
	if clock != "" {
		main := filepath.Join(folder, "service.js")
		src, err := os.ReadFile(main)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(main, []byte(clock+"\n"+strings.ReplaceAll(string(src), "Date.now()", "clock()")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := &pluginDB{path: filepath.Join(DataDir(state), "schedules", "plugin.db")}
	t.Cleanup(d.close)
	var mu sync.Mutex
	var keys []string
	app := func(_ context.Context, raw json.RawMessage) (any, error) {
		var e struct {
			Type string
			Key  struct{ ID string }
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		switch e.Type {
		case "threads.list":
			rows := []map[string]string{}
			for i := range keys {
				rows = append(rows, map[string]string{"id": fmt.Sprintf("thread-%d", i+1), "status": "completed"})
			}
			return rows, nil
		case "threads.create":
			keys = append(keys, e.Key.ID)
			return map[string]string{"thread": fmt.Sprintf("thread-%d", len(keys))}, nil
		}
		return nil, fmt.Errorf("no %s here", e.Type)
	}
	p, err := plugin.Load(folder, plugin.Options{Prelude: Prelude, Contract: Contract, ContractVersion: ContractVersion, DataDir: DataDir(state),
		Functions: map[string]plugin.Function{"db.query": d.query, "db.exec": d.exec, "app": app}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Shutdown)
	return p, scheduleDB(t, state), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(keys)
	}
}

func scheduleDB(t *testing.T, state string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(DataDir(state), "schedules", "plugin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func serveSchedules(p *plugin.Plugin, method string, args, out any) error {
	return p.Call("serve", map[string]any{"method": method, "args": args}, out)
}

type schedule struct {
	ID, Title, NextDue string
	Enabled            bool
}

// saveSchedule saves an interval schedule of five minutes, with fields
// over it.
func saveSchedule(t *testing.T, p *plugin.Plugin, fields map[string]any) schedule {
	t.Helper()
	s := map[string]any{"title": "Nightly", "prompt": "Check.", "workspace": "/project", "provider": "codex", "model": "fake", "kind": "interval", "every": 5}
	for k, v := range fields {
		s[k] = v
	}
	var out schedule
	if err := serveSchedules(p, "save", map[string]any{"schedule": s}, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func sqlExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// intent writes a run of schedule id left starting under key, at due, as
// the service writes one: with the create it sends, sent or not yet.
func intent(t *testing.T, db *sql.DB, id, key, due string, manual, sent bool) {
	t.Helper()
	sqlExec(t, db, `INSERT INTO runs (schedule, key, due, manual, state, request, sent, at)
		SELECT id, ?, ?, ?, 'starting', json_object('prompt', prompt, 'options', json_object('title', title, 'workspace', workspace,
			'model', json_object('provider', provider, 'model', model, 'effort', effort, 'fast', json(iif(fast, 'true', 'false'))),
			'access', access, 'key', json_object('id', ?, 'at', ?))), ?, ?
		FROM schedules WHERE id = ?`, key, due, manual, key, due, sent, due, id)
}

type run struct{ Key, Due, Thread, State, Reason string }

func runsOf(t *testing.T, db *sql.DB, id string) []run {
	t.Helper()
	rows, err := db.Query("SELECT key, due, coalesce(thread, ''), state, coalesce(reason, '') FROM runs WHERE schedule = ? ORDER BY due, key", id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []run
	for rows.Next() {
		var r run
		if err := rows.Scan(&r.Key, &r.Due, &r.Thread, &r.State, &r.Reason); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// A run left starting is its schedule's catch-up: a tick that sends it
// again starts no other occurrence, and moves the schedule past now.
func TestARunLeftStartingIsItsSchedulesCatchUp(t *testing.T) {
	p, db, keys := schedulesRuntime(t, `const clock = () => Date.parse("2026-11-24T10:17:00Z");`)
	s := saveSchedule(t, p, map[string]any{"anchor": "2026-11-24T10:00:00Z"})
	first := "2026-11-24T10:05:00.000Z"
	sqlExec(t, db, "UPDATE schedules SET next_due = ? WHERE id = ?", first, s.ID)
	intent(t, db, s.ID, s.ID+"@"+first, first, false, true)
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	if runs := runsOf(t, db, s.ID); len(runs) != 1 || runs[0].State != "started" || runs[0].Due != first {
		t.Fatalf("runs %+v, want the one left starting, at %s, started alone", runs, first)
	}
	if k := keys(); len(k) != 1 || k[0] != s.ID+"@"+first {
		t.Fatalf("creates %q, want the one of %s", k, first)
	}
	var next string
	if err := db.QueryRow("SELECT next_due FROM schedules WHERE id = ?", s.ID).Scan(&next); err != nil || next != "2026-11-24T10:20:00.000Z" {
		t.Fatalf("next run at %s, %v, want 10:20", next, err)
	}
}

// A tick that reads the clock set back after it found a schedule due
// starts no run before the schedule's next_due.
func TestAClockSetBackMidTickStartsNoEarlierRun(t *testing.T) {
	// The save reads 10:00, so the schedule is next due at 10:05; the tick
	// reads 10:05:01, so it's due; then the clock says 10:04.
	p, db, keys := schedulesRuntime(t, `
		let reads = 0;
		const clock = () => Date.parse(++reads === 1 ? "2026-11-24T10:00:00Z" : reads === 2 ? "2026-11-24T10:05:01Z" : "2026-11-24T10:04:00Z");`)
	s := saveSchedule(t, p, nil)
	if s.NextDue != "2026-11-24T10:05:00.000Z" {
		t.Fatalf("saved next at %s, want 10:05", s.NextDue)
	}
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	if runs := runsOf(t, db, s.ID); len(runs) != 0 || len(keys()) != 0 {
		t.Fatalf("runs %+v and creates %q, want none before 10:05", runs, keys())
	}
	var next string
	if err := db.QueryRow("SELECT next_due FROM schedules WHERE id = ?", s.ID).Scan(&next); err != nil || next != s.NextDue {
		t.Fatalf("next run at %s, %v, want %s still", next, err, s.NextDue)
	}
}

// Two Run nows in one millisecond are two runs, each with the key it was
// created under and its own thread.
func TestTwoRunNowsInOneMillisecondAreTwoRuns(t *testing.T) {
	p, db, keys := schedulesRuntime(t, `const clock = () => Date.parse("2026-11-24T10:00:00Z");`)
	s := saveSchedule(t, p, nil)
	for range 2 {
		if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID}, nil); err != nil {
			t.Fatal(err)
		}
	}
	runs, k := runsOf(t, db, s.ID), keys()
	if len(runs) != 2 || len(k) != 2 {
		t.Fatalf("runs %+v of creates %q, want two", runs, k)
	}
	for _, r := range runs {
		i := slices.Index(k, r.Key)
		if i < 0 || r.Thread != fmt.Sprintf("thread-%d", i+1) {
			t.Fatalf("run %+v isn't the thread its key made, of %q", r, k)
		}
	}
}

// Turning a schedule off drops the automatic run it hadn't sent yet, as
// skipped; one it sent, which the engine may have made, and a Run now
// still finish.
func TestTurningAScheduleOffDropsTheRunsItHadntSent(t *testing.T) {
	p, db, keys := schedulesRuntime(t, "")
	s := saveSchedule(t, p, nil)
	unsent, sent, manual := s.ID+"@2026-11-24T10:00:00.000Z", s.ID+"@2026-11-24T10:05:00.000Z", "manual:01M85G4HXV540JRFTZA18F6P9N"
	intent(t, db, s.ID, unsent, "2026-11-24T10:00:00.000Z", false, false)
	intent(t, db, s.ID, sent, "2026-11-24T10:05:00.000Z", false, true)
	intent(t, db, s.ID, manual, "2026-11-24T10:06:00.000Z", true, true)
	saveSchedule(t, p, map[string]any{"id": s.ID, "enabled": false})
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{unsent: "skipped turned off", sent: "started", manual: "started"}
	for _, r := range runsOf(t, db, s.ID) {
		if got := strings.TrimSpace(r.State + " " + r.Reason); got != want[r.Key] {
			t.Errorf("run %s is %q, want %q", r.Key, got, want[r.Key])
		}
	}
	if k := keys(); len(k) != 2 || slices.Contains(k, unsent) {
		t.Fatalf("creates %q, want the sent run's and Run now's", k)
	}
}

// A schedule's folder is absolute as Go's filepath.IsAbs has it on Unix or
// on Windows, a drive's or a share's; the engine checks it again for its
// own.
func TestAScheduleTakesUnixAndWindowsFolders(t *testing.T) {
	p, _, _ := schedulesRuntime(t, "")
	for _, c := range []struct {
		folder string
		ok     bool
	}{
		{"/Users/me/app", true},
		{`C:\work\app`, true},
		{"C:/work/app", true},
		{`\\files\share\app`, true},
		{"//files/share/app", true},
		{"C:work", false},
		{`\work\app`, false},
		{"work/app", false},
	} {
		args := map[string]any{"schedule": map[string]any{"title": "Nightly", "prompt": "Check.", "workspace": c.folder, "provider": "codex", "model": "fake", "kind": "interval", "every": 5}}
		if err := serveSchedules(p, "save", args, nil); (err == nil) != c.ok {
			t.Errorf("save(%q): %v, want saved %v", c.folder, err, c.ok)
		}
	}
}

// There are at most 1000 schedules, and list answers them a page at a
// time, by title.
func TestSchedulesStopAtTheirMostAndListAPageAtATime(t *testing.T) {
	p, db, _ := schedulesRuntime(t, "")
	saveSchedule(t, p, map[string]any{"title": "a"})
	sqlExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 998)
		INSERT INTO schedules SELECT printf('%026d', n.i), printf('t%04d', n.i), prompt, workspace, worktree, provider, model, effort, fast, access,
			kind, every, at, weekdays, zone, anchor, missed, enabled, next_due FROM schedules, n`)
	saveSchedule(t, p, map[string]any{"title": "z"})
	if err := serveSchedules(p, "save", map[string]any{"schedule": map[string]any{"title": "one too many", "prompt": "Check.", "workspace": "/project", "provider": "codex", "model": "fake", "kind": "interval", "every": 5}}, nil); err == nil || !strings.Contains(err.Error(), "1000") {
		t.Fatalf("the 1001st schedule saved: %v", err)
	}
	var titles []string
	var after any
	for pages := 0; ; pages++ {
		var page struct {
			Schedules []schedule
			Next      any
		}
		if err := serveSchedules(p, "list", map[string]any{"after": after}, &page); err != nil {
			t.Fatal(err)
		}
		for _, s := range page.Schedules {
			titles = append(titles, s.Title)
		}
		if page.Next == nil {
			break
		}
		if after = page.Next; pages > 10 {
			t.Fatal("list never ends")
		}
	}
	if len(titles) != 1000 || !slices.IsSorted(titles) || titles[0] != "a" || titles[999] != "z" {
		t.Fatalf("list answered %d schedules, from %q to %q, want 1000 by title", len(titles), titles[0], titles[len(titles)-1])
	}
}

// A schedule keeps its newest 200 runs, and a run whose thread may still go
// however old it is.
func TestASchedulesHistoryKeepsItsNewest(t *testing.T) {
	p, db, _ := schedulesRuntime(t, "")
	s := saveSchedule(t, p, map[string]any{"enabled": false})
	sqlExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < 300)
		INSERT INTO runs (schedule, key, due, state, thread, open, at)
		SELECT ?, 'old-' || i, strftime('%Y-%m-%dT%H:%M:%fZ', '1970-01-01', printf('+%d minutes', 5 * i)), 'started', 'thread-old-' || i, i = 0, '1970-01-01T00:00:00.000Z' FROM n`, s.ID)
	if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID}, nil); err != nil {
		t.Fatal(err)
	}
	runs := runsOf(t, db, s.ID)
	if len(runs) != 201 || runs[0].Key != "old-0" || runs[1].Key != "old-102" || runs[200].Key[:7] != "manual:" {
		t.Fatalf("%d runs kept, from %s, %s to %s; want the open old-0, then old-102 to old-300 and Run now's", len(runs), runs[0].Key, runs[1].Key, runs[len(runs)-1].Key)
	}
}

// Runs left starting that can't settle hold up no other: each tick takes
// the next page of them, so one past the first page has its turn.
func TestRunsThatCantSettleHoldUpNoOther(t *testing.T) {
	p, db, keys := schedulesRuntime(t, "")
	a := saveSchedule(t, p, map[string]any{"title": "stuck", "enabled": false})
	b := saveSchedule(t, p, map[string]any{"title": "healthy", "enabled": false})
	if b.ID < a.ID {
		a, b = b, a
	}
	sqlExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 500)
		INSERT INTO runs (schedule, key, due, manual, state, request, sent, at)
		SELECT ?, printf('manual:%04d', i), '2026-11-24T10:00:00.000Z', 1, 'starting', '{"prompt":"Check.","options":{}}', 1, '2026-11-24T10:00:00.000Z' FROM n`, a.ID)
	sqlExec(t, db, "CREATE TRIGGER stuck BEFORE UPDATE OF state ON runs WHEN NEW.schedule = '"+a.ID+"' BEGIN SELECT RAISE(ABORT, 'its history is unwritable'); END")
	intent(t, db, b.ID, "manual:healthy", "2026-11-24T10:00:00.000Z", true, true)
	for ticks := 1; ; ticks++ {
		err := p.Call("timer", nil, nil)
		if runs := runsOf(t, db, b.ID); runs[0].State == "started" {
			break
		}
		if err == nil || !strings.Contains(err.Error(), "its history is unwritable") {
			t.Fatalf("tick %d: %v, want the stuck runs' failure", ticks, err)
		}
		if ticks == 11 {
			t.Fatalf("after %d ticks, %d creates, the healthy run is still starting", ticks, len(keys()))
		}
	}
}

// A tick that fails says so as it ends, in the timer's own answer.
func TestATickThatFailsSaysSoAsItEnds(t *testing.T) {
	p, db, keys := schedulesRuntime(t, "")
	s := saveSchedule(t, p, nil)
	due := time.Now().Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	sqlExec(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", due, due, s.ID)
	sqlExec(t, db, "CREATE TRIGGER fault BEFORE INSERT ON runs BEGIN SELECT RAISE(ABORT, 'tick fault'); END")
	if err := p.Call("timer", nil, nil); err == nil || !strings.Contains(err.Error(), "tick fault") {
		t.Fatalf("the failed tick answered %v", err)
	}
	if len(keys()) != 0 {
		t.Fatalf("creates %q, want none", keys())
	}
}

// A write that reaches serve past its call's deadline refuses, and writes
// nothing.
func TestAWriteReachedLateRefuses(t *testing.T) {
	p, _, _ := schedulesRuntime(t, "")
	s := saveSchedule(t, p, nil)
	late := time.Now().Add(-time.Second).UnixMilli()
	for _, c := range []struct {
		method string
		args   map[string]any
	}{
		{"save", map[string]any{"schedule": map[string]any{"id": s.ID, "title": "Renamed", "prompt": "Check.", "workspace": "/project", "provider": "codex", "model": "fake", "kind": "interval", "every": 5}}},
		{"runNow", map[string]any{"id": s.ID}},
		{"delete", map[string]any{"id": s.ID}},
	} {
		if err := p.Call("serve", map[string]any{"method": c.method, "args": c.args, "deadline": late}, nil); err == nil || !strings.Contains(err.Error(), "ran out of time") {
			t.Errorf("%s past its deadline: %v", c.method, err)
		}
	}
	var page struct{ Schedules []schedule }
	if err := serveSchedules(p, "list", nil, &page); err != nil || len(page.Schedules) != 1 || page.Schedules[0].Title != "Nightly" {
		t.Fatalf("after the late writes, the schedules are %+v, %v", page.Schedules, err)
	}
}

// schedulesBuiltIn is the built-in Schedules signed by a release key of
// its own, which it answers the public half of.
func schedulesBuiltIn(t *testing.T) (fs.FS, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "schedules"), os.DirFS(filepath.Join("bundled", "schedules"))); err != nil {
		t.Fatal(err)
	}
	key, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(filepath.Join(root, "schedules"), key); err != nil {
		t.Fatal(err)
	}
	return os.DirFS(root), key.Public()
}

func callSchedules(ctx context.Context, s *Services, method string, args any) (json.RawMessage, error) {
	raw, _ := json.Marshal(args)
	return s.Call(ctx, ServiceCall{Plugin: "schedules", Method: method, Args: raw})
}

// The page's writes wait on no create: a save while a tick's create is
// still out answers at once.
func TestAWriteWaitsOnNoCreate(t *testing.T) {
	state := t.TempDir()
	builtIn, key := schedulesBuiltIn(t)
	out, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	engine := StartServices(state, ServiceOptions{Bundled: builtIn, ReleaseKeys: []string{key}, TimerScale: 0.005, Logf: t.Logf,
		Do: func(_ string, e Effect) (any, error) {
			return Pending(func(done func(any, error)) {
				go func() {
					once.Do(func() { close(out) })
					<-release
					done(map[string]string{"thread": "thread-1"}, nil)
				}()
			}), nil
		}})
	t.Cleanup(engine.Close)
	t.Cleanup(func() { close(release) })
	raw, err := callSchedules(context.Background(), engine, "save", map[string]any{"schedule": map[string]any{"title": "Nightly", "prompt": "Check.", "workspace": "/project", "provider": "codex", "model": "fake", "kind": "interval", "every": 5}})
	if err != nil {
		t.Fatal(err)
	}
	var s schedule
	json.Unmarshal(raw, &s)
	db := scheduleDB(t, state)
	due := time.Now().Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	sqlExec(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", due, due, s.ID)
	<-out
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := callSchedules(ctx, engine, "save", map[string]any{"schedule": map[string]any{"id": s.ID, "title": "Renamed", "prompt": "Check.", "workspace": "/project", "provider": "codex", "model": "fake", "kind": "interval", "every": 5}}); err != nil {
		t.Fatalf("a save while a create is out: %v", err)
	}
	var title string
	if err := db.QueryRow("SELECT title FROM schedules WHERE id = ?", s.ID).Scan(&title); err != nil || title != "Renamed" {
		t.Fatalf("the title is %q, %v", title, err)
	}
}

// A call whose deadline passes before it reaches the service never does:
// a delete that timed out deletes nothing after.
func TestACallThatTimedOutNeverWritesAfter(t *testing.T) {
	state := t.TempDir()
	builtIn, key := schedulesBuiltIn(t)
	out, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	// The effect holds the services' queue, as one the engine ran on it
	// would, till the delete's deadline has passed.
	engine := StartServices(state, ServiceOptions{Bundled: builtIn, ReleaseKeys: []string{key}, TimerScale: 0.005, Logf: t.Logf,
		Do: func(_ string, e Effect) (any, error) {
			once.Do(func() { close(out) })
			<-release
			return map[string]string{"thread": "thread-1"}, nil
		}})
	t.Cleanup(engine.Close)
	raw, err := callSchedules(context.Background(), engine, "save", map[string]any{"schedule": map[string]any{"title": "Nightly", "prompt": "Check.", "workspace": "/project", "provider": "codex", "model": "fake", "kind": "interval", "every": 5}})
	if err != nil {
		t.Fatal(err)
	}
	var s schedule
	json.Unmarshal(raw, &s)
	db := scheduleDB(t, state)
	due := time.Now().Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	sqlExec(t, db, "UPDATE schedules SET anchor = ?, next_due = ? WHERE id = ?", due, due, s.ID)
	<-out
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go func() {
		<-ctx.Done()
		close(release)
	}()
	if _, err := callSchedules(ctx, engine, "delete", map[string]any{"id": s.ID}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the delete answered %v, want its deadline passed", err)
	}
	eventually(t, "the tick's run to start", func() bool {
		var state string
		return db.QueryRow("SELECT state FROM runs WHERE schedule = ?", s.ID).Scan(&state) == nil && state == "started"
	})
	var n int
	if err := db.QueryRow("SELECT count(*) FROM schedules WHERE id = ?", s.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the timed-out delete deleted the schedule after: %d, %v", n, err)
	}
}
