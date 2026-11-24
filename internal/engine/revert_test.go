package engine_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// idleWorktreeThread is a thread of the user's in a worktree of its own
// whose first run ended, as it returns, a child's of parent when parent
// isn't zero.
func idleWorktreeThread(t *testing.T, parent api.ID) (*thread, api.ID) {
	f := worktreeThread(t, parent)
	run, _ := f.decide(dispatch("go", api.StartNow))
	f.see(engine.Started{Run: run})
	f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
	return f, run
}

// sameBut is whether x is y but for its revision and when it was updated.
func sameBut(x, y engine.Thread) bool {
	x.Revision, x.UpdatedAt = y.Revision, y.UpdatedAt
	return reflect.DeepEqual(x, y)
}

// A revert of the files goes to the runtime as one effect, after a
// thread.reverting that moves only the thread's revision, only for an idle thread
// of the user's in a worktree of its own, to a checkpoint it can have.
func TestWhenAThreadReverts(t *testing.T) {
	for name, tc := range map[string]struct {
		thread       func(t *testing.T) (*thread, api.ID)
		to           func(run api.ID) api.CheckpointRef
		conversation bool
		refuse       string
	}{
		"to a run's checkpoint": {
			thread: func(t *testing.T) (*thread, api.ID) { return idleWorktreeThread(t, api.ID{}) },
		},
		"to the base": {
			thread: func(t *testing.T) (*thread, api.ID) { return idleWorktreeThread(t, api.ID{}) },
			to:     func(run api.ID) api.CheckpointRef { return api.CheckpointRef{Phase: api.CheckpointBase, ID: run} },
		},
		"to a revert's recovery": {
			thread: func(t *testing.T) (*thread, api.ID) { return idleWorktreeThread(t, api.ID{}) },
			to: func(api.ID) api.CheckpointRef {
				return api.CheckpointRef{Phase: api.CheckpointRecovery, ID: api.NewID()}
			},
		},
		"of the conversation alone, to its only turn": {
			thread:       func(t *testing.T) (*thread, api.ID) { return idleWorktreeThread(t, api.ID{}) },
			conversation: true,
			refuse:       "The conversation already ends at this turn",
		},
		"of a child, in a worktree of its own too": {
			thread: func(t *testing.T) (*thread, api.ID) { return idleWorktreeThread(t, api.NewID()) },
			refuse: "A child thread keeps no checkpoints",
		},
		"of a thread in its project's folder": {
			thread: func(t *testing.T) (*thread, api.ID) {
				f := newThread(t)
				run, _ := f.decide(dispatch("go", api.StartNow))
				f.see(engine.Started{Run: run})
				f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
				return f, run
			},
			refuse: "Revert needs this thread to run in its own worktree",
		},
		"while a run works": {
			thread: func(t *testing.T) (*thread, api.ID) {
				f := worktreeThread(t, api.ID{})
				run, _ := f.decide(dispatch("go", api.StartNow))
				f.see(engine.Started{Run: run})
				return f, run
			},
			refuse: "Stop the thread before reverting its files",
		},
		"to a run the thread never had": {
			thread: func(t *testing.T) (*thread, api.ID) { return idleWorktreeThread(t, api.ID{}) },
			to:     func(api.ID) api.CheckpointRef { return api.CheckpointRef{Phase: api.CheckpointRun, ID: api.NewID()} },
			refuse: "no run of the thread's has that checkpoint",
		},
		"to a base of a later run": {
			thread: func(t *testing.T) (*thread, api.ID) {
				f, _ := idleWorktreeThread(t, api.ID{})
				run, _ := f.decide(dispatch("more", api.StartNow))
				f.see(engine.Started{Run: run})
				f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
				return f, run
			},
			to:     func(run api.ID) api.CheckpointRef { return api.CheckpointRef{Phase: api.CheckpointBase, ID: run} },
			refuse: "the base checkpoint is its first run's",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, run := tc.thread(t)
			to := api.CheckpointRef{Phase: api.CheckpointRun, ID: run}
			if tc.to != nil {
				to = tc.to(run)
			}
			before := f.th
			id, effects, err := f.submit(api.ThreadRevert{To: to, Files: !tc.conversation, Conversation: tc.conversation})
			if tc.refuse != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refuse) {
					t.Fatalf("revert: %v, %v; want it refused with %q", kinds(effects), err, tc.refuse)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := []engine.Effect{engine.RevertThread{Revert: id, To: to}}; !reflect.DeepEqual(effects, want) {
				t.Fatalf("effects %+v, want %+v", effects, want)
			}
			if !sameBut(f.th, before) {
				t.Fatalf("thread.reverting changed the projection: %+v, want %+v but its revision", f.th, before)
			}
			done := api.ThreadReverted{Revert: id, To: to, Status: api.RevertRestored, Changed: 2, Recovery: "c0ffee"}
			evs, effects, err := engine.Observe(f.th, engine.Reverted{Revert: done}, now)
			if err != nil || len(effects) != 0 || len(evs) != 1 || !reflect.DeepEqual(evs[0].Body, done) {
				t.Fatalf("reverted: events %+v, effects %v, %v; want the one thread.reverted", evs, effects, err)
			}
			f.fold(evs)
			if !sameBut(f.th, before) || f.th.Revision != before.Revision+2 {
				t.Fatalf("thread.reverted changed the projection: %+v, want %+v but its revision, moved twice", f.th, before)
			}
		})
	}
}

// A revert left leased runs again at boot, and does nothing for a thread
// deleted since.
func TestARevertRunsAgainAfterACrash(t *testing.T) {
	f, run := idleWorktreeThread(t, api.ID{})
	e := engine.RevertThread{Revert: api.NewID(), To: api.CheckpointRef{Phase: api.CheckpointRun, ID: run}}
	if !engine.ReplaySafe(e) {
		t.Error("a revert isn't replay-safe: boot would stop on one left leased")
	}
	b, _ := json.Marshal(e)
	if back, err := engine.DecodeEffect(engine.EffectKind(e), b); err != nil || !reflect.DeepEqual(back, e) {
		t.Errorf("%s decodes as %+v, %v", engine.EffectKind(e), back, err)
	}
	if due, skip := engine.Due(f.th, e); !due || skip != nil {
		t.Errorf("a revert on its thread: due %v, skip %v", due, skip)
	}
	f.decide(api.ThreadDelete{})
	if due, _ := engine.Due(f.th, e); due {
		t.Error("a revert is due on a deleted thread")
	}
}
