package daemon

import (
	"context"
	"errors"
	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func cleanupFor(c api.Command) api.Command {
	b := c.Body.(api.ThreadCreate)
	b.Settled = true
	c.ID = api.NewID()
	c.Body = b
	return c
}

func TestR3CleanupInert(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAsk, All: 1}, false)
	l.mustCreate(api.AccessAsk)
	shell, err := l.b.Endpoint().Read(context.Background(), api.Query{})
	if err != nil {
		t.Fatal(err)
	}
	var local api.ID
	for _, th := range shell.Threads {
		if th.Title == "mine" {
			local = th.ID
		}
	}
	if local.IsZero() {
		t.Fatal("no local control")
	}
	localControl := cleanupFor(l.create(api.AccessAsk, 1))
	localControl.Target = local
	if _, err := l.ep.Submit(context.Background(), localControl); !errors.Is(err, api.ErrNotAllowed) {
		t.Fatalf("foreign local control: %v", err)
	}
	c := cleanupFor(l.create(api.AccessFull, api.MaxDepth+1))
	if _, err := l.ep.Submit(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	tok, _, err := PairLink(l.dirB, "second", api.Link{Access: api.AccessAsk})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.client(tok).Submit(context.Background(), cleanupFor(c)); !errors.Is(err, api.ErrNotAllowed) {
		t.Fatalf("other link marker control: %v", err)
	}
	l.close()
	l.startB()
	if err := l.send(c.Target, api.MessageDispatch{Text: "new work", Mode: api.AfterActive}); err == nil {
		t.Fatal("cleanup received work")
	}
	s, err := l.b.Endpoint().Read(context.Background(), api.Query{Thread: c.Target})
	if err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(l.dirB, "worktrees", "*", strings.ToLower(c.Target.String())))
	if err != nil {
		t.Fatal(err)
	}
	if s.Threads[0].Workspace != "" || s.Threads[0].Disposition.State != api.Settled || len(paths) != 0 || l.count(c.Target, "turn.admitted") != 0 {
		t.Fatal("cleanup was not inert")
	}
}

func TestR3CleanupRechecksLink(t *testing.T) {
	ctx := context.Background()
	l := newLinked(t, api.Link{Access: api.AccessAsk}, false)
	original := l.create(api.AccessAsk, 1)
	if _, err := l.ep.Submit(ctx, original); err != nil {
		t.Fatal(err)
	}
	l.waitFor(original.Target, "turn.ended", 1)
	var calls atomic.Int32
	find := links(l.dirB)
	l.b.store.SetLinks(func(id api.ID) (api.Link, error) { calls.Add(1); return find(id) })
	if _, err := l.ep.Submit(ctx, cleanupFor(original)); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n == 0 {
		t.Fatal("existing-target cleanup completed with zero reads of current grant/setting")
	}
}

func TestR3UnboundCleanupOnRevoke(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	x := newProxy(t, p)
	x.stop()
	p.machine(x.addr)
	k := p.place("revoked before cleanup")
	until(t, "attempted unbound child", func() bool { return p.placeholder(k).Remote.Attempted })
	p.submitA(k, settleIt)
	if err := Revoke(p.dirB, "a"); err != nil {
		t.Fatal(err)
	}
	x.start()
	why := p.lost(k)
	if !strings.Contains(why, "stop") || len(p.there()) != 0 {
		t.Fatalf("lost reason=%q remote children=%d", why, len(p.there()))
	}
}

func TestR3CleanupCommitSettlesWholeTree(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	p.adB.Spawns(p.b.Endpoint(), "do it there", fake.Child{Title: "nested", Task: "ask"})
	k := p.place("cleanup while bound")
	b := p.bound(k).Thread
	var nested api.ID
	until(t, "nested thread waiting", func() bool {
		for _, v := range p.there() {
			if v.Parent == b && v.Run == api.RunWaiting {
				nested = v.ID
				return true
			}
		}
		return false
	})
	var frozen api.Command
	for _, e := range eventsOf(t, p.a.Endpoint(), k) {
		x, _ := e.Decode()
		if a, ok := x.(api.RemoteAttempted); ok && a.Create != nil {
			frozen = *a.Create
		}
	}
	if frozen.Target != b {
		t.Fatal("missing original create")
	}
	ctx := api.WithLink(context.Background(), p.link)
	if _, err := p.b.Endpoint().Submit(ctx, cleanupFor(frozen)); err != nil {
		t.Fatal(err)
	}
	if p.onB(b).Disposition.State != api.Settled {
		t.Fatal("cleanup did not settle root")
	}
	sub := engine.View(p.onB(nested))
	if sub.Disposition.State != api.Settled {
		t.Fatalf("existing-target cleanup committed; local descendant still open, run=%v", sub.Run)
	}
}

func TestR3QuietWakeQueue(t *testing.T) {
	for _, act := range []string{"resume", "defer"} {
		t.Run(act, func(t *testing.T) {
			ctx := context.Background()
			l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, false)
			k := l.mustCreate(api.AccessAutoEdits)
			l.waitFor(k, "turn.ended", 1)
			if err := l.send(k, api.TurnInterrupt{}); err != nil {
				t.Fatal(err)
			}
			if _, err := l.b.store.Observe(ctx, k, engine.Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Title: "nested", Kind: api.ReportDone}, Reply: "done"}, time.Now(), 0); err != nil {
				t.Fatal(err)
			}
			l.close()
			l.startB()
			before := l.view(k)
			if before.Queued != 1 || before.Run != api.RunIdle {
				t.Fatalf("expected idle held wake: %+v", before)
			}
			var in api.Intent = api.QueueResume{}
			if act == "defer" {
				in = api.MessageDispatch{Text: "save for later", Mode: api.Defer}
			}
			if _, err := l.b.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: k, Body: in}); err != nil {
				t.Fatal(err)
			}
			n := l.count(k, "turn.admitted")
			if act == "resume" && (n != 2 || !before.Resumable) {
				t.Fatalf("resume offered=%v; explicit resume changed admissions from 1 to %d", before.Resumable, n)
			}
			if act == "defer" && n != 1 {
				t.Fatalf("adding a deferred message admitted the previously stopped report wake; admissions=%d", n)
			}
		})
	}
}

func TestR3RemoteLastTurnCancel(t *testing.T) {
	p := newRemotes(t, fake.New([]provider.Event{provider.TurnEnded{Outcome: api.OutcomeFailed, Error: "quota", Limit: &api.UsageLimit{Resets: time.Now().Add(time.Hour)}}}), "fake", engine.Limits{})
	k := p.place("quota")
	b := p.bound(k).Thread
	until(t, "failure with limit followed", func() bool { r := p.placeholder(k).Remote; return r.Ended != nil && r.Ended.Limit != nil })
	ctx := context.Background()
	run := p.placeholder(k).Remote.Ended.Run
	if _, err := p.b.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: b, Body: api.ResumeCancel{Run: run}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.b.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: b, Body: api.MessageDispatch{Text: "hold", Mode: api.Defer}}); err != nil {
		t.Fatal(err)
	}
	until(t, "A observed subsequent message", func() bool { return p.placeholder(k).Remote.Taken })
	aLast, bLast := engine.View(p.placeholder(k)).LastTurn, engine.View(p.onB(b)).LastTurn
	if aLast.Run != bLast.Run || !bLast.ResumeCancelled {
		t.Fatal("bad control")
	}
	if !aLast.ResumeCancelled {
		t.Fatal("A still reports uncancelled limit for the same completed run after B cancelled and A caught up")
	}
}
