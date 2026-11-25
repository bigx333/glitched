package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/store"
)

func nextWithin(s *stream, d time.Duration) (api.StreamItem, error) {
	c, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return s.Next(c)
}

func TestReplayedBytesCountAgainstTheCap(t *testing.T) {
	mine := api.NewID()
	s := &stream{thread: mine, wake: make(chan struct{}, 1)}
	replayed := api.Event{Cursor: api.Cursor{Seq: 1}, Thread: mine, Payload: make([]byte, 512<<10)}
	s.start([]api.StreamItem{{Kind: api.StreamEvent, Event: &replayed}}, 1)
	for range 2 {
		if _, err := nextWithin(s, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	s.publish(store.Commit{Events: []api.Event{{Cursor: api.Cursor{Seq: 2}, Thread: mine, Payload: make([]byte, streamBytes+(256<<10))}}})
	if _, err := nextWithin(s, time.Second); !errors.Is(err, api.ErrOverflow) {
		t.Fatalf("an event over the cap after a drained replay: %v, want api.ErrOverflow", err)
	}
}

func TestCatchUpTailIsBounded(t *testing.T) {
	mine, other := api.NewID(), api.NewID()
	s := &stream{thread: mine, wake: make(chan struct{}, 1)}
	s.publish(store.Commit{Events: []api.Event{{Cursor: api.Cursor{Seq: 1}, Thread: other, Payload: make([]byte, streamBytes+1)}}})
	s.start(nil, 0)
	if it, err := nextWithin(s, time.Second); err != nil || it.Kind != api.StreamSynchronized {
		t.Fatalf("another thread's event during catch-up: %+v %v, want synchronized", it, err)
	}

	s = &stream{thread: mine, wake: make(chan struct{}, 1)}
	s.publish(store.Commit{Events: []api.Event{{Cursor: api.Cursor{Seq: 1}, Thread: mine, Payload: make([]byte, streamBytes+1)}}})
	if _, err := nextWithin(s, 100*time.Millisecond); !errors.Is(err, api.ErrOverflow) {
		t.Fatalf("a tail over the cap while catching up: %v, want api.ErrOverflow at once", err)
	}
}

func TestShellViewsCountAgainstTheCap(t *testing.T) {
	s := &stream{wake: make(chan struct{}, 1)}
	s.start(nil, 0)
	if it, err := nextWithin(s, time.Second); err != nil || it.Kind != api.StreamSynchronized {
		t.Fatalf("an empty catch-up: %+v %v, want synchronized", it, err)
	}
	view := api.Thread{ID: api.NewID(), Requests: []api.Request{{Body: make([]byte, 1<<20)}}}
	for seq := range uint64(streamBytes>>20) + 1 {
		s.publish(store.Commit{Events: []api.Event{{Cursor: api.Cursor{Seq: seq + 1}, Thread: view.ID}}, Threads: []api.Thread{view}})
	}
	if _, err := nextWithin(s, time.Second); !errors.Is(err, api.ErrOverflow) {
		t.Fatalf("unread views over the cap: %v, want api.ErrOverflow", err)
	}
}

// A thread's stream that asks for views sends the thread's view on the
// item of the last of its events in each commit that moves it, so the two
// arrive whole, and nothing for a commit that doesn't.
func TestAThreadsViewComesWithTheEventsOfItsCommit(t *testing.T) {
	mine, other := api.NewID(), api.NewID()
	s := &stream{thread: mine, views: true, wake: make(chan struct{}, 1)}
	s.start(nil, 0)
	if it, err := nextWithin(s, time.Second); err != nil || it.Kind != api.StreamSynchronized {
		t.Fatalf("an empty catch-up: %+v %v, want synchronized", it, err)
	}
	s.publish(store.Commit{Events: []api.Event{{Cursor: api.Cursor{Seq: 1}, Thread: other}}, Threads: []api.Thread{{ID: other}}})
	s.publish(store.Commit{Events: []api.Event{{Cursor: api.Cursor{Seq: 2}, Thread: mine}, {Cursor: api.Cursor{Seq: 3}, Thread: mine}}, Threads: []api.Thread{{ID: other}, {ID: mine, Resumable: true}}})
	var got []api.StreamItem
	for range 2 {
		it, err := nextWithin(s, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, it)
	}
	if got[0].Event.Cursor.Seq != 2 || got[0].Thread != nil || got[1].Event.Cursor.Seq != 3 || got[1].Thread == nil || got[1].Thread.ID != mine || !got[1].Thread.Resumable {
		t.Fatalf("got %+v, want the commit's events, its view on the last", got)
	}
	if it, err := nextWithin(s, 100*time.Millisecond); err == nil {
		t.Fatalf("got %+v after the commit, want nothing", it)
	}
}
