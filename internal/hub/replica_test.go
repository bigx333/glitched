package hub

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
)

func openReplica(t *testing.T) *Replica {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// run builds events from..to of log, alternating between the given threads.
func run(log api.ID, from, to uint64, threads ...api.ID) []api.Event {
	var evs []api.Event
	for s := from; s <= to; s++ {
		evs = append(evs, api.Event{
			Cursor: api.Cursor{Log: log, Seq: s},
			Thread: threads[int(s)%len(threads)],
			Type:   "message.added",
			At:     time.Unix(int64(s), 0).UTC(),
		})
	}
	return evs
}

func seqs(evs []api.Event) []uint64 {
	var out []uint64
	for _, e := range evs {
		out = append(out, e.Cursor.Seq)
	}
	return out
}

func wantSeqs(t *testing.T, evs []api.Event, from, to uint64) {
	t.Helper()
	got := seqs(evs)
	if len(got) != int(to-from+1) {
		t.Fatalf("got seqs %v, want %d..%d", got, from, to)
	}
	for i, s := range got {
		if s != from+uint64(i) {
			t.Fatalf("got seqs %v, want %d..%d", got, from, to)
		}
	}
}

func TestAppendReplayIsIdempotentAndGapIsRefused(t *testing.T) {
	ctx := context.Background()
	r := openReplica(t)
	host, log, thread := api.NewID(), api.NewID(), api.NewID()

	ack, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: log, Events: run(log, 1, 5, thread)})
	if err != nil || ack.Acked != 5 {
		t.Fatalf("first upload: %v %v", ack, err)
	}
	ack, err = r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: log, Events: run(log, 3, 8, thread)})
	if err != nil || ack.Acked != 8 {
		t.Fatalf("overlapping upload: %v %v", ack, err)
	}
	ack, err = r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: log, Events: run(log, 1, 8, thread)})
	if err != nil || ack.Acked != 8 {
		t.Fatalf("replayed upload: %v %v", ack, err)
	}

	if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: log, Events: run(log, 8, 9, api.NewID())}); !errors.Is(err, ErrRewound) {
		t.Fatalf("upload whose seq 8 is another event: got %v, want ErrRewound", err)
	}

	_, err = r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: log, Events: run(log, 10, 12, thread)})
	var gap *GapError
	if !errors.As(err, &gap) || gap.Acked != 8 {
		t.Fatalf("gap upload: got %v, want GapError{8}", err)
	}

	evs, err := r.Events(ctx, api.ID{}, api.Cursor{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	wantSeqs(t, evs, 1, 8)
	if !evs[0].At.Equal(time.Unix(1, 0)) || evs[0].Thread != thread || evs[0].Type != "message.added" {
		t.Fatalf("event did not round trip: %+v", evs[0])
	}
}

func TestEpochFencing(t *testing.T) {
	ctx := context.Background()
	r := openReplica(t)
	host, log, thread := api.NewID(), api.NewID(), api.NewID()

	if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 2, Log: log, Events: run(log, 1, 3, thread)}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: log, Events: run(log, 4, 4, thread)})
	if !errors.Is(err, api.ErrStaleEpoch) {
		t.Fatalf("epoch 1 after 2: got %v, want ErrStaleEpoch", err)
	}

	// A restored host comes back at a higher epoch on a fresh log; its older
	// self is fenced from then on.
	restored := api.NewID()
	ack, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 3, Log: restored, Events: run(restored, 1, 2, thread)})
	if err != nil || ack.Acked != 2 {
		t.Fatalf("restored log: %v %v", ack, err)
	}
	if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 2, Log: log, Events: run(log, 4, 4, thread)}); !errors.Is(err, api.ErrStaleEpoch) {
		t.Fatalf("old self after restore: got %v, want ErrStaleEpoch", err)
	}
	if n, _ := r.Acked(ctx, log); n != 3 {
		t.Fatalf("old log acked %d, want 3", n)
	}

	other := api.NewID()
	if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: other, Epoch: 1, Log: api.NewID()}); err != nil {
		t.Fatalf("epochs are per host: %v", err)
	}
}

func TestALogBelongsToTheHostThatFirstAppendsIt(t *testing.T) {
	ctx := context.Background()
	r := openReplica(t)
	owner, other, log, thread := api.NewID(), api.NewID(), api.NewID(), api.NewID()

	if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: owner, Epoch: 1, Log: log, Events: run(log, 1, 2, thread)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: other, Epoch: 1, Log: log, Events: run(log, 3, 4, thread)}); !errors.Is(err, ErrForeignLog) {
		t.Fatalf("another host appending the log: got %v, want ErrForeignLog", err)
	}
	if n, _ := r.Acked(ctx, log); n != 2 {
		t.Fatalf("acked %d after the refused append, want 2", n)
	}
	if ack, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: owner, Epoch: 1, Log: log, Events: run(log, 3, 4, thread)}); err != nil || ack.Acked != 4 {
		t.Fatalf("owner continues its log: %v %v", ack, err)
	}
}

func TestThreadReadsAcrossLogs(t *testing.T) {
	ctx := context.Background()
	r := openReplica(t)
	host, a, b := api.NewID(), api.NewID(), api.NewID()
	log1, log2 := api.NewID(), api.NewID()

	if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: log1, Events: run(log1, 1, 6, a, b)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 2, Log: log2, Events: run(log2, 1, 4, a, b)}); err != nil {
		t.Fatal(err)
	}

	evs, err := r.Events(ctx, a, api.Cursor{Log: log1, Seq: 2}, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []api.Cursor{{Log: log1, Seq: 4}, {Log: log1, Seq: 6}, {Log: log2, Seq: 2}, {Log: log2, Seq: 4}}
	if len(evs) != len(want) {
		t.Fatalf("got %v, want %v", cursors(evs), want)
	}
	for i := range want {
		if evs[i].Cursor != want[i] || evs[i].Thread != a {
			t.Fatalf("got %v, want %v", cursors(evs), want)
		}
	}

	evs, err = r.Events(ctx, api.ID{}, api.Cursor{Log: log2}, 100)
	if err != nil {
		t.Fatal(err)
	}
	wantSeqs(t, evs, 1, 4)

	if _, err := r.Events(ctx, a, api.Cursor{Log: api.NewID(), Seq: 1}, 10); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("unknown log: got %v, want ErrNotFound", err)
	}
}

func cursors(evs []api.Event) []api.Cursor {
	var out []api.Cursor
	for _, e := range evs {
		out = append(out, e.Cursor)
	}
	return out
}

func TestFollowSeesLaterAppends(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := openReplica(t)
	host, log, a, b := api.NewID(), api.NewID(), api.NewID(), api.NewID()

	all, err := r.Follow(ctx, api.ID{}, api.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	onlyB, err := r.Follow(ctx, b, api.Cursor{})
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		for s := uint64(1); s <= 6; s++ {
			time.Sleep(5 * time.Millisecond)
			r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: log, Events: run(log, s, s, a, b)})
		}
	}()

	for want := uint64(1); want <= 6; want++ {
		e, err := all.Next(ctx)
		if err != nil || e.Cursor.Seq != want {
			t.Fatalf("all: got %v %v, want seq %d", e.Cursor, err, want)
		}
	}
	for _, want := range []uint64{1, 3, 5} {
		e, err := onlyB.Next(ctx)
		if err != nil || e.Cursor.Seq != want || e.Thread != b {
			t.Fatalf("thread b: got %v %v, want seq %d", e.Cursor, err, want)
		}
	}

	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	if _, err := onlyB.Next(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("idle feed: got %v, want deadline", err)
	}
}

// A thread's tombstone ends its own reads on the replica, as on the host;
// the whole log keeps every event, the tombstone included.
func TestADeletedThreadsReadsFindNothing(t *testing.T) {
	ctx := context.Background()
	r := openReplica(t)
	host, a, b := api.NewID(), api.NewID(), api.NewID()
	log := api.NewID()
	evs := run(log, 1, 4, a, b)
	evs = append(evs, api.Event{Cursor: api.Cursor{Log: log, Seq: 5}, Thread: a, Type: api.EventType(api.ThreadDeleted{}), At: time.Unix(5, 0).UTC()})
	if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: log, Events: evs}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Events(ctx, a, api.Cursor{}, 100); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("the deleted thread's events: %v, want ErrNotFound", err)
	}
	if got, err := r.Events(ctx, b, api.Cursor{}, 100); err != nil || len(got) != 2 {
		t.Fatalf("the other thread's events: %v %v, want its 2", seqs(got), err)
	}
	all, err := r.Events(ctx, api.ID{}, api.Cursor{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	wantSeqs(t, all, 1, 5)
}

func tombstone(log api.ID, seq uint64, thread api.ID) api.Event {
	return api.Event{Cursor: api.Cursor{Log: log, Seq: seq}, Thread: thread, Type: api.EventType(api.ThreadDeleted{}), At: time.Unix(int64(seq), 0).UTC()}
}

// A feed open when a thread's tombstone arrives reads the thread's events
// up to it, more than a page of them too, and then ends; one opened after
// is refused, as on the host.
func TestAFeedEndsAtItsThreadsTombstone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := openReplica(t)
	host, log, a, b := api.NewID(), api.NewID(), api.NewID(), api.NewID()
	if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: log, Events: run(log, 1, 2, a, b)}); err != nil {
		t.Fatal(err)
	}
	feed, err := r.Follow(ctx, a, api.Cursor{Log: log, Seq: 1})
	if err != nil {
		t.Fatal(err)
	}
	evs := append(run(log, 3, 600, a, b), tombstone(log, 601, a))
	if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: log, Events: evs}); err != nil {
		t.Fatal(err)
	}
	var got []api.Event
	for {
		e, err := feed.Next(ctx)
		if errors.Is(err, api.ErrNotFound) {
			break
		}
		if err != nil {
			t.Fatalf("after %d events: %v", len(got), err)
		}
		got = append(got, e)
	}
	if len(got) != 301 || got[0].Cursor.Seq != 2 || got[300].Type != "thread.deleted" {
		t.Fatalf("the feed read %d events, from %v to %v; want a's 300 and its tombstone", len(got), got[0].Cursor, got[len(got)-1].Cursor)
	}
	if _, err := r.Follow(ctx, a, api.Cursor{Log: log, Seq: 1}); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("following the deleted thread from before its tombstone: %v, want ErrNotFound", err)
	}
	if _, err := r.Events(ctx, a, api.Cursor{Log: log, Seq: 1}, 10); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("reading the deleted thread from before its tombstone: %v, want ErrNotFound", err)
	}
}

// A host restored from before a delete comes back on a new log holding the
// thread again. The tombstone stays the old log's: a read of the new log,
// or from a zero cursor, finds the thread; one of the old log does not.
func TestARestoredLogHoldsADeletedThreadAgain(t *testing.T) {
	ctx := context.Background()
	r := openReplica(t)
	host, a, b := api.NewID(), api.NewID(), api.NewID()
	old, restored := api.NewID(), api.NewID()
	if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: old, Events: append(run(old, 1, 4, a, b), tombstone(old, 5, a))}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 2, Log: restored, Events: run(restored, 1, 4, a, b)}); err != nil {
		t.Fatal(err)
	}
	for name, after := range map[string]api.Cursor{"the new log": {Log: restored}, "a zero cursor": {}} {
		evs, err := r.Events(ctx, a, after, 100)
		if err != nil {
			t.Fatalf("reading the restored thread from %s: %v", name, err)
		}
		if want := []api.Cursor{{Log: restored, Seq: 2}, {Log: restored, Seq: 4}}; !slices.Equal(cursors(evs), want) {
			t.Fatalf("reading the restored thread from %s: %v, want %v", name, cursors(evs), want)
		}
		feed, err := r.Follow(ctx, a, after)
		if err != nil {
			t.Fatalf("following the restored thread from %s: %v", name, err)
		}
		if e, err := feed.Next(ctx); err != nil || e.Cursor != (api.Cursor{Log: restored, Seq: 2}) {
			t.Fatalf("following the restored thread from %s: %v %v", name, e.Cursor, err)
		}
	}
	if _, err := r.Events(ctx, a, api.Cursor{Log: old, Seq: 1}, 100); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("reading the old log's deleted thread: %v, want ErrNotFound", err)
	}
}

// A host of protocol 1 sends no protocol, and its hub would read its log
// without the tombstones it lacks: the replica refuses it.
func TestAnUploadOfAnotherProtocolIsRefused(t *testing.T) {
	ctx := context.Background()
	r := openReplica(t)
	host, log := api.NewID(), api.NewID()
	if _, err := r.Append(ctx, api.Upload{Host: host, Epoch: 1, Log: log, Events: run(log, 1, 2, api.NewID())}); !errors.Is(err, api.ErrProtocol) {
		t.Fatalf("an upload of protocol 0: %v, want api.ErrProtocol", err)
	}
	if n, _ := r.Acked(ctx, log); n != 0 {
		t.Fatalf("the refused upload left %d events", n)
	}
	ack, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: log, Events: run(log, 1, 2, api.NewID())})
	if err != nil || ack.Protocol != api.Protocol {
		t.Fatalf("an upload of this protocol: %+v %v, want acked with the protocol", ack, err)
	}
}
