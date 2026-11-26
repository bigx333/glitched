package daemon

import (
	"context"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider/fake"
)

// A stop on B that holds what waits on a link's thread shows on A's
// placeholder as held, as B's own view says it is resumable there, and a
// resume there clears it.
func TestAPlaceholderSaysWhatAStopThereHolds(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(true)), "fake", engine.Limits{})
	p.adB.Spawns(p.b.Endpoint(), "do it there", fake.Child{Title: "local nested", Task: "go"})
	k := p.place("report parent")
	b := p.bound(k).Thread
	grand := waitingGrandchild(t, p, b)
	if _, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: b, Body: api.TurnInterrupt{}}); err != nil {
		t.Fatal(err)
	}
	r := grand.Requests[0]
	if _, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: grand.ID, Body: api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: []byte(`{"allow":true}`)}}); err != nil {
		t.Fatal(err)
	}
	until(t, "B holding its child's report", func() bool { return engine.View(p.onB(b)).Resumable })
	until(t, "A's placeholder held", func() bool { return engine.View(p.placeholder(k)).Remote.Held })
	if v := engine.View(p.placeholder(k)); v.Resumable || v.Run != api.RunIdle {
		t.Errorf("A's placeholder is %v and resumable %v; want idle with nothing to resume here", v.Run, v.Resumable)
	}
	if _, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: b, Body: api.QueueResume{}}); err != nil {
		t.Fatal(err)
	}
	until(t, "A's placeholder no longer held", func() bool { return !engine.View(p.placeholder(k)).Remote.Held })
	folded(t, p.a.store)
}

// A report B's thread already ran a turn for holds nothing after a stop
// there, so A's placeholder doesn't say held.
func TestAStopAfterAReportRanHoldsNothing(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	p.adB.Spawns(p.b.Endpoint(), "do it there", fake.Child{Title: "nested", Task: "do one piece"})
	k := p.place("B consumes a report")
	b := p.bound(k).Thread
	until(t, "B's turn for its child's report ended, and A followed it", func() bool {
		th, r := p.onB(b), p.placeholder(k).Remote
		return len(th.Runs) >= 2 && th.Live() == nil && r.Run == th.Runs[len(th.Runs)-1].ID && r.Slot == api.RunIdle
	})
	c, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: b, Body: api.TurnInterrupt{}})
	if err != nil {
		t.Fatal(err)
	}
	until(t, "A followed the stop", func() bool { return p.placeholder(k).Remote.Cursor.Seq >= c.Committed.Seq })
	if engine.View(p.onB(b)).Resumable {
		t.Fatal("B holds something to resume")
	}
	if engine.View(p.placeholder(k)).Remote.Held {
		t.Error("A's placeholder says held, though B holds nothing")
	}
}

// A placeholder settled here still says held when its thread there, reopened
// by a message deferred on B, holds it.
func TestASettledPlaceholderSaysWhatItsReopenedThreadHolds(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	k := p.place("hold after settle")
	b := p.bound(k).Thread
	until(t, "the first run done", func() bool { return p.placeholder(k).Remote.Ended != nil && p.onB(b).Live() == nil })
	p.submitA(k, settleIt)
	p.settledThere(b)
	c, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: b, Body: api.MessageDispatch{Text: "resume this later", Mode: api.Defer}})
	if err != nil {
		t.Fatal(err)
	}
	until(t, "A followed the deferred message", func() bool { return p.placeholder(k).Remote.Cursor.Seq >= c.Committed.Seq })
	if !engine.View(p.onB(b)).Resumable {
		t.Fatal("B holds nothing to resume")
	}
	if v := engine.View(p.placeholder(k)); !v.Remote.Held || v.Resumable {
		t.Errorf("A's placeholder says held %v and resumable %v; want held with nothing to resume here", v.Remote.Held, v.Resumable)
	}
}

// A resume on B of what it held takes the hold off A's placeholder at the
// commit that admits its run, the same commit A records it working at.
func TestAResumeThereEndsTheHoldWithTheRunItAdmits(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	k := p.place("held then resumed")
	b := p.bound(k).Thread
	until(t, "the first run done", func() bool { return p.placeholder(k).Remote.Ended != nil && p.onB(b).Live() == nil })
	send := func(intent api.Intent) {
		if _, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: b, Body: intent}); err != nil {
			t.Fatal(err)
		}
	}
	send(api.TurnInterrupt{})
	send(api.MessageDispatch{Text: "later", Mode: api.Defer})
	until(t, "A's placeholder held", func() bool { return engine.View(p.placeholder(k)).Remote.Held })
	send(api.QueueResume{})
	until(t, "the resumed run done on B", func() bool { return len(p.onB(b).Runs) >= 2 && p.onB(b).Live() == nil })
	until(t, "A followed it", func() bool { return p.placeholder(k).Remote.Run == p.onB(b).Runs[len(p.onB(b).Runs)-1].ID })
	for _, e := range eventsOf(t, p.a.Endpoint(), k) {
		body, _ := e.Decode()
		if r, ok := body.(api.RemoteSeen); ok && r.Held && r.State != api.RunIdle {
			t.Errorf("A recorded its child %v and held at once", r.State)
		}
	}
	if engine.View(p.placeholder(k)).Remote.Held {
		t.Error("A's placeholder still says held after the resume there")
	}
}

// waitingGrandchild waits for the child b's turn started on B to wait on
// its approval, and for b to be idle with that approval's wake done: the
// wake's turn asks too, and B's person allows it.
func waitingGrandchild(t *testing.T, p *remotes, b api.ID) api.Thread {
	t.Helper()
	var grand api.Thread
	until(t, "the thread there idle and its own child waiting", func() bool {
		v := engine.View(p.onB(b))
		for _, r := range v.Requests {
			p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: b, Body: api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: []byte(`{"allow":true}`)}})
		}
		for _, g := range p.there() {
			if g.Parent == b && g.Run == api.RunWaiting && len(g.Requests) == 1 {
				grand = g
				return v.LastReport != nil && v.LastReport.Child == g.ID && v.Run == api.RunIdle && v.Queued == 0
			}
		}
		return false
	})
	return grand
}
