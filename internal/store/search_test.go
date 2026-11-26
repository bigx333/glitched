package store_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/store"
)

// searchLog is a thread on a store that a test writes to, a second apart.
type searchLog struct {
	t      *testing.T
	s      *store.Store
	thread api.ID
	at     time.Time
}

func newSearchLog(t *testing.T, s *store.Store) *searchLog {
	l := &searchLog{t: t, s: s, thread: api.NewID(), at: time.Now()}
	// A Claude thread, which compacts once idle.
	l.submit(api.NewID(), api.ThreadCreate{Workspace: "w", Title: "t", Model: api.ModelRef{Provider: "claude", Model: "claude-opus-4-6"}})
	return l
}

func (l *searchLog) submit(id api.ID, body api.Intent) {
	l.t.Helper()
	l.at = l.at.Add(time.Second)
	if _, err := l.s.Submit(context.Background(), api.Command{ID: id, Target: l.thread, Body: body}, l.at); err != nil {
		l.t.Fatal(err)
	}
}

func (l *searchLog) send(text string, mode api.DispatchMode) api.ID {
	l.t.Helper()
	id := api.NewID()
	l.submit(id, api.MessageDispatch{Text: text, Mode: mode})
	return id
}

func (l *searchLog) observe(o engine.Observation) {
	l.t.Helper()
	l.observeAt(o, l.at.Add(time.Second))
}

func (l *searchLog) observeAt(o engine.Observation, at time.Time) {
	l.t.Helper()
	l.at = at
	if _, err := l.s.Observe(context.Background(), l.thread, o, at, 0); err != nil {
		l.t.Fatal(err)
	}
}

// seqs are the seqs of the thread's events of type typ that match.
func (l *searchLog) seqs(typ string, match func(api.EventBody) bool) []uint64 {
	l.t.Helper()
	events, _, err := l.s.Since(context.Background(), l.thread, 0, 10000)
	if err != nil {
		l.t.Fatal(err)
	}
	var out []uint64
	for _, e := range events {
		if e.Type != typ {
			continue
		}
		b, err := e.Decode()
		if err != nil {
			l.t.Fatal(err)
		}
		if match(b) {
			out = append(out, e.Cursor.Seq)
		}
	}
	return out
}

// hits are the thread's rows that match q, every one of them.
func (l *searchLog) hits(q string) []api.SearchHit {
	l.t.Helper()
	res, err := l.s.Search(context.Background(), api.SearchQuery{Query: q, Thread: l.thread})
	if err != nil {
		l.t.Fatalf("Search(%q) = %v", q, err)
	}
	return res.Hits
}

// transcriptScenario writes a thread with each kind of text search must
// treat as the transcript does: a queued message edited before it ran, a
// reply split by a tool call, a silent compaction, a child's report wake
// and a run still going. check checks what search finds of it.
type transcriptScenario struct {
	l        *searchLog
	edited   api.ID
	inFlight api.ID
}

func writeTranscript(l *searchLog) transcriptScenario {
	l.t.Helper()
	first := l.send("first question", api.StartNow)
	l.observe(engine.Started{Run: first})
	edited := l.send("draft alphaword", api.AfterActive)
	l.submit(api.NewID(), api.QueueEdit{Message: edited, Text: "final betaword"})
	l.observe(engine.Delta{Run: first, Text: "Starting now"})
	l.observe(engine.Work{Item: api.WorkItem{Run: first, Item: "i1", Kind: "command", Title: "go test", Status: api.WorkStarted}})
	l.observe(engine.Work{Item: api.WorkItem{Run: first, Item: "i1", Kind: "command", Title: "go test", Status: api.WorkCompleted}})
	l.observe(engine.Delta{Run: first, Text: "Validation succeeded"})
	l.observe(engine.Ended{Run: first, Outcome: api.OutcomeCompleted})

	// The edited message runs next, and an idle compaction after it.
	l.observe(engine.Started{Run: edited})
	l.observe(engine.Delta{Run: edited, Text: "second reply"})
	l.observe(engine.Context{Run: edited, Used: 150000, Window: 200000, CacheExpires: l.at.Add(time.Hour)})
	l.observe(engine.Ended{Run: edited, Outcome: api.OutcomeCompleted})
	compact := api.NewID()
	l.observeAt(engine.IdleCompact{AfterRun: edited, Run: compact}, l.at.Add(54*time.Minute))
	l.observe(engine.Started{Run: compact})
	l.observe(engine.Delta{Run: compact, Text: "compactsummaryword"})
	l.observe(engine.Ended{Run: compact, Outcome: api.OutcomeCompleted})

	inFlight := l.send("third prompt", api.StartNow)
	l.observe(engine.Started{Run: inFlight})
	l.observe(engine.Delta{Run: inFlight, Text: "inflightword"})
	l.observe(engine.Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: api.ReportDone, Title: "kid"}, Reply: "wakereplyword"})
	return transcriptScenario{l: l, edited: edited, inFlight: inFlight}
}

func (sc transcriptScenario) check() {
	l := sc.l
	l.t.Helper()
	queued := l.seqs("message.queued", func(b api.EventBody) bool { return b.(api.MessageQueued).Message == sc.edited })
	if hits := l.hits("betaword"); len(hits) != 1 || hits[0].Role != "user" || hits[0].Seq != queued[0] || !strings.Contains(hits[0].Snippet, "betaword") {
		l.t.Errorf("the edited message's new words: %+v; want one user hit at its queued seq %d", hits, queued[0])
	}
	if hits := l.hits("alphaword"); len(hits) != 0 {
		l.t.Errorf("the edited message's old words still match: %+v", hits)
	}

	deltas := l.seqs("assistant.delta", func(b api.EventBody) bool {
		return strings.Contains(b.(api.AssistantDelta).Text, "Starting") || strings.Contains(b.(api.AssistantDelta).Text, "Validation")
	})
	if hits := l.hits("starting"); len(hits) != 1 || hits[0].Seq != deltas[0] {
		l.t.Errorf("the reply before the tool call: %+v; want one hit at seq %d", hits, deltas[0])
	}
	if hits := l.hits("validation"); len(hits) != 1 || hits[0].Seq != deltas[1] {
		l.t.Errorf("the reply after the tool call: %+v; want one hit at seq %d", hits, deltas[1])
	}

	if silent := l.seqs("turn.admitted", func(b api.EventBody) bool { return b.(api.TurnAdmitted).Silent }); len(silent) != 1 {
		l.t.Fatalf("the scenario ran %d silent turns, want 1", len(silent))
	}
	if hits := l.hits("compactsummaryword"); len(hits) != 0 {
		l.t.Errorf("a silent compaction's reply matches: %+v", hits)
	}
	if wakes := l.seqs("message.queued", func(b api.EventBody) bool { return !b.(api.MessageQueued).Report.IsZero() }); len(wakes) != 1 {
		l.t.Fatalf("the scenario queued %d report wakes, want 1", len(wakes))
	}
	if hits := l.hits("wakereplyword"); len(hits) != 0 {
		l.t.Errorf("a child's report wake matches: %+v", hits)
	}
	if hits := l.hits("inflightword"); len(hits) != 0 {
		l.t.Errorf("a run still going matches: %+v", hits)
	}

	l.observe(engine.Ended{Run: sc.inFlight, Outcome: api.OutcomeCompleted})
	if hits := l.hits("inflightword"); len(hits) != 1 {
		l.t.Errorf("the run once it ended: %+v; want one hit", hits)
	}
}

// Search finds what the transcript shows as it is written.
func TestSearchIndexesWhatTheTranscriptShows(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	writeTranscript(newSearchLog(t, s)).check()
}

// A database from before search finds what the transcript shows, as one
// written since does, and a run going as it upgraded matches once it ends.
func TestAVersion21DatabaseIndexesWhatTheTranscriptShows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	sc := writeTranscript(newSearchLog(t, s))
	s.Close()
	downgrade(t, path, "", 21)
	sc.l.s = open(t, path)
	sc.check()
}

// One thread with many matches leaves room for the others.
func TestSearchListsEachMatchingThreadWithinItsLimit(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	chatty := newSearchLog(t, s)
	for range 12 {
		chatty.send("needle", api.AfterActive)
	}
	quiet := newSearchLog(t, s)
	quiet.send("needle among a great many other words that make this message long and its match a weak one by far", api.AfterActive)

	res, err := s.Search(ctx, api.SearchQuery{Query: "needle", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 2 || res.Hits[0].Thread != chatty.thread || res.Hits[1].Thread != quiet.thread {
		t.Fatalf("Search(needle, limit 2) = %+v; want the chatty thread, then the quiet one", res.Hits)
	}
}

// A query holding a NUL byte searches the words around it.
func TestSearchTakesANulByte(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	l := newSearchLog(t, s)
	l.send("alpha beta", api.AfterActive)
	if hits := l.hits("alpha\x00beta"); len(hits) != 1 {
		t.Fatalf("Search(alpha NUL beta) = %+v; want one hit", hits)
	}
}

func TestVersion21UpgradesWithFTS5AndBackfills(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)

	th := api.NewID()
	if _, err := s.Submit(ctx, create(th), time.Now()); err != nil {
		t.Fatal(err)
	}
	r1 := api.NewID()
	if _, err := s.Submit(ctx, api.Command{ID: r1, Target: th, Body: api.MessageDispatch{Text: "legacy deploy pipeline check", Mode: api.StartNow}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, th, engine.Started{Run: r1}, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, th, engine.Delta{Run: r1, Text: "verified telemetry "}, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, th, engine.Delta{Run: r1, Text: "collector config"}, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, th, engine.Ended{Run: r1, Outcome: api.OutcomeCompleted}, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Downgrade to schema 21 (dropping transcript_fts) and reopen to trigger v21 -> v22 migration.
	downgrade(t, path, "", 21)
	s = open(t, path)
	defer s.Close()

	for _, q := range []string{"deploy pipeline", "telemetry collector"} {
		res, err := s.Search(ctx, api.SearchQuery{Query: q})
		if err != nil {
			t.Fatalf("Search(%q) = %v", q, err)
		}
		if len(res.Hits) != 1 || res.Hits[0].Thread != th {
			t.Fatalf("Search(%q) = %+v, want hit for thread %s", q, res.Hits, th)
		}
	}
}

func TestSearchFTS5LiveIndexAndDelete(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "host.db"))

	t1 := api.NewID()
	if _, err := s.Submit(ctx, create(t1), time.Now()); err != nil {
		t.Fatal(err)
	}
	r1 := api.NewID()
	if _, err := s.Submit(ctx, api.Command{ID: r1, Target: t1, Body: api.MessageDispatch{Text: "configure websocket auth rate-limit", Mode: api.StartNow}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, t1, engine.Started{Run: r1}, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, t1, engine.Delta{Run: r1, Text: "Added handshakeLimiter with exponential backoff."}, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, t1, engine.Ended{Run: r1, Outcome: api.OutcomeCompleted}, time.Now(), 0); err != nil {
		t.Fatal(err)
	}

	t2 := api.NewID()
	if _, err := s.Submit(ctx, create(t2), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: t2, Body: api.MessageDispatch{Text: "another rate-limiter note", Mode: api.StartNow}}, time.Now()); err != nil {
		t.Fatal(err)
	}

	// Prefix + punctuation-safe search should match both threads cleanly.
	res, err := s.Search(ctx, api.SearchQuery{Query: "rate-lim"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("Search(\"rate-lim\") returned %d hits: %+v", len(res.Hits), res.Hits)
	}

	// Thread-scoped search returns matching entries in that thread only.
	resOne, err := s.Search(ctx, api.SearchQuery{Query: "exponential", Thread: t1})
	if err != nil {
		t.Fatal(err)
	}
	if len(resOne.Hits) != 1 || resOne.Hits[0].Role != "assistant" {
		t.Fatalf("thread-scoped search = %+v", resOne.Hits)
	}

	// Deleting t1 removes its rows from transcript_fts.
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: t1, Body: api.ThreadDelete{}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	resAfterDelete, err := s.Search(ctx, api.SearchQuery{Query: "handshakeLimiter"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resAfterDelete.Hits) != 0 {
		t.Fatalf("deleted thread still searchable: %+v", resAfterDelete.Hits)
	}
}
