package runtime_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/runtime"
)

// An undo of a native rewind branches off the conversation the thread had
// before it, as far as the last turn the rewind took out, which the
// conversation holds again; the next run takes that branch up, handed no
// turns.
func TestAnUndoOfANativeRewindBranchesOffTheConversationBeforeIt(t *testing.T) {
	ad := newRewinder(provider.Rewind{Session: "rewound", Native: true}, nil, true)
	r := twoTurnsWith(t, dbPath(t), tempDir(t), repo(t), ad)
	from, second := sessionOf(t, r.n.st, r.thread), runsOf(t, r.n.h, r.thread)[1]
	rewind := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true}).Command
	untilReverted(t, r.n.h, r.thread, 1)
	ad.mu.Lock()
	ad.rewind = provider.Rewind{Session: "back", Native: true}
	ad.mu.Unlock()
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: recoveryOf(rewind), Conversation: true})
	got := untilReverted(t, r.n.h, r.thread, 2)
	if got.Status != api.RevertKept || got.Conversation != api.RewindNative || got.Session != "back" || got.From != from || !slices.Equal(got.Restored, []api.ID{second.Run}) || len(got.Dropped) != 0 {
		t.Fatalf("undo %+v; want the conversation from %q back natively, the second run restored", got, from)
	}
	specs, _, _ := ad.seen()
	if len(specs) != 2 || specs[1].Session != from || specs[1].Head != second.Head || len(specs[1].Dropped) != 0 {
		t.Fatalf("rewind specs %+v; want the undo to branch off %q at the second run's head", specs, from)
	}
	th, err := r.n.st.Thread(ctx, r.thread)
	if err != nil {
		t.Fatal(err)
	}
	if th.Session != "back" || slices.ContainsFunc(th.Runs, func(run engine.Run) bool { return run.Dropped }) {
		t.Fatalf("thread session %q, runs %+v; want the undo's session and no run dropped", th.Session, th.Runs)
	}
	submit(t, r.n.h, r.thread, dispatch("again", api.StartNow))
	until(t, r.n.h, r.thread, "the turn after the undo", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
	_, opened, sent := ad.seen()
	if spec := opened[2]; spec.Session != "back" || spec.Rewound == nil || *spec.Rewound != (provider.Rewound{From: from}) || sent[2] != "again" {
		t.Fatalf("the run after the undo opened with session %q, rewound %+v, and was sent %q", spec.Session, spec.Rewound, sent[2])
	}
	assertReplay(t, r.n.st)
}

// An undo of a portable rewind, files and all, puts the files back and
// hands the next run every turn the conversation holds again, the ones
// the rewind took out among them.
func TestAnUndoOfAPortableRewindHandsBackEveryTurn(t *testing.T) {
	ad := newRewinder(provider.Rewind{}, nil, false)
	r := twoTurnsWith(t, dbPath(t), tempDir(t), repo(t), ad)
	rewind := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true, Conversation: true}).Command
	untilReverted(t, r.n.h, r.thread, 1)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: recoveryOf(rewind), Files: true, Conversation: true})
	got := untilReverted(t, r.n.h, r.thread, 2)
	if got.Status != api.RevertRestored || got.Conversation != api.RewindPortable || len(got.Restored) != 1 {
		t.Fatalf("undo %+v; want the files and the conversation back", got)
	}
	if text := read(t, r.ws+"/README"); text != "three\n" {
		t.Fatalf("README %q after the undo, want it as it was before the rewind", text)
	}
	submit(t, r.n.h, r.thread, dispatch("again", api.StartNow))
	until(t, r.n.h, r.thread, "the turn after the undo", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
	_, _, sent := ad.seen()
	handed := sent[2]
	if !strings.HasPrefix(handed, "<rewound_conversation>\nThe user undid a rewind") || !strings.Contains(handed, "<user>\nnext\n</user>") || !strings.Contains(handed, "<user>\ngo\n</user>") || !strings.HasSuffix(handed, "</rewound_conversation>\n\nagain") {
		t.Fatalf("the run after the undo was sent %q; want both turns, then its message", handed)
	}
	if strings.Index(handed, "<user>\nnext") > strings.Index(handed, "<user>\ngo") {
		t.Fatalf("handed %q; want the newest turn first", handed)
	}
	assertReplay(t, r.n.st)
}

// An undo of a rewind refuses, changing nothing, once a turn came after
// the rewind, even one a rewind since took out, for a revert that didn't
// rewind the conversation, and for any rewind but the latest.
func TestAnUndoOfARewindRefusesWhatItCantBringBack(t *testing.T) {
	t.Run("a turn came after", func(t *testing.T) {
		ad := newRewinder(provider.Rewind{}, nil, false)
		r := twoTurnsWith(t, dbPath(t), tempDir(t), repo(t), ad)
		rewind := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true}).Command
		untilReverted(t, r.n.h, r.thread, 1)
		submit(t, r.n.h, r.thread, dispatch("again", api.StartNow))
		until(t, r.n.h, r.thread, "the turn after the rewind", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
		refuses(t, r, ad, rewind, "A turn came after this rewind")
	})
	t.Run("not a rewind", func(t *testing.T) {
		ad := newRewinder(provider.Rewind{}, nil, false)
		r := twoTurnsWith(t, dbPath(t), tempDir(t), repo(t), ad)
		revert := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true}).Command
		untilReverted(t, r.n.h, r.thread, 1)
		refuses(t, r, ad, revert, "That revert didn't rewind the conversation")
	})
	t.Run("a rewind since", func(t *testing.T) {
		ad := newRewinder(provider.Rewind{}, nil, false)
		r := twoTurnsWith(t, dbPath(t), tempDir(t), repo(t), ad)
		submit(t, r.n.h, r.thread, dispatch("third", api.StartNow))
		until(t, r.n.h, r.thread, "the third turn", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
		rewind := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.run), Conversation: true}).Command
		untilReverted(t, r.n.h, r.thread, 1)
		submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true})
		untilReverted(t, r.n.h, r.thread, 2)
		refuses(t, r, ad, rewind, "A revert came after this rewind, and only the latest can be undone")
	})
	t.Run("a turn came after that a rewind since took out", func(t *testing.T) {
		ad := newRewinder(provider.Rewind{}, nil, false)
		r := twoTurnsWith(t, dbPath(t), tempDir(t), repo(t), ad)
		rewind := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true}).Command
		untilReverted(t, r.n.h, r.thread, 1)
		submit(t, r.n.h, r.thread, dispatch("new branch", api.StartNow))
		until(t, r.n.h, r.thread, "the turn after the rewind", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
		submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true})
		untilReverted(t, r.n.h, r.thread, 2)
		refuses(t, r, ad, rewind, "A turn came after this rewind")
	})
	t.Run("an undo since", func(t *testing.T) {
		ad := newRewinder(provider.Rewind{}, nil, false)
		r := twoTurnsWith(t, dbPath(t), tempDir(t), repo(t), ad)
		submit(t, r.n.h, r.thread, dispatch("third", api.StartNow))
		until(t, r.n.h, r.thread, "the third turn", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
		first := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.run), Conversation: true}).Command
		untilReverted(t, r.n.h, r.thread, 1)
		second := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true}).Command
		untilReverted(t, r.n.h, r.thread, 2)
		submit(t, r.n.h, r.thread, api.ThreadRevert{To: recoveryOf(second), Conversation: true})
		if got := untilReverted(t, r.n.h, r.thread, 3); got.Status != api.RevertKept || !slices.Equal(got.Restored, []api.ID{r.run.Run}) {
			t.Fatalf("undo of the latest rewind %+v; want the second turn back", got)
		}
		refuses(t, r, ad, first, "A revert came after this rewind, and only the latest can be undone")
	})
}

// An undo of a rewind whose conversation from before it the agent no
// longer has, as Codex once it lost the rollout, still brings back the
// files and the turns: the next run is handed them.
func TestAnUndoOfARewindTheAgentLostTheConversationOfHandsItBack(t *testing.T) {
	ad := newRewinder(provider.Rewind{Session: "rewound", Native: true}, nil, true)
	r := twoTurnsWith(t, dbPath(t), tempDir(t), repo(t), ad)
	rewind := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true, Conversation: true}).Command
	untilReverted(t, r.n.h, r.thread, 1)
	ad.mu.Lock()
	ad.rewind, ad.err, ad.native = provider.Rewind{}, fmt.Errorf("%w: no rollout found for thread id original", provider.ErrNoConversation), false
	ad.mu.Unlock()
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: recoveryOf(rewind), Files: true, Conversation: true})
	got := untilReverted(t, r.n.h, r.thread, 2)
	if got.Status != api.RevertRestored || got.Conversation != api.RewindPortable || got.Session != "" || len(got.Restored) != 1 {
		t.Fatalf("undo %+v; want the files back, and the conversation handed back in a new session", got)
	}
	if text := read(t, r.ws+"/README"); text != "three\n" {
		t.Fatalf("README %q after the undo, want it as it was before the rewind", text)
	}
	submit(t, r.n.h, r.thread, dispatch("again", api.StartNow))
	until(t, r.n.h, r.thread, "the turn after the undo", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
	if _, _, sent := ad.seen(); !strings.HasPrefix(sent[2], "<rewound_conversation>\nThe user undid a rewind") || !strings.Contains(sent[2], "<user>\nnext\n</user>") {
		t.Fatalf("the run after the undo was sent %q; want the turns handed back", sent[2])
	}
	assertReplay(t, r.n.st)
}

// An undo of a rewind where the adapter would branch off the conversation
// from before it only at the next run, as Claude's does, unchecked, is
// portable: the next run is handed the turns.
func TestAnUndoTheAdapterWouldBranchLaterHandsBackTheTurns(t *testing.T) {
	ad := newRewinder(provider.Rewind{Session: "rewound", Native: true}, nil, true)
	r := twoTurnsWith(t, dbPath(t), tempDir(t), repo(t), ad)
	from := sessionOf(t, r.n.st, r.thread)
	rewind := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true}).Command
	untilReverted(t, r.n.h, r.thread, 1)
	ad.mu.Lock()
	ad.rewind, ad.native = provider.Rewind{Session: "later", Native: true, At: "branch"}, false
	ad.mu.Unlock()
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: recoveryOf(rewind), Conversation: true})
	got := untilReverted(t, r.n.h, r.thread, 2)
	if got.Conversation != api.RewindPortable || got.Session != "later" || got.At != "" || got.From != from || len(got.Restored) != 1 {
		t.Fatalf("undo %+v; want it portable in the adapter's new session, with no branch to make", got)
	}
	submit(t, r.n.h, r.thread, dispatch("again", api.StartNow))
	until(t, r.n.h, r.thread, "the turn after the undo", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
	_, opened, sent := ad.seen()
	if spec := opened[2]; spec.Rewound == nil || *spec.Rewound != (provider.Rewound{From: from}) || !strings.HasPrefix(sent[2], "<rewound_conversation>\nThe user undid a rewind") {
		t.Fatalf("the run after the undo opened with rewound %+v and was sent %q; want no branch and the turns handed back", spec.Rewound, sent[2])
	}
	assertReplay(t, r.n.st)
}

// refuses undoes rewind, which fails with why, leaving the thread's
// session and the provider untold.
func refuses(t *testing.T, r reverting, ad *rewinder, rewind api.ID, why string) {
	t.Helper()
	session := sessionOf(t, r.n.st, r.thread)
	specs, _, _ := ad.seen()
	n := len(revertsOf(t, r.n.h, r.thread))
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: recoveryOf(rewind), Conversation: true})
	got := untilReverted(t, r.n.h, r.thread, n+1)
	if got.Status != api.RevertFailed || got.Conversation != api.RewindFailed || !strings.Contains(got.Error, why) || len(got.Restored) != 0 {
		t.Fatalf("undo %+v; want it to fail with %q", got, why)
	}
	if s := sessionOf(t, r.n.st, r.thread); s != session {
		t.Fatalf("the thread's session went from %q to %q", session, s)
	}
	if again, _, _ := ad.seen(); len(again) != len(specs) {
		t.Fatalf("the provider was asked to rewind %d times, want %d", len(again), len(specs))
	}
	assertReplay(t, r.n.st)
}

// An undo of a rewind asked for while a later rewind is still under way
// refuses once that one ends: it is no longer the latest.
func TestAnUndoQueuedBehindAnotherRewindRefuses(t *testing.T) {
	ad := newRewinder(provider.Rewind{}, nil, false)
	r := twoTurnsWith(t, dbPath(t), tempDir(t), repo(t), ad)
	submit(t, r.n.h, r.thread, dispatch("third", api.StartNow))
	until(t, r.n.h, r.thread, "three turns", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
	one := submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.run), Conversation: true}).Command
	untilReverted(t, r.n.h, r.thread, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	var first, released sync.Once
	defer released.Do(func() { close(release) })
	defer runtime.SetCheckpointHook(func(ctx context.Context, step string) {
		if step != "rewound" {
			return
		}
		first.Do(func() {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
	})()
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true})
	<-entered
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: recoveryOf(one), Conversation: true})
	released.Do(func() { close(release) })
	if second := untilReverted(t, r.n.h, r.thread, 2); !slices.Equal(second.Dropped, []api.ID{r.run.Run}) {
		t.Fatalf("the second rewind %+v; want it to drop the second turn", second)
	}
	got := untilReverted(t, r.n.h, r.thread, 3)
	if got.Status != api.RevertFailed || !strings.Contains(got.Error, "only the latest can be undone") || len(got.Restored) != 0 {
		t.Fatalf("undo %+v; want it refused as not the latest", got)
	}
	th, err := r.n.st.Thread(ctx, r.thread)
	if err != nil {
		t.Fatal(err)
	}
	if !th.Runs[1].Dropped || !th.Runs[2].Dropped {
		t.Fatalf("runs %+v after the refused undo; want the conversation at the first turn", th.Runs)
	}
	assertReplay(t, r.n.st)
}
