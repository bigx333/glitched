package engine_test

import (
	"reflect"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// A fork is the user's, of a thread that is there, from a finished turn
// its conversation still holds that isn't a silent one. Its origin keeps
// the runs the conversation held then, the one live after it included.
func TestWhatForks(t *testing.T) {
	r1, silent, dropped, r4, live := api.NewID(), api.NewID(), api.NewID(), api.NewID(), api.NewID()
	src := engine.Thread{ID: api.NewID(), Title: "the source", Model: api.ModelRef{Provider: "codex"}, Session: "thr", Runs: []engine.Run{
		{ID: r1, State: engine.RunFinished},
		{ID: silent, State: engine.RunFinished, Silent: true},
		{ID: dropped, State: engine.RunFinished, Dropped: true},
		{ID: r4, State: engine.RunFinished, Outcome: api.OutcomeFailed},
		{ID: live, State: engine.RunRunning},
	}}
	gone := src
	gone.Deleted = true
	for name, tc := range map[string]struct {
		src    engine.Thread
		create api.ThreadCreate
		refuse string
	}{
		"a finished turn":         {src: src, create: api.ThreadCreate{Fork: &api.Fork{Run: r1}}},
		"a failed turn":           {src: src, create: api.ThreadCreate{Fork: &api.Fork{Run: r4}}},
		"a silent run":            {src: src, create: api.ThreadCreate{Fork: &api.Fork{Run: silent}}, refuse: "only a finished turn"},
		"a turn a rewind dropped": {src: src, create: api.ThreadCreate{Fork: &api.Fork{Run: dropped}}, refuse: "only a finished turn"},
		"the live run":            {src: src, create: api.ThreadCreate{Fork: &api.Fork{Run: live}}, refuse: "only a finished turn"},
		"a run of another thread": {src: src, create: api.ThreadCreate{Fork: &api.Fork{Run: api.NewID()}}, refuse: "only a finished turn"},
		"a deleted thread":        {src: gone, create: api.ThreadCreate{Fork: &api.Fork{Run: r1}}, refuse: "the thread to fork is gone"},
		"no thread":               {create: api.ThreadCreate{Fork: &api.Fork{Run: r1}}, refuse: "the thread to fork is gone"},
		"an agent's child":        {src: src, create: api.ThreadCreate{Parent: api.NewID(), Run: api.NewID(), Fork: &api.Fork{Run: r1}}, refuse: "only you fork"},
	} {
		t.Run(name, func(t *testing.T) {
			tc.create.Fork.Thread = tc.src.ID
			got, err := engine.CheckFork(tc.src, tc.create)
			if tc.refuse != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refuse) {
					t.Fatalf("CheckFork = %+v, %v; want %q", got, err, tc.refuse)
				}
				return
			}
			want := &api.Forked{Thread: src.ID, Run: tc.create.Fork.Run, Title: "the source", Provider: "codex", Session: "thr", Runs: []api.ID{r1, silent, r4, live}}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("CheckFork = %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

// A fork's create records its origin on thread.created and admits its
// first message in the same decision; an engine handed a fork nobody
// resolved refuses it rather than create a thread with none of it.
func TestAForkIsACreateWithItsOrigin(t *testing.T) {
	forked := &api.Forked{Thread: api.NewID(), Run: api.NewID(), Title: "the source", Provider: "fake", Runs: []api.ID{}}
	id := api.NewID()
	evs, effects, err := engine.Decide(engine.Thread{}, api.Command{ID: api.NewID(), Target: id, Body: api.ThreadCreate{Title: "fork", Text: "go on", Fork: &api.Fork{Thread: forked.Thread, Run: forked.Run}, Forked: forked}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := evs[0].Body.(api.ThreadCreated); !ok || c.Fork != forked {
		t.Fatalf("first event %+v; want thread.created with the fork's origin", evs[0].Body)
	}
	if len(effects) != 1 || engine.EffectKind(effects[0]) != "turn.start" {
		t.Fatalf("effects %+v; want the first message's turn started", effects)
	}
	_, _, err = engine.Decide(engine.Thread{}, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Title: "fork", Fork: &api.Fork{Thread: forked.Thread, Run: forked.Run}}}, now)
	if err == nil || !strings.Contains(err.Error(), "forks no threads") {
		t.Fatalf("an unresolved fork: %v", err)
	}
}
