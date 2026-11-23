package claude_test

import (
	"encoding/json"
	"testing"
	"time"

	"glitched/internal/api"
)

// The recorded turn made three requests; its result sums them, and the
// context is the last one's prompt: 2 + 161 + 32,296.
func TestATurnReportsTheContextItsLastRequestSent(t *testing.T) {
	replay(t, "tools", "")
	before := time.Now().UTC()
	h := host(t, nil)
	thread := newThread(t, h, api.AccessAsk)
	playOn(t, h, thread, "Read hello.txt. Then use the Write tool to create out.txt containing ok. Then reply done.", func(r requestLog) bool {
		race(t, h, thread, r.opened, func(int) json.RawMessage { return allowed(false) })
		return false
	})

	v := must(h.Read(ctx, api.Query{Thread: thread})).Threads[0]
	if v.Context == nil {
		t.Fatalf("the thread's context is nil")
	}
	got := *v.Context
	exp := got.CacheExpires
	got.CacheExpires = time.Time{}
	if want := (api.Context{Used: 32459, Window: 1000000, Model: "claude-haiku-5-5"}); got != want {
		t.Errorf("the thread's context is %+v, want %+v", got, want)
	}
	if exp.Before(before.Add(59*time.Minute)) || exp.After(time.Now().UTC().Add(61*time.Minute)) {
		t.Errorf("the thread's cache expiry is %v, want ~1h from %v", exp, before)
	}
	if v.Tokens <= v.Context.Used {
		t.Errorf("the thread used %d tokens, want more than the %d its context holds: the turn's requests each sent it", v.Tokens, v.Context.Used)
	}
}
