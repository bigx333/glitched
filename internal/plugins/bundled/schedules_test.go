package bundled_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/bundled"
	"glitched/internal/plugins/pack"
)

// schedulesService runs the built-in Schedules' service in state, with o,
// as the user's own copy, signed with the device's key, as the release key
// signs it only once sign-bundled runs. It answers call(method, args).
func schedulesService(t *testing.T, state string, o plugins.ServiceOptions) func(method string, args any) (json.RawMessage, error) {
	t.Helper()
	sub, err := fs.Sub(bundled.FS, "schedules")
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(plugins.Dir(state), "schedules")
	if err := os.CopyFS(dst, sub); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dst, pack.SignatureFile))
	key, err := pack.LoadKey(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(dst, key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "plugins.json"), []byte(`{"grants": {"schedules": ["service", "threads:read", "threads:write", "threads:access"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if o.Logf == nil {
		o.Logf = t.Logf
	}
	s := plugins.StartServices(state, o)
	t.Cleanup(s.Close)
	return func(method string, args any) (json.RawMessage, error) {
		raw, _ := json.Marshal(args)
		return s.Call(context.Background(), plugins.ServiceCall{Plugin: "schedules", Method: method, Args: raw})
	}
}

// schedule is a schedule the page could save, with fields over it.
func schedule(fields map[string]any) map[string]any {
	s := map[string]any{"title": "Nightly", "prompt": "Check the build.", "workspace": "/src/app", "provider": "codex", "model": "gpt-6-luna"}
	for k, v := range fields {
		s[k] = v
	}
	return s
}

// A schedule runs when its zone's clocks say: a time the clocks skip runs
// as the hour after, a time they show twice runs the first time only, on
// its weekdays alone; an interval counts minutes from its anchor whatever
// the clocks do.
func TestASchedulesNextRunsFollowItsZonesClocks(t *testing.T) {
	call := schedulesService(t, t.TempDir(), plugins.ServiceOptions{})
	for _, c := range []struct {
		name     string
		schedule map[string]any
		from     string
		want     []string
	}{
		{"clocks go forward past 02:30 in Berlin", schedule(map[string]any{"kind": "weekly", "at": "02:30", "zone": "Europe/Berlin", "anchor": "2026-01-01T00:00:00Z"}), "2026-03-28T12:00:00Z",
			[]string{"2026-03-29T01:30:00.000Z", "2026-03-30T00:30:00.000Z", "2026-03-31T00:30:00.000Z"}},
		{"clocks go back over 02:30 in Berlin", schedule(map[string]any{"kind": "weekly", "at": "02:30", "zone": "Europe/Berlin", "anchor": "2026-01-01T00:00:00Z"}), "2026-10-24T12:00:00Z",
			[]string{"2026-10-25T00:30:00.000Z", "2026-10-26T01:30:00.000Z", "2026-10-27T01:30:00.000Z"}},
		{"weekdays at 09:00 in New York, from a Friday's", schedule(map[string]any{"kind": "weekly", "at": "09:00", "weekdays": []int{1, 2, 3, 4, 5}, "zone": "America/New_York", "anchor": "2026-01-01T00:00:00Z"}), "2026-11-27T14:00:00Z",
			[]string{"2026-11-30T14:00:00.000Z", "2026-12-01T14:00:00.000Z", "2026-12-02T14:00:00.000Z"}},
		{"Sundays at 23:45 in Kolkata", schedule(map[string]any{"kind": "weekly", "at": "23:45", "weekdays": []int{7}, "zone": "Asia/Kolkata", "anchor": "2026-01-01T00:00:00Z"}), "2026-11-24T00:00:00Z",
			[]string{"2026-11-29T18:15:00.000Z", "2026-12-06T18:15:00.000Z", "2026-12-13T18:15:00.000Z"}},
		{"a weekly time before its anchor", schedule(map[string]any{"kind": "weekly", "at": "09:00", "zone": "UTC", "anchor": "2026-11-24T10:00:00Z"}), "2026-11-20T00:00:00Z",
			[]string{"2026-11-25T09:00:00.000Z", "2026-11-26T09:00:00.000Z", "2026-11-27T09:00:00.000Z"}},
		{"every 45 minutes from its anchor", schedule(map[string]any{"kind": "interval", "every": 45, "anchor": "2026-11-24T07:10:00Z"}), "2026-11-24T08:00:00Z",
			[]string{"2026-11-24T08:40:00.000Z", "2026-11-24T09:25:00.000Z", "2026-11-24T10:10:00.000Z"}},
		{"every 45 minutes, from before its anchor", schedule(map[string]any{"kind": "interval", "every": 45, "anchor": "2026-11-24T07:10:00Z"}), "2026-11-23T00:00:00Z",
			[]string{"2026-11-24T07:10:00.000Z", "2026-11-24T07:55:00.000Z", "2026-11-24T08:40:00.000Z"}},
		{"hourly across Berlin's clocks going forward", schedule(map[string]any{"kind": "interval", "every": 60, "zone": "Europe/Berlin", "anchor": "2026-03-28T23:30:00Z"}), "2026-03-29T00:00:00Z",
			[]string{"2026-03-29T00:30:00.000Z", "2026-03-29T01:30:00.000Z", "2026-03-29T02:30:00.000Z"}},
		{"Saturdays at 23:30 in Nuuk, pushed past midnight by the clocks", schedule(map[string]any{"kind": "weekly", "at": "23:30", "weekdays": []int{6}, "zone": "America/Nuuk", "anchor": "2026-01-01T00:00:00Z"}), "2026-03-29T01:10:00Z",
			[]string{"2026-03-29T01:30:00.000Z", "2026-04-05T00:30:00.000Z", "2026-04-12T00:30:00.000Z"}},
	} {
		out, err := call("preview", map[string]any{"schedule": c.schedule, "from": c.from, "count": 3})
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		var got []string
		json.Unmarshal(out, &got)
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s: runs at %v, want %v", c.name, got, c.want)
		}
	}
}

// A schedule the service can't run is refused whole, saying why.
func TestAScheduleItCantRunIsRefused(t *testing.T) {
	call := schedulesService(t, t.TempDir(), plugins.ServiceOptions{})
	for _, c := range []struct {
		fields map[string]any
		want   string
	}{
		{map[string]any{"kind": "interval", "every": 4}, "every is whole minutes, from 5"},
		{map[string]any{"kind": "interval", "every": 30.5}, "every is whole minutes"},
		{map[string]any{"kind": "weekly", "at": "25:00"}, "at is HH:MM"},
		{map[string]any{"kind": "weekly", "at": "09:00", "weekdays": []int{0}}, "weekdays are 1 (Monday) to 7 (Sunday)"},
		{map[string]any{"kind": "weekly", "at": "09:00", "zone": "Mars/Olympus"}, "zone is an IANA time zone"},
		{map[string]any{"kind": "daily"}, "kind is interval or weekly"},
		{map[string]any{"kind": "interval", "every": 30, "access": "root"}, "access is one of"},
		{map[string]any{"kind": "interval", "every": 30, "missed": "twice"}, "missed is once or skip"},
		{map[string]any{"kind": "interval", "every": 30, "workspace": "src/app"}, "workspace is an absolute folder"},
		{map[string]any{"kind": "interval", "every": 30, "prompt": " "}, "prompt is text"},
		{map[string]any{"kind": "interval", "every": 30, "anchor": "2026-02-30T00:00:00Z"}, "anchor is an RFC 3339 time"},
		{map[string]any{"kind": "interval", "every": 30, "anchor": "2026-11-24T09:00Z"}, "anchor is an RFC 3339 time"},
	} {
		if _, err := call("save", map[string]any{"schedule": schedule(c.fields)}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want an error saying %q", c.fields, err, c.want)
		}
	}
	if out, _ := call("list", nil); string(out) != `{"schedules":[],"next":null}` {
		t.Fatalf("the refused saves left %s", out)
	}
}

// A tick waits on its slow creates, past the 30 s of a call, inside its
// timer's minute: two creates of 16 s each, as an engine answers one out
// of its queue, start both, with no timer that didn't answer, and the
// page's reads answer while they go.
func TestATickWaitsOnItsSlowCreates(t *testing.T) {
	state := t.TempDir()
	const slow = 16 * time.Second
	var mu sync.Mutex
	var creates int
	var late []string
	call := schedulesService(t, state, plugins.ServiceOptions{TimerScale: 0.02,
		Logf: func(format string, args ...any) {
			if line := fmt.Sprintf(format, args...); strings.Contains(line, "did not answer") {
				mu.Lock()
				late = append(late, line)
				mu.Unlock()
			}
		},
		Do: func(_ string, e plugins.Effect) (any, error) {
			if e.Type != "threads.create" {
				return nil, fmt.Errorf("no %s here", e.Type)
			}
			return plugins.Pending(func(done func(any, error)) {
				time.AfterFunc(slow, func() {
					mu.Lock()
					creates++
					mu.Unlock()
					done(map[string]string{"thread": "01M85GA51SCFWW12ATRT8FAVAF"}, nil)
				})
			}), nil
		}})
	const n = 2
	for range n {
		if _, err := call("save", map[string]any{"schedule": schedule(map[string]any{"kind": "interval", "every": 60})}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(plugins.DataDir(state), "schedules", "plugin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	due := time.Now().Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := db.Exec("UPDATE schedules SET anchor = ?, next_due = ?", due, due); err != nil {
		t.Fatal(err)
	}
	started := func() int {
		var runs int
		if err := db.QueryRow("SELECT count(*) FROM runs WHERE state = 'started'").Scan(&runs); err != nil {
			t.Fatal(err)
		}
		return runs
	}
	for deadline := time.Now().Add(60 * time.Second); started() < n; time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d schedules started", started(), n)
		}
		begun := time.Now()
		if _, err := call("list", nil); err != nil || time.Since(begun) > time.Second {
			t.Fatalf("with %d started, the list answered in %v: %v", started(), time.Since(begun), err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(late) > 0 || creates != n {
		t.Fatalf("%d creates, and the host said %q", creates, late)
	}
}

// Run nows at once, as many as a schedule has starting at most, are a run
// each, with the key it was created under and the thread that key made.
func TestRunNowsAtOnceAreARunEach(t *testing.T) {
	var mu sync.Mutex
	created := map[string]string{}
	call := schedulesService(t, t.TempDir(), plugins.ServiceOptions{
		Do: func(_ string, e plugins.Effect) (any, error) {
			if e.Type != "threads.create" {
				return nil, fmt.Errorf("no %s here", e.Type)
			}
			var a struct{ Key struct{ ID string } }
			if err := json.Unmarshal(e.Args, &a); err != nil {
				return nil, err
			}
			mu.Lock()
			defer mu.Unlock()
			thread := fmt.Sprintf("%026d", len(created)+1)
			created[a.Key.ID] = thread
			return map[string]string{"thread": thread}, nil
		}})
	raw, err := call("save", map[string]any{"schedule": schedule(map[string]any{"kind": "interval", "every": 60})})
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ ID string }
	json.Unmarshal(raw, &s)
	const n = 20
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if _, err := call("runNow", map[string]any{"id": s.ID, "op": api.NewID().String()}); err != nil {
				t.Errorf("runNow: %v", err)
			}
		})
	}
	wg.Wait()
	raw, err = call("runs", map[string]any{"id": s.ID, "limit": n})
	if err != nil {
		t.Fatal(err)
	}
	var runs []struct{ Key, Thread string }
	json.Unmarshal(raw, &runs)
	if len(runs) != n || len(created) != n {
		t.Fatalf("%d runs of %d creates, want %d", len(runs), len(created), n)
	}
	for _, r := range runs {
		if created[r.Key] != r.Thread {
			t.Fatalf("run %+v isn't the thread its key made, %s", r, created[r.Key])
		}
	}
}

// A list read page by page while schedules are renamed and deleted lists
// each schedule still there once, and none deleted before its page: a
// rename moves no schedule from one page to another.
func TestAListReadWhileSchedulesAreEditedListsEachOnce(t *testing.T) {
	call := schedulesService(t, t.TempDir(), plugins.ServiceOptions{})
	type saved struct{ ID, Title string }
	var all []saved
	for i := range 150 {
		out, err := call("save", map[string]any{"schedule": schedule(map[string]any{"title": fmt.Sprintf("s%03d", i), "kind": "interval", "every": 30})})
		if err != nil {
			t.Fatal(err)
		}
		var s saved
		json.Unmarshal(out, &s)
		all = append(all, s)
	}
	type page struct {
		Schedules []saved
		Next      any
	}
	read := func(after any) page {
		out, err := call("list", map[string]any{"after": after})
		if err != nil {
			t.Fatal(err)
		}
		var p page
		json.Unmarshal(out, &p)
		return p
	}
	first := read(nil)
	if len(first.Schedules) != 100 || first.Next == nil {
		t.Fatalf("the first page has %d schedules and next %v", len(first.Schedules), first.Next)
	}
	// Between the pages: one listed already is renamed to sort last, one
	// not yet listed is renamed to sort first, and one not yet listed is
	// deleted.
	listed := first.Schedules[0]
	seen := map[string]int{}
	for _, s := range first.Schedules {
		seen[s.ID]++
	}
	var rest []saved
	for _, s := range all {
		if seen[s.ID] == 0 {
			rest = append(rest, s)
		}
	}
	unlisted, gone := rest[0], rest[1]
	for _, edit := range []struct {
		s     saved
		title string
	}{{listed, "zzz"}, {unlisted, "aaa"}} {
		s := schedule(map[string]any{"id": edit.s.ID, "title": edit.title, "kind": "interval", "every": 30})
		if _, err := call("save", map[string]any{"schedule": s}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := call("delete", map[string]any{"id": gone.ID}); err != nil {
		t.Fatal(err)
	}
	for p := first; p.Next != nil; {
		p = read(p.Next)
		for _, s := range p.Schedules {
			seen[s.ID]++
		}
	}
	for _, s := range all {
		want := 1
		if s.ID == gone.ID {
			want = 0
		}
		if seen[s.ID] != want {
			t.Errorf("%s (%s) listed %d times, want %d", s.ID, s.Title, seen[s.ID], want)
		}
	}
}

// A save over a revision is refused once another save or a delete came
// after it was read: an edit brings back no schedule deleted since, and
// undoes no switch since.
func TestASaveOverAnOldRevisionIsRefused(t *testing.T) {
	call := schedulesService(t, t.TempDir(), plugins.ServiceOptions{})
	out, err := call("save", map[string]any{"schedule": schedule(map[string]any{"kind": "interval", "every": 30})})
	if err != nil {
		t.Fatal(err)
	}
	var read struct {
		ID       string
		Revision int
	}
	json.Unmarshal(out, &read)
	if _, err := call("save", map[string]any{"schedule": map[string]any{"id": read.ID, "enabled": false, "revision": read.Revision}}); err != nil {
		t.Fatal(err)
	}
	edit := schedule(map[string]any{"id": read.ID, "title": "Edited", "kind": "interval", "every": 30, "revision": read.Revision})
	if _, err := call("save", map[string]any{"schedule": edit}); err == nil || !strings.Contains(err.Error(), "saved since it was read") {
		t.Fatalf("an edit over the revision before the switch: %v", err)
	}
	if _, err := call("delete", map[string]any{"id": read.ID}); err != nil {
		t.Fatal(err)
	}
	edit["revision"] = read.Revision + 1
	if _, err := call("save", map[string]any{"schedule": edit}); err == nil || !strings.Contains(err.Error(), "was deleted") {
		t.Fatalf("an edit of a schedule deleted since: %v", err)
	}
	if out, _ := call("list", nil); string(out) != `{"schedules":[],"next":null}` {
		t.Fatalf("the refused edit left %s", out)
	}
}
