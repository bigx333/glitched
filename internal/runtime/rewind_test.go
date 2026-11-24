package runtime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// rewinder is the fake provider with a Rewind: it answers rewind and err,
// and keeps each RewindSpec it was given, each run's SessionSpec, and what
// each run's agent was first sent. Its agents say each turn's head is
// "head-<run>", and that they took a rewound conversation up natively
// when native.
type rewinder struct {
	*fake.Adapter
	rewind provider.Rewind
	err    error
	native bool

	mu     sync.Mutex
	specs  []provider.RewindSpec
	opened []provider.SessionSpec
	sent   []string
	// failStarts is how many of the next starts fail before they send.
	failStarts int
	// say, when set, is the reply each turn ends with.
	say string
	// held, when set, closes once a start sent its message, which then
	// returns only once release closes.
	held, release chan struct{}
}

func newRewinder(rewind provider.Rewind, err error, native bool) *rewinder {
	return &rewinder{Adapter: fake.New(fake.Turn(false)), rewind: rewind, err: err, native: native}
}

func (r *rewinder) Rewind(_ context.Context, spec provider.RewindSpec) (provider.Rewind, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs = append(r.specs, spec)
	return r.rewind, r.err
}

func (r *rewinder) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := r.Adapter.Open(ctx, spec)
	r.mu.Lock()
	r.opened = append(r.opened, spec)
	r.mu.Unlock()
	return &rewinding{Agent: ag, r: r, run: spec.Run}, err
}

func (r *rewinder) seen() ([]provider.RewindSpec, []provider.SessionSpec, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.specs), slices.Clone(r.opened), slices.Clone(r.sent)
}

type rewinding struct {
	provider.Agent
	r   *rewinder
	run api.ID
}

func (g *rewinding) Do(ctx context.Context, a provider.Action) error {
	if s, ok := a.(provider.Start); ok {
		g.r.mu.Lock()
		defer g.r.mu.Unlock()
		if g.r.failStarts > 0 {
			g.r.failStarts--
			return errors.New("the start failed")
		}
		g.r.sent = append(g.r.sent, s.Text)
		if held, release := g.r.held, g.r.release; held != nil {
			g.r.held = nil
			if err := g.Agent.Do(ctx, a); err != nil {
				return err
			}
			g.r.mu.Unlock()
			close(held)
			<-release
			g.r.mu.Lock()
			return nil
		}
	}
	return g.Agent.Do(ctx, a)
}

func (g *rewinding) Next(ctx context.Context) (provider.Event, error) {
	e, err := g.Agent.Next(ctx)
	g.r.mu.Lock()
	say := g.r.say
	g.r.mu.Unlock()
	if d, ok := e.(provider.TextDelta); ok && d.Text == " Done." && say != "" {
		e = provider.TextDelta{Text: say}
	}
	if end, ok := e.(provider.TurnEnded); ok {
		end.Head = "head-" + g.run.String()
		e = end
	}
	return e, err
}

func (g *rewinding) Native() bool { return g.r.native }

func sessionOf(t *testing.T, st *store.Store, thread api.ID) string {
	t.Helper()
	th, err := st.Thread(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	return th.Session
}

func boundOf(t *testing.T, ep api.Endpoint, thread api.ID) []api.SessionBound {
	t.Helper()
	var out []api.SessionBound
	for _, e := range events(t, ep, thread) {
		if b, _ := e.Decode(); b != nil {
			if s, ok := b.(api.SessionBound); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// runs is the thread's runs, oldest first, by their turn.ended.
func runsOf(t *testing.T, ep api.Endpoint, thread api.ID) []api.TurnEnded {
	t.Helper()
	var out []api.TurnEnded
	for _, e := range events(t, ep, thread) {
		if b, _ := e.Decode(); b != nil {
			if r, ok := b.(api.TurnEnded); ok {
				out = append(out, r)
			}
		}
	}
	return out
}

// A rewind of the conversation alone, by a provider that can't rewind
// its own, leaves the files and the thread's session as a new one's: the
// next run's message hands the agent the kept turn, newest first, and the
// run after it gets nothing more. The adapter is told where each run left
// the conversation.
func TestAPortableRewindHandsTheKeptTurnsOnce(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	ad := newRewinder(provider.Rewind{}, nil, false)
	r := twoTurnsWith(t, path, dir, repo(t), ad)
	ended := runsOf(t, r.n.h, r.thread)
	first, second := ended[0], ended[1]
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true})
	got := untilReverted(t, r.n.h, r.thread, 1)
	if got.Status != api.RevertKept || got.Conversation != api.RewindPortable || got.Session != "" || got.Recovery != "" {
		t.Fatalf("reverted %+v; want the conversation portable and the files kept", got)
	}
	if text := read(t, filepath.Join(r.ws, "README")); text != "three\n" {
		t.Fatalf("README %q; a rewind of the conversation alone left it as it was", text)
	}
	specs, _, _ := ad.seen()
	if len(specs) != 1 || specs[0].Head != first.Head || !slices.Equal(specs[0].Dropped, []string{second.Head}) || specs[0].Revert != got.Revert || specs[0].Compacted {
		t.Fatalf("rewind specs %+v; want the first run's head, and the second's dropped", specs)
	}
	if first.Head != "head-"+first.Run.String() {
		t.Fatalf("the first run's turn.ended has head %q", first.Head)
	}

	submit(t, r.n.h, r.thread, dispatch("again", api.StartNow))
	until(t, r.n.h, r.thread, "the turn after the rewind", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
	submit(t, r.n.h, r.thread, dispatch("and again", api.StartNow))
	until(t, r.n.h, r.thread, "the turn after that", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 4 })
	_, opened, sent := ad.seen()
	if len(sent) != 4 || len(opened) != 4 {
		t.Fatalf("sent %q to %d agents", sent, len(opened))
	}
	kept := sent[2]
	if !strings.HasPrefix(kept, "<rewound_conversation>") || !strings.Contains(kept, "<user>\ngo\n</user>") || !strings.Contains(kept, "<assistant>\nDone.\n</assistant>") || strings.Contains(kept, "next") || !strings.HasSuffix(kept, "</rewound_conversation>\n\nagain") {
		t.Fatalf("the run after the rewind was sent %q; want the kept turn, then its message", kept)
	}
	if opened[2].Rewound == nil || *opened[2].Rewound != (provider.Rewound{}) || opened[3].Rewound != nil || sent[3] != "and again" {
		t.Fatalf("rewound %+v then %+v, sent %q; want the rewind taken up by the first run after it alone", opened[2].Rewound, opened[3].Rewound, sent[3])
	}
	if bs := boundOf(t, r.n.h, r.thread); len(bs) != 1 || bs[0].Rewound != api.RewindPortable {
		t.Fatalf("session.bound %+v; want one, of the run that took the rewind up", bs)
	}
	assertReplay(t, r.n.st)
}

// A native rewind gives the thread the adapter's session, and the next
// run the rewind to take up, From and At: an agent that took it up gets
// no kept turns, and one that couldn't gets them, as a portable one. One
// its adapter made already, as Codex's, is taken up so too: the provider
// may have lost it since.
func TestANativeRewindIsTakenUpByTheNextRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		at     string
		native bool
	}{
		{"natively", "at", true},
		{"refused at the launch", "at", false},
		{"made by the rewind", "", true},
		{"lost since the rewind made it", "", false},
	} {
		native := tc.native
		t.Run(tc.name, func(t *testing.T) {
			path, dir := dbPath(t), tempDir(t)
			ad := newRewinder(provider.Rewind{Session: "new", Native: true, At: tc.at}, nil, native)
			r := twoTurnsWith(t, path, dir, repo(t), ad)
			from := sessionOf(t, r.n.st, r.thread)
			submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true})
			got := untilReverted(t, r.n.h, r.thread, 1)
			if got.Conversation != api.RewindNative || got.Session != "new" || got.At != tc.at || got.From != from {
				t.Fatalf("reverted %+v; want the native rewind's session, from %q at its At", got, from)
			}
			if s := sessionOf(t, r.n.st, r.thread); s != "new" {
				t.Fatalf("the thread's session is %q", s)
			}
			submit(t, r.n.h, r.thread, dispatch("again", api.StartNow))
			until(t, r.n.h, r.thread, "the turn after the rewind", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
			_, opened, sent := ad.seen()
			if spec := opened[2]; spec.Session != "new" || spec.Rewound == nil || *spec.Rewound != (provider.Rewound{From: from, At: tc.at}) {
				t.Fatalf("the run after the rewind opened with session %q, rewound %+v", spec.Session, spec.Rewound)
			}
			want := api.RewindNative
			if !native {
				want = api.RewindPortable
			}
			if handed := strings.HasPrefix(sent[2], "<rewound_conversation>"); handed == native {
				t.Fatalf("sent %q; native %v", sent[2], native)
			}
			if bs := boundOf(t, r.n.h, r.thread); len(bs) != 1 || bs[0] != (api.SessionBound{Run: bs[0].Run, Session: "new", Rewound: want}) {
				t.Fatalf("session.bound %+v; want the new session, %s", bs, want)
			}
			assertReplay(t, r.n.st)
		})
	}
}

// A rewind the provider can't make fails the revert before any file
// moves: no recovery checkpoint is taken, and the session stays.
func TestARewindThatFailsLeavesTheFiles(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	ad := newRewinder(provider.Rewind{}, context.DeadlineExceeded, false)
	r := twoTurnsWith(t, path, dir, repo(t), ad)
	before := sessionOf(t, r.n.st, r.thread)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true, Conversation: true})
	got := untilReverted(t, r.n.h, r.thread, 1)
	if got.Status != api.RevertFailed || got.Conversation != api.RewindFailed || got.Session != "" || !strings.Contains(got.Error, "so nothing changed") {
		t.Fatalf("reverted %+v; want it failed, with nothing changed", got)
	}
	if text := read(t, filepath.Join(r.ws, "README")); text != "three\n" || !exists(filepath.Join(r.ws, "new.txt")) {
		t.Fatalf("README %q: the files moved", text)
	}
	if refs := refsOf(t, theStore(t, dir), r.thread); slices.ContainsFunc(refs, func(ref string) bool { return strings.Contains(ref, "/recovery/") }) {
		t.Fatalf("refs %v; a failed rewind took a recovery checkpoint", refs)
	}
	if s := sessionOf(t, r.n.st, r.thread); s != before {
		t.Fatalf("the session went from %q to %q", before, s)
	}
	assertReplay(t, r.n.st)
}

// A rewind with the files puts them back as the run left them too.
func TestARewindTakesTheFilesBackToo(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	ad := newRewinder(provider.Rewind{}, nil, false)
	r := twoTurnsWith(t, path, dir, repo(t), ad)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true, Conversation: true})
	got := untilReverted(t, r.n.h, r.thread, 1)
	if got.Status != api.RevertRestored || got.Conversation != api.RewindPortable || got.Recovery == "" {
		t.Fatalf("reverted %+v; want the files restored and the conversation portable", got)
	}
	if exists(filepath.Join(r.ws, "new.txt")) || read(t, filepath.Join(r.ws, "README")) == "three\n" {
		t.Fatal("the files aren't as the first run left them")
	}
	assertReplay(t, r.n.st)
}

// A host that stops after the rewind and before the revert commits
// rewinds again on the next host, and logs one thread.reverted.
func TestARewindCutOffRunsAgain(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	r := twoTurnsWith(t, path, dir, repo(t), newRewinder(provider.Rewind{Session: "lost"}, nil, false))
	entered := make(chan struct{})
	reset := runtime.SetCheckpointHook(func(ctx context.Context, step string) {
		if step == "rewound" {
			close(entered)
			<-ctx.Done()
		}
	})
	defer reset()
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the rewind never ran")
	}
	r.n.kill()
	reset()
	again := newRewinder(provider.Rewind{Session: "made"}, nil, false)
	n := serveCheckpoints(t, path, dir, again)
	got := untilReverted(t, n.h, r.thread, 1)
	if specs, _, _ := again.seen(); got.Session != "made" || len(specs) != 1 || len(revertsOf(t, n.h, r.thread)) != 1 {
		t.Fatalf("after the restart: %+v, rewound %d times here; want the next host's one rewind", got, len(specs))
	}
	assertReplay(t, n.st)
}

// A replay of a revert cut off after it put the files back, whose rewind
// the provider now refuses, puts the files back as they were: the revert
// fails, and changes nothing.
func TestAReplayWhoseRewindIsRefusedPutsTheFilesBack(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	r := twoTurnsWith(t, path, dir, repo(t), newRewinder(provider.Rewind{}, nil, false))
	entered := make(chan struct{})
	reset := runtime.SetCheckpointHook(func(ctx context.Context, step string) {
		if step == "applied" {
			close(entered)
			<-ctx.Done()
		}
	})
	defer reset()
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true, Conversation: true})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the revert never put the files back")
	}
	if text := read(t, filepath.Join(r.ws, "README")); text == "three\n" {
		t.Fatal("the cut-off revert left README as it was")
	}
	r.n.kill()
	reset()
	n := serveCheckpoints(t, path, dir, newRewinder(provider.Rewind{}, errors.New("fork unavailable on replay"), false))
	got := untilReverted(t, n.h, r.thread, 1)
	if got.Status != api.RevertFailed || got.Conversation != api.RewindFailed || !strings.Contains(got.Error, "fork unavailable on replay") || !strings.Contains(got.Error, "put back as they were") {
		t.Fatalf("reverted %+v; want it failed, the files put back", got)
	}
	if text := read(t, filepath.Join(r.ws, "README")); text != "three\n" || !exists(filepath.Join(r.ws, "new.txt")) {
		t.Fatalf("README %q, new.txt there %v; want the files as they were before the revert", text, exists(filepath.Join(r.ws, "new.txt")))
	}
	assertReplay(t, n.st)
}

// A rewind of the conversation alone holds back no start, so its end
// leaves the mark of a revert of the files after it, under which another
// thread in the worktree still waits.
func TestARewindLeavesAFileRevertsMark(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	r := twoTurnsWith(t, path, dir, repo(t), newRewinder(provider.Rewind{}, nil, false))
	rewinding, rewound, held, release := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer runtime.SetCheckpointHook(func(ctx context.Context, step string) {
		switch step {
		case "rewound":
			close(rewinding)
			<-rewound
		case "recovered":
			close(held)
			<-release
		}
	})()
	var once sync.Once
	defer once.Do(func() { close(release) })
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true})
	<-rewinding
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true})
	other := api.NewID()
	write(t, filepath.Join(r.ws, "sub", "notes"), "x\n")
	submit(t, r.n.h, other, api.ThreadCreate{Workspace: filepath.Join(r.ws, "sub"), Title: "Other", Model: api.ModelRef{Provider: "fake", Model: "fake"}})
	close(rewound)
	<-held
	submit(t, r.n.h, other, dispatch("write", api.StartNow))
	until(t, r.n.h, other, "the other thread's start to wait on the revert", func(api.Thread) bool { return runtime.Waiting(r.n.h, other) })
	if th := view(t, r.n.h, other); th.Run != api.RunStarting {
		t.Fatalf("the other thread is %v while the revert puts the files back, want it starting", th.Run)
	}
	once.Do(func() { close(release) })
	untilReverted(t, r.n.h, r.thread, 2)
	until(t, r.n.h, other, "the other thread's turn after the revert", func(th api.Thread) bool { return idle(th) && th.LastTurn != nil })
	assertReplay(t, r.n.st)
}

// A second rewind follows the conversation: the run after it is handed the
// turns the conversation holds, not one the first rewind took out, and the
// adapter is told only of the runs it drops now.
func TestASecondPortableRewindHandsTheConversationsTurns(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	ad := newRewinder(provider.Rewind{}, nil, false)
	r := twoTurnsWith(t, path, dir, repo(t), ad)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true})
	untilReverted(t, r.n.h, r.thread, 1)
	submit(t, r.n.h, r.thread, dispatch("three", api.StartNow))
	until(t, r.n.h, r.thread, "the third turn", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
	submit(t, r.n.h, r.thread, dispatch("four", api.StartNow))
	until(t, r.n.h, r.thread, "the fourth turn", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 4 })
	ended := runsOf(t, r.n.h, r.thread)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointRun, ID: ended[2].Run}, Conversation: true})
	if got := untilReverted(t, r.n.h, r.thread, 2); !slices.Equal(got.Dropped, []api.ID{ended[3].Run}) {
		t.Fatalf("the second rewind dropped %v, want the fourth run alone", got.Dropped)
	}
	submit(t, r.n.h, r.thread, dispatch("five", api.StartNow))
	until(t, r.n.h, r.thread, "the fifth turn", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 5 })
	specs, _, sent := ad.seen()
	if len(specs) != 2 || specs[1].Head != ended[2].Head || !slices.Equal(specs[1].Dropped, []string{ended[3].Head}) {
		t.Fatalf("rewind specs %+v; want the second at the third run's head, dropping the fourth's", specs)
	}
	kept := sent[4]
	for _, want := range []string{"<user>\nthree\n</user>", "<user>\ngo\n</user>"} {
		if !strings.Contains(kept, want) {
			t.Errorf("the run after the second rewind was sent %q; want %q in it", kept, want)
		}
	}
	for _, gone := range []string{"<user>\nnext\n</user>", "<user>\nfour\n</user>"} {
		if strings.Contains(kept, gone) {
			t.Errorf("the run after the second rewind was sent %q; want no %q, which a rewind took out", kept, gone)
		}
	}
	assertReplay(t, r.n.st)
}

// A run after a rewind whose first message never went out leaves the
// rewind to the next run, which is handed the kept turns.
func TestAFailedStartLeavesTheHandoff(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	ad := newRewinder(provider.Rewind{}, nil, false)
	r := twoTurnsWith(t, path, dir, repo(t), ad)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true})
	untilReverted(t, r.n.h, r.thread, 1)
	ad.mu.Lock()
	ad.failStarts = 1
	ad.mu.Unlock()
	submit(t, r.n.h, r.thread, dispatch("lost", api.StartNow))
	until(t, r.n.h, r.thread, "the run whose start failed", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
	if bs := boundOf(t, r.n.h, r.thread); len(bs) != 0 {
		t.Fatalf("session.bound %+v after a start that sent nothing; want the rewind still to take up", bs)
	}
	submit(t, r.n.h, r.thread, dispatch("again", api.StartNow))
	until(t, r.n.h, r.thread, "the run after it", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 4 })
	_, _, sent := ad.seen()
	if last := sent[len(sent)-1]; !strings.HasPrefix(last, "<rewound_conversation>") || !strings.HasSuffix(last, "again") {
		t.Fatalf("the run after the failed start was sent %q; want the kept turns, then its message", last)
	}
	if bs := boundOf(t, r.n.h, r.thread); len(bs) != 1 || bs[0].Rewound != api.RewindPortable {
		t.Fatalf("session.bound %+v; want the one of the run that sent the kept turns", bs)
	}
	assertReplay(t, r.n.st)
}

// A handoff always hands the turn the conversation was rewound to, cut to
// fit, and leaves out the oldest where the rest don't: the whole block,
// its own text too, in 48 KiB, and still UTF-8.
func TestAHandoffKeepsTheTurnItRewoundTo(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	ad := newRewinder(provider.Rewind{}, nil, false)
	r := twoTurnsWith(t, path, dir, repo(t), ad)
	big := "big " + strings.Repeat("ü", 30<<10)
	ad.mu.Lock()
	ad.say = strings.Repeat("é", 30<<10)
	ad.mu.Unlock()
	submit(t, r.n.h, r.thread, dispatch(big, api.StartNow))
	until(t, r.n.h, r.thread, "the big turn", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
	ad.mu.Lock()
	ad.say = ""
	ad.mu.Unlock()
	submit(t, r.n.h, r.thread, dispatch("after", api.StartNow))
	until(t, r.n.h, r.thread, "the turn after it", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 4 })
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointRun, ID: runsOf(t, r.n.h, r.thread)[2].Run}, Conversation: true})
	untilReverted(t, r.n.h, r.thread, 1)
	submit(t, r.n.h, r.thread, dispatch("go on", api.StartNow))
	until(t, r.n.h, r.thread, "the run after the rewind", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 5 })
	_, _, sent := ad.seen()
	kept := strings.TrimSuffix(sent[4], "go on")
	if !strings.HasSuffix(kept, "</rewound_conversation>\n\n") || !strings.Contains(kept, "<user>\nbig üü") || !strings.Contains(kept, "<assistant>\néé") || strings.Count(kept, "[the rest didn't fit]") != 2 || strings.Contains(kept, "after") {
		t.Fatalf("the run after the rewind was handed %q...; want the big turn's message and reply cut to fit", kept[:min(len(kept), 300)])
	}
	if len(kept) > 48<<10 || !utf8.ValidString(kept) {
		t.Fatalf("the handoff takes %d bytes, UTF-8 %v; want at most %d, and UTF-8", len(kept), utf8.ValidString(kept), 48<<10)
	}
	assertReplay(t, r.n.st)
}

// A replay of a revert cut off before it began to put the files back,
// whose rewind the provider now refuses, touches no file: an ignored
// draft at a path the target holds stays.
func TestAReplayCutOffBeforeTheFilesMovedTouchesNone(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	r := twoTurnsWith(t, path, dir, repo(t), newRewinder(provider.Rewind{}, nil, false))
	submit(t, r.n.h, r.thread, dispatch("three", api.StartNow))
	until(t, r.n.h, r.thread, "the third turn", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
	draft := filepath.Join(r.ws, "new.txt")
	write(t, filepath.Join(r.ws, ".gitignore"), "new.txt\n")
	write(t, draft, "a draft, in no checkpoint\n")
	entered := make(chan struct{})
	reset := runtime.SetCheckpointHook(func(ctx context.Context, step string) {
		if step == "recovered" {
			close(entered)
			<-ctx.Done()
		}
	})
	defer reset()
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.run), Files: true, Conversation: true})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the revert never saved the files first")
	}
	r.n.kill()
	reset()
	n := serveCheckpoints(t, path, dir, newRewinder(provider.Rewind{}, errors.New("fork refused after restart"), false))
	got := untilReverted(t, n.h, r.thread, 1)
	if got.Status != api.RevertFailed || strings.Contains(got.Error, "put back") {
		t.Fatalf("reverted %+v; want it failed, with no file put back", got)
	}
	if text := read(t, draft); text != "a draft, in no checkpoint\n" {
		t.Fatalf("new.txt %q; the replay wrote over a draft the attempt never touched", text)
	}
	if text := read(t, filepath.Join(r.ws, "README")); text != "three\n" || !exists(filepath.Join(r.ws, ".gitignore")) {
		t.Fatalf("README %q, .gitignore there %v; want the files as they were", text, exists(filepath.Join(r.ws, ".gitignore")))
	}
	assertReplay(t, n.st)
}

// A replay of a revert cut off once it began to write, whose rewind the
// provider now refuses, puts back exactly the paths the attempt's plan
// named, either side of a folder that became a file or a file that became
// a folder, and leaves a path the attempt never had to write: an ignored
// file already as the target has it stays.
func TestARefusedReplayPutsBackOnlyThePathsItsPlanNamed(t *testing.T) {
	for _, c := range []string{"a folder became a file", "a file became a folder", "an ignored file already the target's"} {
		t.Run(c, func(t *testing.T) {
			path, dir := dbPath(t), tempDir(t)
			r := twoTurnsWith(t, path, dir, repo(t), newRewinder(provider.Rewind{}, nil, false))
			slot := filepath.Join(r.ws, "slot")
			target, before := slot, filepath.Join(slot, "notes.txt")
			if c == "a file became a folder" {
				target, before = before, slot
			}
			write(t, target, "the target's\n")
			submit(t, r.n.h, r.thread, dispatch("target", api.StartNow))
			untilCheckpoints(t, r.n.h, r.thread, 4)
			until(t, r.n.h, r.thread, "the target turn", idle)
			to := runsOf(t, r.n.h, r.thread)[2].Run
			if err := os.RemoveAll(slot); err != nil {
				t.Fatal(err)
			}
			step, want := "applied", "before the revert\n"
			if c == "an ignored file already the target's" {
				write(t, filepath.Join(r.ws, ".gitignore"), "slot\n")
				before, step, want = slot, "applying", "the target's\n"
			}
			write(t, before, want)
			submit(t, r.n.h, r.thread, dispatch("after the target", api.StartNow))
			untilCheckpoints(t, r.n.h, r.thread, 5)
			until(t, r.n.h, r.thread, "the later turn", idle)
			entered := make(chan struct{})
			reset := runtime.SetCheckpointHook(func(ctx context.Context, s string) {
				if s == step {
					close(entered)
					<-ctx.Done()
				}
			})
			defer reset()
			submit(t, r.n.h, r.thread, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointRun, ID: to}, Files: true, Conversation: true})
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatalf("the revert never reached %s", step)
			}
			r.n.kill()
			reset()
			if text := read(t, target); text != "the target's\n" {
				t.Fatalf("%s %q when the revert was cut off; want the target's", target, text)
			}
			n := serveCheckpoints(t, path, dir, newRewinder(provider.Rewind{}, errors.New("fork refused after restart"), false))
			got := untilReverted(t, n.h, r.thread, 1)
			if got.Status != api.RevertFailed || !strings.Contains(got.Error, "fork refused after restart") {
				t.Fatalf("reverted %+v; want it failed by the refused rewind", got)
			}
			if text := read(t, before); text != want {
				t.Fatalf("%s %q after the refused replay; want %q", before, text, want)
			}
			assertReplay(t, n.st)
		})
	}
}

// A replay that rewinds and is cut off again keeps the plan the first
// attempt left, though every path it names is the target's by then: a
// later replay whose rewind is refused still puts them back.
func TestASecondCutOffReplayKeepsTheFirstAttemptsPlan(t *testing.T) {
	path, dir := dbPath(t), tempDir(t)
	r := twoTurnsWith(t, path, dir, repo(t), newRewinder(provider.Rewind{}, nil, false))
	entered := make(chan struct{}, 2)
	reset := runtime.SetCheckpointHook(func(ctx context.Context, step string) {
		if step == "applied" {
			entered <- struct{}{}
			<-ctx.Done()
		}
	})
	defer reset()
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Files: true, Conversation: true})
	n := r.n
	for i := range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("attempt %d never put the files back", i+1)
		}
		n.kill()
		if i == 0 {
			n = serveCheckpoints(t, path, dir, newRewinder(provider.Rewind{}, nil, false))
		}
	}
	reset()
	if text := read(t, filepath.Join(r.ws, "README")); text == "three\n" {
		t.Fatal("the cut-off replay left README as it was")
	}
	n = serveCheckpoints(t, path, dir, newRewinder(provider.Rewind{}, errors.New("fork refused after restart"), false))
	got := untilReverted(t, n.h, r.thread, 1)
	if got.Status != api.RevertFailed || !strings.Contains(got.Error, "put back as they were") {
		t.Fatalf("reverted %+v; want it failed, the files put back", got)
	}
	if text := read(t, filepath.Join(r.ws, "README")); text != "three\n" || !exists(filepath.Join(r.ws, "new.txt")) {
		t.Fatalf("README %q, new.txt there %v; want the files as they were before the revert", text, exists(filepath.Join(r.ws, "new.txt")))
	}
	assertReplay(t, n.st)
}

// A refused replay undoes only what the cut-off attempt wrote, so a file
// saved while the host was down stays. Where that leaves a path the plan
// names as recovery doesn't have it, the revert says so.
func TestARefusedReplayKeepsWhatWasSavedSince(t *testing.T) {
	for _, c := range []struct {
		name, step, draft, left string
		target, saved           []string
	}{
		{name: "into the folder the plan makes a file", step: "planned", draft: "slot/deep/editor-draft.tmp",
			target: []string{"slot"}, saved: []string{"slot/notes.txt", "slot/deep/note.txt"}},
		{name: "into a folder where the attempt deleted a file", step: "applying", draft: "slot/draft.txt", left: "slot",
			target: []string{"slot/notes.txt"}, saved: []string{"slot"}},
		{name: "over a file the attempt never wrote", step: "planned", draft: "notes.txt", left: "notes.txt",
			target: []string{"notes.txt"}, saved: []string{"notes.txt"}},
		{name: "into a folder the attempt made", step: "applied", draft: "slot/draft.txt", left: "slot",
			target: []string{"slot/notes.txt"}, saved: []string{"slot"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path, dir := dbPath(t), tempDir(t)
			r := twoTurnsWith(t, path, dir, repo(t), newRewinder(provider.Rewind{}, nil, false))
			slot := filepath.Join(r.ws, "slot")
			in := func(rel string) string { return filepath.Join(r.ws, rel) }
			for _, f := range c.target {
				write(t, in(f), "the target's\n")
			}
			submit(t, r.n.h, r.thread, dispatch("target", api.StartNow))
			untilCheckpoints(t, r.n.h, r.thread, 4)
			until(t, r.n.h, r.thread, "the target turn", idle)
			to := runsOf(t, r.n.h, r.thread)[2].Run
			if err := os.RemoveAll(slot); err != nil {
				t.Fatal(err)
			}
			for _, f := range c.saved {
				write(t, in(f), "before the revert\n")
			}
			submit(t, r.n.h, r.thread, dispatch("after the target", api.StartNow))
			untilCheckpoints(t, r.n.h, r.thread, 5)
			until(t, r.n.h, r.thread, "the later turn", idle)
			entered := make(chan struct{})
			reset := runtime.SetCheckpointHook(func(ctx context.Context, s string) {
				if s == c.step {
					close(entered)
					<-ctx.Done()
				}
			})
			defer reset()
			submit(t, r.n.h, r.thread, api.ThreadRevert{To: api.CheckpointRef{Phase: api.CheckpointRun, ID: to}, Files: true, Conversation: true})
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatalf("the revert never reached %s", c.step)
			}
			r.n.kill()
			reset()
			write(t, in(c.draft), "saved while the host was down\n")
			n := serveCheckpoints(t, path, dir, newRewinder(provider.Rewind{}, errors.New("fork refused after restart"), false))
			got := untilReverted(t, n.h, r.thread, 1)
			if got.Status != api.RevertFailed || !strings.Contains(got.Error, "fork refused after restart") {
				t.Fatalf("reverted %+v; want it failed by the refused rewind", got)
			}
			if text := read(t, in(c.draft)); text != "saved while the host was down\n" {
				t.Fatalf("%s %q after the refused replay; want the file saved since", c.draft, text)
			}
			if c.left != "" {
				if !strings.Contains(got.Error, c.left+" changed since the revert began") {
					t.Fatalf("reverted %+v; want it to say %s was left as it is", got, c.left)
				}
			} else {
				if strings.Contains(got.Error, "changed since") {
					t.Fatalf("reverted %+v; want every path put back", got)
				}
				for _, f := range c.saved {
					if text := read(t, in(f)); text != "before the revert\n" {
						t.Fatalf("%s %q after the refused replay; want it as before the revert", f, text)
					}
				}
			}
			assertReplay(t, n.st)
		})
	}
}

// Two rewinds admitted against one conversation: the second's turn, which
// the first took out by the time the second runs, refuses it, and the
// first's rewind stays the one the next run takes up.
func TestARewindToATurnAnEarlierOneTookOutFails(t *testing.T) {
	ad := newRewinder(provider.Rewind{}, nil, false)
	r := twoTurnsWith(t, dbPath(t), tempDir(t), repo(t), ad)
	submit(t, r.n.h, r.thread, dispatch("three", api.StartNow))
	until(t, r.n.h, r.thread, "the third turn", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
	entered, release := make(chan struct{}), make(chan struct{})
	var first, released sync.Once
	defer released.Do(func() { close(release) })
	defer runtime.SetCheckpointHook(func(ctx context.Context, step string) {
		if step == "rewound" {
			first.Do(func() {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
		}
	})()
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true})
	<-entered
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.run), Conversation: true})
	released.Do(func() { close(release) })
	one, two := untilReverted(t, r.n.h, r.thread, 1), untilReverted(t, r.n.h, r.thread, 2)
	if !slices.Contains(one.Dropped, r.run.Run) {
		t.Fatalf("the first rewind %+v didn't drop the second's turn", one)
	}
	if two.Conversation != api.RewindFailed || !strings.Contains(two.Error, "A rewind took this turn out of the conversation") {
		t.Fatalf("the second rewind %+v; want it refused, its turn taken out", two)
	}
	if pending, err := r.n.st.Rewound(ctx, r.thread); err != nil || pending == nil || pending.To.ID != r.first.Run {
		t.Fatalf("the pending rewind is %+v, %v; want the first's", pending, err)
	}
	if specs, _, _ := ad.seen(); len(specs) != 1 {
		t.Fatalf("the adapter rewound %d times; want the first rewind's alone", len(specs))
	}
	assertReplay(t, r.n.st)
}

// A run whose message went out holds the rewind's kept turns, whatever a
// stop does after: it binds the rewind, and the run after it is sent its
// own message alone.
func TestAStopAfterTheSendKeepsTheHandoffTaken(t *testing.T) {
	ad := newRewinder(provider.Rewind{}, nil, false)
	r := twoTurnsWith(t, dbPath(t), tempDir(t), repo(t), ad)
	submit(t, r.n.h, r.thread, api.ThreadRevert{To: runCheckpoint(r.first), Conversation: true})
	untilReverted(t, r.n.h, r.thread, 1)
	held, release := make(chan struct{}), make(chan struct{})
	ad.mu.Lock()
	ad.held, ad.release = held, release
	ad.mu.Unlock()
	submit(t, r.n.h, r.thread, dispatch("three", api.StartNow))
	<-held
	submit(t, r.n.h, r.thread, api.TurnInterrupt{})
	close(release)
	until(t, r.n.h, r.thread, "the stopped run's end", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 3 })
	if bs := boundOf(t, r.n.h, r.thread); len(bs) != 1 || bs[0].Rewound != api.RewindPortable {
		t.Fatalf("session.bound %+v; want the run that sent the kept turns to bind the rewind", bs)
	}
	submit(t, r.n.h, r.thread, dispatch("four", api.StartNow))
	until(t, r.n.h, r.thread, "the run after it", func(th api.Thread) bool { return idle(th) && len(runsOf(t, r.n.h, r.thread)) == 4 })
	if _, _, sent := ad.seen(); sent[len(sent)-1] != "four" {
		t.Fatalf("the run after was sent %q; want its message alone", sent[len(sent)-1])
	}
	assertReplay(t, r.n.st)
}
