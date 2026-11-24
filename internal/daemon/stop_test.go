package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/mcp"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

var settleIt = api.InboxSet{Disposition: api.Disposition{State: api.Settled}}

// asking is A's agent as a parent that waits on a question each turn,
// so the turn a child's report wakes stays live to act on the child.
func (p *remotes) asking() {
	p.t.Helper()
	p.stopA()
	p.adA = fake.New(fake.Asking())
	p.startA()
}

// tool is A's parent's agent calling the task tool name with args through
// glitched mcp: what it answered, or its refusal.
func (p *remotes) tool(name, args string) (mcp.TaskState, string) {
	p.t.Helper()
	until(p.t, "the parent's turn live", func() bool {
		snap, err := p.a.Endpoint().Read(context.Background(), api.Query{Thread: p.parent})
		return err == nil && snap.Threads[0].Turn != nil && snap.Threads[0].Run == api.RunWaiting
	})
	line := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + "}}\n"
	var out bytes.Buffer
	tools := &mcp.Tools{Engine: p.a.Endpoint(), Origin: mcp.Origin{Thread: p.parent, Access: api.AccessAutoEdits}}
	if err := mcp.Serve(context.Background(), strings.NewReader(line), &out, tools); err != nil {
		p.t.Fatal(err)
	}
	var r struct {
		Result struct {
			IsError bool                    `json:"isError"`
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || len(r.Result.Content) == 0 {
		p.t.Fatalf("%s: %q %v", name, out.String(), err)
	}
	if r.Result.IsError {
		return mcp.TaskState{}, r.Result.Content[0].Text
	}
	var s mcp.TaskState
	json.Unmarshal([]byte(r.Result.Content[0].Text), &s)
	return s, ""
}

func taskArgs(kid api.ID, more string) string {
	return `{"task_id":"` + kid.String() + `"` + more + "}"
}

// onB is B's thread as B's store has it.
func (p *remotes) onB(thread api.ID) engine.Thread {
	p.t.Helper()
	th, err := p.b.store.Thread(context.Background(), thread)
	if err != nil {
		p.t.Fatal(err)
	}
	return th
}

// count is how many events of type B's log holds of thread.
func (p *remotes) count(thread api.ID, typ string) int {
	p.t.Helper()
	n := 0
	for _, e := range eventsOf(p.t, p.b.Endpoint(), thread) {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// leased are the kinds of A's effects leased now for thread.
func (p *remotes) leased(thread api.ID) []string {
	p.t.Helper()
	es, err := p.a.store.Leased(context.Background())
	if err != nil {
		p.t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		if e.Thread == thread {
			out = append(out, engine.EffectKind(e.Body))
		}
	}
	return out
}

func (p *remotes) submitA(target api.ID, b api.Intent) api.Receipt {
	p.t.Helper()
	r, err := p.a.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: target, Body: b})
	if err != nil {
		p.t.Fatalf("%s: %v", api.IntentType(b), err)
	}
	return r
}

// settledThere waits for B's thread to be settled with nothing running,
// and keeping its transcript.
func (p *remotes) settledThere(thread api.ID) {
	p.t.Helper()
	until(p.t, "the child settled on b", func() bool {
		th := p.onB(thread)
		return th.Disposition.State == api.Settled && th.Live() == nil
	})
	if th := p.onB(thread); th.Deleted || p.count(thread, "message.queued") == 0 {
		p.t.Fatalf("the child on b after its settle: deleted %v, %d messages; want it kept with its transcript", th.Deleted, p.count(thread, "message.queued"))
	}
}

// Settling A's tree settles its remote child in the same commit on A,
// and on B once B is up again.
func TestSettlingATreeSettlesItsRemoteChildThere(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	kid := p.place("there")
	there := p.bound(kid).Thread
	until(t, "the child waiting on b", func() bool { return p.placeholder(kid).Remote.Slot == api.RunWaiting })
	p.stopB()
	before, err := p.a.store.ReadLog(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	r := p.submitA(p.parent, settleIt)
	after, err := p.a.store.ReadLog(context.Background(), uint64(len(before)), 0)
	if err != nil {
		t.Fatal(err)
	}
	var kidAt, parentAt uint64
	for _, e := range after {
		if e.Type == "disposition.changed" && e.Thread == kid {
			kidAt = e.Cursor.Seq
		}
		if e.Type == "disposition.changed" && e.Thread == p.parent {
			parentAt = e.Cursor.Seq
		}
	}
	if kidAt == 0 || parentAt == 0 || kidAt > r.Committed.Seq || parentAt > r.Committed.Seq {
		t.Fatalf("the settle: placeholder at %d, parent at %d, its commit up to %d; want both in it", kidAt, parentAt, r.Committed.Seq)
	}
	until(t, "the stop trying with b down", func() bool {
		l := p.leased(kid)
		return len(l) == 1 && l[0] == "remote.stop"
	})
	p.startB()
	p.settledThere(there)
	folded(t, p.a.store)
}

// A settle or a delete between the create found due and its first try
// recorded leaves the create untried: nothing dials B.
func TestASettleBeforeTheFirstTryDialsNothing(t *testing.T) {
	for _, body := range []api.Intent{settleIt, api.ThreadDelete{}} {
		t.Run(api.IntentType(body), func(t *testing.T) {
			p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
			x := newProxy(t, p)
			p.machine(x.addr)
			held, release := make(chan api.ID, 1), make(chan struct{})
			p.steps = func(step string, id api.ID) {
				if step == "due" {
					held <- id
					<-release
				}
			}
			p.stopA()
			p.startA()
			kid := p.place("held")
			select {
			case id := <-held:
				if id != kid {
					t.Fatalf("held %v; want the placeholder %v", id, kid)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the create never came due")
			}
			p.submitA(kid, body)
			close(release)
			until(t, "the create done", func() bool { return len(p.leased(kid)) == 0 })
			time.Sleep(300 * time.Millisecond)
			x.mu.Lock()
			infos := x.infos
			x.mu.Unlock()
			if infos != 0 || len(p.there()) != 0 {
				t.Fatalf("%d dials of b, %d threads there; want none", infos, len(p.there()))
			}
			if r := p.placeholder(kid).Remote; r.Attempted {
				t.Fatalf("the placeholder %+v; want it never tried", r)
			}
		})
	}
}

// A settle while the create retries with B down ends its tries, so the
// settle's stop has the lane; once B is up the stop sends the create and
// then the settle, and the child B never saw is settled there before any
// turn of it starts.
func TestASettleWhileACreateRetriesHandsTheLaneToTheStop(t *testing.T) {
	adB := fake.New(fake.Turn(false))
	p := newRemotes(t, adB, "fake", engine.Limits{})
	tries := make(chan struct{}, 64)
	p.steps = func(step string, _ api.ID) {
		if step == "try" {
			tries <- struct{}{}
		}
	}
	p.stopA()
	p.stopB()
	p.startA()
	kid := p.place("retrying")
	for range 2 {
		select {
		case <-tries:
		case <-time.After(10 * time.Second):
			t.Fatal("the create isn't retrying")
		}
	}
	p.submitA(kid, settleIt)
	until(t, "the stop on the lane", func() bool {
		l := p.leased(kid)
		return len(l) == 1 && l[0] == "remote.stop"
	})
	if r := p.placeholder(kid).Remote; r.State != engine.RemoteCreating || !r.Attempted || r.Create == nil {
		t.Fatalf("the placeholder %+v; want it tried and unbound", r)
	}
	p.startB()
	there := p.placeholder(kid).Remote.Create.Target
	until(t, "the child on b", func() bool { return p.onB(there).Exists() })
	p.settledThere(there)
	until(t, "the stop done", func() bool { return len(p.leased(kid)) == 0 })
	if n := p.count(there, "turn.admitted"); n != 0 || adB.Starts() != 0 {
		t.Fatalf("%d turns admitted on b, %d started; want none", n, adB.Starts())
	}
	folded(t, p.a.store)
}

// A delete on A, across a restart of A before B is back, leaves B's
// child settled with its transcript.
func TestADeleteLeavesTheChildSettledThereAcrossARestart(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	kid := p.place("there")
	there := p.bound(kid).Thread
	until(t, "the child waiting on b", func() bool { return p.placeholder(kid).Remote.Slot == api.RunWaiting })
	p.stopB()
	p.submitA(p.parent, api.ThreadDelete{})
	if !p.placeholder(kid).Deleted {
		t.Fatal("the placeholder outlived its tree's delete")
	}
	p.stopA()
	p.startA()
	p.startB()
	p.settledThere(there)
	if n := p.count(there, "request.opened"); n == 0 {
		t.Fatal("b's child lost its transcript")
	}
}

// A task_cancel with B down is recorded on A at once, and stops the run
// on B once B is up, as the parent's agent's stop.
func TestACancelWithBDownStopsTheRunThereAfter(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	p.asking()
	kid := p.place("there")
	there := p.bound(kid).Thread
	until(t, "the child waiting on b", func() bool { return p.placeholder(kid).Remote.Slot == api.RunWaiting })
	run := p.onB(there).Live().ID
	p.stopB()
	if _, err := p.tool("task_cancel", taskArgs(kid, "")); err != "" {
		t.Fatalf("task_cancel with b down: %s", err)
	}
	until(t, "the stop on the lane", func() bool {
		l := p.leased(kid)
		return len(l) == 1 && l[0] == "remote.stop"
	})
	p.startB()
	until(t, "the run stopped on b", func() bool { th := p.onB(there); return th.Live() == nil })
	var by api.ID
	for _, e := range eventsOf(t, p.b.Endpoint(), there) {
		if b, _ := e.Decode(); b != nil {
			if s, ok := b.(api.StopAsked); ok && s.Run == run {
				by = s.By
			}
		}
	}
	if by != p.parent {
		t.Fatalf("the stop on b was asked by %v; want the parent's agent, %v", by, p.parent)
	}
}

// A message to a remote child whose run is done starts a new run there,
// whose end reports again.
func TestAMessageToAFinishedRemoteChildReportsItsNewRun(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	p.asking()
	kid := p.place("there")
	until(t, "the first report", func() bool { reports, _ := p.reports(kid); return len(reports) == 1 })
	if s, err := p.tool("task_send", taskArgs(kid, `,"message":"and more"`)); err != "" {
		t.Fatalf("task_send: %s %+v", err, s)
	}
	until(t, "the second report", func() bool { reports, _ := p.reports(kid); return len(reports) == 2 })
	reports, _ := p.reports(kid)
	if reports[0].Kind != api.ReportDone || reports[1].Kind != api.ReportDone || reports[0].Run == reports[1].Run {
		t.Fatalf("reports %+v; want two done, of two runs", reports)
	}
	s, err := p.tool("task_status", taskArgs(kid, ""))
	if err != "" || !strings.HasPrefix(s.On, "on b, last heard at ") {
		t.Fatalf("task_status: %+v %s; want it on b, heard from", s, err)
	}
	if n := p.count(p.bound(kid).Thread, "turn.admitted"); n != 2 {
		t.Fatalf("%d runs on b; want 2", n)
	}
}

// Once B's person messages B's child, B refuses the link's message and
// stop for it, and A's task_send and task_cancel are refused.
func TestOnceBsPersonTakesTheChildOverTheParentCantSteerIt(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	p.asking()
	kid := p.place("there")
	there := p.bound(kid).Thread
	until(t, "the child waiting on b", func() bool { return p.placeholder(kid).Remote.Slot == api.RunWaiting })
	link := transport.NewEndpointClient(transport.Tailnet{Address: p.addrB, Token: p.token}, api.ID{})
	defer link.Close()
	ctx := context.Background()
	if _, err := link.Submit(ctx, api.Command{ID: api.NewID(), Target: there, Body: api.MessageDispatch{Text: "from a", Mode: api.AfterActive}}); err != nil {
		t.Fatalf("the link's message before b's person's: %v", err)
	}
	if r := p.placeholder(kid).Remote; r.Taken {
		t.Fatal("the link's own message took the child over")
	}
	p.message(there)
	for _, b := range []api.Intent{api.MessageDispatch{Text: "from a", Mode: api.AfterActive}, api.TurnInterrupt{Tree: true}} {
		_, err := link.Submit(ctx, api.Command{ID: api.NewID(), Target: there, Body: b})
		var rej *api.RejectedError
		if !errors.As(err, &rej) || !strings.Contains(rej.Reason, "took this thread over") {
			t.Fatalf("the link's %s once b's person took the child over: %v; want refused", api.IntentType(b), err)
		}
	}
	until(t, "a sees b's person took it over", func() bool { return p.placeholder(kid).Remote.Taken })
	for _, c := range []struct{ name, args string }{{"task_send", `,"message":"go on"`}, {"task_cancel", ""}} {
		if _, err := p.tool(c.name, taskArgs(kid, c.args)); !strings.Contains(err, "took it over there") {
			t.Fatalf("%s once b's person took the child over: %q; want refused", c.name, err)
		}
	}
	if th := p.onB(there); th.Live() == nil {
		t.Fatal("the child's run on b was stopped")
	}
}

// A revoke on B while its child runs blocks A's stop: the child is lost
// here, saying its person on B stops it by hand, and it still runs there.
func TestARevokeBlocksTheStopAndSaysSo(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	kid := p.place("there")
	there := p.bound(kid).Thread
	until(t, "the child waiting on b", func() bool { return p.placeholder(kid).Remote.Slot == api.RunWaiting })
	if err := Revoke(p.dirB, "a"); err != nil {
		t.Fatal(err)
	}
	p.submitA(kid, api.TurnInterrupt{})
	p.lostOnce(kid, "its person's to stop by hand")
	until(t, "the stop done", func() bool { return len(p.leased(kid)) == 0 })
	if th := p.onB(there); th.Live() == nil {
		t.Fatal("the child on b stopped through a revoked link")
	}
}
