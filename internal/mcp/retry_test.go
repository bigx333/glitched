package mcp_test

import (
	"testing"
	"time"

	"glitched/internal/api"
)

func TestAnEarlierTurnsAgentFindsTheChildItStarted(t *testing.T) {
	h := engine(t)
	origin := working(t, h)
	create := delegate(1, `{"task":"Reply with ok","client_request_id":"stable-task"}`, "")
	first := serve(t, h, origin, create)[1]
	want := first.Result.StructuredContent.TaskID
	if first.Result.IsError || want == "" {
		t.Fatalf("initial delegate: %+v", first)
	}

	send := func(b api.Intent) {
		t.Helper()
		if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: origin.Thread, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(state api.RunState) {
		t.Helper()
		for until := time.Now().Add(5 * time.Second); time.Now().Before(until); time.Sleep(time.Millisecond) {
			s, err := h.Read(ctx, api.Query{Thread: origin.Thread})
			if err == nil && s.Threads[0].Run == state {
				return
			}
		}
		t.Fatalf("parent never reached %v", state)
	}

	send(api.TurnInterrupt{})
	wait(api.RunIdle)
	configure(t, h, origin.Thread, api.ThreadConfigure{Access: new(api.AccessFull)})
	send(api.MessageDispatch{Mode: api.StartNow, Text: "next turn"})
	wait(api.RunWaiting)

	replay := serve(t, h, origin, create)[1].Result
	if len(children(t, h, origin.Thread)) != 1 {
		t.Fatal("exact MCP replay did not preserve one child")
	}
	if replay.IsError || replay.StructuredContent.TaskID != want {
		t.Fatalf("replayed committed delegate: error=%t content=%v task=%q; want original child %s", replay.IsError, replay.Content, replay.StructuredContent.TaskID, want)
	}
}
