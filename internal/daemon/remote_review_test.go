package daemon

import (
	"context"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider/fake"
)

func TestReviewCleanupCreateNeverRuns(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	x := newProxy(t, p)
	x.stop()
	p.machine(x.addr)
	kid := p.place("cleanup")
	until(t, "first attempt", func() bool { return p.placeholder(kid).Remote.Attempted })
	p.submitA(kid, settleIt)
	until(t, "cleanup leased", func() bool { return slices.Contains(p.leased(kid), "remote.stop") })
	x.mu.Lock()
	x.hold = true
	x.mu.Unlock()
	x.start()
	select {
	case <-x.submitted:
	case <-time.After(10 * time.Second):
		t.Fatal("cleanup create not delivered")
	}
	there := p.placeholder(kid).Remote.Create.Target
	until(t, "worktree ready on B", func() bool { return !p.onB(there).Origin.Making })
	if n := p.count(there, "turn.admitted"); n != 0 {
		t.Fatalf("B admitted %d turn(s) from the cleanup create before the settle; want zero", n)
	}
}

func TestReviewSettleIsNotTakeover(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	p.asking()
	kid := p.place("settle")
	there := p.bound(kid).Thread
	until(t, "waiting", func() bool { return p.placeholder(kid).Remote.Slot == api.RunWaiting })
	p.submitA(kid, settleIt)
	p.settledThere(there)
	until(t, "idle on A", func() bool { return p.placeholder(kid).Remote.Slot == api.RunIdle })
	if _, why := p.tool("task_send", taskArgs(kid, `,"message":"more"`)); why != "" {
		t.Fatalf("task_send after A's settle, without user activity on B: %s", why)
	}
}

func TestReviewOfflineNarrowingSurvivesSettle(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	kid := p.place("narrow")
	there := p.bound(kid).Thread
	until(t, "first report", func() bool { r, _ := p.reports(kid); return len(r) == 1 })
	p.stopB()
	ask := api.AccessAsk
	p.submitA(kid, api.ThreadConfigure{Access: &ask})
	until(t, "narrowing leased", func() bool { return slices.Contains(p.leased(kid), "remote.configure") })
	p.submitA(kid, settleIt)
	time.Sleep(300 * time.Millisecond)
	if l := p.leased(kid); !slices.Contains(l, "remote.configure") {
		t.Fatalf("a settle with b down took the narrowing off the lane: %v", l)
	}
	p.startB()
	p.settledThere(there)
	p.submitA(kid, api.MessageDispatch{Text: "new work", Mode: api.AfterActive})
	until(t, "second report", func() bool { r, _ := p.reports(kid); return len(r) == 2 })
	th := p.onB(there)
	if a := th.Runs[len(th.Runs)-1].Access; a != ask {
		t.Fatalf("second turn on B admitted with access %d; A narrowed the child to %d", a, ask)
	}
}

func TestReviewRemoteWorkDoesNotHoldAAwake(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	p.stopB()
	kid := p.place("offline")
	until(t, "attempt", func() bool { return p.placeholder(kid).Remote.Attempted })
	p.parentIdle()
	live, err := p.a.store.LiveThreads(context.Background())
	if err != nil || len(live) != 0 {
		t.Fatalf("local live threads: %v, %v", live, err)
	}
	if p.a.awake.working.Load() {
		t.Fatal("A tells its sleep inhibitor an agent is working with zero local live runs and B offline")
	}
}

func TestReviewRemoteTaskStatusAfterCompletion(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	p.asking()
	kid := p.place("status")
	until(t, "first report", func() bool { r, _ := p.reports(kid); return len(r) == 1 })
	s, why := p.tool("task_status", taskArgs(kid, ""))
	if why != "" || s.Status != "done" {
		t.Fatalf("task_status after completion on B: status=%q last_report=%q error=%q; want done", s.Status, s.LastReport, why)
	}
}

func TestReviewSettleDropsEveryEarlierMessage(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	kid := p.place("queued")
	there := p.bound(kid).Thread
	until(t, "first report", func() bool { r, _ := p.reports(kid); return len(r) == 1 })
	p.stopB()
	p.submitA(kid, api.MessageDispatch{Text: "first stale message", Mode: api.AfterActive})
	until(t, "first send leased", func() bool { return slices.Contains(p.leased(kid), "remote.send") })
	p.submitA(kid, api.MessageDispatch{Text: "second stale message", Mode: api.AfterActive})
	p.submitA(kid, settleIt)
	p.startB()
	p.settledThere(there)
	for _, e := range eventsOf(t, p.b.Endpoint(), there) {
		if b, _ := e.Decode(); b != nil {
			if q, ok := b.(api.MessageQueued); ok && q.Text == "second stale message" {
				t.Fatal("B received the second stale message after A had committed its settle")
			}
		}
	}
}

func TestReviewCancelSkipsTakenGrandchild(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	p.asking()
	ep := p.a.Endpoint()
	p.adA.Spawns(ep, "place remote", fake.Child{Title: "remote", Task: "go", Remote: "b"})
	ids, errs := spawn(t, ep, p.adA, p.parent, fake.Child{Title: "local", Task: "place remote"})
	if errs[0] != nil {
		t.Fatal(errs[0])
	}
	local := ids[0]
	var kid api.ID
	until(t, "remote grandchild", func() bool {
		snap, err := ep.Read(context.Background(), api.Query{})
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range snap.Threads {
			if v.Parent == local && v.Remote != nil {
				kid = v.ID
				return true
			}
		}
		return false
	})
	there := p.bound(kid).Thread
	until(t, "remote waits", func() bool { return p.placeholder(kid).Remote.Slot == api.RunWaiting })
	p.message(there)
	until(t, "takeover folded", func() bool { return p.placeholder(kid).Remote.Taken })
	if _, why := p.tool("task_cancel", taskArgs(local, "")); why != "" {
		t.Fatalf("task_cancel on the local child rejected instead of skipping the taken grandchild: %s", why)
	}
}

func TestReviewTwoPlaceholdersReplayTheirOwnStop(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	ids, errs := spawn(t, p.a.Endpoint(), p.adA, p.parent, fake.Child{Title: "one", Task: "one", Remote: "b"}, fake.Child{Title: "two", Task: "two", Remote: "b"})
	there := make([]api.ID, 0, 2)
	for i, kid := range ids {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		there = append(there, p.bound(kid).Thread)
	}
	p.stopB()
	p.submitA(p.parent, api.ThreadDelete{})
	for _, kid := range ids {
		until(t, "stop leased", func() bool { return slices.Contains(p.leased(kid), "remote.stop") })
	}
	p.stopA()
	p.startA()
	p.startB()
	for _, id := range there {
		p.settledThere(id)
	}
}

func TestReviewLostCleanupDoesNotWakeSettledParent(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	p.stopB()
	kid := p.place("offline")
	until(t, "attempt", func() bool { return p.placeholder(kid).Remote.Attempted })
	p.submitA(p.parent, settleIt)
	if err := Revoke(p.dirB, "a"); err != nil {
		t.Fatal(err)
	}
	p.startB()
	until(t, "loss reported", func() bool { r, _ := p.reports(kid); return len(r) > 0 })
	r, w := p.reports(kid)
	if len(w) > 0 || len(r) != 1 || r[0].Kind != api.ReportFailed || p.placeholder(p.parent).Disposition.State != api.Settled {
		t.Fatalf("settled parent received %d reports and %d wakes", len(r), len(w))
	}
}

func TestReviewInterruptDuringWorktreeMake(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, true)
	thread := l.mustCreate(api.AccessAsk)
	l.making(thread)
	if err := l.send(thread, api.TurnInterrupt{Tree: true}); err != nil {
		t.Fatal(err)
	}
	l.open()
	l.waitFor(thread, "worktree.ready", 1)
	if n := l.count(thread, "turn.admitted"); n != 0 {
		t.Fatalf("B admitted %d turn after acknowledging cancel while git was preparing the worktree; want zero", n)
	}
}
