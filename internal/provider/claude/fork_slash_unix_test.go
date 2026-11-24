//go:build unix

package claude_test

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
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

// A rewind recorded before branches named their session still forks the
// thread's own.
func TestClaudeLegacyRewindStillUsesItsOriginalSession(t *testing.T) {
	argv, _ := keep(t, []string{"hi", "hi"}, []string{"again-resume"})
	t.Setenv(envSessions, t.TempDir())
	ad := &claude.Adapter{Path: wrapper(t, hiddenFlags...)}
	t.Cleanup(func() { ad.Close() })
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	src, revert := api.NewID(), api.NewID()
	s := provider.SessionSpec{Thread: src, Run: api.NewID(), Generation: 1, Workspace: t.TempDir(),
		Model: api.ModelRef{Provider: "claude", Model: "haiku"}, Access: api.AccessAsk}
	g := must(ad.Open(c, s))
	finish(t, c, g, "source turn to keep")
	if err := g.Close(c); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		s.Run, s.Generation = api.NewID(), s.Generation+1
		s.Session = claude.SessionOf(revert)
		s.Rewound = &provider.Rewound{At: `{"at":"` + hiAt + `"}`}
		if explicit {
			s.Rewound.From = claude.SessionOf(src)
		}
		g = must(ad.Open(c, s))
		if !g.(provider.Rewinding).Native() {
			t.Fatal("legacy rewind gave up its native fork")
		}
		got := sessionArgs(t, argv)
		want := []string{"--resume", claude.SessionOf(src), "--resume-session-at", hiAt, "--fork-session", "--session-id", s.Session}
		if !reflect.DeepEqual(got[len(got)-1], want) {
			t.Fatalf("legacy rewind launched %q; expected %q", got[len(got)-1], want)
		}
		if err := g.Close(c); err != nil {
			t.Fatal(err)
		}
		revert = api.NewID()
	}
}

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
	if first.Rewound != "" || second.Rewound != api.RewindPortable {
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

// A command after a portable rewind of a pending fork runs in a session
// the next message goes on in.
func TestClaudeACommandAfterARewindKeepsItsSession(t *testing.T) {
	argv, _ := keep(t, []string{"hi", "hi"}, []string{"again-resume"})
	t.Setenv(envSessions, t.TempDir())
	ad := &claude.Adapter{Path: wrapper(t)}
	t.Cleanup(func() { ad.Close() })
	st := must(store.Open(filepath.Join(t.TempDir(), "host.db")))
	defer st.Close()
	h := must(runtime.New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(false)), "claude": ad}))
	defer h.Close()
	src, dst, ws := api.NewID(), api.NewID(), t.TempDir()
	submit := func(thread api.ID, body api.Intent) api.ID {
		t.Helper()
		id := api.NewID()
		if _, err := h.Submit(ctx, api.Command{ID: id, Target: thread, Body: body}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	wait := func(thread api.ID, match func(api.EventBody) bool) {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
			s, err := h.Read(ctx, api.Query{Thread: thread})
			if err != nil {
				t.Fatal(err)
			}
			for _, ev := range s.Events {
				b, _ := ev.Decode()
				if match(b) {
					return
				}
			}
		}
		t.Fatal("expected engine event did not arrive")
	}
	run := func(thread api.ID, body api.Intent) api.ID {
		t.Helper()
		id := submit(thread, body)
		wait(thread, func(b api.EventBody) bool {
			e, ok := b.(api.TurnEnded)
			if ok && e.Run == id && e.Outcome != api.OutcomeCompleted {
				t.Fatalf("run ended %+v", e)
			}
			return ok && e.Run == id
		})
		return id
	}
	send := func(text string) { run(dst, api.MessageDispatch{Text: text, Mode: api.StartNow}) }

	source := run(src, api.ThreadCreate{Workspace: ws, Model: api.ModelRef{Provider: "fake"}, Text: "source context"})
	first := run(dst, api.ThreadCreate{Workspace: ws, Model: api.ModelRef{Provider: "claude", Model: "haiku"}, Text: "/status",
		Fork: &api.Fork{Thread: src, Run: source}})
	send("/help")
	rewind := submit(dst, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointRun, ID: first}, Conversation: true})
	wait(dst, func(b api.EventBody) bool {
		r, ok := b.(api.ThreadReverted)
		if ok && r.Revert == rewind && r.Conversation != api.RewindPortable {
			t.Fatalf("expected a portable rewind, got %+v", r)
		}
		return ok && r.Revert == rewind
	})

	send("/review")
	args := sessionArgs(t, argv)
	last := args[len(args)-1]
	if len(last) != 2 || last[0] != "--session-id" {
		t.Fatalf("fixture expected the portable rewind to open a fresh session, got %q", last)
	}
	slashSession := last[1]

	send("go on")
	args = sessionArgs(t, argv)
	th := must(st.Thread(ctx, dst))
	if th.Session != slashSession {
		t.Fatalf("after /review completed in %s, the next message bound %s; next Claude launches were %q", slashSession, th.Session, slices.Clone(args[len(args)-2:]))
	}
}
