package codex

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// twoRuns runs "one" and "two" on a thread of scenario's, on one
// app-server, and returns the thread's spec, its Codex thread and each
// run's Head.
func twoRuns(t *testing.T, scenario string) (*Adapter, string, provider.SessionSpec, string, []string) {
	t.Helper()
	a, log := fake(t, scenario)
	s := spec(t)
	var heads []string
	for _, said := range []string{"one", "two"} {
		s.Run = api.NewID()
		g := open(t, a, s)
		do(t, g, provider.Start{Text: said})
		heads = append(heads, ended(turn(t, g)).Head)
		s.Session = g.(provider.Sessioned).Session()
		closeRun(t, g)
	}
	return a, log, s, s.Session, heads
}

func fork(a *Adapter, s provider.SessionSpec, head string, dropped ...string) (provider.ConversationFork, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.ForkConversation(ctx, provider.ForkSpec{Thread: s.Thread, Target: api.NewID(), Workspace: s.Workspace, Session: s.Session, Head: head, Dropped: dropped})
}

// A run's Head is Codex's id of its turn.
func TestARunsHeadIsItsTurn(t *testing.T) {
	_, _, _, _, heads := twoRuns(t, "hi")
	if !slices.Equal(heads, []string{"turn-1", "turn-2"}) {
		t.Errorf("heads %q", heads)
	}
}

// A fork Codex refuses, or makes past the run's turn, fails the fork.
func TestAForkNotAtTheTurnFails(t *testing.T) {
	for scenario, want := range map[string]string{
		"fork-past":    "Codex didn't fork the conversation at that turn.",
		"fork-refused": "can't be forked",
	} {
		a, _, s, _, heads := twoRuns(t, scenario)
		r, err := fork(a, s, heads[0], heads[1])
		if err == nil || !strings.Contains(err.Error(), want) || r != (provider.ConversationFork{}) {
			t.Errorf("%s: fork %+v, %v", scenario, r, err)
		}
	}
}

// A thread with no conversation, or a run that started no turn, rewinds
// portably, with no app-server.
func TestAForkWithNothingToForkIsPortable(t *testing.T) {
	a, log := fake(t, "hi")
	s := spec(t)
	for _, c := range []struct{ session, head string }{{"", "turn-1"}, {"019a0000-0000-7000-8000-kept", ""}} {
		s.Session = c.session
		r, err := fork(a, s, c.head)
		if err != nil || r != (provider.ConversationFork{}) {
			t.Errorf("session %q head %q: fork %+v, %v", c.session, c.head, r, err)
		}
	}
	if _, err := os.ReadFile(log); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an app-server started: %v", err)
	}
}

// Start returns only once turn/start is written: a Codex that can't take
// it fails the start, which never claims a send.
func TestAStartCodexNeverGotFails(t *testing.T) {
	a, log, s, _, _ := twoRuns(t, "memory")
	s.Run = api.NewID()
	g := open(t, a, s).(*Agent)
	defer closeRun(t, g)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.Do(context.Background(), provider.Start{Text: "never sent"}); err == nil {
		t.Fatal("Start returned nil for a turn Codex never got")
	}
	if n := len(sent(t, log, "turn/start")); n != 2 {
		t.Fatalf("Codex got %d turn/start; want the two earlier runs'", n)
	}
}

// A fork of a conversation Codex no longer has fails as one no branch
// of can be made, which the host can tell from a fork that failed.
func TestAForkOfAConversationCodexLostSaysSo(t *testing.T) {
	a, log, spec, original, heads := twoRuns(t, "memory")
	if err := os.Remove(filepath.Join(filepath.Dir(log), "rollout-"+original)); err != nil {
		t.Fatal(err)
	}
	if _, err := fork(a, spec, heads[0], heads[1]); !errors.Is(err, provider.ErrNoConversation) {
		t.Fatalf("fork of a lost conversation: %v, want provider.ErrNoConversation", err)
	}
}
