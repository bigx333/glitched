package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"glitched/internal/api"
)

// A remote client sees an access change pending as the engine does: held
// while the turn it was made in runs, then for the next turn, after the
// engine restarts too, until the next turn's provider confirms it.
func TestARemoteClientSeesAPendingAccessAcrossARestart(t *testing.T) {
	ctx := context.Background()
	dir := stateDir(t)
	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	d := start(t, Config{Dir: dir, Listen: []string{"127.0.0.1:0"}})
	ep := remote(t, d, token)
	thread := api.NewID()
	submit := func(body api.Intent) {
		t.Helper()
		if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	view := func(what string, ok func(api.Thread) bool) api.Thread {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			s, err := ep.Read(ctx, api.Query{Thread: thread})
			if err == nil && len(s.Threads) == 1 && ok(s.Threads[0]) {
				return s.Threads[0]
			}
			if time.Now().After(deadline) {
				t.Fatalf("never %s: %+v %v", what, s.Threads, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	submit(api.ThreadCreate{Workspace: t.TempDir(), Title: "t", Model: api.ModelRef{Provider: "fake", Model: "m"}, Access: api.AccessAsk})
	submit(api.MessageDispatch{Text: "go", Mode: api.StartNow})
	v := view("waiting on its approval", func(v api.Thread) bool { return len(v.Requests) == 1 && v.Confirmed.Access != nil })
	if v.AccessStanding() != api.InEffect || v.Confirmed.Access.Access != api.AccessAsk {
		t.Fatalf("the first turn stands %d confirmed at %+v", v.AccessStanding(), v.Confirmed.Access)
	}
	submit(api.ThreadConfigure{Access: new(api.AccessFull)})
	v = view("at Full access", func(v api.Thread) bool { return v.Access == api.AccessFull })
	if v.AccessStanding() != api.Held {
		t.Errorf("a change during the turn stands %d, want held", v.AccessStanding())
	}
	r := v.Requests[0]
	submit(api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: json.RawMessage(`{"allow":true}`)})
	view("idle", func(v api.Thread) bool { return v.Run == api.RunIdle && v.AccessStanding() == api.NextTurn })

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = start(t, Config{Dir: dir, Listen: []string{"127.0.0.1:0"}})
	ep = remote(t, d, token)
	view("pending after a restart", func(v api.Thread) bool { return v.AccessStanding() == api.NextTurn })
	submit(api.MessageDispatch{Text: "again", Mode: api.StartNow})
	v = view("confirmed by the next turn", func(v api.Thread) bool {
		return v.Turn != nil && v.Confirmed.Access != nil && v.Confirmed.Access.Run == v.Turn.Run
	})
	if v.AccessStanding() != api.InEffect || v.Confirmed.Access.Access != api.AccessFull {
		t.Errorf("the next turn stands %d confirmed at %+v", v.AccessStanding(), v.Confirmed.Access)
	}
}
