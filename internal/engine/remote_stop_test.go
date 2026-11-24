package engine_test

import (
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// creating is a remote child's placeholder whose machine hasn't taken it,
// its create tried when tried.
func creating(t *testing.T, tried bool) (engine.Thread, api.Command) {
	t.Helper()
	at := api.Remote{Engine: api.NewID(), Link: api.NewID()}
	th := engine.Apply(engine.Thread{}, engine.Event{Thread: api.NewID(), At: time.Now(), Body: api.ThreadCreated{Title: "there", Parent: api.NewID(), Access: api.AccessAutoEdits, Remote: &at}})
	create := api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Title: "there", Text: "go"}}
	if tried {
		th = engine.Apply(th, engine.Event{Thread: th.ID, At: time.Now(), Body: api.RemoteAttempted{Create: &create}})
	}
	return th, create
}

func decide(t *testing.T, th engine.Thread, b api.Intent, caller *api.Caller) ([]engine.Event, []engine.Effect, error) {
	t.Helper()
	return engine.Decide(th, api.Command{ID: api.NewID(), Target: th.ID, Body: b, Caller: caller}, time.Now())
}

func stops(effs []engine.Effect) []engine.RemoteStop {
	var out []engine.RemoteStop
	for _, e := range effs {
		if s, ok := e.(engine.RemoteStop); ok {
			out = append(out, s)
		}
	}
	return out
}

var settle = api.InboxSet{Disposition: api.Disposition{State: api.Settled}}

// A placeholder whose create was never tried has nothing on its machine:
// settling or deleting it sends nothing there.
func TestANeverTriedPlaceholderSettlesWithNothingSent(t *testing.T) {
	th, _ := creating(t, false)
	for _, b := range []api.Intent{settle, api.ThreadDelete{}} {
		evs, effs, err := decide(t, th, b, nil)
		if err != nil || len(evs) == 0 || len(effs) != 0 {
			t.Fatalf("%T: %v %v %v; want it recorded here and nothing sent", b, evs, effs, err)
		}
	}
}

// A placeholder tried but not bound is settled there by a stop that sends
// its create first, on a settle and on a delete alike; one bound is
// settled with no create.
func TestASettleOrDeleteSettlesTheChildThere(t *testing.T) {
	tried, create := creating(t, true)
	bound := placeholder(t)
	for _, c := range []struct {
		name   string
		th     engine.Thread
		create bool
	}{{"tried", tried, true}, {"bound", bound, false}} {
		for _, b := range []api.Intent{settle, api.ThreadDelete{}} {
			evs, effs, err := decide(t, c.th, b, nil)
			s := stops(effs)
			if err != nil || len(s) != 1 || len(effs) != 1 {
				t.Fatalf("%s %T: %v %v; want one stop", c.name, b, effs, err)
			}
			if body, ok := s[0].Command.Body.(api.InboxSet); !ok || body.Disposition.State != api.Settled {
				t.Fatalf("%s %T: the stop sends %+v; want a settle there", c.name, b, s[0].Command.Body)
			}
			if c.create != (s[0].Create != nil) || c.create && (s[0].Create.Target != create.Target || s[0].Create.ID == create.ID || !s[0].Create.Body.(api.ThreadCreate).Settled) {
				t.Fatalf("%s %T: the stop's create %+v; want it only while unbound", c.name, b, s[0].Create)
			}
			if s[0].Engine != c.th.Remote.Engine || s[0].Link != c.th.Remote.Link || s[0].Command.Target.IsZero() {
				t.Fatalf("%s %T: the stop goes to %+v", c.name, b, s[0])
			}
			if _, deleted := b.(api.ThreadDelete); deleted && api.EventType(evs[len(evs)-1].Body) != api.EventType(api.ThreadDeleted{}) {
				t.Fatalf("%s: a delete's events %v; want the tombstone", c.name, evs)
			}
		}
	}
	settledOnce := tried
	evs, _, _ := decide(t, tried, settle, nil)
	for _, e := range evs {
		settledOnce = engine.Apply(settledOnce, e)
	}
	if _, effs, err := decide(t, settledOnce, settle, nil); err != nil || len(effs) != 0 {
		t.Fatalf("settled again: %v %v; want nothing sent", effs, err)
	}
	if _, _, err := decide(t, settledOnce, api.InboxSet{}, nil); err == nil {
		t.Fatal("a placeholder settled before its machine took it came back to the inbox; want it refused")
	}
}

// A stop, a message and a narrowing reach the child there, and nothing of
// them is queued or run here.
func TestAStopAMessageAndANarrowingReachTheChildThere(t *testing.T) {
	th := placeholder(t)
	th.Access = api.AccessAutoEdits
	_, effs, err := decide(t, th, api.TurnInterrupt{Tree: true}, nil)
	if s := stops(effs); err != nil || len(s) != 1 || s[0].Create != nil {
		t.Fatalf("a stop: %v %v; want one stop", effs, err)
	} else if b, ok := s[0].Command.Body.(api.TurnInterrupt); !ok || !b.Tree {
		t.Fatalf("a stop sends %+v; want its tree stopped there", s[0].Command.Body)
	}
	evs, effs, err := decide(t, th, api.MessageDispatch{Text: "more", Mode: api.StartNow}, nil)
	if err != nil || len(evs) != 0 || len(effs) != 1 {
		t.Fatalf("a message: %v %v %v; want one send and nothing here", evs, effs, err)
	}
	if s, ok := effs[0].(engine.RemoteSend); !ok || s.Command.Body.(api.MessageDispatch).Text != "more" || s.Command.Body.(api.MessageDispatch).Mode != api.AfterActive {
		t.Fatalf("a message sends %+v; want it after the run there", effs[0])
	}
	if _, _, err := decide(t, th, api.MessageDispatch{Text: "x", Mode: api.Defer}, nil); err == nil {
		t.Fatal("a held message to a placeholder was taken")
	}
	read := api.AccessAsk
	_, effs, err = decide(t, th, api.ThreadConfigure{Access: &read}, nil)
	if err != nil || len(effs) != 1 {
		t.Fatalf("a narrowing: %v %v; want one configure", effs, err)
	}
	if c, ok := effs[0].(engine.RemoteConfigure); !ok || *c.Command.Body.(api.ThreadConfigure).Access != api.AccessAsk {
		t.Fatalf("a narrowing sends %+v", effs[0])
	}
	same := api.AccessAutoEdits
	if _, effs, err := decide(t, th, api.ThreadConfigure{Access: &same}, nil); err != nil || len(effs) != 0 {
		t.Fatalf("an access the child has: %v %v; want nothing sent", effs, err)
	}
	if _, _, err := decide(t, th, api.RequestRespond{Request: api.NewID()}, nil); err == nil {
		t.Fatal("an answer for a remote child was taken here")
	}
	for _, b := range []api.Intent{api.ThreadDelete{}, settle, api.TurnInterrupt{}, api.MessageDispatch{Text: "x", Mode: api.AfterActive}} {
		_, effs, _ := decide(t, th, b, nil)
		for _, e := range effs {
			if _, ok := e.(engine.StartTurn); ok {
				t.Fatalf("%T on a placeholder starts a run here", b)
			}
		}
	}
}

// Two placeholders one command takes send two commands there.
func TestOneCommandSendsEachPlaceholderItsOwn(t *testing.T) {
	a, b := placeholder(t), placeholder(t)
	id := api.NewID()
	var ids []api.ID
	for _, th := range []engine.Thread{a, b} {
		_, effs, err := engine.Decide(th, api.Command{ID: id, Target: th.ID, Body: settle}, time.Now())
		if err != nil || len(stops(effs)) != 1 {
			t.Fatal(effs, err)
		}
		ids = append(ids, stops(effs)[0].Command.ID)
	}
	if ids[0] == ids[1] {
		t.Fatal("two placeholders' stops share one command id there")
	}
}

// An agent's message or stop is refused once the person on the child's
// machine took it over there, and a lost child takes no message but is
// still stopped.
func TestTheMachinesPersonTakesTheChildOver(t *testing.T) {
	th := placeholder(t)
	caller := &api.Caller{Thread: th.Parent, Run: api.NewID()}
	if _, effs, err := decide(t, th, api.MessageDispatch{Text: "x", Mode: api.AfterActive}, caller); err != nil || len(effs) != 1 {
		t.Fatalf("an agent's message before: %v %v", effs, err)
	}
	r := *th.Remote
	r.Taken = true
	th.Remote = &r
	if _, _, err := decide(t, th, api.MessageDispatch{Text: "x", Mode: api.AfterActive}, caller); err == nil {
		t.Fatal("an agent's message to a child its machine's person took over was taken")
	}
	if _, _, err := decide(t, th, api.TurnInterrupt{}, caller); err == nil {
		t.Fatal("an agent's stop of a child its machine's person took over was taken")
	}
	if _, effs, err := decide(t, th, api.MessageDispatch{Text: "x", Mode: api.AfterActive}, nil); err != nil || len(effs) != 1 {
		t.Fatalf("this machine's person's message: %v %v; want it sent", effs, err)
	}
	lost := engine.Apply(placeholder(t), engine.Event{At: time.Now(), Body: api.RemoteLost{Reason: "gone"}})
	if _, _, err := decide(t, lost, api.MessageDispatch{Text: "x", Mode: api.AfterActive}, nil); err == nil {
		t.Fatal("a lost child took a message")
	}
	if _, effs, err := decide(t, lost, api.TurnInterrupt{}, nil); err != nil || len(stops(effs)) != 1 {
		t.Fatalf("a lost child's stop: %v %v; want it still sent", effs, err)
	}
}

// A stop is due on a deleted or lost placeholder; a message or a narrowing
// is not.
func TestRemoteEffectsDue(t *testing.T) {
	th := placeholder(t)
	deleted := engine.Apply(th, engine.Event{At: time.Now(), Body: api.ThreadDeleted{}})
	lost := engine.Apply(th, engine.Event{At: time.Now(), Body: api.RemoteLost{Reason: "gone"}})
	for name, c := range map[string]struct {
		th   engine.Thread
		e    engine.Effect
		want bool
	}{
		"stop deleted":   {deleted, engine.RemoteStop{}, true},
		"stop lost":      {lost, engine.RemoteStop{}, true},
		"send bound":     {th, engine.RemoteSend{}, true},
		"send deleted":   {deleted, engine.RemoteSend{}, false},
		"send lost":      {lost, engine.RemoteSend{}, false},
		"narrow deleted": {deleted, engine.RemoteConfigure{}, false},
	} {
		if due, _ := engine.Due(c.th, c.e); due != c.want {
			t.Errorf("%s: due %v; want %v", name, due, c.want)
		}
		if !engine.ReplaySafe(c.e) {
			t.Errorf("%s: %s isn't replay-safe", name, engine.EffectKind(c.e))
		}
	}
}

// The last message or stop on the child's machine that no agent's thread
// sent is its person's: they took it over there.
func TestFollowSeesTheMachinesPersonTakeOver(t *testing.T) {
	origin := api.NewID()
	r := engine.Remote{State: engine.RemoteBoundState}
	steps := []struct {
		b     api.EventBody
		taken bool
	}{
		{api.MessageQueued{Message: api.NewID(), Text: "go", By: origin}, false},
		{api.MessageQueued{Message: api.NewID(), Text: "report", Report: api.NewID()}, false},
		{api.StopAsked{Run: api.NewID(), By: origin}, false},
		{api.MessageQueued{Message: api.NewID(), Text: "mine"}, true},
		{api.MessageQueued{Message: api.NewID(), Text: "compact", Silent: true}, true},
		{api.StopAsked{Run: api.NewID()}, true},
	}
	for i, s := range steps {
		var err error
		if r, _, _, err = engine.Follow(r, []api.Event{on(t, uint64(i+1), s.b)}, false); err != nil {
			t.Fatal(err)
		}
		if r.Taken != s.taken {
			t.Fatalf("after %s %d: taken %v; want %v", api.EventType(s.b), i, r.Taken, s.taken)
		}
	}
}

// A tree's stop reaches a placeholder in it, which stops its child's tree
// there.
func TestATreesStopTakesAPlaceholder(t *testing.T) {
	if b, ok := engine.Below(api.TurnInterrupt{Tree: true}, placeholder(t)).(api.TurnInterrupt); !ok || !b.Tree {
		t.Fatalf("below a tree's stop, a placeholder gets %+v", b)
	}
}
