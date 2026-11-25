package runtime

import (
	"context"
	"errors"
	"sync"

	"glitched/internal/api"
	"glitched/internal/store"
)

const (
	replayEvents = 128
	replayBytes  = 1 << 20
	streamBytes  = 8 << 20
)

var errStreamClosed = errors.New("runtime: stream closed")

// Open sends the catch-up (a replay from the client's cursor, or a snapshot
// at a fence), then whatever committed meanwhile, then StreamSynchronized,
// then live items. A thread subscription gets the thread's events, and ends
// with api.ErrNotFound after its tombstone; opening one on a deleted thread
// is api.ErrNotFound. The shell gets one StreamThread per thread a commit
// moved, and always catches up from a snapshot: it holds no events to replay.
func (h *Host) Open(ctx context.Context, sub api.Subscription) (api.Stream, error) {
	if link, ok := api.LinkOf(ctx); ok {
		if _, err := h.owned(ctx, link, sub.Thread); err != nil {
			return nil, err
		}
	}
	s := &stream{h: h, thread: sub.Thread, views: sub.Views && !sub.Thread.IsZero(), wake: make(chan struct{}, 1)}
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
	if !q.Thread.IsZero() && q.After.Log == h.store.LogID() {
		events, head, view, err := h.store.SinceView(ctx, q.Thread, q.After.Seq, replayEvents+1)
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
			if q.Views && view.ID == q.Thread {
				items = append(items, api.StreamItem{Kind: api.StreamThread, Thread: &view})
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
	thread api.ID // zero is the shell
	views  bool   // a thread's view follows its events, as Query.Views says
	wake   chan struct{}

	mu    sync.Mutex
	ready bool
	tail  []batch // this stream's part of what committed while the catch-up was read
	items []api.StreamItem
	bytes int    // held in tail and items
	last  uint64 // head of the last commit queued; a commit at or below it is a duplicate
	over  bool   // the thread's tombstone is queued: nothing follows the items
	err   error
}

// batch is one commit's items for a stream. A commit lands whole on either
// side of a catch-up's fence, so its head alone tells a duplicate.
type batch struct {
	head  uint64
	items []api.StreamItem
}

func (s *stream) start(items []api.StreamItem, fence uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	tail := s.tail
	s.tail, s.items, s.bytes, s.last, s.ready = nil, nil, 0, fence, true
	s.queue(items)
	for _, b := range tail {
		s.push(b)
	}
	s.items = append(s.items, api.StreamItem{Kind: api.StreamSynchronized})
	s.signal()
}

func (s *stream) publish(c store.Commit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	b := s.batchOf(c)
	if len(b.items) == 0 {
		return
	}
	if s.ready {
		s.push(b)
	} else {
		s.tail = append(s.tail, b)
		for _, it := range b.items {
			s.bytes += size(it)
		}
		s.bound()
	}
	s.signal()
}

func (s *stream) batchOf(c store.Commit) batch {
	b := batch{head: c.Events[len(c.Events)-1].Cursor.Seq}
	if s.thread.IsZero() {
		for i := range c.Threads {
			b.items = append(b.items, api.StreamItem{Kind: api.StreamThread, Thread: &c.Threads[i]})
		}
		return b
	}
	for i := range c.Events {
		if c.Events[i].Thread == s.thread {
			b.items = append(b.items, api.StreamItem{Kind: api.StreamEvent, Event: &c.Events[i]})
		}
	}
	if s.views && len(b.items) > 0 {
		for i := range c.Threads {
			if c.Threads[i].ID == s.thread {
				b.items = append(b.items, api.StreamItem{Kind: api.StreamThread, Thread: &c.Threads[i]})
			}
		}
	}
	return b
}

func (s *stream) push(b batch) {
	if b.head <= s.last {
		return
	}
	s.last = b.head
	s.queue(b.items)
	s.bound()
}

func (s *stream) queue(items []api.StreamItem) {
	s.items = append(s.items, items...)
	for _, it := range items {
		s.bytes += size(it)
		if it.Event != nil && it.Event.Type == api.EventType(api.ThreadDeleted{}) {
			s.over = true
		}
	}
}

// viewOverhead stands in for a view's fixed-size fields; its strings and
// request bodies are counted as they are.
const viewOverhead = 256

func size(it api.StreamItem) int {
	switch {
	case it.Event != nil:
		return len(it.Event.Payload)
	case it.Thread != nil:
		n := viewOverhead + len(it.Thread.Title) + len(it.Thread.Workspace)
		for _, r := range it.Thread.Requests {
			n += len(r.Body)
		}
		return n
	}
	return 0
}

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
			s.bytes -= size(it)
			s.mu.Unlock()
			return it, nil
		}
		err := s.err
		if err == nil && s.over {
			err = api.ErrNotFound
		}
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
