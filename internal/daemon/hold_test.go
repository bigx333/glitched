package daemon

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// A stop on B that holds what waits on a link's thread shows on A's
// placeholder as held, as B's own view says it is resumable there, and a
// resume there clears it.
func TestAPlaceholderSaysWhatAStopThereHolds(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(true)), "fake", engine.Limits{})
	p.adB.Spawns(p.b.Endpoint(), "do it there", fake.Child{Title: "local nested", Task: "go"})
	k := p.place("report parent")
	b := p.bound(k).Thread
	var grand api.Thread
	until(t, "the thread there idle and its own child waiting", func() bool {
		for _, v := range p.there() {
			if v.Parent == b && v.Run == api.RunWaiting && len(v.Requests) == 1 {
				grand = v
				return p.onB(b).Live() == nil
			}
		}
		return false
	})
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

// readProbe is a link's server on B that answers its socket's Reads with
// hook, while one is set, and every other call as B does.
type readProbe struct {
	*proxy
	mu   sync.Mutex
	hook func(context.Context, api.Query) (api.Snapshot, error)
}

// probeLink serves B's links through a readProbe, and points A at it.
func probeLink(t *testing.T, p *remotes) *readProbe {
	ctx, cancel := context.WithCancel(context.Background())
	tn := transport.Tailnet{Address: freeAddr(t)}
	ln, err := tn.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g := &grants{dir: p.dirB, base: ctx}
	go g.watch(ctx)
	pr := &readProbe{proxy: &proxy{t: t, p: p, dirB: p.dirB, addr: tn.Address}}
	srv := &transport.Server{Transport: tn, Endpoint: pr, Replies: pr, Authorize: func(req *http.Request, proved func(transport.Key) bool) (transport.Grant, error) {
		return g.authorize(req, proved)
	}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Serve(ctx, ln)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	p.machine(tn.Address)
	return pr
}

func (pr *readProbe) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	pr.mu.Lock()
	hook := pr.hook
	pr.mu.Unlock()
	if hook != nil {
		return hook(ctx, q)
	}
	return pr.inner().Read(ctx, q)
}

func (pr *readProbe) intercept(fn func(context.Context, api.Query) (api.Snapshot, error)) {
	pr.mu.Lock()
	pr.hook = fn
	pr.mu.Unlock()
}

// B's Reads failing over and over, while its stream and its replies
// answer, keep A from knowing what holds there, never from its child or
// the child's done report.
func TestFailedReadsOfWhatHoldsKeepTheChildAndItsReport(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(true)), "fake", engine.Limits{})
	pr := probeLink(t, p)
	k := p.place("report without the hold")
	b := p.bound(k).Thread
	send := func(intent api.Intent) api.Receipt {
		c, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: b, Body: intent})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	approve := func() {
		rq := engine.View(p.onB(b)).Requests[0]
		send(api.RequestRespond{Request: rq.ID, Generation: rq.Generation, Answer: []byte(`{"allow":true}`)})
	}
	until(t, "the blocked report", func() bool {
		rs, _ := p.reports(k)
		return len(rs) == 1 && p.placeholder(k).Remote.Slot == api.RunWaiting
	})
	approve()
	until(t, "the first turn ended", func() bool { return p.placeholder(k).Remote.Ended != nil })
	second := send(api.MessageDispatch{Text: "second task"})
	until(t, "the second turn waiting on both machines", func() bool {
		r := p.placeholder(k).Remote
		return r.Run == second.Command && r.Slot == api.RunWaiting
	})
	var failed atomic.Int32
	pr.intercept(func(ctx context.Context, q api.Query) (api.Snapshot, error) {
		if failed.Add(1) <= 6 {
			return api.Snapshot{}, errors.New("a brief storage error")
		}
		return pr.inner().Read(ctx, q)
	})
	send(api.ThreadConfigure{Effort: new("high")})
	until(t, "six failed reads", func() bool { return failed.Load() >= 6 })
	approve()
	until(t, "the second turn's report", func() bool {
		rs, _ := p.reports(k)
		return slices.ContainsFunc(rs, func(r api.ChildReported) bool { return r.Run == second.Command && r.Kind == api.ReportDone })
	})
	rs, _ := p.reports(k)
	if slices.ContainsFunc(rs, func(r api.ChildReported) bool { return r.Kind == api.ReportFailed }) || p.placeholder(k).Remote.Lost != "" {
		t.Errorf("after failed reads, A reported %v and lost %q; want only its child done", rs, p.placeholder(k).Remote.Lost)
	}
}

// What A records of its child holds at the cursor it records: a Read B
// answers after more commits there is not taken for the events before.
func TestAHoldIsOfTheCursorItIsRecordedAt(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(true)), "fake", engine.Limits{})
	pr := probeLink(t, p)
	k := p.place("read fence")
	b := p.bound(k).Thread
	until(t, "both machines waiting on the approval", func() bool {
		return p.placeholder(k).Remote.Slot == api.RunWaiting && len(engine.View(p.onB(b)).Requests) == 1
	})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	pr.intercept(func(ctx context.Context, q api.Query) (api.Snapshot, error) {
		once.Do(func() {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		return pr.inner().Read(ctx, q)
	})
	send := func(intent api.Intent) api.Receipt {
		c, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: b, Body: intent})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	old := send(api.ThreadConfigure{Effort: new("high")})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("A read nothing for the change")
	}
	send(api.MessageDispatch{Text: "later", Mode: api.Defer})
	rq := engine.View(p.onB(b)).Requests[0]
	send(api.RequestRespond{Request: rq.ID, Generation: rq.Generation, Answer: []byte(`{"allow":true}`)})
	until(t, "B idle and holding the message", func() bool { return engine.View(p.onB(b)).Resumable })
	unblock()
	until(t, "A's placeholder idle and held", func() bool {
		r := p.placeholder(k).Remote
		return r.Held && r.Slot == api.RunIdle
	})
	for _, e := range eventsOf(t, p.a.Endpoint(), k) {
		body, _ := e.Decode()
		if r, ok := body.(api.RemoteSeen); ok && r.Cursor == old.Committed && r.State == api.RunWaiting && r.Held {
			t.Error("A recorded a hold B had only after it, with the approval B waited on at its cursor")
		}
	}
}
