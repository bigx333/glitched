package client

import (
	"testing"

	"glitched/internal/api"
)

// A fork's transcript starts with where it goes on from, and its first
// session.bound that says how it took the conversation up says so on it;
// one that says nothing leaves it as it was.
func TestAForksTranscriptStartsWithItsSource(t *testing.T) {
	forked := api.Forked{Thread: api.NewID(), Run: api.NewID(), Title: "the source", Provider: "claude", Runs: []api.ID{}}
	run := api.NewID()
	tr := play(api.ThreadCreated{Title: "the source fork", Fork: &forked}, api.SessionBound{Run: run, Session: "s"})
	f, ok := tr.Entries[0].(*ForkEntry)
	if len(tr.Entries) != 1 || !ok || f.Thread != forked.Thread || f.Run != forked.Run || f.Took != "" || !f.At.Equal(at(1)) {
		t.Fatalf("entries %+v; want the fork's source alone", tr.Entries)
	}
	tr.apply(event(3, api.SessionBound{Run: run, Session: "s", Fork: api.ForkNative}))
	if f.Took != api.ForkNative {
		t.Fatalf("took %q once the run said it took it up natively", f.Took)
	}
	if plain := play(api.ThreadCreated{Title: "plain"}); len(plain.Entries) != 0 {
		t.Fatalf("a thread that isn't a fork starts with %+v", plain.Entries)
	}
}
