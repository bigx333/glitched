package daemon

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider/fake"
)

type captureCleanup struct {
	slog.Handler
	why chan string
}

func (c captureCleanup) Enabled(context.Context, slog.Level) bool { return true }
func (c captureCleanup) Handle(_ context.Context, r slog.Record) error {
	if strings.Contains(r.Message, "refused this machine's") {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "reason" {
				select {
				case c.why <- a.Value.String():
				default:
				}
			}
			return true
		})
	}
	return nil
}

func TestR2SettleOfAlreadySettledChild(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	k := p.place("work")
	b := p.bound(k).Thread
	until(t, "first result", func() bool { reps, _ := p.reports(k); return len(reps) == 1 })
	p.parentIdle()
	g := delayFrame(t, p, "earlier message")
	p.submitA(k, api.MessageDispatch{Text: "earlier message", Mode: api.AfterActive})
	g.wait(t, g.held)
	if _, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: b, Body: settleIt}); err != nil {
		t.Fatal(err)
	}
	p.submitA(k, settleIt)
	waitStopDone(t, p, k)
	fence := p.onB(b).Origin.Fence
	p.stopA()
	p.startA()
	g.release()
	g.wait(t, g.answered)
	if th := p.onB(b); th.Disposition.State != api.Settled || p.count(b, "turn.admitted") != 1 {
		t.Fatalf("after B settled locally and A's settle completed: B fence=%d, A fence=%d; delayed message reopened B, admissions=%d", fence, p.placeholder(k).Remote.Settles, p.count(b, "turn.admitted"))
	}
}

func TestR2CleanupDeniedThenCapacityReturns(t *testing.T) {
	for _, condition := range []string{"count", "depth", "access"} {
		t.Run(condition, func(t *testing.T) {
			p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
			if condition == "count" {
				filler := p.place("existing")
				until(t, "first result", func() bool { rs, _ := p.reports(filler); return len(rs) == 1 })
				p.parentIdle()
			}
			if condition == "depth" {
				ids, errs := spawn(t, p.a.Endpoint(), p.adA, p.parent, fake.Child{Title: "local parent", Task: "prepare"})
				if errs[0] != nil {
					t.Fatal(errs[0])
				}
				p.parent = ids[0]
				p.parentIdle()
			}
			g := delayFrame(t, p, "do it there")
			k := p.place("pending")
			g.wait(t, g.held)
			b := p.placeholder(k).Remote.Create.Target
			switch condition {
			case "count":
				p.b.store.SetLimits(engine.Limits{All: 1})
			case "depth":
				p.b.store.SetLimits(engine.Limits{Depth: 1})
			case "access":
				allowChildren(t, p.dirB, api.AccessAsk)
			}
			p.submitA(k, settleIt)
			waitStopDone(t, p, k)
			notLost(t, p, k)
			if p.onB(b).Disposition.State != api.Settled {
				t.Fatalf("%s: the settled cleanup create was held back by B's limits", condition)
			}
			p.b.store.SetLimits(engine.Limits{})
			allowChildren(t, p.dirB, api.AccessAutoEdits)
			g.release()
			g.wait(t, g.answered)
			until(t, "B completed create", func() bool { th := p.onB(b); return th.Exists() && !th.Origin.Making })
			if n := p.count(b, "turn.admitted"); n != 0 {
				t.Fatalf("%s: A finished cleanup and is settled=%t; B ran the late original, admissions=%d", condition, p.placeholder(k).Disposition.State == api.Settled, n)
			}
		})
	}
}

func TestR2IdleStopWithReportWake(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, false)
	k := l.mustCreate(api.AccessAutoEdits)
	l.waitFor(k, "turn.ended", 1)
	allowChildren(t, l.dirB, api.AccessAsk)
	_, err := l.b.store.Observe(context.Background(), k, engine.Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Title: "finished child", Kind: api.ReportDone}, Reply: "done"}, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if th := l.view(k); th.Queued != 1 || th.Run != api.RunIdle {
		t.Fatalf("bad precondition %+v", th)
	}
	if err := l.send(k, api.TurnInterrupt{}); err != nil {
		t.Fatal(err)
	}
	if err := l.send(k, api.ThreadConfigure{Access: new(api.AccessAsk)}); err != nil {
		t.Fatal(err)
	}
	if n := l.count(k, "turn.admitted"); n != 1 {
		t.Fatalf("idle stop acknowledged; closing the access gap alone admitted the waiting child-report wake, admissions=%d", n)
	}
}

func TestR2UpgradeWithLiveNextRun(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	k := p.place("upgrade")
	b := p.bound(k).Thread
	until(t, "first waiting", func() bool { return p.placeholder(k).Remote.Slot == api.RunWaiting })
	p.submitA(k, settleIt)
	p.settledThere(b)
	first := p.onB(b).Runs[0].ID
	p.submitA(k, api.MessageDispatch{Text: "second", Mode: api.AfterActive})
	until(t, "second waiting", func() bool {
		r := p.placeholder(k).Remote
		return r.Slot == api.RunWaiting && r.Run != first && r.Ended != nil
	})
	p.stopA()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(p.dirA, "engine.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"UPDATE threads SET remote=json_remove(remote,'$.ended') WHERE id=?", "UPDATE events SET payload=CAST(json_remove(payload,'$.ended') AS BLOB) WHERE thread=? AND type='remote.seen'"} {
		if _, err := db.Exec(s, k[:]); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	p.startA()
	p.message(b)
	until(t, "post upgrade message followed", func() bool { return p.placeholder(k).Remote.Taken })
	v := engine.View(p.placeholder(k))
	if v.LastTurn == nil || v.LastTurn.Run != first {
		t.Fatalf("upgraded while second run was waiting: A LastTurn=%+v, B LastTurn=%s", v.LastTurn, engine.View(p.onB(b)).LastTurn.Run)
	}
}

func TestR2LostSettleReceiptReplayed(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	k := p.place("retry")
	b := p.bound(k).Thread
	until(t, "first report", func() bool { rs, _ := p.reports(k); return len(rs) == 1 })
	p.parentIdle()
	x := newProxy(t, p)
	p.machine(x.addr)
	x.mu.Lock()
	x.hold = true
	x.mu.Unlock()
	p.submitA(k, settleIt)
	select {
	case <-x.submitted:
	case <-time.After(10 * time.Second):
		t.Fatal("no lost receipt")
	}
	p.stopA()
	x.stop()
	p.stopB()
	p.startB()
	x.mu.Lock()
	x.hold = false
	x.mu.Unlock()
	x.start()
	p.startA()
	waitStopDone(t, p, k)
	if p.onB(b).Origin.Fence != 1 || p.placeholder(k).Remote.Settles != 1 {
		t.Fatal("fence lost or doubled")
	}
	p.submitA(k, api.MessageDispatch{Text: "new work", Mode: api.AfterActive})
	until(t, "post-settle admission", func() bool { return p.count(b, "turn.admitted") == 2 })
}

func TestR2IdleStopRealReport(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(true)), "fake", engine.Limits{})
	p.adB.Spawns(p.b.Endpoint(), "do it there", fake.Child{Title: "local nested", Task: "go"})
	k := p.place("report parent")
	b := p.bound(k).Thread
	var grand api.Thread
	until(t, "origin thread finished and grandchild awaiting approval", func() bool {
		for _, v := range p.there() {
			if v.Parent == b && v.Run == api.RunWaiting && len(v.Requests) == 1 {
				grand = v
				return p.onB(b).Live() == nil
			}
		}
		return false
	})
	allowChildren(t, p.dirB, api.AccessAsk)
	r := grand.Requests[0]
	_, err := p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: grand.ID, Body: api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: []byte(`{"allow":true}`)}})
	if err != nil {
		t.Fatal(err)
	}
	until(t, "B holding child's completion report", func() bool { v := engine.View(p.onB(b)); return v.Run == api.RunIdle && v.Queued == 1 })
	p.submitA(k, api.TurnInterrupt{Tree: true})
	waitStopDone(t, p, k)
	p.submitA(k, api.ThreadConfigure{Access: new(api.AccessAsk)})
	until(t, "B accepted narrowing", func() bool { return p.onB(b).Access == api.AccessAsk })
	if n := p.count(b, "turn.admitted"); n != 1 {
		t.Fatalf("B reported a real local child's completed turn, acknowledged tree stop, then admitted that wake on narrowing alone; admissions=%d", n)
	}
}

func TestR2UncertainTakenGrandchildUnderSettle(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	p.adB.Spawns(p.b.Endpoint(), "do it there", fake.Child{Title: "grandchild", Task: "ask"})
	k := p.place("tree")
	b := p.bound(k).Thread
	var grand api.ID
	until(t, "waiting grandchild", func() bool {
		for _, th := range p.there() {
			if th.Parent == b && th.Run == api.RunWaiting {
				grand = th.ID
				return true
			}
		}
		return false
	})
	p.message(grand)
	p.stopB()
	p.adB = fake.New(fake.Asking())
	p.startB()
	until(t, "B-local child uncertain", func() bool { return engine.View(p.onB(grand)).Run == api.RunUncertain })
	p.submitA(k, settleIt)
	waitStopDone(t, p, k)
	p.settledThere(b)
	if v := engine.View(p.onB(grand)); v.Disposition.State != api.Settled || v.Queued != 0 {
		t.Fatalf("A's settle finished; B's parent settled, user-taken grandchild remained disposition=%d run=%d queued=%d", v.Disposition.State, v.Run, v.Queued)
	}
}

func TestR2EndedCarriesCompletionTime(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	k := p.place("time")
	b := p.bound(k).Thread
	until(t, "A has B's last turn", func() bool { return p.placeholder(k).Remote.Ended != nil })
	get := func(ep api.Endpoint, id api.ID) api.TurnResult {
		s, err := ep.Read(context.Background(), api.Query{Thread: id})
		if err != nil || s.Threads[0].LastTurn == nil {
			t.Fatalf("missing LastTurn: %v", err)
		}
		return *s.Threads[0].LastTurn
	}
	aLast, bLast := get(p.a.Endpoint(), k), get(p.b.Endpoint(), b)
	if aLast.Run != bLast.Run || bLast.Ended.IsZero() {
		t.Fatal("bad control")
	}
	if !aLast.Ended.Equal(bLast.Ended) || aLast.Access != bLast.Access || aLast.Model != bLast.Model {
		t.Fatalf("same finished run: A ended=%s access=%d model=%q; B ended=%s access=%d model=%q", aLast.Ended, aLast.Access, aLast.Model.Model, bLast.Ended, bLast.Access, bLast.Model.Model)
	}
}

func TestR2OriginalWinsWhileCleanupWaitsForWriter(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	g := delayFrame(t, p, "do it there")
	k := p.place("overlap")
	g.wait(t, g.held)
	b := p.placeholder(k).Remote.Create.Target
	entered, release := make(chan struct{}), make(chan struct{})
	var once, finished sync.Once
	unblock := func() { finished.Do(func() { close(release) }) }
	defer unblock()
	find := links(p.dirB)
	p.b.store.SetLinks(func(id api.ID) (api.Link, error) { once.Do(func() { close(entered); <-release }); return find(id) })
	g.release()
	g.wait(t, entered)
	p.submitA(k, settleIt)
	until(t, "settled cleanup past empty target and waiting for store mutex", func() bool {
		buf := make([]byte, 1<<19)
		n := goruntime.Stack(buf, true)
		for _, s := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(s, "internal/runtime.(*Host).createFromLink(") && strings.Contains(s, "internal/store.(*Store).commit(") && strings.Contains(s, "Mutex") {
				return true
			}
		}
		return false
	})
	unblock()
	g.wait(t, g.answered)
	waitStopDone(t, p, k)
	until(t, "original run state", func() bool { return !p.onB(b).Origin.Making })
	if th := p.onB(b); th.Disposition.State != api.Settled {
		t.Fatalf("original committed before waiting cleanup; A marked stop done, B open, admissions=%d", p.count(b, "turn.admitted"))
	}
}
