package client

import (
	"cmp"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	eng "glitched/internal/engine"
)

var (
	testLog = api.NewID()
	t0      = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
)

// at is when the nth event of a play happened: one second apart.
func at(n int) time.Time { return t0.Add(time.Duration(n-1) * time.Second) }

func event(seq int, b api.EventBody) api.Event {
	p, err := json.Marshal(b)
	if err != nil {
		panic(err)
	}
	return api.Event{Cursor: api.Cursor{Log: testLog, Seq: uint64(seq)}, Type: api.EventType(b), Schema: api.EventSchema, At: at(seq), Payload: p}
}

func play(bodies ...api.EventBody) *Transcript {
	t := &Transcript{}
	for i, b := range bodies {
		t.apply(event(i+1, b))
	}
	return t
}

var messageStates = map[MessageState]string{Queued: "queued", Held: "held", Claimed: "claimed", Sent: "sent", Cancelled: "cancelled", Uncertain: "uncertain"}

var requestStates = map[api.RequestState]string{
	api.RequestPending: "pending", api.RequestAnswered: "answered", api.RequestDelivered: "delivered", api.RequestInvalidated: "invalidated", api.RequestExpired: "expired",
}

var workStates = map[api.WorkStatus]string{api.WorkStarted: "started", api.WorkCompleted: "completed", api.WorkFailed: "failed"}

func describe(e Entry) string {
	switch e := e.(type) {
	case *UserEntry:
		return words("user", fmt.Sprintf("%q", e.Text), messageStates[e.State], e.Reason)
	case *ErrorEntry:
		return "error " + fmt.Sprintf("%q", e.Error)
	case *AssistantEntry:
		if e.Streaming {
			return fmt.Sprintf("assistant %q streaming", e.Text)
		}
		return fmt.Sprintf("assistant %q", e.Text)
	case *WorkEntry:
		var items []string
		for _, s := range e.Items {
			items = append(items, s.Item+":"+workStates[s.Status])
		}
		if e.Ended.IsZero() {
			items = append(items, "working")
		}
		return "work " + strings.Join(items, " ")
	case *RequestEntry:
		return words("request", e.Kind, requestStates[e.State], string(e.Answer), e.Reason)
	case *PlanEntry:
		return "plan " + planOf(e.Steps)
	case *CompactionEntry:
		state := "done"
		if e.Running {
			state = "running"
		} else if !e.Compacted {
			state = "skipped"
		}
		return fmt.Sprintf("compact %s %d->%d", state, e.PreTokens, e.PostTokens)
	}
	return fmt.Sprintf("%T", e)
}

func words(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " ")
}

// lines renders the conversation, then the queue after a "--".
func lines(t *Transcript) []string {
	var out []string
	for _, e := range t.Entries {
		out = append(out, describe(e))
	}
	if len(t.Queue) > 0 {
		out = append(out, "--")
		for _, u := range t.Queue {
			out = append(out, describe(u))
		}
	}
	return out
}

func expect(t *testing.T, tr *Transcript, want ...string) {
	t.Helper()
	if got := lines(tr); !slices.Equal(got, want) {
		t.Fatalf("transcript:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

var planMarks = map[api.PlanStatus]string{api.PlanPending: "[ ]", api.PlanActive: "[>]", api.PlanDone: "[x]"}

func planOf(steps []api.PlanStep) string {
	var out []string
	for _, s := range steps {
		out = append(out, planMarks[s.Status]+s.Text)
	}
	return strings.Join(out, " ")
}

func steps(spec ...string) []api.PlanStep {
	var out []api.PlanStep
	for _, s := range spec {
		st := map[string]api.PlanStatus{"[ ]": api.PlanPending, "[>]": api.PlanActive, "[x]": api.PlanDone}[s[:3]]
		out = append(out, api.PlanStep{Text: s[3:], Status: st})
	}
	return out
}

func work(run api.ID, item string, s api.WorkStatus) api.WorkItem {
	return api.WorkItem{Run: run, Item: item, Kind: "read", Title: item, Status: s}
}

func start(msg api.ID, text string, pos int64) []api.EventBody {
	return []api.EventBody{
		api.MessageQueued{Message: msg, Text: text, Mode: api.AfterActive, Pos: pos},
		api.TurnAdmitted{Run: msg, Generation: 1},
		api.TurnStarted{Run: msg},
	}
}

func TestFoldInterleavesTextAndWorkInTheOrderTheyHappened(t *testing.T) {
	a, req := api.NewID(), api.NewID()
	evs := slices.Concat(start(a, "hello", 0), []api.EventBody{
		api.AssistantDelta{Run: a, Text: "Looking "},
		api.AssistantDelta{Run: a, Text: "at the code."},
		work(a, "read-1", api.WorkStarted),
		work(a, "read-1", api.WorkCompleted),
		api.AssistantDelta{Run: a, Text: " May I edit it?"},
		api.RequestOpened{Request: req, Run: a, Generation: 1, Dedup: "approve-1", Kind: "approval", Body: json.RawMessage(`{}`)},
		api.RequestAnswer{Request: req, Answer: json.RawMessage(`{"allow":true}`)},
		api.RequestDelivery{Request: req},
		work(a, "edit-1", api.WorkStarted),
		work(a, "edit-1", api.WorkCompleted),
		api.AssistantDelta{Run: a, Text: " Done."},
	})
	tr := play(evs...)
	expect(t, tr,
		`user "hello" sent`,
		`assistant "Looking at the code."`,
		`work read-1:completed`,
		`assistant " May I edit it?"`,
		`request approval delivered {"allow":true}`,
		`work edit-1:completed`,
		`assistant " Done." streaming`,
	)
	if w := tr.Entries[2].(*WorkEntry); !w.Started.Equal(at(6)) || !w.Ended.Equal(at(7)) {
		t.Fatalf("read-1 worked from %v to %v, want from the start event to the completion", w.Started, w.Ended)
	}
	tr.apply(event(len(evs)+1, api.TurnEnded{Run: a, Outcome: api.OutcomeCompleted}))
	if last := describe(tr.last()); last != `assistant " Done."` {
		t.Fatalf("after the turn ended the last entry is %s, want it done streaming", last)
	}
	if tr.Cursor != (api.Cursor{Log: testLog, Seq: uint64(len(evs) + 1)}) {
		t.Fatalf("cursor %+v, want the last event's", tr.Cursor)
	}
}

// A run's plans are one entry, where it first said one, holding the last;
// the thread's plan is the latest, done or not, until it is cleared. The
// next run's plan is an entry of its own.
func TestFoldKeepsEachRunsLatestPlan(t *testing.T) {
	a, b := api.NewID(), api.NewID()
	tr := play(slices.Concat(start(a, "go", 0), []api.EventBody{
		api.PlanUpdated{Run: a, Steps: steps("[>]Read", "[ ]Fix")},
		work(a, "read-1", api.WorkCompleted),
		api.PlanUpdated{Run: a, Steps: steps("[x]Read", "[>]Fix")},
		api.AssistantDelta{Run: a, Text: "Fixing."},
	})...)
	expect(t, tr,
		`user "go" sent`,
		`plan [x]Read [>]Fix`,
		`work read-1:completed`,
		`assistant "Fixing." streaming`,
	)
	if tr.Plan == nil || planOf(tr.Plan.Steps) != "[x]Read [>]Fix" || tr.Plan.Done() != 1 {
		t.Fatalf("the thread's plan is %+v, want the run's latest, one of two done", tr.Plan)
	}
	next := 8
	for i, ev := range slices.Concat([]api.EventBody{api.TurnEnded{Run: a, Outcome: api.OutcomeInterrupted}}, start(b, "more", 1),
		[]api.EventBody{api.PlanUpdated{Run: b, Steps: steps("[x]Test")}}) {
		tr.apply(event(next+i, ev))
	}
	if tr.Plan == nil || planOf(tr.Plan.Steps) != "[x]Test" || describe(tr.last()) != "plan [x]Test" {
		t.Fatalf("after the next run's plan the thread's is %+v and its last entry %s, want the next run's in an entry of its own", tr.Plan, describe(tr.last()))
	}
	tr.apply(event(next+5, api.TurnEnded{Run: b, Outcome: api.OutcomeCompleted}))
	if tr.Plan == nil || planOf(tr.Plan.Steps) != "[x]Test" {
		t.Fatalf("a run that ended with its plan done leaves the thread's plan %+v, want it kept till the next", tr.Plan)
	}
	cleared := play(slices.Concat(start(a, "go", 0), []api.EventBody{
		api.PlanUpdated{Run: a, Steps: steps("[>]Read")},
		api.PlanUpdated{Run: a},
	})...)
	if cleared.Plan != nil || describe(cleared.Entries[1]) != "plan [>]Read" {
		t.Fatalf("a cleared plan leaves the thread's %+v and the entry %s, want none and the entry as it last said", cleared.Plan, describe(cleared.Entries[1]))
	}
}

func TestFoldUpsertsWorkItemsByID(t *testing.T) {
	a := api.NewID()
	evs := slices.Concat(start(a, "go", 0), []api.EventBody{
		work(a, "a", api.WorkStarted),
		work(a, "b", api.WorkStarted),
		work(a, "a", api.WorkCompleted),
	})
	tr := play(evs...)
	expect(t, tr, `user "go" sent`, `work a:completed b:started working`)

	evs = append(evs,
		api.AssistantDelta{Run: a, Text: "meanwhile"},
		work(a, "b", api.WorkFailed),
		work(a, "c", api.WorkStarted),
	)
	tr = play(evs...)
	expect(t, tr, `user "go" sent`, `work a:completed b:failed`, `assistant "meanwhile"`, `work c:started working`)
	if w := tr.Entries[1].(*WorkEntry); !w.Ended.Equal(at(8)) {
		t.Fatalf("the first work entry ended at %v, want when its last item finished (%v)", w.Ended, at(8))
	}

	tr.apply(event(len(evs)+1, api.TurnEnded{Run: a, Outcome: api.OutcomeInterrupted}))
	if w := tr.Entries[3].(*WorkEntry); !w.Ended.Equal(at(len(evs) + 1)) {
		t.Fatalf("an item still going when the run ended leaves its entry ended at %v, want the run's end", w.Ended)
	}
}

func TestFoldKeepsAQueuedMessageOutOfTheTurnItWaitsBehind(t *testing.T) {
	a, b, r := api.NewID(), api.NewID(), api.NewID()
	evs := slices.Concat(start(a, "one", 0), []api.EventBody{
		api.AssistantDelta{Run: a, Text: "first"},
		api.MessageQueued{Message: b, Text: "two", Mode: api.AfterActive, Pos: 1},
		api.MessageQueued{Message: r, Text: "redo", Mode: api.RestartActive, Pos: -1},
		api.AssistantDelta{Run: a, Text: " half"},
	})
	tr := play(evs...)
	expect(t, tr,
		`user "one" sent`,
		`assistant "first half" streaming`,
		"--",
		`user "redo" queued`,
		`user "two" queued`,
	)

	evs = append(evs,
		api.TurnEnded{Run: a, Outcome: api.OutcomeInterrupted},
		api.TurnAdmitted{Run: r, Generation: 2},
		api.TurnStarted{Run: r},
		api.AssistantDelta{Run: r, Text: "again"},
	)
	expect(t, play(evs...),
		`user "one" sent`,
		`assistant "first half"`,
		`user "redo" sent`,
		`assistant "again" streaming`,
		"--",
		`user "two" queued`,
	)
}

func TestFoldPlacesASteerWhereItReachedTheRun(t *testing.T) {
	a, c := api.NewID(), api.NewID()
	evs := slices.Concat(start(a, "one", 0), []api.EventBody{
		api.AssistantDelta{Run: a, Text: "first"},
		api.MessageQueued{Message: c, Text: "faster", Mode: api.SteerActive, Pos: 1},
		api.MessageClaimed{Message: c, Run: a},
		api.AssistantDelta{Run: a, Text: " half"},
	})
	expect(t, play(evs...),
		`user "one" sent`,
		`assistant "first half" streaming`,
		"--",
		`user "faster" claimed`,
	)
	evs = append(evs,
		api.MessageDispatched{Message: c},
		api.AssistantDelta{Run: a, Text: "steered: faster"},
	)
	expect(t, play(evs...),
		`user "one" sent`,
		`assistant "first half"`,
		`user "faster" sent`,
		`assistant "steered: faster" streaming`,
	)
}

func TestFoldReleasesHeldBackAndCancelledMessages(t *testing.T) {
	a, steer, held, gone, never := api.NewID(), api.NewID(), api.NewID(), api.NewID(), api.NewID()
	evs := slices.Concat(start(a, "one", 0), []api.EventBody{
		api.MessageQueued{Message: held, Text: "later", Mode: api.Defer, Pos: 1, Held: true},
		api.MessageQueued{Message: gone, Text: "lost", Mode: api.AfterActive, Pos: 2},
		api.MessageCancelled{Message: gone, Reason: "the database was restored"},
		api.MessageQueued{Message: steer, Text: "faster", Mode: api.SteerActive, Pos: 3},
		api.MessageClaimed{Message: steer, Run: a},
		api.TurnEnded{Run: a, Outcome: api.OutcomeInterrupted},
		api.MessageQueued{Message: never, Text: "now", Mode: api.StartNow, Pos: 4},
		api.TurnAdmitted{Run: never, Generation: 2},
		api.TurnEnded{Run: never, Outcome: api.OutcomeFailed},
	})
	expect(t, play(evs...),
		`user "one" sent`,
		`user "lost" cancelled the database was restored`,
		`user "faster" cancelled`,
		`user "now" cancelled`,
		"--",
		`user "later" held`,
	)
}

// A stop holds what waits behind the run, and a resume or a send makes it
// queued again, in its place.
func TestFoldHoldsWhatAStopHeld(t *testing.T) {
	a, b, c := api.NewID(), api.NewID(), api.NewID()
	evs := slices.Concat(start(a, "one", 0), []api.EventBody{
		api.MessageQueued{Message: b, Text: "two", Mode: api.AfterActive, Pos: 1},
		api.MessageQueued{Message: c, Text: "three", Mode: api.AfterActive, Pos: 2},
		api.TurnStopping{Run: a},
		api.MessageHeld{Message: b},
		api.MessageHeld{Message: c},
		api.TurnEnded{Run: a, Outcome: api.OutcomeInterrupted},
	})
	expect(t, play(evs...),
		`user "one" sent`,
		"--",
		`user "two" held`,
		`user "three" held`,
	)
	evs = append(evs, api.MessageReleased{Message: b}, api.MessageReleased{Message: c})
	expect(t, play(evs...),
		`user "one" sent`,
		"--",
		`user "two" queued`,
		`user "three" queued`,
	)
}

// An edit changes a queued message's words where it waits, and a move its
// place, before and after one that ties with it.
func TestFoldShowsAQueuedMessagesNewWordsAndPlace(t *testing.T) {
	a, b, c, d := api.NewID(), api.NewID(), api.NewID(), api.NewID()
	evs := slices.Concat(start(a, "one", 0), []api.EventBody{
		api.MessageQueued{Message: b, Text: "two", Mode: api.AfterActive, Pos: 1},
		api.MessageQueued{Message: c, Text: "three", Mode: api.AfterActive, Pos: 2},
		api.MessageQueued{Message: d, Text: "four", Mode: api.Defer, Pos: 3, Held: true},
		api.MessageEdited{Message: c, Text: "three, shorter"},
		api.MessageMoved{Message: d, Pos: 0},
		api.MessageMoved{Message: c, Pos: 1},
		api.MessageMoved{Message: b, Pos: 2},
	})
	expect(t, play(evs...),
		`user "one" sent`,
		"--",
		`user "four" held`,
		`user "three, shorter" queued`,
		`user "two" queued`,
	)
}

// A failed turn shows why it failed, whether its agent never started or
// started and then failed; a turn that ended any other way shows no error.
func TestFoldShowsWhyATurnFailed(t *testing.T) {
	a, b, c := api.NewID(), api.NewID(), api.NewID()
	evs := slices.Concat(start(a, "one", 0), []api.EventBody{
		api.AssistantDelta{Run: a, Text: "Hi"},
		api.TurnEnded{Run: a, Outcome: api.OutcomeFailed, Error: "Claude Code is not signed in."},
		api.MessageQueued{Message: b, Text: "two", Mode: api.StartNow, Pos: 1},
		api.TurnAdmitted{Run: b, Generation: 2},
		api.MessageClaimed{Message: b, Run: b},
		api.TurnEnded{Run: b, Outcome: api.OutcomeFailed, Error: "Claude Code is not installed."},
		api.MessageQueued{Message: c, Text: "three", Mode: api.StartNow, Pos: 2},
		api.TurnAdmitted{Run: c, Generation: 3},
		api.MessageClaimed{Message: c, Run: c},
		api.TurnEnded{Run: c, Outcome: api.OutcomeInterrupted, Error: "stopped"},
	})
	expect(t, play(evs...),
		`user "one" sent`,
		`assistant "Hi"`,
		`error "Claude Code is not signed in."`,
		`user "two" cancelled`,
		`error "Claude Code is not installed."`,
		`user "three" cancelled`,
	)
}

func TestFoldMarksWhatAnUncertainRunHeldUncertain(t *testing.T) {
	a, steer, cut, req := api.NewID(), api.NewID(), api.NewID(), api.NewID()
	evs := slices.Concat(start(a, "one", 0), []api.EventBody{
		api.MessageQueued{Message: cut, Text: "cut", Mode: api.SteerActive, Pos: 1},
		api.MessageClaimed{Message: cut, Run: a},
		api.MessageUncertain{Message: cut},
		api.RequestOpened{Request: req, Run: a, Generation: 1, Kind: "question", Body: json.RawMessage(`{}`)},
		api.AssistantDelta{Run: a, Text: "…"},
		api.MessageQueued{Message: steer, Text: "faster", Mode: api.SteerActive, Pos: 2},
		api.MessageClaimed{Message: steer, Run: a},
		api.TurnUncertain{Run: a},
	})
	expect(t, play(evs...),
		`user "one" sent`,
		`user "cut" uncertain`,
		`request question invalidated`,
		`assistant "…"`,
		`user "faster" uncertain`,
	)
}

func TestFoldTracksARequestToItsEnd(t *testing.T) {
	a, answered, voided, refused := api.NewID(), api.NewID(), api.NewID(), api.NewID()
	evs := slices.Concat(start(a, "one", 0), []api.EventBody{
		api.RequestOpened{Request: answered, Run: a, Generation: 1, Kind: "approval", Body: json.RawMessage(`{}`)},
		api.RequestOpened{Request: voided, Run: a, Generation: 1, Kind: "approval", Body: json.RawMessage(`{}`)},
		api.RequestOpened{Request: refused, Run: a, Generation: 1, Kind: "approval", Body: json.RawMessage(`{}`)},
		api.RequestAnswer{Request: answered, Answer: json.RawMessage(`{"allow":false}`)},
		api.RequestInvalidation{Request: refused, Reason: "the agent went away"},
	})
	expect(t, play(evs...),
		`user "one" sent`,
		`request approval answered {"allow":false}`,
		`request approval pending`,
		`request approval invalidated the agent went away`,
	)
	evs = append(evs, api.TurnStopping{Run: a})
	expect(t, play(evs...),
		`user "one" sent`,
		`request approval answered {"allow":false}`,
		`request approval invalidated`,
		`request approval invalidated the agent went away`,
	)
}

func TestFoldSkipsUnknownAndRepeatedEvents(t *testing.T) {
	a := api.NewID()
	tr := play(start(a, "one", 0)...)
	tr.apply(event(4, api.AssistantDelta{Run: a, Text: "x"}))
	tr.apply(api.Event{Cursor: api.Cursor{Log: testLog, Seq: 5}, Type: "thread.renamed", Payload: json.RawMessage(`{"title":"new"}`)})
	tr.apply(api.Event{Cursor: api.Cursor{Log: testLog, Seq: 6}, Type: "assistant.delta", Payload: json.RawMessage(`{"run":7}`)})
	tr.apply(event(4, api.AssistantDelta{Run: a, Text: "x"}))
	tr.apply(event(7, api.AssistantDelta{Run: a, Text: "y"}))
	expect(t, tr, `user "one" sent`, `assistant "xy" streaming`)
	if tr.Cursor.Seq != 7 {
		t.Fatalf("cursor at %d, want 7: skipped events still move it", tr.Cursor.Seq)
	}
	// A restored database starts a new log, whose events fold even at a
	// lower seq.
	tr.apply(api.Event{Cursor: api.Cursor{Log: api.NewID(), Seq: 1}, Type: "assistant.delta", Payload: json.RawMessage(`{"run":"` + a.String() + `","text":"z"}`)})
	expect(t, tr, `user "one" sent`, `assistant "xyz" streaming`)
}

func TestFoldMarksADeletedThread(t *testing.T) {
	a := api.NewID()
	tr := play(append(start(a, "one", 0), api.TurnEnded{Run: a}, api.ThreadDeleted{})...)
	if !tr.Deleted {
		t.Fatal("the tombstone folded in, and the transcript is not marked deleted")
	}
	expect(t, tr, `user "one" sent`)
}

// longRun is a turn whose text arrives in n deltas.
func longRun(n int) []api.EventBody {
	run := api.NewID()
	evs := start(run, "go", 0)
	for range n {
		evs = append(evs, api.AssistantDelta{Run: run, Text: "twenty bytes of text"})
	}
	return evs
}

func allocated() uint64 {
	var s runtime.MemStats
	runtime.ReadMemStats(&s)
	return s.TotalAlloc
}

func TestFoldingTextCostsInProportionToIt(t *testing.T) {
	cost := func(n int) uint64 {
		evs := longRun(n)
		before := allocated()
		tr := play(evs...)
		after := allocated()
		if got := len(tr.last().(*AssistantEntry).Text); got != 20*n {
			t.Fatalf("the text is %d bytes, want %d", got, 20*n)
		}
		return after - before
	}
	small, large := cost(1000), cost(2000)
	if large > 3*small {
		t.Fatalf("folding twice the text allocated %d bytes against %d, want about twice", large, small)
	}
}

var engineStates = map[eng.QueueState]MessageState{
	eng.QueueReady: Queued, eng.QueueHeld: Held, eng.QueueClaimed: Claimed,
	eng.QueueDispatched: Sent, eng.QueueCancelled: Cancelled, eng.QueueUncertain: Uncertain,
}

// The fold reads events as the engine's Apply does: after every event of a
// play, each message and request is in the state the engine holds.
func TestFoldAgreesWithTheEngine(t *testing.T) {
	a, b, c, d, req := api.NewID(), api.NewID(), api.NewID(), api.NewID(), api.NewID()
	opened := api.RequestOpened{Request: req, Run: a, Generation: 1, Kind: "approval", Body: json.RawMessage(`{}`)}
	queued := func(id api.ID, mode api.DispatchMode, pos int64) api.MessageQueued {
		return api.MessageQueued{Message: id, Text: "x", Mode: mode, Pos: pos, Held: mode == api.Defer}
	}
	plays := []struct {
		name   string
		events []api.EventBody
	}{
		{"normal", slices.Concat(start(a, "one", 0), []api.EventBody{
			api.AssistantDelta{Run: a, Text: "hi"},
			opened,
			api.RequestAnswer{Request: req, Answer: json.RawMessage(`{"allow":true}`)},
			api.RequestDelivery{Request: req},
			queued(b, api.AfterActive, 1),
			api.TurnEnded{Run: a, Outcome: api.OutcomeCompleted},
			api.TurnAdmitted{Run: b, Generation: 2},
			api.TurnStarted{Run: b},
			api.TurnEnded{Run: b, Outcome: api.OutcomeCompleted},
		})},
		{"steered", slices.Concat(start(a, "one", 0), []api.EventBody{
			queued(b, api.SteerActive, 1),
			api.MessageClaimed{Message: b, Run: a},
			api.MessageDispatched{Message: b},
			queued(c, api.SteerActive, 2),
			api.MessageClaimed{Message: c, Run: a},
			api.TurnEnded{Run: a, Outcome: api.OutcomeCompleted},
		})},
		{"deferred", slices.Concat(start(a, "one", 0), []api.EventBody{
			queued(b, api.Defer, 1),
			api.TurnEnded{Run: a, Outcome: api.OutcomeCompleted},
			api.TurnAdmitted{Run: b, Generation: 2},
			api.TurnStarted{Run: b},
			api.TurnEnded{Run: b, Outcome: api.OutcomeCompleted},
		})},
		{"cancelled", slices.Concat(start(a, "one", 0), []api.EventBody{
			queued(b, api.AfterActive, 1),
			api.MessageCancelled{Message: b, Reason: "the database was restored"},
			queued(c, api.SteerActive, 2),
			api.MessageClaimed{Message: c, Run: a},
			api.MessageCancelled{Message: c, Reason: "the agent refused it"},
			api.TurnEnded{Run: a, Outcome: api.OutcomeFailed},
		})},
		{"interrupted", slices.Concat(start(a, "one", 0), []api.EventBody{
			opened,
			queued(b, api.SteerActive, 1),
			api.MessageClaimed{Message: b, Run: a},
			api.TurnStopping{Run: a},
			api.TurnSignalled{Run: a},
			api.TurnEnded{Run: a, Outcome: api.OutcomeInterrupted},
		})},
		{"interrupted while starting", []api.EventBody{
			queued(a, api.AfterActive, 0),
			api.TurnAdmitted{Run: a, Generation: 1},
			api.TurnStopping{Run: a},
			api.MessageDispatched{Message: a},
			api.TurnEnded{Run: a, Outcome: api.OutcomeInterrupted},
		}},
		{"restored starting", []api.EventBody{
			queued(a, api.AfterActive, 0),
			api.TurnAdmitted{Run: a, Generation: 1},
			api.TurnRestored{Run: a},
			api.TurnEnded{Run: a, Outcome: api.OutcomeCompleted},
		}},
		{"restored running", slices.Concat(start(a, "one", 0), []api.EventBody{
			api.TurnRestored{Run: a},
			api.TurnEnded{Run: a, Outcome: api.OutcomeCompleted},
		})},
		{"restored stopping", []api.EventBody{
			queued(a, api.AfterActive, 0),
			api.TurnAdmitted{Run: a, Generation: 1},
			api.TurnStopping{Run: a},
			api.TurnRestored{Run: a},
			api.TurnEnded{Run: a, Outcome: api.OutcomeInterrupted},
		}},
		{"edited and moved", slices.Concat(start(a, "one", 0), []api.EventBody{
			queued(b, api.AfterActive, 1),
			queued(c, api.AfterActive, 2),
			queued(d, api.Defer, 3),
			api.MessageEdited{Message: b, Text: "two"},
			api.MessageMoved{Message: d, Pos: 0},
			api.MessageMoved{Message: b, Pos: 4},
			api.MessageMoved{Message: c, Pos: 5},
			api.MessageEdited{Message: d, Text: "first"},
			api.TurnEnded{Run: a, Outcome: api.OutcomeCompleted},
			api.TurnAdmitted{Run: b, Generation: 2},
			api.MessageMoved{Message: d, Pos: 6},
			api.MessageCancelled{Message: c, Reason: "taken out of the queue"},
		})},
		{"uncertain", slices.Concat(start(a, "one", 0), []api.EventBody{
			queued(b, api.SteerActive, 1),
			api.MessageClaimed{Message: b, Run: a},
			api.MessageUncertain{Message: b},
			opened,
			queued(c, api.SteerActive, 2),
			api.MessageClaimed{Message: c, Run: a},
			api.TurnUncertain{Run: a},
			api.TurnEnded{Run: a, Outcome: api.OutcomeAbandoned},
		})},
	}
	for _, p := range plays {
		t.Run(p.name, func(t *testing.T) {
			thread := api.NewID()
			events := append([]api.EventBody{api.ThreadCreated{Workspace: "/w", Title: "t", Model: api.ModelRef{Provider: "fake", Model: "fake"}}}, p.events...)
			var et eng.Thread
			tr := &Transcript{}
			for i, b := range events {
				et = eng.Apply(et, eng.Event{Thread: thread, At: at(i + 1), Body: b})
				tr.apply(event(i+1, b))
				var line []eng.Message
				for _, m := range et.Queue {
					if u := tr.message(m.ID); u == nil || u.State != engineStates[m.State] || u.Text != m.Text {
						t.Fatalf("after event %d, %s, the fold shows message %s as %s, want %s %q", i, api.EventType(b), m.ID, describe(u), m.State, m.Text)
					}
					if m.State == eng.QueueReady || m.State == eng.QueueHeld && m.Mode == api.Defer {
						line = append(line, m)
					}
				}
				slices.SortStableFunc(line, func(x, y eng.Message) int { return cmp.Compare(x.Pos, y.Pos) })
				var want, got []api.ID
				for _, m := range line {
					want = append(want, m.ID)
				}
				for _, u := range tr.Queue {
					if u.State == Queued || u.State == Held {
						got = append(got, u.Message)
					}
				}
				if !slices.Equal(got, want) {
					t.Fatalf("after event %d, %s, the fold queues %v, the engine %v", i, api.EventType(b), got, want)
				}
				for _, r := range et.Requests {
					if e := tr.request(r.ID); e == nil || e.State != r.State {
						t.Fatalf("after event %d, %s, the fold shows request %s as %s, want %s", i, api.EventType(b), r.ID, describe(e), requestStates[r.State])
					}
				}
			}
		})
	}
}

// A step keeps when it started across its updates and when it ended, and
// a command's exit code and output.
func TestFoldTimesEachStepAndKeepsHowACommandWent(t *testing.T) {
	a := api.NewID()
	cmd := api.WorkItem{Run: a, Item: "c", Kind: "command", Title: "bun test", Status: api.WorkStarted}
	done := cmd
	done.Status, done.Exit, done.Output = api.WorkFailed, new(1), "1 failed"
	evs := slices.Concat(start(a, "go", 0), []api.EventBody{cmd, api.AssistantDelta{Run: a, Text: "…"}, done})
	tr := play(evs...)
	s := tr.Entries[1].(*WorkEntry).Items[0]
	if first := len(evs) - 2; !s.Started.Equal(at(first)) || !s.Ended.Equal(at(len(evs))) {
		t.Errorf("step from %v to %v, want %v to %v", s.Started, s.Ended, at(first), at(len(evs)))
	}
	if s.Exit == nil || *s.Exit != 1 || s.Output != "1 failed" || s.Status != api.WorkFailed {
		t.Errorf("step %+v, want failed with exit 1 and its output", s)
	}
}

// A later event for a step that leaves a field out keeps what the earlier
// one said: an edit's path, a command's exit code and output.
func TestFoldKeepsWhatALaterStepEventLeavesOut(t *testing.T) {
	a := api.NewID()
	edit := api.WorkItem{Run: a, Item: "e", Kind: "edit", Title: "Edit main.go", Status: api.WorkStarted, Path: "main.go"}
	cmd := api.WorkItem{Run: a, Item: "c", Kind: "command", Title: "bun test", Status: api.WorkFailed, Exit: new(1), Output: "1 failed"}
	tr := play(slices.Concat(start(a, "go", 0), []api.EventBody{
		edit, cmd,
		api.WorkItem{Run: a, Item: "e", Status: api.WorkCompleted},
		api.WorkItem{Run: a, Item: "c", Status: api.WorkFailed},
	})...)
	steps := tr.Entries[1].(*WorkEntry).Items
	if e := steps[0]; e.Path != "main.go" || e.Kind != "edit" || e.Title != "Edit main.go" || e.Status != api.WorkCompleted {
		t.Errorf("edit step %+v, want its path, kind and title kept, completed", e)
	}
	if c := steps[1]; c.Exit == nil || *c.Exit != 1 || c.Output != "1 failed" {
		t.Errorf("command step %+v, want its exit code and output kept", c)
	}
}

// A child's report folds in as an entry of its own, after the text before
// it, which it ends, and marks when the last report came.
func TestAReportIsAnEntry(t *testing.T) {
	run, child := api.NewID(), api.NewID()
	tr := play(
		api.AssistantDelta{Run: run, Text: "Started a child."},
		api.ChildReported{Child: child, Run: api.NewID(), Kind: api.ReportFailed, Title: "Fix it", Outcome: "boom"},
	)
	if len(tr.Entries) != 2 {
		t.Fatalf("%d entries, want the text and the report", len(tr.Entries))
	}
	if a := tr.Entries[0].(*AssistantEntry); a.Streaming {
		t.Error("the text before the report still streams")
	}
	r, ok := tr.Entries[1].(*ReportEntry)
	if !ok || r.Child != child || r.Kind != api.ReportFailed || r.Outcome != "boom" || !r.At.Equal(at(2)) || !tr.Reported.Equal(at(2)) {
		t.Errorf("the report: %+v, reported %v", tr.Entries[1], tr.Reported)
	}
}

// A report after the last read turn makes the thread unread, as a turn's
// end does; marking it shown reads it.
func TestAReportIsNews(t *testing.T) {
	m, _ := manager(t, stateDir(t)) // closed at the end, its saves with it
	th := &Thread{View: api.Thread{ID: api.NewID(), LastTurn: &api.TurnResult{Ended: at(1)}}}
	m.MarkRead(th)
	if m.Unread(th) {
		t.Fatal("unread once read")
	}
	th.View.Reported = at(2)
	if !m.Unread(th) {
		t.Fatal("a report after the read turn left the thread read")
	}
	th.Transcript = &Transcript{Ended: at(1), Reported: at(2)}
	m.MarkShown(th)
	if m.Unread(th) {
		t.Error("shown with its report, the thread is still unread")
	}
}

// A worker's report to a Manager is no news: the Manager vets it in a quiet
// wake. A wake it raised ends as a turn of the user's does, which is.
func TestAReportToAManagerIsNoNews(t *testing.T) {
	m, _ := manager(t, stateDir(t))
	th := &Thread{View: api.Thread{ID: api.NewID(), Role: api.RoleManager, LastTurn: &api.TurnResult{Ended: at(1)}}}
	m.MarkRead(th)
	th.View.Reported = at(2)
	if m.Unread(th) {
		t.Fatal("a worker's report made the Manager unread")
	}
	th.View.LastTurn = &api.TurnResult{Ended: at(3)}
	if !m.Unread(th) {
		t.Fatal("a raised wake's end left the Manager read")
	}
	th.Transcript = &Transcript{Ended: at(1), Reported: at(4)}
	m.MarkShown(th)
	if !m.Unread(th) {
		t.Error("a report shown read the raised wake the transcript has not shown")
	}
}

// A wake the engine queued with a child's report shows on that report, not
// as the user's message: waiting, then sent with its own turn or with the
// user's, or lost with a run that never started.
func TestAWakeShowsOnItsReport(t *testing.T) {
	child, w1, w2, w3, user := api.NewID(), api.NewID(), api.NewID(), api.NewID(), api.NewID()
	reported := func(kind api.ReportKind) api.ChildReported {
		return api.ChildReported{Child: child, Run: api.NewID(), Kind: kind, Title: "Write the tests", Outcome: "Added 9 table tests."}
	}
	wake := func(id api.ID) api.MessageQueued {
		return api.MessageQueued{Message: id, Text: "<task>…</task>", Mode: api.AfterActive, Held: true, Report: child}
	}
	tr := play(reported(api.ReportDone), wake(w1))
	r := tr.Entries[0].(*ReportEntry)
	if len(tr.Entries) != 1 || len(tr.Queue) != 0 || r.Wake != WakeWaiting {
		t.Fatalf("a queued wake: %v, wake %d; want only the report, waiting", lines(tr), r.Wake)
	}

	tr = play(reported(api.ReportDone), wake(w1), api.TurnAdmitted{Run: w1, Generation: 1}, api.TurnStarted{Run: w1})
	if r := tr.Entries[0].(*ReportEntry); r.Wake != WakeSent {
		t.Errorf("a wake's own turn started: wake %d, want sent", r.Wake)
	}

	tr = play(reported(api.ReportFailed), wake(w2),
		api.MessageQueued{Message: user, Text: "carry on", Mode: api.StartNow},
		api.TurnAdmitted{Run: user, Generation: 1}, api.MessageClaimed{Message: w2, Run: user},
		api.TurnStarted{Run: user}, api.MessageDispatched{Message: w2})
	if r := tr.Entries[0].(*ReportEntry); r.Wake != WakeSent || describe(tr.Entries[1]) != `user "carry on" sent` {
		t.Errorf("a wake that went in with the user's turn: %v, wake %d; want the report sent, then the user's words", lines(tr), r.Wake)
	}

	tr = play(reported(api.ReportDone), wake(w3),
		api.TurnAdmitted{Run: w3, Generation: 1}, api.TurnStopping{Run: w3}, api.TurnEnded{Run: w3, Outcome: api.OutcomeInterrupted})
	if r := tr.Entries[0].(*ReportEntry); r.Wake != WakeLost {
		t.Errorf("a wake whose turn never started: wake %d, want lost", r.Wake)
	}

	tr = play(reported(api.ReportDone), wake(w1), api.MessageCancelled{Message: w1, Reason: "the thread was settled or deleted"})
	if r := tr.Entries[0].(*ReportEntry); r.Wake != WakeLost || len(tr.Entries) != 1 {
		t.Errorf("a cancelled wake: %v, wake %d; want the report alone, lost", lines(tr), r.Wake)
	}
}

// A turn's end, and how long it ran, is kept on its last reply that says
// something, or on its message when it said nothing; a run of no message
// of the user's, as a child's report waking the agent, keeps it unasked.
func TestFoldKeepsHowATurnEndedOnWhatItSaidLast(t *testing.T) {
	a, b, wake := api.NewID(), api.NewID(), api.NewID()
	evs := slices.Concat(start(a, "one", 0), []api.EventBody{
		api.AssistantDelta{Run: a, Text: "Looking."},
		work(a, "read", api.WorkCompleted),
		api.AssistantDelta{Run: a, Text: "Done."},
		api.TurnEnded{Run: a, Outcome: api.OutcomeCompleted},
	}, start(b, "two", 1), []api.EventBody{
		api.AssistantDelta{Run: b, Text: "Half"},
		work(b, "edit", api.WorkCompleted),
		api.AssistantDelta{Run: b, Text: " \n "},
		api.TurnEnded{Run: b, Outcome: api.OutcomeFailed, Error: "boom"},
		api.TurnAdmitted{Run: wake, Generation: 3},
		api.TurnStarted{Run: wake},
		api.AssistantDelta{Run: wake, Text: "The child is done."},
		api.TurnEnded{Run: wake, Outcome: api.OutcomeFailed, Error: "boom"},
	})
	tr := play(evs...)
	var got []string
	for _, e := range tr.Entries {
		var end *TurnEnd
		switch e := e.(type) {
		case *UserEntry:
			end = e.End
		case *AssistantEntry:
			end = e.End
		default:
			continue
		}
		s := describe(e)
		if end != nil {
			s += fmt.Sprintf(" ended %d at %s after %s", end.Outcome, end.At.Format("15:04:05"), end.Took)
			if !end.Asked {
				s += " unasked"
			}
		}
		got = append(got, s)
	}
	want := []string{
		`user "one" sent`,
		`assistant "Looking."`,
		`assistant "Done." ended 0 at 09:00:06 after 4s`,
		`user "two" sent`,
		`assistant "Half" ended 2 at 09:00:13 after 4s`,
		`assistant " \n "`,
		`assistant "The child is done." ended 2 at 09:00:17 after 2s unasked`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A silent idle compaction folds into a single CompactionEntry without
// showing a /compact user bubble, assistant noise, or failure errors, and
// leaves Transcript.Ended on the last real turn.
func TestFoldFoldsSilentCompaction(t *testing.T) {
	a, c1, c2 := api.NewID(), api.NewID(), api.NewID()
	evs := slices.Concat(start(a, "one", 0), []api.EventBody{
		api.AssistantDelta{Run: a, Text: "Done."},
		api.TurnEnded{Run: a, Outcome: api.OutcomeCompleted},
		api.MessageQueued{Message: c1, Text: "/compact", Mode: api.AfterActive, Pos: 1, Silent: true},
		api.TurnAdmitted{Run: c1, Generation: 2, Silent: true},
		api.TurnStarted{Run: c1},
		api.AssistantDelta{Run: c1, Text: "Compacted."},
		api.ContextReported{Run: c1, Compacted: true, PreTokens: 250000, PostTokens: 32000},
		api.TurnEnded{Run: c1, Outcome: api.OutcomeCompleted},
		api.MessageQueued{Message: c2, Text: "/compact", Mode: api.AfterActive, Pos: 2, Silent: true},
		api.TurnAdmitted{Run: c2, Generation: 3, Silent: true},
		api.TurnEnded{Run: c2, Outcome: api.OutcomeFailed, Error: "session gone"},
	})
	tr := play(evs...)
	expect(t, tr,
		`user "one" sent`,
		`assistant "Done."`,
		`compact done 250000->32000`,
		`compact skipped 0->0`,
	)
	if !tr.Ended.Equal(at(5)) {
		t.Errorf("Transcript.Ended = %v, want %v (last non-silent turn)", tr.Ended, at(5))
	}
}
