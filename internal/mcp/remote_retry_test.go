package mcp_test

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// A retry of a remote delegate in a later turn, with the same task and
// client_request_id, finds the placeholder the first made.
func TestARetryOfARemoteDelegateInALaterTurnFindsItsPlaceholder(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		if b, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, b)
		}
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "engine.db"))
	if err != nil {
		t.Fatal(err)
	}
	at := api.Remote{Engine: api.NewID(), Link: api.NewID()}
	s.SetPlaces(func(string) (api.Remote, bool, error) { return at, true, nil })
	h, err := runtime.New(ctx, s, provider.Mux{"fake": fake.New(fake.Turn(true))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(); s.Close() })
	o := workingIn(t, h, dir)
	args := `{"task":"same task","machine":"b","client_request_id":"same"}`
	first := serve(t, h, o, delegate(1, args, ""))[1]
	if first.Result.IsError {
		t.Fatal(first.Result.Content)
	}
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: o.Thread, Body: api.TurnInterrupt{}}); err != nil {
		t.Fatal(err)
	}
	wait := func(want api.RunState) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
			if r, _ := h.Read(ctx, api.Query{Thread: o.Thread}); r.Threads[0].Run == want {
				return
			}
		}
		t.Fatalf("the parent never got to %v", want)
	}
	wait(api.RunIdle)
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: o.Thread, Body: api.MessageDispatch{Text: "retry", Mode: api.AfterActive}}); err != nil {
		t.Fatal(err)
	}
	wait(api.RunWaiting)
	second := serve(t, h, o, delegate(2, args, ""))[2]
	if second.Result.IsError || second.Result.Content[0].Text != first.Result.Content[0].Text {
		t.Fatalf("the retry: %+v; want the first's placeholder, %+v", second.Result, first.Result)
	}
}
