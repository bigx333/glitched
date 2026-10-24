package runtime_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider/fake"
)

// viewsUntil reads a shell stream until thread's view satisfies ok and
// returns that thread's views in order. Anything but a thread view fails.
func viewsUntil(t *testing.T, s api.Stream, thread api.ID, what string, ok func(api.Thread) bool) []api.Thread {
	t.Helper()
	var views []api.Thread
	for {
		it := next(t, s)
		if it.Kind != api.StreamThread || it.Thread == nil {
			t.Fatalf("waiting for %s: shell item %+v, want a thread view", what, it)
		}
		if it.Thread.ID != thread {
			continue
		}
		if n := len(views); n > 0 && it.Thread.Revision <= views[n-1].Revision {
			t.Fatalf("waiting for %s: revision %d after %d, want one view per commit", what, it.Thread.Revision, views[n-1].Revision)
		}
		views = append(views, *it.Thread)
		if ok(*it.Thread) {
			return views
		}
	}
}

// stored is the view a fresh read returns. Times are compared as instants.
func stored(t *testing.T, ep api.Endpoint, thread api.ID) api.Thread {
	t.Helper()
	snap, err := ep.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	return instants(snap.Threads[0])
}

func instants(v api.Thread) api.Thread {
	v.UpdatedAt = v.UpdatedAt.UTC()
	v.Disposition.Until = v.Disposition.Until.UTC()
	if v.LastTurn != nil {
		last := *v.LastTurn
		last.Ended = last.Ended.UTC()
		v.LastTurn = &last
	}
	return v
}

func runs(views []api.Thread) []api.RunState {
	var out []api.RunState
	for _, v := range views {
		if len(out) == 0 || out[len(out)-1] != v.Run {
			out = append(out, v.Run)
		}
	}
	return out
}

func TestShellStreamsOneViewPerCommit(t *testing.T) {
	n := start(t, dbPath(t), fake.New(fake.Turn(true)))
	a := newThread(t, n.h)

	shell, err := n.h.Open(ctx, api.Subscription{})
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	first := next(t, shell)
	if first.Kind != api.StreamSnapshot || len(first.Snapshot.Threads) != 1 || first.Snapshot.Threads[0].ID != a || len(first.Snapshot.Events) != 0 {
		t.Fatalf("first shell item %+v, want a snapshot of the one thread without events", first)
	}
	if first.Snapshot.Threads[0].LastTurn != nil {
		t.Fatalf("a thread that never ran has last turn %+v", first.Snapshot.Threads[0].LastTurn)
	}
	if it := next(t, shell); it.Kind != api.StreamSynchronized {
		t.Fatalf("second shell item %v, want synchronized", it.Kind)
	}
	chat, err := n.h.Open(ctx, api.Subscription{Thread: a})
	if err != nil {
		t.Fatal(err)
	}
	defer chat.Close()
	if it := next(t, chat); it.Kind != api.StreamSnapshot {
		t.Fatalf("thread subscription: %v, want a snapshot", it.Kind)
	}
	next(t, chat)

	b := newThread(t, n.h)
	if v := viewsUntil(t, shell, b, "the new thread", func(api.Thread) bool { return true }); v[0].Revision != 1 || v[0].Title != "fix it" {
		t.Fatalf("created thread view %+v", v[0])
	}

	before := stored(t, n.h, a).Revision
	submit(t, n.h, a, dispatch("go", api.StartNow))
	views := viewsUntil(t, shell, a, "a pending request", waiting)
	if views[0].Run != api.RunStarting || views[0].Revision != before+2 {
		t.Fatalf("dispatch view %+v, want one starting view two revisions on: queued and admitted commit together", views[0])
	}
	if got, want := runs(views), []api.RunState{api.RunStarting, api.RunRunning, api.RunWaiting}; !reflect.DeepEqual(got, want) {
		t.Fatalf("run states %v, want %v", got, want)
	}
	last := instants(views[len(views)-1])
	if want := stored(t, n.h, a); !reflect.DeepEqual(last, want) {
		t.Fatalf("waiting view\n%+v\nwant the stored view\n%+v", last, want)
	}

	r := last.Requests[0]
	submit(t, n.h, a, api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: json.RawMessage(`{"allow":true}`)})
	views = viewsUntil(t, shell, a, "the turn to complete", idle)
	done := views[len(views)-1]
	if done.LastTurn == nil || done.LastTurn.Outcome != api.OutcomeCompleted || done.LastTurn.Ended.IsZero() || len(done.Requests) != 0 {
		t.Fatalf("completed view %+v, last turn %+v", done, done.LastTurn)
	}

	submit(t, n.h, a, api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	if v := viewsUntil(t, shell, a, "settled", func(api.Thread) bool { return true }); v[0].Disposition.State != api.Settled {
		t.Fatalf("disposition view %+v", v[0].Disposition)
	}

	submit(t, n.h, a, dispatch("again", api.StartNow))
	viewsUntil(t, shell, a, "the second request", waiting)
	submit(t, n.h, a, api.TurnInterrupt{})
	views = viewsUntil(t, shell, a, "the interrupt", idle)
	if got := runs(views); got[0] != api.RunStopping {
		t.Fatalf("run states after the interrupt %v, want stopping first", got)
	}
	ended := views[len(views)-1]
	if ended.LastTurn == nil || ended.LastTurn.Outcome != api.OutcomeInterrupted || !ended.LastTurn.Ended.After(done.LastTurn.Ended) {
		t.Fatalf("interrupted view, last turn %+v after %+v", ended.LastTurn, done.LastTurn)
	}
	if got := stored(t, n.h, a); !reflect.DeepEqual(instants(ended), got) {
		t.Fatalf("interrupted view\n%+v\nwant the stored view\n%+v", instants(ended), got)
	}

	var raw []api.Event
	for len(raw) == 0 || count(raw, "turn.ended") < 2 {
		it := next(t, chat)
		if it.Kind != api.StreamEvent {
			t.Fatalf("thread subscription item %v, want an event", it.Kind)
		}
		raw = append(raw, *it.Event)
	}
	if count(raw, "assistant.delta") == 0 {
		t.Fatal("the thread subscription saw no text deltas")
	}

	again, err := n.h.Open(ctx, api.Subscription{After: first.Snapshot.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	it := next(t, again)
	if it.Kind != api.StreamSnapshot || len(it.Snapshot.Threads) != 2 {
		t.Fatalf("reopened shell with a cursor: %+v, want a fresh snapshot of both threads", it)
	}
}
