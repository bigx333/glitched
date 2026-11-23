package cliupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// lookup answers latest, or fails with err, and counts what it was asked.
type lookup struct {
	mu     sync.Mutex
	latest string
	err    error
	asked  atomic.Int32
	gate   chan struct{} // when set, each lookup waits for it to close
}

func (l *lookup) Latest(ctx context.Context, s Source) (string, error) {
	l.asked.Add(1)
	if l.gate != nil {
		<-l.gate
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.latest, l.err
}

var native = Install{Source: Source{URL: "https://registry.example/claude/latest", Field: "version"}, Command: []string{"claude", "update"}}

// clock is a fake now that a test moves.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func checker(l Lookup, file string, c *clock) *Checker {
	ch := NewChecker(l, file)
	ch.now = c.now
	return ch
}

func TestCheckerAsksOnceADay(t *testing.T) {
	l := &lookup{latest: "2.1.330"}
	c := &clock{time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	file := filepath.Join(t.TempDir(), "cli-versions.json")
	ch := checker(l, file, c)
	ctx := context.Background()

	st := ch.Check(ctx, "claude", "2.1.323", native, false)
	if want := (Status{Installed: "2.1.323", Latest: "2.1.330", Checked: c.t, Available: true, Command: native.Command}); !equal(st, want) {
		t.Fatalf("first check %+v, want %+v", st, want)
	}
	c.t = c.t.Add(23 * time.Hour)
	if st := ch.Check(ctx, "claude", "2.1.323", native, false); st.Latest != "2.1.330" || l.asked.Load() != 1 {
		t.Errorf("within the day: latest %q after %d lookups, want the cached answer and one lookup", st.Latest, l.asked.Load())
	}
	l.latest = "2.1.331"
	if st := ch.Check(ctx, "claude", "2.1.323", native, true); st.Latest != "2.1.331" || l.asked.Load() != 2 {
		t.Errorf("forced: latest %q after %d lookups, want a new lookup", st.Latest, l.asked.Load())
	}
	c.t = c.t.Add(checkEvery)
	l.latest = "2.1.332"
	if st := ch.Check(ctx, "claude", "2.1.332", native, false); st.Latest != "2.1.332" || st.Available || l.asked.Load() != 3 {
		t.Errorf("a day on: %+v after %d lookups, want a new lookup, and the installed one current", st, l.asked.Load())
	}

	again := checker(l, file, c)
	if st := again.Check(ctx, "claude", "2.1.323", native, false); st.Latest != "2.1.332" || l.asked.Load() != 3 {
		t.Errorf("a new checker on the same file: latest %q after %d lookups, want the cached answer", st.Latest, l.asked.Load())
	}
	if st := again.Check(ctx, "codex", "0.170.0", native, false); l.asked.Load() != 4 || st.Latest != "2.1.332" {
		t.Errorf("another provider asks its own: %d lookups", l.asked.Load())
	}
}

// Offline is no news: the status shows only what's installed, and the
// failure counts as the day's lookup.
func TestCheckerOfflineIsSilent(t *testing.T) {
	l := &lookup{err: errors.New("dial tcp: no route to host")}
	c := &clock{time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	ch := checker(l, filepath.Join(t.TempDir(), "cli-versions.json"), c)
	ctx := context.Background()

	if st := ch.Check(ctx, "claude", "2.1.323", native, false); st.Latest != "" || st.Available || st.Installed != "2.1.323" {
		t.Errorf("offline: %+v, want only what's installed", st)
	}
	c.t = c.t.Add(time.Hour)
	ch.Check(ctx, "claude", "2.1.323", native, false)
	if n := l.asked.Load(); n != 1 {
		t.Errorf("%d lookups within the day of a failure, want 1", n)
	}

	l.err, l.latest = nil, "2.1.330"
	ch.Check(ctx, "claude", "2.1.323", native, true)
	l.err = errors.New("offline again")
	if st := ch.Check(ctx, "claude", "2.1.323", native, true); st.Latest != "2.1.330" || !st.Available {
		t.Errorf("a failure after an answer: %+v, want the answer kept", st)
	}
}

// A cache that can't be written still holds the day's lookup for this
// engine.
func TestCheckerThatCantWriteItsCacheStillAsksOnceADay(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cli-versions.json")
	if err := os.Mkdir(file, 0o755); err != nil {
		t.Fatal(err)
	}
	l := &lookup{err: errors.New("offline")}
	ch := NewChecker(l, file)
	for range 2 {
		ch.Check(context.Background(), "claude", "2.1.323", native, false)
	}
	if n := l.asked.Load(); n != 1 {
		t.Errorf("%d lookups for two checks with an unwritable cache, want 1", n)
	}
}

func TestCheckerWithoutLookupLooksNothingUp(t *testing.T) {
	ch := NewChecker(nil, filepath.Join(t.TempDir(), "cli-versions.json"))
	in := Install{Source: native.Source, Command: []string{"sudo", "claude", "update"}, Manual: "needs sudo"}
	st := ch.Check(context.Background(), "claude", "2.1.323", in, true)
	if want := (Status{Installed: "2.1.323", Command: in.Command, Manual: in.Manual}); !equal(st, want) {
		t.Errorf("%+v, want %+v", st, want)
	}
}

func TestChecksAtOnceShareOneLookup(t *testing.T) {
	l := &lookup{latest: "2.1.330", gate: make(chan struct{})}
	ch := NewChecker(l, filepath.Join(t.TempDir(), "cli-versions.json"))
	var wg sync.WaitGroup
	got := make([]Status, 3)
	for i := range got {
		wg.Go(func() { got[i] = ch.Check(context.Background(), "claude", "2.1.323", native, true) })
	}
	for l.asked.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(l.gate)
	wg.Wait()
	if n := l.asked.Load(); n != 1 {
		t.Errorf("%d lookups for three checks at once, want 1", n)
	}
	for _, st := range got {
		if st.Latest != "2.1.330" {
			t.Errorf("a check that waited got %+v", st)
		}
	}
}

func TestHTTPReadsTheField(t *testing.T) {
	var agent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent = r.Header.Get("User-Agent")
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "rust-v0.171.0", "versions": map[string]any{"stable": "0.171.0"}})
	}))
	defer srv.Close()
	for _, s := range []Source{{URL: srv.URL, Field: "tag_name", Trim: "rust-v"}, {URL: srv.URL, Field: "versions.stable"}} {
		got, err := HTTP{}.Latest(context.Background(), s)
		if err != nil || got != "0.171.0" {
			t.Errorf("%+v: %q, %v; want 0.171.0", s, got, err)
		}
	}
	if agent != "glitched" {
		t.Errorf("User-Agent %q, want glitched", agent)
	}
	if _, err := (HTTP{}).Latest(context.Background(), Source{URL: srv.URL, Field: "version"}); err == nil {
		t.Error("a field that isn't there answered")
	}
}

func equal(a, b Status) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}
