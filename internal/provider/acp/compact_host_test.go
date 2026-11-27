package acp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/acp/acptest"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

type startWatch struct {
	*Adapter
	checkPassed chan struct{}
}

func (a startWatch) Open(ctx context.Context, s provider.SessionSpec) (provider.Agent, error) {
	g, err := a.Adapter.Open(ctx, s)
	if err != nil || !s.ResumeOnly {
		return g, err
	}
	return startWatchAgent{Agent: g, checkPassed: a.checkPassed}, nil
}

type startWatchAgent struct {
	provider.Agent
	checkPassed chan struct{}
}

func (g startWatchAgent) Do(ctx context.Context, a provider.Action) error {
	if _, ok := a.(provider.Start); ok {
		close(g.checkPassed)
	}
	return g.Agent.Do(ctx, a)
}

func compactionHost(t *testing.T, now func() time.Time, end time.Time, resumedModel, cmdsGate string) (*runtime.Host, *store.Store, api.ID, string, <-chan struct{}) {
	t.Helper()
	t.Setenv(acptest.DefaultModel, "gemini-3-pro")
	a, log := fake(t, "compact-agy")
	s := spec(t)
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "Remember this conversation."})
	turn(t, g)
	id := g.(provider.Sessioned).Session()
	closeRun(t, g)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(acptest.DefaultModel, resumedModel)
	if cmdsGate != "" {
		t.Setenv(acptest.Gate, cmdsGate)
	}
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, c := range []api.Command{
		{ID: api.NewID(), Target: s.Thread, Body: api.ThreadCreate{Workspace: s.Workspace, Title: "Gemini cache", Model: api.ModelRef{Provider: "antigravity"}}},
		{ID: s.Run, Target: s.Thread, Body: api.MessageDispatch{Text: "Remember this conversation.", Mode: api.StartNow}},
	} {
		if _, err := st.Submit(ctx, c, end); err != nil {
			t.Fatal(err)
		}
	}
	e, ok, err := st.Lease(ctx, func() []api.ID { return nil })
	if err != nil || !ok {
		t.Fatalf("lease: %v, %v", ok, err)
	}
	for _, o := range []engine.Observation{
		engine.Bound{Run: s.Run, Session: id},
		engine.Started{Run: s.Run},
		engine.Ran{Run: s.Run, Model: "gemini-3-pro"},
		engine.Context{Run: s.Run, Used: 250_000, Window: 1_000_000},
		engine.Ended{Run: s.Run, Outcome: api.OutcomeCompleted},
	} {
		var done int64
		if _, ok := o.(engine.Started); ok {
			done = e.ID
		}
		if _, err := st.Observe(ctx, s.Thread, o, end, done); err != nil {
			t.Fatal(err)
		}
	}
	b := &Adapter{Profile: Antigravity, Path: a.Path}
	t.Cleanup(func() { b.Close() })
	checkPassed := make(chan struct{})
	h, err := runtime.New(ctx, st, provider.Mux{"antigravity": startWatch{b, checkPassed}}, runtime.WithClock(now))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h, st, s.Thread, log, checkPassed
}

func waitFor(t *testing.T, want string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("waited for %s", want)
		}
	}
}

func TestAChangeWhileAgyListsItsCommandsCancelsTheCompaction(t *testing.T) {
	for _, change := range []string{"opt-out", "snooze", "queued message", "cache expired"} {
		t.Run(change, func(t *testing.T) {
			gate := filepath.Join(t.TempDir(), "list-commands-now")
			end := time.Now().UTC()
			var advance atomic.Int64
			advance.Store(int64(engine.AntigravityCacheTTL*9/10 + time.Second))
			now := func() time.Time { return end.Add(time.Duration(advance.Load())) }
			// Blank selection lets the fake's resumed process open on its default.
			h, st, thread, log, _ := compactionHost(t, now, end, "gemini-3-pro", gate)
			waitFor(t, "the compaction's resume", func() bool {
				b, _ := os.ReadFile(log)
				return strings.Contains(string(b), "session/resume")
			})
			ctx := context.Background()
			var changeIntent api.Intent
			switch change {
			case "opt-out":
				h.SetIdleCompaction(false)
			case "snooze":
				changeIntent = api.InboxSet{Disposition: api.Disposition{State: api.Snoozed, Until: end.Add(time.Hour)}}
			case "queued message":
				changeIntent = api.MessageDispatch{Text: "next", Mode: api.StartNow}
			case "cache expired":
				advance.Store(int64(engine.AntigravityCacheTTL + time.Minute))
			}
			if changeIntent != nil {
				if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: changeIntent}); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(gate, nil, 0600); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the maintenance run to finish", func() bool {
				th, err := st.Thread(ctx, thread)
				return err == nil && len(th.Runs) > 1 && th.Runs[1].State == engine.RunFinished
			})
			for _, e := range find(t, wire(t, log), "session/prompt") {
				if strings.Contains(string(e.Params), `"/compact"`) {
					t.Fatalf("ACP received /compact after %s while its command announcement was pending", change)
				}
			}
		})
	}
}

func TestAFailedAgyCompactionFails(t *testing.T) {
	t.Setenv(acptest.DefaultModel, "gemini-3-pro")
	g, _ := resumeFor(t, "compact-failed")
	do(t, g, provider.Start{Text: "/compact"})
	events := turn(t, g)
	closeRun(t, g)
	for _, ev := range events {
		if c, ok := ev.(provider.Context); ok && c.Compacted {
			t.Errorf("ACP reported compaction status failed, but adapter emitted Context{Compacted: true}")
		}
	}
	if e := ended(t, events); e.Outcome != api.OutcomeFailed {
		t.Errorf("ACP reported compaction status failed, but adapter ended %v; want failed", e.Outcome)
	}
}

func TestAgyResumedOnANonGeminiDefaultDoesNotCompact(t *testing.T) {
	end := time.Now().UTC()
	now := func() time.Time { return end.Add(engine.AntigravityCacheTTL*9/10 + time.Second) }
	_, st, thread, log, _ := compactionHost(t, now, end, "claude-sonnet-4-6", "")
	ctx := context.Background()
	waitFor(t, "the maintenance run to finish", func() bool {
		th, err := st.Thread(ctx, thread)
		return err == nil && len(th.Runs) > 1 && th.Runs[1].State == engine.RunFinished
	})
	th, err := st.Thread(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range find(t, wire(t, log), "session/prompt") {
		if strings.Contains(string(e.Params), `"/compact"`) {
			t.Fatalf("ACP received /compact after resuming on non-Gemini default %q (last turn ran Gemini)", th.Confirmed.Model.Model)
		}
	}
}
