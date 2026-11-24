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
	if out, _ := call("list", nil); string(out) != "[]" {
		t.Fatalf("the refused saves left %s", out)
	}
}

// A tick's slow starts don't hold up the timer that began it: two creates
// of 16 s each, both inside what the host gives one, start both, with no
// timer that didn't answer, and a page's read waits on one create at most.
func TestATickOfSlowStartsLeavesItsTimerFree(t *testing.T) {
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
			time.Sleep(slow)
			mu.Lock()
			creates++
			mu.Unlock()
			return map[string]string{"thread": "01M85GA51SCFWW12ATRT8FAVAF"}, nil
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
		mu.Lock()
		defer mu.Unlock()
		return creates
	}
	for deadline := time.Now().Add(60 * time.Second); started() < n; time.Sleep(500 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d schedules started", started(), n)
		}
		begun := time.Now()
		if _, err := call("list", nil); err != nil || time.Since(begun) > slow+time.Second {
			t.Fatalf("with %d started, the list answered in %v: %v", started(), time.Since(begun), err)
		}
	}
	time.Sleep(2 * time.Second) // the runs' records, and a timer more
	var runs int
	if err := db.QueryRow("SELECT count(*) FROM runs WHERE state = 'started'").Scan(&runs); err != nil || runs != n {
		t.Fatalf("%d runs started, %v, want %d", runs, err, n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(late) > 0 || creates != n {
		t.Fatalf("%d creates, and the host said %q", creates, late)
	}
}
