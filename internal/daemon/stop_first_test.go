package daemon

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
)

// B's person can stop a link's thread before its first run: its first
// message waits on the worktree, Stop is offered on it, and a stop holds
// the message, kept, for a Resume that runs it.
func TestBsPersonStopsALinksThreadBeforeItsFirstRun(t *testing.T) {
	l := newLinked(t, api.Link{Access: api.AccessAutoEdits}, true)
	thread := l.mustCreate(api.AccessAsk)
	l.making(thread)
	if v := l.view(thread); v.Live() || v.Queued != 1 || !v.Stoppable() {
		t.Fatalf("waiting on its worktree the thread is %v, queues %d and offers Stop %v; want its first message waiting with Stop offered", v.Run, v.Queued, v.Stoppable())
	}
	if _, err := l.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: api.TurnInterrupt{}}); err != nil {
		t.Fatal(err)
	}
	l.open()
	l.waitFor(thread, "worktree.ready", 1)
	time.Sleep(200 * time.Millisecond)
	if n := l.count(thread, "turn.admitted"); n != 0 {
		t.Fatalf("%d turns admitted after B's person stopped the thread before its first run", n)
	}
	if v := l.view(thread); v.Queued != 1 || !v.Resumable || v.Stoppable() {
		t.Fatalf("after the stop the thread queues %d, resumable %v, Stop %v; want its message kept, held for Resume, with no Stop", v.Queued, v.Resumable, v.Stoppable())
	}
	if _, err := l.b.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: api.QueueResume{}}); err != nil {
		t.Fatal(err)
	}
	l.waitFor(thread, "turn.ended", 1)
}
