//go:build claudelive

package claude_test

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider/claude"
)

// TestFollowUpLatency measures, for the signed-in claude on PATH, how long
// a follow-up turn takes from its send to TurnStarted, when the prompt has
// gone to claude, and to its first AssistantDelta. It spends one haiku
// prompt for the first turn and one for each follow-up.
func TestFollowUpLatency(t *testing.T) {
	h := host(t, &claude.Adapter{})
	thread := api.NewID()
	ws := must(os.MkdirTemp("/tmp", "glitched-latency-"))
	t.Cleanup(func() { os.RemoveAll(ws) })
	submit(t, h, thread, api.ThreadCreate{Workspace: ws, Title: "latency", Access: api.AccessAsk,
		Model: api.ModelRef{Provider: "claude", Model: "haiku"}})
	if got := turn(t, h, thread, "Reply with the single word one."); got.ended.Outcome != api.OutcomeCompleted {
		t.Fatalf("first turn: %+v", got.ended)
	}
	var started, delta []time.Duration
	for i := range 4 {
		run := api.NewID()
		sent := time.Now()
		if _, err := h.Submit(ctx, api.Command{ID: run, Target: thread, Body: api.MessageDispatch{Text: "Reply with the single word again.", Mode: api.StartNow}}); err != nil {
			t.Fatal(err)
		}
		var s, d time.Duration
		for deadline := time.Now().Add(time.Minute); s == 0 || d == 0; time.Sleep(2 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("follow-up %d: no start or delta in a minute", i)
			}
			snap, err := h.Read(ctx, api.Query{Thread: thread})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range snap.Events {
				switch b := must(e.Decode()).(type) {
				case api.TurnStarted:
					if b.Run == run && s == 0 {
						s = time.Since(sent)
					}
				case api.AssistantDelta:
					if b.Run == run && d == 0 {
						d = time.Since(sent)
					}
				}
			}
		}
		waitEnded(t, h, thread, run)
		started, delta = append(started, s), append(delta, d)
	}
	t.Logf("send to TurnStarted: %v, median %v", started, median(started))
	t.Logf("send to first AssistantDelta: %v, median %v", delta, median(delta))
}

func waitEnded(t *testing.T, h interface {
	Read(context.Context, api.Query) (api.Snapshot, error)
}, thread, run api.ID) {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		snap := must(h.Read(ctx, api.Query{Thread: thread}))
		for _, e := range snap.Events {
			if b, ok := must(e.Decode()).(api.TurnEnded); ok && b.Run == run {
				return
			}
		}
	}
	t.Fatalf("run %s did not end", run)
}

func median(ds []time.Duration) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[len(s)/2]
}
