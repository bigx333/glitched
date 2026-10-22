package hub

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
)

// fakeLog stands in for the host store: a committed log with a dense seq.
type fakeLog struct {
	mu     sync.Mutex
	id     api.ID
	events []api.Event
}

func newFakeLog() *fakeLog { return &fakeLog{id: api.NewID()} }

func (l *fakeLog) commit(n int, thread api.ID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	next := uint64(len(l.events)) + 1
	l.events = append(l.events, run(l.id, next, next+uint64(n)-1, thread)...)
}

// restore swaps in a database restored from a backup holding the first keep
// events: a new log ID, its seq starting over.
func (l *fakeLog) restore(keep int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.id = api.NewID()
	l.events = run(l.id, 1, uint64(keep), l.events[0].Thread)
}

// truncate is a restore the host's fence missed, because the backup held the
// fence too: the log keeps its ID with only its first keep events.
func (l *fakeLog) truncate(keep int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = l.events[:keep]
}

func (l *fakeLog) LogID() api.ID {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.id
}

func (l *fakeLog) ReadLog(ctx context.Context, after uint64, limit int) ([]api.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if after >= uint64(len(l.events)) {
		return nil, nil
	}
	evs := l.events[after:]
	return append([]api.Event(nil), evs[:min(limit, len(evs))]...), nil
}

// lossyHub commits the upload, then loses the reply on the calls lose picks.
type lossyHub struct {
	*Replica
	mu    sync.Mutex
	calls int
	lose  func(call int) error
}

func (h *lossyHub) Append(ctx context.Context, u api.Upload) (api.UploadAck, error) {
	ack, err := h.Replica.Append(ctx, u)
	h.mu.Lock()
	h.calls++
	call := h.calls
	h.mu.Unlock()
	if err == nil {
		if lost := h.lose(call); lost != nil {
			return api.UploadAck{}, lost
		}
	}
	return ack, err
}

func newTestUploader(src LogSource, hub Client) *Uploader {
	u := NewUploader(api.NewID(), 1, src, hub)
	u.Batch, u.Interval, u.MaxDelay = 3, time.Hour, 20*time.Millisecond
	return u
}

func waitAcked(t *testing.T, r *Replica, log api.ID, want uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n, err := r.Acked(context.Background(), log)
		if err != nil {
			t.Fatal(err)
		}
		if n == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("acked %d, want %d", n, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func wantReplica(t *testing.T, r *Replica, log api.ID, n uint64) {
	t.Helper()
	all, err := r.Events(context.Background(), api.ID{}, api.Cursor{}, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	var evs []api.Event
	for _, e := range all {
		if e.Cursor.Log == log {
			evs = append(evs, e)
		}
	}
	wantSeqs(t, evs, 1, n)
}

func TestUploaderKilledAfterHubCommitResumesWithoutGapOrDuplicate(t *testing.T) {
	r := openReplica(t)
	src := newFakeLog()
	src.commit(10, api.NewID())

	ctx, kill := context.WithCancel(context.Background())
	hub := &lossyHub{Replica: r, lose: func(call int) error {
		if call == 2 {
			kill()
			return context.Canceled
		}
		return nil
	}}
	u := newTestUploader(src, hub)
	if err := u.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("killed uploader returned %v", err)
	}
	waitAcked(t, r, src.LogID(), 6)

	src.commit(5, api.NewID())
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	restarted := NewUploader(u.Host, u.Epoch, src, r)
	go restarted.Run(ctx)
	waitAcked(t, r, src.LogID(), 15)
	wantReplica(t, r, src.LogID(), 15)
}

func TestUploaderRetriesLostReplies(t *testing.T) {
	r := openReplica(t)
	src := newFakeLog()
	src.commit(20, api.NewID())
	hub := &lossyHub{Replica: r, lose: func(call int) error {
		if call%2 == 0 {
			return errors.New("connection reset")
		}
		return nil
	}}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go newTestUploader(src, hub).Run(ctx)
	waitAcked(t, r, src.LogID(), 20)
	wantReplica(t, r, src.LogID(), 20)
}

func TestUploaderWakesOnNotifyAndFollowsARestoredLog(t *testing.T) {
	r := openReplica(t)
	src := newFakeLog()
	src.commit(4, api.NewID())
	u := newTestUploader(src, r)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go u.Run(ctx)
	waitAcked(t, r, src.LogID(), 4)

	src.commit(3, api.NewID())
	u.Notify()
	waitAcked(t, r, src.LogID(), 7)

	old := src.LogID()
	src.restore(5)
	src.commit(1, api.NewID())
	u.Notify()
	waitAcked(t, r, src.LogID(), 6)
	wantReplica(t, r, src.LogID(), 6)
	wantReplica(t, r, old, 7)
}

func TestUploaderStopsWhenFenced(t *testing.T) {
	r := openReplica(t)
	src := newFakeLog()
	src.commit(2, api.NewID())
	u := newTestUploader(src, r)
	if _, err := r.Append(context.Background(), api.Upload{Host: u.Host, Epoch: u.Epoch + 1, Log: api.NewID()}); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := u.Run(ctx); !errors.Is(err, api.ErrStaleEpoch) {
		t.Fatalf("fenced uploader returned %v, want ErrStaleEpoch", err)
	}
}

// failingHub refuses the first appends with errs, in order.
type failingHub struct {
	*Replica
	mu   sync.Mutex
	errs []error
}

func (h *failingHub) Append(ctx context.Context, u api.Upload) (api.UploadAck, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.errs) > 0 {
		err := h.errs[0]
		h.errs = h.errs[1:]
		return api.UploadAck{}, err
	}
	return h.Replica.Append(ctx, u)
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestUploaderLogsEachNewErrorAndTheRecovery(t *testing.T) {
	r := openReplica(t)
	src := newFakeLog()
	src.commit(6, api.NewID())
	refused, reset := errors.New("hub refused"), errors.New("connection reset")
	hub := &failingHub{Replica: r, errs: []error{refused, refused, refused, reset, reset}}
	var out syncBuffer
	u := newTestUploader(src, hub)
	u.Logger = slog.New(slog.NewTextHandler(&out, nil))
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go u.Run(ctx)
	waitAcked(t, r, src.LogID(), 6)

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "recovered") {
		if time.Now().After(deadline) {
			t.Fatalf("no recovery logged:\n%s", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	logged := out.String()
	for _, want := range []string{refused.Error(), reset.Error(), "recovered"} {
		if n := strings.Count(logged, want); n != 1 {
			t.Fatalf("%q logged %d times, want once:\n%s", want, n, logged)
		}
	}
}

func TestUploaderStopsWhenItsLogBelongsToAnotherHost(t *testing.T) {
	r := openReplica(t)
	src := newFakeLog()
	src.commit(2, api.NewID())
	u := newTestUploader(src, r)
	first, err := src.ReadLog(context.Background(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Append(context.Background(), api.Upload{Host: api.NewID(), Epoch: 1, Log: src.LogID(), Events: first}); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := u.Run(ctx); !errors.Is(err, ErrForeignLog) {
		t.Fatalf("uploader of a foreign log returned %v, want ErrForeignLog", err)
	}
}

func TestUploaderStopsWhenTheHubHoldsMoreOfItsLog(t *testing.T) {
	r := openReplica(t)
	src := newFakeLog()
	src.commit(5, api.NewID())
	u := newTestUploader(src, r)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- u.Run(ctx) }()
	waitAcked(t, r, src.LogID(), 5)
	stop()
	<-done

	src.truncate(3)
	src.commit(1, api.NewID())
	ctx, stop = context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := NewUploader(u.Host, u.Epoch, src, r).Run(ctx); !errors.Is(err, ErrRewound) {
		t.Fatalf("uploader of a log the hub holds more of returned %v, want ErrRewound", err)
	}
	if err := u.Check(ctx); !errors.Is(err, ErrRewound) {
		t.Fatalf("Check: %v, want ErrRewound", err)
	}
}
