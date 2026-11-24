package engine_test

import (
	"reflect"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// turned is f after one more run of the user's, which it returns.
func turned(f *thread, text string) api.ID {
	run, _ := f.decide(dispatch(text, api.StartNow))
	f.see(engine.Started{Run: run})
	f.see(engine.Context{Run: run, Used: 900, Window: 1000})
	f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted, Head: "head-" + text})
	return run
}

// A rewind of the conversation goes to the runtime as the revert's one
// effect, with the files or without them, to a run with a turn of the
// user's after it, on an idle top-level thread: one in its project's
// folder too, when it leaves the files.
func TestWhenAConversationRewinds(t *testing.T) {
	inWorktree := func(t *testing.T) *thread { return worktreeThread(t, api.ID{}) }
	for name, tc := range map[string]struct {
		thread func(t *testing.T) *thread
		files  bool
		to     func(first, second api.ID) api.CheckpointRef
		live   bool
		refuse string
	}{
		"leaving the files":                    {thread: inWorktree},
		"with the files":                       {thread: inWorktree, files: true},
		"in its project's folder":              {thread: newThread},
		"with the files of a folder it shares": {thread: newThread, files: true, refuse: "Revert needs this thread to run in its own worktree"},
		"of a child": {
			thread: func(t *testing.T) *thread { return worktreeThread(t, api.NewID()) },
			refuse: "A child thread keeps no checkpoints",
		},
		"to its last turn": {
			thread: inWorktree,
			to: func(_, second api.ID) api.CheckpointRef {
				return api.CheckpointRef{Phase: api.CheckpointRun, ID: second}
			},
			refuse: "The conversation already ends at this turn",
		},
		"to the base": {
			thread: inWorktree,
			to: func(first, _ api.ID) api.CheckpointRef {
				return api.CheckpointRef{Phase: api.CheckpointBase, ID: first}
			},
			refuse: "Nothing comes before the thread's first turn",
		},
		"while a run works": {thread: inWorktree, live: true, refuse: "Stop the thread before rewinding its conversation"},
	} {
		t.Run(name, func(t *testing.T) {
			f := tc.thread(t)
			first, second := turned(f, "one"), turned(f, "two")
			if tc.live {
				run, _ := f.decide(dispatch("three", api.StartNow))
				f.see(engine.Started{Run: run})
			}
			to := api.CheckpointRef{Phase: api.CheckpointRun, ID: first}
			if tc.to != nil {
				to = tc.to(first, second)
			}
			before := f.th
			id, effects, err := f.submit(api.ThreadRevert{To: to, Files: tc.files, Conversation: true})
			if tc.refuse != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refuse) {
					t.Fatalf("rewind: %v, %v; want it refused with %q", kinds(effects), err, tc.refuse)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := []engine.Effect{engine.RevertThread{Revert: id, To: to, Conversation: true, KeepFiles: !tc.files, Dropped: []api.ID{second}}}; !reflect.DeepEqual(effects, want) {
				t.Fatalf("effects %+v, want %+v", effects, want)
			}
			if !sameBut(f.th, before) {
				t.Fatalf("thread.reverting changed the projection: %+v, want %+v but its revision", f.th, before)
			}
			if f.th.Context.Used == 0 {
				t.Fatal("the thread's context wasn't known before the rewind")
			}
			done := api.ThreadReverted{Revert: id, To: to, Status: api.RevertKept, Conversation: api.RewindNative, Session: "forked", From: f.th.Session, Dropped: []api.ID{second}}
			f.see(engine.Reverted{Revert: done})
			if f.th.Session != "forked" || f.th.Context != (api.Context{}) {
				t.Fatalf("after the rewind: session %q, context %+v; want the forked one, and nothing known of it", f.th.Session, f.th.Context)
			}
			if f.th.Runs[0].Dropped || !f.th.Runs[1].Dropped {
				t.Fatalf("after the rewind the runs are %+v; want the second dropped, the first kept", f.th.Runs)
			}
		})
	}
}

// A turn sent behind a rewind, and stopped before its agent began, leaves
// the rewind to land: the thread takes up the new session.
func TestARewindLandsBehindAStoppedTurn(t *testing.T) {
	f := worktreeThread(t, api.ID{})
	first, second := turned(f, "one"), turned(f, "two")
	to := api.CheckpointRef{Phase: api.CheckpointRun, ID: first}
	id := f.must(api.ThreadRevert{To: to, Conversation: true})
	f.decide(dispatch("three", api.StartNow))
	if _, _, err := f.submit(api.TurnInterrupt{}); err != nil || f.th.Live().State != engine.RunStopping {
		t.Fatalf("stop: %v; the run is %+v, want it stopping", err, f.th.Live())
	}
	f.see(engine.Reverted{Revert: api.ThreadReverted{Revert: id, To: to, Status: api.RevertKept, Conversation: api.RewindNative, Session: "forked", Dropped: []api.ID{second}}})
	if f.th.Session != "forked" || !f.th.Runs[1].Dropped {
		t.Fatalf("after the rewind: session %q, runs %+v; want the forked session and the second run dropped", f.th.Session, f.th.Runs)
	}
}

// A second rewind follows the conversation, not the timeline: it drops
// only what the conversation still holds, and refuses a turn the first
// one took out, and the turn the conversation ends at.
func TestASecondRewindFollowsTheConversation(t *testing.T) {
	f := worktreeThread(t, api.ID{})
	first, second := turned(f, "one"), turned(f, "two")
	to := api.CheckpointRef{Phase: api.CheckpointRun, ID: first}
	id := f.must(api.ThreadRevert{To: to, Conversation: true})
	f.see(engine.Reverted{Revert: api.ThreadReverted{Revert: id, To: to, Status: api.RevertKept, Conversation: api.RewindPortable, Session: "s2", Dropped: []api.ID{second}}})
	if _, _, err := f.submit(api.ThreadRevert{To: to, Conversation: true}); err == nil || !strings.Contains(err.Error(), "The conversation already ends at this turn") {
		t.Fatalf("rewinding to where the conversation ends: %v", err)
	}
	third := turned(f, "three")
	turned(f, "four")
	if _, _, err := f.submit(api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointRun, ID: second}, Conversation: true}); err == nil || !strings.Contains(err.Error(), "A rewind took this turn out of the conversation") {
		t.Fatalf("rewinding to a turn the conversation dropped: %v", err)
	}
	_, effects, err := f.submit(api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointRun, ID: third}, Conversation: true})
	if err != nil || len(effects) != 1 || !reflect.DeepEqual(effects[0].(engine.RevertThread).Dropped, []api.ID{f.th.Runs[3].ID}) {
		t.Fatalf("rewinding to the third turn: %+v, %v; want it to drop the fourth alone", effects, err)
	}
}

// A rewind that failed changes nothing of the thread's but its revision.
func TestAFailedRewindKeepsTheSession(t *testing.T) {
	f := worktreeThread(t, api.ID{})
	first := turned(f, "one")
	turned(f, "two")
	f.see(engine.Bound{Run: f.th.Runs[1].ID, Session: "s1"})
	to := api.CheckpointRef{Phase: api.CheckpointRun, ID: first}
	id := f.must(api.ThreadRevert{To: to, Conversation: true})
	before := f.th
	f.see(engine.Reverted{Revert: api.ThreadReverted{Revert: id, To: to, Status: api.RevertFailed, Conversation: api.RewindFailed, Error: "no"}})
	if !sameBut(f.th, before) {
		t.Fatalf("a failed rewind changed the thread: %+v, want %+v", f.th, before)
	}
}

// The first run after a rewind says how it took the conversation up, the
// same session too, as Claude's is minted by the revert; a later run of
// the same session binds nothing.
func TestTheRunAfterARewindSaysHowItTookItUp(t *testing.T) {
	f := worktreeThread(t, api.ID{})
	first := turned(f, "one")
	turned(f, "two")
	to := api.CheckpointRef{Phase: api.CheckpointRun, ID: first}
	id := f.must(api.ThreadRevert{To: to, Conversation: true})
	f.see(engine.Reverted{Revert: api.ThreadReverted{Revert: id, To: to, Status: api.RevertKept, Conversation: api.RewindNative, Session: "new", At: "at"}})
	run, _ := f.decide(dispatch("three", api.StartNow))
	evs := f.observe(engine.Bound{Run: run, Session: "new", Rewound: api.RewindPortable})
	if len(evs) != 1 || !reflect.DeepEqual(evs[0].Body, api.SessionBound{Run: run, Session: "new", Rewound: api.RewindPortable}) {
		t.Fatalf("bound: %+v; want the one session.bound that says it went portable", evs)
	}
	if evs := f.observe(engine.Bound{Run: run, Session: "new"}); len(evs) != 0 {
		t.Fatalf("binding the session the thread has again: %+v", evs)
	}
}

// A run's head reaches its turn.ended.
func TestARunsHeadIsLogged(t *testing.T) {
	f := worktreeThread(t, api.ID{})
	run, _ := f.decide(dispatch("go", api.StartNow))
	f.see(engine.Started{Run: run})
	evs := f.observe(engine.Ended{Run: run, Outcome: api.OutcomeCompleted, Head: "turn-1"})
	for _, e := range evs {
		if b, ok := e.Body.(api.TurnEnded); ok && b.Head == "turn-1" {
			return
		}
	}
	t.Fatalf("ended: %+v; want a turn.ended with the head", evs)
}

// A conversation revert to a rewind's recovery checkpoint undoes the
// rewind: the engine hands it to the runtime with nothing to drop, and the
// runs the undo restored are the conversation's again.
func TestAnUndoOfARewindRestoresItsRuns(t *testing.T) {
	f := worktreeThread(t, api.ID{})
	first, second := turned(f, "one"), turned(f, "two")
	rewind, _, err := f.submit(api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointRun, ID: first}, Conversation: true})
	if err != nil {
		t.Fatal(err)
	}
	f.see(engine.Reverted{Revert: api.ThreadReverted{Revert: rewind, To: api.CheckpointRef{Phase: api.CheckpointRun, ID: first}, Status: api.RevertKept, Conversation: api.RewindPortable, Dropped: []api.ID{second}}})
	to := api.CheckpointRef{Phase: api.CheckpointRecovery, ID: rewind}
	id, effects, err := f.submit(api.ThreadRevert{To: to, Conversation: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := []engine.Effect{engine.RevertThread{Revert: id, To: to, Conversation: true, KeepFiles: true}}; !reflect.DeepEqual(effects, want) {
		t.Fatalf("effects %+v, want %+v", effects, want)
	}
	f.see(engine.Reverted{Revert: api.ThreadReverted{Revert: id, To: to, Status: api.RevertKept, Conversation: api.RewindNative, Session: "back", Restored: []api.ID{second}}})
	if f.th.Session != "back" || f.th.Runs[0].Dropped || f.th.Runs[1].Dropped {
		t.Fatalf("after the undo: session %q, runs %+v; want the undo's session and no run dropped", f.th.Session, f.th.Runs)
	}
}
