package runtime_test

import (
	"fmt"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// A fork can go into a new worktree of its source's branch: the host's
// check before it makes the worktree resolves the fork as the commit does.
func TestAForkGoesIntoANewWorktree(t *testing.T) {
	project := repo(t)
	ad := newRewinder(provider.Rewind{}, nil, false)
	r := twoTurnsWith(t, dbPath(t), tempDir(t), project, ad)
	fork := api.NewID()
	c := api.Command{ID: api.NewID(), Target: fork, Body: api.ThreadCreate{
		Workspace: project, Title: "new worktree fork", Model: api.ModelRef{Provider: "fake"},
		Worktree: &api.NewWorktree{Base: view(t, r.n.h, r.thread).Branch},
		Fork:     &api.Fork{Thread: r.thread, Run: r.run.Run},
	}}
	receipt, err := r.n.h.Submit(ctx, c)
	if err != nil {
		t.Fatalf("fork from a finished turn into a new worktree rejected: %v", err)
	}
	dst := view(t, r.n.h, fork)
	if dst.Workspace == r.ws || dst.Workspace == project || receipt.Command != c.ID {
		t.Fatalf("fork in %q from %q; expected a distinct worktree and a receipt", dst.Workspace, r.ws)
	}
}

// A portable fork of a portable fork hands its agent the turns of the
// whole line, newest first, up to each fork's turn.
func TestAForkOfAForkKeepsItsAncestors(t *testing.T) {
	f := newForking(t, provider.Rewind{}, false)
	first := f.fork(t, 1, "other", "child request")
	_, _, sent := f.other.seen()
	if !strings.Contains(sent[0], "<user>\none\n</user>") || !strings.Contains(sent[0], "<user>\ntwo\n</user>") {
		t.Fatalf("fixture's first fork didn't receive its ancestors: %q", sent[0])
	}
	grand := api.NewID()
	submit(t, f.n.h, grand, api.ThreadCreate{Workspace: "/src/app", Title: "grandchild", Model: api.ModelRef{Provider: "fake"}, Text: "grandchild request",
		Fork: &api.Fork{Thread: first}})
	until(t, f.n.h, grand, "the grandchild's first turn", func(th api.Thread) bool { return idle(th) && th.LastTurn != nil })
	_, _, sent = f.fake.seen()
	got := sent[len(sent)-1]
	if !strings.Contains(got, "<user>\nchild request\n</user>") || !strings.Contains(got, "<user>\none\n</user>") ||
		!strings.Contains(got, "<user>\ntwo\n</user>") || strings.Contains(got, "<user>\nthree\n</user>") {
		t.Fatalf("grandchild's provider received %q; expected child request plus ancestor's kept turns one and two, not three", got)
	}
}

// A portable rewind of a fork hands its agent the source's turns too,
// whether or not the fork had taken its source up yet.
func TestARewindOfAForkKeepsItsOrigin(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("origin_pending_%t", pending), func(t *testing.T) {
			f := newForking(t, provider.Rewind{}, false)
			first, second := "child request", "child later request"
			if pending {
				first, second = "/status", "/help"
			}
			fork := f.fork(t, 1, "other", first)
			f.again(t, fork, second, 2)
			origin, err := f.n.st.Forked(ctx, fork)
			if err != nil || (origin != nil) != pending {
				t.Fatalf("fixture pending=%v, got %+v, %v", pending, origin, err)
			}
			to := runsOf(t, f.n.h, fork)[0].Run
			submit(t, f.n.h, fork, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointRun, ID: to}, Conversation: true})
			if got := untilReverted(t, f.n.h, fork, 1); got.Conversation != api.RewindPortable {
				t.Fatalf("fixture failed to rewind: %+v", got)
			}
			f.again(t, fork, "after rewind", 3)
			_, _, sent := f.other.seen()
			got := sent[len(sent)-1]
			if !strings.Contains(got, "<user>\none\n</user>") || !strings.Contains(got, "<user>\ntwo\n</user>") {
				remaining, err := f.n.st.Forked(ctx, fork)
				t.Fatalf("rewound fork's provider received %q; ancestor's turns one and two missing; pending origin after send=%v, store error=%v", got, remaining != nil, err)
			}
		})
	}
}

// A first message that starts with a path is no command: it is handed
// the source's turns.
func TestAForkHandsTheTurnsToAPathPrompt(t *testing.T) {
	f := newForking(t, provider.Rewind{}, false)
	const first = "/tmp/example/repro.txt shows the failure. Explain it using our previous conclusion."
	fork := f.fork(t, 1, "other", first)
	_, _, sent := f.other.seen()
	got := sent[len(sent)-1]
	if !strings.Contains(got, "<user>\ntwo\n</user>") || !strings.HasSuffix(got, first) {
		pending, _ := f.n.st.Forked(ctx, fork)
		t.Fatalf("first ordinary prompt was sent as %q, origin still pending=%v; expected the source turn in the prompt", got, pending != nil)
	}
}
