package runtime_test

import (
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// A fork can go into a new worktree of its source's branch: the host's
// check before it makes the worktree resolves the fork as the commit does.
func TestAForkGoesIntoANewWorktree(t *testing.T) {
	project := repo(t)
	ad := newForkAdapter(provider.ConversationFork{}, nil, false)
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
	f := newForking(t, provider.ConversationFork{}, false)
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

// A first message that starts with a path is no command: it is handed
// the source's turns.
func TestAForkHandsTheTurnsToAPathPrompt(t *testing.T) {
	f := newForking(t, provider.ConversationFork{}, false)
	const first = "/tmp/example/repro.txt shows the failure. Explain it using our previous conclusion."
	fork := f.fork(t, 1, "other", first)
	_, _, sent := f.other.seen()
	got := sent[len(sent)-1]
	if !strings.Contains(got, "<user>\ntwo\n</user>") || !strings.HasSuffix(got, first) {
		pending, _ := f.n.st.Forked(ctx, fork)
		t.Fatalf("first ordinary prompt was sent as %q, origin still pending=%v; expected the source turn in the prompt", got, pending != nil)
	}
}
