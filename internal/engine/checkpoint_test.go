package engine_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// worktreeThread is a thread in a worktree of its own, a child's of parent
// when parent isn't zero.
func worktreeThread(t *testing.T, parent api.ID) *thread {
	t.Helper()
	body := api.ThreadCreate{Title: "t", Workspace: "/worktrees/app/t", Project: "/src/app", Branch: "glitched/t", Parent: parent}
	evs, _, err := engine.Decide(engine.Thread{}, api.Command{ID: api.NewID(), Target: api.NewID(), Body: body}, now)
	if err != nil {
		t.Fatal(err)
	}
	f := &thread{t: t}
	f.fold(evs)
	return f
}

// decide folds what body decides on f, and returns its ID and the effects
// of that one commit.
func (f *thread) decide(body api.Intent) (api.ID, []engine.Effect) {
	f.t.Helper()
	id, effects, err := f.submit(body)
	if err != nil {
		f.t.Fatalf("%s: %v", api.IntentType(body), err)
	}
	return id, effects
}

func kinds(effects []engine.Effect) []string {
	out := []string{}
	for _, e := range effects {
		k := engine.EffectKind(e)
		if c, ok := e.(engine.CaptureCheckpoint); ok {
			k += " " + string(c.Phase)
		}
		out = append(out, k)
	}
	return out
}

// TestWhenACheckpointIsTaken lists the commits that save a thread's files,
// and those that don't: each case returns one commit's effects, in commit
// order, and the run its captures name.
func TestWhenACheckpointIsTaken(t *testing.T) {
	for name, tc := range map[string]struct {
		commit func(t *testing.T) (effects []engine.Effect, run api.ID)
		want   []string
	}{
		"a worktree thread saves its base before its first turn starts": {
			commit: func(t *testing.T) ([]engine.Effect, api.ID) {
				f := worktreeThread(t, api.ID{})
				return swap(f.decide(dispatch("go", api.StartNow)))
			},
			want: []string{"checkpoint.capture base", "turn.start"},
		},
		"a later turn saves no base": {
			commit: func(t *testing.T) ([]engine.Effect, api.ID) {
				f := worktreeThread(t, api.ID{})
				run, _ := f.decide(dispatch("go", api.StartNow))
				f.see(engine.Started{Run: run})
				f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
				return swap(f.decide(dispatch("more", api.StartNow)))
			},
			want: []string{"turn.start"},
		},
		"a run's end saves it before the next turn starts": {
			commit: func(t *testing.T) ([]engine.Effect, api.ID) {
				f := worktreeThread(t, api.ID{})
				run, _ := f.decide(dispatch("go", api.StartNow))
				f.see(engine.Started{Run: run})
				f.decide(dispatch("next", api.AfterActive))
				return f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted}), run
			},
			want: []string{"checkpoint.capture run", "turn.start"},
		},
		"a stopped run is saved": {
			commit: func(t *testing.T) ([]engine.Effect, api.ID) {
				f := worktreeThread(t, api.ID{})
				run, _ := f.decide(dispatch("go", api.StartNow))
				f.see(engine.Started{Run: run})
				f.decide(api.TurnInterrupt{})
				return f.see(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted}), run
			},
			want: []string{"checkpoint.capture run"},
		},
		"a run whose start failed is saved": {
			commit: func(t *testing.T) ([]engine.Effect, api.ID) {
				f := worktreeThread(t, api.ID{})
				run, _ := f.decide(dispatch("go", api.StartNow))
				return f.see(engine.Ended{Run: run, Outcome: api.OutcomeFailed, Error: "no such cli"}), run
			},
			want: []string{"checkpoint.capture run"},
		},
		"the user's end of an uncertain run saves it": {
			commit: func(t *testing.T) ([]engine.Effect, api.ID) {
				f := worktreeThread(t, api.ID{})
				run, _ := f.decide(dispatch("go", api.StartNow))
				f.see(engine.Started{Run: run})
				f.see(engine.Reconciled{Run: run})
				_, effects := f.decide(api.TurnInterrupt{})
				return effects, run
			},
			want: []string{"checkpoint.capture run"},
		},
		"a thread in its project's folder saves nothing": {
			commit: func(t *testing.T) ([]engine.Effect, api.ID) {
				f := newThread(t)
				run, _ := f.decide(dispatch("go", api.StartNow))
				f.see(engine.Started{Run: run})
				f.decide(dispatch("next", api.AfterActive))
				return f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted}), run
			},
			want: []string{"turn.start"},
		},
		"a worktree child saves nothing: its work reaches the person through its parent's branch": {
			commit: func(t *testing.T) ([]engine.Effect, api.ID) {
				f := worktreeThread(t, api.NewID())
				run, effects := f.decide(dispatch("go", api.StartNow))
				if got := kinds(effects); !reflect.DeepEqual(got, []string{"turn.start"}) {
					t.Fatalf("a child's first turn: %v, want only its start", got)
				}
				f.see(engine.Started{Run: run})
				return f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted}), run
			},
			want: []string{"child.report"},
		},
		"a silent compaction saves nothing": {
			commit: func(t *testing.T) ([]engine.Effect, api.ID) {
				base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
				expires := base.Add(time.Hour)
				th, after := makeIdleThread("claude", "claude-opus-4-6", "sess-1", engine.MinIdleCompactTokens, base, expires)
				th.Project, th.Branch = "/src/app", "glitched/t"
				f := &thread{t: t, th: th}
				at := expires.Add(-6*time.Minute + 30*time.Second)
				run := api.NewID()
				evs, effects, err := engine.Observe(f.th, engine.IdleCompact{Run: run, AfterRun: after}, at)
				if err != nil || !reflect.DeepEqual(kinds(effects), []string{"turn.start"}) {
					t.Fatalf("idle compaction: %v, %v", kinds(effects), err)
				}
				f.fold(evs)
				f.see(engine.Started{Run: run})
				return f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted}), run
			},
			want: []string{},
		},
		"deleting drops the checkpoints, and saves nothing of the live run": {
			commit: func(t *testing.T) ([]engine.Effect, api.ID) {
				f := worktreeThread(t, api.ID{})
				run, _ := f.decide(dispatch("go", api.StartNow))
				f.see(engine.Started{Run: run})
				_, effects := f.decide(api.ThreadDelete{})
				return effects, run
			},
			want: []string{"agent.close", "checkpoint.drop"},
		},
		"deleting a thread with its worktree removes it, then drops the checkpoints": {
			commit: func(t *testing.T) ([]engine.Effect, api.ID) {
				f := worktreeThread(t, api.ID{})
				run, _ := f.decide(dispatch("go", api.StartNow))
				f.see(engine.Started{Run: run})
				f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
				_, effects := f.decide(api.ThreadDelete{RemoveWorktree: true})
				return effects, run
			},
			want: []string{"agent.close", "worktree.remove", "checkpoint.drop"},
		},
		"deleting a worktree child removes its worktree and drops nothing": {
			commit: func(t *testing.T) ([]engine.Effect, api.ID) {
				f := worktreeThread(t, api.NewID())
				run, _ := f.decide(dispatch("go", api.StartNow))
				f.see(engine.Started{Run: run})
				f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
				_, effects := f.decide(api.ThreadDelete{RemoveWorktree: true})
				return effects, run
			},
			want: []string{"agent.close", "worktree.remove"},
		},
		"deleting a thread in its project's folder drops nothing": {
			commit: func(t *testing.T) ([]engine.Effect, api.ID) {
				f := newThread(t)
				run, _ := f.decide(dispatch("go", api.StartNow))
				f.see(engine.Started{Run: run})
				_, effects := f.decide(api.ThreadDelete{})
				return effects, run
			},
			want: []string{"agent.close"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			effects, run := tc.commit(t)
			if got := kinds(effects); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("effects %v, want %v", got, tc.want)
			}
			for _, e := range effects {
				if c, ok := e.(engine.CaptureCheckpoint); ok && c.Run != run {
					t.Fatalf("capture of run %v, want %v", c.Run, run)
				}
			}
		})
	}
}

func swap(run api.ID, effects []engine.Effect) ([]engine.Effect, api.ID) { return effects, run }

// A capture's report is transcript only: it changes nothing a client
// reads of the thread, and one for a run the thread never had records
// nothing.
func TestACheckpointIsTranscriptOnly(t *testing.T) {
	f := worktreeThread(t, api.ID{})
	run, _ := f.decide(dispatch("go", api.StartNow))
	f.see(engine.Started{Run: run})
	f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
	before := f.th
	c := api.CheckpointCaptured{Run: run, Phase: api.CheckpointRun, Status: api.CheckpointReady, Commit: "c0ffee", Files: 3, Changed: 1, Ms: 12}
	evs, effects, err := engine.Observe(f.th, engine.Captured{Checkpoint: c}, now.Add(time.Minute))
	if err != nil || len(effects) != 0 || len(evs) != 1 || !reflect.DeepEqual(evs[0].Body, c) {
		t.Fatalf("captured: events %+v, effects %v, %v; want the one checkpoint.captured and nothing else", evs, effects, err)
	}
	f.fold(evs)
	if !reflect.DeepEqual(f.th, before) {
		t.Fatalf("checkpoint.captured changed the projection: %+v, want %+v", f.th, before)
	}
	evs, _, err = engine.Observe(f.th, engine.Captured{Checkpoint: api.CheckpointCaptured{Run: api.NewID()}}, now)
	if err != nil || len(evs) != 0 {
		t.Fatalf("a capture of no run of the thread's: %+v, %v; want nothing", evs, err)
	}
}

func TestCheckpointEffectsRunAgainAfterACrash(t *testing.T) {
	f := worktreeThread(t, api.ID{})
	run, _ := f.decide(dispatch("go", api.StartNow))
	capture, drop := engine.CaptureCheckpoint{Run: run, Phase: api.CheckpointBase}, engine.DropCheckpoints{}
	for _, e := range []engine.Effect{capture, drop} {
		if !engine.ReplaySafe(e) {
			t.Errorf("%s isn't replay-safe: boot would stop on one left leased", engine.EffectKind(e))
		}
		b, _ := json.Marshal(e)
		if back, err := engine.DecodeEffect(engine.EffectKind(e), b); err != nil || !reflect.DeepEqual(back, e) {
			t.Errorf("%s decodes as %+v, %v", engine.EffectKind(e), back, err)
		}
		if due, skip := engine.Due(f.th, e); !due || skip != nil {
			t.Errorf("%s on a live thread: due %v, skip %v", engine.EffectKind(e), due, skip)
		}
	}
	f.decide(api.ThreadDelete{})
	if due, _ := engine.Due(f.th, capture); due {
		t.Error("a capture is due on a deleted thread, whose checkpoints its drop deletes")
	}
	if due, _ := engine.Due(f.th, drop); !due {
		t.Error("a drop isn't due on the deleted thread it is for")
	}
}
