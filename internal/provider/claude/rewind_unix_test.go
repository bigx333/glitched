//go:build unix

package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

// hiddenFlags are the flags of Claude Code's that rewind a conversation.
var hiddenFlags = []string{"--resume-session-at", "--resume-drops-turn", "--fork-session"}

// wrapper writes a script that starts the test binary as claude, holding
// flags in a comment as Claude Code's program holds its hidden flags, and
// has the test run it: the test binary holds the adapter's own flags, so
// a probe of it would always find them.
func wrapper(t *testing.T, flags ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	writeWrapper(t, path, flags...)
	claude.RunAs(t, path)
	return path
}

func writeWrapper(t *testing.T, path string, flags ...string) {
	t.Helper()
	script := "#!/bin/sh\n# " + strings.Join(flags, " ") + "\nexec '" + must(os.Executable()) + "' \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// sessionArgs is each start's session flags, which argv ends with.
func sessionArgs(t *testing.T, argv string) [][]string {
	t.Helper()
	var got [][]string
	for _, l := range strings.Split(strings.TrimSpace(string(must(os.ReadFile(argv)))), "\n") {
		var args []string
		json.Unmarshal([]byte(l), &args)
		i := slices.IndexFunc(args, func(a string) bool { return a == "--resume" || a == "--session-id" })
		got = append(got, args[i:])
	}
	return got
}

// A rewind forks the thread's Claude session at the message the target
// run left it at, as a session of its own, where the program holds the
// hidden flags and nothing dropped compacted it; else, or where Claude
// Code refuses the fork, the run starts a new conversation on the new
// session. Either way the run never takes the claude the thread's earlier
// runs left, and the thread's later runs resume the new session.
func TestARewindForksTheConversationWhereClaudeCan(t *testing.T) {
	for _, tc := range []struct {
		name      string
		flags     []string // the hidden flags the program holds at the rewind
		launch    []string // and at the launch, when they differ
		refuses   string   // what Claude Code refuses
		dropped   int      // the runs the rewind drops
		compacted bool
		rewinds   bool // the rewind is native
		forks     bool // the launch tries the fork
		drops     bool // naming the dropped run
	}{
		{name: "forks", flags: hiddenFlags, dropped: 1, rewinds: true, forks: true, drops: true},
		{name: "two dropped", flags: hiddenFlags, dropped: 2, rewinds: true, forks: true},
		{name: "no drops flag", flags: []string{"--resume-session-at", "--fork-session"}, dropped: 1, rewinds: true, forks: true},
		{name: "no flags", dropped: 1},
		{name: "no fork flag", flags: []string{"--resume-session-at", "--resume-drops-turn"}, dropped: 1},
		{name: "flags gone at launch", flags: hiddenFlags, launch: []string{}, dropped: 1, rewinds: true},
		{name: "refuses the message", flags: hiddenFlags, refuses: envRefuseAt, dropped: 1, rewinds: true, forks: true, drops: true},
		{name: "refuses the turn", flags: hiddenFlags, refuses: envRefuseDrops, dropped: 1, rewinds: true, forks: true, drops: true},
		{name: "compacted", flags: hiddenFlags, dropped: 1, compacted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := wrapper(t, tc.flags...)
			turns := slices.Repeat([]string{"hi"}, tc.dropped+2)
			argv, started := keep(t, turns, []string{"again-resume"})
			t.Setenv(envSessions, t.TempDir())
			if tc.refuses != "" {
				t.Setenv(tc.refuses, "1")
			}
			ad := &claude.Adapter{Path: path}
			t.Cleanup(func() { ad.Close() })
			bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			var sent prompts
			thread, revert, ws := api.NewID(), api.NewID(), t.TempDir()
			spec := func(gen uint64) provider.SessionSpec {
				return provider.SessionSpec{Thread: thread, Run: api.NewID(), Generation: gen, Workspace: ws,
					Model: api.ModelRef{Provider: "claude", Model: "haiku"}, Access: api.AccessAsk, Tap: sent.tap}
			}

			var heads, asked []string
			for i := range tc.dropped + 1 {
				ag := must(ad.Open(bounded, spec(uint64(i+1))))
				heads = append(heads, finish(t, bounded, ag, "Say hi in three words.").Head)
				asked = append(asked, sent.last())
				if err := ag.Close(bounded); err != nil {
					t.Fatal(err)
				}
			}
			old := started("claude")
			if len(old) != 1 {
				t.Fatalf("the runs before the rewind started claude %v, want one kept between them", old)
			}

			rw := must(ad.Rewind(bounded, provider.RewindSpec{Thread: thread, Revert: revert, Head: heads[0], Dropped: heads[1:], Compacted: tc.compacted}))
			session := claude.SessionOf(revert)
			if rw.Session != session || rw.Native != tc.rewinds || rw.Native != (rw.At != "") {
				t.Fatalf("the rewind is %+v, want session %s, native %v", rw, session, tc.rewinds)
			}
			if tc.launch != nil {
				writeWrapper(t, path, tc.launch...)
			}

			s := spec(uint64(tc.dropped + 2))
			s.Session, s.Rewound = rw.Session, &provider.Rewound{At: rw.At}
			ag := must(ad.Open(bounded, s))
			native := tc.forks && tc.refuses == ""
			if got := ag.(provider.Rewinding).Native(); got != native {
				t.Errorf("the rewound run's claude says native %v, want %v", got, native)
			}
			// A new conversation gets a session of its own, never the one
			// the fork makes, so that a claude that resumes that one holds
			// the kept conversation.
			now := ag.(provider.Sessioned).Session()
			if native != (now == session) || now == claude.SessionOf(thread) {
				t.Errorf("the rewound run's claude is on session %q, want %q when native, else a new one", now, session)
			}
			want := [][]string{{"--resume", session}}
			if tc.forks {
				fork := []string{"--resume", claude.SessionOf(thread), "--resume-session-at", hiAt}
				if tc.drops {
					fork = append(fork, "--resume-drops-turn", asked[1])
				}
				want = append(want, append(fork, "--fork-session", "--session-id", session))
			}
			if !native {
				want = append(want, []string{"--session-id", now})
			}
			if got := sessionArgs(t, argv)[1:]; !reflect.DeepEqual(got, want) {
				t.Errorf("the rewound run started claude with %q, want %q", got, want)
			}
			waitGone(t, old[0], "the rewound run opened, but the claude of the conversation before the rewind")
			if e := finish(t, bounded, ag, "What did I ask you to do last time? Answer in under ten words."); e.Outcome != api.OutcomeCompleted {
				t.Fatalf("the rewound run ended %+v", e)
			}
			if err := ag.Close(bounded); err != nil {
				t.Fatal(err)
			}

			// A run that takes the rewind up again, as one whose first
			// message never went out would, resumes the fork; a new
			// conversation an earlier launch began holds no kept turns,
			// so it starts another, to be handed them.
			again := spec(uint64(tc.dropped + 2))
			again.Session, again.Rewound = s.Session, s.Rewound
			ag = must(ad.Open(bounded, again))
			got := sessionArgs(t, argv)[len(want)+1:]
			if ag.(provider.Rewinding).Native() != native || got[0][0] != "--resume" || got[0][1] != session {
				t.Errorf("the rewound run again started claude with %q, native %v, want the fork resumed, native %v", got, ag.(provider.Rewinding).Native(), native)
			}
			if !native && (got[len(got)-1][0] != "--session-id" || got[len(got)-1][1] == now) {
				t.Errorf("the rewound run again started claude with %q, want a new session once more, not %s", got, now)
			}
			now = ag.(provider.Sessioned).Session()
			if e := finish(t, bounded, ag, "What did I ask you to do last time? Answer in under ten words."); e.Outcome != api.OutcomeCompleted {
				t.Fatalf("the rewound run, again, ended %+v", e)
			}
			if err := ag.Close(bounded); err != nil {
				t.Fatal(err)
			}

			starts := len(sessionArgs(t, argv))
			later := spec(uint64(tc.dropped + 3))
			later.Session = now
			ag = must(ad.Open(bounded, later))
			ag.Close(bounded)
			if n := len(sessionArgs(t, argv)); n != starts {
				t.Errorf("the run after the rewound one started claude again, want it on the claude that one left")
			}
			fresh := &claude.Adapter{Path: path}
			t.Cleanup(func() { fresh.Close() })
			ag = must(fresh.Open(bounded, later))
			ag.Close(bounded)
			if got := sessionArgs(t, argv)[starts:]; !reflect.DeepEqual(got, [][]string{{"--resume", now}}) {
				t.Errorf("a later claude started with %q, want the rewound session resumed", got)
			}
		})
	}
}

// A rewind is native only where the target run said where it left the
// conversation.
func TestARewindToARunWithoutAHeadIsPortable(t *testing.T) {
	ad := &claude.Adapter{Path: wrapper(t, hiddenFlags...)}
	revert := api.NewID()
	rw := must(ad.Rewind(ctx, provider.RewindSpec{Thread: api.NewID(), Revert: revert, Dropped: []string{""}}))
	if rw != (provider.Rewind{Session: claude.SessionOf(revert)}) {
		t.Errorf("a rewind to a run that said nothing of its head is %+v, want a portable one on a new session", rw)
	}
}
