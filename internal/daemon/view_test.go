package daemon

import (
	"context"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider/fake"
)

// A placeholder's view says which engine and link its child runs through
// and its thread there, which the GUI opens it by; the thread there says
// which thread on which machine started it, by the link's label there.
func TestAViewSaysWhereARemoteChildRunsAndWhereItCameFrom(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	kid := p.place("there")
	r := p.bound(kid)
	snap, err := p.a.Endpoint().Read(context.Background(), api.Query{Thread: kid})
	if err != nil {
		t.Fatal(err)
	}
	if v := snap.Threads[0].Remote; v == nil || v.Engine != p.engineB || v.Link != p.link.ID || v.Thread != r.Thread || v.Machine != "b" {
		t.Fatalf("the placeholder's view places it at %+v; want b's engine %v, link %v and thread %v", v, p.engineB, p.link.ID, r.Thread)
	}
	there := p.there()
	if len(there) != 1 {
		t.Fatalf("b's threads from the link: %+v", there)
	}
	if o := there[0].Origin; o == nil || o.Thread != p.parent || o.Title != "parent on a" || o.Link != p.link.ID || o.Machine != "a" || o.Engine.IsZero() {
		t.Fatalf("b's thread came from %+v; want a's parent, through the link labelled a", o)
	}
}
