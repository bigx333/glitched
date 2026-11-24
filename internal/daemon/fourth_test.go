package daemon

import (
	"context"
	"errors"
	"glitched/internal/api"
	"glitched/internal/engine"
	"strings"
	"testing"
)

func TestFourthInitialStopAttribution(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, true)
	k := l.mustCreate(api.AccessAutoEdits)
	l.making(k)
	ctx := context.Background()
	if _, err := l.b.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: k, Body: api.TurnInterrupt{}}); err != nil {
		t.Fatal(err)
	}
	l.open()
	l.waitFor(k, "worktree.ready", 1)
	l.close()
	l.startB()
	s, err := l.b.Endpoint().Read(ctx, api.Query{Thread: k})
	if err != nil {
		t.Fatal(err)
	}
	r, _, _, err := engine.Follow(engine.Remote{Thread: k}, s.Events, true)
	if err != nil {
		t.Fatal(err)
	}
	if n := l.count(k, "turn.admitted"); n != 0 || l.view(k).Queued != 1 {
		t.Fatal("stop did not hold first message")
	}
	if !r.Taken {
		t.Fatalf("B's person stopped the first queued message; after restart queued=1, stop.asked=%d, follower Taken=false", l.count(k, "stop.asked"))
	}
	var rej *api.RejectedError
	if err := l.send(k, api.MessageDispatch{Text: "the link steers again", Mode: api.AfterActive}); !errors.As(err, &rej) || !strings.Contains(rej.Reason, "took this thread over") {
		t.Fatalf("after B's person's stop before the first run and a restart, the link's message: %v; want refused as their takeover", err)
	}
}

func TestFourthFailedGrantLeavesOriginalOpen(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAsk}, false)
	ctx := context.Background()
	c := l.create(api.AccessAsk, 1)
	if _, err := l.ep.Submit(ctx, c); err != nil {
		t.Fatal(err)
	}
	l.waitFor(c.Target, "turn.ended", 1)
	l.b.store.SetLinks(func(api.ID) (api.Link, error) { return api.Link{}, errors.New("grant unavailable") })
	_, err := l.ep.Submit(ctx, cleanupFor(c))
	if err == nil || l.view(c.Target).Disposition.State != api.Open {
		t.Fatalf("failed grant should refuse cleanup without changing original: %v", err)
	}
}
