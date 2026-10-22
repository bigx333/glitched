package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/hub"
)

func start(t *testing.T, network string, srv *Server) Loopback {
	t.Helper()
	addr := "127.0.0.1:0"
	if network == "unix" {
		addr = filepath.Join(t.TempDir(), "g.sock")
	}
	ln, err := Loopback{Network: network, Address: addr}.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lb := Loopback{Network: network, Address: ln.Addr().String()}
	if srv.Transport == nil {
		srv.Transport = lb
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return lb
}

// fakeEndpoint answers Submit and Open for a target in errs with that error;
// it is read-only once the server starts. closed, when set, gets the thread
// of each stream the server closes; pulled counts the items its streams gave
// the server.
type fakeEndpoint struct {
	env    api.Environment
	errs   map[api.ID]error
	closed chan api.ID
	pulled atomic.Int64
}

func (f *fakeEndpoint) Info(context.Context) (api.Environment, error) { return f.env, nil }

func (f *fakeEndpoint) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	if q.Thread == blocks {
		<-ctx.Done()
		return api.Snapshot{}, ctx.Err()
	}
	return api.Snapshot{Cursor: q.After, Threads: []api.Thread{{ID: q.Thread, Title: "read"}}}, nil
}

func (f *fakeEndpoint) Submit(_ context.Context, c api.Command) (api.Receipt, error) {
	if err := f.errs[c.Target]; err != nil {
		return api.Receipt{}, err
	}
	return api.Receipt{Command: c.ID, Committed: api.Cursor{Log: f.env.Log, Seq: 7}}, nil
}

// Open streams a snapshot, three events and the synchronized marker, then
// stays open. A target in errs fails the open; endsBadly fails the stream.
func (f *fakeEndpoint) Open(_ context.Context, s api.Subscription) (api.Stream, error) {
	if err := f.errs[s.Thread]; err != nil {
		return nil, err
	}
	items := []api.StreamItem{{Kind: api.StreamSnapshot, Snapshot: &api.Snapshot{Cursor: s.After}}}
	for seq := s.After.Seq + 1; seq <= s.After.Seq+3; seq++ {
		items = append(items, api.StreamItem{Kind: api.StreamEvent, Event: &api.Event{Cursor: api.Cursor{Log: f.env.Log, Seq: seq}, Thread: s.Thread}})
	}
	items = append(items, api.StreamItem{Kind: api.StreamSynchronized})
	if s.Thread == firehose {
		payload := json.RawMessage(`"` + strings.Repeat("x", 64<<10) + `"`)
		items = items[:0]
		for seq := range uint64(200) {
			items = append(items, api.StreamItem{Kind: api.StreamEvent, Event: &api.Event{Cursor: api.Cursor{Log: f.env.Log, Seq: seq + 1}, Thread: s.Thread, Payload: payload}})
		}
	}
	st := &fakeStream{items: items, thread: s.Thread, closed: f.closed, pulled: &f.pulled}
	if s.Thread == endsBadly {
		st.end = api.ErrOverflow
	}
	return st, nil
}

// endsBadly's stream fails after its items; firehose's is 200 items of 64 KiB,
// three times the stream window; a Read of blocks waits for its ctx.
var endsBadly, firehose, blocks = api.NewID(), api.NewID(), api.NewID()

type fakeStream struct {
	items  []api.StreamItem
	end    error
	thread api.ID
	closed chan api.ID
	pulled *atomic.Int64
}

func (s *fakeStream) Next(ctx context.Context) (api.StreamItem, error) {
	if len(s.items) > 0 {
		it := s.items[0]
		s.items = s.items[1:]
		if s.pulled != nil {
			s.pulled.Add(1)
		}
		return it, nil
	}
	if s.end != nil {
		return api.StreamItem{}, s.end
	}
	<-ctx.Done()
	return api.StreamItem{}, ctx.Err()
}

func (s *fakeStream) Close() error {
	select {
	case s.closed <- s.thread:
	default:
	}
	return nil
}

func TestEndpointRoundTrip(t *testing.T) {
	moved, rejected, wrapped, opaque := api.NewID(), api.NewID(), api.NewID(), api.NewID()
	movedTo := api.NewID()
	sentinels := []error{api.ErrConflict, api.ErrRevision, api.ErrExpired, api.ErrNotFound, api.ErrStaleEpoch, api.ErrAlreadyAnswered, api.ErrOverflow}
	errs := map[api.ID]error{
		moved:    &api.MovedError{To: movedTo, Epoch: 4},
		rejected: &api.RejectedError{Reason: "thread is archived"},
		wrapped:  fmt.Errorf("engine: %w", api.ErrRevision),
		opaque:   errors.New("disk on fire"),
	}
	targets := map[error]api.ID{}
	for _, s := range sentinels {
		targets[s] = api.NewID()
		errs[targets[s]] = s
	}
	fake := &fakeEndpoint{env: api.Environment{ID: api.NewID(), Name: "mac", Protocol: 1, Log: api.NewID()}, errs: errs}

	for _, network := range []string{"unix", "tcp"} {
		t.Run(network, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c := NewEndpointClient(start(t, network, &Server{Endpoint: fake}), fake.env.ID)

			env, err := c.Info(ctx)
			if err != nil || env != fake.env {
				t.Fatalf("info: %+v %v", env, err)
			}

			thread := api.NewID()
			after := api.Cursor{Log: fake.env.Log, Seq: 3}
			snap, err := c.Read(ctx, api.Query{Thread: thread, After: after})
			if err != nil || snap.Cursor != after || len(snap.Threads) != 1 || snap.Threads[0].ID != thread {
				t.Fatalf("read: %+v %v", snap, err)
			}

			cmd := api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "hi"}}
			rcpt, err := c.Submit(ctx, cmd)
			if err != nil || rcpt.Command != cmd.ID || rcpt.Committed.Seq != 7 {
				t.Fatalf("submit: %+v %v", rcpt, err)
			}

			submit := func(target api.ID) error {
				_, err := c.Submit(ctx, api.Command{ID: api.NewID(), Target: target, Body: api.TurnInterrupt{}})
				return err
			}
			for _, s := range sentinels {
				if err := submit(targets[s]); !errors.Is(err, s) {
					t.Errorf("sent %v, got %v", s, err)
				}
			}
			if err := submit(wrapped); !errors.Is(err, api.ErrRevision) {
				t.Errorf("sent wrapped ErrRevision, got %v", err)
			}
			var me *api.MovedError
			if err := submit(moved); !errors.As(err, &me) || me.To != movedTo || me.Epoch != 4 {
				t.Errorf("sent MovedError, got %v", err)
			}
			var re *api.RejectedError
			if err := submit(rejected); !errors.As(err, &re) || re.Reason != "thread is archived" {
				t.Errorf("sent RejectedError, got %v", err)
			}
			if err := submit(opaque); err == nil || errors.As(err, &me) || errors.As(err, &re) || errors.Is(err, api.ErrNotFound) {
				t.Errorf("sent an opaque error, got %v", err)
			}

			st, err := c.Open(ctx, api.Subscription{Thread: thread, After: after})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			wantKinds := []api.StreamKind{api.StreamSnapshot, api.StreamEvent, api.StreamEvent, api.StreamEvent, api.StreamSynchronized}
			for i, k := range wantKinds {
				it, err := st.Next(ctx)
				if err != nil || it.Kind != k {
					t.Fatalf("item %d: %+v %v, want kind %d", i, it, err, k)
				}
				if k == api.StreamEvent && (it.Event.Cursor.Seq != after.Seq+uint64(i) || it.Event.Thread != thread) {
					t.Fatalf("item %d: event %+v", i, it.Event)
				}
				if k == api.StreamSnapshot && it.Snapshot.Cursor != after {
					t.Fatalf("snapshot cursor %v", it.Snapshot.Cursor)
				}
			}
			idle, stop := context.WithTimeout(ctx, 50*time.Millisecond)
			defer stop()
			if _, err := st.Next(idle); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("idle stream: got %v, want deadline", err)
			}

			if _, err := c.Open(ctx, api.Subscription{Thread: moved}); !errors.As(err, &me) || me.To != movedTo {
				t.Fatalf("open on a moved thread: got %v", err)
			}
			bad, err := c.Open(ctx, api.Subscription{Thread: endsBadly})
			if err != nil {
				t.Fatal(err)
			}
			defer bad.Close()
			for range wantKinds {
				if _, err := bad.Next(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := bad.Next(ctx); !errors.Is(err, api.ErrOverflow) {
				t.Fatalf("stream end: got %v, want ErrOverflow", err)
			}
		})
	}
}

type refuseAll struct{ Loopback }

func (refuseAll) WhoIs(net.Conn) (Peer, error) { return Peer{}, errors.New("not enrolled") }

func TestServerRefusesWhatWhoIsRejects(t *testing.T) {
	fake := &fakeEndpoint{env: api.Environment{Name: "mac"}}
	lb := start(t, "tcp", &Server{Transport: refuseAll{}, Endpoint: fake})
	if _, err := NewEndpointClient(lb, api.ID{}).Info(context.Background()); err == nil {
		t.Fatal("a peer WhoIs rejects was served")
	}
}

func openReplica(t *testing.T) *hub.Replica {
	t.Helper()
	r, err := hub.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func events(log, thread api.ID, from, to uint64) []api.Event {
	var evs []api.Event
	for s := from; s <= to; s++ {
		evs = append(evs, api.Event{Cursor: api.Cursor{Log: log, Seq: s}, Thread: thread, Type: "message.added"})
	}
	return evs
}

func TestHubOverTheWire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := openReplica(t)
	c := NewHubClient(start(t, "unix", &Server{Hub: r}), api.ID{})
	host, log, a, b := api.NewID(), api.NewID(), api.NewID(), api.NewID()

	feed, err := c.Follow(ctx, a, api.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Close()

	if ack, err := c.Append(ctx, api.Upload{Host: host, Epoch: 2, Log: log, Events: events(log, a, 1, 3)}); err != nil || ack.Acked != 3 {
		t.Fatalf("append: %v %v", ack, err)
	}
	if ack, err := c.Append(ctx, api.Upload{Host: host, Epoch: 2, Log: log, Events: events(log, b, 4, 5)}); err != nil || ack.Acked != 5 {
		t.Fatalf("append: %v %v", ack, err)
	}
	if ack, err := c.Append(ctx, api.Upload{Host: host, Epoch: 2, Log: log, Events: events(log, a, 6, 6)}); err != nil || ack.Acked != 6 {
		t.Fatalf("append: %v %v", ack, err)
	}
	for _, want := range []uint64{1, 2, 3, 6} {
		e, err := feed.Next(ctx)
		if err != nil || e.Cursor.Seq != want || e.Thread != a {
			t.Fatalf("feed: got %+v %v, want seq %d", e.Cursor, err, want)
		}
	}

	var gap *hub.GapError
	if _, err := c.Append(ctx, api.Upload{Host: host, Epoch: 2, Log: log, Events: events(log, a, 9, 9)}); !errors.As(err, &gap) || gap.Acked != 6 {
		t.Fatalf("gap: got %v, want GapError{6}", err)
	}
	if _, err := c.Append(ctx, api.Upload{Host: host, Epoch: 1, Log: log, Events: events(log, a, 7, 7)}); !errors.Is(err, api.ErrStaleEpoch) {
		t.Fatalf("stale epoch: got %v", err)
	}
	if _, err := c.Append(ctx, api.Upload{Host: api.NewID(), Epoch: 2, Log: log, Events: events(log, a, 7, 7)}); !errors.Is(err, hub.ErrForeignLog) {
		t.Fatalf("another host's append: got %v, want ErrForeignLog", err)
	}
	if _, err := c.Append(ctx, api.Upload{Host: host, Epoch: 2, Log: log, Events: events(log, b, 6, 7)}); !errors.Is(err, hub.ErrRewound) {
		t.Fatalf("append whose seq 6 is another event: got %v, want ErrRewound", err)
	}
	if n, err := c.Acked(ctx, log); err != nil || n != 6 {
		t.Fatalf("acked: %d %v", n, err)
	}
	evs, err := c.Events(ctx, b, api.Cursor{}, 0)
	if err != nil || len(evs) != 2 || evs[0].Cursor.Seq != 4 || evs[1].Cursor.Seq != 5 {
		t.Fatalf("thread b: %v %v", evs, err)
	}
	if _, err := c.Events(ctx, a, api.Cursor{Log: api.NewID(), Seq: 1}, 0); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("unknown log: got %v", err)
	}
}

type fakeLog struct {
	mu     sync.Mutex
	id     api.ID
	events []api.Event
}

func (l *fakeLog) LogID() api.ID { return l.id }

func (l *fakeLog) ReadLog(_ context.Context, after uint64, limit int) ([]api.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if after >= uint64(len(l.events)) {
		return nil, nil
	}
	evs := l.events[after:]
	return append([]api.Event(nil), evs[:min(limit, len(evs))]...), nil
}

// dropAfterCommit cuts the connection under the first response that arrives
// once the replica holds seq at, so the hub has committed an upload whose
// ack the host never reads.
type dropAfterCommit struct {
	Loopback
	r       *hub.Replica
	log     api.ID
	at      uint64
	dropped atomic.Bool
}

func (d *dropAfterCommit) Dial(ctx context.Context, m api.ID) (net.Conn, error) {
	c, err := d.Loopback.Dial(ctx, m)
	return &dropConn{Conn: c, d: d}, err
}

type dropConn struct {
	net.Conn
	d *dropAfterCommit
}

func (c *dropConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && !c.d.dropped.Load() {
		if acked, _ := c.d.r.Acked(context.Background(), c.d.log); acked >= c.d.at && c.d.dropped.CompareAndSwap(false, true) {
			c.Conn.Close()
			return 0, io.ErrUnexpectedEOF
		}
	}
	return n, err
}

func TestUploadSurvivesAConnectionDroppedAfterCommit(t *testing.T) {
	r := openReplica(t)
	src := &fakeLog{id: api.NewID()}
	src.events = events(src.id, api.NewID(), 1, 12)
	drop := &dropAfterCommit{Loopback: start(t, "tcp", &Server{Hub: r}), r: r, log: src.id, at: 4}

	u := hub.NewUploader(api.NewID(), 1, src, NewHubClient(drop, api.ID{}))
	u.Batch, u.MaxDelay = 4, 20*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- u.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for n, _ := r.Acked(ctx, src.id); n < 12; n, _ = r.Acked(ctx, src.id) {
		if time.Now().After(deadline) {
			t.Fatalf("acked %d, want 12", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if !drop.dropped.Load() {
		t.Fatal("the connection was never dropped")
	}
	evs, err := r.Events(context.Background(), api.ID{}, api.Cursor{}, 100)
	if err != nil || len(evs) != 12 {
		t.Fatalf("replica: %d events, %v", len(evs), err)
	}
	for i, e := range evs {
		if e.Cursor.Seq != uint64(i+1) {
			t.Fatalf("replica seq %d at %d", e.Cursor.Seq, i)
		}
	}
}

func TestUploaderBatchesFitTheServerBodyLimit(t *testing.T) {
	r := openReplica(t)
	src := &fakeLog{id: api.NewID()}
	src.events = events(src.id, api.NewID(), 1, 80)
	payload := json.RawMessage(`"` + strings.Repeat("x", 1<<20) + `"`)
	for i := range src.events {
		src.events[i].Payload = payload
	}
	u := hub.NewUploader(api.NewID(), 1, src, NewHubClient(start(t, "unix", &Server{Hub: r}), api.ID{}))
	u.MaxDelay = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- u.Run(ctx) }()
	defer func() { cancel(); <-done }()

	// The whole backlog is over the server's body limit; replicating more than
	// two batches' worth shows the uploader splits it. All 80 is slow under -race.
	deadline := time.Now().Add(20 * time.Second)
	for n, _ := r.Acked(ctx, src.id); n < 20; n, _ = r.Acked(ctx, src.id) {
		if time.Now().After(deadline) {
			t.Fatalf("acked %d of 80 MiB-sized events: a batch is over the server's body limit", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// dialer counts the sockets a client dials and can cut them.
type dialer struct {
	Loopback
	mu    sync.Mutex
	conns []net.Conn
}

func (d *dialer) Dial(ctx context.Context, m api.ID) (net.Conn, error) {
	c, err := d.Loopback.Dial(ctx, m)
	if err == nil {
		d.mu.Lock()
		d.conns = append(d.conns, c)
		d.mu.Unlock()
	}
	return c, err
}

func (d *dialer) dialed() []net.Conn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]net.Conn(nil), d.conns...)
}

func readStream(ctx context.Context, st api.Stream, thread api.ID, after api.Cursor) error {
	want := []api.StreamKind{api.StreamSnapshot, api.StreamEvent, api.StreamEvent, api.StreamEvent, api.StreamSynchronized}
	for i, k := range want {
		it, err := st.Next(ctx)
		if err != nil {
			return err
		}
		if it.Kind != k || (k == api.StreamEvent && (it.Event.Thread != thread || it.Event.Cursor.Seq != after.Seq+uint64(i))) {
			return fmt.Errorf("item %d: %+v, want kind %d of thread %v", i, it, k, thread)
		}
	}
	return nil
}

func TestCallsAndStreamsShareOneSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fake := &fakeEndpoint{env: api.Environment{ID: api.NewID(), Log: api.NewID()}}
	d := &dialer{Loopback: start(t, "unix", &Server{Endpoint: fake})}
	c := NewEndpointClient(d, fake.env.ID)

	a, b := api.NewID(), api.NewID()
	sa, err := c.Open(ctx, api.Subscription{Thread: a, After: api.Cursor{Log: fake.env.Log, Seq: 10}})
	if err != nil {
		t.Fatal(err)
	}
	defer sa.Close()
	sb, err := c.Open(ctx, api.Subscription{Thread: b, After: api.Cursor{Log: fake.env.Log, Seq: 20}})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 102)
	for range 100 {
		wg.Go(func() {
			cmd := api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.TurnInterrupt{}}
			r, err := c.Submit(ctx, cmd)
			if err == nil && r.Command != cmd.ID {
				err = fmt.Errorf("submit %v answered for %v", cmd.ID, r.Command)
			}
			errs <- err
		})
	}
	wg.Go(func() { errs <- readStream(ctx, sa, a, api.Cursor{Seq: 10}) })
	wg.Go(func() { errs <- readStream(ctx, sb, b, api.Cursor{Seq: 20}) })
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := len(d.dialed()); n != 1 {
		t.Fatalf("dialed %d sockets, want 1", n)
	}
}

func TestUnreadStreamWaitsAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fake := &fakeEndpoint{env: api.Environment{ID: api.NewID(), Name: "mac", Log: api.NewID()}}
	d := &dialer{Loopback: start(t, "tcp", &Server{Endpoint: fake})}
	c := NewEndpointClient(d, fake.env.ID)

	slow, err := c.Open(ctx, api.Subscription{Thread: firehose})
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	thread := api.NewID()
	other := mustOpen(t, ctx, c, thread)
	if err := readStream(ctx, other, thread, api.Cursor{}); err != nil {
		t.Fatal(err)
	}
	if env, err := c.Info(ctx); err != nil || env != fake.env {
		t.Fatalf("info beside an unread stream: %+v %v", env, err)
	}

	// The server sends 64 KiB items until a window's worth is unacked. The
	// other stream took five.
	time.Sleep(100 * time.Millisecond)
	if n, most := fake.pulled.Load()-5, int64(streamWindow/(64<<10)+1); n > most {
		t.Fatalf("the server pulled %d firehose items for a reader that took none, want at most %d", n, most)
	}
	for i := range 200 {
		it, err := slow.Next(ctx)
		if err != nil || it.Event.Cursor.Seq != uint64(i+1) {
			t.Fatalf("firehose item %d: %+v %v", i, it.Event, err)
		}
	}
	if n := len(d.dialed()); n != 1 {
		t.Fatalf("dialed %d sockets, want 1", n)
	}
}

// bigSnapshot opens every stream as the runtime does for a far-behind cursor:
// one Snapshot item holding a 9 MiB thread, then Synchronized.
type bigSnapshot struct{ fakeEndpoint }

const bigTitle = 9 << 20

func (b *bigSnapshot) Open(context.Context, api.Subscription) (api.Stream, error) {
	snap := api.Snapshot{Threads: []api.Thread{{ID: api.NewID(), Title: strings.Repeat("x", bigTitle)}}}
	return &fakeStream{items: []api.StreamItem{
		{Kind: api.StreamSnapshot, Snapshot: &snap},
		{Kind: api.StreamSynchronized},
	}}, nil
}

func TestStreamDeliversAnItemOverTheWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := NewEndpointClient(start(t, "unix", &Server{Endpoint: &bigSnapshot{}}), api.ID{})
	for _, busy := range []time.Duration{200 * time.Millisecond, 0, 0} {
		st, err := c.Open(ctx, api.Subscription{Thread: api.NewID()})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(busy)
		it, err := st.Next(ctx)
		if err != nil || it.Kind != api.StreamSnapshot || len(it.Snapshot.Threads[0].Title) != bigTitle {
			t.Fatalf("reader busy %v: snapshot %v", busy, err)
		}
		if it, err := st.Next(ctx); err != nil || it.Kind != api.StreamSynchronized {
			t.Fatalf("reader busy %v: after the snapshot %+v %v", busy, it, err)
		}
		st.Close()
	}
}

func TestHubFollowCatchesUpOverTheWire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r := openReplica(t)
	host, log, thread := api.NewID(), api.NewID(), api.NewID()
	evs := events(log, thread, 1, 20)
	payload := json.RawMessage(`"` + strings.Repeat("x", 1<<20) + `"`)
	for i := range evs {
		evs[i].Payload = payload
	}
	for i := 0; i < len(evs); i += 4 {
		if _, err := r.Append(ctx, api.Upload{Host: host, Epoch: 1, Log: log, Events: evs[i : i+4]}); err != nil {
			t.Fatal(err)
		}
	}

	c := NewHubClient(start(t, "unix", &Server{Hub: r}), api.ID{})
	feed, err := c.Follow(ctx, thread, api.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Close()
	time.Sleep(200 * time.Millisecond)
	for i := range 20 {
		if e, err := feed.Next(ctx); err != nil || e.Cursor.Seq != uint64(i+1) || len(e.Payload) != len(payload) {
			t.Fatalf("event %d of a 20 MiB catch-up: seq %d, %v", i, e.Cursor.Seq, err)
		}
	}
}

func mustOpen(t *testing.T, ctx context.Context, c *EndpointClient, thread api.ID) api.Stream {
	t.Helper()
	st, err := c.Open(ctx, api.Subscription{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestDroppedSocketEndsStreamsAndRedials(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fake := &fakeEndpoint{env: api.Environment{ID: api.NewID(), Name: "mac", Log: api.NewID()}}
	d := &dialer{Loopback: start(t, "tcp", &Server{Endpoint: fake})}
	c := NewEndpointClient(d, fake.env.ID)

	thread := api.NewID()
	st := mustOpen(t, ctx, c, thread)
	if it, err := st.Next(ctx); err != nil || it.Kind != api.StreamSnapshot {
		t.Fatalf("first item: %+v %v", it, err)
	}
	blocked := make(chan error)
	go func() {
		_, err := c.Read(ctx, api.Query{Thread: blocks})
		blocked <- err
	}()
	// Let the Read reach the server before the socket goes.
	time.Sleep(50 * time.Millisecond)
	d.dialed()[0].Close()

	for range 4 {
		if _, err := st.Next(ctx); err != nil {
			break
		}
	}
	if _, err := st.Next(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stream on a dropped socket: got %v, want a connection error", err)
	}
	if err := <-blocked; err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call on a dropped socket: got %v, want a connection error", err)
	}
	if env, err := c.Info(ctx); err != nil || env != fake.env {
		t.Fatalf("info after the drop: %+v %v", env, err)
	}
	if n := len(d.dialed()); n != 2 {
		t.Fatalf("dialed %d sockets, want 2", n)
	}
}

// gated dials connections whose writes it can hold, as a stalled link would.
type gated struct {
	Loopback
	mu   sync.Mutex
	last *gatedConn
}

func (g *gated) Dial(ctx context.Context, m api.ID) (net.Conn, error) {
	c, err := g.Loopback.Dial(ctx, m)
	if err != nil {
		return nil, err
	}
	gc := &gatedConn{Conn: c, held: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	g.mu.Lock()
	g.last = gc
	g.mu.Unlock()
	return gc, nil
}

func (g *gated) conn() *gatedConn {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}

type gatedConn struct {
	net.Conn
	hold      atomic.Bool
	held      chan struct{} // closed once a write is held
	release   chan struct{}
	closed    chan struct{}
	heldOnce  sync.Once
	closeOnce sync.Once
}

func (c *gatedConn) Write(p []byte) (int, error) {
	if c.hold.Load() {
		c.heldOnce.Do(func() { close(c.held) })
		select {
		case <-c.release:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(p)
}

func (c *gatedConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func TestDroppedSocketIsNotTheCallersCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g := &gated{Loopback: start(t, "tcp", &Server{Endpoint: &fakeEndpoint{}})}
	c := NewEndpointClient(g, api.ID{})
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	gc := g.conn()
	gc.hold.Store(true)
	done := make(chan error, 1)
	go func() {
		_, err := c.Submit(ctx, api.Command{ID: api.NewID(), Body: api.TurnInterrupt{}})
		done <- err
	}()
	<-gc.held
	gc.Conn.(*net.TCPConn).CloseRead()
	err := <-done
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errLost) {
		t.Fatalf("submit on a socket that died under its write: got %v, want a lost connection and no context error", err)
	}
}

// slowLink carries a client's writes at rate bytes a second.
type slowLink struct {
	Loopback
	rate int
}

func (l slowLink) Dial(ctx context.Context, m api.ID) (net.Conn, error) {
	c, err := l.Loopback.Dial(ctx, m)
	if err != nil {
		return nil, err
	}
	return slowConn{c, l.rate}, nil
}

type slowConn struct {
	net.Conn
	rate int
}

func (s slowConn) Write(p []byte) (int, error) {
	time.Sleep(time.Duration(len(p)) * time.Second / time.Duration(s.rate))
	return s.Conn.Write(p)
}

func TestLongWriteLetsPongsThrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r := openReplica(t)
	// A message that takes a second to write, to a server that drops the
	// socket when a pong is 300ms late.
	srv := &Server{Hub: r, keepalive: keepalive{every: 20 * time.Millisecond, within: 300 * time.Millisecond}}
	c := NewHubClient(slowLink{start(t, "unix", srv), 512 << 10}, api.ID{})
	log := api.NewID()
	evs := events(log, api.NewID(), 1, 1)
	evs[0].Payload = json.RawMessage(`"` + strings.Repeat("x", 512<<10) + `"`)
	if ack, err := c.Append(ctx, api.Upload{Host: api.NewID(), Epoch: 1, Log: log, Events: evs}); err != nil || ack.Acked != 1 {
		t.Fatalf("append over a slow link: %+v %v", ack, err)
	}
}

// hung's dials wait for their ctx, like a peer that never answers.
type hung struct {
	Loopback
	dialing chan struct{} // gets one value per dial begun
	gave    chan struct{} // gets one value per dial given up
}

func (h *hung) Dial(ctx context.Context, _ api.ID) (net.Conn, error) {
	h.dialing <- struct{}{}
	<-ctx.Done()
	h.gave <- struct{}{}
	return nil, ctx.Err()
}

func TestDialWaitHonorsEachCallersContext(t *testing.T) {
	h := &hung{dialing: make(chan struct{}, 2), gave: make(chan struct{}, 2)}
	c := NewEndpointClient(h, api.ID{})
	first, stopFirst := context.WithCancel(context.Background())
	defer stopFirst()
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.Info(first)
		firstDone <- err
	}()
	<-h.dialing

	second, stopSecond := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stopSecond()
	secondDone := make(chan error, 1)
	go func() {
		_, err := c.Info(second)
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("a 30ms call behind a hung dial: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		stopFirst()
		t.Fatal("a 30ms call waits on another caller's hung dial")
	}
	select {
	case <-h.gave:
		t.Fatal("the dial was given up while a caller still waited for it")
	default:
	}
	stopFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller: %v", err)
	}
	select {
	case <-h.gave:
	case <-time.After(time.Second):
		t.Fatal("the dial went on after every caller had left")
	}
	if len(h.dialing) != 0 {
		t.Fatal("the second caller dialed again instead of waiting for the first dial")
	}
}

func TestWriteWaitHonorsTheCallersContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g := &gated{Loopback: start(t, "tcp", &Server{Endpoint: &fakeEndpoint{}})}
	c := NewEndpointClient(g, api.ID{})
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	gc := g.conn()
	gc.hold.Store(true)
	submit, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := c.Submit(submit, api.Command{ID: api.NewID(), Body: api.TurnInterrupt{}})
		done <- err
	}()
	<-gc.held
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("submit: %v, want its own cancel", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a cancelled submit still waits on a held write")
	}
	gc.hold.Store(false)
	close(gc.release)
	if _, err := c.Info(ctx); err != nil {
		t.Fatalf("the socket after a cancelled write: %v", err)
	}
}

// submits is an Endpoint that reports each command it runs.
type submits struct {
	fakeEndpoint
	ran chan api.ID
}

func (s *submits) Submit(ctx context.Context, cmd api.Command) (api.Receipt, error) {
	s.ran <- cmd.ID
	return s.fakeEndpoint.Submit(ctx, cmd)
}

func TestCancelledCallSendsNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ep := &submits{ran: make(chan api.ID, 2)}
	c := NewEndpointClient(start(t, "tcp", &Server{Endpoint: ep}), api.ID{})
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	gone, stop := context.WithCancel(ctx)
	stop()
	if _, err := c.Submit(gone, api.Command{ID: api.NewID(), Body: api.TurnInterrupt{}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("submit with a cancelled ctx: %v", err)
	}
	live := api.Command{ID: api.NewID(), Body: api.TurnInterrupt{}}
	if _, err := c.Submit(ctx, live); err != nil {
		t.Fatal(err)
	}
	if got := <-ep.ran; got != live.ID {
		t.Fatalf("the server ran %v, which its caller had cancelled before sending", got)
	}
	select {
	case got := <-ep.ran:
		t.Fatalf("the server also ran %v, which its caller had cancelled before sending", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCallWaitsBehindAnotherWriteOnlyUntilItsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ep := &submits{ran: make(chan api.ID, 1)}
	g := &gated{Loopback: start(t, "unix", &Server{Endpoint: ep})}
	c := NewEndpointClient(g, api.ID{})
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	gc := g.conn()
	gc.hold.Store(true)
	first := api.Command{ID: api.NewID(), Body: api.TurnInterrupt{}}
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.Submit(ctx, first)
		firstDone <- err
	}()
	<-gc.held

	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	t0 := time.Now()
	if _, err := c.Info(short); !errors.Is(err, context.DeadlineExceeded) || time.Since(t0) > 500*time.Millisecond {
		t.Fatalf("a 100ms call behind a held write: %v after %v", err, time.Since(t0))
	}
	gc.hold.Store(false)
	close(gc.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("the held submit: %v", err)
	}
	if got := <-ep.ran; got != first.ID {
		t.Fatalf("ran %v", got)
	}
}

func TestCloseEndsTheClientsCallsAndStreams(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fake := &fakeEndpoint{env: api.Environment{ID: api.NewID()}, closed: make(chan api.ID, 1)}
	g := &gated{Loopback: start(t, "tcp", &Server{Endpoint: fake})}
	c := NewEndpointClient(g, api.ID{})
	thread := api.NewID()
	st, err := c.Open(ctx, api.Subscription{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() {
		_, err := c.Read(ctx, api.Query{Thread: blocks})
		blocked <- err
	}()
	for range 5 {
		if _, err := st.Next(ctx); err != nil {
			t.Fatal(err)
		}
	}

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g.conn().closed:
	default:
		t.Fatal("Close returned with the socket still open")
	}
	if _, err := st.Next(ctx); !errors.Is(err, errClosed) {
		t.Fatalf("stream after Close: %v", err)
	}
	if err := <-blocked; !errors.Is(err, errClosed) {
		t.Fatalf("call in flight at Close: %v", err)
	}
	if _, err := c.Info(ctx); !errors.Is(err, errClosed) {
		t.Fatalf("call after Close: %v", err)
	}
	select {
	case got := <-fake.closed:
		if got != thread {
			t.Fatalf("server closed %v", got)
		}
	case <-ctx.Done():
		t.Fatal("the server never closed the stream of a closed client")
	}
}

func TestIdleSocketCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g := &gated{Loopback: start(t, "tcp", &Server{Endpoint: &fakeEndpoint{}})}
	c := NewEndpointClient(g, api.ID{})
	c.c.idleAfter = 50 * time.Millisecond

	st, err := c.Open(ctx, api.Subscription{Thread: api.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	first := g.conn()
	select {
	case <-first.closed:
		t.Fatal("a socket with an open stream was closed as idle")
	case <-time.After(200 * time.Millisecond):
	}
	st.Close()
	select {
	case <-first.closed:
	case <-ctx.Done():
		t.Fatal("a socket with no calls or streams stayed open")
	}
	if _, err := c.Info(ctx); err != nil {
		t.Fatalf("a call after the idle close: %v", err)
	}
	if g.conn() == first {
		t.Fatal("the call after the idle close did not dial again")
	}
}

func TestOversizedRequestLeavesTheSocketAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fake := &fakeEndpoint{env: api.Environment{ID: api.NewID()}}
	d := &dialer{Loopback: start(t, "unix", &Server{Endpoint: fake})}
	c := NewEndpointClient(d, api.ID{})
	thread := api.NewID()
	st := mustOpen(t, ctx, c, thread)

	big := api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: strings.Repeat("x", maxMessage)}}
	if _, err := c.Submit(ctx, big); !errors.Is(err, errTooLarge) {
		t.Fatalf("a submit over the server's limit: %v", err)
	}
	if err := readStream(ctx, st, thread, api.Cursor{}); err != nil {
		t.Fatal(err)
	}
	idle, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	if _, err := st.Next(idle); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the stream beside a refused submit: %v, want it still open", err)
	}
	if env, err := c.Info(ctx); err != nil || env != fake.env {
		t.Fatalf("info: %+v %v", env, err)
	}
	if n := len(d.dialed()); n != 1 {
		t.Fatalf("dialed %d sockets, want 1", n)
	}
}
