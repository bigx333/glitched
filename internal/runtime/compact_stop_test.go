package runtime_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/runtime"
)

// An idle compaction that ends early, stopped or failed, leaves the
// thread as if it had not started: the next wake, a child's report or a
// message the user sent while it ran, starts a turn that resumes the
// parent's conversation and carries that wake.
func TestAnIdleCompactionCutShortLeavesTheNextWakeItsTurn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fail    bool
		message bool
		want    string
	}{
		{name: "stopped, then a child's report", want: "child result"},
		{name: "failed, then a child's report", fail: true, want: "child result"},
		{name: "stopped, with a message sent during it", message: true, want: "follow up"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
			var clockMu sync.Mutex
			clock := func() time.Time {
				clockMu.Lock()
				defer clockMu.Unlock()
				return now
			}
			expires := now.Add(time.Hour)
			compacting := make(chan struct{})
			compactEnd := make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-compactEnd:
				default:
					close(compactEnd)
				}
			})
			work, child := make(chan struct{}), make(chan struct{})
			ad := &compactAdapter{
				session: func(spec provider.SessionSpec) string { return "sess-" + spec.Thread.String() },
				onStart: func(_ provider.SessionSpec, text string) ([]provider.Event, <-chan struct{}) {
					switch text {
					case "/compact":
						close(compacting)
						if tc.fail {
							return []provider.Event{provider.TurnEnded{Outcome: api.OutcomeFailed, Error: "compaction failed"}}, compactEnd
						}
						return nil, compactEnd
					case "work":
						return []provider.Event{provider.Context{Used: 250000, Window: 1000000, Model: "claude-opus-4-6", CacheExpires: expires}}, work
					case "child task":
						return []provider.Event{provider.TextDelta{Text: "child result"}}, child
					}
					return nil, nil
				},
			}
			n := serveOpts(t, dbPath(t), provider.Mux{"claude": ad}, runtime.WithClock(clock), runtime.WithIdleCompaction(true))
			model := api.ModelRef{Provider: "claude", Model: "claude-opus-4-6"}
			parent := api.NewID()
			submit(t, n.h, parent, api.ThreadCreate{Workspace: "/src/app", Title: "parent", Model: model})
			submit(t, n.h, parent, dispatch("work", api.StartNow))
			v := until(t, n.h, parent, "the parent's turn", func(v api.Thread) bool { return v.Run == api.RunRunning && v.Turn != nil })
			if !tc.message {
				submit(t, n.h, api.NewID(), api.ThreadCreate{Workspace: "/src/app", Title: "child", Model: model, Parent: parent, Run: v.Turn.Run, Text: "child task"})
			}
			close(work)
			until(t, n.h, parent, "the parent idle", func(v api.Thread) bool { return v.Run == api.RunIdle && v.LastTurn != nil })

			clockMu.Lock()
			now = expires.Add(-engine.ClaudeCacheTTL/10 + time.Second)
			clockMu.Unlock()
			n.h.SetIdleCompaction(false)
			n.h.SetIdleCompaction(true)
			<-compacting
			until(t, n.h, parent, "the compaction", func(v api.Thread) bool { return v.Turn != nil && v.Turn.Silent })
			if tc.message {
				submit(t, n.h, parent, dispatch("follow up", api.StartNow))
			}
			if tc.fail {
				close(compactEnd)
			} else {
				submit(t, n.h, parent, api.TurnInterrupt{})
			}
			until(t, n.h, parent, "the compaction's end", func(v api.Thread) bool { return v.Turn == nil || !v.Turn.Silent })
			if !tc.message {
				close(child)
			}

			deadline := time.Now().Add(5 * time.Second)
			for {
				starts := ad.recorded()
				i := len(starts) - 1
				if i >= 0 && starts[i].spec.Thread == parent && !starts[i].spec.ResumeOnly && starts[i].text != "work" {
					if got := starts[i]; got.spec.Session != "sess-"+parent.String() || !strings.Contains(got.text, tc.want) {
						t.Fatalf("the next turn resumed %q with %q; want the parent's session and %q", got.spec.Session, got.text, tc.want)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("no turn followed the compaction; starts: %+v", starts)
				}
				time.Sleep(2 * time.Millisecond)
			}
		})
	}
}
