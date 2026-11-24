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
	"unicode/utf8"

	plugin "github.com/yxxbc/mygo-plugin"

	"glitched/internal/api"
	"glitched/internal/plugins/pack"
)

// schedulesRuntime runs the built-in Schedules' service as its module, on
// its own database, with threads it makes up: each create answers
// thread-N, and threads.list answers them all completed. clock, when set,
// is JavaScript that defines clock(), the time the service reads for each
// Date.now(). It answers the runtime, the database as a test writes it,
// and the keys each create was sent, in order.
func schedulesRuntime(t *testing.T, clock string) (*plugin.Plugin, *sql.DB, func() []string) {
	return schedulesRuntimeWith(t, clock, &scheduleHooks{})
}

// scheduleHooks change what schedulesRuntimeWith's host does, read as it
// does it: created, when set, runs as a create is sent, and its error is
// the create's; listed, when set, runs as threads.list answers, after it
// read the threads, and its threads and error are added to the answer;
// wrote, when set, runs after each db.exec that wrote, with its SQL, and
// its error is the write's, though the write stands.
type scheduleHooks struct {
	created func(prompt string) error
	listed  func() ([]map[string]string, error)
	wrote   func(sql string) error
}

func schedulesRuntimeWith(t *testing.T, clock string, hooks *scheduleHooks) (*plugin.Plugin, *sql.DB, func() []string) {
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
			Type, Prompt string
			Key          struct{ ID string }
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		switch e.Type {
		case "threads.list":
			mu.Lock()
			rows := []map[string]string{}
			for i := range keys {
				rows = append(rows, map[string]string{"id": fmt.Sprintf("thread-%d", i+1), "status": "completed"})
			}
			mu.Unlock()
			if hooks.listed != nil {
				more, err := hooks.listed()
				if err != nil {
					return nil, err
				}
				rows = append(rows, more...)
			}
			return rows, nil
		case "threads.create":
			if hooks.created != nil {
				if err := hooks.created(e.Prompt); err != nil {
					return nil, err
				}
			}
			mu.Lock()
			defer mu.Unlock()
			keys = append(keys, e.Key.ID)
			return map[string]string{"thread": fmt.Sprintf("thread-%d", len(keys))}, nil
		}
		return nil, fmt.Errorf("no %s here", e.Type)
	}
	after := func(write plugin.Function) plugin.Function {
		return func(ctx context.Context, raw json.RawMessage) (any, error) {
			out, err := write(ctx, raw)
			if err == nil && hooks.wrote != nil {
				err = hooks.wrote(string(raw))
			}
			return out, err
		}
	}
	p, err := plugin.Load(folder, plugin.Options{Prelude: Prelude, Contract: Contract, ContractVersion: ContractVersion, DataDir: DataDir(state),
		Functions: map[string]plugin.Function{"db.query": d.query, "db.exec": after(d.exec), "app": app}})
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

// op is the nth op a page makes at at, an RFC 3339 time: a ULID.
func op(at string, n byte) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		panic(err)
	}
	id, err := api.IDAt(t, [10]byte{9: n})
	if err != nil {
		panic(err)
	}
	return id.String()
}

type schedule struct {
	ID, Title, NextDue string
	Enabled            bool
	Starting           int
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
	for i := range 2 {
		if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID, "op": op(from10, byte(i))}, nil); err != nil {
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
		{`é:\notes`, false},
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
			kind, every, at, weekdays, zone, anchor, missed, enabled, next_due, revision, served FROM schedules, n`)
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

// A schedule keeps its newest 50 runs, and a run whose thread may still go
// however old it is.
func TestASchedulesHistoryKeepsItsNewest(t *testing.T) {
	p, db, _ := schedulesRuntime(t, "")
	s := saveSchedule(t, p, map[string]any{"enabled": false})
	sqlExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < 300)
		INSERT INTO runs (schedule, key, due, state, thread, open, at)
		SELECT ?, 'old-' || i, strftime('%Y-%m-%dT%H:%M:%fZ', '1970-01-01', printf('+%d minutes', 5 * i)), 'started', 'thread-old-' || i, i = 0, '1970-01-01T00:00:00.000Z' FROM n`, s.ID)
	if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID, "op": api.NewID().String()}, nil); err != nil {
		t.Fatal(err)
	}
	runs := runsOf(t, db, s.ID)
	if len(runs) != keptRuns+1 || runs[0].Key != "old-0" || runs[1].Key != "old-252" || runs[keptRuns].Key[:7] != "manual:" {
		t.Fatalf("%d runs kept, from %s, %s to %s; want the open old-0, then old-252 to old-300 and Run now's", len(runs), runs[0].Key, runs[1].Key, runs[len(runs)-1].Key)
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

// The clocks these tests give the service: one at 10:17, past a 10:05 run
// of a schedule from 10:00, and one a test moves, with at and wait.
const (
	at1017  = "const clock = () => Date.parse('2026-11-24T10:17:00Z');"
	movable = "let now = Date.parse('2026-11-24T10:00:00Z'); const clock = () => now; export function at(t) { now = Date.parse(t) } export function wait(ms) { now += ms }"
	from10  = "2026-11-24T10:00:00.000Z"
	due1005 = "2026-11-24T10:05:00.000Z"
)

// dueAt1005 saves a five-minute schedule from 10:00, fields over it, whose
// 10:05 run is due.
func dueAt1005(t *testing.T, p *plugin.Plugin, db *sql.DB, fields map[string]any) schedule {
	t.Helper()
	s := saveSchedule(t, p, map[string]any{"anchor": from10})
	if fields != nil {
		fields["id"] = s.ID
		s = saveSchedule(t, p, fields)
	}
	sqlExec(t, db, "UPDATE schedules SET next_due = ? WHERE id = ?", due1005, s.ID)
	return s
}

// A run its schedule marked sent before it was turned off may still start
// after, as the engine may have it; turning the schedule off answers how
// many such runs it has.
func TestARunSentBeforeItsScheduleTurnedOffMayStillStart(t *testing.T) {
	hooks := &scheduleHooks{wrote: func(q string) error {
		if strings.Contains(q, "SET sent = 1") {
			return errors.New("cut once the run was marked sent")
		}
		return nil
	}}
	p, db, keys := schedulesRuntimeWith(t, at1017, hooks)
	s := dueAt1005(t, p, db, nil)
	if err := p.Call("timer", nil, nil); err == nil || !strings.Contains(err.Error(), "cut once") {
		t.Fatalf("the tick answered %v, want the cut", err)
	}
	p.Close()
	hooks.wrote = nil
	off := saveSchedule(t, p, map[string]any{"id": s.ID, "enabled": false})
	if off.Starting != 1 || len(keys()) != 0 {
		t.Fatalf("turned off with %d runs starting and creates %q; want the one marked sent, not sent yet", off.Starting, keys())
	}
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	if runs := runsOf(t, db, s.ID); len(keys()) != 1 || len(runs) != 1 || runs[0].State != "started" {
		t.Fatalf("creates %q, runs %+v; want the run marked sent started", keys(), runs)
	}
}

// A run its schedule hadn't marked sent when it was turned off never
// starts, though turning it off lands as the tick writes the run, and
// goes on only once the tick has tried to mark it sent.
func TestARunNotSentWhenItsScheduleTurnedOffNeverStarts(t *testing.T) {
	hooks := &scheduleHooks{}
	p, db, keys := schedulesRuntimeWith(t, at1017, hooks)
	s := dueAt1005(t, p, db, nil)
	saved, offWrote, marked := make(chan error, 1), make(chan struct{}), make(chan struct{})
	var once sync.Once
	hooks.wrote = func(q string) error {
		switch {
		case strings.Contains(q, "INSERT INTO runs"):
			once.Do(func() {
				go func() {
					saved <- serveSchedules(p, "save", map[string]any{"schedule": map[string]any{"id": s.ID, "title": "Nightly", "prompt": "Check.", "workspace": "/project", "provider": "codex", "model": "fake", "kind": "interval", "every": 5, "enabled": false}}, nil)
				}()
				<-offWrote
			})
		case strings.Contains(q, "UPDATE schedules SET title"):
			close(offWrote)
			<-marked
		case strings.Contains(q, "SET sent = 1"):
			close(marked)
		}
		return nil
	}
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	if runs := runsOf(t, db, s.ID); len(keys()) != 0 || len(runs) != 1 || runs[0].State+" "+runs[0].Reason != "skipped turned off" {
		t.Fatalf("creates %q, runs %+v; want the run skipped, turned off", keys(), runs)
	}
}

// A schedule saved while the tick sends another's run starts its next run
// with what was saved.
func TestAnEditBeforeItsRunIsWrittenStartsTheEdit(t *testing.T) {
	hooks := &scheduleHooks{}
	p, db, _ := schedulesRuntimeWith(t, at1017, hooks)
	a := dueAt1005(t, p, db, map[string]any{"prompt": "old"})
	b := dueAt1005(t, p, db, map[string]any{"prompt": "old"})
	if b.ID < a.ID {
		a, b = b, a
	}
	in, release := make(chan struct{}), make(chan struct{})
	var prompts []string
	hooks.created = func(prompt string) error {
		prompts = append(prompts, prompt)
		if len(prompts) == 1 {
			close(in)
			<-release
		}
		return nil
	}
	ticked := make(chan error, 1)
	go func() { ticked <- p.Call("timer", nil, nil) }()
	<-in
	saveSchedule(t, p, map[string]any{"id": b.ID, "prompt": "new"})
	close(release)
	if err := <-ticked; err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(prompts, []string{"old", "new"}) {
		t.Fatalf("the tick sent the prompts %q, want old, then the edit", prompts)
	}
}

// A cut as a catch-up's schedule moves its next_due, or once it has and
// before the run settles, leaves the run starting, so the next tick sends
// the same catch-up again, under its key, and no later occurrence.
func TestACutBetweenACatchUpAndItsNextDueRunsNoOther(t *testing.T) {
	for _, cut := range []string{"BEFORE UPDATE OF next_due ON schedules", "BEFORE UPDATE OF state ON runs"} {
		t.Run(cut, func(t *testing.T) {
			p, db, keys := schedulesRuntime(t, at1017)
			s := dueAt1005(t, p, db, nil)
			catchUp := s.ID + "@" + due1005
			intent(t, db, s.ID, catchUp, due1005, false, false)
			sqlExec(t, db, "CREATE TRIGGER cut "+cut+" BEGIN SELECT RAISE(ABORT, 'the cut'); END")
			if err := p.Call("timer", nil, nil); err == nil || !strings.Contains(err.Error(), "the cut") {
				t.Fatalf("the tick answered %v, want the cut", err)
			}
			if runs := runsOf(t, db, s.ID); len(runs) != 1 || runs[0].State != "starting" {
				t.Fatalf("after the cut the runs are %+v, want the catch-up still starting", runs)
			}
			p.Close()
			sqlExec(t, db, "DROP TRIGGER cut")
			if err := p.Call("timer", nil, nil); err != nil {
				t.Fatal(err)
			}
			var next string
			if err := db.QueryRow("SELECT next_due FROM schedules").Scan(&next); err != nil {
				t.Fatal(err)
			}
			runs := runsOf(t, db, s.ID)
			if k := slices.Compact(keys()); len(k) != 1 || k[0] != catchUp || len(runs) != 1 || runs[0].State != "started" || next != "2026-11-24T10:20:00.000Z" {
				t.Fatalf("creates %q, runs %+v, next due %s; want the one catch-up, then 10:20", keys(), runs, next)
			}
		})
	}
}

// A cut once a run is recorded and before its schedule's next_due moves
// leaves the run recorded once: the next tick records it again under its
// key, which writes nothing, and moves next_due.
func TestACutBeforeARunsNextDueMovesRecordsItOnce(t *testing.T) {
	p, db, keys := schedulesRuntime(t, at1017)
	s := dueAt1005(t, p, db, map[string]any{"missed": "skip"})
	sqlExec(t, db, "CREATE TRIGGER cut BEFORE UPDATE OF next_due ON schedules BEGIN SELECT RAISE(ABORT, 'the cut'); END")
	if err := p.Call("timer", nil, nil); err == nil || !strings.Contains(err.Error(), "the cut") {
		t.Fatalf("the tick answered %v, want the cut", err)
	}
	sqlExec(t, db, "DROP TRIGGER cut")
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	var next string
	if err := db.QueryRow("SELECT next_due FROM schedules").Scan(&next); err != nil {
		t.Fatal(err)
	}
	if runs := runsOf(t, db, s.ID); len(runs) != 1 || runs[0].State != "missed" || next != "2026-11-24T10:20:00.000Z" || len(keys()) != 0 {
		t.Fatalf("runs %+v, next due %s, creates %q; want the one run missed, then 10:20", runs, next, keys())
	}
}

// A Run now left starting that takes a tick's whole budget holds up no
// schedule: the two kinds of work take turns, from tick to tick too.
func TestAStuckRunNowHoldsUpNoSchedule(t *testing.T) {
	var p *plugin.Plugin
	hooks := &scheduleHooks{created: func(string) error { return p.Call("wait", 21_000, nil) }}
	p, db, _ := schedulesRuntimeWith(t, movable, hooks)
	a := saveSchedule(t, p, map[string]any{"enabled": false})
	b := dueAt1005(t, p, db, nil)
	if err := p.Call("at", "2026-11-24T10:05:00Z", nil); err != nil {
		t.Fatal(err)
	}
	intent(t, db, a.ID, "manual:stuck", due1005, true, true)
	sqlExec(t, db, "CREATE TRIGGER stuck BEFORE UPDATE OF state ON runs WHEN OLD.manual = 1 BEGIN SELECT RAISE(ABORT, 'stuck'); END")
	for range 2 {
		p.Call("timer", nil, nil)
	}
	if runs := runsOf(t, db, b.ID); len(runs) != 1 || runs[0].State != "started" {
		t.Fatalf("after two ticks the due schedule's runs are %+v, want one started", runs)
	}
}

// A service restarted between ticks goes on to the Run nows it hasn't
// tried, not those it tried last: a run's last try is in its record.
func TestARestartedServiceTriesTheRunsItTriedLongestAgoFirst(t *testing.T) {
	var p *plugin.Plugin
	hooks := &scheduleHooks{created: func(string) error { return p.Call("wait", 1000, nil) }}
	p, db, _ := schedulesRuntimeWith(t, movable, hooks)
	a := saveSchedule(t, p, map[string]any{"enabled": false})
	b := saveSchedule(t, p, map[string]any{"enabled": false})
	if b.ID < a.ID {
		a, b = b, a
	}
	for i := range 50 {
		intent(t, db, a.ID, fmt.Sprintf("manual:%02d", i), from10, true, true)
	}
	intent(t, db, b.ID, "manual:healthy", from10, true, true)
	sqlExec(t, db, "CREATE TRIGGER stuck BEFORE UPDATE OF state ON runs WHEN OLD.schedule = '"+a.ID+"' BEGIN SELECT RAISE(ABORT, 'stuck'); END")
	for i := range 3 {
		if err := p.Call("at", fmt.Sprintf("2026-11-24T10:%02d:00Z", 10+i), nil); err != nil {
			t.Fatal(err)
		}
		p.Call("timer", nil, nil)
		p.Close()
	}
	if runs := runsOf(t, db, b.ID); len(runs) != 1 || runs[0].State != "started" {
		t.Fatalf("after three ticks, each in a new runtime, the 51st Run now is %+v, want started", runs)
	}
}

// Every schedule due in a minute starts its run in it, however many there
// are, while the tick has time.
func TestEveryDueScheduleStartsInItsMinute(t *testing.T) {
	p, db, keys := schedulesRuntime(t, "const clock = () => Date.parse('2026-11-24T10:05:00Z');")
	s := dueAt1005(t, p, db, nil)
	sqlExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 299)
		INSERT INTO schedules (id, title, prompt, workspace, worktree, provider, model, effort, fast, access, kind, every, at, weekdays, zone, anchor, missed, enabled, next_due)
		SELECT printf('%026d', n.i), title, prompt, workspace, worktree, provider, model, effort, fast, access, kind, every, at, weekdays, zone, anchor, missed, enabled, next_due
		FROM schedules, n WHERE id = ?`, s.ID)
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM runs WHERE due = ? AND state = 'started'", due1005).Scan(&n); err != nil || n != 300 || len(keys()) != 300 {
		t.Fatalf("%d of 300 due schedules started at 10:05, with %d creates, %v", n, len(keys()), err)
	}
}

// A schedule that's off keeps its newest history, however many Run nows
// it had: their threads, once stopped, stop counting.
func TestTheHistoryOfAScheduleThatsOffStaysItsNewest(t *testing.T) {
	p, db, _ := schedulesRuntime(t, movable)
	s := saveSchedule(t, p, map[string]any{"enabled": false})
	for i := range keptRuns + 40 {
		if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID, "op": op(from10, byte(i))}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Call("at", "2026-11-24T10:01:00Z", nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	var n, open int
	if err := db.QueryRow("SELECT count(*), sum(open) FROM runs").Scan(&n, &open); err != nil || n != keptRuns || open != 0 {
		t.Fatalf("%d runs kept, %d of them open, %v; want the newest %d, none open", n, open, err, keptRuns)
	}
}

// Ten thousand runs whose threads may still go hold up no schedule's
// next run: their threads are checked a page at a time.
func TestTenThousandOpenRunsHoldUpNoSchedule(t *testing.T) {
	p, db, keys := schedulesRuntime(t, at1017)
	s := dueAt1005(t, p, db, nil)
	sqlExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i <= 10000)
		INSERT INTO runs (schedule, key, due, manual, state, sent, open, thread, at)
		SELECT ?, printf('manual:%05d', i), ?, 1, 'started', 1, 1, printf('gone-%d', i), ? FROM n`, s.ID, from10, from10)
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow("SELECT state FROM runs WHERE manual = 0").Scan(&state); err != nil || state != "started" || len(keys()) != 1 {
		t.Fatalf("the due run is %q, with creates %q, %v; want it started", state, keys(), err)
	}
}

// The longest of a schedule's texts: a prompt in UTF-16 code units, a
// reason in code points.
const (
	maxPrompt = 4000
	maxReason = 100
	keptRuns  = 50
)

// A thousand schedules of the longest prompt and title, each with a run
// starting and its whole history of the longest reasons, leave a third of
// their database: one measured example, with a folder of 1,024 bytes and
// short model names, not a bound on what schedules can take.
func TestAThousandOfTheLongestSchedulesLeaveAThirdOfTheirDatabase(t *testing.T) {
	p, db, _ := schedulesRuntime(t, "")
	long := func(n int) string { return strings.Repeat("€", n) }
	fields := map[string]any{"title": long(200), "prompt": long(maxPrompt + 1), "workspace": "/" + strings.Repeat("a", 1023), "provider": "codex", "model": "fake", "kind": "interval", "every": 5, "enabled": false}
	if err := serveSchedules(p, "save", map[string]any{"schedule": fields}, nil); err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("a prompt of %d characters answered %v, want refused", maxPrompt+1, err)
	}
	fields["prompt"] = long(maxPrompt)
	s := saveSchedule(t, p, fields)
	sqlExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 999)
		INSERT INTO schedules (id, title, prompt, workspace, worktree, provider, model, effort, fast, access, kind, every, at, weekdays, zone, anchor, missed, enabled, next_due)
		SELECT printf('%026d', n.i), title, prompt, workspace, worktree, provider, model, effort, fast, access, kind, every, at, weekdays, zone, anchor, missed, enabled, next_due
		FROM schedules, n WHERE id = ?`, s.ID)
	sqlExec(t, db, `INSERT INTO runs (schedule, key, due, state, request, sent, tried, at)
		SELECT id, id || '@2026-11-24T10:05:00.000Z', '2026-11-24T10:05:00.000Z', 'starting', json_object('prompt', prompt, 'options', json_object('title', title, 'workspace', workspace,
			'model', json_object('provider', provider, 'model', model, 'effort', effort, 'fast', json('false')), 'access', access,
			'key', json_object('id', id || '@2026-11-24T10:05:00.000Z', 'at', '2026-11-24T10:05:00.000Z'))), 1, 1, '2026-11-24T10:05:00.000Z'
		FROM schedules`)
	sqlExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO runs (schedule, key, due, manual, state, thread, reason, at)
		SELECT id, printf('manual:%s:%036d', id, i), strftime('%Y-%m-%dT%H:%M:%fZ', '2026-01-01', printf('+%d minutes', i)), 1, 'failed', '01M85PZFWMFKKYRY8FQB3M67BZ', ?, '2026-11-24T10:05:00.000Z'
		FROM schedules, n`, keptRuns, long(maxReason))
	var pages, size int
	if err := db.QueryRow("SELECT page_count, page_size FROM pragma_page_count(), pragma_page_size()").Scan(&pages, &size); err != nil {
		t.Fatal(err)
	}
	if pages*size > MaxDBBytes*2/3 {
		t.Fatalf("they take %d MiB of their database's %d", pages*size>>20, MaxDBBytes>>20)
	}
}

// A thread list asked for before a Run now settled doesn't count its thread
// as stopped, though the list answers after: the automatic run due beside
// it skips, as the Run now may still go.
func TestAListAskedForBeforeARunSettledCountsItAsGoing(t *testing.T) {
	hooks := &scheduleHooks{}
	p, db, _ := schedulesRuntimeWith(t, movable, hooks)
	u := dueAt1005(t, p, db, nil)
	s := dueAt1005(t, p, db, nil)
	sqlExec(t, db, "UPDATE schedules SET next_due = ? WHERE id = ?", from10, u.ID)
	sqlExec(t, db, "INSERT INTO runs (schedule, key, due, manual, state, thread, open, at) VALUES (?, 'manual:old', ?, 1, 'started', 'gone', 1, ?)", u.ID, from10, from10)
	if err := p.Call("at", "2026-11-24T10:05:00Z", nil); err != nil {
		t.Fatal(err)
	}
	asked, answer := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hooks.listed = func() ([]map[string]string, error) {
		once.Do(func() {
			close(asked)
			<-answer
		})
		return nil, nil
	}
	ticked := make(chan error, 1)
	go func() { ticked <- p.Call("timer", nil, nil) }()
	<-asked
	if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID, "op": op("2026-11-24T10:05:00Z", 1)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Call("wait", 1000, nil); err != nil {
		t.Fatal(err)
	}
	close(answer)
	if err := <-ticked; err != nil {
		t.Fatal(err)
	}
	var manualOpen int
	var automatic string
	if err := db.QueryRow("SELECT open FROM runs WHERE schedule = ? AND manual = 1", s.ID).Scan(&manualOpen); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT state || ' ' || coalesce(reason, '') FROM runs WHERE schedule = ? AND manual = 0", s.ID).Scan(&automatic); err != nil {
		t.Fatal(err)
	}
	if manualOpen != 1 || automatic != "skipped the previous run is still going" {
		t.Fatalf("the Run now is open %d, the automatic run %q; want it open and that run skipped", manualOpen, automatic)
	}
}

// A Run now tried again once its run has left the history answers the
// thread it made and starts no other, for 7 days; then it is refused, and
// never runs again.
func TestARunNowTriedAgainPastItsHistoryStartsNoOther(t *testing.T) {
	p, db, keys := schedulesRuntime(t, movable)
	s := saveSchedule(t, p, map[string]any{"enabled": false})
	first := op(from10, 1)
	var made, again struct{ Thread string }
	if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID, "op": first}, &made); err != nil {
		t.Fatal(err)
	}
	if err := p.Call("at", "2026-11-24T10:01:00Z", nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	sqlExec(t, db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO runs (schedule, key, due, manual, state, at) SELECT ?, 'manual:newer-' || i, '2026-11-24T10:00:30.000Z', 1, 'failed', '2026-11-24T10:00:30.000Z' FROM n`, keptRuns, s.ID)
	if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID, "op": op("2026-11-24T10:01:00Z", 2)}, nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range runsOf(t, db, s.ID) {
		if r.Key == "manual:"+s.ID+":"+first {
			t.Fatalf("the first Run now is still in the history: %+v", r)
		}
	}
	if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID, "op": first}, &again); err != nil {
		t.Fatal(err)
	}
	if made.Thread == "" || again.Thread != made.Thread || len(keys()) != 2 {
		t.Fatalf("tried again, the first Run now answered %q, first %q, with creates %q; want its thread and no other create", again.Thread, made.Thread, keys())
	}
	if err := p.Call("at", "2026-12-01T10:02:00Z", nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID, "op": first}, nil); err == nil || !strings.Contains(err.Error(), "7 days") || len(keys()) != 2 {
		t.Fatalf("a week on, the first Run now answered %v, with creates %q; want refused, as more than 7 days old", err, keys())
	}
}

// A tick whose check of the threads that may still go takes its whole
// budget still gives a Run now left starting and a due schedule a turn.
func TestASlowThreadCheckHoldsUpNoRun(t *testing.T) {
	var p *plugin.Plugin
	hooks := &scheduleHooks{listed: func() ([]map[string]string, error) {
		return []map[string]string{{"id": "working", "status": "working"}}, p.Call("wait", 21_000, nil)
	}}
	p, db, keys := schedulesRuntimeWith(t, movable, hooks)
	a := saveSchedule(t, p, map[string]any{"enabled": false})
	b := dueAt1005(t, p, db, nil)
	if err := p.Call("at", "2026-11-24T10:05:00Z", nil); err != nil {
		t.Fatal(err)
	}
	sqlExec(t, db, "INSERT INTO runs (schedule, key, due, manual, state, thread, open, at) VALUES (?, 'manual:working', ?, 1, 'started', 'working', 1, ?)", a.ID, from10, from10)
	intent(t, db, a.ID, "manual:waiting", from10, true, true)
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	var started int
	if err := db.QueryRow("SELECT count(*) FROM runs WHERE key IN ('manual:waiting', ?) AND state = 'started'", b.ID+"@"+due1005).Scan(&started); err != nil || started != 2 || len(keys()) != 2 {
		t.Fatalf("%d of the Run now and the due run started, with creates %q, %v; want both", started, keys(), err)
	}
}

// Schedules due beside each other take turns when a tick has time for one
// run: the one due longest ago goes first.
func TestDueSchedulesTakeTurnsUnderLoad(t *testing.T) {
	var p *plugin.Plugin
	hooks := &scheduleHooks{created: func(string) error { return p.Call("wait", 21_000, nil) }}
	p, db, _ := schedulesRuntimeWith(t, movable, hooks)
	for range 6 {
		dueAt1005(t, p, db, nil)
	}
	for i := range 30 {
		if err := p.Call("at", fmt.Sprintf("2026-11-24T10:%02d:00Z", 5+i), nil); err != nil {
			t.Fatal(err)
		}
		if err := p.Call("timer", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Query("SELECT count(r.key) FROM schedules s LEFT JOIN runs r ON r.schedule = s.id AND r.state = 'started' GROUP BY s.id ORDER BY s.id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var starts []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		starts = append(starts, n)
	}
	if !slices.Equal(starts, []int{5, 5, 5, 5, 5, 5}) {
		t.Fatalf("in 30 ticks of one run each, the schedules started %v runs; want 5 each", starts)
	}
}

// A schedule has 20 Run nows starting at most: one more is refused.
func TestAScheduleHasTwentyRunNowsStartingAtMost(t *testing.T) {
	hooks := &scheduleHooks{wrote: func(q string) error {
		if strings.Contains(q, "INSERT INTO runs") {
			return errors.New("cut once the run was written")
		}
		return nil
	}}
	p, db, keys := schedulesRuntimeWith(t, movable, hooks)
	s := saveSchedule(t, p, map[string]any{"enabled": false})
	for i := range 20 {
		if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID, "op": op(from10, byte(i))}, nil); err == nil || !strings.Contains(err.Error(), "cut once") {
			t.Fatalf("Run now %d answered %v, want the cut", i, err)
		}
	}
	hooks.wrote = nil
	if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID, "op": op(from10, 20)}, nil); err == nil || !strings.Contains(err.Error(), "20 Run nows") {
		t.Fatalf("the 21st Run now answered %v, want refused", err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM runs WHERE manual = 1 AND state = 'starting'").Scan(&n); err != nil || n != 20 || len(keys()) != 0 {
		t.Fatalf("%d Run nows starting, with creates %q, %v; want 20 and none sent", n, keys(), err)
	}
}

// Every reason a run records is its first 100 code points, as SQLite counts
// them: a create's error and a failed check of its schedule's threads
// alike.
func TestEveryReasonARunRecordsIsClipped(t *testing.T) {
	long := errors.New(strings.Repeat("é", 420))
	hooks := &scheduleHooks{
		created: func(string) error { return long },
		listed:  func() ([]map[string]string, error) { return nil, long },
	}
	p, db, _ := schedulesRuntimeWith(t, at1017, hooks)
	s := dueAt1005(t, p, db, nil)
	sqlExec(t, db, "INSERT INTO runs (schedule, key, due, manual, state, thread, open, at) VALUES (?, 'manual:old', ?, 1, 'started', 'gone', 1, ?)", s.ID, from10, from10)
	if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID, "op": op("2026-11-24T10:17:00Z", 1)}, nil); err != nil {
		t.Fatal(err)
	}
	p.Call("timer", nil, nil)
	var reasons []string
	for _, r := range runsOf(t, db, s.ID) {
		if r.State == "failed" {
			reasons = append(reasons, r.Reason)
		}
	}
	if len(reasons) != 2 {
		t.Fatalf("failed runs' reasons %q, want the Run now's and the automatic run's", reasons)
	}
	for _, r := range reasons {
		if utf8.RuneCountInString(r) > maxReason {
			t.Errorf("a reason of %d code points: %q", utf8.RuneCountInString(r), r)
		}
	}
}

// A Run now left starting is tried on the next tick though the clock went
// back since it was written: its place in the order of tries is no time.
func TestAClockSetBackHoldsUpNoRunNowLeftStarting(t *testing.T) {
	hooks := &scheduleHooks{wrote: func(q string) error {
		if strings.Contains(q, "INSERT INTO runs") {
			return errors.New("cut once the run was written")
		}
		return nil
	}}
	p, db, keys := schedulesRuntimeWith(t, movable, hooks)
	s := saveSchedule(t, p, map[string]any{"enabled": false})
	if err := p.Call("at", "2026-11-24T10:17:00Z", nil); err != nil {
		t.Fatal(err)
	}
	if err := serveSchedules(p, "runNow", map[string]any{"id": s.ID, "op": op("2026-11-24T10:17:00Z", 1)}, nil); err == nil {
		t.Fatal("Run now answered though its record was cut")
	}
	hooks.wrote = nil
	p.Close()
	if err := p.Call("at", "2026-11-24T09:17:00Z", nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	if runs := runsOf(t, db, s.ID); len(runs) != 1 || runs[0].State != "started" || len(keys()) != 1 {
		t.Fatalf("an hour back, runs %+v with creates %q; want the Run now started", runs, keys())
	}
}

// A cut as a schedule turns off, once it is saved and before the runs it
// hadn't sent are dropped, sends none of them: the next tick drops them.
func TestACutAsAScheduleTurnsOffSendsNoRunItHadntSent(t *testing.T) {
	hooks := &scheduleHooks{}
	p, db, keys := schedulesRuntimeWith(t, at1017, hooks)
	s := dueAt1005(t, p, db, nil)
	intent(t, db, s.ID, s.ID+"@"+due1005, due1005, false, false)
	hooks.wrote = func(q string) error {
		if strings.Contains(q, "UPDATE schedules SET title") {
			return errors.New("cut once the schedule was saved")
		}
		return nil
	}
	off := map[string]any{"id": s.ID, "title": "Nightly", "prompt": "Check.", "workspace": "/project", "provider": "codex", "model": "fake", "kind": "interval", "every": 5, "enabled": false}
	if err := serveSchedules(p, "save", map[string]any{"schedule": off}, nil); err == nil || !strings.Contains(err.Error(), "cut once") {
		t.Fatalf("the save answered %v, want the cut", err)
	}
	hooks.wrote = nil
	if err := p.Call("timer", nil, nil); err != nil {
		t.Fatal(err)
	}
	if runs := runsOf(t, db, s.ID); len(keys()) != 0 || len(runs) != 1 || runs[0].State+" "+runs[0].Reason != "skipped turned off" {
		t.Fatalf("creates %q, runs %+v; want the run skipped, turned off", keys(), runs)
	}
}

// A database of an earlier build of Schedules, from before its release, is
// reset as the service starts: what it held goes, and the service works.
func TestADatabaseOfAnEarlierBuildIsReset(t *testing.T) {
	p, db, _ := schedulesRuntime(t, "")
	saveSchedule(t, p, nil)
	p.Close()
	sqlExec(t, db, `DROP TABLE ops; DROP TABLE runs; DROP TABLE schedules; UPDATE meta SET value = '1' WHERE key = 'schema';
		CREATE TABLE schedules (id TEXT PRIMARY KEY, title TEXT NOT NULL); INSERT INTO schedules VALUES ('01M85G4HXV540JRFTZA18F6P9N', 'old');
		CREATE TABLE runs (schedule TEXT NOT NULL, key TEXT NOT NULL, due TEXT NOT NULL, state TEXT NOT NULL, at TEXT NOT NULL, PRIMARY KEY (schedule, key))`)
	var page struct{ Schedules []schedule }
	if err := serveSchedules(p, "list", nil, &page); err != nil || len(page.Schedules) != 0 {
		t.Fatalf("an earlier build's database lists %+v, %v; want it reset, with none", page.Schedules, err)
	}
	saveSchedule(t, p, nil)
}
