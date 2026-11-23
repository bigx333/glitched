package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/provider"
)

var fixedNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// contexts is what a turn reads of the lines of a recorded run.
func contexts(t *testing.T, path string) []provider.Context {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tn := newTurn(t.TempDir())
	tn.now = func() time.Time { return fixedNow }
	var got []provider.Context
	s := bufio.NewScanner(f)
	s.Buffer(nil, 1<<20)
	for s.Scan() {
		var m message
		if err := json.Unmarshal(s.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		for _, e := range tn.read(m) {
			if c, ok := e.(provider.Context); ok {
				got = append(got, c)
			}
		}
	}
	return got
}

// Three runs of one haiku session on claude 2.1.330: a reply, /compact,
// and a reply after it. Their 5-minute cache is none to compact before.
func TestACompactionLeavesTheContextUnknownUntilTheNextRequest(t *testing.T) {
	var exp time.Time
	want := [][]provider.Context{
		{{Used: 25041, Model: haiku, CacheExpires: exp}, {Used: 25041, Window: 1000000, Model: haiku, CacheExpires: exp}},
		{{Compacted: true, PreTokens: 25050, PostTokens: 5734}},
		{{Used: 25797, Model: haiku, CacheExpires: exp}, {Used: 25797, Window: 1000000, Model: haiku, CacheExpires: exp}},
	}
	for i, w := range want {
		path := filepath.Join("testdata", "compacted", string(rune('1'+i))+".jsonl")
		if got := contexts(t, path); !slices.Equal(got, w) {
			t.Errorf("run %d reported %+v, want %+v", i+1, got, w)
		}
	}
}

func TestTheWindowIsTheLastRequestsModels(t *testing.T) {
	models := map[string]modelUsage{
		"claude-opus-5-5":  {Canonical: "claude-opus-5-5", ContextWindow: 200000},
		"claude-haiku-5-5": {Canonical: "claude-haiku-5-5", ContextWindow: 1000000},
	}
	for model, want := range map[string]int64{
		"claude-opus-5-5":  200000,
		"claude-haiku-5-5": 1000000,
		"claude-other":     0,
	} {
		if got := window(models, model); got != want {
			t.Errorf("the window of %s is %d, want %d", model, got, want)
		}
	}
	canonical := map[string]modelUsage{"opus[1m]": {Canonical: "claude-opus-5-5", ContextWindow: 1000000}, "haiku": {Canonical: "claude-haiku-5-5", ContextWindow: 1}}
	if got := window(canonical, "claude-opus-5-5"); got != 1000000 {
		t.Errorf("the window by canonical name is %d, want 1000000", got)
	}
	if got := window(map[string]modelUsage{"claude-haiku-5-5": {ContextWindow: 1000000}}, ""); got != 0 {
		t.Errorf("a request of no model said took haiku's window %d", got)
	}
	ambiguous := map[string]modelUsage{"opus": {Canonical: "claude-opus-5-5", ContextWindow: 200000}, "opus[1m]": {Canonical: "claude-opus-5-5", ContextWindow: 1000000}}
	for range 20 {
		if got := window(ambiguous, "claude-opus-5-5"); got != 0 {
			t.Fatalf("two windows under one canonical name gave %d, want none", got)
		}
	}
	unsaid := map[string]modelUsage{"opus": {Canonical: "claude-opus-5-5"}, "opus[1m]": {Canonical: "claude-opus-5-5", ContextWindow: 1000000}}
	for range 20 {
		if got := window(unsaid, "claude-opus-5-5"); got != 0 {
			t.Fatalf("a window one entry of the name leaves out gave %d, want none", got)
		}
	}
	if got := window(map[string]modelUsage{"claude-haiku-5-5": {Canonical: "claude-haiku-5-5", ContextWindow: 1000000}}, "claude-opus-5-5"); got != 0 {
		t.Errorf("another model's window counted for opus: %d", got)
	}
}

func line(t *testing.T, s string) message {
	t.Helper()
	var m message
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func read(tn *turn, ms ...message) []provider.Context {
	var got []provider.Context
	for _, m := range ms {
		for _, e := range tn.read(m) {
			if c, ok := e.(provider.Context); ok {
				got = append(got, c)
			}
		}
	}
	return got
}

const twoModels = `{"type":"result","subtype":"success","usage":{},"modelUsage":{"claude-opus-5-5":{"canonicalModel":"claude-opus-5-5","contextWindow":200000},"claude-haiku-5-5":{"canonicalModel":"claude-haiku-5-5","contextWindow":1000000}}}`

// The window is the model's the last request went to, even where that
// request sent as much as the one before it.
func TestTheWindowFollowsTheLastRequestsModel(t *testing.T) {
	got := read(newTurn(t.TempDir()),
		line(t, `{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":40000}}}`),
		line(t, `{"type":"assistant","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":40000}}}`),
		line(t, twoModels))
	if want := []provider.Context{{Used: 40000, Model: "claude-opus-5-5"}, {Used: 40000, Model: haiku}, {Used: 40000, Window: 1000000, Model: haiku}}; !slices.Equal(got, want) {
		t.Errorf("reported %+v, want %+v: haiku's window", got, want)
	}
}

// A request's message_start says what it sent before its reply comes, so
// a reply stopped midway still measures the context.
func TestARequestsStartMeasuresTheContext(t *testing.T) {
	got := read(newTurn(t.TempDir()),
		line(t, `{"type":"stream_event","event":{"type":"message_start","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":2,"cache_read_input_tokens":32000,"cache_creation_input_tokens":158}}}}`),
		line(t, `{"type":"stream_event","parent_tool_use_id":"toolu_1","event":{"type":"message_start","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":90000}}}}`),
		line(t, `{"type":"assistant","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":2,"cache_read_input_tokens":32000,"cache_creation_input_tokens":158}}}`),
		line(t, twoModels))
	if want := []provider.Context{{Used: 32160, Model: haiku}, {Used: 32160, Window: 1000000, Model: haiku}}; !slices.Equal(got, want) {
		t.Errorf("reported %+v, want %+v: once from the start, a subagent's left out", got, want)
	}
}

// A result that doesn't end the turn, as a late steer's first, still
// gives the window it says.
func TestAResultBeforeTheLastGivesItsWindow(t *testing.T) {
	tn := newTurn(t.TempDir())
	read(tn, line(t, `{"type":"assistant","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":32002}}}`))
	var got []provider.Context
	for _, e := range tn.tally(line(t, twoModels)) {
		got = append(got, e.(provider.Context))
	}
	if want := []provider.Context{{Used: 32002, Window: 1000000, Model: haiku}}; !slices.Equal(got, want) {
		t.Errorf("an early result reported %+v, want %+v", got, want)
	}
}

// A request's cache lifetime is the one most of what it wrote has, a
// pure read refreshes the lifetime the cache had, and only a one-hour
// cache over the CLI's threshold, with idle compaction on and the account
// clear of its limit, is reported as one to compact before.
func TestCacheCreationTTLAndPureCacheRead(t *testing.T) {
	now := fixedNow
	cache := &cacheState{idle: true, min: 200_000}
	turnOn := func() *turn {
		tn := newTurn(t.TempDir())
		tn.now = func() time.Time { return now }
		tn.cache = cache
		return tn
	}
	tn := turnOn()
	got := read(tn,
		line(t, `{"type":"stream_event","event":{"type":"message_start","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":10,"cache_creation_input_tokens":1000,"cache_read_input_tokens":200000,"cache_creation":{"ephemeral_1h_input_tokens":1000,"ephemeral_5m_input_tokens":500}}}}}`),
	)
	now = now.Add(30 * time.Second)
	got = append(got, read(tn,
		line(t, `{"type":"assistant","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":10,"cache_creation_input_tokens":1000,"cache_read_input_tokens":200000,"cache_creation":{"ephemeral_1h_input_tokens":1000}}}}`),
		line(t, twoModels),
	)...)
	want1h := fixedNow.Add(time.Hour)
	want := []provider.Context{
		{Used: 201010, Model: haiku, CacheExpires: want1h},
		{Used: 201010, Window: 1000000, Model: haiku, CacheExpires: want1h},
	}
	if !slices.Equal(got, want) {
		t.Errorf("1h cache reported %+v, want %+v", got, want)
	}

	now = fixedNow.Add(10 * time.Minute)
	got = read(turnOn(),
		line(t, `{"type":"assistant","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":5,"cache_read_input_tokens":250000}}}`),
	)
	if want := []provider.Context{{Used: 250005, Model: haiku, CacheExpires: now.Add(time.Hour)}}; !slices.Equal(got, want) {
		t.Errorf("a pure read of a 1h cache reported %+v, want %+v: the hit refreshes it", got, want)
	}

	got = read(turnOn(),
		line(t, `{"type":"assistant","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":1,"cache_creation_input_tokens":250000,"cache_creation":{"ephemeral_1h_input_tokens":1000,"ephemeral_5m_input_tokens":249000}}}}`),
	)
	if want := []provider.Context{{Used: 250001, Model: haiku}}; !slices.Equal(got, want) {
		t.Errorf("a request that wrote mostly a 5m cache reported %+v, want %+v", got, want)
	}

	for name, c := range map[string]cacheState{
		"idle compaction off":  {idle: false, min: 200_000},
		"below the threshold":  {idle: true, min: 300_000},
		"near the usage limit": {idle: true, min: 200_000, limited: limitedAccount()},
	} {
		cache = &c
		got = read(turnOn(),
			line(t, `{"type":"assistant","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":1,"cache_creation_input_tokens":250000,"cache_creation":{"ephemeral_1h_input_tokens":250000}}}}`),
		)
		if want := []provider.Context{{Used: 250001, Model: haiku}}; !slices.Equal(got, want) {
			t.Errorf("%s: reported %+v, want %+v", name, got, want)
		}
	}

	cache = &cacheState{idle: true, min: 200_000, limited: new(atomic.Bool)}
	tn = turnOn()
	read(tn, line(t, `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning"}}`))
	if !cache.limited.Load() {
		t.Error("a rate_limit_event that isn't allowed left the account clear")
	}
	read(tn, line(t, `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`))
	if cache.limited.Load() {
		t.Error("an allowed rate_limit_event left the account limited")
	}
}

const haiku = "claude-haiku-5-5"

func limitedAccount() *atomic.Bool {
	l := new(atomic.Bool)
	l.Store(true)
	return l
}
