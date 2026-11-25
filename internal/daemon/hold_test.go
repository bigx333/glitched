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
