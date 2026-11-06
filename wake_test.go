package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// parking plays its script on every thread but the parked one, whose
// turns stall mid-way until stopped.
type parking struct {
	*fake.Adapter
	stall  provider.Adapter
	mu     sync.Mutex
	parked api.ID
}

func (p *parking) park(thread api.ID) {
	p.mu.Lock()
	p.parked = thread
	p.mu.Unlock()
}

func (p *parking) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	p.mu.Lock()
	parked := p.parked == spec.Thread
	p.mu.Unlock()
	if parked {
		return p.stall.Open(ctx, spec)
	}
	return p.Adapter.Open(ctx, spec)
}

func submitTo(t *testing.T, d *daemon.Daemon, thread api.ID, b api.Intent) {
	t.Helper()
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: b}); err != nil {
		t.Fatal(err)
	}
}

// heldReport is the app on a parent, Ship the cache, that the user stopped
// while its child, Pick a store, worked; the child's done report waits in
// the parent for the user's next message.
func heldReport(t *testing.T) (*harness, *daemon.Daemon, *parking, api.ID) {
	t.Helper()
	dir := tempDir(t)
	ad := &parking{Adapter: fake.New([]provider.Event{provider.TextDelta{Text: "SQLite fits: one file, no server."}}), stall: stalled{fake.New(midTurn), 1}}
	d := engine(t, daemon.Config{Dir: dir}, ad)
	top := seed(t, d, "/work/t3code", "Ship the cache")
	ad.park(top)
	submitTo(t, d, top, api.MessageDispatch{Text: "Cache the thread list.", Mode: api.StartNow})
	ep := d.Endpoint()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		snap, err := ep.Read(context.Background(), api.Query{Thread: top})
		if err != nil {
			t.Fatal(err)
		}
		if snap.Threads[0].Run == api.RunRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the parent's turn never started")
		}
	}
	kid := seedChild(t, d, top, "Pick a store")
	submitTo(t, d, top, api.TurnInterrupt{})
	settled(t, ep, top)
	submitTo(t, d, kid, api.MessageDispatch{Text: "Pick one.", Mode: api.StartNow})
	settled(t, ep, kid)
	reportedAll(t, ep, kid)
	h := start(t, dir)
	h.a.select_(h.thread("Ship the cache"))
	h.until("the report", func() bool { return h.tt.HasText("Pick a store") && h.tt.HasText("Done") })
	h.tt.Advance(time.Second)
	return h, d, ad, top
}

// A report that wakes the parent's agent shows as a line under its card,
// not as words of the user's: held while the user's stop stands, then sent
// with the user's next message.
func TestAWakeShowsUnderItsReport(t *testing.T) {
	h, d, ad, top := heldReport(t)
	if !h.tt.HasText("↳ Goes to the agent with your next message") {
		t.Errorf("a report held by the user's stop: %q; want it to say it goes with the next message", h.tt.Texts())
	}
	ad.park(api.ID{})
	submitTo(t, d, top, api.MessageDispatch{Text: "Use what it picked.", Mode: api.StartNow})
	h.until("the report sent", func() bool { return h.tt.HasText("↳ Sent to the agent") && h.tt.HasText("Use what it picked.") })
	for _, s := range h.tt.Texts() {
		if strings.Contains(s, "<task") {
			t.Errorf("the wake shows as text: %q", s)
		}
	}
}
