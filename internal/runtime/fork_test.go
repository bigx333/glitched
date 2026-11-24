package runtime_test

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// forking is a host with two providers, fake and other, and a thread of
// fake's with three turns: one, two and three.
type forking struct {
	n      node
	fake   *rewinder
	other  *rewinder
	thread api.ID
	runs   []api.TurnEnded
}

func newForking(t *testing.T, rewind provider.Rewind, native bool) forking {
	t.Helper()
	f := forking{fake: newRewinder(rewind, nil, native), other: newRewinder(provider.Rewind{}, nil, false), thread: api.NewID()}
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": f.fake, "other": f.other})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	f.n = node{st, h}
	t.Cleanup(f.n.kill)
	submit(t, h, f.thread, api.ThreadCreate{Workspace: "/src/app", Title: "fix it", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Text: "one"})
	for i, text := range []string{"", "two", "three"} {
		if text != "" {
			submit(t, h, f.thread, dispatch(text, api.StartNow))
		}
		until(t, h, f.thread, text+"'s end", func(th api.Thread) bool { return idle(th) && len(runsOf(t, h, f.thread)) == i+1 })
	}
	f.runs = runsOf(t, h, f.thread)
	return f
}

// fork creates a fork of the thread at its run'th run, on provider, with
// text as its first message, and waits for that turn to end.
func (f forking) fork(t *testing.T, run int, prov, text string) api.ID {
	t.Helper()
	fork := api.NewID()
	submit(t, f.n.h, fork, api.ThreadCreate{Workspace: "/src/app", Title: "fix it fork", Model: api.ModelRef{Provider: prov, Model: prov}, Text: text,
		Fork: &api.Fork{Thread: f.thread, Run: f.runs[run].Run}})
	until(t, f.n.h, fork, "the fork's first turn", func(th api.Thread) bool { return idle(th) && len(runsOf(t, f.n.h, fork)) == 1 })
	return fork
}

func (f forking) again(t *testing.T, thread api.ID, text string, runs int) {
	t.Helper()
	submit(t, f.n.h, thread, dispatch(text, api.StartNow))
	until(t, f.n.h, thread, text+"'s end", func(th api.Thread) bool { return idle(th) && len(runsOf(t, f.n.h, thread)) == runs })
}

// untouched fails t unless the source has the events and session it had
// before the fork.
func (f forking) untouched(t *testing.T, before int, session string) {
	t.Helper()
	if n := len(events(t, f.n.h, f.thread)); n != before {
		t.Fatalf("the source has %d events, %d before the fork", n, before)
	}
	if s := sessionOf(t, f.n.st, f.thread); s != session {
		t.Fatalf("the source's session went from %q to %q", session, s)
	}
}

// A thread goes on on another provider as a fork there, from its middle
// turn: the fork's first message hands the agent the turns up to that
// one, none after it, and its next message is its own. The source stays
// as it was, and its provider is never asked to fork anything.
func TestAThreadGoesOnOnAnotherProvider(t *testing.T) {
	f := newForking(t, provider.Rewind{Session: "forked", Native: true}, true)
	before, session := len(events(t, f.n.h, f.thread)), sessionOf(t, f.n.st, f.thread)
	fork := f.fork(t, 1, "other", "go on")
	f.again(t, fork, "and again", 2)
	_, opened, sent := f.other.seen()
	if len(sent) != 2 {
		t.Fatalf("the fork sent %q", sent)
	}
	handed := sent[0]
	if !strings.HasPrefix(handed, "<forked_conversation>") || !strings.HasSuffix(handed, "</forked_conversation>\n\ngo on") ||
		!strings.Contains(handed, "<user>\none\n</user>") || !strings.Contains(handed, "<user>\ntwo\n</user>") || strings.Contains(handed, "three") ||
		strings.Index(handed, "<user>\ntwo") > strings.Index(handed, "<user>\none") {
		t.Fatalf("the fork's first message %q; want turns two and one, then its own", handed)
	}
	if sent[1] != "and again" || opened[0].Rewound == nil || *opened[0].Rewound != (provider.Rewound{}) || opened[0].Session != "" || opened[1].Rewound != nil {
		t.Fatalf("sent %q, opened %+v then %+v; want the handoff taken up by the first run alone", sent[1], opened[0].Rewound, opened[1].Rewound)
	}
	if specs, _, _ := f.fake.seen(); len(specs) != 0 {
		t.Fatalf("the source's provider was asked to rewind %+v", specs)
	}
	if bs := boundOf(t, f.n.h, fork); len(bs) != 1 || bs[0].Rewound != api.RewindPortable {
		t.Fatalf("the fork's session.bound %+v; want one, portable", bs)
	}
	f.untouched(t, before, session)
	assertReplay(t, f.n.st)
}

// A fork on the source's provider forks its conversation at the turn, as
// a rewind would, without its later turns: the fork resumes the
// provider's fork, and its agent is handed nothing.
func TestAForkOnItsProviderForksTheConversation(t *testing.T) {
	f := newForking(t, provider.Rewind{Session: "forked", Native: true, At: "at"}, true)
	before, session := len(events(t, f.n.h, f.thread)), sessionOf(t, f.n.st, f.thread)
	fork := f.fork(t, 1, "fake", "go on")
	f.again(t, fork, "and again", 2)
	specs, opened, sent := f.fake.seen()
	if len(specs) != 1 || !reflect.DeepEqual(specs[0], provider.RewindSpec{Thread: f.thread, Revert: fork, Workspace: "/src/app", Session: session, Head: f.runs[1].Head, Dropped: []string{f.runs[2].Head}}) {
		t.Fatalf("rewind specs %+v; want the source's conversation forked at its second turn, the third dropped", specs)
	}
	last := opened[len(opened)-2:]
	if last[0].Session != "forked" || last[0].Rewound == nil || *last[0].Rewound != (provider.Rewound{From: session, At: "at"}) || last[1].Rewound != nil || last[1].Session != "forked" {
		t.Fatalf("the fork opened %+v then %+v; want the provider's fork taken up once, then resumed", last[0], last[1])
	}
	if got := sent[len(sent)-2:]; got[0] != "go on" || got[1] != "and again" {
		t.Fatalf("the fork sent %q; want its own messages alone", got)
	}
	if bs := boundOf(t, f.n.h, fork); len(bs) != 1 || bs[0].Session != "forked" || bs[0].Rewound != api.RewindNative {
		t.Fatalf("the fork's session.bound %+v; want the provider's fork, native", bs)
	}
	f.untouched(t, before, session)
	assertReplay(t, f.n.st)
}

// A fork its provider can't make natively, or that its agent couldn't
// take up, hands the turns over as text instead.
func TestAForkTheProviderCantMakeIsHandedTheTurns(t *testing.T) {
	for name, tc := range map[string]struct {
		rewind provider.Rewind
		native bool
	}{
		"no native fork":         {rewind: provider.Rewind{Session: "s"}},
		"one the agent couldn't": {rewind: provider.Rewind{Session: "forked", Native: true, At: "at"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newForking(t, tc.rewind, tc.native)
			fork := f.fork(t, 0, "fake", "go on")
			_, _, sent := f.fake.seen()
			if handed := sent[len(sent)-1]; !strings.HasPrefix(handed, "<forked_conversation>") || !strings.Contains(handed, "<user>\none\n</user>") || strings.Contains(handed, "two") {
				t.Fatalf("the fork's first message %q; want the first turn handed", handed)
			}
			if bs := boundOf(t, f.n.h, fork); len(bs) != 1 || bs[0].Rewound != api.RewindPortable {
				t.Fatalf("the fork's session.bound %+v", bs)
			}
			assertReplay(t, f.n.st)
		})
	}
}

// A fork goes on from the conversation its source held when it was made:
// a rewind of the source since that drops the fork's turn leaves the
// fork's handoff as it was.
func TestAForkKeepsTheConversationItWasMadeFrom(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	ad := newRewinder(provider.Rewind{}, nil, false)
	r := twoTurnsWith(t, path, dir, repo(t), ad)
	fork := api.NewID()
	submit(t, r.n.h, fork, api.ThreadCreate{Workspace: r.ws, Title: "fork", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Fork: &api.Fork{Thread: r.thread, Run: r.run.Run}})
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true})
	if got := untilReverted(t, r.n.h, r.thread, 1); got.Conversation != api.RewindPortable {
		t.Fatalf("the source's rewind %+v", got)
	}
	submit(t, r.n.h, fork, dispatch("go on", api.StartNow))
	until(t, r.n.h, fork, "the fork's first turn", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, fork)) == 1 })
	_, _, sent := ad.seen()
	if handed := sent[len(sent)-1]; !strings.HasPrefix(handed, "<forked_conversation>") || !strings.Contains(handed, "<user>\nnext\n</user>") || !strings.HasSuffix(handed, "go on") {
		t.Fatalf("the fork's first message %q; want the turn the source dropped since handed", handed)
	}
	assertReplay(t, r.n.st)
}

// A fork's first message that never reached its agent hands the turns
// over with the next; one that is a command of the agent's goes as it
// is, and the turns go with the message after it.
func TestAForkHandsTheTurnsWithTheFirstMessageSent(t *testing.T) {
	for name, first := range map[string]string{"a start that failed": "go on", "a command": "/status"} {
		t.Run(name, func(t *testing.T) {
			f := newForking(t, provider.Rewind{}, false)
			if first == "go on" {
				f.other.mu.Lock()
				f.other.failStarts = 1
				f.other.mu.Unlock()
			}
			fork := f.fork(t, 2, "other", first)
			f.again(t, fork, "now", 2)
			f.again(t, fork, "later", 3)
			_, _, sent := f.other.seen()
			want := 2
			if first != "go on" {
				want = 3
				if sent[0] != "/status" {
					t.Fatalf("the command went as %q", sent[0])
				}
			}
			if len(sent) != want || !strings.HasPrefix(sent[want-2], "<forked_conversation>") || !strings.HasSuffix(sent[want-2], "now") || sent[want-1] != "later" {
				t.Fatalf("the fork sent %q; want the turns handed once, with \"now\"", sent)
			}
			if bs := boundOf(t, f.n.h, fork); !slices.ContainsFunc(bs, func(b api.SessionBound) bool { return b.Rewound == api.RewindPortable }) {
				t.Fatalf("the fork's session.bound %+v", bs)
			}
			assertReplay(t, f.n.st)
		})
	}
}
