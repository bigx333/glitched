package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
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

func rewind(a *Adapter, s provider.SessionSpec, head string, dropped ...string) (provider.Rewind, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.Rewind(ctx, provider.RewindSpec{Thread: s.Thread, Revert: api.NewID(), Workspace: s.Workspace, Session: s.Session, Head: head, Dropped: dropped})
}

// A run's Head is Codex's id of its turn.
func TestARunsHeadIsItsTurn(t *testing.T) {
	_, _, _, _, heads := twoRuns(t, "hi")
	if !slices.Equal(heads, []string{"turn-1", "turn-2"}) {
		t.Errorf("heads %q", heads)
	}
}

// A rewind forks the Codex thread through the run's turn on an app-server
// of its own, which it stops, and checks the fork ends there. The
// thread's next run resumes the fork, with only the kept turns.
func TestARewindForksAtTheRunsTurn(t *testing.T) {
	a, log, s, session, heads := twoRuns(t, "memory")
	r, err := rewind(a, s, heads[0], heads[1])
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if !r.Native || r.Session == "" || r.Session == session || r.At != "" {
		t.Fatalf("rewind %+v of %q", r, session)
	}
	var fork struct {
		ThreadID, LastTurnID, Cwd string
		ExcludeTurns              bool
	}
	forks := sent(t, log, "thread/fork")
	if len(forks) != 1 {
		t.Fatalf("forks %s", forks)
	}
	json.Unmarshal(forks[0], &fork)
	if fork.ThreadID != session || fork.LastTurnID != "turn-1" || fork.Cwd != s.Workspace || !fork.ExcludeTurns {
		t.Errorf("thread/fork %s", forks[0])
	}
	var list struct {
		ThreadID, SortDirection, ItemsView string
		Limit                              int
	}
	lists := sent(t, log, "thread/turns/list")
	if len(lists) != 1 {
		t.Fatalf("turns/list %s", lists)
	}
	json.Unmarshal(lists[0], &list)
	if list.ThreadID != r.Session || list.Limit != 1 || list.SortDirection != "desc" || list.ItemsView != "notLoaded" {
		t.Errorf("thread/turns/list %s", lists[0])
	}
	var forker int
	closed := map[int]bool{}
	for _, e := range wire(t, log) {
		switch e.Method {
		case "thread/fork":
			forker = e.PID
		case "stdin closed":
			closed[e.PID] = true
		}
	}
	if !closed[forker] {
		t.Error("the app-server that forked still runs")
	}

	s.Run, s.Session, s.Rewound = api.NewID(), r.Session, &provider.Rewound{From: session}
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "three"})
	got := text(turn(t, g))
	closeRun(t, g)
	if got != "earlier you said: one" {
		t.Errorf("after the rewind Codex said %q", got)
	}
	if !g.(provider.Rewinding).Native() || g.(provider.Sessioned).Session() != r.Session {
		t.Errorf("native %v, session %q, want %q", g.(provider.Rewinding).Native(), g.(provider.Sessioned).Session(), r.Session)
	}
	var resume struct{ ThreadID string }
	resumed := sent(t, log, "thread/resume")
	if len(resumed) == 1 {
		json.Unmarshal(resumed[0], &resume)
	}
	if resume.ThreadID != r.Session {
		t.Errorf("resumed %s", resumed)
	}
}

// A portable rewind's next run starts a new conversation on a new
// app-server, never on the parked one, which holds the conversation
// before the rewind.
func TestARewoundRunNeverTakesTheParkedAppServer(t *testing.T) {
	a, log, s, session, _ := twoRuns(t, "memory")
	s.Run, s.Session, s.Rewound = api.NewID(), "", &provider.Rewound{From: session}
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "three"})
	got := text(turn(t, g))
	closeRun(t, g)
	if got != "earlier you said: " {
		t.Errorf("after the rewind Codex said %q", got)
	}
	if g.(provider.Rewinding).Native() || g.(provider.Sessioned).Session() == session {
		t.Errorf("native %v, session %q", g.(provider.Rewinding).Native(), g.(provider.Sessioned).Session())
	}
	if n := len(sent(t, log, "thread/start")); n != 2 {
		t.Errorf("%d threads started", n)
	}
}

// A fork Codex refuses, or makes past the run's turn, fails the rewind.
func TestAForkNotAtTheTurnFails(t *testing.T) {
	for scenario, want := range map[string]string{
		"fork-past":    "Codex didn't fork the conversation at that turn.",
		"fork-refused": "can't be forked",
	} {
		a, _, s, _, heads := twoRuns(t, scenario)
		r, err := rewind(a, s, heads[0], heads[1])
		if err == nil || !strings.Contains(err.Error(), want) || r != (provider.Rewind{}) {
			t.Errorf("%s: rewind %+v, %v", scenario, r, err)
		}
	}
}

// A thread with no conversation, or a run that started no turn, rewinds
// portably, with no app-server.
func TestARewindWithNothingToForkIsPortable(t *testing.T) {
	a, log := fake(t, "hi")
	s := spec(t)
	for _, c := range []struct{ session, head string }{{"", "turn-1"}, {"019a0000-0000-7000-8000-kept", ""}} {
		s.Session = c.session
		r, err := rewind(a, s, c.head)
		if err != nil || r != (provider.Rewind{}) {
			t.Errorf("session %q head %q: rewind %+v, %v", c.session, c.head, r, err)
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
