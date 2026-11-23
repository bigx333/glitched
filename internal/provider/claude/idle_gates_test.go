package claude_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	eng "glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

const rateWarning = `{"recv":{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","rateLimitType":"five_hour","utilization":0.95}}}
`

type gateIdle struct {
	h          *runtime.Host
	ad         *claude.Adapter
	compact    string
	st         *store.Store
	home, sent string
	ahead      atomic.Int64
}

func gateHost(t *testing.T, tape string) *gateIdle {
	t.Helper()
	dir := t.TempDir()
	f := &gateIdle{home: filepath.Join(dir, "claude"), sent: filepath.Join(dir, "compact-on-stdin"), compact: idleCompaction}
	if err := os.Mkdir(f.home, 0700); err != nil {
		t.Fatal(err)
	}
	f.tape(t, tape)
	resume := filepath.Join(dir, "resume.jsonl")
	data := idleInit + f.compactionTape() + idleEnd
	if err := os.WriteFile(resume, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envResume, resume)
	f.st = must(store.Open(filepath.Join(dir, "host.db")))
	f.start()
	t.Cleanup(func() {
		f.h.Close()
		f.ad.Close()
		f.st.Close()
	})
	return f
}

func (f *gateIdle) start() {
	f.ad = &claude.Adapter{Path: must(os.Executable()), Home: f.home}
	f.h = must(runtime.New(ctx, f.st, provider.Mux{"claude": f.ad}, runtime.WithClock(func() time.Time {
		return time.Now().Add(time.Duration(f.ahead.Load()))
	}), runtime.WithIdleCompaction(false)))
}

func (f *gateIdle) tape(t *testing.T, tape string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "turn.jsonl")
	if err := os.WriteFile(path, []byte(tape+f.compactionTape()+idleEnd), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envFake, path)
}

func (f *gateIdle) compactionTape() string {
	marker := `{"take":"` + f.sent + `"}` + "\n"
	return strings.Replace(f.compact, `{"recv":{"type":"system"`, marker+`{"recv":{"type":"system"`, 1)
}

func (f *gateIdle) assertNoCompactSent(t *testing.T, thread api.ID, why string) {
	t.Helper()
	f.ahead.Store(int64(54*time.Minute + 10*time.Second))
	f.h.SetIdleCompaction(true)
	for deadline := time.Now().Add(1200 * time.Millisecond); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(f.sent); err == nil {
			t.Fatalf("fake Claude received /compact after %s; want no command", why)
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		th := must(f.st.Thread(ctx, thread))
		if len(th.Runs) > 1 && th.Runs[1].State == eng.RunFinished && th.Runs[1].Outcome == api.OutcomeCompleted {
			t.Fatalf("silent compaction completed after %s; want no command", why)
		}
		if time.Now().After(deadline) {
			return
		}
	}
}

func TestAnOptOutSinceTheLastTurnStopsTheCompaction(t *testing.T) {
	f := gateHost(t, idleTurn)
	thread := newThread(t, f.h, api.AccessAsk)
	turn(t, f.h, thread, "work")
	if th := must(f.st.Thread(ctx, thread)); th.Context.CacheExpires.IsZero() {
		t.Fatal("the first turn did not leave an eligible cache")
	}
	if err := f.h.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.ad.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, "settings.json"), []byte(`{"idleCompaction":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	f.start()
	f.assertNoCompactSent(t, thread, "idleCompaction became false before a fresh CLI resume")
}

func TestALateRateWarningStopsTheCompaction(t *testing.T) {
	tape := strings.Replace(idleTurn, `{"recv":{"type":"result"`, rateWarning+`{"recv":{"type":"result"`, 1)
	tape = strings.Replace(tape, `"modelUsage":{"claude-haiku-5-5":{"contextWindow":1000000}}`, `"modelUsage":{}`, 1)
	f := gateHost(t, tape)
	thread := newThread(t, f.h, api.AccessAsk)
	turn(t, f.h, thread, "work")
	f.assertNoCompactSent(t, thread, "Claude reported allowed_warning before a result with empty modelUsage")
}

func TestARateWarningOnAnotherThreadStopsTheCompaction(t *testing.T) {
	f := gateHost(t, idleTurn)
	thread := newThread(t, f.h, api.AccessAsk)
	turn(t, f.h, thread, "work")
	tape := strings.Replace(idleTurn, `{"recv":{"type":"result"`, rateWarning+`{"recv":{"type":"result"`, 1)
	f.tape(t, tape)
	warned := newThread(t, f.h, api.AccessAsk)
	turn(t, f.h, warned, "work")
	if th := must(f.st.Thread(ctx, warned)); !th.Context.CacheExpires.IsZero() {
		t.Fatal("the fake's warning did not clear eligibility on its own thread")
	}
	f.assertNoCompactSent(t, thread, "another thread on the same account reported allowed_warning")
}

func TestNothingToCompactIsNotShownAsCompacted(t *testing.T) {
	f := gateHost(t, idleTurn)
	for _, line := range strings.Split(idleCompaction, "\n") {
		if strings.Contains(line, "compact_boundary") {
			f.compact = strings.Replace(f.compact, line+"\n", "", 1)
			break
		}
	}
	f.compact = strings.Replace(f.compact, `"result":""`, `"result":"Nothing to compact."`, 1)
	f.tape(t, idleTurn)
	if err := os.WriteFile(os.Getenv(envResume), []byte(idleInit+f.compactionTape()+idleEnd), 0600); err != nil {
		t.Fatal(err)
	}
	thread := newThread(t, f.h, api.AccessAsk)
	turn(t, f.h, thread, "work")
	f.ahead.Store(int64(54*time.Minute + 10*time.Second))
	f.h.SetIdleCompaction(true)
	for deadline := time.Now().Add(turnWait); ; time.Sleep(10 * time.Millisecond) {
		th := must(f.st.Thread(ctx, thread))
		if len(th.Runs) > 1 && th.Runs[1].State == eng.RunFinished {
			snap := must(f.h.Read(ctx, api.Query{Thread: thread}))
			var tr client.Transcript
			for _, ev := range snap.Events {
				tr.Apply(ev)
			}
			for _, e := range tr.Entries {
				if c, ok := e.(*client.CompactionEntry); ok && c.Compacted {
					t.Fatalf("client reports Compacted=true after Claude said Nothing to compact and emitted no boundary; context stale=%v", th.Context.Stale)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("compaction did not finish")
		}
	}
}

func TestAPureReadAfterAResumeKeepsTheHourCache(t *testing.T) {
	f := gateHost(t, idleTurn)
	thread := newThread(t, f.h, api.AccessAsk)
	turn(t, f.h, thread, "work")
	earlier := must(f.st.Thread(ctx, thread)).Context.CacheExpires
	if earlier.IsZero() {
		t.Fatal("the first turn did not record a one-hour cache")
	}
	if err := f.h.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.ad.Close(); err != nil {
		t.Fatal(err)
	}
	resumed := strings.Replace(idleTurn, `"cache_creation_input_tokens":250000,"cache_creation":{"ephemeral_1h_input_tokens":250000}`, `"cache_read_input_tokens":250000`, 1)
	if err := os.WriteFile(os.Getenv(envResume), []byte(resumed+f.compactionTape()+idleEnd), 0600); err != nil {
		t.Fatal(err)
	}
	f.start()
	turn(t, f.h, thread, "work")
	th := must(f.st.Thread(ctx, thread))
	if th.Context.Used != 250001 {
		t.Fatalf("resumed cache-reading turn reported %d input tokens, want 250001", th.Context.Used)
	}
	if th.Context.CacheExpires.IsZero() {
		t.Fatal("250000 tokens read from the still-warm one-hour cache after CLI resume, but stored expiry is zero")
	}
	if !th.Context.CacheExpires.After(earlier) {
		t.Fatal("the one-hour cache read did not advance its expiry")
	}
}
