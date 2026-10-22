package hub

import (
	"context"
	"errors"
	"path/filepath"
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

	ack, err := r.Append(ctx, api.Upload{Host: host, Epoch: 1, Log: log, Events: run(log, 1, 5, thread)})
	if err != nil || ack.Acked != 5 {
		t.Fatalf("first upload: %v %v", ack, err)
	}
	ack, err = r.Append(ctx, api.Upload{Host: host, Epoch: 1, Log: log, Events: run(log, 3, 8, thread)})
	if err != nil || ack.Acked != 8 {
		t.Fatalf("overlapping upload: %v %v", ack, err)
	}
	ack, err = r.Append(ctx, api.Upload{Host: host, Epoch: 1, Log: log, Events: run(log, 1, 8, thread)})
	if err != nil || ack.Acked != 8 {
		t.Fatalf("replayed upload: %v %v", ack, err)
	}

	_, err = r.Append(ctx, api.Upload{Host: host, Epoch: 1, Log: log, Events: run(log, 10, 12, thread)})
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

	if _, err := r.Append(ctx, api.Upload{Host: host, Epoch: 2, Log: log, Events: run(log, 1, 3, thread)}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Append(ctx, api.Upload{Host: host, Epoch: 1, Log: log, Events: run(log, 4, 4, thread)})
	if !errors.Is(err, api.ErrStaleEpoch) {
		t.Fatalf("epoch 1 after 2: got %v, want ErrStaleEpoch", err)
	}

	// A restored host comes back at a higher epoch on a fresh log; its older
	// self is fenced from then on.
	restored := api.NewID()
	ack, err := r.Append(ctx, api.Upload{Host: host, Epoch: 3, Log: restored, Events: run(restored, 1, 2, thread)})
	if err != nil || ack.Acked != 2 {
		t.Fatalf("restored log: %v %v", ack, err)
	}
	if _, err := r.Append(ctx, api.Upload{Host: host, Epoch: 2, Log: log, Events: run(log, 4, 4, thread)}); !errors.Is(err, api.ErrStaleEpoch) {
		t.Fatalf("old self after restore: got %v, want ErrStaleEpoch", err)
	}
	if n, _ := r.Acked(ctx, log); n != 3 {
		t.Fatalf("old log acked %d, want 3", n)
	}

	other := api.NewID()
	if _, err := r.Append(ctx, api.Upload{Host: other, Epoch: 1, Log: api.NewID()}); err != nil {
		t.Fatalf("epochs are per host: %v", err)
	}
}

func TestALogBelongsToTheHostThatFirstAppendsIt(t *testing.T) {
	ctx := context.Background()
	r := openReplica(t)
	owner, other, log, thread := api.NewID(), api.NewID(), api.NewID(), api.NewID()

	if _, err := r.Append(ctx, api.Upload{Host: owner, Epoch: 1, Log: log, Events: run(log, 1, 2, thread)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Append(ctx, api.Upload{Host: other, Epoch: 1, Log: log, Events: run(log, 3, 4, thread)}); !errors.Is(err, ErrForeignLog) {
		t.Fatalf("another host appending the log: got %v, want ErrForeignLog", err)
	}
	if n, _ := r.Acked(ctx, log); n != 2 {
		t.Fatalf("acked %d after the refused append, want 2", n)
	}
	if ack, err := r.Append(ctx, api.Upload{Host: owner, Epoch: 1, Log: log, Events: run(log, 3, 4, thread)}); err != nil || ack.Acked != 4 {
		t.Fatalf("owner continues its log: %v %v", ack, err)
	}
}

func TestThreadReadsAcrossLogs(t *testing.T) {
	ctx := context.Background()
	r := openReplica(t)
	host, a, b := api.NewID(), api.NewID(), api.NewID()
	log1, log2 := api.NewID(), api.NewID()

	if _, err := r.Append(ctx, api.Upload{Host: host, Epoch: 1, Log: log1, Events: run(log1, 1, 6, a, b)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Append(ctx, api.Upload{Host: host, Epoch: 2, Log: log2, Events: run(log2, 1, 4, a, b)}); err != nil {
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
			r.Append(ctx, api.Upload{Host: host, Epoch: 1, Log: log, Events: run(log, s, s, a, b)})
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
