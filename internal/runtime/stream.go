package runtime

import (
	"context"
	"errors"
	"sync"

	"glitched/internal/api"
)

const (
	replayEvents = 128
	replayBytes  = 1 << 20
	streamBytes  = 8 << 20
)

var errStreamClosed = errors.New("runtime: stream closed")

// Open sends the catch-up (a replay from the client's cursor, or a snapshot
// at a fence), then whatever committed meanwhile, then StreamSynchronized,
// then live events.
func (h *Host) Open(ctx context.Context, sub api.Subscription) (api.Stream, error) {
	s := &stream{h: h, thread: sub.Thread, wake: make(chan struct{}, 1)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	items, fence, err := h.catchUp(ctx, api.Query(sub))
	if err != nil {
		s.Close()
		return nil, err
	}
	s.start(items, fence)
	return s, nil
}

func (h *Host) catchUp(ctx context.Context, q api.Query) ([]api.StreamItem, uint64, error) {
	if q.After.Log == h.store.LogID() {
		events, head, err := h.store.Since(ctx, q.Thread, q.After.Seq, replayEvents+1)
		if err != nil {
			return nil, 0, err
		}
		size := 0
		for _, e := range events {
			size += len(e.Payload)
		}
		if q.After.Seq <= head && len(events) <= replayEvents && size <= replayBytes {
			items := make([]api.StreamItem, len(events))
			for i := range events {
				items[i] = api.StreamItem{Kind: api.StreamEvent, Event: &events[i]}
			}
			return items, head, nil
		}
	}
	snap, err := h.store.Read(ctx, api.Query{Thread: q.Thread})
	if err != nil {
		return nil, 0, err
	}
	return []api.StreamItem{{Kind: api.StreamSnapshot, Snapshot: &snap}}, snap.Cursor.Seq, nil
}

type stream struct {
	h      *Host
	thread api.ID // zero follows every thread
	wake   chan struct{}

	mu    sync.Mutex
	ready bool
	tail  []api.Event // this stream's events committed while the catch-up was read
	items []api.StreamItem
	bytes int    // event payload held in tail and items
	last  uint64 // highest seq queued; anything at or below it is a duplicate
	err   error
}

func (s *stream) start(items []api.StreamItem, fence uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	tail := s.tail
	s.tail, s.items, s.bytes, s.last, s.ready = nil, items, 0, fence, true
	for _, it := range items {
		if it.Event != nil {
			s.bytes += len(it.Event.Payload)
		}
	}
	s.push(tail)
	s.items = append(s.items, api.StreamItem{Kind: api.StreamSynchronized})
	s.signal()
}

func (s *stream) publish(events []api.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	if s.ready {
		s.push(events)
	} else {
		for _, e := range events {
			if s.follows(e) {
				s.tail = append(s.tail, e)
				s.bytes += len(e.Payload)
			}
		}
		s.bound()
	}
	s.signal()
}

func (s *stream) push(events []api.Event) {
	for _, e := range events {
		if e.Cursor.Seq <= s.last || !s.follows(e) {
			continue
		}
		s.last = e.Cursor.Seq
		s.items = append(s.items, api.StreamItem{Kind: api.StreamEvent, Event: &e})
		s.bytes += len(e.Payload)
	}
	s.bound()
}

func (s *stream) follows(e api.Event) bool { return s.thread.IsZero() || e.Thread == s.thread }

func (s *stream) bound() {
	if s.bytes > streamBytes {
		s.err, s.items, s.tail = api.ErrOverflow, nil, nil
	}
}

func (s *stream) Next(ctx context.Context) (api.StreamItem, error) {
	for {
		s.mu.Lock()
		if s.err == nil && len(s.items) > 0 {
			it := s.items[0]
			s.items = s.items[1:]
			if it.Event != nil {
				s.bytes -= len(it.Event.Payload)
			}
			s.mu.Unlock()
			return it, nil
		}
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return api.StreamItem{}, err
		}
		select {
		case <-s.wake:
		case <-ctx.Done():
			return api.StreamItem{}, ctx.Err()
		}
	}
}

func (s *stream) Close() error {
	s.h.mu.Lock()
	delete(s.h.subs, s)
	s.h.mu.Unlock()
	s.fail(errStreamClosed)
	return nil
}

func (s *stream) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.signal()
}

func (s *stream) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
