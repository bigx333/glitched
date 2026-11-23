package main

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/bundled"
)

// The built-in's main.js and its native copy draw the same meter, node
// for node, whatever the agent said.
func TestTheBuiltInMeterIsItsNativeCopy(t *testing.T) {
	q := make(chan func(), 1024)
	h := plugins.Open(t.TempDir(), plugins.Options{Post: func(fn func()) { q <- fn }, Bundled: bundled.FS, ReleaseKeys: []string{bundled.ReleaseKey}})
	t.Cleanup(h.Close)
	s := h.App("child-threads")
	if s == nil {
		t.Fatalf("the built-in didn't load: %v", h.Failed("child-threads"))
	}
	for i, u := range []headerUsage{
		{Tokens: 1250, Total: 3000},
		{Tokens: 48200, Total: 48200, Context: &api.Context{}},
		{Tokens: 1250, Total: 3000, Context: &api.Context{Window: 258400}},
		{Tokens: 1750, Total: 3000, Context: &api.Context{Used: 999500}},
		{Tokens: 48200, Total: 48200, Context: &api.Context{Used: 25041, Window: 1000000, Stale: true}},
		{Tokens: 48200, Total: 48200, Context: &api.Context{Used: 25041, Window: 1000000, Model: "claude-haiku-5-5"}},
		{Tokens: 48200, Total: 90000, Context: &api.Context{Used: 744999, Window: 1000000}},
		{Tokens: 48200, Total: 48200, Context: &api.Context{Used: 745000, Window: 1000000}},
		{Tokens: 48200, Total: 48200, Context: &api.Context{Used: 895000, Window: 1000000}},
		{Tokens: 1, Total: 1, Context: &api.Context{Used: 1, Window: 20000}},
		{Tokens: 1, Total: 1, Context: &api.Context{Used: 1200000, Window: 1000000}},
		{Tokens: 1, Total: 1, Context: &api.Context{Used: 258399, Window: 258400}},
		{Tokens: 1, Total: 1, Context: &api.Context{Used: 5960000000000001, Window: 8000000000000002}},
		{Tokens: 1, Total: 1, Context: &api.Context{Used: 100000000000000000, Window: 100000000000000000}},
		{Tokens: 1, Total: 1, Context: &api.Context{Used: math.MaxInt64}},
		{Tokens: 1, Total: 1, Context: &api.Context{Used: -1, Window: 1000000}},
		{Tokens: 1, Total: 1, Context: &api.Context{Used: -5001, Window: 1000000}},
		{Tokens: 9007199254740992, Total: 9007199254740993, Context: &api.Context{Used: 25041, Window: 1000000}},
	} {
		key := string(rune('a' + i))
		s.SetItems("usage", []plugins.PlaceItem{{Key: key, Version: key, Context: u}})
		deadline := time.After(5 * time.Second)
		for s.PlaceView("usage", key) == nil && s.Err == nil {
			select {
			case fn := <-q:
				fn()
			case <-deadline:
				t.Fatalf("the built-in drew nothing for %+v: %v", u.Context, s.Err)
			}
		}
		if got, want := s.PlaceView("usage", key), usageBadge(u); !reflect.DeepEqual(got, want) {
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(want)
			t.Errorf("for %+v (%d, %d) main.js drew %s, its native copy %s", u.Context, u.Tokens, u.Total, g, w)
		}
	}
}
