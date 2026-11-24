package codex

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// A fork of a thread at its first turn forks the source's Codex thread
// there, in the fork's folder, and the fork's first run resumes it with
// only that turn; the source's own Codex thread keeps both of its turns.
func TestAForkForksTheSourcesCodexThread(t *testing.T) {
	a, log, src, session, heads := twoRuns(t, "memory")
	fork := spec(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := a.Rewind(ctx, provider.RewindSpec{Thread: src.Thread, Revert: fork.Thread, Workspace: fork.Workspace, Session: session, Head: heads[0], Dropped: heads[1:]})
	if err != nil || !r.Native || r.Session == session {
		t.Fatalf("the fork's rewind %+v, %v", r, err)
	}
	if forks := sent(t, log, "thread/fork"); len(forks) != 1 {
		t.Fatalf("forks %s", forks)
	}

	fork.Run, fork.Session, fork.Rewound = api.NewID(), r.Session, &provider.Rewound{From: session}
	g := open(t, a, fork)
	do(t, g, provider.Start{Text: "three"})
	got := text(turn(t, g))
	if !g.(provider.Rewinding).Native() || g.(provider.Sessioned).Session() != r.Session {
		t.Errorf("the fork's run: native %v, session %q, want %q", g.(provider.Rewinding).Native(), g.(provider.Sessioned).Session(), r.Session)
	}
	closeRun(t, g)
	if got != "earlier you said: one" {
		t.Errorf("the fork's Codex said %q", got)
	}

	src.Run = api.NewID()
	g = open(t, a, src)
	do(t, g, provider.Start{Text: "three"})
	got = text(turn(t, g))
	closeRun(t, g)
	if got != "earlier you said: one | two" || g.(provider.Sessioned).Session() != session {
		t.Errorf("the source's Codex said %q on %q after the fork", got, g.(provider.Sessioned).Session())
	}
}
