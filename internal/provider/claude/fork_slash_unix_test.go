//go:build unix

package claude_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// A portable fork whose first message was a command goes on in the
// session that command ran in.
func TestClaudePortableForkKeepsItsSlashTurn(t *testing.T) {
	argv, _ := keep(t, []string{"hi", "hi"}, []string{"again-resume"})
	t.Setenv(envSessions, t.TempDir())
	ad := &claude.Adapter{Path: wrapper(t, hiddenFlags...)}
	t.Cleanup(func() { ad.Close() })
	st := must(store.Open(filepath.Join(t.TempDir(), "host.db")))
	defer st.Close()
	h := must(runtime.New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(false)), "claude": ad}))
	defer h.Close()
	src, dst, ws := api.NewID(), api.NewID(), t.TempDir()
	run := func(thread api.ID, body api.Intent) (api.ID, []api.Event) {
		t.Helper()
		id := api.NewID()
		if _, err := h.Submit(ctx, api.Command{ID: id, Target: thread, Body: body}); err != nil {
			t.Fatal(err)
		}
		for end := time.Now().Add(8 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
			s, err := h.Read(ctx, api.Query{Thread: thread})
			if err != nil {
				t.Fatal(err)
			}
			for _, ev := range s.Events {
				b, _ := ev.Decode()
				if e, ok := b.(api.TurnEnded); ok && e.Run == id {
					if e.Outcome != api.OutcomeCompleted {
						t.Fatalf("run ended %+v", e)
					}
					return id, s.Events
				}
			}
		}
		t.Fatal("run did not finish")
		return api.ID{}, nil
	}
	sourceRun, _ := run(src, api.ThreadCreate{Workspace: ws, Model: api.ModelRef{Provider: "fake"}, Text: "source"})
	_, before := run(dst, api.ThreadCreate{Workspace: ws, Model: api.ModelRef{Provider: "claude", Model: "haiku"}, Text: "/status",
		Fork: &api.Fork{Thread: src, Run: sourceRun}})
	_, after := run(dst, api.MessageDispatch{Text: "continue", Mode: api.StartNow})
	bind := func(events []api.Event) api.SessionBound {
		for i := len(events) - 1; i >= 0; i-- {
			b, _ := events[i].Decode()
			if s, ok := b.(api.SessionBound); ok {
				return s
			}
		}
		t.Fatal("no bound session")
		return api.SessionBound{}
	}
	first, second := bind(before), bind(after)
	if first.Fork != "" || second.Fork != api.ForkPortable {
		t.Fatalf("fixture did not defer handoff: %+v then %+v", first, second)
	}
	if first.Session != second.Session {
		var flags []string
		for _, args := range sessionArgs(t, argv) {
			flags = append(flags, strings.Join(args, " "))
		}
		t.Fatalf("handoff after /status discarded the completed slash turn's session %q, bound %q instead; launches: %q", first.Session, second.Session, flags)
	}
}
