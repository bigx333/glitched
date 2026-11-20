package main

import (
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

// TestRowsFollowTheTranscript changes a long transcript at random, as the
// client folds events and the user sends, and checks that the rows the
// timeline reads are entriesOf's after each change, with the files, turns
// and waiting requests they hold, wherever in the thread the change was.
func TestRowsFollowTheTranscript(t *testing.T) {
	for seed := range uint64(4) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, 7))
			a := &app{rows: map[api.ID]*rowsCache{}}
			tr := &client.Transcript{}
			th := thread{Thread: &client.Thread{Transcript: tr}}
			at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
			runs := []api.ID{api.NewID()}
			run := func() api.ID { return runs[r.IntN(len(runs))] }
			// A change is mostly near the end, where a run is, and else
			// anywhere.
			pick := func(ok func(client.Entry) bool) client.Entry {
				var es []client.Entry
				from := 0
				if r.IntN(2) == 0 {
					from = max(0, len(tr.Entries)-2*rowsLive)
				}
				for _, e := range tr.Entries[from:] {
					if ok(e) {
						es = append(es, e)
					}
				}
				if len(es) == 0 {
					return nil
				}
				return es[r.IntN(len(es))]
			}
			step := func(path string, status api.WorkStatus) client.WorkStep {
				at = at.Add(time.Second)
				return client.WorkStep{Item: fmt.Sprint(r.Int()), Kind: "edit", Path: path, Status: status, Started: at, Ended: at}
			}
			ops := []func(){
				func() {
					id := api.NewID()
					runs = append(runs, id)
					tr.Entries = append(tr.Entries, &client.UserEntry{Message: id, Text: "ask", State: client.Sent})
				},
				func() { tr.Entries = append(tr.Entries, &client.AssistantEntry{Run: run(), Text: "a", Streaming: true}) },
				func() {
					if e, ok := pick(func(e client.Entry) bool { _, ok := e.(*client.AssistantEntry); return ok }).(*client.AssistantEntry); ok {
						e.Text += "more"
						e.Streaming = r.IntN(3) > 0
					}
				},
				func() {
					tr.Entries = append(tr.Entries, &client.WorkEntry{Run: run(), Started: at, Items: []client.WorkStep{step(fmt.Sprintf("f%d.go", r.IntN(9)), api.WorkStarted)}})
				},
				func() {
					if e, ok := pick(func(e client.Entry) bool { _, ok := e.(*client.WorkEntry); return ok }).(*client.WorkEntry); ok {
						if r.IntN(2) == 0 {
							e.Items = append(e.Items, step(fmt.Sprintf("f%d.go", r.IntN(9)), api.WorkCompleted))
						} else if i := r.IntN(len(e.Items)); r.IntN(2) == 0 {
							e.Items[i].Status, e.Items[i].Ended = api.WorkCompleted, at.Add(time.Minute)
							e.Ended = at.Add(time.Minute)
						} else {
							// Started again, it has no end, and its file
							// moved when it started.
							e.Items[i].Status, e.Items[i].Ended = api.WorkStarted, time.Time{}
							e.Ended = time.Time{}
						}
					}
				},
				func() {
					tr.Entries = append(tr.Entries, &client.RequestEntry{Run: run(), Request: api.NewID(), Kind: "approval", State: api.RequestPending})
				},
				func() {
					if e, ok := pick(func(e client.Entry) bool { _, ok := e.(*client.RequestEntry); return ok }).(*client.RequestEntry); ok {
						e.State = []api.RequestState{api.RequestPending, api.RequestAnswered, api.RequestInvalidated}[r.IntN(3)]
					}
				},
				func() {
					// Reports one after another make one row.
					for range 1 + r.IntN(3) {
						tr.Entries = append(tr.Entries, &client.ReportEntry{ChildReported: api.ChildReported{Child: api.NewID(), Run: run()}})
					}
				},
				func() {
					if e, ok := pick(func(e client.Entry) bool { _, ok := e.(*client.ReportEntry); return ok }).(*client.ReportEntry); ok {
						e.Wake = client.WakeSent
					}
				},
				func() {
					if e, ok := pick(func(e client.Entry) bool { _, ok := e.(*client.UserEntry); return ok }).(*client.UserEntry); ok {
						e.State, e.Reason = client.Cancelled, "stopped"
					}
				},
				func() { tr.Entries = append(tr.Entries, &client.ErrorEntry{Run: run(), Error: "failed"}) },
				func() {
					if r.IntN(2) == 0 || len(tr.Queue) == 0 {
						tr.Queue = append(tr.Queue, &client.UserEntry{Message: api.NewID(), Text: "next"})
					} else {
						u := tr.Queue[0]
						tr.Queue = tr.Queue[1:]
						u.State = client.Sent
						tr.Entries = append(tr.Entries, u)
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
					// Mostly at the end, where a thread grows.
					for range 1 + r.IntN(3) {
						ops[r.IntN(len(ops))]()
					}
					a.posts++ // as changed counts each change posted
				}
				want := entriesOf(th)
				got := a.rowsOf(th)
				if !reflect.DeepEqual(got.es, want) {
					t.Fatalf("step %d: the rows differ from entriesOf's at %d entries", i, len(tr.Entries))
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
