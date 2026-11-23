package claude_test

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	eng "glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

const idleTurn = `{"sent":{"request":{"subtype":"initialize"},"type":"control_request"}}
{"recv":{"response":{"request_id":"glitched-init","response":{},"subtype":"success"},"type":"control_response"}}
{"sent":{"message":{"content":"work","role":"user"},"type":"user"}}
{"recv":{"type":"system","subtype":"init","session_id":"11111111-2222-8333-8444-555555555555"}}
{"recv":{"type":"assistant","message":{"model":"claude-haiku-5-5","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":1,"cache_creation_input_tokens":250000,"cache_creation":{"ephemeral_1h_input_tokens":250000}}}}}
{"recv":{"type":"result","subtype":"success","usage":{"input_tokens":1},"modelUsage":{"claude-haiku-5-5":{"contextWindow":1000000}}}}
`

const idleCompaction = `{"sent":{"message":{"content":"/compact","role":"user"},"type":"user"}}
{"recv":{"type":"system","subtype":"compact_boundary","session_id":"11111111-2222-8333-8444-555555555555","compact_metadata":{"trigger":"manual","pre_tokens":250001,"post_tokens":9000}}}
{"recv":{"type":"result","subtype":"success","result":"","local_command":"compact","usage":{"input_tokens":0},"modelUsage":{"claude-haiku-5-5":{"contextWindow":1000000}}}}
`

const idleInit = `{"sent":{"request":{"subtype":"initialize"},"type":"control_request"}}
{"recv":{"response":{"request_id":"glitched-init","response":{},"subtype":"success"},"type":"control_response"}}
`

const idleEnd = `{"eof":true}
{"exit":0}
`

// idleHost runs the real Claude adapter on the fake CLI, with settings
// (the CLI's settings.json in its config folder) and idle compaction on,
// its clock past the 90% of a one-hour cache where Claude Code's own idle
// compaction fires.
func idleHost(t *testing.T, settings string) (*runtime.Host, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, data string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Setenv(envFake, write("turn.jsonl", idleTurn+idleCompaction+idleEnd))
	t.Setenv(envResume, write("resume.jsonl", idleInit+idleCompaction+idleEnd))
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if settings != "" {
		if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(settings), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var ahead atomic.Int64
	ahead.Store(int64(54*time.Minute + 30*time.Second))
	clock := func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }
	ad := &claude.Adapter{Path: must(os.Executable()), Home: home}
	st := must(store.Open(filepath.Join(dir, "host.db")))
	h := must(runtime.New(ctx, st, provider.Mux{"claude": ad}, runtime.WithClock(clock), runtime.WithIdleCompaction(true)))
	t.Cleanup(func() {
		h.Close()
		ad.Close()
		st.Close()
	})
	return h, st
}

// A Claude thread holds no session of its own, its id being the session's:
// a turn that wrote 250k tokens of one-hour cache, its clock at 90% of
// the hour, compacts, and its context reads compacted.
func TestAClaudeThreadOnAnHourCacheCompactsWhileIdle(t *testing.T) {
	h, st := idleHost(t, "")
	thread := newThread(t, h, api.AccessAsk)
	turn(t, h, thread, "work")
	for deadline := time.Now().Add(turnWait); ; time.Sleep(10 * time.Millisecond) {
		th := must(st.Thread(ctx, thread))
		if n := len(th.Runs); n == 2 && th.Runs[1].Silent && th.Runs[1].State == eng.RunFinished && th.Runs[1].Outcome == api.OutcomeCompleted {
			if !th.Context.Stale {
				t.Errorf("after the compaction the context is %+v, want it stale", th.Context)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no idle compaction: runs %+v, context %+v", th.Runs, th.Context)
		}
	}
}

// A user who turned Claude Code's idle compaction off, in the settings
// its CLI reads, gets none from Glitched either.
func TestClaudeSettingsTurnIdleCompactionOff(t *testing.T) {
	for name, settings := range map[string]string{
		"idleCompaction":       `{"idleCompaction": false}`,
		"autoCompactEnabled":   `{"autoCompactEnabled": false}`,
		"DISABLE_AUTO_COMPACT": `{"env": {"DISABLE_AUTO_COMPACT": "1"}}`,
		"a higher threshold":   `{"env": {"CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS": "300000"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			h, st := idleHost(t, settings)
			thread := newThread(t, h, api.AccessAsk)
			turn(t, h, thread, "work")
			time.Sleep(500 * time.Millisecond)
			th := must(st.Thread(ctx, thread))
			if len(th.Runs) != 1 || !th.Context.CacheExpires.IsZero() {
				t.Errorf("runs %+v, cache expiry %v: want one run and none", th.Runs, th.Context.CacheExpires)
			}
		})
	}
}
