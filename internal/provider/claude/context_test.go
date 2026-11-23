package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
// and a reply after it.
func TestACompactionLeavesTheContextUnknownUntilTheNextRequest(t *testing.T) {
	exp := fixedNow.Add(5 * time.Minute)
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

// Cache creation anchors CacheExpires to the request's first reading: 1h
// takes priority over 5m, a pure cache read leaves CacheExpires zero, and
// a later duplicate assistant message does not push the deadline out.
func TestCacheCreationTTLAndPureCacheRead(t *testing.T) {
	now := fixedNow
	tn := newTurn(t.TempDir())
	tn.now = func() time.Time { return now }

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

	// Pure cache read (no cache_creation) leaves CacheExpires zero.
	tn2 := newTurn(t.TempDir())
	tn2.now = func() time.Time { return fixedNow }
	got2 := read(tn2,
		line(t, `{"type":"assistant","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":5,"cache_read_input_tokens":250000}}}`),
		line(t, twoModels),
	)
	want2 := []provider.Context{
		{Used: 250005, Model: haiku},
		{Used: 250005, Window: 1000000, Model: haiku},
	}
	if !slices.Equal(got2, want2) {
		t.Errorf("pure cache read reported %+v, want %+v", got2, want2)
	}
}

const haiku = "claude-haiku-5-5"
