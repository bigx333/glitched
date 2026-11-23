package runtime_test

import (
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// What a turn's agent says of its context lands on the thread's view,
// the last report standing, and outlives a restart of the engine.
func TestATurnsContextReportsLandOnTheThread(t *testing.T) {
	ad := fake.New([]provider.Event{
		provider.Context{Used: 20000, Window: 200000, Model: "claude-haiku-5-5"},
		provider.TextDelta{Text: "Reading."},
		provider.Context{Used: 26000, Model: "claude-haiku-5-5"},
	})
	path := dbPath(t)
	n := start(t, path, ad)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	want := api.Context{Used: 26000, Window: 200000, Model: "claude-haiku-5-5"}
	if v := until(t, n.h, thread, "the turn", idle); v.Context == nil || *v.Context != want {
		t.Fatalf("the thread's context is %+v, want %+v", v.Context, want)
	}
	n.stop()
	n = start(t, path, ad)
	if v := until(t, n.h, thread, "the thread", idle); v.Context == nil || *v.Context != want {
		t.Errorf("after a restart the thread's context is %+v, want %+v", v.Context, want)
	}
}
