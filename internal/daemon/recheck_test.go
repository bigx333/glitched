package daemon

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider/fake"
)

// Delay one client frame and the cancellation or EOF behind it on the same
// TCP connection. A's later RPCs on fresh connections still go straight to B.
type lagFrame struct {
	held, deliver, delivered, answered, tail chan struct{}
	goOnce, tailOnce                         sync.Once
}

func (g *lagFrame) release() { g.goOnce.Do(func() { close(g.deliver) }) }
func (g *lagFrame) finish()  { g.release(); g.tailOnce.Do(func() { close(g.tail) }) }
func (g *lagFrame) wait(t *testing.T, c chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(10 * time.Second):
		t.Fatal("frame not seen")
	}
}
func delayFrame(t *testing.T, p *remotes, needle string) *lagFrame {
	t.Helper()
	g := &lagFrame{held: make(chan struct{}), deliver: make(chan struct{}), delivered: make(chan struct{}), answered: make(chan struct{}), tail: make(chan struct{})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var grabbed atomic.Bool
	go func() {
		for {
			a, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer a.Close()
				b, err := net.Dial("tcp", p.addrB)
				if err != nil {
					return
				}
				defer b.Close()
				var pendingReply atomic.Bool
				go func() {
					buf := make([]byte, 32768)
					for {
						n, err := b.Read(buf)
						if n > 0 {
							if pendingReply.CompareAndSwap(true, false) {
								close(g.answered)
							}
							if _, werr := a.Write(buf[:n]); werr != nil {
								return
							}
						}
						if err != nil {
							return
						}
					}
				}()
				r := bufio.NewReader(a)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if _, err = b.Write([]byte(line)); err != nil {
						return
					}
					if line == "\r\n" {
						break
					}
				}
				delivering := false
				for {
					h := make([]byte, 2)
					if _, err = io.ReadFull(r, h); err != nil {
						return
					}
					n := uint64(h[1] & 127)
					if n > 125 {
						k := 2
						if n == 127 {
							k = 8
						}
						ext := make([]byte, k)
						if _, err = io.ReadFull(r, ext); err != nil {
							return
						}
						h = append(h, ext...)
						n = 0
						for _, v := range ext {
							n = n<<8 | uint64(v)
						}
					}
					if h[1]&128 == 0 || n > 1<<20 {
						return
					}
					var mask [4]byte
					if _, err = io.ReadFull(r, mask[:]); err != nil {
						return
					}
					data := make([]byte, int(n))
					if _, err = io.ReadFull(r, data); err != nil {
						return
					}
					plain := bytes.Clone(data)
					for i := range plain {
						plain[i] ^= mask[i%4]
					}
					held := bytes.Contains(plain, []byte(needle)) && grabbed.CompareAndSwap(false, true)
					if held {
						close(g.held)
						<-g.deliver
						delivering = true
					}
					end := delivering && h[0]&128 != 0 && h[0]&15 < 8
					if end {
						pendingReply.Store(true)
					}
					if _, err = b.Write(append(append(h, mask[:]...), data...)); err != nil {
						return
					}
					if end {
						delivering = false
						close(g.delivered)
						<-g.tail
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { g.finish(); ln.Close() })
	p.machine(ln.Addr().String())
	return g
}

func TestRecheckInFlightSendAfterSettle(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	kid := p.place("delayed send")
	there := p.bound(kid).Thread
	until(t, "first finished", func() bool { r, _ := p.reports(kid); return len(r) == 1 })
	g := delayFrame(t, p, "message delayed on the wire")
	p.submitA(kid, api.MessageDispatch{Text: "message delayed on the wire", Mode: api.AfterActive})
	g.wait(t, g.held)
	p.submitA(kid, settleIt)
	p.settledThere(there)
	g.release()
	g.wait(t, g.delivered)
	g.wait(t, g.answered)
	if n := p.count(there, "turn.admitted"); n != 1 || p.onB(there).Disposition.State != api.Settled {
		t.Fatalf("A remains settled=%t; B settled=%t and admitted %d turns after a pre-settle message arrived late", p.placeholder(kid).Disposition.State == api.Settled, p.onB(there).Disposition.State == api.Settled, n)
	}
}

func TestRecheckLateOriginalAfterCleanup(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		name := "accepted"
		if refuse {
			name = "temporarily_missing_branch"
		}
		t.Run(name, func(t *testing.T) {
			p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
			g := delayFrame(t, p, "do it there")
			kid := p.place("delayed create")
			g.wait(t, g.held)
			there := p.placeholder(kid).Remote.Create.Target
			if refuse {
				gitIn(t, p.repo, "branch", "-m", "main", "renamed")
			}
			p.submitA(kid, settleIt)
			waitStopDone(t, p, kid)
			notLost(t, p, kid)
			if refuse {
				gitIn(t, p.repo, "branch", "-m", "renamed", "main")
			}
			g.release()
			g.wait(t, g.delivered)
			g.wait(t, g.answered)
			if !p.onB(there).Exists() {
				if !refuse {
					t.Fatal("an accepted cleanup should have created the settled child")
				}
				return
			}
			until(t, "receiver finished making worktree", func() bool { return !p.onB(there).Origin.Making })
			if th := p.onB(there); th.Disposition.State != api.Settled || p.count(there, "turn.admitted") != 0 {
				t.Fatalf("cleanup finished on A; late original now settled=%t on B with %d turns admitted", th.Disposition.State == api.Settled, p.count(there, "turn.admitted"))
			}
		})
	}
}

func TestRecheckStatusAfterCancelDuringWorktreeMake(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{Working: 1})
	p.asking()
	gate := filepath.Join(t.TempDir(), "release")
	hook := []byte("#!/bin/sh\nwhile [ ! -f '" + gate + "' ]; do sleep 0.02; done\n")
	if err := os.WriteFile(filepath.Join(p.repo, ".git", "hooks", "post-checkout"), hook, 0755); err != nil {
		t.Fatal(err)
	}
	release := func() { os.WriteFile(gate, nil, 0644) }
	defer release()
	kid := p.place("held")
	there := p.bound(kid).Thread
	p.submitA(p.parent, api.MessageDispatch{Text: "await instructions", Mode: api.AfterActive})
	if _, why := p.tool("task_cancel", taskArgs(kid, "")); why != "" {
		t.Fatal(why)
	}
	until(t, "first task held on B", func() bool { return p.count(there, "message.held") == 1 })
	release()
	until(t, "worktree ready", func() bool { return p.count(there, "worktree.ready") == 1 })
	r, _, _, err := engine.Follow(*p.placeholder(kid).Remote, eventsOf(t, p.b.Endpoint(), there), true)
	if err != nil {
		t.Fatal(err)
	}
	status, why := p.tool("task_status", taskArgs(kid, ""))
	if why != "" {
		t.Fatal(why)
	}
	if r.Slot != api.RunIdle && status.Status == "working" {
		t.Fatalf("after worktree.ready B has live=%t and %d turns; A's task_status=%q, even the full fold says starting=%t", p.onB(there).Live() != nil, p.count(there, "turn.admitted"), status.Status, r.Slot == api.RunStarting)
	}
}

func TestRecheckSettlePreservesGrandchildTakeover(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	p.adB.Spawns(p.b.Endpoint(), "do it there", fake.Child{Title: "grandchild", Task: "local work"})
	kid := p.place("remote")
	there := p.bound(kid).Thread
	var grand api.ID
	until(t, "B's local grandchild waiting", func() bool {
		for _, th := range p.there() {
			if th.Parent == there && th.Run == api.RunWaiting {
				grand = th.ID
				return true
			}
		}
		return false
	})
	p.message(grand)
	ctx := context.Background()
	try := func() error {
		until(t, "B's parent waiting", func() bool { return p.onB(there).Live() != nil })
		_, err := p.b.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: grand, Caller: &api.Caller{Thread: there, Run: p.onB(there).Live().ID}, Body: api.MessageDispatch{Text: "agent work", Mode: api.AfterActive}})
		return err
	}
	var rej *api.RejectedError
	if err := try(); !errors.As(err, &rej) || !strings.Contains(rej.Reason, "user took this thread over") {
		t.Fatalf("before settle: %v", err)
	}
	p.submitA(kid, settleIt)
	p.settledThere(there)
	p.settledThere(grand)
	p.submitA(kid, api.MessageDispatch{Text: "another turn", Mode: api.AfterActive})
	if err := try(); err == nil {
		t.Fatal("B refused the parent agent's message to the user-taken grandchild before A's tree settle, then accepted it after the settle")
	}
}

func TestRecheckLastTurnMatchesFinishedRun(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	kid := p.place("two runs")
	there := p.bound(kid).Thread
	until(t, "B waiting", func() bool { return p.placeholder(kid).Remote.Slot == api.RunWaiting })
	p.submitA(kid, settleIt)
	p.settledThere(there)
	first := p.onB(there).Runs[0].ID
	p.submitA(kid, api.MessageDispatch{Text: "second run", Mode: api.AfterActive})
	until(t, "new run followed", func() bool { r := p.placeholder(kid).Remote; return r.Slot == api.RunWaiting && r.Run != first })
	snapshot, err := p.a.Endpoint().Read(context.Background(), api.Query{Thread: kid})
	if err != nil {
		t.Fatal(err)
	}
	v := snapshot.Threads[0]
	if v.LastTurn == nil || v.LastTurn.Run != first {
		t.Fatalf("A's LastTurn=%+v; finished run=%s, current unfinished run=%s", v.LastTurn, first, p.onB(there).Live().ID)
	}
}

func TestRecheckConcurrentCreateBeatsCleanupAtCommit(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	g := delayFrame(t, p, "do it there")
	kid := p.place("both preflight a new thread")
	g.wait(t, g.held)
	there := p.placeholder(kid).Remote.Create.Target
	p.submitA(kid, settleIt)
	waitStopDone(t, p, kid)
	notLost(t, p, kid)
	g.release()
	g.wait(t, g.answered)
	if !p.onB(there).Exists() {
		t.Fatal("original create did not win the commit")
	}
	until(t, "worktree decided on B", func() bool { return !p.onB(there).Origin.Making })
	if th := p.onB(there); th.Disposition.State != api.Settled {
		t.Fatalf("both creates saw an empty target; the first create won, A completed its cleanup, B was left unsettled with %d turn(s)", p.count(there, "turn.admitted"))
	}
}

func TestRecheckUnacknowledgedCreateKeepsNarrowing(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	x := newProxy(t, p)
	x.mu.Lock()
	x.hold = true
	x.mu.Unlock()
	p.machine(x.addr)
	kid := p.place("create receipt delayed")
	select {
	case <-x.submitted:
	case <-time.After(10 * time.Second):
		t.Fatal("missing original create")
	}
	there := p.placeholder(kid).Remote.Create.Target
	until(t, "B is waiting", func() bool { return p.count(there, "request.opened") > 0 })
	p.submitA(kid, api.ThreadConfigure{Access: new(api.AccessAsk)})
	x.mu.Lock()
	x.hold = false
	x.mu.Unlock()
	p.submitA(kid, settleIt)
	p.settledThere(there)
	p.message(there)
	until(t, "B resumed", func() bool { return p.count(there, "turn.admitted") >= 2 })
	if a := p.onB(there).Live().Access; a != api.AccessAsk {
		t.Fatalf("original create reached B without its receipt reaching A: A narrowed to Ask then settled, B's next run has access=%d (AutoEdits=%d)", a, api.AccessAutoEdits)
	}
}

func TestRecheckInterruptAfterWorktreeBeforeFirstRun(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, true)
	thread := l.mustCreate(api.AccessAutoEdits)
	l.making(thread)
	allowChildren(t, l.dirB, api.AccessAsk)
	l.open()
	l.waitFor(thread, "worktree.ready", 1)
	if l.count(thread, "turn.admitted") != 0 || l.view(thread).Queued != 1 {
		t.Fatal("need a ready workspace and first message waiting on its ceiling")
	}
	if err := l.send(thread, api.TurnInterrupt{Tree: true}); err != nil {
		t.Fatal(err)
	}
	if err := l.send(thread, api.ThreadConfigure{Access: new(api.AccessAsk)}); err != nil {
		t.Fatal(err)
	}
	if n := l.count(thread, "turn.admitted"); n != 0 {
		t.Fatalf("B acknowledged the stop after worktree.ready with no admitted turns; narrowing alone admitted %d turn(s) from the cancelled first message", n)
	}
}
func waitStopDone(t *testing.T, p *remotes, kid api.ID) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(p.dirA, "engine.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	until(t, "cleanup completed", func() bool {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM effects WHERE thread=? AND kind='remote.stop' AND state='done'", kid[:]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	})
}

func TestRecheckFinishedChildOnUpgrade(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	p.asking()
	kid := p.place("upgrade cursor")
	there := p.bound(kid).Thread
	until(t, "completed outcome folded", func() bool { return p.placeholder(kid).Remote.Ended != nil })
	// The report's wake is the parent's to stop, once it has it.
	until(t, "the parent's report", func() bool { reports, _ := p.reports(kid); return len(reports) == 1 })
	p.submitA(p.parent, api.TurnInterrupt{})
	p.parentIdle()
	p.stopA()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(p.dirA, "engine.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{"UPDATE threads SET remote=json_remove(remote,'$.ended') WHERE id=?", "UPDATE events SET payload=CAST(json_remove(payload,'$.ended') AS BLOB) WHERE thread=? AND type='remote.seen'"} {
		if _, err := db.Exec(stmt, kid[:]); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()
	p.startA()
	p.submitA(p.parent, api.MessageDispatch{Text: "await status", Mode: api.AfterActive})
	_, err = p.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: there, Body: api.MessageDispatch{Text: "deferred", Mode: api.Defer}})
	if err != nil {
		t.Fatal(err)
	}
	until(t, "B's post-restart change followed", func() bool { return p.placeholder(kid).Remote.Taken })
	s, why := p.tool("task_status", taskArgs(kid, ""))
	if why != "" {
		t.Fatal(why)
	}
	if s.Status != "done" {
		t.Fatalf("using a stored cursor from before Ended existed, A followed a new event without a new run; task_status=%q, last_report=%q, B LastTurn=%v", s.Status, s.LastReport, engine.View(p.onB(there)).LastTurn.Outcome)
	}
}

func TestRecheckCleanupReceiptReplay(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	x := newProxy(t, p)
	x.stop()
	p.machine(x.addr)
	kid := p.place("replay cleanup")
	until(t, "create attempted", func() bool { return p.placeholder(kid).Remote.Attempted })
	p.submitA(kid, settleIt)
	until(t, "stop attempting", func() bool { return slices.Contains(p.leased(kid), "remote.stop") })
	x.mu.Lock()
	x.hold = true
	x.mu.Unlock()
	x.start()
	select {
	case <-x.submitted:
	case <-time.After(10 * time.Second):
		t.Fatal("no cleanup create")
	}
	there := p.placeholder(kid).Remote.Create.Target
	if p.onB(there).Disposition.State != api.Settled {
		t.Fatal("cleanup create was not settled on B")
	}
	p.stopA()
	x.mu.Lock()
	x.hold = false
	x.mu.Unlock()
	p.startA()
	waitStopDone(t, p, kid)
	notLost(t, p, kid)
	p.settledThere(there)
	if len(p.there()) != 1 || p.count(there, "turn.admitted") != 0 {
		t.Fatal("replay duplicated or started B's child")
	}
}

func TestRecheckOldFirstMessageOnB(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, false)
	thread := l.mustCreate(api.AccessAsk)
	l.waitFor(thread, "turn.ended", 1)
	l.close()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(l.dirB, "engine.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE events SET payload=CAST(json_remove(payload,'$.by') AS BLOB) WHERE thread=? AND type='message.queued'", thread[:])
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	l.startB()
	if err := l.send(thread, api.MessageDispatch{Text: "still the origin's", Mode: api.AfterActive}); err != nil {
		t.Fatalf("legacy create rejected its origin's follow-up: %v", err)
	}
	l.waitFor(thread, "turn.ended", 2)
}

func notLost(t *testing.T, p *remotes, kid api.ID) {
	t.Helper()
	if r := p.placeholder(kid).Remote; r.State == engine.RemoteLostState || r.Lost != "" {
		t.Fatalf("the cleanup lost the child: %q", r.Lost)
	}
}
