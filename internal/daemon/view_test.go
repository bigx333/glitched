package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider/fake"
)

// A placeholder's view says which engine and link its child runs through,
// its thread and its last run there, which the GUI opens it by and
// matches its reports to; the thread there says which thread started it,
// through which link.
func TestAViewSaysWhereARemoteChildRunsAndWhereItCameFrom(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Turn(false)), "fake", engine.Limits{})
	kid := p.place("there")
	r := p.bound(kid)
	snap, err := p.a.Endpoint().Read(context.Background(), api.Query{Thread: kid})
	if err != nil {
		t.Fatal(err)
	}
	if v := snap.Threads[0].Remote; v == nil || v.Engine != p.engineB || v.Link != p.link.ID || v.Thread != r.Thread || v.Run != r.Run || v.Machine != "b" {
		t.Fatalf("the placeholder's view places it at %+v; want b's engine %v, link %v and thread %v", v, p.engineB, p.link.ID, r.Thread)
	}
	there := p.there()
	if len(there) != 1 {
		t.Fatalf("b's threads from the link: %+v", there)
	}
	if o := there[0].Origin; o == nil || o.Thread != p.parent || o.Title != "parent on a" || o.Link != p.link.ID || o.Engine.IsZero() {
		t.Fatalf("b's thread came from %+v; want a's parent, through the link labelled a", o)
	}
}

// The status the GUI reads holds no token's hash, which reaches the
// engine as the token does, and an unreadable machines.json takes only
// the machines from it.
func TestStatusHoldsNoHashAndOutlivesABrokenMachineList(t *testing.T) {
	dir := stateDir(t)
	allowChildren(t, dir, api.AccessAutoEdits)
	if _, err := Pair(dir, "window"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PairLink(dir, "a", api.Link{Access: api.AccessAutoEdits}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, machinesFile), []byte("[{"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Inspect(context.Background(), dir, func(context.Context) (api.Tailnet, error) { return api.Tailnet{}, nil })
	if err != nil {
		t.Fatalf("inspect with a broken machines.json: %v", err)
	}
	if len(st.Tokens) != 2 || st.MachinesErr == nil {
		t.Fatalf("status holds %d tokens and machines error %v; want both tokens and why the machines can't be read", len(st.Tokens), st.MachinesErr)
	}
	for _, tok := range st.Tokens {
		if tok.hash() != "" {
			t.Errorf("status token %q keeps its hash", tok.Label)
		}
	}
	if ts, _ := Tokens(dir); len(ts) != 2 || ts[0].hash() == "" || ts[1].hash() == "" {
		t.Errorf("tokens.json lost its hashes: %+v", ts)
	}
}
