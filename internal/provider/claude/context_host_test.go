package claude_test

import (
	"encoding/json"
	"testing"

	"glitched/internal/api"
)

// The recorded turn made three requests; its result sums them, and the
// context is the last one's prompt: 2 + 161 + 32,296.
func TestATurnReportsTheContextItsLastRequestSent(t *testing.T) {
	replay(t, "tools", "")
	h := host(t, nil)
	thread := newThread(t, h, api.AccessAsk)
	playOn(t, h, thread, "Read hello.txt. Then use the Write tool to create out.txt containing ok. Then reply done.", func(r requestLog) bool {
		race(t, h, thread, r.opened, func(int) json.RawMessage { return allowed(false) })
		return false
	})

	v := must(h.Read(ctx, api.Query{Thread: thread})).Threads[0]
	if want := (api.Context{Used: 32459, Window: 1000000, Model: "claude-haiku-5-5"}); v.Context == nil || *v.Context != want {
		t.Errorf("the thread's context is %+v, want %+v", v.Context, want)
	}
	if v.Tokens <= v.Context.Used {
		t.Errorf("the thread used %d tokens, want more than the %d its context holds: the turn's requests each sent it", v.Tokens, v.Context.Used)
	}
}
