package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
)

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

// The server counts an item's bytes as it marshals them and the client acks
// the bytes it reads; a mismatch would leak or stall the window.
func TestItemBytesMatchAcrossTheWire(t *testing.T) {
	ev := api.Event{Type: "message.added", Payload: json.RawMessage(`{"text":"<b>&</b> \u2028 café 😀", "n": 1}`)}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := json.Marshal(response{ID: 1, Item: b})
	if err != nil {
		t.Fatal(err)
	}
	var r response
	if err := json.Unmarshal(frame, &r); err != nil || len(r.Item) != len(b) {
		t.Fatalf("server counted %d bytes, client reads %d: %v", len(b), len(r.Item), err)
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
		if _, err := r.Append(ctx, api.Upload{Protocol: api.Protocol, Host: host, Epoch: 1, Log: log, Events: evs[i : i+4]}); err != nil {
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
