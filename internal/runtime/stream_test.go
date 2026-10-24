package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"glitched/internal/api"
)

func nextWithin(s *stream, d time.Duration) (api.StreamItem, error) {
	c, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return s.Next(c)
}

func TestReplayedBytesCountAgainstTheCap(t *testing.T) {
	s := &stream{wake: make(chan struct{}, 1)}
	replayed := api.Event{Cursor: api.Cursor{Seq: 1}, Payload: make([]byte, 512<<10)}
	s.start([]api.StreamItem{{Kind: api.StreamEvent, Event: &replayed}}, 1)
	for range 2 {
		if _, err := nextWithin(s, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	s.publish([]api.Event{{Cursor: api.Cursor{Seq: 2}, Payload: make([]byte, streamBytes+(256<<10))}})
	if _, err := nextWithin(s, time.Second); !errors.Is(err, api.ErrOverflow) {
		t.Fatalf("an event over the cap after a drained replay: %v, want api.ErrOverflow", err)
	}
}

func TestCatchUpTailIsBounded(t *testing.T) {
	mine, other := api.NewID(), api.NewID()
	s := &stream{thread: mine, wake: make(chan struct{}, 1)}
	s.publish([]api.Event{{Cursor: api.Cursor{Seq: 1}, Thread: other, Payload: make([]byte, streamBytes+1)}})
	s.start(nil, 0)
	if it, err := nextWithin(s, time.Second); err != nil || it.Kind != api.StreamSynchronized {
		t.Fatalf("another thread's event during catch-up: %+v %v, want synchronized", it, err)
	}

	s = &stream{thread: mine, wake: make(chan struct{}, 1)}
	s.publish([]api.Event{{Cursor: api.Cursor{Seq: 1}, Thread: mine, Payload: make([]byte, streamBytes+1)}})
	if _, err := nextWithin(s, 100*time.Millisecond); !errors.Is(err, api.ErrOverflow) {
		t.Fatalf("a tail over the cap while catching up: %v, want api.ErrOverflow at once", err)
	}
}
