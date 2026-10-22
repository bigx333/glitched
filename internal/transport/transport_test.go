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
// of each stream the server closes; reads, when set, gets nil as a Read of
// blocks begins and its ctx's error as it ends; pulled counts the items its
// streams gave the server.
type fakeEndpoint struct {
	env    api.Environment
	errs   map[api.ID]error
	closed chan api.ID
	reads  chan error
	pulled atomic.Int64
}

func (f *fakeEndpoint) Info(context.Context) (api.Environment, error) { return f.env, nil }

func (f *fakeEndpoint) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	if q.Thread == blocks {
		if f.reads != nil {
			f.reads <- nil
			defer func() { f.reads <- ctx.Err() }()
		}
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

func mustOpen(t *testing.T, ctx context.Context, c *EndpointClient, thread api.ID) api.Stream {
	t.Helper()
	st, err := c.Open(ctx, api.Subscription{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// panics is an Endpoint whose Submit panics.
type panics struct{ fakeEndpoint }

func (*panics) Submit(context.Context, api.Command) (api.Receipt, error) { panic("submit is broken") }

func TestPanickingHandlerAnswersWithAnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ep := &panics{fakeEndpoint{env: api.Environment{ID: api.NewID()}}}
	d := &dialer{Loopback: start(t, "unix", &Server{Endpoint: ep})}
	c := NewEndpointClient(d, api.ID{})
	if _, err := c.Submit(ctx, api.Command{ID: api.NewID(), Body: api.TurnInterrupt{}}); err == nil || errors.Is(err, errLost) {
		t.Fatalf("submit to a panicking handler: %v, want the server's error", err)
	}
	if env, err := c.Info(ctx); err != nil || env != ep.env {
		t.Fatalf("info after the panic: %+v %v", env, err)
	}
	if n := len(d.dialed()); n != 1 {
		t.Fatalf("dialed %d sockets, want 1", n)
	}
}
