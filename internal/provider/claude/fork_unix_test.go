//go:build unix

package claude_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

// A fork of a thread's conversation at its first turn, made as a rewind
// is, forks the source's own session there into the fork's, and leaves
// the source's session for the source's next run to resume.
func TestAForkForksTheSourcesSession(t *testing.T) {
	path := wrapper(t, hiddenFlags...)
	argv, _ := keep(t, []string{"hi", "hi", "hi", "hi"}, []string{"again-resume"})
	t.Setenv(envSessions, t.TempDir())
	ad := &claude.Adapter{Path: path}
	t.Cleanup(func() { ad.Close() })
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var sent prompts
	src, fork, ws := api.NewID(), api.NewID(), t.TempDir()
	spec := func(thread api.ID, gen uint64) provider.SessionSpec {
		return provider.SessionSpec{Thread: thread, Run: api.NewID(), Generation: gen, Workspace: ws,
			Model: api.ModelRef{Provider: "claude", Model: "haiku"}, Access: api.AccessAsk, Tap: sent.tap}
	}
	var heads, asked []string
	for i := range 2 {
		ag := must(ad.Open(bounded, spec(src, uint64(i+1))))
		heads = append(heads, finish(t, bounded, ag, "Say hi in three words.").Head)
		asked = append(asked, sent.last())
		if err := ag.Close(bounded); err != nil {
			t.Fatal(err)
		}
	}

	rw := must(ad.Rewind(bounded, provider.RewindSpec{Thread: src, Revert: fork, Workspace: ws, Head: heads[0], Dropped: heads[1:]}))
	if rw.Session != claude.SessionOf(fork) || !rw.Native {
		t.Fatalf("the fork's rewind is %+v, want native on the fork's own session", rw)
	}
	s := spec(fork, 1)
	s.Session, s.Rewound = rw.Session, &provider.Rewound{At: rw.At}
	ag := must(ad.Open(bounded, s))
	if !ag.(provider.Rewinding).Native() || ag.(provider.Sessioned).Session() != claude.SessionOf(fork) {
		t.Fatalf("the fork's claude says native %v on %q", ag.(provider.Rewinding).Native(), ag.(provider.Sessioned).Session())
	}
	if e := finish(t, bounded, ag, "Go on."); e.Outcome != api.OutcomeCompleted {
		t.Fatalf("the fork's run ended %+v", e)
	}
	ag.Close(bounded)
	want := [][]string{
		{"--resume", claude.SessionOf(fork)},
		{"--resume", claude.SessionOf(src), "--resume-session-at", hiAt, "--resume-drops-turn", asked[1], "--fork-session", "--session-id", claude.SessionOf(fork)},
	}
	if got := sessionArgs(t, argv)[1:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("the fork started claude with %q, want %q", got, want)
	}

	fresh := &claude.Adapter{Path: path}
	t.Cleanup(func() { fresh.Close() })
	ag = must(fresh.Open(bounded, spec(src, 3)))
	ag.Close(bounded)
	if got := sessionArgs(t, argv)[3:]; !reflect.DeepEqual(got, [][]string{{"--resume", claude.SessionOf(src)}}) {
		t.Fatalf("the source's next claude started with %q, want its own session resumed", got)
	}
}
