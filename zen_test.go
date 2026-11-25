package main

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
)

// shape is what a view lists: each row's key, or a fold as the keys of
// the rows it folds.
func shape(es []entry, v timelineView) []string {
	var s []string
	for j := range v.len() {
		if f := v.fold(j); f != nil {
			var ks []string
			for _, i := range f.rows {
				ks = append(ks, es[i].key)
			}
			s = append(s, "fold("+strings.Join(ks, " ")+")")
			continue
		}
		s = append(s, es[v.row(j)].key)
	}
	return s
}

func zenRow(key string, kind entryKind, run api.ID) entry {
	return entry{key: key, kind: kind, run: run}
}

// zenClock is when the next zenWork starts: each works a second, one
// after another.
var zenClock atomic.Int64

func zenWork(key string, run api.ID, steps ...api.WorkStatus) entry {
	at := time.Unix(zenClock.Add(2), 0)
	e := entry{key: key, kind: entryWork, run: run, started: at, ended: at.Add(time.Second)}
	for _, s := range steps {
		e.work = append(e.work, client.WorkStep{Status: s})
	}
	return e
}

func zenReply(key string, run api.ID) entry {
	return entry{key: key, kind: entryAssistant, run: run, turn: run}
}

// folds is the view's folds, in order.
func folds(v timelineView) []*zenFold {
	var fs []*zenFold
	for j := range v.len() {
		if f := v.fold(j); f != nil {
			fs = append(fs, f)
		}
	}
	return fs
}

func wantShape(t *testing.T, es []entry, v timelineView, want ...string) {
	t.Helper()
	if got := shape(es, v); !slices.Equal(got, want) {
		t.Fatalf("view is %q, want %q", got, want)
	}
}

// finishedTurn is a turn with work, a reply in between, a plan and the
// final reply.
func finishedTurn() []entry {
	r := api.NewID()
	return []entry{
		zenRow("u", entryUser, r),
		zenWork("w1", r, api.WorkCompleted, api.WorkCompleted),
		zenRow("a1", entryAssistant, r),
		zenWork("w2", r, api.WorkCompleted),
		zenRow("p", entryPlan, r),
		zenWork("w3", r, api.WorkCompleted),
		zenReply("a2", r),
	}
}

func TestZenOffListsEveryRowAsItIs(t *testing.T) {
	es := finishedTurn()
	a := &app{}
	v := a.viewOf(api.NewID(), es)
	if v.len() != len(es) {
		t.Fatalf("len %d, want %d", v.len(), len(es))
	}
	for i := range es {
		if v.key(i) != any(es[i].key) || v.row(i) != i || v.fold(i) != nil || v.pos(i) != i || v.anchor(i) != i || v.thinkingPos(i) != i {
			t.Fatalf("item %d: key %v row %d pos %d anchor %d thinking %d", i, v.key(i), v.row(i), v.pos(i), v.anchor(i), v.thinkingPos(i))
		}
	}
	if v.thinkingPos(-1) != -1 || v.pos(len(es)) != -1 || v.pos(-1) != -1 {
		t.Fatal("out of range positions aren't -1")
	}
}

func TestZenOnAnEmptyThreadListsNothing(t *testing.T) {
	v := zenView(nil, nil)
	if v.len() != 0 || v.thinkingPos(-1) != -1 || v.pos(0) != -1 {
		t.Fatalf("an empty view has %d items", v.len())
	}
}

func TestZenFoldsATurnsMiddleIntoOneLine(t *testing.T) {
	es := finishedTurn()
	v := zenView(es, nil)
	wantShape(t, es, v, "u", "fold(w1 a1 w2 p w3)", "a2")
	f := v.fold(1)
	if f.steps != 4 || f.failed != 0 || f.worked != 3*time.Second || f.working {
		t.Fatalf("fold %+v", f)
	}
	if v.key(1) != any(f.key) || f.key != "z/"+es[0].run.String()+"/u" {
		t.Fatalf("fold key %v", v.key(1))
	}
	open := zenView(es, map[string]bool{f.key: true})
	wantShape(t, es, open, "u", "fold(w1 a1 w2 p w3)", "w1", "a1", "w2", "p", "w3", "a2")
}

func TestZenFoldsWorkAfterTheFinalReplyToo(t *testing.T) {
	r := api.NewID()
	es := []entry{zenRow("u", entryUser, r), zenWork("w1", r), zenReply("a", r), zenWork("w2", r)}
	wantShape(t, es, zenView(es, nil), "u", "fold(w1 w2)", "a")
}

func TestZenFoldsASilentRun(t *testing.T) {
	r := api.NewID()
	es := []entry{zenRow("u", entryUser, r), zenWork("w1", r)}
	wantShape(t, es, zenView(es, nil), "u", "fold(w1)")
}

func TestZenKeepsTheReplyBeingWritten(t *testing.T) {
	r := api.NewID()
	es := []entry{zenRow("u", entryUser, r), zenWork("w1", r), zenRow("a1", entryAssistant, r)}
	es[1].working, es[1].started, es[1].ended = true, time.Time{}, time.Time{}
	es[2].streaming = true
	v := zenView(es, nil)
	wantShape(t, es, v, "u", "fold(w1)", "a1")
	if !v.fold(1).working {
		t.Fatal("the fold of running work isn't working")
	}
	es = append(es, zenWork("w2", r))
	wantShape(t, es, zenView(es, nil), "u", "fold(w1 a1 w2)")
}

func TestZenKeepsTheReplyThatAsksAndItsRequest(t *testing.T) {
	r := api.NewID()
	es := []entry{zenRow("u", entryUser, r), zenRow("a1", entryAssistant, r), zenWork("w1", r), zenRow("a2", entryAssistant, r), zenAsk("q", r, api.KindApproval)}
	v := zenView(es, nil)
	wantShape(t, es, v, "u", "fold(a1 w1)", "a2", "q")
	if v.fold(1).paused != "Paused for approval" {
		t.Fatalf("paused %q", v.fold(1).paused)
	}
}

func TestZenKeepsThePauseAsMoreWorkFollowsTheRequest(t *testing.T) {
	r := api.NewID()
	es := []entry{zenRow("u", entryUser, r), zenWork("w1", r), zenAsk("q", r, api.KindApproval), zenWork("w2", r)}
	v := zenView(es, nil)
	wantShape(t, es, v, "u", "fold(w1 w2)", "q")
	if got := v.fold(1).paused; got != "Paused for approval" {
		t.Fatalf("paused %q, want %q", got, "Paused for approval")
	}
}

func TestZenPutsThinkingPastTheRowsNowhere(t *testing.T) {
	es := finishedTurn()
	for _, v := range []timelineView{zenView(es, nil), zenView(nil, nil)} {
		if got := v.thinkingPos(len(v.es)); got != -1 {
			t.Fatalf("thinkingPos(%d) = %d, want -1", len(v.es), got)
		}
		if got := v.thinkingPos(len(v.es) + 5); got != -1 {
			t.Fatalf("thinkingPos(%d) = %d, want -1", len(v.es)+5, got)
		}
	}
}

func TestZenFoldsNothingOfARunWithoutWork(t *testing.T) {
	r := api.NewID()
	es := []entry{zenRow("u", entryUser, r), zenRow("a1", entryAssistant, r), {key: "q", kind: entryRequest}, zenReply("a2", r)}
	wantShape(t, es, zenView(es, nil), "u", "a1", "q", "a2")
}

func TestZenKeepsWhatMustShowInPlace(t *testing.T) {
	r := api.NewID()
	es := []entry{
		zenRow("u", entryUser, r), zenWork("w1", r), {key: "f", kind: entryFailed}, zenWork("w2", r), {key: "e", kind: entryError},
		{key: "rep", kind: entryReport}, {key: "c", kind: entryCompact}, zenRow("a1", entryAssistant, r), {key: "fk", kind: entryFork}, zenReply("a2", r),
	}
	v := zenView(es, nil)
	wantShape(t, es, v, "u", "fold(w1 w2 a1)", "f", "e", "rep", "c", "fk", "a2")
	open := zenView(es, map[string]bool{v.fold(1).key: true})
	wantShape(t, es, open, "u", "fold(w1 w2 a1)", "w1", "f", "w2", "e", "rep", "c", "a1", "fk", "a2")
}

func TestZenFoldsEachSteerOnItsOwn(t *testing.T) {
	r := api.NewID()
	es := []entry{zenRow("u", entryUser, r), zenWork("w1", r), zenRow("a1", entryAssistant, r), zenRow("u2", entryUser, r), zenWork("w2", r), zenReply("a2", r)}
	v := zenView(es, nil)
	wantShape(t, es, v, "u", "fold(w1)", "a1", "u2", "fold(w2)", "a2")
	if fs := folds(v); fs[0].key == fs[1].key {
		t.Fatalf("both folds are %q", fs[0].key)
	}
}

func TestZenFoldsRowsBeforeTheFirstMessage(t *testing.T) {
	r := api.NewID()
	es := []entry{zenWork("w0", r), zenRow("a0", entryAssistant, r), zenRow("u", entryUser, r), zenWork("w1", r)}
	v := zenView(es, nil)
	wantShape(t, es, v, "fold(w0)", "a0", "u", "fold(w1)")
	if f := v.fold(0); f.key != "z/"+r.String()+"/w0" {
		t.Fatalf("key %q", f.key)
	}
}

func TestZenFoldsEachRunOfAMessage(t *testing.T) {
	r1, r2 := api.NewID(), api.NewID()
	es := []entry{zenRow("u", entryUser, r1), zenWork("w1", r1), zenReply("a1", r1), {key: "rep", kind: entryReport}, zenWork("w2", r2), zenReply("a2", r2)}
	v := zenView(es, nil)
	wantShape(t, es, v, "u", "fold(w1)", "a1", "rep", "fold(w2)", "a2")
	if fs := folds(v); !strings.HasPrefix(fs[0].key, "z/"+r1.String()+"/") || !strings.HasPrefix(fs[1].key, "z/"+r2.String()+"/") {
		t.Fatalf("folds %+v %+v", fs[0], fs[1])
	}
}

func TestZenKeepsAFoldsKeyAsTheTurnEnds(t *testing.T) {
	r := api.NewID()
	es := []entry{zenRow("u", entryUser, r), zenRow("a1", entryAssistant, r), zenWork("w", r), zenRow("a2", entryAssistant, r)}
	before := folds(zenView(es, nil))
	es[3].turn = r
	after := folds(zenView(es, nil))
	if len(before) != 1 || len(after) != 1 || before[0].key != after[0].key {
		t.Fatalf("folds %v, then %v", before, after)
	}
}

func TestZenCountsFailedSteps(t *testing.T) {
	r := api.NewID()
	es := []entry{zenRow("u", entryUser, r), zenWork("w1", r, api.WorkCompleted, api.WorkFailed), zenWork("w2", r, api.WorkFailed), zenReply("a", r)}
	f := zenView(es, nil).fold(1)
	if f.steps != 3 || f.failed != 2 {
		t.Fatalf("steps %d failed %d", f.steps, f.failed)
	}
}

func TestZenPutsAFoldedRowAtItsLine(t *testing.T) {
	es := finishedTurn()
	v := zenView(es, nil)
	for i, want := range []int{0, 1, 1, 1, 1, 1, 2} {
		if got := v.pos(i); got != want {
			t.Fatalf("pos(%d) = %d, want %d", i, got, want)
		}
	}
	if v.anchor(1) != 1 || v.anchor(2) != 6 {
		t.Fatalf("anchors %d %d", v.anchor(1), v.anchor(2))
	}
	for at, want := range []int{0, 1, 1, 1, 1, 1, 2} {
		if got := v.thinkingPos(at); got != want {
			t.Fatalf("thinkingPos(%d) = %d, want %d", at, got, want)
		}
	}
	open := zenView(es, map[string]bool{v.fold(1).key: true})
	for i := range es {
		if j := open.pos(i); open.row(j) != i {
			t.Fatalf("open pos(%d) = %d, row %d", i, j, open.row(j))
		}
	}
	// A silent run's last row is folded; thinking goes under its line.
	es = es[:6]
	v = zenView(es, nil)
	if got := v.thinkingPos(5); got != 1 {
		t.Fatalf("thinkingPos(5) = %d, want 1", got)
	}
}

// mustShow are the kinds of row Zen never folds: messages, approvals and
// questions, failures, errors, reports and the dividers.
var mustShow = []entryKind{entryUser, entryRequest, entryFailed, entryError, entryReport, entryCompact, entryFork}

// TestZenNeverHidesWhatMustShow builds threads at random and checks that
// whatever is folded or open, every row that must show shows once and in
// order, every final reply shows, each row's position is the item showing
// it, and a folded row's is the line of the fold holding it.
func TestZenNeverHidesWhatMustShow(t *testing.T) {
	kinds := []entryKind{entryUser, entryAssistant, entryWork, entryRequest, entryFailed, entryReport, entryError, entryCompact, entryPlan, entryFork}
	r := rand.New(rand.NewPCG(287, 1))
	for n := range 2000 {
		runs := make([]api.ID, 1+r.IntN(3))
		for k := range runs {
			runs[k] = api.NewID()
		}
		es := make([]entry, r.IntN(41))
		for i := range es {
			e := entry{key: fmt.Sprint("k", i), kind: kinds[r.IntN(len(kinds))], run: runs[r.IntN(len(runs))]}
			switch e.kind {
			case entryAssistant:
				if r.IntN(3) == 0 {
					e.turn = e.run
				}
			case entryWork:
				e.work = make([]client.WorkStep, r.IntN(3))
			case entryRequest:
				if r.IntN(2) == 0 {
					e.request = &client.RequestEntry{Run: e.run, Kind: api.KindApproval, State: api.RequestState(r.IntN(2))}
				}
			}
			es[i] = e
		}
		open := map[string]bool{}
		all := map[string]bool{}
		for _, f := range folds(zenView(es, nil)) {
			all[f.key] = true
			if r.IntN(2) == 0 {
				open[f.key] = true
			}
		}
		check := func(v timelineView, all bool) {
			seen := map[int]int{}
			last, lastAnchor := -1, -1
			for j := range v.len() {
				if a := v.anchor(j); a < lastAnchor {
					t.Fatalf("case %d: anchor %d after %d", n, a, lastAnchor)
				} else {
					lastAnchor = a
				}
				i := v.row(j)
				if i < 0 {
					continue
				}
				seen[i]++
				if slices.Contains(mustShow, es[i].kind) {
					if i <= last {
						t.Fatalf("case %d: row %d after %d", n, i, last)
					}
					last = i
				}
			}
			for i, e := range es {
				switch {
				case seen[i] > 1:
					t.Fatalf("case %d: row %d shows %d times", n, i, seen[i])
				case seen[i] == 0 && (slices.Contains(mustShow, e.kind) || e.kind == entryAssistant && !e.turn.IsZero() || all):
					t.Fatalf("case %d: row %d (kind %d) doesn't show: %q", n, i, e.kind, shape(es, v))
				}
				j := v.pos(i)
				if j < 0 || j >= v.len() {
					t.Fatalf("case %d: pos(%d) = %d of %d items", n, i, j, v.len())
				}
				if seen[i] == 1 && v.row(j) != i {
					t.Fatalf("case %d: pos(%d) = %d, which shows row %d", n, i, j, v.row(j))
				}
				if f := v.fold(j); seen[i] == 0 && (f == nil || !slices.Contains(f.rows, i)) {
					t.Fatalf("case %d: row %d is folded, but pos(%d) = %d isn't a fold of it", n, i, i, j)
				}
			}
		}
		check(zenView(es, open), false)
		check(zenView(es, all), true)
		id := (&app{}).viewOf(api.NewID(), es)
		if id.len() != len(es) {
			t.Fatalf("case %d: identity has %d items", n, id.len())
		}
		for i := range es {
			if id.row(i) != i || id.pos(i) != i || id.key(i) != any(es[i].key) {
				t.Fatalf("case %d: identity item %d", n, i)
			}
		}
	}
}

// The timeline's Zen view is built again only as the rows, or the lines
// open, change; with Zen off it's each row as it is, for nothing.
func TestZenViewIsBuiltAgainOnlyAsWhatItShowsChanges(t *testing.T) {
	a := &app{listed: map[api.ID]*timelineMemo{}}
	a.settings.zen = true
	id := api.NewID()
	rs := rows{es: finishedTurn(), change: 1}
	v := a.viewFor(id, rs)
	wantShape(t, rs.es, v, "u", "fold(w1 a1 w2 p w3)", "a2")
	if again := a.viewFor(id, rs); !sameItems(v, again) {
		t.Fatal("the same rows built the view again")
	}
	if n := testing.AllocsPerRun(100, func() { a.viewFor(id, rs) }); n != 0 {
		t.Fatalf("the view kept costs %v allocations", n)
	}
	rs.change = 2
	if again := a.viewFor(id, rs); sameItems(v, again) {
		t.Fatal("the rows changed, and the view didn't")
	}
	v = a.viewFor(id, rs)
	a.memoOf(id).toggle(v.fold(1).key)
	open := a.viewFor(id, rs)
	wantShape(t, rs.es, open, "u", "fold(w1 a1 w2 p w3)", "w1", "a1", "w2", "p", "w3", "a2")
	rs.es = append(rs.es, zenRow("u2", entryUser, api.NewID()))
	if got := a.viewFor(id, rs); got.len() != open.len()+1 {
		t.Fatalf("a row more lists %d items, want %d", got.len(), open.len()+1)
	}
	a.settings.zen = false
	if v := a.viewFor(id, rs); v.items != nil || v.len() != len(rs.es) {
		t.Fatalf("with Zen off the view lists %d items", v.len())
	}
	if n := testing.AllocsPerRun(100, func() { a.viewFor(id, rs) }); n != 0 {
		t.Fatalf("with Zen off the view costs %v allocations", n)
	}
}

// zenRows is a long thread's rows: turns of a message, work, a reply in
// between, more work and the reply.
func zenRows(n int) []entry {
	var es []entry
	for i := 0; len(es) < n; i++ {
		r := api.NewID()
		k := fmt.Sprint(i)
		es = append(es, zenRow("u"+k, entryUser, r), zenWork("w"+k, r, api.WorkCompleted, api.WorkCompleted), zenRow("a"+k, entryAssistant, r),
			zenWork("v"+k, r, api.WorkCompleted), zenReply("f"+k, r))
	}
	return es[:n]
}

// BenchmarkZenView is the timeline's view of 5,000 rows a frame: with Zen
// off, built anew, and kept.
func BenchmarkZenView(b *testing.B) {
	rs := rows{es: zenRows(5000), change: 1}
	id := api.NewID()
	a := &app{listed: map[api.ID]*timelineMemo{}}
	b.Run("off", func(b *testing.B) {
		a.settings.zen = false
		b.ReportAllocs()
		for b.Loop() {
			a.viewFor(id, rs)
		}
	})
	b.Run("cold", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			zenView(rs.es, nil)
		}
	})
	b.Run("kept", func(b *testing.B) {
		a.settings.zen = true
		a.viewFor(id, rs)
		b.ReportAllocs()
		for b.Loop() {
			a.viewFor(id, rs)
		}
	})
}

func TestWorkEntryKeepsHowLongItWorked(t *testing.T) {
	start := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	e := workEntry("w", &client.WorkEntry{Started: start, Ended: start.Add(1500 * time.Millisecond)})
	if e.ended.Sub(e.started) != 1500*time.Millisecond || e.took != "Worked for 2s" {
		t.Fatalf("worked %v to %v, took %q", e.started, e.ended, e.took)
	}
	if e := workEntry("w", &client.WorkEntry{Started: start}); !e.started.IsZero() || !e.working {
		t.Fatalf("running work started %v", e.started)
	}
}

func zenAsk(key string, run api.ID, kind string) entry {
	return entry{key: key, kind: entryRequest, run: run, request: &client.RequestEntry{Run: run, Request: api.NewID(), Kind: kind, State: api.RequestPending}}
}

// A reply that explains what a waiting request asks shows with its card,
// though work comes between them; once answered, it folds with the work.
func TestZenKeepsTheReplyAboveAWaitingRequest(t *testing.T) {
	r := api.NewID()
	es := []entry{zenRow("u", entryUser, r), zenWork("w0", r), zenRow("a1", entryAssistant, r), zenWork("w1", r), zenAsk("q", r, api.KindApproval)}
	wantShape(t, es, zenView(es, nil), "u", "fold(w0 w1)", "a1", "q")
	es[4].request.State = api.RequestAnswered
	es = append(es, zenWork("w2", r), zenReply("a2", r))
	wantShape(t, es, zenView(es, nil), "u", "fold(w0 a1 w1 w2)", "q", "a2")
}

// A run that planned and then asks says it's paused, though no work row
// says so.
func TestZenSaysARunIsPausedFromItsRequest(t *testing.T) {
	for kind, want := range map[string]string{api.KindApproval: "Paused for approval", api.KindQuestion: "Paused for your answer"} {
		r := api.NewID()
		es := []entry{zenRow("u", entryUser, r), zenRow("p", entryPlan, r), zenAsk("q", r, kind)}
		v := zenView(es, nil)
		wantShape(t, es, v, "u", "fold(p)", "q")
		if got := v.fold(1).paused; got != want {
			t.Errorf("%s: paused %q, want %q", kind, got, want)
		}
		es[2].request.State = api.RequestAnswered
		if got := zenView(es, nil).fold(1).paused; got != "" {
			t.Errorf("%s: answered, paused %q", kind, got)
		}
	}
}

// Work that overlaps counts its time once: two calls at once in a run
// worked as long as the longer stretch they cover.
func TestZenCountsOverlappingWorkOnce(t *testing.T) {
	r := api.NewID()
	at := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	work := func(key string, from, to int) entry {
		e := workEntry(key, &client.WorkEntry{Run: r, Started: at.Add(time.Duration(from) * time.Second), Ended: at.Add(time.Duration(to) * time.Second)})
		e.key = key
		return e
	}
	es := []entry{zenRow("u", entryUser, r), work("w1", 0, 8), work("w2", 2, 10), work("w3", 12, 13), zenReply("a", r)}
	if got := zenView(es, nil).fold(1).worked; got != 11*time.Second {
		t.Fatalf("worked %v, want 11s", got)
	}
}

// An update to a long thread builds again only the folds of the turns
// that can change: with Zen on it costs not much more than with it off.
func TestZenUpdateBuildsOnlyTheLiveTurnsAgain(t *testing.T) {
	th := thread{Thread: &client.Thread{Transcript: &client.Transcript{Entries: zenTurns(500)}}}
	cost := map[bool]float64{}
	for _, zen := range []bool{false, true} {
		a := &app{rows: map[api.ID]*rowsCache{}, listed: map[api.ID]*timelineMemo{}}
		a.settings.zen = zen
		a.viewFor(th.id(), a.rowsOf(th))
		cost[zen] = testing.AllocsPerRun(50, func() {
			a.posts++
			a.viewFor(th.id(), a.rowsOf(th))
		})
	}
	t.Logf("allocations per update: %v with Zen off, %v on", cost[false], cost[true])
	if cost[true] > 2*cost[false] {
		t.Fatalf("an update costs %v allocations with Zen on, %v off", cost[true], cost[false])
	}
}

// A reply that explains two commands shows while either card waits: the
// first answered, it still explains the second.
func TestZenKeepsTheReplyWhileARequestAfterItWaits(t *testing.T) {
	r := api.NewID()
	es := []entry{zenRow("u", entryUser, r), zenWork("w0", r), zenRow("a1", entryAssistant, r), zenAsk("q1", r, api.KindApproval), zenAsk("q2", r, api.KindApproval)}
	wantShape(t, es, zenView(es, nil), "u", "fold(w0)", "a1", "q1", "q2")
	es[3].request.State = api.RequestAnswered
	es = append(es, zenWork("w1", r))
	v := zenView(es, nil)
	wantShape(t, es, v, "u", "fold(w0 w1)", "a1", "q1", "q2")
	if got := v.fold(1).paused; got != "Paused for approval" {
		t.Fatalf("paused %q", got)
	}
	es[4].request.State = api.RequestAnswered
	wantShape(t, es, zenView(es, nil), "u", "fold(w0 a1 w1)", "q1", "q2")
}

// A steer between a command starting and its approval puts them in turns
// of their own, of one run: the run's line says it's paused, and the reply
// that explains the command shows.
func TestZenPausesARunAcrossASteer(t *testing.T) {
	r := api.NewID()
	msg := "m/" + r.String()
	es := []entry{zenRow(msg, entryUser, r), zenRow("a1", entryAssistant, r), zenWork("w1", r), zenRow("steer", entryUser, r), zenAsk("q", r, api.KindApproval)}
	es[2].working = true
	v := zenView(es, nil)
	wantShape(t, es, v, msg, "a1", "fold(w1)", "steer", "q")
	if got := v.fold(2).paused; got != "Paused for approval" {
		t.Fatalf("paused %q", got)
	}
	es[4].request.State = api.RequestAnswered
	v = zenView(es, nil)
	wantShape(t, es, v, msg, "fold(a1 w1)", "steer", "q")
	if f := v.fold(1); f.paused != "" || !f.working {
		t.Fatalf("answered, paused %q, working %v", f.paused, f.working)
	}
}

// A turn's fold kept from before Zen turned off is built again if its rows
// changed while it was off: its approval answered, its command failed and
// the thread grown past it.
func TestZenBuildsAgainAFoldThatChangedWhileOff(t *testing.T) {
	run := api.NewID()
	work := &client.WorkEntry{Run: run, Items: []client.WorkStep{{Item: "c", Kind: "command", Title: "Ran it", Status: api.WorkStarted}}}
	ask := &client.RequestEntry{Run: run, Request: api.NewID(), Kind: api.KindApproval, State: api.RequestPending}
	tr := &client.Transcript{Entries: []client.Entry{&client.UserEntry{Message: run, Text: "ask", State: client.Sent}, work, ask}}
	th := thread{Thread: &client.Thread{Transcript: tr}}
	a := &app{rows: map[api.ID]*rowsCache{}, listed: map[api.ID]*timelineMemo{}}
	a.settings.zen = true
	if f := a.viewFor(th.id(), a.rowsOf(th)).fold(1); f == nil || f.paused == "" {
		t.Fatalf("the waiting turn's line is %+v", f)
	}
	a.settings.zen = false
	ask.State = api.RequestAnswered
	work.Items[0].Status = api.WorkFailed
	a.posts++
	a.viewFor(th.id(), a.rowsOf(th))
	tr.Entries = append(tr.Entries, zenTurns(2*rowsLive/5)...)
	a.posts++
	a.viewFor(th.id(), a.rowsOf(th))
	a.settings.zen = true
	rs := a.rowsOf(th)
	if rs.stable < 3 {
		t.Fatalf("the turn's rows aren't stable: %d", rs.stable)
	}
	kept, fresh := a.viewFor(th.id(), rs).fold(1), zenView(rs.es, nil).fold(1)
	if kept.paused != fresh.paused || kept.failed != fresh.failed {
		t.Fatalf("the line kept is paused %q with %d failed, built afresh %q with %d", kept.paused, kept.failed, fresh.paused, fresh.failed)
	}
}
