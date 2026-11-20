package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
)

// TestRowsFollowTheTranscript folds a long thread's events at random, as
// following it does, wherever in the thread they land, with snapshots that
// replace it and commands sent from here, and checks after each that the
// rows the timeline reads are entriesOf's, with the files, turns and
// waiting requests they hold.
func TestRowsFollowTheTranscript(t *testing.T) {
	for seed := range uint64(4) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, 7))
			a := &app{rows: map[api.ID]*rowsCache{}}
			tr := &client.Transcript{}
			th := thread{Thread: &client.Thread{Transcript: tr}}
			log := api.NewID()
			var played []api.Event
			play := func(b api.EventBody) {
				p, err := json.Marshal(b)
				if err != nil {
					t.Fatal(err)
				}
				e := api.Event{Cursor: api.Cursor{Log: log, Seq: uint64(len(played) + 1)}, Type: api.EventType(b), Schema: api.EventSchema,
					At: time.Date(2026, 11, 20, 9, 0, len(played), 0, time.UTC), Payload: p}
				played = append(played, e)
				tr.Apply(e)
			}
			// Mostly the newest, where a thread changes, and else any.
			pick := func(n int) int {
				if r.IntN(2) == 0 {
					return max(0, n-1-r.IntN(4))
				}
				return r.IntN(n)
			}
			var runs, queued []api.ID
			var requests []api.ID
			items := map[api.ID][]string{}
			pos := int64(0)
			turn := func() {
				id := api.NewID()
				pos++
				play(api.MessageQueued{Message: id, Text: "ask", Pos: pos})
				play(api.TurnAdmitted{Run: id})
				play(api.TurnStarted{Run: id})
				runs = append(runs, id)
			}
			turn()
			run := func() api.ID { return runs[pick(len(runs))] }
			ops := []func(){
				turn,
				func() { play(api.AssistantDelta{Run: runs[len(runs)-1], Text: "Some words. "}) },
				func() { play(api.AssistantDelta{Run: runs[len(runs)-1], Text: "More words. "}) },
				func() {
					// A step, new or again: an edit's path may change to
					// another of the same length.
					run := run()
					item := fmt.Sprint(r.IntN(1 << 20))
					if is := items[run]; len(is) > 0 && r.IntN(2) == 0 {
						item = is[pick(len(is))]
					} else {
						items[run] = append(items[run], item)
					}
					status := []api.WorkStatus{api.WorkStarted, api.WorkCompleted, api.WorkFailed}[r.IntN(3)]
					play(api.WorkItem{Run: run, Item: item, Kind: "edit", Title: "Edited", Status: status, Path: fmt.Sprintf("%c.go", 'a'+r.IntN(4))})
				},
				func() {
					id := api.NewID()
					requests = append(requests, id)
					kind := []string{"approval", "question"}[r.IntN(2)]
					play(api.RequestOpened{Request: id, Run: run(), Kind: kind, Body: json.RawMessage("{}")})
				},
				func() {
					if len(requests) > 0 {
						id := requests[pick(len(requests))]
						switch r.IntN(3) {
						case 0:
							play(api.RequestAnswer{Request: id, Answer: json.RawMessage("{}")})
						case 1:
							play(api.RequestDelivery{Request: id})
						default:
							play(api.RequestInvalidation{Request: id, Reason: "gone"})
						}
					}
				},
				func() {
					outcome := []api.Outcome{api.OutcomeCompleted, api.OutcomeFailed, api.OutcomeInterrupted}[r.IntN(3)]
					play(api.TurnEnded{Run: run(), Outcome: outcome, Error: "it failed"})
				},
				func() {
					// Reports one after another make one row.
					for range 1 + r.IntN(3) {
						play(api.ChildReported{Child: api.NewID(), Run: run(), Kind: api.ReportDone, Title: "child"})
					}
				},
				func() {
					if len(queued) > 0 && r.IntN(2) == 0 {
						id := queued[0]
						queued = queued[1:]
						play(api.MessageCancelled{Message: id, Reason: "stopped"})
						return
					}
					id := api.NewID()
					pos++
					play(api.MessageQueued{Message: id, Text: "later", Pos: pos})
					queued = append(queued, id)
				},
				func() {
					// A snapshot replaces the transcript, as reconnecting does.
					if r.IntN(10) == 0 {
						var fresh client.Transcript
						for _, e := range played {
							fresh.Apply(e)
						}
						*tr = fresh
					}
				},
			}
			// A command sent from here changes Pending at once, with no
			// change posted.
			pending := []func(){
				func() {
					th.Pending = append(th.Pending, client.Pending{Command: api.NewID(), Body: api.MessageDispatch{Text: "sending"}})
				},
				func() {
					th.Pending = append(th.Pending, client.Pending{Command: api.NewID(), Body: api.TurnInterrupt{}, Err: errors.New("refused")})
				},
				func() {
					if len(th.Pending) > 0 {
						i := r.IntN(len(th.Pending))
						if th.Pending[i].Err == nil {
							th.Pending[i].Err = errors.New("lost")
						} else {
							th.Pending = slices.Delete(th.Pending, i, i+1)
						}
					}
				},
			}
			for i := range 1200 {
				if r.IntN(8) == 0 {
					pending[r.IntN(len(pending))]()
				} else {
					for range 1 + r.IntN(3) {
						ops[r.IntN(len(ops))]()
					}
					a.posts++ // as changed counts each change posted
				}
				want := entriesOf(th)
				got := a.rowsOf(th)
				if !reflect.DeepEqual(got.es, want) {
					for k := range min(len(want), len(got.es)) {
						if !reflect.DeepEqual(got.es[k], want[k]) {
							t.Fatalf("step %d: row %d of %d is %+v, want %+v", i, k, len(want), got.es[k], want[k])
						}
					}
					t.Fatalf("step %d: %d rows, want %d", i, len(got.es), len(want))
				}
				if !reflect.DeepEqual(got.files, changedFiles(want)) || !slices.Equal(got.turns, turnsOf(want)) {
					t.Fatalf("step %d: the files or turns differ", i)
				}
				var asks []int
				for k, e := range want {
					if e.request != nil && e.request.State == api.RequestPending {
						asks = append(asks, k)
					}
				}
				if !slices.Equal(got.asks, asks) {
					t.Fatalf("step %d: the waiting requests are %v, not %v", i, got.asks, asks)
				}
			}
			if len(tr.Entries) < 3*rowsLive {
				t.Fatalf("only %d entries: the settled part was never tried", len(tr.Entries))
			}
		})
	}
}

// TestAnEditsPathChanges changes the path of an edit far up a thread to
// another of the same length: the diff lists the new path, not the old.
func TestAnEditsPathChanges(t *testing.T) {
	a := &app{rows: map[api.ID]*rowsCache{}}
	tr := &client.Transcript{}
	th := thread{Thread: &client.Thread{Transcript: tr}}
	log := api.NewID()
	seq := 0
	play := func(b api.EventBody) {
		p, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		seq++
		tr.Apply(api.Event{Cursor: api.Cursor{Log: log, Seq: uint64(seq)}, Type: api.EventType(b), Schema: api.EventSchema,
			At: time.Date(2026, 11, 20, 9, 0, seq, 0, time.UTC), Payload: p})
		a.posts++
	}
	var first api.ID
	for i := range 3 * rowsLive {
		run := api.NewID()
		if i == 0 {
			first = run
		}
		play(api.MessageQueued{Message: run, Text: "ask", Pos: int64(i + 1)})
		play(api.TurnAdmitted{Run: run})
		play(api.TurnStarted{Run: run})
		if i == 0 {
			play(api.WorkItem{Run: run, Item: "e", Kind: "edit", Title: "Edited", Status: api.WorkCompleted, Path: "a.go"})
		}
		play(api.AssistantDelta{Run: run, Text: "Done."})
		play(api.TurnEnded{Run: run, Outcome: api.OutcomeCompleted})
	}
	if files := a.rowsOf(th).files; len(files) != 1 || files[0].path != "a.go" {
		t.Fatalf("the diff lists %+v, want a.go", files)
	}
	play(api.WorkItem{Run: first, Item: "e", Status: api.WorkCompleted, Path: "b.go"})
	if files := a.rowsOf(th).files; len(files) != 1 || files[0].path != "b.go" {
		t.Errorf("the diff lists %+v after the edit moved to b.go", files)
	}
}
